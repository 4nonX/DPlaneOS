package ha

import "database/sql"

// WatchdogConfig holds the hardware watchdog configuration.
type WatchdogConfig struct {
	Enable         bool   `json:"enable"`
	Device         string `json:"device"`           // e.g. /dev/watchdog
	TimeoutSecs    int    `json:"timeout_secs"`     // kernel watchdog timeout; must be < failover_after
	PetIntervalSec int    `json:"pet_interval_sec"` // how often to write (should be < timeout/2)
}

// GetWatchdogConfig reads the watchdog configuration from the database.
func GetWatchdogConfig(db *sql.DB) (WatchdogConfig, error) {
	cfg := WatchdogConfig{
		Device:         "/dev/watchdog",
		TimeoutSecs:    30,
		PetIntervalSec: 10,
	}
	var device string
	var timeout, pet int
	var enable bool
	err := db.QueryRow(
		`SELECT enable, device, timeout_secs, pet_interval_sec FROM ha_watchdog_config WHERE id = 1`,
	).Scan(&enable, &device, &timeout, &pet)
	if err != nil {
		return cfg, err
	}
	cfg.Enable = enable
	if device != "" {
		cfg.Device = device
	}
	if timeout > 0 {
		cfg.TimeoutSecs = timeout
	}
	if pet > 0 {
		cfg.PetIntervalSec = pet
	}
	return cfg, nil
}

// SaveWatchdogConfig persists watchdog configuration to the database.
func SaveWatchdogConfig(db *sql.DB, cfg WatchdogConfig) error {
	device := cfg.Device
	if device == "" {
		device = "/dev/watchdog"
	}
	timeout := cfg.TimeoutSecs
	if timeout <= 0 {
		timeout = 30
	}
	pet := cfg.PetIntervalSec
	if pet <= 0 {
		pet = 10
	}
	_, err := db.Exec(`
		INSERT INTO ha_watchdog_config (id, enable, device, timeout_secs, pet_interval_sec)
		VALUES (1, $1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			enable           = excluded.enable,
			device           = excluded.device,
			timeout_secs     = excluded.timeout_secs,
			pet_interval_sec = excluded.pet_interval_sec`,
		cfg.Enable, device, timeout, pet)
	return err
}
