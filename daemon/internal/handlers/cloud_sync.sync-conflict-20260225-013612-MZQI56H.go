package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
)

// CloudSyncHandler manages rclone-based cloud storage remotes and sync jobs.
// Requires rclone to be installed: apt install rclone
type CloudSyncHandler struct{}

func NewCloudSyncHandler() *CloudSyncHandler { return &CloudSyncHandler{} }

const rcloneConfig = "/etc/dplaneos/rclone.conf"

func ensureConfigDir() error {
	return os.MkdirAll("/etc/dplaneos", 0700)
}

// runRclone executes rclone with our config file
func runRclone(args ...string) ([]byte, error) {
	a := append([]string{"--config", rcloneConfig}, args...)
	return cmdutil.RunFast("rclone", a...)
}

func runRcloneSlow(args ...string) ([]byte, error) {
	a := append([]string{"--config", rcloneConfig}, args...)
	return cmdutil.RunSlow("rclone", a...)
}

// providerDef describes a supported cloud storage provider
type providerDef struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Fields      []providerField `json:"fields"`
}

type providerField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Help        string `json:"help"`
	Type        string `json:"type"` // text, password, select
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder"`
}

var supportedProviders = []providerDef{
	{
		ID: "s3", Name: "Amazon S3 / S3-Compatible", Description: "Amazon S3 or any S3-compatible service (MinIO, Wasabi, Cloudflare R2)",
		Icon: "cloud",
		Fields: []providerField{
			{Key: "access_key_id", Label: "Access Key ID", Type: "text", Required: true, Placeholder: "AKIAIOSFODNN7EXAMPLE"},
			{Key: "secret_access_key", Label: "Secret Access Key", Type: "password", Required: true},
			{Key: "region", Label: "Region", Type: "text", Placeholder: "us-east-1"},
			{Key: "endpoint", Label: "Custom Endpoint", Type: "text", Help: "For S3-compatible services: https://s3.example.com"},
		},
	},
	{
		ID: "b2", Name: "Backblaze B2", Description: "Backblaze B2 Cloud Storage",
		Icon: "backup",
		Fields: []providerField{
			{Key: "account", Label: "Account ID", Type: "text", Required: true, Placeholder: "123456789abc"},
			{Key: "key", Label: "Application Key", Type: "password", Required: true},
		},
	},
	{
		ID: "azureblob", Name: "Azure Blob Storage", Description: "Microsoft Azure Blob Storage",
		Icon: "cloud_queue",
		Fields: []providerField{
			{Key: "account", Label: "Storage Account Name", Type: "text", Required: true},
			{Key: "key", Label: "Storage Account Key", Type: "password", Help: "Leave blank if using SAS URL"},
			{Key: "sas_url", Label: "SAS URL", Type: "text", Help: "Alternative to account key"},
		},
	},
	{
		ID: "sftp", Name: "SFTP / SSH", Description: "Connect to any server via SSH/SFTP",
		Icon: "terminal",
		Fields: []providerField{
			{Key: "host", Label: "Host", Type: "text", Required: true, Placeholder: "backup.example.com"},
			{Key: "user", Label: "Username", Type: "text", Required: true},
			{Key: "port", Label: "Port", Type: "text", Placeholder: "22"},
			{Key: "pass", Label: "Password", Type: "password", Help: "Leave blank if using SSH key"},
			{Key: "key_file", Label: "SSH Key Path", Type: "text", Help: "Absolute path to private key, e.g. /root/.ssh/id_rsa"},
		},
	},
	{
		ID: "ftp", Name: "FTP / FTPS", Description: "FTP or FTPS server",
		Icon: "lan",
		Fields: []providerField{
			{Key: "host", Label: "Host", Type: "text", Required: true, Placeholder: "ftp.example.com"},
			{Key: "user", Label: "Username", Type: "text"},
			{Key: "port", Label: "Port", Type: "text", Placeholder: "21"},
			{Key: "pass", Label: "Password", Type: "password"},
			{Key: "tls", Label: "Implicit TLS (FTPS)", Type: "text", Placeholder: "false"},
		},
	},
	{
		ID: "webdav", Name: "WebDAV", Description: "WebDAV: Nextcloud, Owncloud, Sharepoint, etc.",
		Icon: "http",
		Fields: []providerField{
			{Key: "url", Label: "URL", Type: "text", Required: true, Placeholder: "https://nextcloud.example.com/remote.php/dav/files/user/"},
			{Key: "vendor", Label: "Vendor", Type: "text", Placeholder: "nextcloud"},
			{Key: "user", Label: "Username", Type: "text"},
			{Key: "pass", Label: "Password", Type: "password"},
		},
	},
}

// validProviderIDs is a set for O(1) lookup
var validProviderIDs = func() map[string]bool {
	m := make(map[string]bool)
	for _, p := range supportedProviders {
		m[p.ID] = true
	}
	return m
}()

// validateRemoteName ensures safe alphanumeric-dash-underscore names
func validateRemoteName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

// validateConfigKey ensures rclone config keys are safe (lowercase + underscore only)
func validateConfigKey(k string) bool {
	for _, ch := range k {
		if !((ch >= 'a' && ch <= 'z') || ch == '_') {
			return false
		}
	}
	return k != ""
}

// HandleCloudSync is the main router for cloud sync operations
func (h *CloudSyncHandler) HandleCloudSync(w http.ResponseWriter, r *http.Request) {
	if err := ensureConfigDir(); err != nil {
		respondErrorSimple(w, "Failed to initialize config directory", http.StatusInternalServerError)
		return
	}

	action := r.URL.Query().Get("action")
	if action == "" && r.Method == http.MethodPost {
		// Peek at body to get action
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(body))
		var peek struct{ Action string `json:"action"` }
		json.Unmarshal(body, &peek)
		action = peek.Action
	}

	switch r.Method {
	case http.MethodGet:
		switch action {
		case "status":
			h.getStatus(w)
		case "providers":
			h.getProviders(w)
		case "remotes":
			h.listRemotes(w)
		case "jobs":
			h.listJobs(w)
		default:
			respondErrorSimple(w, "Unknown GET action: "+action, http.StatusBadRequest)
		}
	case http.MethodPost:
		switch action {
		case "configure":
			h.createRemote(w, r)
		case "test":
			h.testRemote(w, r)
		case "delete":
			h.deleteRemote(w, r)
		case "sync":
			h.runSync(w, r)
		default:
			respondErrorSimple(w, "Unknown POST action: "+action, http.StatusBadRequest)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *CloudSyncHandler) getStatus(w http.ResponseWriter) {
	_, err := exec.LookPath("rclone")
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success":          true,
			"rclone_available": false,
			"message":          "rclone not installed. Run: apt install rclone",
		})
		return
	}

	out, _ := runRclone("version")
	ver := strings.Split(strings.TrimSpace(string(out)), "\n")[0]

	remotes := h.getRcloneRemotes()
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":          true,
		"rclone_available": true,
		"version":          ver,
		"remote_count":     len(remotes),
		"config_path":      rcloneConfig,
	})
}

func (h *CloudSyncHandler) getProviders(w http.ResponseWriter) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"providers": supportedProviders,
	})
}

func (h *CloudSyncHandler) getRcloneRemotes() []string {
	out, err := runRclone("listremotes")
	if err != nil {
		return nil
	}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSuffix(strings.TrimSpace(line), ":")
		if name != "" {
			remotes = append(remotes, name)
		}
	}
	return remotes
}

type remoteInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (h *CloudSyncHandler) listRemotes(w http.ResponseWriter) {
	names := h.getRcloneRemotes()
	remotes := make([]remoteInfo, 0, len(names))

	for _, name := range names {
		// Parse type from config show
		typeOut, _ := runRclone("config", "show", name)
		remoteType := ""
		for _, line := range strings.Split(string(typeOut), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "type") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					remoteType = strings.TrimSpace(parts[1])
				}
			}
		}
		remotes = append(remotes, remoteInfo{Name: name, Type: remoteType})
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"remotes": remotes,
	})
}

func (h *CloudSyncHandler) listJobs(w http.ResponseWriter) {
	// Sync jobs are currently fire-and-forget (no job queue yet)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"jobs":    []interface{}{},
		"note":    "Sync runs synchronously; job history coming in a future release.",
	})
}

func (h *CloudSyncHandler) createRemote(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !validateRemoteName(req.Name) {
		respondErrorSimple(w, "Invalid remote name (letters, digits, hyphens, underscores only)", http.StatusBadRequest)
		return
	}
	if !validProviderIDs[req.Type] {
		respondErrorSimple(w, "Unsupported provider: "+req.Type, http.StatusBadRequest)
		return
	}

	// Build args: rclone config create <name> <type> key=value ...
	args := []string{"config", "create", req.Name, req.Type}
	for k, v := range req.Config {
		if !validateConfigKey(k) {
			respondErrorSimple(w, "Invalid config key: "+k, http.StatusBadRequest)
			return
		}
		args = append(args, k+"="+v)
	}

	out, err := runRclone(args...)
	if err != nil {
		log.Printf("CLOUD SYNC: config create failed: %v — %s", err, out)
		respondErrorSimple(w, "Failed to create remote: "+strings.TrimSpace(string(out)), http.StatusInternalServerError)
		return
	}

	audit.LogAction("cloud_sync", user, fmt.Sprintf("Created remote '%s' (type: %s)", req.Name, req.Type), true, 0)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Remote '%s' created", req.Name),
	})
}

func (h *CloudSyncHandler) testRemote(w http.ResponseWriter, r *http.Request) {
	// Support both body JSON and query param
	remoteName := r.URL.Query().Get("remote")
	if remoteName == "" {
		var req struct{ Remote string `json:"remote"` }
		json.NewDecoder(r.Body).Decode(&req)
		remoteName = req.Remote
	}

	if !validateRemoteName(remoteName) {
		respondErrorSimple(w, "Invalid remote name", http.StatusBadRequest)
		return
	}

	out, err := runRclone("lsd", "--max-depth", "1", remoteName+":")
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success":   true,
			"connected": false,
			"error":     strings.TrimSpace(string(out)),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"connected": true,
		"message":   "Connection successful",
	})
}

func (h *CloudSyncHandler) deleteRemote(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	remoteName := r.URL.Query().Get("remote")
	if remoteName == "" {
		var req struct{ Remote string `json:"remote"` }
		json.NewDecoder(r.Body).Decode(&req)
		remoteName = req.Remote
	}

	if !validateRemoteName(remoteName) {
		respondErrorSimple(w, "Invalid remote name", http.StatusBadRequest)
		return
	}

	out, err := runRclone("config", "delete", remoteName)
	if err != nil {
		respondErrorSimple(w, "Failed to delete remote: "+strings.TrimSpace(string(out)), http.StatusInternalServerError)
		return
	}

	audit.LogAction("cloud_sync", user, fmt.Sprintf("Deleted remote '%s'", remoteName), true, 0)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Remote '%s' deleted", remoteName),
	})
}

func (h *CloudSyncHandler) runSync(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req struct {
		Remote    string `json:"remote"`
		LocalPath string `json:"local_path"`
		Direction string `json:"direction"` // "upload" or "download"
		DryRun    bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !validateRemoteName(req.Remote) {
		respondErrorSimple(w, "Invalid remote name", http.StatusBadRequest)
		return
	}
	if req.LocalPath == "" {
		respondErrorSimple(w, "local_path is required", http.StatusBadRequest)
		return
	}
	// Restrict local paths to safe locations
	if !strings.HasPrefix(req.LocalPath, "/mnt/") &&
		!strings.HasPrefix(req.LocalPath, "/data/") &&
		!strings.HasPrefix(req.LocalPath, "/tank/") {
		respondErrorSimple(w, "local_path must be under /mnt/, /data/, or /tank/", http.StatusBadRequest)
		return
	}
	if strings.Contains(req.LocalPath, "..") {
		respondErrorSimple(w, "Path traversal not allowed", http.StatusBadRequest)
		return
	}

	var src, dst string
	if req.Direction == "download" {
		src = req.Remote + ":"
		dst = req.LocalPath
	} else {
		src = req.LocalPath
		dst = req.Remote + ":"
	}

	args := []string{"sync", src, dst, "--stats-one-line", "--stats", "5s"}
	if req.DryRun {
		args = append(args, "--dry-run")
	}

	out, err := runRcloneSlow(args...)
	audit.LogAction("cloud_sync", user, fmt.Sprintf("Sync %s ↔ %s (%s)", req.Remote, req.LocalPath, req.Direction), err == nil, 0)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   strings.TrimSpace(string(out)),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"output":  strings.TrimSpace(string(out)),
	})
}
