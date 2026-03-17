package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// ReplicationHandler handles ZFS replication to remote targets
type ReplicationHandler struct{}

func NewReplicationHandler() *ReplicationHandler {
	return &ReplicationHandler{}
}

// ReplicateToRemote performs zfs send | ssh remote zfs recv
// Supports resume tokens for interrupted transfers (critical for large pools)
// POST /api/replication/remote
func (h *ReplicationHandler) ReplicateToRemote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot    string `json:"snapshot"`     // tank/data@daily-2025-02-15
		RemoteHost  string `json:"remote_host"`  // backup-server.local
		RemotePort  int    `json:"remote_port"`  // 22 (default)
		RemoteUser  string `json:"remote_user"`  // root
		RemotePool  string `json:"remote_pool"`  // backup/nas
		Incremental bool   `json:"incremental"`  // use -i flag
		BaseSnap    string `json:"base_snapshot"` // for incremental: previous snapshot
		Compressed  bool   `json:"compressed"`   // use -c flag (compressed stream)
		SSHKey      string `json:"ssh_key_path"` // /root/.ssh/id_ed25519
		Resume      bool   `json:"resume"`       // attempt to resume interrupted transfer
		RateLimit   string `json:"rate_limit"`   // bandwidth limit, e.g. "50M" (50 MB/s), "1G", empty=unlimited
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate inputs
	if !isValidSnapshotName(req.Snapshot) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}
	if req.RemoteHost == "" || len(req.RemoteHost) > 253 {
		respondErrorSimple(w, "Invalid remote host", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.RemoteHost, ";|&$`\\\"'") {
		respondErrorSimple(w, "Invalid characters in remote host", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.RemotePool) {
		respondErrorSimple(w, "Invalid remote pool name", http.StatusBadRequest)
		return
	}
	if req.RemoteUser == "" {
		req.RemoteUser = "root"
	}
	// Validate RemoteUser: only alphanumeric, dots, dashes, underscores (no shell chars)
	if !isValidSSHUser(req.RemoteUser) {
		respondErrorSimple(w, "Invalid characters in remote user", http.StatusBadRequest)
		return
	}
	if req.RemotePort == 0 {
		req.RemotePort = 22
	}
	if req.RemotePort < 1 || req.RemotePort > 65535 {
		respondErrorSimple(w, "Invalid port number", http.StatusBadRequest)
		return
	}

	// Build SSH args (shared between send and resume paths)
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-p", fmt.Sprintf("%d", req.RemotePort),
	}
	if req.SSHKey != "" && !strings.ContainsAny(req.SSHKey, ";|&$`\\\"'") {
		sshArgs = append(sshArgs, "-i", req.SSHKey)
	}

	sshTarget := fmt.Sprintf("%s@%s", req.RemoteUser, req.RemoteHost)

	// Extract dataset name from snapshot for remote path
	snapParts := strings.SplitN(req.Snapshot, "@", 2)
	datasetName := snapParts[0]
	parts := strings.Split(datasetName, "/")
	remoteDataset := req.RemotePool + "/" + parts[len(parts)-1]

	// Check for resume token on remote side first
	if req.Resume {
		token := getResumeToken(sshArgs, sshTarget, remoteDataset)
		if token != "" {
			// Validate the resume token before using it in a command arg.
			// Tokens are base64url-encoded opaque blobs from ZFS - only
			// alphanumeric + +/= are valid; reject anything else.
			if !isValidResumeToken(token) {
				respondErrorSimple(w, "Invalid resume token format", http.StatusBadRequest)
				return
			}

			// Resume interrupted transfer.
			// Use two separate exec.Command calls connected via a pipe,
			// not bash -c with string interpolation (prevents shell injection).
			start := time.Now()
			output, err := execPipedZFSSend(
				[]string{"send", "-t", token},
				sshArgs, sshTarget,
				[]string{"recv", "-s", "-F", remoteDataset},
				nil, // no rate limit for resume
			)
			duration := time.Since(start)

			if err != nil {
				respondOK(w, map[string]interface{}{
					"success":     false,
					"resumed":     true,
					"error":       fmt.Sprintf("Resume failed: %v", err),
					"output":      output,
					"duration_ms": duration.Milliseconds(),
					"hint":        "Transfer may be partially complete. Try resume again.",
				})
				return
			}

			respondOK(w, map[string]interface{}{
				"success":     true,
				"resumed":     true,
				"snapshot":    req.Snapshot,
				"remote":      fmt.Sprintf("%s:%s", sshTarget, remoteDataset),
				"duration_ms": duration.Milliseconds(),
			})
			return
		}
		// No resume token found - fall through to normal send
	}

	// Build zfs send args
	sendArgs := []string{"send"}
	if req.Compressed {
		sendArgs = append(sendArgs, "-c")
	}
	sendArgs = append(sendArgs, "-R") // replicate (include properties)

	if req.Incremental && req.BaseSnap != "" {
		if !isValidSnapshotName(req.BaseSnap) {
			respondErrorSimple(w, "Invalid base snapshot name", http.StatusBadRequest)
			return
		}
		sendArgs = append(sendArgs, "-i", req.BaseSnap)
	}
	sendArgs = append(sendArgs, req.Snapshot)

	// Full command with -s on receive side for resume support.
	// execPipedZFSSend connects zfs-send stdout → (optional pv) → ssh stdin
	// using Go pipes - no shell, no string interpolation.
	var rateLimitBytes []string
	if req.RateLimit != "" && !strings.ContainsAny(req.RateLimit, ";|&$`\\\"' ") {
		rateLimitBytes = []string{req.RateLimit}
	}

	start := time.Now()
	output, err := execPipedZFSSend(
		sendArgs,
		sshArgs, sshTarget,
		[]string{"recv", "-s", "-F", remoteDataset},
		rateLimitBytes,
	)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       fmt.Sprintf("Replication failed: %v", err),
			"output":      output,
			"duration_ms": duration.Milliseconds(),
			"hint":        "If transfer was interrupted, retry with resume=true to continue from where it stopped.",
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":        true,
		"snapshot":       req.Snapshot,
		"remote":         fmt.Sprintf("%s:%s", sshTarget, remoteDataset),
		"incremental":    req.Incremental,
		"compressed":     req.Compressed,
		"duration_ms":    duration.Milliseconds(),
	})
}

// TestRemoteConnection tests SSH connectivity to a replication target
// POST /api/replication/test
func (h *ReplicationHandler) TestRemoteConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoteHost string `json:"remote_host"`
		RemotePort int    `json:"remote_port"`
		RemoteUser string `json:"remote_user"`
		SSHKey     string `json:"ssh_key_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.RemoteHost == "" || strings.ContainsAny(req.RemoteHost, ";|&$`\\\"'") {
		respondErrorSimple(w, "Invalid remote host", http.StatusBadRequest)
		return
	}
	if req.RemoteUser == "" {
		req.RemoteUser = "root"
	}
	// Validate RemoteUser: only alphanumeric, dots, dashes, underscores (no shell chars)
	if !isValidSSHUser(req.RemoteUser) {
		respondErrorSimple(w, "Invalid characters in remote user", http.StatusBadRequest)
		return
	}
	if req.RemotePort == 0 {
		req.RemotePort = 22
	}

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=5",
		"-o", "BatchMode=yes",
		"-p", fmt.Sprintf("%d", req.RemotePort),
	}
	if req.SSHKey != "" && !strings.ContainsAny(req.SSHKey, ";|&$`\\\"'") {
		sshArgs = append(sshArgs, "-i", req.SSHKey)
	}
	sshArgs = append(sshArgs,
		fmt.Sprintf("%s@%s", req.RemoteUser, req.RemoteHost),
		"echo ok && zfs version 2>/dev/null || zpool version 2>/dev/null || echo no-zfs",
	)

	start := time.Now()
	output, err := executeCommand("/usr/bin/ssh", sshArgs)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       fmt.Sprintf("Connection failed: %v", err),
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	hasZFS := strings.Contains(output, "zfs") || !strings.Contains(output, "no-zfs")

	respondOK(w, map[string]interface{}{
		"success":     true,
		"remote_zfs":  hasZFS,
		"output":      strings.TrimSpace(output),
		"duration_ms": duration.Milliseconds(),
	})
}


// isValidResumeToken checks that a ZFS resume token contains only safe characters.
// ZFS tokens are base64url-encoded opaque blobs. Reject anything with shell metacharacters.
func isValidResumeToken(token string) bool {
	if len(token) == 0 || len(token) > 4096 {
		return false
	}
	for _, c := range token {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// execPipedZFSSend performs: zfs send <sendArgs> [| pv -q -L rateLimit] | ssh <sshArgs> sshTarget zfs <recvArgs>
//
// All processes are connected with Go pipes - no shell, no string interpolation,
// no bash -c. Each argument is a discrete element in argv, so shell metacharacter
// injection is not possible.
func execPipedZFSSend(
	sendArgs []string,
	sshArgs []string,
	sshTarget string,
	recvArgs []string,
	rateLimit []string, // nil = no rate limit; []string{"50M"} = pv -q -L 50M
) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshFullArgs := append([]string{}, sshArgs...)
	sshFullArgs = append(sshFullArgs, sshTarget, "/usr/sbin/zfs")
	sshFullArgs = append(sshFullArgs, recvArgs...)

	sender := exec.CommandContext(ctx, "/usr/sbin/zfs", sendArgs...)
	sendOut, err := sender.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("send stdout pipe: %w", err)
	}
	var senderStderr bytes.Buffer
	sender.Stderr = &senderStderr

	receiver := exec.CommandContext(ctx, "/usr/bin/ssh", sshFullArgs...)
	var recvStdout, recvStderr bytes.Buffer
	receiver.Stdout = &recvStdout
	receiver.Stderr = &recvStderr

	if len(rateLimit) == 1 {
		throttle := exec.CommandContext(ctx, "/usr/bin/pv", "-q", "-L", rateLimit[0])
		throttleOut, err := throttle.StdoutPipe()
		if err != nil {
			return "", fmt.Errorf("pv stdout pipe: %w", err)
		}
		throttle.Stdin = sendOut
		receiver.Stdin = throttleOut
		if err := sender.Start(); err != nil {
			return "", fmt.Errorf("start zfs send: %w", err)
		}
		if err := throttle.Start(); err != nil {
			sender.Wait() //nolint
			return "", fmt.Errorf("start pv: %w", err)
		}
		if err := receiver.Start(); err != nil {
			sender.Wait()   //nolint
			throttle.Wait() //nolint
			return "", fmt.Errorf("start ssh recv: %w", err)
		}
		sender.Wait()   //nolint
		throttle.Wait() //nolint
	} else {
		receiver.Stdin = sendOut
		if err := sender.Start(); err != nil {
			return "", fmt.Errorf("start zfs send: %w", err)
		}
		if err := receiver.Start(); err != nil {
			sender.Wait() //nolint
			return "", fmt.Errorf("start ssh recv: %w", err)
		}
		sender.Wait() //nolint
	}

	if err := receiver.Wait(); err != nil {
		combined := strings.TrimSpace(recvStderr.String() + " " + senderStderr.String())
		return combined, fmt.Errorf("replication failed: %w", err)
	}
	return recvStdout.String(), nil
}

// getResumeToken checks if the remote side has a resume token for an interrupted transfer
// isValidSSHUser validates SSH usernames: alphanumeric, dot, dash, underscore only.
// Applied before RemoteUser is passed as an exec.Command argument.
func isValidSSHUser(user string) bool {
	if len(user) == 0 || len(user) > 64 {
		return false
	}
	for _, c := range user {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func getResumeToken(sshArgs []string, sshTarget, remoteDataset string) string {
	checkArgs := append([]string{}, sshArgs...)
	checkArgs = append(checkArgs, sshTarget,
		fmt.Sprintf("/usr/sbin/zfs get -H -o value receive_resume_token %s 2>/dev/null", remoteDataset),
	)

	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/ssh", checkArgs)
	if err != nil {
		return ""
	}

	token := strings.TrimSpace(output)
	if token == "" || token == "-" {
		return ""
	}
	return token
}

