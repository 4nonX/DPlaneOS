package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Shared DB path for settings access
var alertDBPath = "/var/lib/dplaneos/dplaneos.db"

// ═══════════════════════════════════════════════════════════════
//  SMTP EMAIL ALERTING
// ═══════════════════════════════════════════════════════════════

type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"` // comma-separated
	TLS      bool   `json:"tls"`
}

// GetSMTPConfig returns current SMTP configuration
// GET /api/alerts/smtp
func GetSMTPConfig(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("sqlite3", alertDBPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "configured": false})
		return
	}
	defer db.Close()

	var value string
	err = db.QueryRow("SELECT value FROM settings WHERE key = ?", "smtp_config").Scan(&value)
	if err != nil || value == "" {
		respondOK(w, map[string]interface{}{"success": true, "configured": false})
		return
	}
	var cfg SMTPConfig
	if json.Unmarshal([]byte(value), &cfg) != nil {
		respondOK(w, map[string]interface{}{"success": true, "configured": false})
		return
	}
	cfg.Password = "***" // never expose
	respondOK(w, map[string]interface{}{"success": true, "configured": true, "config": cfg})
}

// SaveSMTPConfig saves SMTP settings
// POST /api/alerts/smtp
func SaveSMTPConfig(w http.ResponseWriter, r *http.Request) {
	var cfg SMTPConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if cfg.Host == "" || cfg.Port == 0 || cfg.From == "" || cfg.To == "" {
		respondErrorSimple(w, "Host, port, from, and to are required", http.StatusBadRequest)
		return
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		respondErrorSimple(w, "Failed to encode config", http.StatusInternalServerError)
		return
	}

	db, err := sql.Open("sqlite3", alertDBPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	if err != nil {
		respondErrorSimple(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()
	_, err = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", "smtp_config", string(data))
	if err != nil {
		respondErrorSimple(w, "Failed to save", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true})
}

// TestSMTP sends a test email
// POST /api/alerts/smtp/test
func TestSMTP(w http.ResponseWriter, r *http.Request) {
	var cfg SMTPConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: D-PlaneOS Test Alert\r\n\r\nThis is a test email from D-PlaneOS at %s.\r\nIf you received this, SMTP alerting is working correctly.\r\n",
		cfg.From, cfg.To, time.Now().Format(time.RFC3339))

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	err := smtp.SendMail(addr, auth, cfg.From, strings.Split(cfg.To, ","), []byte(msg))
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "message": "Test email sent to " + cfg.To})
}

// SendSMTPAlert sends an alert email (called internally by other handlers)
func SendSMTPAlert(subject, body string) {
	db, err := sql.Open("sqlite3", alertDBPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	if err != nil {
		return
	}
	defer db.Close()

	var value string
	err = db.QueryRow("SELECT value FROM settings WHERE key = ?", "smtp_config").Scan(&value)
	if err != nil || value == "" {
		return // SMTP not configured
	}
	var cfg SMTPConfig
	if json.Unmarshal([]byte(value), &cfg) != nil {
		return
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: [D-PlaneOS] %s\r\n\r\n%s\r\n",
		cfg.From, cfg.To, subject, body)
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	go func() {
		if err := smtp.SendMail(addr, auth, cfg.From, strings.Split(cfg.To, ","), []byte(msg)); err != nil {
			log.Printf("SMTP ALERT ERROR: %v", err)
		}
	}()
}

// ═══════════════════════════════════════════════════════════════
//  ZFS SCRUB CRON SCHEDULER
// ═══════════════════════════════════════════════════════════════

type ScrubSchedule struct {
	Pool     string `json:"pool"`
	Interval string `json:"interval"` // daily, weekly, monthly
	Day      int    `json:"day"`      // 0=Sunday for weekly, 1-28 for monthly
	Hour     int    `json:"hour"`     // 0-23
}

// GetScrubSchedules returns configured scrub schedules
// GET /api/zfs/scrub/schedule
func GetScrubSchedules(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("sqlite3", alertDBPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "schedules": []ScrubSchedule{}})
		return
	}
	defer db.Close()

	var value string
	err = db.QueryRow("SELECT value FROM settings WHERE key = ?", "scrub_schedules").Scan(&value)
	if err != nil || value == "" {
		respondOK(w, map[string]interface{}{"success": true, "schedules": []ScrubSchedule{}})
		return
	}
	var schedules []ScrubSchedule
	json.Unmarshal([]byte(value), &schedules)
	respondOK(w, map[string]interface{}{"success": true, "schedules": schedules})
}

// SaveScrubSchedules saves and installs scrub cron jobs
// POST /api/zfs/scrub/schedule
func SaveScrubSchedules(w http.ResponseWriter, r *http.Request) {
	var schedules []ScrubSchedule
	if err := json.NewDecoder(r.Body).Decode(&schedules); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate
	for _, s := range schedules {
		if !isValidDataset(s.Pool) {
			respondErrorSimple(w, "Invalid pool name: "+s.Pool, http.StatusBadRequest)
			return
		}
		validIntervals := map[string]bool{"daily": true, "weekly": true, "monthly": true}
		if !validIntervals[s.Interval] {
			respondErrorSimple(w, "Invalid interval (daily/weekly/monthly)", http.StatusBadRequest)
			return
		}
	}

	// Save to DB (prepared statement — safe against SQL injection)
	data, err := json.Marshal(schedules)
	if err != nil {
		respondErrorSimple(w, "Failed to encode schedules", http.StatusInternalServerError)
		return
	}
	db, err := sql.Open("sqlite3", alertDBPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	if err == nil {
		defer db.Close()
		db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", "scrub_schedules", string(data))
	}

	// Generate crontab entries
	var crontab strings.Builder
	crontab.WriteString("# D-PlaneOS ZFS Scrub Schedules (auto-generated)\n")
	for _, s := range schedules {
		var cronExpr string
		switch s.Interval {
		case "daily":
			cronExpr = fmt.Sprintf("%d %d * * *", 0, s.Hour)
		case "weekly":
			cronExpr = fmt.Sprintf("%d %d * * %d", 0, s.Hour, s.Day)
		case "monthly":
			day := s.Day
			if day < 1 {
				day = 1
			}
			cronExpr = fmt.Sprintf("%d %d %d * *", 0, s.Hour, day)
		}
		crontab.WriteString(fmt.Sprintf("%s /usr/sbin/zpool scrub %s\n", cronExpr, s.Pool))
	}

	// Write to crontab file and install
	cronFile := "/etc/cron.d/dplaneos-scrub"
	executeCommandWithTimeout(TimeoutFast, "/bin/bash", []string{
		"-c", fmt.Sprintf("echo '%s' > %s && chmod 644 %s", crontab.String(), cronFile, cronFile),
	})

	respondOK(w, map[string]interface{}{
		"success":   true,
		"schedules": schedules,
		"cron_file": cronFile,
	})
}

// StartScrubMonitor runs a background goroutine checking scrub schedules
func StartScrubMonitor() {
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			// Check all pools for scrub age
			output, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zpool", []string{"list", "-H", "-o", "name"})
			if err != nil {
				continue
			}
			for _, pool := range strings.Split(strings.TrimSpace(output), "\n") {
				pool = strings.TrimSpace(pool)
				if pool == "" {
					continue
				}
				status, _ := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{"status", pool})
				if strings.Contains(status, "none requested") {
					// No scrub ever run — alert
					SendSMTPAlert(
						"ZFS Scrub Warning: "+pool,
						fmt.Sprintf("Pool '%s' has never been scrubbed. Schedule regular scrubs to protect your data.", pool),
					)
				}
			}
		}
	}()
}
