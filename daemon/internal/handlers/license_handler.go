package handlers

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var licensePublicKeyBase64 = os.Getenv("DPLANE_LICENSE_PUBLIC_KEY")

type LicensePayload struct {
	Customer      string `json:"customer"`
	AuditsLimit   int    `json:"audits_limit"`
	ExpiresAt     string `json:"expires_at"`
	CERepoURL     string `json:"ce_repo_url"`
	CEVersion     string `json:"ce_version"`
	CEAccessToken string `json:"ce_access_token"`
	IssuedAt      string `json:"issued_at"`
}

type LicenseStatus struct {
	// Configured: this build can verify a license at all (it was started
	// with the vendor's public key). The released images are not; the web
	// interface shows nothing about licences there.
	Configured      bool   `json:"configured"`
	Active          bool   `json:"active"`
	Customer        string `json:"customer,omitempty"`
	AuditsLimit     int    `json:"audits_limit,omitempty"`
	AuditsRemaining int    `json:"audits_remaining,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	CEInstalled     bool   `json:"ce_installed,omitempty"`
	CERunning       bool   `json:"ce_running,omitempty"`
}

func handleLicenseActivate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if licensePublicKeyBase64 == "" {
			http.Error(w, "Licensing not configured", http.StatusInternalServerError)
			return
		}

		var req struct {
			LicenseKey string `json:"license_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		req.LicenseKey = strings.TrimSpace(req.LicenseKey)
		if req.LicenseKey == "" {
			http.Error(w, "License key required", http.StatusBadRequest)
			return
		}

		log.Printf("[LICENSE] Activation attempt: validating signature")

		valid, payload, err := VerifyLicense(req.LicenseKey, licensePublicKeyBase64)
		if !valid || err != nil {
			log.Printf("[LICENSE] Validation failed: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Invalid or expired license: " + err.Error(),
			})
			return
		}

		log.Printf("[LICENSE] Signature valid for customer: %s", payload.Customer)
		log.Printf("[LICENSE] Fetching CE code from: %s (version %s)", payload.CERepoURL, payload.CEVersion)

		ceCode, err := fetchCECode(payload)
		if err != nil {
			log.Printf("[LICENSE] Failed to fetch CE code: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Failed to fetch enterprise code: " + err.Error(),
			})
			return
		}

		log.Printf("[LICENSE] CE code downloaded (%d bytes), installing", len(ceCode))

		if err := installCECode(ceCode); err != nil {
			log.Printf("[LICENSE] Installation failed: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Failed to install enterprise code: " + err.Error(),
			})
			return
		}

		log.Printf("[LICENSE] CE code installed, storing license in database")

		_, err = db.Exec(`
			UPDATE enterprise_license
			SET customer_name = $1, audits_limit = $2, expires_at = $3,
				license_key = $4, ce_repo_url = $5, ce_version = $6,
				activated_at = $7, audits_consumed = 0
			WHERE id = 1
		`,
			payload.Customer, payload.AuditsLimit, payload.ExpiresAt,
			req.LicenseKey, payload.CERepoURL, payload.CEVersion,
			time.Now())

		if err != nil {
			log.Printf("[LICENSE] Failed to store license: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Failed to activate license: " + err.Error(),
			})
			return
		}

		log.Printf("[LICENSE] License stored for customer: %s", payload.Customer)
		log.Printf("[LICENSE] Starting CE daemon")

		if err := startCEDaemon(); err != nil {
			log.Printf("[LICENSE] Warning: CE daemon failed to start: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":      true,
			"customer":     payload.Customer,
			"audits_limit": payload.AuditsLimit,
			"expires_at":   payload.ExpiresAt,
			"message":      "Enterprise license activated successfully",
		})
	}
}

func handleLicenseStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var customer string
		var auditsLimit int
		var auditsConsumed int
		var expiresAt string

		err := db.QueryRow(`
			SELECT customer_name, audits_limit, audits_consumed, expires_at
			FROM enterprise_license WHERE id = 1
		`).Scan(&customer, &auditsLimit, &auditsConsumed, &expiresAt)

		configured := licensePublicKeyBase64 != ""
		if !configured || err == sql.ErrNoRows || customer == "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(LicenseStatus{Configured: configured, Active: false})
			return
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		isExpired := false
		if expiresAt != "never" {
			expTime, _ := time.Parse(time.RFC3339, expiresAt)
			isExpired = time.Now().After(expTime)
		}

		ceRunning := isCEDaemonRunning()
		ceInstalled := isCECodeInstalled()

		status := LicenseStatus{
			Configured:  true,
			Active:      !isExpired,
			Customer:    customer,
			AuditsLimit: auditsLimit,
			ExpiresAt:   expiresAt,
			CEInstalled: ceInstalled,
			CERunning:   ceRunning,
		}

		if auditsLimit > 0 {
			status.AuditsRemaining = auditsLimit - auditsConsumed
		} else if auditsLimit == -1 {
			status.AuditsRemaining = -1
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

func VerifyLicense(licenseKey, publicKeyBase64 string) (bool, *LicensePayload, error) {
	parts := strings.Split(licenseKey, ".")
	if len(parts) != 2 {
		return false, nil, fmt.Errorf("invalid format")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		return false, nil, fmt.Errorf("public key error")
	}
	pubKey := ed25519.PublicKey(pubKeyBytes)

	sigBytes, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return false, nil, fmt.Errorf("invalid signature")
	}

	payloadBytes, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false, nil, fmt.Errorf("invalid payload")
	}

	if !ed25519.Verify(pubKey, payloadBytes, sigBytes) {
		return false, nil, fmt.Errorf("signature mismatch")
	}

	var payload LicensePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return false, nil, fmt.Errorf("corrupted payload")
	}

	if payload.ExpiresAt != "never" {
		expiresAt, err := time.Parse(time.RFC3339, payload.ExpiresAt)
		if err != nil {
			return false, nil, fmt.Errorf("invalid expiration date")
		}
		if time.Now().After(expiresAt) {
			return false, nil, fmt.Errorf("license expired on %s", expiresAt.Format("2006-01-02"))
		}
	}

	return true, &payload, nil
}

func fetchCECode(payload *LicensePayload) ([]byte, error) {
	url := fmt.Sprintf("%s/releases/download/%s/dplane-compliance-linux-amd64.tar.gz",
		payload.CERepoURL, payload.CEVersion)

	req, _ := http.NewRequest("GET", url, nil)
	if payload.CEAccessToken != "" {
		req.Header.Set("Authorization", "token "+payload.CEAccessToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

func installCECode(tarGzBytes []byte) error {
	targetDir := "/opt/dplane-compliance"

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp("", "ce-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(tarGzBytes); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	cmd := exec.Command("tar", "-xzf", tmpFile.Name(), "-C", targetDir)
	if err := cmd.Run(); err != nil {
		return err
	}

	binaryPath := filepath.Join(targetDir, "dplane-compliance")
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return err
	}

	log.Printf("[LICENSE] CE code installed to %s", targetDir)
	return nil
}

func startCEDaemon() error {
	if isCEDaemonRunning() {
		return nil
	}

	tokenFile := "/etc/dplaneos-compliance/token"
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		return fmt.Errorf("CE token not found at %s; run: dplane init-ce-token", tokenFile)
	}

	cmd := exec.Command("/opt/dplane-compliance/dplane-compliance",
		"--port", "9001",
		"--ce-url", "http://localhost:9000/api")

	cmd.Env = append(os.Environ(), "CE_API_TOKEN="+strings.TrimSpace(string(tokenBytes)))

	logFile, err := os.OpenFile("/var/log/dplaneos-compliance/sidecar.log",
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return err
	}

	log.Printf("[LICENSE] CE daemon started (PID %d)", cmd.Process.Pid)
	return nil
}

func stopCEDaemon() error {
	cmd := exec.Command("pkill", "-f", "dplane-compliance")
	_ = cmd.Run()
	return nil
}

func isCEDaemonRunning() bool {
	cmd := exec.Command("pgrep", "-f", "dplane-compliance")
	return cmd.Run() == nil
}

func isCECodeInstalled() bool {
	_, err := os.Stat("/opt/dplane-compliance/dplane-compliance")
	return err == nil
}

func LicenseActivateHandler(db *sql.DB) http.HandlerFunc {
	return handleLicenseActivate(db)
}

func LicenseStatusHandler(db *sql.DB) http.HandlerFunc {
	return handleLicenseStatus(db)
}

func CheckLicenseExpiration(db *sql.DB) {
	var expiresAt string

	err := db.QueryRow(`
		SELECT expires_at FROM enterprise_license WHERE id = 1 AND customer_name != ''
	`).Scan(&expiresAt)

	if err == sql.ErrNoRows {
		return
	}

	if err != nil {
		log.Printf("[LICENSE] Expiration check failed: %v", err)
		return
	}

	if expiresAt == "never" {
		return
	}

	expTime, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		log.Printf("[LICENSE] Failed to parse expiration date: %v", err)
		return
	}

	if time.Now().After(expTime) {
		log.Printf("[LICENSE] License expired on %s, disabling enterprise features", expTime.Format("2006-01-02"))
		if err := stopCEDaemon(); err != nil {
			log.Printf("[LICENSE] Warning: could not stop CE daemon: %v", err)
		}
	}
}
