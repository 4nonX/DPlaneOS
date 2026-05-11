package ha

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"
)

// SBDConfig holds the ZFS-dataset-lease fencing configuration.
// All fields are optional: an empty Pool means SBD is unconfigured and the
// lease goroutine never starts. Single-node deployments leave this zeroed
// and are completely unaffected.
type SBDConfig struct {
	Pool         string `json:"pool"`           // ZFS pool name; "" = SBD disabled
	Dataset      string `json:"dataset"`        // dataset under pool for the lease token
	LeaseTTLSecs int    `json:"lease_ttl_secs"` // seconds before a stale lease is considered dead; default 30
}

// GetSBDConfig reads the SBD config from the database.
func GetSBDConfig(db *sql.DB) (SBDConfig, error) {
	var cfg SBDConfig
	var pool, dataset sql.NullString
	var ttl sql.NullInt64
	err := db.QueryRow(`
		SELECT pool, dataset, lease_ttl_secs
		FROM ha_sbd_config
		LIMIT 1
	`).Scan(&pool, &dataset, &ttl)
	if err == sql.ErrNoRows {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	cfg.Pool = pool.String
	cfg.Dataset = dataset.String
	cfg.LeaseTTLSecs = int(ttl.Int64)
	return cfg, nil
}

// SaveSBDConfig upserts the SBD config row.
func SaveSBDConfig(db *sql.DB, cfg SBDConfig) error {
	if cfg.LeaseTTLSecs < 5 {
		cfg.LeaseTTLSecs = 30
	}
	if cfg.LeaseTTLSecs > 300 {
		cfg.LeaseTTLSecs = 300
	}
	_, err := db.Exec(`
		INSERT INTO ha_sbd_config (id, pool, dataset, lease_ttl_secs)
		VALUES (1, $1, $2, $3)
		ON CONFLICT(id) DO UPDATE SET
			pool           = excluded.pool,
			dataset        = excluded.dataset,
			lease_ttl_secs = excluded.lease_ttl_secs
	`, cfg.Pool, cfg.Dataset, cfg.LeaseTTLSecs)
	return err
}

// GlobalSBD is the daemon-wide SBD lease manager. Started at daemon init if
// SBD is configured; handlers call Restart when the config is saved.
var GlobalSBD SBDLeaseManager

// SBDLeaseManager continuously writes a ZFS property timestamp so that peer
// nodes can detect when this node has stopped renewing (i.e. died).
// It is a no-op when Pool is empty, keeping single-node deployments clean.
type SBDLeaseManager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	live   bool
	lastOK time.Time
}

// Start begins the renewal goroutine if cfg.Pool is non-empty.
// Calling Start when already running is a no-op.
func (s *SBDLeaseManager) Start(cfg SBDConfig) {
	if cfg.Pool == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.live = true
	go s.renewLoop(ctx, cfg)
	log.Printf("SBD: Lease manager started for %s/%s (TTL %ds)", cfg.Pool, cfg.Dataset, cfg.LeaseTTLSecs)
}

// Stop halts the renewal goroutine. Safe to call when not started.
func (s *SBDLeaseManager) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live {
		return
	}
	s.cancel()
	s.live = false
	log.Printf("SBD: Lease manager stopped")
}

// Restart stops any running goroutine and starts fresh with the new config.
// If cfg.Pool is empty the manager ends up stopped (no-op for single-node).
func (s *SBDLeaseManager) Restart(cfg SBDConfig) {
	s.Stop()
	s.Start(cfg)
}

// IsLive reports whether the renewal goroutine is currently running.
func (s *SBDLeaseManager) IsLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// LastOK returns the last successful renewal timestamp. Zero if never renewed.
func (s *SBDLeaseManager) LastOK() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastOK
}

func (s *SBDLeaseManager) renewLoop(ctx context.Context, cfg SBDConfig) {
	ttl := cfg.LeaseTTLSecs
	if ttl < 5 {
		ttl = 30
	}
	interval := max(time.Duration(ttl/3)*time.Second, 2*time.Second)
	// Renew immediately so the lease is hot from the first second.
	if err := renewLease(cfg); err != nil {
		log.Printf("SBD: Initial lease renewal failed: %v", err)
	} else {
		s.mu.Lock()
		s.lastOK = time.Now()
		s.mu.Unlock()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := renewLease(cfg); err != nil {
				log.Printf("SBD: Lease renewal failed: %v", err)
			} else {
				s.mu.Lock()
				s.lastOK = time.Now()
				s.mu.Unlock()
			}
		}
	}
}

// renewLease writes a unix timestamp as a ZFS user property on the SBD dataset
// so peer nodes can read it and detect a gap exceeding LeaseTTLSecs.
func renewLease(cfg SBDConfig) error {
	ds := cfg.Pool + "/" + cfg.Dataset
	ts := fmt.Sprintf("%d", time.Now().Unix())
	cmd := exec.Command("zfs", "set", "dplaneos:sbd_lease="+ts, ds)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zfs set failed on %s: %v - %s", ds, err, string(out))
	}
	return nil
}

// ExecuteSBDFence triggers an immediate self-reboot via `reboot -f`.
// It is only called when a peer node confirms that this node's lease has
// expired AND this node itself cannot renew - meaning it has been partitioned
// or lost ZFS access. The self-reboot guarantees the fencing action happens
// even without an out-of-band BMC channel.
func ExecuteSBDFence(reason string) error {
	log.Printf("SBD: SELF-FENCE triggered - reason: %s - executing reboot -f", reason)
	cmd := exec.Command("reboot", "-f")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("SBD self-fence reboot failed: %v - %s", err, string(out))
	}
	return nil
}
