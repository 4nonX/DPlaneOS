package ha

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"dplaned/internal/audit"
)

// PDUConfig holds parameters for HTTP-based PDU (Power Distribution Unit) outlet fencing.
// This provides out-of-band STONITH that bypasses the operating system entirely —
// the rack physically cuts power to a misbehaving node via its PDU HTTP API.
// Compatible with Digital Loggers, iBoot, Raritan, and any PDU with a simple HTTP API.
type PDUConfig struct {
	Enable         bool   `json:"enable"`
	OutletOffURL   string `json:"outlet_off_url"`  // URL to cut power (e.g. http://pdu/outlet/2/off)
	Method         string `json:"method"`           // "GET" or "POST"; default "GET"
	Username       string `json:"username"`
	PasswordFile   string `json:"password_file"`    // path to 0600 password file
	TimeoutSecs    int    `json:"timeout_secs"`
	ExpectedStatus int    `json:"expected_status"`  // 0 = accept any 2xx
}

// GetPDUConfig reads the PDU fencing configuration from the database.
func GetPDUConfig(db *sql.DB) (PDUConfig, error) {
	var cfg PDUConfig
	err := db.QueryRow(`
		SELECT enable, outlet_off_url, method, username, password_file, timeout_secs, expected_status
		FROM ha_pdu_config LIMIT 1
	`).Scan(&cfg.Enable, &cfg.OutletOffURL, &cfg.Method, &cfg.Username,
		&cfg.PasswordFile, &cfg.TimeoutSecs, &cfg.ExpectedStatus)
	if err == sql.ErrNoRows {
		return cfg, nil
	}
	return cfg, err
}

// SavePDUConfig upserts the PDU fencing configuration.
func SavePDUConfig(db *sql.DB, cfg PDUConfig) error {
	method := strings.ToUpper(cfg.Method)
	if method != "GET" && method != "POST" {
		method = "GET"
	}
	if cfg.TimeoutSecs <= 0 {
		cfg.TimeoutSecs = 10
	}
	_, err := db.Exec(`
		INSERT INTO ha_pdu_config (id, enable, outlet_off_url, method, username, password_file, timeout_secs, expected_status)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(id) DO UPDATE SET
			enable          = excluded.enable,
			outlet_off_url  = excluded.outlet_off_url,
			method          = excluded.method,
			username        = excluded.username,
			password_file   = excluded.password_file,
			timeout_secs    = excluded.timeout_secs,
			expected_status = excluded.expected_status
	`, cfg.Enable, cfg.OutletOffURL, method, cfg.Username, cfg.PasswordFile, cfg.TimeoutSecs, cfg.ExpectedStatus)
	return err
}

// ExecutePDUFencing sends an HTTP command to the PDU to physically cut power to the peer's outlet.
// This is the "Silver Bullet" — it bypasses the peer OS entirely, firing even when the data
// network is fully partitioned (the PDU has its own management network path).
func ExecutePDUFencing(nodeID string, cfg PDUConfig) error {
	log.Printf("STONITH PDU: Initiating outlet power cut against node %s via %s", nodeID, cfg.OutletOffURL)
	start := time.Now()

	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	password := ""
	if cfg.PasswordFile != "" {
		passBytes, err := os.ReadFile(cfg.PasswordFile)
		if err != nil {
			errStr := fmt.Sprintf("PDU fence: failed to read password file: %v", err)
			audit.LogAction("ha_pdu_fence", "system", errStr, false, 0)
			return fmt.Errorf("%s", errStr)
		}
		password = strings.TrimSpace(string(passBytes))
	}

	method := strings.ToUpper(cfg.Method)
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequest(method, cfg.OutletOffURL, nil)
	if err != nil {
		errStr := fmt.Sprintf("PDU fence: invalid outlet URL %q: %v", cfg.OutletOffURL, err)
		audit.LogAction("ha_pdu_fence", "system", errStr, false, 0)
		return fmt.Errorf("%s", errStr)
	}
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, password)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		errStr := fmt.Sprintf("PDU fence: HTTP request to %s failed for node %s: %v", cfg.OutletOffURL, nodeID, err)
		audit.LogAction("ha_pdu_fence", "system", errStr, false, time.Since(start))
		return fmt.Errorf("%s", errStr)
	}
	io.Copy(io.Discard, resp.Body) //nolint
	resp.Body.Close()

	// Validate response
	if cfg.ExpectedStatus != 0 && resp.StatusCode != cfg.ExpectedStatus {
		errStr := fmt.Sprintf("PDU fence: unexpected status %d (want %d) fencing node %s", resp.StatusCode, cfg.ExpectedStatus, nodeID)
		audit.LogAction("ha_pdu_fence", "system", errStr, false, time.Since(start))
		return fmt.Errorf("%s", errStr)
	}
	if cfg.ExpectedStatus == 0 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		errStr := fmt.Sprintf("PDU fence: non-2xx status %d fencing node %s", resp.StatusCode, nodeID)
		audit.LogAction("ha_pdu_fence", "system", errStr, false, time.Since(start))
		return fmt.Errorf("%s", errStr)
	}

	log.Printf("STONITH PDU: Outlet power cut confirmed for node %s (PDU returned %d). Node is physically dead.", nodeID, resp.StatusCode)
	audit.LogAction("ha_pdu_fence", "system",
		fmt.Sprintf("PDU fenced node %s via %s — outlet power cut confirmed", nodeID, cfg.OutletOffURL),
		true, time.Since(start))
	return nil
}
