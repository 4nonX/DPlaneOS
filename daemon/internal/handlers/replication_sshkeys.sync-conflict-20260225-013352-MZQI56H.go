package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SSH key used exclusively for ZFS replication.
// Stored separately from any user keys to limit blast radius.
const (
	replKeyDir  = "/root/.ssh"
	replKeyPath = "/root/.ssh/dplaneos_replication"
	replPubPath = "/root/.ssh/dplaneos_replication.pub"
)

// GenerateReplicationKey generates a new ed25519 key pair at the replication key path.
// POST /api/replication/ssh-keygen
// Body: {} (no parameters — path and algorithm are fixed)
// Response: { "success": true, "public_key": "ssh-ed25519 ..." }
//
// Safe to call repeatedly — regenerates the key each time. If the key is regenerated,
// the old public key is invalidated on the remote and ssh-copy-id must be re-run.
func GenerateReplicationKey(w http.ResponseWriter, r *http.Request) {
	if err := os.MkdirAll(replKeyDir, 0700); err != nil {
		respondErrorSimple(w, "Failed to create .ssh directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove existing key before generating — ssh-keygen refuses to overwrite
	_ = os.Remove(replKeyPath)
	_ = os.Remove(replPubPath)

	comment := fmt.Sprintf("dplaneos-replication@%s", replHostname())

	_, err := executeCommandWithTimeout(
		10*time.Second,
		"/usr/bin/ssh-keygen",
		[]string{"-t", "ed25519", "-f", replKeyPath, "-N", "", "-C", comment},
	)
	if err != nil {
		respondErrorSimple(w, "ssh-keygen failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	pubKey, err := os.ReadFile(replPubPath)
	if err != nil {
		respondErrorSimple(w, "Key generated but could not read public key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"public_key": strings.TrimSpace(string(pubKey)),
		"key_path":   replKeyPath,
	})
}

// GetReplicationPubKey returns the current replication public key.
// GET /api/replication/ssh-pubkey
// Response: { "success": true, "exists": true, "public_key": "ssh-ed25519 ..." }
//            { "success": true, "exists": false }
func GetReplicationPubKey(w http.ResponseWriter, r *http.Request) {
	pubKey, err := os.ReadFile(replPubPath)
	if err != nil {
		if os.IsNotExist(err) {
			respondJSON(w, http.StatusOK, map[string]interface{}{
				"success": true,
				"exists":  false,
			})
			return
		}
		respondErrorSimple(w, "Failed to read public key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"exists":     true,
		"public_key": strings.TrimSpace(string(pubKey)),
		"key_path":   replKeyPath,
	})
}

// CopyReplicationKey pushes the replication public key to a remote host's
// authorized_keys using ssh-copy-id with sshpass for one-time password auth.
// After this succeeds, password auth is no longer needed for replication.
//
// POST /api/replication/ssh-copy-id
// Body: { "remote_host": "...", "remote_user": "root", "remote_port": 22, "password": "..." }
// Response: { "success": true, "message": "..." }
func CopyReplicationKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoteHost string `json:"remote_host"`
		RemoteUser string `json:"remote_user"`
		RemotePort int    `json:"remote_port"`
		Password   string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate inputs — same rules as replication_remote.go
	if req.RemoteHost == "" || len(req.RemoteHost) > 253 {
		respondErrorSimple(w, "Invalid remote host", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.RemoteHost, ";|&$`\\\"'") {
		respondErrorSimple(w, "Invalid characters in remote host", http.StatusBadRequest)
		return
	}
	if req.RemoteUser == "" {
		req.RemoteUser = "root"
	}
	if !isValidSSHUser(req.RemoteUser) {
		respondErrorSimple(w, "Invalid remote user", http.StatusBadRequest)
		return
	}
	if req.RemotePort == 0 {
		req.RemotePort = 22
	}
	if req.RemotePort < 1 || req.RemotePort > 65535 {
		respondErrorSimple(w, "Invalid port", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		respondErrorSimple(w, "Password required for initial key installation", http.StatusBadRequest)
		return
	}

	// Must have a key to copy
	if _, err := os.Stat(replPubPath); os.IsNotExist(err) {
		respondErrorSimple(w, "No replication key found — generate one first", http.StatusBadRequest)
		return
	}

	// sshpass must be installed
	sshpassPath, err := exec.LookPath("sshpass")
	if err != nil {
		respondErrorSimple(w,
			"sshpass is required for password-based key installation. Install with: apt install sshpass",
			http.StatusInternalServerError,
		)
		return
	}

	// Build command: sshpass -e ssh-copy-id -i <pubkey> -o StrictHostKeyChecking=accept-new -p <port> user@host
	// Password is passed via SSHPASS env var (not CLI arg) to keep it out of /proc/*/cmdline.
	target := fmt.Sprintf("%s@%s", req.RemoteUser, req.RemoteHost)
	args := []string{
		"-e", // read password from SSHPASS env var
		"/usr/bin/ssh-copy-id",
		"-i", replPubPath,
		"-o", "StrictHostKeyChecking=accept-new",
		"-p", fmt.Sprintf("%d", req.RemotePort),
		target,
	}

	out, execErr := replExecWithEnv(sshpassPath, args, map[string]string{
		"SSHPASS": req.Password,
	})
	if execErr != nil {
		respondErrorSimple(w,
			"Key installation failed — check host, user, port, and password. Details: "+sanitiseSSHOutput(out),
			http.StatusBadRequest,
		)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Replication key installed on %s. Password authentication is no longer required for replication to this host.", target),
	})
}

// replExecWithEnv runs a command inheriting the current environment plus any
// additional variables. Uses the same exec.CommandContext pattern as git_sync.go.
func replExecWithEnv(path string, args []string, extra map[string]string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("binary path must be absolute")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), envPairs(extra)...)

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// envPairs converts a map to "KEY=VALUE" strings for cmd.Env.
func envPairs(m map[string]string) []string {
	pairs := make([]string, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, k+"="+v)
	}
	return pairs
}

// sanitiseSSHOutput strips lines that may contain credential material
// before returning error output to the client.
func sanitiseSSHOutput(out string) string {
	lines := strings.Split(out, "\n")
	safe := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "password") || strings.Contains(lower, "sshpass") {
			continue
		}
		safe = append(safe, strings.TrimSpace(line))
	}
	result := strings.TrimSpace(strings.Join(safe, " "))
	if result == "" {
		return "authentication failed or host unreachable"
	}
	return result
}

func replHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "dplaneos"
	}
	return h
}
