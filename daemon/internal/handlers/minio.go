package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
)

// ═══════════════════════════════════════════════════════════════
//  MinIO Object Storage Management
//  Manages MinIO via systemctl and writes /etc/minio.env.
// ═══════════════════════════════════════════════════════════════

const (
	minioConfigFile  = "minio-config.json"
	minioEnvFile     = "/etc/minio.env"
	minioServiceFile = "/etc/systemd/system/minio.service"
	minioService     = "minio"
)

var minioMu sync.RWMutex

type MinioConfig struct {
	RootUser     string `json:"root_user"`
	RootPassword string `json:"root_password"`
	VolumePath   string `json:"volume_path"`
	APIPort      int    `json:"api_port"`
	ConsolePort  int    `json:"console_port"`
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

func saveMinioConfig(cfg MinioConfig) error {
	minioMu.Lock()
	defer minioMu.Unlock()

	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(minioConfigFile), data, 0600)
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
		return fmt.Errorf("volume_path must not contain ..")
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
	sb.WriteString(fmt.Sprintf("MINIO_ROOT_USER=%s\n", cfg.RootUser))
	sb.WriteString(fmt.Sprintf("MINIO_ROOT_PASSWORD=%s\n", cfg.RootPassword))
	sb.WriteString(fmt.Sprintf("MINIO_VOLUMES=%s\n", cfg.VolumePath))
	sb.WriteString(fmt.Sprintf("MINIO_OPTS=--address :%d --console-address :%d\n", cfg.APIPort, cfg.ConsolePort))
	return sb.String()
}

const minioServiceUnit = `[Unit]
Description=MinIO Object Storage
Documentation=https://docs.min.io
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/minio.env
ExecStart=/run/current-system/sw/bin/minio server $MINIO_VOLUMES $MINIO_OPTS
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

func applyMinioConfig(cfg MinioConfig) error {
	// Ensure volume path exists
	if err := os.MkdirAll(cfg.VolumePath, 0755); err != nil {
		return fmt.Errorf("failed to create volume path: %w", err)
	}

	// Write env file
	if err := os.WriteFile(minioEnvFile, []byte(generateMinioEnv(cfg)), 0640); err != nil {
		return fmt.Errorf("failed to write minio env: %w", err)
	}

	// Write service file if not already present
	if _, err := os.Stat(minioServiceFile); os.IsNotExist(err) {
		serviceDir := filepath.Dir(minioServiceFile)
		if mkErr := os.MkdirAll(serviceDir, 0755); mkErr != nil {
			log.Printf("WARN: minio service dir: %v", mkErr)
		}
		if writeErr := os.WriteFile(minioServiceFile, []byte(minioServiceUnit), 0644); writeErr != nil {
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

	respondOK(w, map[string]interface{}{
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
	out := map[string]interface{}{
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
	respondOK(w, map[string]interface{}{"success": true, "config": out})
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

	current, _ := loadMinioConfig()

	// Apply only non-empty fields; keep existing password if placeholder sent
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

	if err := validateMinioConfig(current); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := saveMinioConfig(current); err != nil {
		respondErrorSimple(w, "Failed to save config", http.StatusInternalServerError)
		return
	}

	if err := applyMinioConfig(current); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}

	audit.LogActivity("system", "minio_config_updated", map[string]interface{}{"action": "MinIO configuration updated"})
	respondOK(w, map[string]interface{}{"success": true, "message": "Configuration applied"})
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
		respondErrorSimple(w, "Failed to start MinIO: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogActivity("system", "minio_start", nil)
	respondOK(w, map[string]interface{}{"success": true})
}

// StopMinio stops the MinIO service.
// POST /api/s3/stop
func StopMinio(w http.ResponseWriter, r *http.Request) {
	if _, err := cmdutil.RunFast("systemctl", "stop", minioService); err != nil {
		respondErrorSimple(w, "Failed to stop MinIO: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogActivity("system", "minio_stop", nil)
	respondOK(w, map[string]interface{}{"success": true})
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
		respondErrorSimple(w, "Failed to restart MinIO: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogActivity("system", "minio_restart", nil)
	respondOK(w, map[string]interface{}{"success": true})
}
