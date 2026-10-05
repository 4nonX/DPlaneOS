package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/systemd"
)

// ═══════════════════════════════════════════════════════════════
//  MinIO Object Storage Management
//  Manages MinIO via systemctl and writes /etc/minio.env.
// ═══════════════════════════════════════════════════════════════

const (
	minioConfigFile  = "minio-config.json"
	minioServiceFile = "/etc/systemd/system/minio.service"
)

// On NixOS /etc is read-only and no minio unit exists: the env file lives in
// /var/lib/dplaneos and the daemon runs MinIO as runtime unit dplaneos-minio
// (services.dplaneos.s3.enable provides the binary).
var minioEnvFile, minioService = func() (string, string) {
	if IsNixOS() {
		return "/var/lib/dplaneos/minio/minio.env", "dplaneos-minio"
	}
	return "/etc/minio.env", "minio"
}()

var minioMu sync.RWMutex

type MinioConfig struct {
	RootUser     string `json:"root_user"`
	RootPassword string `json:"root_password"`
	VolumePath   string `json:"volume_path"`
	APIPort      int    `json:"api_port"`
	ConsolePort  int    `json:"console_port"`
	// Enabled records that the operator started MinIO, so the daemon can
	// restore it at start (runtime units do not survive a reboot on NixOS).
	Enabled bool `json:"enabled"`
}

func defaultMinioConfig() MinioConfig {
	return MinioConfig{
		RootUser:     "minioadmin",
		RootPassword: "",
		VolumePath:   "/tank/minio",
		APIPort:      9000,
		ConsolePort:  9001,
	}
}

func loadMinioConfig() (MinioConfig, error) {
	minioMu.RLock()
	defer minioMu.RUnlock()

	data, err := os.ReadFile(configPath(minioConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return defaultMinioConfig(), nil
		}
		return MinioConfig{}, err
	}
	var cfg MinioConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return MinioConfig{}, err
	}
	return cfg, nil
}

var errMinioValidation = errors.New("minio config validation failed")

// atomicModifyMinioConfig holds the write lock across the full load-modify-save cycle.
// Callbacks must return errMinioValidation for user-input errors (mapped to 400).
func atomicModifyMinioConfig(fn func(MinioConfig) (MinioConfig, error)) error {
	minioMu.Lock()
	defer minioMu.Unlock()

	data, err := os.ReadFile(configPath(minioConfigFile))
	var cfg MinioConfig
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		cfg = defaultMinioConfig()
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
	return os.WriteFile(configPath(minioConfigFile), out, 0600)
}

func minioInstalled() bool {
	_, err := cmdutil.RunFast("which", "minio")
	return err == nil
}

func minioServiceActive() bool {
	out, err := cmdutil.RunFast("systemctl", "is-active", minioService)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

func validateMinioConfig(cfg MinioConfig) error {
	if cfg.RootUser == "" {
		return fmt.Errorf("root_user is required")
	}
	if len(cfg.RootUser) < 3 || len(cfg.RootUser) > 64 {
		return fmt.Errorf("root_user must be 3-64 characters")
	}
	if cfg.RootPassword != "" && len(cfg.RootPassword) < 8 {
		return fmt.Errorf("root_password must be at least 8 characters")
	}
	if cfg.VolumePath == "" || !strings.HasPrefix(cfg.VolumePath, "/") {
		return fmt.Errorf("volume_path must be an absolute path")
	}
	if strings.Contains(cfg.VolumePath, "..") {
		return fmt.Errorf("volume_path must not contain path traversal sequences")
	}
	if cfg.APIPort < 1 || cfg.APIPort > 65535 {
		return fmt.Errorf("api_port must be 1-65535")
	}
	if cfg.ConsolePort < 1 || cfg.ConsolePort > 65535 {
		return fmt.Errorf("console_port must be 1-65535")
	}
	if cfg.APIPort == cfg.ConsolePort {
		return fmt.Errorf("api_port and console_port must be different")
	}
	return nil
}

func generateMinioEnv(cfg MinioConfig) string {
	var sb strings.Builder
	sb.WriteString("# DPlaneOS MinIO - managed automatically, do not edit by hand\n")
	fmt.Fprintf(&sb, "MINIO_ROOT_USER=%s\n", cfg.RootUser)
	fmt.Fprintf(&sb, "MINIO_ROOT_PASSWORD=%s\n", cfg.RootPassword)
	fmt.Fprintf(&sb, "MINIO_VOLUMES=%s\n", cfg.VolumePath)
	fmt.Fprintf(&sb, "MINIO_OPTS=--address :%d --console-address :%d\n", cfg.APIPort, cfg.ConsolePort)
	return sb.String()
}

func minioServiceUnit() (string, error) {
	minio, err := systemd.ResolveCommand("minio")
	if err != nil {
		return "", fmt.Errorf("minio (services.dplaneos.s3.enable): %w", err)
	}
	return fmt.Sprintf(`[Unit]
Description=MinIO Object Storage
Documentation=https://docs.min.io
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=%s
ExecStart=%s server $MINIO_VOLUMES $MINIO_OPTS
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, minioEnvFile, minio), nil
}

func applyMinioConfig(cfg MinioConfig) error {
	// Ensure volume path exists
	if err := os.MkdirAll(cfg.VolumePath, 0755); err != nil {
		return fmt.Errorf("failed to create volume path: %w", err)
	}

	// Write env file
	if err := os.MkdirAll(filepath.Dir(minioEnvFile), 0700); err != nil {
		return fmt.Errorf("failed to create minio env dir: %w", err)
	}
	if err := os.WriteFile(minioEnvFile, []byte(generateMinioEnv(cfg)), 0640); err != nil {
		return fmt.Errorf("failed to write minio env: %w", err)
	}

	if IsNixOS() {
		unit, err := minioServiceUnit()
		if err != nil {
			return err
		}
		if err := systemd.InstallService(minioService, unit); err != nil {
			return fmt.Errorf("failed to install minio unit: %w", err)
		}
	} else if _, err := os.Stat(minioServiceFile); os.IsNotExist(err) {
		// Write service file if not already present
		serviceDir := filepath.Dir(minioServiceFile)
		if mkErr := os.MkdirAll(serviceDir, 0755); mkErr != nil {
			log.Printf("WARN: minio service dir: %v", mkErr)
		}
		unit, unitErr := minioServiceUnit()
		if unitErr != nil {
			return unitErr
		}
		if writeErr := os.WriteFile(minioServiceFile, []byte(unit), 0644); writeErr != nil {
			log.Printf("WARN: minio service file write: %v", writeErr)
		} else {
			// Daemon reload so systemd picks up the new unit
			cmdutil.RunFast("systemctl", "daemon-reload") //nolint
		}
	}

	// Restart if already running, otherwise just reload env
	if minioServiceActive() {
		_, err := cmdutil.RunFast("systemctl", "restart", minioService)
		if err != nil {
			return fmt.Errorf("failed to restart minio: %w", err)
		}
	}

	return nil
}

// GetMinioStatus returns the running state and config of MinIO.
// GET /api/s3/status
func GetMinioStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadMinioConfig()
	installed := minioInstalled()
	active := installed && minioServiceActive()

	respondOK(w, map[string]any{
		"success":      true,
		"installed":    installed,
		"active":       active,
		"api_port":     cfg.APIPort,
		"console_port": cfg.ConsolePort,
	})
}

// GetMinioConfig returns the current MinIO configuration (password redacted).
// GET /api/s3/config
func GetMinioConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadMinioConfig()
	if err != nil {
		respondErrorSimple(w, "Failed to load config", http.StatusInternalServerError)
		return
	}
	// Redact password in response
	out := map[string]any{
		"root_user":    cfg.RootUser,
		"root_password": func() string {
			if cfg.RootPassword != "" {
				return "••••••••"
			}
			return ""
		}(),
		"volume_path":  cfg.VolumePath,
		"api_port":     cfg.APIPort,
		"console_port": cfg.ConsolePort,
	}
	respondOK(w, map[string]any{"success": true, "config": out})
}

// UpdateMinioConfig saves config and applies it (restarts service if running).
// PUT /api/s3/config
func UpdateMinioConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootUser     string `json:"root_user"`
		RootPassword string `json:"root_password"`
		VolumePath   string `json:"volume_path"`
		APIPort      int    `json:"api_port"`
		ConsolePort  int    `json:"console_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var validationErr error
	var merged MinioConfig
	if err := atomicModifyMinioConfig(func(current MinioConfig) (MinioConfig, error) {
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
		if req.ConsolePort != 0 {
			current.ConsolePort = req.ConsolePort
		}
		if validationErr = validateMinioConfig(current); validationErr != nil {
			return MinioConfig{}, errMinioValidation
		}
		merged = current
		return current, nil
	}); err != nil {
		if errors.Is(err, errMinioValidation) {
			respondErrorSimple(w, validationErr.Error(), http.StatusBadRequest)
		} else {
			respondErrorSimple(w, "Failed to save config", http.StatusInternalServerError)
		}
		return
	}

	if err := applyMinioConfig(merged); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}

	audit.LogActivity("system", "minio_config_updated", map[string]any{"action": "MinIO configuration updated"})
	respondOK(w, map[string]any{"success": true, "message": "Configuration applied"})
}

// StartMinio starts the MinIO service.
// POST /api/s3/start
func StartMinio(w http.ResponseWriter, r *http.Request) {
	if !minioInstalled() {
		respondErrorSimple(w, "MinIO is not installed", http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadMinioConfig()
	if cfg.RootPassword == "" {
		respondErrorSimple(w, "Set a root password before starting MinIO", http.StatusBadRequest)
		return
	}
	if err := applyMinioConfig(cfg); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "start", minioService); err != nil {
		respondUserErrStatus(w, http.StatusInternalServerError, SanitizeServiceControl("MinIO", err), err)
		return
	}
	setMinioEnabled(true)
	audit.LogActivity("system", "minio_start", nil)
	respondOK(w, map[string]any{"success": true})
}

// StopMinio stops the MinIO service.
// POST /api/s3/stop
func StopMinio(w http.ResponseWriter, r *http.Request) {
	if _, err := cmdutil.RunFast("systemctl", "stop", minioService); err != nil {
		respondUserErrStatus(w, http.StatusInternalServerError, SanitizeServiceControl("MinIO", err), err)
		return
	}
	setMinioEnabled(false)
	audit.LogActivity("system", "minio_stop", nil)
	respondOK(w, map[string]any{"success": true})
}

// RestartMinio restarts the MinIO service.
// POST /api/s3/restart
func RestartMinio(w http.ResponseWriter, r *http.Request) {
	if !minioInstalled() {
		respondErrorSimple(w, "MinIO is not installed", http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadMinioConfig()
	if err := applyMinioConfig(cfg); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "restart", minioService); err != nil {
		respondUserErrStatus(w, http.StatusInternalServerError, SanitizeServiceControl("MinIO", err), err)
		return
	}
	audit.LogActivity("system", "minio_restart", nil)
	respondOK(w, map[string]any{"success": true})
}

func setMinioEnabled(on bool) {
	if err := atomicModifyMinioConfig(func(c MinioConfig) (MinioConfig, error) {
		c.Enabled = on
		return c, nil
	}); err != nil {
		log.Printf("WARN: minio enabled flag: %v", err)
	}
}

// RestoreMinio re-installs and starts MinIO at daemon start when it was
// running before (NixOS runtime units are cleared on reboot).
func RestoreMinio() {
	if !IsNixOS() || !minioInstalled() {
		return
	}
	cfg, err := loadMinioConfig()
	if err != nil || !cfg.Enabled {
		return
	}
	if err := applyMinioConfig(cfg); err != nil {
		log.Printf("WARN: restore minio: %v", err)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "start", minioService); err != nil {
		log.Printf("WARN: start minio: %v", err)
	}
}
