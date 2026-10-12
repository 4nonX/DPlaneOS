package ha

import (
	"crypto/rand"
	"database/sql"
	"math/big"
	"time"
)

// FencingConfig holds the settings of fencing that are not about one node:
// the random wait before a power-off, and how many of a pool's disks may
// fail to be reserved. (How each node is powered off is a PowerTarget.)
type FencingConfig struct {
	JitterMaxMs           int `json:"jitter_max_ms"`            // max random pre-fire delay in ms; default 3000
	DiskFaultTolerancePct int `json:"disk_fault_tolerance_pct"` // % of pool disks that may fail SCSI-3 PR without aborting; default 10
}

// GetFencingConfig reads the fencing settings.
func GetFencingConfig(db *sql.DB) (FencingConfig, error) {
	var cfg FencingConfig
	var jitterMaxMs, diskTolPct sql.NullInt64
	err := db.QueryRow(`SELECT jitter_max_ms, COALESCE(disk_fault_tolerance_pct, 10) FROM ha_fencing_config LIMIT 1`).
		Scan(&jitterMaxMs, &diskTolPct)
	if err == sql.ErrNoRows {
		cfg.DiskFaultTolerancePct = 10
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}
	cfg.JitterMaxMs = int(jitterMaxMs.Int64)
	cfg.DiskFaultTolerancePct = max(0, min(50, int(diskTolPct.Int64)))
	return cfg, nil
}

// SaveFencingConfig stores the fencing settings.
func SaveFencingConfig(db *sql.DB, cfg FencingConfig) error {
	// Beyond 30 s the wait would be longer than the takeover it protects.
	jitter := max(0, min(30000, cfg.JitterMaxMs))
	tol := max(0, min(50, cfg.DiskFaultTolerancePct))
	_, err := db.Exec(`
		INSERT INTO ha_fencing_config (id, jitter_max_ms, disk_fault_tolerance_pct)
		VALUES (1, $1, $2)
		ON CONFLICT(id) DO UPDATE SET
			jitter_max_ms            = excluded.jitter_max_ms,
			disk_fault_tolerance_pct = excluded.disk_fault_tolerance_pct
	`, jitter, tol)
	return err
}

// stonithJitter returns a random delay in [0, maxMs) milliseconds, so that
// two nodes do not roll the same one.
func stonithJitter(maxMs int) time.Duration {
	if maxMs <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxMs)))
	if err != nil {
		return time.Duration(maxMs) * time.Millisecond
	}
	return time.Duration(n.Int64()) * time.Millisecond
}
