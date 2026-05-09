package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// ═══════════════════════════════════════════════════════════════
//  SSH authorized_keys management
//
//  Manages SSH public keys in each system user's ~/.ssh/authorized_keys.
//  On the first mutation for a given user, any pre-existing keys in
//  their authorized_keys file are imported into our store automatically
//  so no manually-added keys are lost.
//
//  SSH daemon settings (port, password auth) are intentionally NOT
//  managed here - they are NixOS-module configuration and must stay
//  under NixOS/GitOps control.
// ═══════════════════════════════════════════════════════════════

const sshKeysFile = "ssh-keys.json"

var sshKeysMu sync.RWMutex

// validSSHKeyTypes is the set of key type prefixes accepted in public key lines.
var validSSHKeyTypes = map[string]bool{
	"ssh-rsa":                            true,
	"ssh-dss":                            true,
	"ssh-ed25519":                        true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

// SSHManagedKey is the stored representation of an authorized public key.
type SSHManagedKey struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Label     string    `json:"label"`
	PublicKey string    `json:"public_key"`
	KeyType   string    `json:"key_type"`
	Comment   string    `json:"comment"`
	AddedBy   string    `json:"added_by"`
	AddedAt   time.Time `json:"added_at"`
	Imported  bool      `json:"imported"` // true if auto-imported from a pre-existing file
}

// parseSSHPublicKey extracts the key type, base64 blob, and comment from a
// public key line. Returns an error if the format is not a valid SSH public key.
func parseSSHPublicKey(raw string) (keyType, blob, comment string, err error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Fields(raw)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("public key must have at least two fields (type and key data)")
	}
	keyType = parts[0]
	if !validSSHKeyTypes[keyType] {
		return "", "", "", fmt.Errorf("unsupported key type %q; accepted: rsa, ed25519, ecdsa", keyType)
	}
	blob = parts[1]
	if len(parts) >= 3 {
		comment = strings.Join(parts[2:], " ")
	}
	return keyType, blob, comment, nil
}

// userHomeDir returns the home directory for a system user.
func userHomeDir(username string) (string, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return "", fmt.Errorf("user %q not found: %w", username, err)
	}
	return u.HomeDir, nil
}

// authorizedKeysPath returns the path to the user's authorized_keys file.
func authorizedKeysPath(username string) (string, error) {
	home, err := userHomeDir(username)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), nil
}

// writeAuthorizedKeys regenerates the authorized_keys file for a user
// from the current state of the managed store. Creates ~/.ssh/ if needed.
func writeAuthorizedKeys(username string, keys []SSHManagedKey) error {
	home, err := userHomeDir(username)
	if err != nil {
		return err
	}

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("create .ssh dir: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# D-PlaneOS managed authorized_keys - changes will be overwritten\n")
	for _, k := range keys {
		if k.Username != username {
			continue
		}
		if k.Label != "" {
			sb.WriteString(fmt.Sprintf("# %s\n", k.Label))
		}
		sb.WriteString(k.PublicKey + "\n")
	}

	akPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(akPath, []byte(sb.String()), 0600); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	// Ensure correct ownership - the daemon runs as root so this is safe
	if _, err := cmdutil.RunFast("chown", "-R", username+":"+username, sshDir); err != nil {
		log.Printf("ssh_keys: chown %s: %v", sshDir, err)
	}

	return nil
}

// importExistingKeys reads a user's current authorized_keys file (if any)
// and appends any keys not already in our store. Returns the updated store.
func importExistingKeys(username string, existing []SSHManagedKey) []SSHManagedKey {
	akPath, err := authorizedKeysPath(username)
	if err != nil {
		return existing
	}

	f, err := os.Open(akPath)
	if err != nil {
		return existing // file doesn't exist yet - nothing to import
	}
	defer f.Close()

	// Build a set of blobs already in the store for this user
	known := make(map[string]bool)
	for _, k := range existing {
		if k.Username == username {
			parts := strings.Fields(k.PublicKey)
			if len(parts) >= 2 {
				known[parts[1]] = true
			}
		}
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kt, blob, comment, err := parseSSHPublicKey(line)
		if err != nil {
			continue
		}
		if known[blob] {
			continue
		}
		existing = append(existing, SSHManagedKey{
			ID:        uuid.New().String(),
			Username:  username,
			Label:     comment,
			PublicKey: line,
			KeyType:   kt,
			Comment:   comment,
			AddedBy:   "import",
			AddedAt:   time.Now(),
			Imported:  true,
		})
		known[blob] = true
	}

	return existing
}

// loadSSHKeys reads the stored key list.
func loadSSHKeys() ([]SSHManagedKey, error) {
	sshKeysMu.RLock()
	defer sshKeysMu.RUnlock()

	data, err := os.ReadFile(configPath(sshKeysFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []SSHManagedKey{}, nil
		}
		return nil, err
	}
	var out []SSHManagedKey
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func saveSSHKeys(keys []SSHManagedKey) error {
	sshKeysMu.Lock()
	defer sshKeysMu.Unlock()

	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(sshKeysFile), data, 0600)
}

// ─── HTTP Handlers ─────────────────────────────────────────────

// GetSSHStatus GET /api/ssh/status
// Returns whether the SSH daemon is running and which port it listens on.
func GetSSHStatus(w http.ResponseWriter, r *http.Request) {
	out, err := cmdutil.RunFast("systemctl", "is-active", "sshd")
	active := err == nil && strings.TrimSpace(string(out)) == "active"

	port := ""
	if active {
		ssOut, ssErr := cmdutil.RunFast("ss", "-tlnp")
		if ssErr == nil {
			for _, line := range strings.Split(string(ssOut), "\n") {
				if strings.Contains(line, "sshd") {
					fields := strings.Fields(line)
					if len(fields) >= 4 {
						addr := fields[3]
						if idx := strings.LastIndex(addr, ":"); idx != -1 {
							port = addr[idx+1:]
						}
					}
					break
				}
			}
		}
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"active":  active,
		"port":    port,
	})
}

// ListSSHKeys GET /api/ssh/keys[?username=foo]
// Returns all managed keys, optionally filtered by username.
func ListSSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := loadSSHKeys()
	if err != nil {
		respondErrorSimple(w, "Failed to load SSH keys", http.StatusInternalServerError)
		return
	}

	if u := r.URL.Query().Get("username"); u != "" {
		filtered := keys[:0]
		for _, k := range keys {
			if k.Username == u {
				filtered = append(filtered, k)
			}
		}
		keys = filtered
	}

	if keys == nil {
		keys = []SSHManagedKey{}
	}
	respondOK(w, map[string]interface{}{"success": true, "keys": keys})
}

// AddSSHKey POST /api/ssh/keys
// Body: { username, label, public_key }
func AddSSHKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username  string `json:"username"`
		Label     string `json:"label"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	req.PublicKey = strings.TrimSpace(req.PublicKey)

	if !isValidSSHUser(req.Username) {
		respondErrorSimple(w, "Invalid username", http.StatusBadRequest)
		return
	}
	kt, blob, comment, err := parseSSHPublicKey(req.PublicKey)
	if err != nil {
		respondErrorSimple(w, "Invalid SSH public key: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verify system user exists
	if _, err := userHomeDir(req.Username); err != nil {
		respondErrorSimple(w, "User not found on system: "+req.Username, http.StatusBadRequest)
		return
	}

	keys, err := loadSSHKeys()
	if err != nil {
		respondErrorSimple(w, "Failed to load SSH keys", http.StatusInternalServerError)
		return
	}

	// Import pre-existing authorized_keys on first touch for this user
	touched := false
	for _, k := range keys {
		if k.Username == req.Username {
			touched = true
			break
		}
	}
	if !touched {
		keys = importExistingKeys(req.Username, keys)
	}

	// Reject duplicate key for this user
	for _, k := range keys {
		if k.Username == req.Username {
			parts := strings.Fields(k.PublicKey)
			if len(parts) >= 2 && parts[1] == blob {
				respondErrorSimple(w, "This public key is already authorized for "+req.Username, http.StatusConflict)
				return
			}
		}
	}

	label := req.Label
	if label == "" {
		label = comment
	}

	key := SSHManagedKey{
		ID:        uuid.New().String(),
		Username:  req.Username,
		Label:     label,
		PublicKey: req.PublicKey,
		KeyType:   kt,
		Comment:   comment,
		AddedBy:   r.Header.Get("X-User"),
		AddedAt:   time.Now(),
	}
	keys = append(keys, key)

	if err := saveSSHKeys(keys); err != nil {
		respondErrorSimple(w, "Failed to save SSH keys", http.StatusInternalServerError)
		return
	}
	if err := writeAuthorizedKeys(req.Username, keys); err != nil {
		log.Printf("ssh_keys: writeAuthorizedKeys for %s: %v", req.Username, err)
		respondOK(w, map[string]interface{}{
			"success": true,
			"key":     key,
			"warning": "Key saved but authorized_keys write failed: " + err.Error(),
		})
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "ssh_key_add", map[string]interface{}{
		"id": key.ID, "username": req.Username, "key_type": kt,
	})
	respondOK(w, map[string]interface{}{"success": true, "key": key})
}

// DeleteSSHKey DELETE /api/ssh/keys/{id}
func DeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	keys, err := loadSSHKeys()
	if err != nil {
		respondErrorSimple(w, "Failed to load SSH keys", http.StatusInternalServerError)
		return
	}

	username := ""
	filtered := keys[:0]
	for _, k := range keys {
		if k.ID == id {
			username = k.Username
			continue
		}
		filtered = append(filtered, k)
	}
	if username == "" {
		respondErrorSimple(w, "Key not found", http.StatusNotFound)
		return
	}

	if err := saveSSHKeys(filtered); err != nil {
		respondErrorSimple(w, "Failed to save SSH keys", http.StatusInternalServerError)
		return
	}
	if err := writeAuthorizedKeys(username, filtered); err != nil {
		log.Printf("ssh_keys: writeAuthorizedKeys for %s after delete: %v", username, err)
	}

	audit.LogActivity(r.Header.Get("X-User"), "ssh_key_delete", map[string]interface{}{"id": id, "username": username})
	respondOK(w, map[string]interface{}{"success": true})
}
