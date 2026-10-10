package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"log"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/nixwriter"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// ═══════════════════════════════════════════════════════════════
//  SSH authorized_keys management + SSH daemon settings
//
//  Manages SSH public keys in each system user's ~/.ssh/authorized_keys.
//  On the first mutation for a given user, any pre-existing keys in
//  their authorized_keys file are imported into our store automatically
//  so no manually-added keys are lost.
//
//  SSH daemon settings (port, password auth, permit root login) are
//  managed via GET/POST /api/system/ssh-daemon, which writes to the
//  nixwriter state. Settings take effect on the next nixos-rebuild.
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
//
// The line is written into authorized_keys as is, so it must be exactly one
// key: one line, no options (command=, from=, ...), a key blob that really
// decodes to a key of the stated type.
func parseSSHPublicKey(raw string) (keyType, blob, comment string, err error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\r\n\x00") {
		return "", "", "", fmt.Errorf("public key must be a single line")
	}
	parts := strings.Fields(raw)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("public key must have at least two fields (type and key data)")
	}
	keyType = parts[0]
	if !validSSHKeyTypes[keyType] {
		return "", "", "", fmt.Errorf("unsupported key type %q; accepted: rsa, ed25519, ecdsa", keyType)
	}
	pk, _, options, _, perr := ssh.ParseAuthorizedKey([]byte(raw))
	if perr != nil {
		return "", "", "", fmt.Errorf("key data does not decode: %v", perr)
	}
	if len(options) > 0 {
		return "", "", "", fmt.Errorf("key options are not accepted")
	}
	if pk.Type() != keyType {
		return "", "", "", fmt.Errorf("key data is a %s key, not %s", pk.Type(), keyType)
	}
	blob = parts[1]
	if len(parts) >= 3 {
		comment = strings.Join(parts[2:], " ")
	}
	return keyType, blob, comment, nil
}

// sanitizeKeyLabel makes a label safe for a "# label" comment line: no line
// breaks (a newline would start a new authorized_keys entry) or controls.
func sanitizeKeyLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, label)
	if len(label) > 128 {
		label = label[:128]
	}
	return strings.TrimSpace(label)
}

// sshLookupUser resolves a system user (a variable so tests can use a
// temporary home directory).
var sshLookupUser = user.Lookup

// userHomeDir returns the home directory for a system user.
func userHomeDir(username string) (string, error) {
	u, err := sshLookupUser(username)
	if err != nil {
		return "", fmt.Errorf("user %q not found: %w", username, err)
	}
	return u.HomeDir, nil
}

// writeAuthorizedKeys regenerates the authorized_keys file for a user
// from the current state of the managed store. Creates ~/.ssh/ if needed.
func writeAuthorizedKeys(username string, keys []SSHManagedKey) error {
	u, err := sshLookupUser(username)
	if err != nil {
		return fmt.Errorf("user %q not found: %w", username, err)
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("user %q has no numeric uid/gid", username)
	}

	var sb strings.Builder
	sb.WriteString("# DPlaneOS managed authorized_keys - changes will be overwritten\n")
	for _, k := range keys {
		if k.Username != username {
			continue
		}
		// Stored keys predate the stricter parser: re-check every line.
		if _, _, _, err := parseSSHPublicKey(k.PublicKey); err != nil {
			log.Printf("ssh_keys: skipping invalid stored key %s for %s: %v", k.ID, username, err)
			continue
		}
		if label := sanitizeKeyLabel(k.Label); label != "" {
			fmt.Fprintf(&sb, "# %s\n", label)
		}
		sb.WriteString(k.PublicKey + "\n")
	}

	// Written as root into a directory the user controls: no symlinks are
	// followed, the file is replaced atomically and owned by the user.
	return writeUserSSHFile(u.HomeDir, uid, gid, "authorized_keys", []byte(sb.String()))
}

// importExistingKeys reads a user's current authorized_keys file (if any)
// and appends any keys not already in our store. Returns the updated store.
func importExistingKeys(username string, existing []SSHManagedKey) []SSHManagedKey {
	home, err := userHomeDir(username)
	if err != nil {
		return existing
	}
	data, err := readUserSSHFile(home, "authorized_keys")
	if err != nil {
		log.Printf("ssh_keys: not importing keys of %s: %v", username, err)
		return existing
	}
	if len(data) == 0 {
		return existing // file doesn't exist yet - nothing to import
	}

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

	scanner := bufio.NewScanner(bytes.NewReader(data))
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

var (
	errSSHKeyNotFound  = errors.New("ssh key not found")
	errSSHKeyDuplicate = errors.New("ssh key duplicate")
)

// atomicModifySSHKeys holds the write lock across the full load-modify-save cycle.
func atomicModifySSHKeys(fn func([]SSHManagedKey) ([]SSHManagedKey, error)) error {
	sshKeysMu.Lock()
	defer sshKeysMu.Unlock()

	data, err := os.ReadFile(configPath(sshKeysFile))
	var keys []SSHManagedKey
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := json.Unmarshal(data, &keys); err != nil {
			return err
		}
	}
	modified, err := fn(keys)
	if err != nil {
		return err
	}
	os.MkdirAll(ConfigDir, 0755)
	out, err := json.MarshalIndent(modified, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(sshKeysFile), out, 0600)
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
			for line := range strings.SplitSeq(string(ssOut), "\n") {
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

	respondOK(w, map[string]any{
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
	respondOK(w, map[string]any{"success": true, "keys": keys})
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

	label := sanitizeKeyLabel(req.Label)
	if label == "" {
		label = sanitizeKeyLabel(comment)
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

	var finalKeys []SSHManagedKey
	if err := atomicModifySSHKeys(func(all []SSHManagedKey) ([]SSHManagedKey, error) {
		// Import pre-existing authorized_keys on first touch for this user.
		touched := false
		for _, k := range all {
			if k.Username == req.Username {
				touched = true
				break
			}
		}
		if !touched {
			all = importExistingKeys(req.Username, all)
		}
		// Reject duplicate key for this user.
		for _, k := range all {
			if k.Username == req.Username {
				parts := strings.Fields(k.PublicKey)
				if len(parts) >= 2 && parts[1] == blob {
					return nil, errSSHKeyDuplicate
				}
			}
		}
		all = append(all, key)
		finalKeys = all
		return all, nil
	}); err != nil {
		if errors.Is(err, errSSHKeyDuplicate) {
			respondErrorSimple(w, "This public key is already authorized for "+req.Username, http.StatusConflict)
		} else {
			respondErrorSimple(w, "Failed to save SSH keys", http.StatusInternalServerError)
		}
		return
	}

	if err := writeAuthorizedKeys(req.Username, finalKeys); err != nil {
		log.Printf("ssh_keys: writeAuthorizedKeys for %s: %v", req.Username, err)
		respondOK(w, map[string]any{
			"success": true,
			"key":     key,
			"warning": "Key saved but authorized_keys write failed: " + err.Error(),
		})
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "ssh_key_add", map[string]any{
		"id": key.ID, "username": req.Username, "key_type": kt,
	})
	respondOK(w, map[string]any{"success": true, "key": key})
}

// DeleteSSHKey DELETE /api/ssh/keys/{id}
func DeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	username := ""
	if err := atomicModifySSHKeys(func(all []SSHManagedKey) ([]SSHManagedKey, error) {
		out := all[:0]
		for _, k := range all {
			if k.ID == id {
				username = k.Username
				continue
			}
			out = append(out, k)
		}
		if username == "" {
			return nil, errSSHKeyNotFound
		}
		return out, nil
	}); err != nil {
		if errors.Is(err, errSSHKeyNotFound) {
			respondErrorSimple(w, "Key not found", http.StatusNotFound)
		} else {
			respondErrorSimple(w, "Failed to save SSH keys", http.StatusInternalServerError)
		}
		return
	}

	// Re-read the saved state to pass consistent key list to writeAuthorizedKeys.
	finalKeys, _ := loadSSHKeys()
	if err := writeAuthorizedKeys(username, finalKeys); err != nil {
		log.Printf("ssh_keys: writeAuthorizedKeys for %s after delete: %v", username, err)
	}

	audit.LogActivity(r.Header.Get("X-User"), "ssh_key_delete", map[string]any{"id": id, "username": username})
	respondOK(w, map[string]any{"success": true})
}

// ─── SSH Daemon Settings ────────────────────────────────────────

var validPermitRootLoginValues = map[string]bool{
	"yes":                  true,
	"no":                   true,
	"prohibit-password":    true,
	"forced-commands-only": true,
}

// GetSSHDaemon GET /api/system/ssh-daemon
// Returns current SSH daemon settings from the nixwriter state.
func GetSSHDaemon(w http.ResponseWriter, r *http.Request) {
	nw := nixwriter.Default()
	if err := nw.LoadFromDisk(); err != nil {
		log.Printf("ssh_daemon: load nixwriter state: %v", err)
	}
	state := nw.State()

	var passwordAuth *bool
	if state.SSHPasswordAuth != nil {
		v := *state.SSHPasswordAuth
		passwordAuth = &v
	}

	respondOK(w, map[string]any{
		"success":           true,
		"port":              state.SSHPort,
		"password_auth":     passwordAuth,
		"permit_root_login": state.SSHPermitRootLogin,
	})
}

// PostSSHDaemon POST /api/system/ssh-daemon
// Body: { port?: int, password_auth?: bool|null, permit_root_login?: string }
// Writes settings to nixwriter state. Changes take effect on next nixos-rebuild.
func PostSSHDaemon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port            *int    `json:"port"`
		PasswordAuth    *bool   `json:"password_auth"`
		PermitRootLogin *string `json:"permit_root_login"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	port := 0
	if req.Port != nil {
		port = *req.Port
		if port < 1 || port > 65535 {
			respondErrorSimple(w, fmt.Sprintf("Port %d out of range 1-65535", port), http.StatusBadRequest)
			return
		}
	}

	permitRootLogin := ""
	if req.PermitRootLogin != nil {
		permitRootLogin = *req.PermitRootLogin
		if permitRootLogin != "" && !validPermitRootLoginValues[permitRootLogin] {
			respondErrorSimple(w, fmt.Sprintf("Invalid permit_root_login %q; valid: yes, no, prohibit-password, forced-commands-only", permitRootLogin), http.StatusBadRequest)
			return
		}
	}

	nw := nixwriter.Default()
	if err := nw.LoadFromDisk(); err != nil {
		log.Printf("ssh_daemon: load nixwriter state: %v", err)
	}

	if err := nw.SetSSHDaemon(nixwriter.SSHDaemonOpts{
		Port:            port,
		PasswordAuth:    req.PasswordAuth,
		PermitRootLogin: permitRootLogin,
	}); err != nil {
		respondErrorSimple(w, "Failed to save SSH daemon settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "ssh_daemon_update", map[string]any{
		"port":              port,
		"password_auth":     req.PasswordAuth,
		"permit_root_login": permitRootLogin,
	})
	respondOK(w, map[string]any{
		"success": true,
		"message": "SSH daemon settings saved. Apply NixOS configuration to activate.",
	})
}
