package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/systemd"
)

// S3 object storage: Versity Gateway (versitygw) serving a folder of a pool
// over the S3 protocol. Buckets are the folder's directories and objects are
// plain files in them, so what is stored through S3 is ordinary data of the
// dataset: snapshots, replication and the file browser see it.
//
// (It replaces MinIO, which its makers abandoned with known holes that let
// anyone write objects without credentials.)
//
// The daemon runs it as a unit of its own (dplaneos-s3) with the settings of
// the S3 page; the system provides the program (services.dplaneos.s3.enable).

const (
	s3ConfigFile  = "s3-config.json"
	s3ServiceFile = "/etc/systemd/system/dplaneos-s3.service"
	s3Program     = "versitygw"
	s3HealthPath  = "/health"
)

var s3EnvFile, s3Service = func() (string, string) {
	if IsNixOS() {
		return "/var/lib/dplaneos/s3/s3.env", "dplaneos-s3"
	}
	return "/etc/dplaneos-s3.env", "dplaneos-s3"
}()

var s3Mu sync.RWMutex

type S3Config struct {
	RootUser     string `json:"root_user"`     // access key of the administrator
	RootPassword string `json:"root_password"` // its secret key
	VolumePath   string `json:"volume_path"`   // folder whose directories are the buckets
	APIPort      int    `json:"api_port"`
	// Enabled records that the operator started the service, so the daemon
	// starts it again after a reboot.
	Enabled bool `json:"enabled"`
}

func defaultS3Config() S3Config {
	return S3Config{
		RootUser:     "s3admin",
		RootPassword: "",
		VolumePath:   "",
		APIPort:      9000,
	}
}

func loadS3Config() (S3Config, error) {
	s3Mu.RLock()
	defer s3Mu.RUnlock()

	data, err := os.ReadFile(configPath(s3ConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return defaultS3Config(), nil
		}
		return S3Config{}, err
	}
	var cfg S3Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return S3Config{}, err
	}
	return cfg, nil
}

var errS3Validation = errors.New("s3 config validation failed")

// atomicModifyS3Config holds the write lock across the full load-modify-save cycle.
// Callbacks must return errS3Validation for user-input errors (mapped to 400).
func atomicModifyS3Config(fn func(S3Config) (S3Config, error)) error {
	s3Mu.Lock()
	defer s3Mu.Unlock()

	data, err := os.ReadFile(configPath(s3ConfigFile))
	var cfg S3Config
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		cfg = defaultS3Config()
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
	}
	modified, err := fn(cfg)
	if err != nil {
		return err
	}
	os.MkdirAll(ConfigDir, 0755)
	out, err := json.MarshalIndent(modified, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(s3ConfigFile), out, 0600)
}

func s3Installed() bool {
	_, err := exec.LookPath(s3Program)
	return err == nil
}

func s3ServiceActive() bool {
	out, err := cmdutil.RunFast("systemctl", "is-active", s3Service)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

var s3KeyRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateS3Config(cfg S3Config) error {
	if len(cfg.RootUser) < 3 || len(cfg.RootUser) > 64 || !s3KeyRe.MatchString(cfg.RootUser) {
		return fmt.Errorf("access key: 3 to 64 letters, digits, dots, dashes or underscores")
	}
	if cfg.RootPassword != "" {
		if len(cfg.RootPassword) < 8 {
			return fmt.Errorf("secret key must be at least 8 characters")
		}
		// Goes into an environment file.
		if strings.ContainsAny(cfg.RootPassword, "\n\r\"'\\$` ") {
			return fmt.Errorf("secret key must not contain spaces, quotes, backslashes, $ or backticks")
		}
	}
	if cfg.VolumePath == "" {
		return fmt.Errorf("choose the folder the buckets are stored in")
	}
	if err := sharePathOK(cfg.VolumePath); err != nil {
		return fmt.Errorf("folder: %w", err)
	}
	if cfg.APIPort < 1 || cfg.APIPort > 65535 {
		return fmt.Errorf("api_port must be 1-65535")
	}
	return nil
}

func generateS3Env(cfg S3Config) string {
	var sb strings.Builder
	sb.WriteString("# DPlaneOS S3 service - written from the S3 page, do not edit by hand\n")
	fmt.Fprintf(&sb, "ROOT_ACCESS_KEY=%s\n", cfg.RootUser)
	fmt.Fprintf(&sb, "ROOT_SECRET_KEY=%s\n", cfg.RootPassword)
	fmt.Fprintf(&sb, "VGW_PORT=:%d\n", cfg.APIPort)
	// Answers without credentials: lets monitoring see that the service is up.
	fmt.Fprintf(&sb, "VGW_HEALTH=%s\n", s3HealthPath)
	return sb.String()
}

func s3ServiceUnit(cfg S3Config) (string, error) {
	program, err := systemd.ResolveCommand(s3Program)
	if err != nil {
		return "", fmt.Errorf("%s (services.dplaneos.s3.enable): %w", s3Program, err)
	}
	return fmt.Sprintf(`[Unit]
Description=DPlaneOS S3 object storage (Versity Gateway)
After=network-online.target zfs.target
Wants=network-online.target
RequiresMountsFor=%s

[Service]
EnvironmentFile=%s
ExecStart=%s posix %s
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, cfg.VolumePath, s3EnvFile, program, cfg.VolumePath), nil
}

func applyS3Config(cfg S3Config) error {
	if err := validateS3Config(cfg); err != nil {
		return err
	}

	// Write env file
	if err := os.MkdirAll(filepath.Dir(s3EnvFile), 0700); err != nil {
		return fmt.Errorf("failed to create s3 env dir: %w", err)
	}
	if err := os.WriteFile(s3EnvFile, []byte(generateS3Env(cfg)), 0640); err != nil {
		return fmt.Errorf("failed to write s3 env: %w", err)
	}

	if IsNixOS() {
		unit, err := s3ServiceUnit(cfg)
		if err != nil {
			return err
		}
		if err := systemd.InstallService(s3Service, unit); err != nil {
			return fmt.Errorf("failed to install s3 unit: %w", err)
		}
	} else if _, err := os.Stat(s3ServiceFile); os.IsNotExist(err) {
		// Write service file if not already present
		serviceDir := filepath.Dir(s3ServiceFile)
		if mkErr := os.MkdirAll(serviceDir, 0755); mkErr != nil {
			log.Printf("WARN: s3 service dir: %v", mkErr)
		}
		unit, unitErr := s3ServiceUnit(cfg)
		if unitErr != nil {
			return unitErr
		}
		if writeErr := os.WriteFile(s3ServiceFile, []byte(unit), 0644); writeErr != nil {
			log.Printf("WARN: s3 service file write: %v", writeErr)
		} else {
			// Daemon reload so systemd picks up the new unit
			cmdutil.RunFast("systemctl", "daemon-reload") //nolint
		}
	}

	// The firewall lets S3 clients and the console in while it is switched on.
	var ports []string
	if cfg.Enabled {
		ports = []string{fmt.Sprintf("tcp %d", cfg.APIPort)}
	}
	if err := openServicePorts("s3", ports); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}

	// Restart if already running, otherwise just reload env
	if s3ServiceActive() {
		_, err := cmdutil.RunFast("systemctl", "restart", s3Service)
		if err != nil {
			return fmt.Errorf("failed to restart the S3 service: %w", err)
		}
	}

	return nil
}

// GetS3Status returns the running state and settings of the S3 service.
// GET /api/s3/status
func GetS3Status(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadS3Config()
	installed := s3Installed()
	active := installed && s3ServiceActive()

	respondOK(w, map[string]any{
		"success":     true,
		"installed":   installed,
		"active":      active,
		"api_port":    cfg.APIPort,
		"health_path": s3HealthPath,
	})
}

// GetS3Config returns the current S3 configuration (password redacted).
// GET /api/s3/config
func GetS3Config(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadS3Config()
	if err != nil {
		respondErrorSimple(w, "Failed to load config", http.StatusInternalServerError)
		return
	}
	// Redact password in response
	out := map[string]any{
		"root_user": cfg.RootUser,
		"root_password": func() string {
			if cfg.RootPassword != "" {
				return "••••••••"
			}
			return ""
		}(),
		"volume_path": cfg.VolumePath,
		"api_port":    cfg.APIPort,
	}
	respondOK(w, map[string]any{"success": true, "config": out})
}

// UpdateS3Config saves config and applies it (restarts service if running).
// PUT /api/s3/config
func UpdateS3Config(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootUser     string `json:"root_user"`
		RootPassword string `json:"root_password"`
		VolumePath   string `json:"volume_path"`
		APIPort      int    `json:"api_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var validationErr error
	var merged S3Config
	if err := atomicModifyS3Config(func(current S3Config) (S3Config, error) {
		// Apply only non-empty fields; keep existing password if placeholder sent.
		if req.RootUser != "" {
			current.RootUser = req.RootUser
		}
		if req.RootPassword != "" && req.RootPassword != "••••••••" {
			current.RootPassword = req.RootPassword
		}
		if req.VolumePath != "" {
			current.VolumePath = req.VolumePath
		}
		if req.APIPort != 0 {
			current.APIPort = req.APIPort
		}
		if validationErr = validateS3Config(current); validationErr != nil {
			return S3Config{}, errS3Validation
		}
		merged = current
		return current, nil
	}); err != nil {
		if errors.Is(err, errS3Validation) {
			respondErrorSimple(w, validationErr.Error(), http.StatusBadRequest)
		} else {
			respondErrorSimple(w, "Failed to save config", http.StatusInternalServerError)
		}
		return
	}

	if err := applyS3Config(merged); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}

	audit.LogActivity("system", "s3_config_updated", map[string]any{"action": "S3 configuration updated"})
	respondOK(w, map[string]any{"success": true, "message": "Configuration applied"})
}

// StartS3 starts the S3 service.
// POST /api/s3/start
func StartS3(w http.ResponseWriter, r *http.Request) {
	if !s3Installed() {
		respondErrorSimple(w, "The S3 service is not part of this system", http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadS3Config()
	if cfg.RootPassword == "" {
		respondErrorSimple(w, "Set a secret key before starting the S3 service", http.StatusBadRequest)
		return
	}
	if err := applyS3Config(cfg); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "start", s3Service); err != nil {
		respondUserErrStatus(w, http.StatusInternalServerError, SanitizeServiceControl("S3", err), err)
		return
	}
	setS3Enabled(true)
	audit.LogActivity("system", "s3_start", nil)
	respondOK(w, map[string]any{"success": true})
}

// StopS3 stops the S3 service.
// POST /api/s3/stop
func StopS3(w http.ResponseWriter, r *http.Request) {
	if _, err := cmdutil.RunFast("systemctl", "stop", s3Service); err != nil {
		respondUserErrStatus(w, http.StatusInternalServerError, SanitizeServiceControl("S3", err), err)
		return
	}
	setS3Enabled(false)
	audit.LogActivity("system", "s3_stop", nil)
	respondOK(w, map[string]any{"success": true})
}

// RestartS3 restarts the S3 service.
// POST /api/s3/restart
func RestartS3(w http.ResponseWriter, r *http.Request) {
	if !s3Installed() {
		respondErrorSimple(w, "The S3 service is not part of this system", http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadS3Config()
	if err := applyS3Config(cfg); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "restart", s3Service); err != nil {
		respondUserErrStatus(w, http.StatusInternalServerError, SanitizeServiceControl("S3", err), err)
		return
	}
	audit.LogActivity("system", "s3_restart", nil)
	respondOK(w, map[string]any{"success": true})
}

// setS3Enabled records whether the S3 service is switched on (it is started again
// after a reboot) and opens or closes its ports in the firewall.
func setS3Enabled(on bool) {
	var ports []string
	if err := atomicModifyS3Config(func(c S3Config) (S3Config, error) {
		c.Enabled = on
		if on {
			ports = []string{fmt.Sprintf("tcp %d", c.APIPort)}
		}
		return c, nil
	}); err != nil {
		log.Printf("WARN: s3 enabled flag: %v", err)
	}
	if err := openServicePorts("s3", ports); err != nil {
		log.Printf("WARN: s3 firewall: %v", err)
	}
}

// RestoreS3 re-installs and starts the S3 service at daemon start when it was
// running before (NixOS runtime units are cleared on reboot).
func RestoreS3() {
	if !IsNixOS() || !s3Installed() {
		return
	}
	cfg, err := loadS3Config()
	if err != nil || !cfg.Enabled {
		return
	}
	if err := applyS3Config(cfg); err != nil {
		log.Printf("WARN: restore s3: %v", err)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "start", s3Service); err != nil {
		log.Printf("WARN: start s3: %v", err)
	}
}
