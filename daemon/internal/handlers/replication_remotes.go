package handlers

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"dplaned/internal/gitops"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"golang.org/x/crypto/ssh"
)

// Remote is a replication target peer. One Remote can be referenced by many
// ReplicationSchedules, decoupling connection details from task configuration.
type Remote struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Host         string    `json:"host"`
	User         string    `json:"user"`
	Port         int       `json:"port"`
	Fingerprint  string    `json:"fingerprint"`         // SHA256:<base64> - SSH host key, pinned after first connect
	HostKey      string    `json:"host_key,omitempty"`  // authorized_keys format, used for known_hosts pinning by ssh binary
	KeyInstalled bool      `json:"key_installed"`       // true once authorize has succeeded
	LastTested   time.Time `json:"last_tested,omitempty"`
	TestOK       bool      `json:"test_ok"`
	CreatedAt    time.Time `json:"created_at"`
}

// replStateMu guards both the remotes file and the schedules file as a single
// unit. Using one mutex for both stores eliminates lock-ordering deadlocks that
// would arise from holding one lock while reading the other (e.g. the cross-file
// check in HandleDeleteRemote), and makes cross-store invariants easy to enforce
// inside a single atomicModify callback.
var replStateMu sync.RWMutex

const replRemotesFile = "replication-remotes.json"

func loadRemotes() ([]Remote, error) {
	replStateMu.RLock()
	defer replStateMu.RUnlock()
	data, err := os.ReadFile(configPath(replRemotesFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []Remote{}, nil
		}
		return nil, err
	}
	var remotes []Remote
	if err := json.Unmarshal(data, &remotes); err != nil {
		return nil, err
	}
	return remotes, nil
}

func saveRemotes(remotes []Remote) error {
	replStateMu.Lock()
	defer replStateMu.Unlock()
	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(remotes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(replRemotesFile), data, 0644)
}

var errRemoteNotFound = errors.New("remote not found")
var errRemoteInUse    = errors.New("peer is referenced by a schedule")

// atomicModifyRemotes holds replStateMu exclusively across the full
// load-modify-save cycle. Because replStateMu also guards the schedules file,
// callbacks may safely read the schedules file directly (without going through
// loadReplicationSchedules) to enforce cross-store invariants.
func atomicModifyRemotes(fn func([]Remote) ([]Remote, error)) error {
	replStateMu.Lock()
	defer replStateMu.Unlock()

	data, err := os.ReadFile(configPath(replRemotesFile))
	var remotes []Remote
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := json.Unmarshal(data, &remotes); err != nil {
			return err
		}
	}
	modified, err := fn(remotes)
	if err != nil {
		return err
	}
	os.MkdirAll(ConfigDir, 0755)
	out, err := json.MarshalIndent(modified, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(replRemotesFile), out, 0644)
}

// ResolveRemoteByID looks up a remote by ID and returns a copy.
// Used by the schedule runner and one-shot replication handler.
func ResolveRemoteByID(id string) (*Remote, error) {
	remotes, err := loadRemotes()
	if err != nil {
		return nil, err
	}
	for _, r := range remotes {
		if r.ID == id {
			cp := r
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("remote %q not found", id)
}

// RemotesHandler handles /api/replication/remotes routes.
type RemotesHandler struct {
	db *sql.DB
}

func NewRemotesHandler(db *sql.DB) *RemotesHandler {
	return &RemotesHandler{db: db}
}

// HandleListRemotes serves GET /api/replication/remotes
func (h *RemotesHandler) HandleListRemotes(w http.ResponseWriter, r *http.Request) {
	remotes, err := loadRemotes()
	if err != nil {
		respondErrorSimple(w, "Failed to load remotes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "remotes": remotes})
}

// HandleCreateRemote serves POST /api/replication/remotes
// Body: { "name": "...", "host": "...", "user": "root", "port": 22 }
func (h *RemotesHandler) HandleCreateRemote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Host string `json:"host"`
		User string `json:"user"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.Name) > 64 {
		respondErrorSimple(w, "name is required (max 64 chars)", http.StatusBadRequest)
		return
	}
	if req.Host == "" || len(req.Host) > 253 || strings.ContainsAny(req.Host, ";|&$`\\\"'") {
		respondErrorSimple(w, "Invalid host", http.StatusBadRequest)
		return
	}
	if req.User == "" {
		req.User = "root"
	}
	if !isValidSSHUser(req.User) {
		respondErrorSimple(w, "Invalid user", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Port < 1 || req.Port > 65535 {
		respondErrorSimple(w, "Invalid port", http.StatusBadRequest)
		return
	}

	remote := Remote{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Host:      req.Host,
		User:      req.User,
		Port:      req.Port,
		CreatedAt: time.Now(),
	}

	if err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		return append(all, remote), nil
	}); err != nil {
		respondErrorSimple(w, "Failed to save remotes", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "remote": remote})
	gitops.CommitAllAsync(h.db)
}

// HandleUpdateRemote serves PUT /api/replication/remotes/{id}
// If host, user, or port changes, key_installed and fingerprint are cleared
// because the stored key is no longer valid for the new target address.
func (h *RemotesHandler) HandleUpdateRemote(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var req struct {
		Name string `json:"name"`
		Host string `json:"host"`
		User string `json:"user"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate fields before acquiring the lock.
	if req.Host != "" && (len(req.Host) > 253 || strings.ContainsAny(req.Host, ";|&$`\\\"'")) {
		respondErrorSimple(w, "Invalid host", http.StatusBadRequest)
		return
	}
	if req.User != "" && !isValidSSHUser(req.User) {
		respondErrorSimple(w, "Invalid user", http.StatusBadRequest)
		return
	}
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		respondErrorSimple(w, "Invalid port", http.StatusBadRequest)
		return
	}

	err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		for i := range all {
			if all[i].ID != id {
				continue
			}
			connectionChanged := false
			if req.Name != "" {
				all[i].Name = req.Name
			}
			if req.Host != "" {
				if req.Host != all[i].Host {
					connectionChanged = true
				}
				all[i].Host = req.Host
			}
			if req.User != "" {
				if req.User != all[i].User {
					connectionChanged = true
				}
				all[i].User = req.User
			}
			if req.Port != 0 {
				if req.Port != all[i].Port {
					connectionChanged = true
				}
				all[i].Port = req.Port
			}
			if connectionChanged {
				all[i].KeyInstalled = false
				all[i].Fingerprint = ""
				all[i].HostKey = ""
				all[i].TestOK = false
			}
			return all, nil
		}
		return nil, errRemoteNotFound
	})

	if errors.Is(err, errRemoteNotFound) {
		respondErrorSimple(w, "Remote not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save remotes", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true})
	gitops.CommitAllAsync(h.db)
}

// HandleDeleteRemote serves DELETE /api/replication/remotes/{id}
// Blocked if any replication schedule still references this peer.
// The referencing-schedule check and the delete happen inside a single
// replStateMu-exclusive callback, eliminating the TOCTOU window where a
// concurrent schedule create could slip a new reference in between.
func (h *RemotesHandler) HandleDeleteRemote(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		// Cross-file check: read schedules directly while holding replStateMu.Lock().
		// loadReplicationSchedules cannot be called here (it also acquires replStateMu),
		// so we read the file raw. The lock guarantees no concurrent schedule write.
		schedData, _ := os.ReadFile(configPath(replScheduleFile))
		var schedules []ReplicationSchedule
		json.Unmarshal(schedData, &schedules) //nolint:errcheck - empty/missing file is fine
		for _, s := range schedules {
			if s.RemoteID == id {
				return nil, fmt.Errorf("schedule %q references this peer - remove or reassign it first: %w", s.Name, errRemoteInUse)
			}
		}

		out := make([]Remote, 0, len(all))
		found := false
		for _, rem := range all {
			if rem.ID == id {
				found = true
				continue
			}
			out = append(out, rem)
		}
		if !found {
			return nil, errRemoteNotFound
		}
		return out, nil
	})

	if errors.Is(err, errRemoteNotFound) {
		respondErrorSimple(w, "Remote not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, errRemoteInUse) {
		respondErrorSimple(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save remotes", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true})
	gitops.CommitAllAsync(h.db)
}

// HandleAuthorizeRemote serves POST /api/replication/remotes/{id}/authorize
// Body: { "password": "..." }
//
// Uses a Go SSH client (no sshpass binary) to push the replication public key
// to the remote's authorized_keys file. The password is held only in the
// request buffer and discarded immediately after the session closes - it is
// never written to disk, logged, or included in error responses.
//
// Host key fingerprint behavior:
//   - First authorization: TOFU - accept the host key, record its SHA256 fingerprint.
//   - Subsequent operations: pin-check - reject if fingerprint has changed.
func (h *RemotesHandler) HandleAuthorizeRemote(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		respondErrorSimple(w, "password is required", http.StatusBadRequest)
		return
	}

	remotes, err := loadRemotes()
	if err != nil {
		respondErrorSimple(w, "Failed to load remotes", http.StatusInternalServerError)
		return
	}
	idx := -1
	for i, rem := range remotes {
		if rem.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		respondErrorSimple(w, "Remote not found", http.StatusNotFound)
		return
	}
	remote := remotes[idx]

	// Auto-generate the replication keypair if it does not exist yet.
	if _, statErr := os.Stat(replPubPath); os.IsNotExist(statErr) {
		if genErr := generateReplKeyInternal(); genErr != nil {
			respondErrorSimple(w, "Failed to auto-generate replication key: "+genErr.Error(), http.StatusInternalServerError)
			return
		}
	}

	pubKeyBytes, err := os.ReadFile(replPubPath)
	if err != nil {
		respondErrorSimple(w, "Failed to read replication public key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pubKey := strings.TrimSpace(string(pubKeyBytes))

	var captured hostKeyCapture
	cfg := &ssh.ClientConfig{
		User:            remote.User,
		Auth:            []ssh.AuthMethod{ssh.Password(req.Password)},
		HostKeyCallback: buildHostKeyCallback(remote.Fingerprint, &captured),
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(remote.Host, fmt.Sprintf("%d", remote.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		respondErrorSimple(w, "SSH connection failed: "+sanitiseSSHConnError(err), http.StatusBadRequest)
		return
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		respondErrorSimple(w, "Failed to open SSH session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer sess.Close()

	// Public key is delivered via stdin - no shell string interpolation of user data.
	sess.Stdin = bytes.NewBufferString(pubKey + "\n")
	var authStderr bytes.Buffer
	sess.Stderr = &authStderr

	const installCmd = `mkdir -p ~/.ssh && chmod 700 ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`
	if runErr := sess.Run(installCmd); runErr != nil {
		respondErrorSimple(w, "Key installation command failed: "+authStderr.String(), http.StatusInternalServerError)
		return
	}

	var savedFingerprint string
	if err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		for i := range all {
			if all[i].ID != id {
				continue
			}
			all[i].KeyInstalled = true
			if captured.Fingerprint != "" {
				all[i].Fingerprint = captured.Fingerprint
			}
			if captured.HostKey != "" {
				all[i].HostKey = captured.HostKey
			}
			savedFingerprint = all[i].Fingerprint
			break
		}
		return all, nil
	}); err != nil {
		respondErrorSimple(w, "Key installed but failed to persist state: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"message":     fmt.Sprintf("Replication key installed on %s@%s. Password authentication is no longer required for replication to this peer.", remote.User, remote.Host),
		"fingerprint": savedFingerprint,
	})
	gitops.CommitAllAsync(h.db)
}

// HandleTestRemote serves POST /api/replication/remotes/{id}/test
// Authenticates using the replication private key (not a password) and returns:
//   - remote hostname (confirms identity)
//   - ZFS version / readiness (confirms the target can receive a ZFS stream)
//   - round-trip latency
func (h *RemotesHandler) HandleTestRemote(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	remote, err := ResolveRemoteByID(id)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusNotFound)
		return
	}
	privKeyBytes, err := os.ReadFile(replKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			respondErrorSimple(w, "No replication key found - generate one from the Peers tab first", http.StatusBadRequest)
			return
		}
		respondErrorSimple(w, "Failed to read replication key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	signer, err := ssh.ParsePrivateKey(privKeyBytes)
	if err != nil {
		respondErrorSimple(w, "Failed to parse replication key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	wasInstalled := remote.KeyInstalled
	var captured hostKeyCapture
	cfg := &ssh.ClientConfig{
		User:            remote.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: buildHostKeyCallback(remote.Fingerprint, &captured),
		Timeout:         10 * time.Second,
	}

	start := time.Now()
	addr := net.JoinHostPort(remote.Host, fmt.Sprintf("%d", remote.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		updateRemoteTestStatus(id, false)
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       sanitiseSSHConnError(err),
			"duration_ms": time.Since(start).Milliseconds(),
		})
		return
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		updateRemoteTestStatus(id, false)
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       "Session open failed: " + err.Error(),
			"duration_ms": time.Since(start).Milliseconds(),
		})
		return
	}
	defer sess.Close()

	var stdout bytes.Buffer
	sess.Stdout = &stdout
	// Key=value output avoids locale/format ambiguity in downstream parsing.
	_ = sess.Run(`printf "hostname=%s\n" "$(hostname)"; printf "zfs_version=%s\n" "$(zfs version 2>/dev/null | head -1 || echo none)"`)
	duration := time.Since(start)

	result := parseRemoteTestOutput(stdout.String())
	result["success"] = true
	result["duration_ms"] = duration.Milliseconds()

	// A successful key-based connection proves the key is installed.
	// This is the authorization path for sovereign targets where password auth is disabled.
	persistTestSuccess(id, captured.Fingerprint, captured.HostKey)
	if !wasInstalled {
		gitops.CommitAllAsync(h.db)
	}
	respondOK(w, result)
}

// hostKeyCapture holds the SSH host key material captured during connection establishment.
type hostKeyCapture struct {
	Fingerprint string // SHA256:<base64>
	HostKey     string // authorized_keys format: "keytype base64"
}

// buildHostKeyCallback returns a callback that pins the host key fingerprint.
// On the first connection (storedFP == ""), it accepts the key and writes the
// fingerprint and raw key to out (TOFU). On all subsequent connections it rejects
// keys that do not match the stored fingerprint.
func buildHostKeyCallback(storedFP string, out *hostKeyCapture) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := makeFingerprint(key)
		if out != nil {
			out.Fingerprint = fp
			out.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		}
		if storedFP == "" {
			return nil // TOFU: record and accept
		}
		if fp != storedFP {
			return fmt.Errorf("host key fingerprint mismatch (expected %s, got %s) - possible MITM or host key rotation", storedFP, fp)
		}
		return nil
	}
}

// makeFingerprint returns the SHA256 fingerprint of a host key in OpenSSH format.
func makeFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// resetAllRemotesKeyState sets key_installed=false and clears fingerprints on every
// remote. Called after the replication keypair is regenerated so that the daemon
// state accurately reflects that the old key is no longer installed on any peer.
func resetAllRemotesKeyState() {
	if err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		for i := range all {
			if all[i].KeyInstalled || all[i].TestOK {
				all[i].KeyInstalled = false
				all[i].TestOK = false
			}
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: resetAllRemotesKeyState: failed to persist reset: %v", err)
	}
}

// generateReplKeyInternal runs ssh-keygen to produce the replication keypair.
// Called automatically by HandleAuthorizeRemote when no key exists.
func generateReplKeyInternal() error {
	if err := os.MkdirAll(replKeyDir, 0700); err != nil {
		return err
	}
	_ = os.Remove(replKeyPath)
	_ = os.Remove(replPubPath)
	comment := fmt.Sprintf("dplaneos-replication@%s", replHostname())
	_, err := executeCommandWithTimeout(10*time.Second, "ssh-keygen",
		[]string{"-t", "ed25519", "-f", replKeyPath, "-N", "", "-C", comment})
	if err != nil {
		return err
	}
	resetAllRemotesKeyState()
	return nil
}

// updateRemoteTestStatus persists a failed test result.
func updateRemoteTestStatus(id string, ok bool) {
	now := time.Now()
	if err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		for i := range all {
			if all[i].ID == id {
				all[i].LastTested = now
				all[i].TestOK = ok
				break
			}
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: updateRemoteTestStatus: %v", err)
	}
}

// persistTestSuccess marks the peer as tested OK and, critically, sets key_installed=true
// if it was not already set. This is the authorization path for sovereign targets where
// password auth is disabled and the key was copied manually to authorized_keys.
// Fingerprint is pinned on first successful test if not already stored.
func persistTestSuccess(id, fingerprint, hostKey string) {
	now := time.Now()
	if err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		for i := range all {
			if all[i].ID != id {
				continue
			}
			all[i].LastTested = now
			all[i].TestOK = true
			if !all[i].KeyInstalled {
				all[i].KeyInstalled = true
			}
			if fingerprint != "" && all[i].Fingerprint == "" {
				all[i].Fingerprint = fingerprint
			}
			if hostKey != "" && all[i].HostKey == "" {
				all[i].HostKey = hostKey
			}
			break
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: persistTestSuccess: %v", err)
	}
}

// parseRemoteTestOutput extracts hostname and zfs_version from key=value lines.
func parseRemoteTestOutput(out string) map[string]interface{} {
	result := map[string]interface{}{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "hostname":
			result["remote_hostname"] = v
		case "zfs_version":
			result["zfs_version"] = v
			result["zfs_ready"] = v != "none" && v != ""
		}
	}
	return result
}

// makeKnownHostsEntry formats a single known_hosts line for host/port.
// Standard port 22 uses "hostname keytype base64"; non-standard uses "[hostname]:port keytype base64".
func makeKnownHostsEntry(host string, port int, hostKey string) string {
	if port == 22 || port == 0 {
		return host + " " + hostKey
	}
	return fmt.Sprintf("[%s]:%d %s", host, port, hostKey)
}

// buildKnownHostsArgs returns ssh(1) args that pin the remote host key using a
// temporary known_hosts file. The caller MUST invoke cleanup() after the ssh
// process exits to delete the temp file (call it inside a job goroutine so the
// file outlives the HTTP handler).
// If the peer has no stored HostKey yet, returns accept-new so the first
// connection can capture the fingerprint. Returns an error if the temp file
// cannot be written - callers must fail the job rather than silently degrading.
func buildKnownHostsArgs(remote *Remote) (args []string, cleanup func(), err error) {
	if remote.HostKey == "" {
		return []string{"-o", "StrictHostKeyChecking=accept-new"}, func() {}, nil
	}
	entry := makeKnownHostsEntry(remote.Host, remote.Port, remote.HostKey)
	f, ferr := os.CreateTemp("", "dplaneos-known-hosts-*")
	if ferr != nil {
		return nil, func() {}, fmt.Errorf("failed to create temp known_hosts file: %w", ferr)
	}
	if _, werr := fmt.Fprintln(f, entry); werr != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, func() {}, fmt.Errorf("failed to write temp known_hosts file: %w", werr)
	}
	f.Close()
	path := f.Name()
	return []string{
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + path,
	}, func() { os.Remove(path) }, nil
}

// HandleResetFingerprint clears the stored host key and fingerprint for a peer,
// allowing it to be re-trusted on the next Authorize or Test. Use when the remote
// server has been reinstalled and its SSH host key has changed.
// POST /api/replication/remotes/{id}/reset-fingerprint
func (h *RemotesHandler) HandleResetFingerprint(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		respondErrorSimple(w, "id is required", http.StatusBadRequest)
		return
	}

	err := atomicModifyRemotes(func(all []Remote) ([]Remote, error) {
		for i := range all {
			if all[i].ID == id {
				all[i].Fingerprint = ""
				all[i].HostKey = ""
				all[i].TestOK = false
				// KeyInstalled is intentionally preserved: our client key is still in the
				// remote's authorized_keys. Only the server's host key changed.
				// Replication will run in TOFU mode until the operator runs Test to re-pin.
				return all, nil
			}
		}
		return nil, errRemoteNotFound
	})

	if errors.Is(err, errRemoteNotFound) {
		respondErrorSimple(w, "Peer not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})
	gitops.CommitAllAsync(h.db)
}

// sanitiseSSHConnError strips credential material from SSH dial errors before
// returning them to the client.
func sanitiseSSHConnError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "password") || strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "auth") {
		return "authentication failed - check host, user, and password"
	}
	return err.Error()
}
