package handlers

import (
	"fmt"
	"net/http"
	"os"
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
// Body: {} (no parameters - path and algorithm are fixed)
// Response: { "success": true, "public_key": "ssh-ed25519 ..." }
//
// Safe to call repeatedly - regenerates the key each time. If the key is regenerated,
// the old public key is invalidated on the remote and ssh-copy-id must be re-run.
func GenerateReplicationKey(w http.ResponseWriter, r *http.Request) {
	if err := os.MkdirAll(replKeyDir, 0700); err != nil {
		respondErrorSimple(w, "Failed to create .ssh directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove existing key before generating - ssh-keygen refuses to overwrite
	_ = os.Remove(replKeyPath)
	_ = os.Remove(replPubPath)

	comment := fmt.Sprintf("dplaneos-replication@%s", replHostname())

	_, err := executeCommandWithTimeout(
		10*time.Second,
		"ssh-keygen",
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

	// Invalidate all peer authorizations - the old key is no longer installed on any host.
	resetAllRemotesKeyState()

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

func replHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "dplaneos"
	}
	return h
}

