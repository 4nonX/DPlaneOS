package handlers

import (
	"context"
	"database/sql"
	"dplaned/internal/cmdutil"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type SystemStatusHandler struct {
	db        *sql.DB
	startTime time.Time
	version   string
}

func NewSystemStatusHandler(db *sql.DB, version string) *SystemStatusHandler {
	return &SystemStatusHandler{db: db, startTime: time.Now(), version: version}
}

func (h *SystemStatusHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var setupDone int
	h.db.QueryRow(`SELECT COUNT(*) FROM system_config WHERE key = 'setup_complete' AND value = '1'`).Scan(&setupDone)
	var userCount int
	h.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&userCount)

	poolOutput, err := cmdutil.RunFast("zpool", "list", "-H", "-o", "name")
	if err != nil {
		log.Printf("WARN: zpool list: %v", err)
	}
	pools := strings.Split(strings.TrimSpace(string(poolOutput)), "\n")
	poolCount := 0
	for _, p := range pools {
		if p != "" { poolCount++ }
	}

	// ECC RAM detection — non-blocking, advisory only
	ecc := detectECCRAM()

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "version": h.version, "setup_complete": setupDone > 0,
		"has_users": userCount > 0, "has_pools": poolCount > 0,
		"first_run": setupDone == 0 && userCount <= 1,
		"uptime_seconds": int(time.Since(h.startTime).Seconds()),
		"ecc_ram":         ecc.HasECC,
		"ecc_known":       ecc.Known,
		"ecc_virtual":     ecc.IsVirtual,
		"ecc_warning":     !ecc.HasECC && ecc.Known && !ecc.IsVirtual,
		"ecc_warning_msg": ecc.Warning,
	})
}

func (h *SystemStatusHandler) HandleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.db.Exec(`CREATE TABLE IF NOT EXISTS system_config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	var body struct { Hostname string `json:"hostname"`; Timezone string `json:"timezone"` }
	json.NewDecoder(r.Body).Decode(&body)
	h.db.Exec(`INSERT OR REPLACE INTO system_config (key, value) VALUES ('setup_complete', '1')`)
	if body.Hostname != "" {
		h.db.Exec(`INSERT OR REPLACE INTO system_config (key, value) VALUES ('hostname', ?)`, body.Hostname)
		if _, err := cmdutil.RunFast("hostnamectl", "set-hostname", body.Hostname); err != nil {
			log.Printf("WARN: hostnamectl: %v", err)
		}
		// Persist to Nix fragment (NixOS: networking.hostName)
		persistHostname(body.Hostname)
	}
	if body.Timezone != "" {
		h.db.Exec(`INSERT OR REPLACE INTO system_config (key, value) VALUES ('timezone', ?)`, body.Timezone)
		if _, err := cmdutil.RunFast("timedatectl", "set-timezone", body.Timezone); err != nil {
			log.Printf("WARN: timedatectl: %v", err)
		}
		// Persist to Nix fragment (NixOS: time.timeZone)
		persistTimezone(body.Timezone)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "Setup completed"})
}

func (h *SystemStatusHandler) HandleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hostname, err := os.Hostname()
	if err != nil { log.Printf("WARN: os.Hostname: %v", err); hostname = "unknown" }
	var storedHostname, timezone, description string
	h.db.QueryRow(`SELECT COALESCE(value,'') FROM system_config WHERE key = 'hostname'`).Scan(&storedHostname)
	h.db.QueryRow(`SELECT COALESCE(value,'') FROM system_config WHERE key = 'timezone'`).Scan(&timezone)
	h.db.QueryRow(`SELECT COALESCE(value,'') FROM system_config WHERE key = 'description'`).Scan(&description)
	if storedHostname != "" { hostname = storedHostname }
	if timezone == "" {
		if tzBytes, err := cmdutil.RunFast("timedatectl", "show", "--property=Timezone", "--value"); err != nil {
			log.Printf("WARN: timedatectl: %v", err)
		} else { timezone = strings.TrimSpace(string(tzBytes)) }
	}
	kernel, err := cmdutil.RunFast("uname", "-r")
	if err != nil { log.Printf("WARN: uname: %v", err) }
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "profile": map[string]interface{}{
		"hostname": hostname, "timezone": timezone, "description": description,
		"version": h.version, "kernel": strings.TrimSpace(string(kernel)),
		"arch": runtime.GOARCH, "os": runtime.GOOS, "uptime": int(time.Since(h.startTime).Seconds()),
	}})
}

func (h *SystemStatusHandler) HandlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type check struct { Name string `json:"name"`; Status string `json:"status"`; Message string `json:"message"` }
	var checks []check
	if _, err := cmdutil.RunFast("which", "zpool"); err != nil {
		checks = append(checks, check{"ZFS", "fail", "ZFS tools not installed"})
	} else { checks = append(checks, check{"ZFS", "pass", "ZFS available"}) }
	if _, err := cmdutil.RunFast("which", "docker"); err != nil {
		checks = append(checks, check{"Docker", "warn", "Docker not installed"})
	} else { checks = append(checks, check{"Docker", "pass", "Docker available"}) }
	if _, err := cmdutil.RunFast("which", "smbd"); err != nil {
		checks = append(checks, check{"Samba", "warn", "Samba not installed"})
	} else { checks = append(checks, check{"Samba", "pass", "Samba available"}) }
	if _, err := cmdutil.RunFast("which", "exportfs"); err != nil {
		checks = append(checks, check{"NFS", "warn", "NFS not installed"})
	} else { checks = append(checks, check{"NFS", "pass", "NFS available"}) }
	dfOut, err := cmdutil.RunFast("df", "-h", "/"); diskMsg := "Unable to check"
	if err != nil { log.Printf("WARN: df: %v", err) } else {
		lines := strings.Split(string(dfOut), "\n"); if len(lines) > 1 { diskMsg = strings.TrimSpace(lines[1]) }
	}
	checks = append(checks, check{"Root Disk", "pass", diskMsg})
	memOut, err := cmdutil.RunFast("free", "-h"); memMsg := "Unable to check"
	if err != nil { log.Printf("WARN: free: %v", err) } else {
		lines := strings.Split(string(memOut), "\n"); if len(lines) > 1 { memMsg = strings.TrimSpace(lines[1]) }
	}
	checks = append(checks, check{"Memory", "pass", memMsg})
	overall := "pass"
	for _, c := range checks { if c.Status == "fail" { overall = "fail"; break }; if c.Status == "warn" { overall = "warn" } }
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "status": overall, "checks": checks})
}

func (h *SystemStatusHandler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	h.db.Exec(`CREATE TABLE IF NOT EXISTS system_config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query(`SELECT key, value FROM system_config ORDER BY key`)
		if err != nil { respondErrorSimple(w, "Failed to read settings", http.StatusInternalServerError); return }
		defer rows.Close()
		settings := map[string]string{}
		for rows.Next() { var k, v string; rows.Scan(&k, &v); settings[k] = v }
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "settings": settings})
	case http.MethodPost:
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { respondErrorSimple(w, "Invalid request", http.StatusBadRequest); return }
		for k, v := range body { h.db.Exec(`INSERT OR REPLACE INTO system_config (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)`, k, v) }
		// Persist system-level changes to Nix fragment so they survive nixos-rebuild
		if hn, ok := body["hostname"]; ok && hn != "" {
			if _, err := cmdutil.RunFast("hostnamectl", "set-hostname", hn); err != nil {
				log.Printf("WARN: hostnamectl runtime: %v", err)
			}
			persistHostname(hn)
		}
		if tz, ok := body["timezone"]; ok && tz != "" {
			if _, err := cmdutil.RunFast("timedatectl", "set-timezone", tz); err != nil {
				log.Printf("WARN: timedatectl runtime: %v", err)
			}
			persistTimezone(tz)
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": fmt.Sprintf("%d settings saved", len(body))})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ECCStatus holds the result of ECC detection including reliability info.
type ECCStatus struct {
	HasECC      bool   // true = ECC detected
	Known       bool   // false = detection unreliable (VM or dmidecode unavailable)
	IsVirtual   bool   // true = running inside a VM
	Warning     string // human-readable advisory
}

// detectECCRAM checks dmidecode for ECC memory presence.
// In virtual machines, dmidecode often returns no or incorrect memory type info,
// so we detect the VM context and adjust the warning accordingly.
func detectECCRAM() ECCStatus {
	// Detect virtualisation: check /sys/class/dmi/id/product_name and /proc/cpuinfo
	virtual := isVirtualMachine()

	out, err := cmdutil.RunFast("dmidecode", "-t", "memory")
	if err != nil {
		warning := "ECC status unknown (dmidecode not available)"
		if virtual {
			warning = "ECC detection unreliable in virtual machines — check host hardware directly"
		}
		return ECCStatus{HasECC: false, Known: false, IsVirtual: virtual, Warning: warning}
	}

	s := string(out)

	// In VMs, dmidecode may return empty memory tables or "Unknown" types
	if virtual || strings.Count(s, "Memory Device") < 2 {
		return ECCStatus{
			HasECC:    false,
			Known:     false,
			IsVirtual: virtual,
			Warning:   "ECC detection unreliable in virtual/container environments. Check host hardware directly.",
		}
	}

	hasECC := strings.Contains(s, "Error Correction Type:") &&
		!strings.Contains(s, "Error Correction Type: None") &&
		!strings.Contains(s, "Error Correction Type: Unknown")

	warning := ""
	if !hasECC {
		warning = "Non-ECC RAM detected. ZFS cannot protect against in-memory bit flips. ECC RAM is recommended for data integrity."
	}
	return ECCStatus{HasECC: hasECC, Known: true, IsVirtual: false, Warning: warning}
}

// isVirtualMachine checks common indicators of VM/container environments.
func isVirtualMachine() bool {
	// Check DMI product name
	if data, err := os.ReadFile("/sys/class/dmi/id/product_name"); err == nil {
		p := strings.ToLower(strings.TrimSpace(string(data)))
		for _, vm := range []string{"vmware", "virtualbox", "kvm", "qemu", "xen", "hyper-v", "proxmox", "lxc", "container"} {
			if strings.Contains(p, vm) {
				return true
			}
		}
	}
	// Check hypervisor bit in /proc/cpuinfo
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		if strings.Contains(string(data), "hypervisor") {
			return true
		}
	}
	return false
}

// HandleZFSGateStatus reports whether the ZFS mount gate marker is present.
// The marker is written by zfs-mount-wait.sh once all pools are online + writable.
// GET /api/system/zfs-gate-status
func (h *SystemStatusHandler) HandleZFSGateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const markerPath = "/run/dplaneos/zfs-ready"
	_, err := os.Stat(markerPath)
	gateReady := err == nil
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"gate_ready": gateReady,
		"marker":     markerPath,
	})
}

// ═══════════════════════════════════════════════════════════════
//  IPMI / BMC SENSOR DATA  (ipmitool sdr)
// ═══════════════════════════════════════════════════════════════

// IPMISensor represents one sensor reading from ipmitool.
type IPMISensor struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Unit   string `json:"unit"`
	Status string `json:"status"` // "ok", "warn", "critical", "ns" (not supported)
}

// HandleIPMISensors returns BMC sensor readings via ipmitool sdr.
// Returns 200 with available=false if ipmitool is not installed or no BMC is found.
// GET /api/system/ipmi
func (h *SystemStatusHandler) HandleIPMISensors(w http.ResponseWriter, r *http.Request) {
	// Check ipmitool availability
	ipmitoolPath := "/usr/bin/ipmitool"
	if _, err := os.Stat(ipmitoolPath); os.IsNotExist(err) {
		// Try alternate location
		ipmitoolPath = "/usr/sbin/ipmitool"
		if _, err2 := os.Stat(ipmitoolPath); os.IsNotExist(err2) {
			respondJSON(w, http.StatusOK, map[string]interface{}{
				"available": false,
				"reason":    "ipmitool not installed",
				"sensors":   []IPMISensor{},
			})
			return
		}
	}

	// Run: ipmitool sdr -c (compact/parseable CSV output)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ipmitoolPath, "sdr")
	out, err := cmd.Output()
	if err != nil {
		// BMC not accessible (e.g. consumer hardware, VM)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"available": false,
			"reason":    "BMC not accessible: " + err.Error(),
			"sensors":   []IPMISensor{},
		})
		return
	}

	sensors := parseIPMISdr(string(out))
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"available": true,
		"sensors":   sensors,
	})
}

// parseIPMISdr parses `ipmitool sdr` output (one sensor per line).
// Format: Name             | Value      | Status
func parseIPMISdr(output string) []IPMISensor {
	var sensors []IPMISensor
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		raw := strings.TrimSpace(parts[1])
		status := strings.ToLower(strings.TrimSpace(parts[2]))

		// Parse value and unit from raw (e.g. "3.30 Volts", "45 degrees C", "no reading")
		value, unit := splitValueUnit(raw)

		normalStatus := "ok"
		switch {
		case strings.Contains(status, "ok") || strings.Contains(status, "present"):
			normalStatus = "ok"
		case strings.Contains(status, "warn") || strings.Contains(status, "upper non"):
			normalStatus = "warn"
		case strings.Contains(status, "crit") || strings.Contains(status, "upper crit") || strings.Contains(status, "lower crit"):
			normalStatus = "critical"
		case strings.Contains(status, "no reading") || strings.Contains(status, "ns") || strings.Contains(status, "not present"):
			normalStatus = "ns"
		default:
			normalStatus = status
		}

		sensors = append(sensors, IPMISensor{
			Name:   name,
			Value:  value,
			Unit:   unit,
			Status: normalStatus,
		})
	}
	if sensors == nil {
		sensors = []IPMISensor{}
	}
	return sensors
}

// splitValueUnit splits "3.30 Volts" into ("3.30", "Volts"),
// "no reading" into ("no reading", ""), etc.
func splitValueUnit(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "no reading" || raw == "disabled" {
		return raw, ""
	}
	idx := strings.LastIndex(raw, " ")
	if idx < 0 {
		return raw, ""
	}
	value := strings.TrimSpace(raw[:idx])
	unit := strings.TrimSpace(raw[idx+1:])
	// Sanity: if "unit" looks numeric, it's all one value
	if len(unit) > 0 && (unit[0] >= '0' && unit[0] <= '9') {
		return raw, ""
	}
	return value, unit
}

// HandleSetupAdmin — POST /api/system/setup-admin
// Sets the admin password during initial setup. Only works before setup is marked complete.
// This endpoint is public (no session required) but is gated by setup_complete flag.
func (h *SystemStatusHandler) HandleSetupAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Gate: only allow if setup is NOT yet complete
	var setupDone int
	h.db.QueryRow(`SELECT COUNT(*) FROM system_config WHERE key = 'setup_complete' AND value = '1'`).Scan(&setupDone)
	if setupDone > 0 {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{
			"success": false, "error": "Setup already completed",
		})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "Invalid request body",
		})
		return
	}

	if req.Username == "" || req.Password == "" {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "Username and password are required",
		})
		return
	}
	if len(req.Password) < 8 {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "Password must be at least 8 characters",
		})
		return
	}

	// Hash the password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Failed to hash password",
		})
		return
	}

	// Update the seeded admin user's password (created with empty hash at startup)
	// If username differs from "admin", rename too
	result, err := h.db.Exec(
		`UPDATE users SET password_hash = ?, username = ?, must_change_password = 0 WHERE username = 'admin'`,
		string(hash), req.Username,
	)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Failed to set admin credentials",
		})
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		// Admin user doesn't exist yet — insert fresh
		_, err = h.db.Exec(
			`INSERT INTO users (username, password_hash, email, role, active) VALUES (?, ?, 'admin@localhost', 'admin', 1)`,
			req.Username, string(hash),
		)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false, "error": "Failed to create admin user",
			})
			return
		}
	}

	log.Printf("SETUP: admin credentials configured for user '%s'", req.Username)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "message": "Admin credentials configured",
	})
}
