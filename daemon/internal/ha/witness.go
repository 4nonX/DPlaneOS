package ha

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// WitnessEntry is a single probe target with optional strict validation rules.
// Omit optional fields for a basic reachability check.
type WitnessEntry struct {
	URL               string `json:"url"`
	ExpectedStatus    int    `json:"expected_status,omitempty"`    // 0 = any valid HTTP response
	ExpectedBodyRegex string `json:"expected_body_regex,omitempty"` // "" = skip body check
	StrictTLS         bool   `json:"strict_tls"`                   // true = enforce cert verification
}

// WitnessConfig holds parameters for the quorum witness array.
// When enabled, the auto-failover gate in checkFailover() will only proceed
// if RequiredHealthy or more witnesses pass — proving this node is not network-isolated.
type WitnessConfig struct {
	Enable          bool           `json:"enable"`
	Witnesses       []WitnessEntry `json:"witnesses"`
	RequiredHealthy int            `json:"required_healthy"` // minimum passing probes; default 1
	TimeoutSecs     int            `json:"timeout_secs"`
}

// GetWitnessConfig reads the witness configuration from the database.
func GetWitnessConfig(db *sql.DB) (WitnessConfig, error) {
	var cfg WitnessConfig
	var witnessesJSON string
	err := db.QueryRow(`
		SELECT enable, witnesses_json, required_healthy, timeout_secs
		FROM ha_witness_config WHERE id = 1
	`).Scan(&cfg.Enable, &witnessesJSON, &cfg.RequiredHealthy, &cfg.TimeoutSecs)
	if err == sql.ErrNoRows {
		return WitnessConfig{TimeoutSecs: 5, RequiredHealthy: 1}, nil
	}
	if err != nil {
		return WitnessConfig{TimeoutSecs: 5, RequiredHealthy: 1}, err
	}
	if witnessesJSON != "" && witnessesJSON != "[]" {
		_ = json.Unmarshal([]byte(witnessesJSON), &cfg.Witnesses)
	}
	return cfg, nil
}

// SaveWitnessConfig persists the witness configuration to the database.
// Returns an error if any witness entry contains an invalid regex, preventing
// a bad config from silently disabling all failovers at probe time.
func SaveWitnessConfig(db *sql.DB, cfg WitnessConfig) error {
	if cfg.TimeoutSecs <= 0 {
		cfg.TimeoutSecs = 5
	}
	if cfg.RequiredHealthy <= 0 {
		cfg.RequiredHealthy = 1
	}
	for i, w := range cfg.Witnesses {
		if w.ExpectedBodyRegex != "" {
			if _, err := regexp.Compile(w.ExpectedBodyRegex); err != nil {
				return fmt.Errorf("witness[%d] has invalid body regex %q: %w", i, w.ExpectedBodyRegex, err)
			}
		}
	}
	witnessBytes, err := json.Marshal(cfg.Witnesses)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT INTO ha_witness_config (id, enable, witnesses_json, required_healthy, timeout_secs)
		VALUES (1, $1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
		  SET enable           = EXCLUDED.enable,
		      witnesses_json   = EXCLUDED.witnesses_json,
		      required_healthy = EXCLUDED.required_healthy,
		      timeout_secs     = EXCLUDED.timeout_secs
	`, cfg.Enable, string(witnessBytes), cfg.RequiredHealthy, cfg.TimeoutSecs)
	return err
}

// canReachWitness probes all configured witnesses concurrently.
// Returns true if at least cfg.RequiredHealthy witnesses pass their validation checks.
func canReachWitness(cfg WitnessConfig) bool {
	if len(cfg.Witnesses) == 0 {
		return false
	}
	required := cfg.RequiredHealthy
	if required <= 0 {
		required = 1
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	results := make(chan bool, len(cfg.Witnesses))
	var wg sync.WaitGroup
	for _, w := range cfg.Witnesses {
		wg.Add(1)
		go func(entry WitnessEntry) {
			defer wg.Done()
			results <- ProbeWitnessEntry(entry, timeout)
		}(w)
	}
	wg.Wait()
	close(results)

	healthy := 0
	for ok := range results {
		if ok {
			healthy++
		}
	}
	return healthy >= required
}

// ProbeWitnessEntry performs a single HTTP probe against one WitnessEntry,
// applying optional TLS enforcement, status code, and body regex checks.
// Exported for use by the handler's ad-hoc test endpoint.
func ProbeWitnessEntry(entry WitnessEntry, timeout time.Duration) bool {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !entry.StrictTLS},
	}
	client := &http.Client{Timeout: timeout, Transport: transport}

	resp, err := client.Get(entry.URL)
	if err != nil {
		log.Printf("HA WITNESS: probe to %s failed: %v", entry.URL, err)
		return false
	}
	defer resp.Body.Close()

	if entry.ExpectedStatus != 0 && resp.StatusCode != entry.ExpectedStatus {
		log.Printf("HA WITNESS: %s returned status %d, want %d", entry.URL, resp.StatusCode, entry.ExpectedStatus)
		return false
	}

	if entry.ExpectedBodyRegex != "" {
		re, err := regexp.Compile(entry.ExpectedBodyRegex)
		if err != nil {
			log.Printf("HA WITNESS: invalid body regex %q: %v", entry.ExpectedBodyRegex, err)
			return false
		}
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		if !re.Match(buf[:n]) {
			log.Printf("HA WITNESS: %s body did not match regex %q", entry.URL, entry.ExpectedBodyRegex)
			return false
		}
	}

	return true
}
