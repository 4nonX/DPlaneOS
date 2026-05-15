package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/jobs"
	"dplaned/internal/zfs"
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
		Snapshot      string `json:"snapshot"`       // tank/data@daily-2025-02-15; empty = auto-select latest
		SourceDataset string `json:"source_dataset"` // required when snapshot is empty
		RemoteID      string `json:"remote_id"`      // if set, peer lookup overrides host/user/port/key
		RemoteHost    string `json:"remote_host"`
		RemotePort    int    `json:"remote_port"`
		RemoteUser    string `json:"remote_user"`
		RemotePool    string `json:"remote_pool"`
		Incremental   bool   `json:"incremental"`
		BaseSnap      string `json:"base_snapshot"`
		Compressed    bool   `json:"compressed"`
		SSHKey        string `json:"ssh_key_path"`
		Resume        bool   `json:"resume"`
		RateLimit     string `json:"rate_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// If a peer ID is provided, resolve it and populate connection fields.
	var resolvedRemote *Remote
	if req.RemoteID != "" {
		r, rerr := ResolveRemoteByID(req.RemoteID)
		if rerr != nil {
			respondErrorSimple(w, "Peer not found: "+rerr.Error(), http.StatusBadRequest)
			return
		}
		if !r.KeyInstalled {
			respondErrorSimple(w, fmt.Sprintf("Peer %q is not authorized - open the Peers tab and click Authorize", r.Name), http.StatusBadRequest)
			return
		}
		resolvedRemote = r
		req.RemoteHost = r.Host
		req.RemoteUser = r.User
		req.RemotePort = r.Port
		req.SSHKey = replKeyPath
	}

	// Auto-select the latest snapshot when the caller omits one.
	if req.Snapshot == "" {
		if req.SourceDataset == "" || !isValidDataset(req.SourceDataset) {
			respondErrorSimple(w, "source_dataset is required when snapshot is not specified", http.StatusBadRequest)
			return
		}
		snapOut, snapErr := executeCommandWithTimeout(TimeoutFast, "zfs",
			[]string{"list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-r", req.SourceDataset})
		if snapErr != nil || strings.TrimSpace(snapOut) == "" {
			respondErrorSimple(w, "No snapshots found for dataset "+req.SourceDataset+" - create a snapshot first", http.StatusBadRequest)
			return
		}
		lines := strings.Split(strings.TrimSpace(snapOut), "\n")
		req.Snapshot = strings.TrimSpace(lines[len(lines)-1])
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

	sshTarget := fmt.Sprintf("%s@%s", req.RemoteUser, req.RemoteHost)

	// Extract remote dataset path
	snapParts := strings.SplitN(req.Snapshot, "@", 2)
	datasetName := snapParts[0]
	parts := strings.Split(datasetName, "/")
	remoteDataset := req.RemotePool + "/" + parts[len(parts)-1]

	// Start ASYNC JOB
	jobID := jobs.Start("replication_remote", func(j *jobs.Job) {
		// Build SSH args inside the job so the known_hosts temp file survives until ZFS send completes.
		var sshArgs []string
		if resolvedRemote != nil {
			khArgs, cleanupKH := buildKnownHostsArgs(resolvedRemote)
			defer cleanupKH()
			sshArgs = append([]string{"-i", replKeyPath}, khArgs...)
		} else {
			sshArgs = []string{"-o", "StrictHostKeyChecking=accept-new"}
			if req.SSHKey != "" && !strings.ContainsAny(req.SSHKey, ";|&$`\\\"'") {
				sshArgs = append(sshArgs, "-i", req.SSHKey)
			}
		}
		sshArgs = append(sshArgs,
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=30",
			"-o", "ServerAliveCountMax=3",
			"-p", fmt.Sprintf("%d", req.RemotePort),
		)

		j.Log(fmt.Sprintf("Starting replication: %s -> %s:%s", req.Snapshot, sshTarget, remoteDataset))

		// Check for resume token
		if req.Resume {
			token := getResumeToken(sshArgs, sshTarget, remoteDataset)
			if token != "" && isValidResumeToken(token) {
				j.Log("Found resume token, attempting to resume...")
				output, err := execPipedZFSSend(j, []string{"send", "-V", "-t", token}, sshArgs, sshTarget, []string{"recv", "-s", "-F", remoteDataset}, nil)
				if err != nil {
					j.Fail(fmt.Sprintf("Resume failed: %v\nOutput: %s", err, output))
					return
				}
				j.Done(map[string]interface{}{"resumed": true, "output": output})
				return
			}
		}

		// Normal send
		sendArgs := []string{"send", "-P"} // -P for progress parsing
		if req.Compressed {
			sendArgs = append(sendArgs, "-c")
		}
		sendArgs = append(sendArgs, "-R")
		if req.Incremental && req.BaseSnap != "" {
			sendArgs = append(sendArgs, "-i", req.BaseSnap)
		}
		sendArgs = append(sendArgs, req.Snapshot)

		var rateLimitBytes []string
		if req.RateLimit != "" && !strings.ContainsAny(req.RateLimit, ";|&$`\\\"' ") {
			rateLimitBytes = []string{req.RateLimit}
		}

		output, err := execPipedZFSSend(j, sendArgs, sshArgs, sshTarget, []string{"recv", "-s", "-F", remoteDataset}, rateLimitBytes)
		if err != nil {
			j.Log("ERROR: " + err.Error())
			j.Fail(fmt.Sprintf("Replication failed: %v\nOutput: %s", err, output))
			return
		}

		j.Done(map[string]interface{}{"snapshot": req.Snapshot, "remote": remoteDataset, "output": output})
	})

	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
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
	j *jobs.Job,
	sendArgs []string,
	sshArgs []string,
	sshTarget string,
	recvArgs []string,
	rateLimit []string,
) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshFullArgs := append([]string{}, sshArgs...)
	sshFullArgs = append(sshFullArgs, sshTarget, "zfs")
	sshFullArgs = append(sshFullArgs, recvArgs...)

	sender := exec.CommandContext(ctx, "zfs", sendArgs...)
	sendOut, err := sender.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("send stdout pipe: %w", err)
	}
	sendErr, err := sender.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("send stderr pipe: %w", err)
	}

	receiver := exec.CommandContext(ctx, "ssh", sshFullArgs...)
	var recvStdout, recvStderr bytes.Buffer
	receiver.Stdout = &recvStdout
	receiver.Stderr = &recvStderr

	// Start sender and capture progress from stderr
	if err := sender.Start(); err != nil {
		return "", fmt.Errorf("start zfs send: %w", err)
	}

	go func() {
		scanner := bufio.NewScanner(sendErr)
		var st zfs.SendProgressState
		for scanner.Scan() {
			if up, ok := zfs.FeedSendProgressLine(scanner.Text(), &st, 500*time.Millisecond); ok {
				j.Progress(up)
			}
		}
	}()

	if len(rateLimit) == 1 {
		if _, lookErr := exec.LookPath("pv"); lookErr != nil {
			j.Log("WARN: pv not installed - rate limit ignored. Install pv to enable bandwidth throttling.")
		} else {
			throttle := exec.CommandContext(ctx, "pv", "-q", "-L", rateLimit[0])
			throttleOut, err := throttle.StdoutPipe()
			if err != nil {
				sender.Wait() //nolint
				return "", fmt.Errorf("pv stdout pipe: %w", err)
			}
			throttle.Stdin = sendOut
			receiver.Stdin = throttleOut
			if err := throttle.Start(); err != nil {
				sender.Wait() //nolint
				return "", fmt.Errorf("start pv: %w", err)
			}
			if err := receiver.Start(); err != nil {
				sender.Wait()   //nolint
				throttle.Wait() //nolint
				return "", fmt.Errorf("start ssh recv: %w", err)
			}
			throttle.Wait() //nolint
			sender.Wait()   //nolint
			if err := receiver.Wait(); err != nil {
				return recvStderr.String(), fmt.Errorf("replication failed: %w", err)
			}
			return recvStdout.String(), nil
		}
	}
	// Direct pipe: no rate limit or pv not available
	receiver.Stdin = sendOut
	if err := receiver.Start(); err != nil {
		sender.Wait() //nolint
		return "", fmt.Errorf("start ssh recv: %w", err)
	}
	sender.Wait() //nolint
	if err := receiver.Wait(); err != nil {
		return recvStderr.String(), fmt.Errorf("replication failed: %w", err)
	}
	return recvStdout.String(), nil
}

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

// getResumeToken checks if the remote side has a resume token for an interrupted transfer.
// The command is passed as a single string arg, executed by the remote shell.
func getResumeToken(sshArgs []string, sshTarget, remoteDataset string) string {
	checkArgs := append([]string{}, sshArgs...)
	checkArgs = append(checkArgs, sshTarget,
		fmt.Sprintf("zfs get -H -o value receive_resume_token %s 2>/dev/null", remoteDataset),
	)

	output, err := executeCommandWithTimeout(TimeoutFast, "ssh", checkArgs)
	if err != nil {
		return ""
	}

	token := strings.TrimSpace(output)
	if token == "" || token == "-" {
		return ""
	}
	return token
}

