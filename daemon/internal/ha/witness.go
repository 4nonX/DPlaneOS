package ha

import (
	"database/sql"
	"log"
	"net/http"
	"time"
)

// WitnessConfig holds parameters for the quorum witness endpoint.
// When enabled, the auto-failover gate in checkFailover() will only proceed
// if this node can reach the witness — proving it is not network-isolated.
type WitnessConfig struct {
	Enable      bool   `json:"enable"`
	URL         string `json:"url"`
	TimeoutSecs int    `json:"timeout_secs"`
}

// GetWitnessConfig reads the witness configuration from the database.
func GetWitnessConfig(db *sql.DB) (WitnessConfig, error) {
	var cfg WitnessConfig
	err := db.QueryRow(`
		SELECT enable, url, timeout_secs
		FROM ha_witness_config WHERE id = 1
	`).Scan(&cfg.Enable, &cfg.URL, &cfg.TimeoutSecs)
	if err == sql.ErrNoRows {
		return WitnessConfig{TimeoutSecs: 5}, nil
	}
	if err != nil {
		return WitnessConfig{TimeoutSecs: 5}, err
	}
	return cfg, nil
}

// SaveWitnessConfig persists the witness configuration to the database.
func SaveWitnessConfig(db *sql.DB, cfg WitnessConfig) error {
	if cfg.TimeoutSecs <= 0 {
		cfg.TimeoutSecs = 5
	}
	_, err := db.Exec(`
		INSERT INTO ha_witness_config (id, enable, url, timeout_secs)
		VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE
		  SET enable = EXCLUDED.enable,
		      url = EXCLUDED.url,
		      timeout_secs = EXCLUDED.timeout_secs
	`, cfg.Enable, cfg.URL, cfg.TimeoutSecs)
	return err
}

// canReachWitness performs a single HTTP GET to the witness URL.
// Any valid HTTP response (any status code) counts as reachable.
// A connection error or timeout means this node cannot reach the witness.
func canReachWitness(cfg WitnessConfig) bool {
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(cfg.URL)
	if err != nil {
		log.Printf("HA WITNESS: probe to %s failed: %v", cfg.URL, err)
		return false
	}
	resp.Body.Close()
	return true
}
