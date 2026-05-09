package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
)

// ═══════════════════════════════════════════════════════════════
//  FTP/FTPS Server Management
//  Manages vsftpd via systemctl and writes /etc/vsftpd.conf.
// ═══════════════════════════════════════════════════════════════

const (
	ftpConfigFile  = "ftp-config.json"
	vsftpdConf     = "/etc/vsftpd.conf"
	vsftpdUserList = "/etc/vsftpd.userlist"
	vsftpdService  = "vsftpd"
)

var ftpMu sync.RWMutex

type FTPConfig struct {
	Enabled         bool     `json:"enabled"`
	Mode            string   `json:"mode"`             // "ftp" | "ftps"
	Port            int      `json:"port"`
	PassiveMinPort  int      `json:"passive_min_port"`
	PassiveMaxPort  int      `json:"passive_max_port"`
	AllowAnonymous  bool     `json:"allow_anonymous"`
	AllowLocalUsers bool     `json:"allow_local_users"`
	WriteEnable     bool     `json:"write_enable"`
	ChrootLocalUser bool     `json:"chroot_local_user"`
	MaxClients      int      `json:"max_clients"`
	MaxPerIP        int      `json:"max_per_ip"`
	Banner          string   `json:"banner"`
	TLSCertPath     string   `json:"tls_cert_path"`
	TLSKeyPath      string   `json:"tls_key_path"`
	AllowedUsers    []string `json:"allowed_users"`
}

func defaultFTPConfig() FTPConfig {
	return FTPConfig{
		Enabled:         false,
		Mode:            "ftps",
		Port:            21,
		PassiveMinPort:  40000,
		PassiveMaxPort:  40100,
		AllowAnonymous:  false,
		AllowLocalUsers: true,
		WriteEnable:     true,
		ChrootLocalUser: true,
		MaxClients:      10,
		MaxPerIP:        3,
		Banner:          "D-PlaneOS FTP",
		TLSCertPath:     "/etc/ssl/certs/ssl-cert-snakeoil.pem",
		TLSKeyPath:      "/etc/ssl/private/ssl-cert-snakeoil.key",
		AllowedUsers:    []string{},
	}
}

func loadFTPConfig() (FTPConfig, error) {
	ftpMu.RLock()
	defer ftpMu.RUnlock()

	data, err := os.ReadFile(configPath(ftpConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return defaultFTPConfig(), nil
		}
		return FTPConfig{}, err
	}
	var cfg FTPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return FTPConfig{}, err
	}
	return cfg, nil
}

func saveFTPConfig(cfg FTPConfig) error {
	ftpMu.Lock()
	defer ftpMu.Unlock()

	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(ftpConfigFile), data, 0600)
}

// ftpInstalled returns true if vsftpd is available on the system.
func ftpInstalled() bool {
	_, err := cmdutil.RunFast("which", "vsftpd")
	return err == nil
}

// ftpServiceActive returns true if the vsftpd systemd unit is active.
func ftpServiceActive() bool {
	out, err := cmdutil.RunFast("systemctl", "is-active", vsftpdService)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// validBanner rejects control characters and newlines in the banner string.
var bannedBannerChars = regexp.MustCompile(`[\r\n\x00-\x1f]`)

// validateFTPConfig returns an error if any field is out of range or unsafe.
func validateFTPConfig(cfg FTPConfig) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("port must be 1-65535")
	}
	if cfg.PassiveMinPort < 1024 || cfg.PassiveMinPort > 65534 {
		return fmt.Errorf("passive_min_port must be 1024-65534")
	}
	if cfg.PassiveMaxPort <= cfg.PassiveMinPort || cfg.PassiveMaxPort > 65535 {
		return fmt.Errorf("passive_max_port must be greater than passive_min_port and at most 65535")
	}
	if cfg.MaxClients < 0 || cfg.MaxClients > 10000 {
		return fmt.Errorf("max_clients must be 0-10000")
	}
	if cfg.MaxPerIP < 0 || cfg.MaxPerIP > 1000 {
		return fmt.Errorf("max_per_ip must be 0-1000")
	}
	if cfg.Mode != "ftp" && cfg.Mode != "ftps" {
		return fmt.Errorf("mode must be ftp or ftps")
	}
	if bannedBannerChars.MatchString(cfg.Banner) {
		return fmt.Errorf("banner must not contain control characters or newlines")
	}
	if cfg.TLSCertPath != "" && !strings.HasPrefix(cfg.TLSCertPath, "/") {
		return fmt.Errorf("tls_cert_path must be an absolute path")
	}
	if cfg.TLSKeyPath != "" && !strings.HasPrefix(cfg.TLSKeyPath, "/") {
		return fmt.Errorf("tls_key_path must be an absolute path")
	}
	validUser := regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	for _, u := range cfg.AllowedUsers {
		if !validUser.MatchString(u) {
			return fmt.Errorf("invalid username: %q", u)
		}
	}
	return nil
}

func boolVal(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

// generateVsftpdConf builds the vsftpd.conf content from FTPConfig.
func generateVsftpdConf(cfg FTPConfig) string {
	var sb strings.Builder
	sb.WriteString("# D-PlaneOS FTP - managed automatically, do not edit by hand\n\n")

	sb.WriteString("listen=YES\n")
	sb.WriteString("listen_ipv6=NO\n")
	sb.WriteString(fmt.Sprintf("listen_port=%d\n", cfg.Port))
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("anonymous_enable=%s\n", boolVal(cfg.AllowAnonymous)))
	sb.WriteString(fmt.Sprintf("local_enable=%s\n", boolVal(cfg.AllowLocalUsers)))
	sb.WriteString(fmt.Sprintf("write_enable=%s\n", boolVal(cfg.WriteEnable)))
	sb.WriteString("local_umask=022\n")
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("chroot_local_user=%s\n", boolVal(cfg.ChrootLocalUser)))
	if cfg.ChrootLocalUser {
		sb.WriteString("allow_writeable_chroot=YES\n")
	}
	sb.WriteString("\n")

	sb.WriteString("dirmessage_enable=YES\n")
	sb.WriteString("use_localtime=YES\n")
	sb.WriteString("xferlog_enable=YES\n")
	sb.WriteString("connect_from_port_20=YES\n")
	sb.WriteString("xferlog_std_format=YES\n")
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("max_clients=%d\n", cfg.MaxClients))
	sb.WriteString(fmt.Sprintf("max_per_ip=%d\n", cfg.MaxPerIP))
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("pasv_min_port=%d\n", cfg.PassiveMinPort))
	sb.WriteString(fmt.Sprintf("pasv_max_port=%d\n", cfg.PassiveMaxPort))
	sb.WriteString("\n")

	banner := cfg.Banner
	if banner == "" {
		banner = "D-PlaneOS FTP"
	}
	sb.WriteString(fmt.Sprintf("ftpd_banner=%s\n", banner))
	sb.WriteString("\n")

	sb.WriteString("pam_service_name=vsftpd\n")
	sb.WriteString("secure_chroot_dir=/var/run/vsftpd/empty\n")
	sb.WriteString("\n")

	// User list: only explicitly listed users may connect
	sb.WriteString("userlist_enable=YES\n")
	sb.WriteString(fmt.Sprintf("userlist_file=%s\n", vsftpdUserList))
	sb.WriteString("userlist_deny=NO\n")
	sb.WriteString("\n")

	// TLS (FTPS)
	certPath := cfg.TLSCertPath
	keyPath := cfg.TLSKeyPath
	if certPath == "" {
		certPath = "/etc/ssl/certs/ssl-cert-snakeoil.pem"
	}
	if keyPath == "" {
		keyPath = "/etc/ssl/private/ssl-cert-snakeoil.key"
	}
	sb.WriteString(fmt.Sprintf("rsa_cert_file=%s\n", certPath))
	sb.WriteString(fmt.Sprintf("rsa_private_key_file=%s\n", keyPath))

	if cfg.Mode == "ftps" {
		sb.WriteString("ssl_enable=YES\n")
		sb.WriteString("force_local_data_ssl=YES\n")
		sb.WriteString("force_local_logins_ssl=YES\n")
		sb.WriteString("ssl_tlsv1=YES\n")
		sb.WriteString("ssl_sslv2=NO\n")
		sb.WriteString("ssl_sslv3=NO\n")
		sb.WriteString("require_ssl_reuse=NO\n")
		sb.WriteString("ssl_ciphers=HIGH\n")
	} else {
		sb.WriteString("ssl_enable=NO\n")
	}

	return sb.String()
}

// applyFTPConfig writes vsftpd.conf and the user list, then starts/stops the service.
func applyFTPConfig(cfg FTPConfig) error {
	// Write config file
	if err := os.WriteFile(vsftpdConf, []byte(generateVsftpdConf(cfg)), 0644); err != nil {
		return fmt.Errorf("write vsftpd.conf: %w", err)
	}

	// Write user list
	var userList strings.Builder
	for _, u := range cfg.AllowedUsers {
		userList.WriteString(u + "\n")
	}
	if err := os.WriteFile(vsftpdUserList, []byte(userList.String()), 0644); err != nil {
		return fmt.Errorf("write vsftpd.userlist: %w", err)
	}

	// Start, stop, or restart based on enabled state
	if cfg.Enabled {
		if ftpServiceActive() {
			_, err := cmdutil.RunFast("systemctl", "restart", vsftpdService)
			return err
		}
		_, err := cmdutil.RunFast("systemctl", "start", vsftpdService)
		return err
	}
	if ftpServiceActive() {
		_, err := cmdutil.RunFast("systemctl", "stop", vsftpdService)
		return err
	}
	return nil
}

// ─── HTTP Handlers ─────────────────────────────────────────────

// GetFTPStatus GET /api/ftp/status
func GetFTPStatus(w http.ResponseWriter, r *http.Request) {
	installed := ftpInstalled()
	active := false
	if installed {
		active = ftpServiceActive()
	}
	respondOK(w, map[string]interface{}{
		"success":   true,
		"installed": installed,
		"active":    active,
	})
}

// GetFTPConfig GET /api/ftp/config
func GetFTPConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadFTPConfig()
	if err != nil {
		respondErrorSimple(w, "Failed to load FTP config", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "config": cfg})
}

// UpdateFTPConfig PUT /api/ftp/config
func UpdateFTPConfig(w http.ResponseWriter, r *http.Request) {
	var cfg FTPConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if err := validateFTPConfig(cfg); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveFTPConfig(cfg); err != nil {
		respondErrorSimple(w, "Failed to save config", http.StatusInternalServerError)
		return
	}
	if !ftpInstalled() {
		respondOK(w, map[string]interface{}{
			"success": true,
			"warning": "vsftpd is not installed. Config saved but service not started.",
		})
		return
	}
	if err := applyFTPConfig(cfg); err != nil {
		log.Printf("applyFTPConfig: %v", err)
		respondOK(w, map[string]interface{}{
			"success": true,
			"warning": "Config saved but service apply failed: " + err.Error(),
		})
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "ftp_config_update", map[string]interface{}{
		"enabled": cfg.Enabled,
		"mode":    cfg.Mode,
	})
	respondOK(w, map[string]interface{}{"success": true})
}

// StartFTP POST /api/ftp/start
func StartFTP(w http.ResponseWriter, r *http.Request) {
	if !ftpInstalled() {
		respondErrorSimple(w, "vsftpd is not installed", http.StatusServiceUnavailable)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "start", vsftpdService); err != nil {
		respondErrorSimple(w, "Failed to start vsftpd: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "ftp_start", nil)
	respondOK(w, map[string]interface{}{"success": true, "active": true})
}

// StopFTP POST /api/ftp/stop
func StopFTP(w http.ResponseWriter, r *http.Request) {
	if !ftpInstalled() {
		respondErrorSimple(w, "vsftpd is not installed", http.StatusServiceUnavailable)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "stop", vsftpdService); err != nil {
		respondErrorSimple(w, "Failed to stop vsftpd: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "ftp_stop", nil)
	respondOK(w, map[string]interface{}{"success": true, "active": false})
}

// RestartFTP POST /api/ftp/restart
func RestartFTP(w http.ResponseWriter, r *http.Request) {
	if !ftpInstalled() {
		respondErrorSimple(w, "vsftpd is not installed", http.StatusServiceUnavailable)
		return
	}
	if _, err := cmdutil.RunFast("systemctl", "restart", vsftpdService); err != nil {
		respondErrorSimple(w, "Failed to restart vsftpd: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "ftp_restart", nil)
	respondOK(w, map[string]interface{}{"success": true, "active": true})
}
