package ha

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/security"
)

type FencingConfig struct {
	Enable          bool   `json:"enable"`
	BMCIP           string `json:"bmc_ip"`
	BMCUser         string `json:"bmc_user"`
	BMCPasswordFile string `json:"bmc_password_file"`
	JitterMaxMs     int    `json:"jitter_max_ms"` // max random pre-fire delay in ms; default 3000
}

// GetFencingConfig reads the Fencing HA config from the PostgreSQL database.
func GetFencingConfig(db *sql.DB) (FencingConfig, error) {
	var cfg FencingConfig
	var bmcIP, bmcUser, bmcPassFile sql.NullString
	var enable sql.NullBool

	var jitterMaxMs sql.NullInt64
	err := db.QueryRow(`
		SELECT enable, bmc_ip, bmc_user, bmc_password_file, jitter_max_ms
		FROM ha_fencing_config
		LIMIT 1
	`).Scan(&enable, &bmcIP, &bmcUser, &bmcPassFile, &jitterMaxMs)

	if err == sql.ErrNoRows {
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}

	cfg.Enable = enable.Bool
	cfg.BMCIP = bmcIP.String
	cfg.BMCUser = bmcUser.String
	cfg.BMCPasswordFile = bmcPassFile.String
	cfg.JitterMaxMs = int(jitterMaxMs.Int64)
	return cfg, nil
}

// SaveFencingConfig upserts the Fencing HA config.
func SaveFencingConfig(db *sql.DB, cfg FencingConfig) error {
	if cfg.JitterMaxMs < 0 {
		cfg.JitterMaxMs = 0
	}
	if cfg.JitterMaxMs > 30000 {
		cfg.JitterMaxMs = 30000 // cap at 30s — beyond this the fencing window is unreasonably large
	}
	_, err := db.Exec(`
		INSERT INTO ha_fencing_config (id, enable, bmc_ip, bmc_user, bmc_password_file, jitter_max_ms)
		VALUES (1, $1, $2, $3, $4, $5)
		ON CONFLICT(id) DO UPDATE SET
			enable            = excluded.enable,
			bmc_ip            = excluded.bmc_ip,
			bmc_user          = excluded.bmc_user,
			bmc_password_file = excluded.bmc_password_file,
			jitter_max_ms     = excluded.jitter_max_ms
	`, cfg.Enable, cfg.BMCIP, cfg.BMCUser, cfg.BMCPasswordFile, cfg.JitterMaxMs)
	return err
}

// stonithJitter returns a cryptographically random delay in [0, maxMs) milliseconds.
// Using crypto/rand prevents both nodes from rolling identical delays.
func stonithJitter(maxMs int) time.Duration {
	if maxMs <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxMs)))
	if err != nil {
		// On the rare failure, use maxMs as a conservative safe default.
		return time.Duration(maxMs) * time.Millisecond
	}
	return time.Duration(n.Int64()) * time.Millisecond
}

// ExecuteFencing safely connects to the BMC and forces a chassis power off.
// Returns an error if the power off is unsuccessful or chassis doesn't go dark within 60s.
func ExecuteFencing(nodeID string, cfg FencingConfig) error {
	log.Printf("STONITH: Initiating fencing sequence against node %s at BMC %s", nodeID, cfg.BMCIP)
	start := time.Now()
	
	if !cfg.Enable {
		return fmt.Errorf("fencing is disabled but ExecuteFencing was invoked")
	}

	// STONITH Jitter: random pre-fire delay prevents both nodes from shooting
	// simultaneously when they hit the FailoverAfter threshold at the same moment.
	// Node A rolls 400ms, Node B rolls 2100ms — A fires first, B dies before waking.
	maxMs := cfg.JitterMaxMs
	if maxMs == 0 {
		maxMs = 3000 // default 3s window
	}
	delay := stonithJitter(maxMs)
	log.Printf("STONITH: Jitter delay %v before fencing node %s (mutual-destruction prevention)", delay, nodeID)
	time.Sleep(delay)

	// Read the raw password securely from the 0600 file.
	passBytes, err := os.ReadFile(cfg.BMCPasswordFile)
	if err != nil {
		errStr := fmt.Sprintf("Failed to read BMC password file: %v", err)
		audit.LogAction("ha_fence", "system", errStr, false, 0)
		return fmt.Errorf("%s", errStr)
	}
	password := strings.TrimSpace(string(passBytes))

	// 1. Issue the Power Off Command
	// A 30-second hard deadline guards against a hung or extremely slow BMC.
	// Without this, the goroutine blocks indefinitely with fencingInProgress=true,
	// permanently preventing any future automated failover on this node.
	const powerOffTimeout = 30 * time.Second
	powerOffCtx, powerOffCancel := context.WithTimeout(context.Background(), powerOffTimeout)
	defer powerOffCancel()

	args := []string{"-I", "lanplus", "-H", cfg.BMCIP, "-U", cfg.BMCUser, "-E", "chassis", "power", "off"}
	if err := security.ValidateCommand("ipmitool_power_off", args); err != nil {
		errStr := fmt.Sprintf("Security validation rejected fencing command: %v", err)
		audit.LogAction("ha_fence", "system", errStr, false, 0)
		return fmt.Errorf("%s", errStr)
	}

	cmd := exec.CommandContext(powerOffCtx, "ipmitool", args...)
	cmd.Env = append(os.Environ(), "IPMI_PASSWORD="+password)

	if out, err := cmd.CombinedOutput(); err != nil {
		errStr := fmt.Sprintf("Fencing power off failed for %s: %v - %s", cfg.BMCIP, err, strings.TrimSpace(string(out)))
		audit.LogAction("ha_fence", "system", errStr, false, time.Since(start))
		return fmt.Errorf("%s", errStr)
	}

	// 2. Poll the status for up to 60 seconds
	log.Printf("STONITH: Power off issued. Polling chassis power status for up to 60 seconds...")
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

statusLoop:
	for {
		select {
		case <-timeout:
			errStr := fmt.Sprintf("Fencing timeout breached (60s). Chassis %s did not confirm power off.", cfg.BMCIP)
			audit.LogAction("ha_fence", "system", errStr, false, time.Since(start))
			return fmt.Errorf("%s", errStr)
		case <-ticker.C:
			statusArgs := []string{"-I", "lanplus", "-H", cfg.BMCIP, "-U", cfg.BMCUser, "-E", "chassis", "power", "status"}
			if err := security.ValidateCommand("ipmitool_power_status", statusArgs); err != nil {
				return fmt.Errorf("Security validation rejected status command: %v", err)
			}

			// Per-poll 10s timeout: a single slow BMC response must not consume
			// the remaining budget of the outer 60-second status window.
			pollCtx, pollCancel := context.WithTimeout(context.Background(), 10*time.Second)
			statusCmd := exec.CommandContext(pollCtx, "ipmitool", statusArgs...)
			statusCmd.Env = append(os.Environ(), "IPMI_PASSWORD="+password)
			out, err := statusCmd.CombinedOutput()
			pollCancel()
			if err == nil {
				outputStr := strings.ToLower(strings.TrimSpace(string(out)))
				if strings.Contains(outputStr, "is off") {
					log.Printf("STONITH: Verified node %s is successfully fenced and mathematically dead.", nodeID)
					break statusLoop
				}
			}
		}
	}

	audit.LogAction("ha_fence", "system", fmt.Sprintf("Fenced node %s at BMC %s — chassis confirmed dark", nodeID, cfg.BMCIP), true, time.Since(start))
	return nil
}
