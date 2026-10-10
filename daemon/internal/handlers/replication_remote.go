package handlers

import (
	"regexp"
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
		NonRecursive  bool   `json:"non_recursive"`
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
		// -d 1: snapshots of this dataset only (with -r the newest snapshot
		// of a child dataset could be picked and replicated instead).
		snapOut, snapErr := executeCommandWithTimeout(TimeoutFast, "zfs",
			[]string{"list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-d", "1", req.SourceDataset})
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
	if !isValidSSHHost(req.RemoteHost) {
		respondErrorSimple(w, "Invalid remote host", http.StatusBadRequest)
		return
	}
	// base_snapshot goes to "zfs send -i": a snapshot of the source
	// dataset, full or short (@name).
	if req.Incremental && req.BaseSnap != "" && !baseSnapRe.MatchString(req.BaseSnap) &&
		!(isValidSnapshotName(req.BaseSnap) && strings.SplitN(req.BaseSnap, "@", 2)[0] == strings.SplitN(req.Snapshot, "@", 2)[0]) {
		respondErrorSimple(w, "Invalid base_snapshot (a snapshot of the source dataset)", http.StatusBadRequest)
		return
	}
	if req.RateLimit != "" && !rateLimitRe.MatchString(req.RateLimit) {
		respondErrorSimple(w, "Invalid rate_limit (bytes per second, e.g. 50M)", http.StatusBadRequest)
		return
	}
	if req.SSHKey != "" && (!keyPathRe.MatchString(req.SSHKey) || strings.Contains(req.SSHKey, "..")) {
		respondErrorSimple(w, "Invalid ssh_key_path", http.StatusBadRequest)
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
		if req.RateLimit != "" {
			if _, pvErr := exec.LookPath("pv"); pvErr != nil {
				j.Log("WARN: pv is not installed - rate limit will be ignored. Install pv to enable bandwidth throttling.")
			}
		}

		// Build SSH args inside the job so the known_hosts temp file survives until ZFS send completes.
		var sshArgs []string
		if resolvedRemote != nil {
			khArgs, cleanupKH, khErr := buildKnownHostsArgs(resolvedRemote)
			defer cleanupKH()
			if khErr != nil {
				j.Fail("Failed to prepare known_hosts for peer: " + khErr.Error())
				return
			}
			if resolvedRemote.HostKey == "" {
				j.Log("WARN: peer has no pinned host key - running in TOFU mode (accept-new). Use the Peers tab to Test this peer and pin the fingerprint.")
			}
			sshArgs = append([]string{"-i", replKeyPath}, khArgs...)
		} else {
			sshArgs = []string{"-o", "StrictHostKeyChecking=accept-new"}
			if req.SSHKey != "" {
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
				j.Done(map[string]any{"resumed": true, "output": output})
				return
			}
		}

		// Normal send
		sendArgs := []string{"send", "-P"} // -P for progress parsing
		if req.Compressed {
			sendArgs = append(sendArgs, "-c")
		}
		if !req.NonRecursive {
			sendArgs = append(sendArgs, "-R")
		}
		if req.Incremental && req.BaseSnap != "" {
			sendArgs = append(sendArgs, "-i", req.BaseSnap)
		}
		sendArgs = append(sendArgs, req.Snapshot)

		var rateLimitBytes []string
		if req.RateLimit != "" {
			rateLimitBytes = []string{req.RateLimit}
		}

		output, err := execPipedZFSSend(j, sendArgs, sshArgs, sshTarget, []string{"recv", "-s", "-F", remoteDataset}, rateLimitBytes)
		if err != nil {
			j.Log("ERROR: " + err.Error())
			j.Fail(fmt.Sprintf("Replication failed: %v\nOutput: %s", err, output))
			return
		}

		j.Done(map[string]any{"snapshot": req.Snapshot, "remote": remoteDataset, "output": output})
	})

	respondOK(w, map[string]any{"success": true, "job_id": jobID})
}

// sshHostRe: host names and IP addresses (IPv6 with colons, optionally in
// brackets). The host is an ssh argument: no leading "-" (option injection,
// e.g. -oProxyCommand), no spaces or shell characters.
var sshHostRe = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9.:\[\]-]{0,252}$`)

func isValidSSHHost(host string) bool { return sshHostRe.MatchString(host) }

var (
	rateLimitRe = regexp.MustCompile(`^[0-9]{1,12}[kKmMgGtT]?$`)
	baseSnapRe  = regexp.MustCompile(`^@[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	keyPathRe   = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
)

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
	// ssh reads an argument starting with "-" as an option.
	if user[0] == '-' || user[0] == '.' {
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
func getResumeToken(sshArgs []string, sshTarget, remoteDataset string) string {
	checkArgs := append([]string{}, sshArgs...)
	checkArgs = append(checkArgs, sshTarget,
		"zfs", "get", "-H", "-o", "value", "receive_resume_token", remoteDataset,
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

// RestoreFromRemote pulls a snapshot from a remote peer into a local dataset.
// This is the disaster-recovery path: remote has the backup, local needs to receive it.
// The operation is: ssh remote zfs send <snapshot> | zfs recv [-F] <local_dataset>
//
// POST /api/replication/restore
// Request: { "remote_id": "...", "remote_snapshot": "backuppool/data@snap-2025", "local_dataset": "tank/data", "force": false }
func (h *ReplicationHandler) RestoreFromRemote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoteID       string `json:"remote_id"`
		RemoteSnapshot string `json:"remote_snapshot"` // full path on remote e.g. backuppool/data@snap-2025
		LocalDataset   string `json:"local_dataset"`   // where to receive, e.g. tank/data-restore
		Force          bool   `json:"force"`           // -F: destroy newer snapshots on local to force receive
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !isValidSnapshotName(req.RemoteSnapshot) {
		respondErrorSimple(w, "Invalid remote snapshot name", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.LocalDataset) {
		respondErrorSimple(w, "Invalid local dataset name", http.StatusBadRequest)
		return
	}

	remote, err := ResolveRemoteByID(req.RemoteID)
	if err != nil {
		respondErrorSimple(w, "Peer not found: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !remote.KeyInstalled {
		respondErrorSimple(w, "Peer not authorized - click Authorize in the Peers tab first", http.StatusBadRequest)
		return
	}

	jobID := jobs.Start("replication_restore", func(j *jobs.Job) {
		knownHostsArgs, cleanup, khErr := buildKnownHostsArgs(remote)
		defer cleanup()
		if khErr != nil {
			j.Fail("Failed to prepare SSH known_hosts: " + khErr.Error())
			return
		}

		sshArgs := append([]string{"-i", replKeyPath}, knownHostsArgs...)
		sshArgs = append(sshArgs,
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=30",
			"-o", "ServerAliveCountMax=3",
			"-p", fmt.Sprintf("%d", remote.Port),
		)
		sshTarget := fmt.Sprintf("%s@%s", remote.User, remote.Host)

		// Verify the snapshot exists on the remote before starting.
		checkArgs := append([]string{}, sshArgs...)
		checkArgs = append(checkArgs, sshTarget,
			"zfs", "list", "-H", "-t", "snapshot", "-o", "name", req.RemoteSnapshot)
		if _, checkErr := executeCommandWithTimeout(TimeoutMedium, "ssh", checkArgs); checkErr != nil {
			j.Fail(fmt.Sprintf("Snapshot %q not found on remote %s: %v", req.RemoteSnapshot, remote.Name, checkErr))
			return
		}

		// Build remote send command: ssh remote zfs send [-R] <snapshot>
		remoteSendCmd := []string{"send", "-P", "-R", req.RemoteSnapshot}

		// Build local recv args
		recvArgs := []string{"recv", "-s"}
		if req.Force {
			recvArgs = append(recvArgs, "-F")
		}
		recvArgs = append(recvArgs, req.LocalDataset)

		j.Log(fmt.Sprintf("Restoring %s:%s -> local:%s", remote.Name, req.RemoteSnapshot, req.LocalDataset))

		// Execute the reverse pipeline: ssh remote zfs send | local zfs recv
		// We reuse execPipedZFSSend but invert the flow:
		// ssh <args> remote "zfs send <snapshot>" | zfs recv <local>
		// This is done by constructing a special "send" that is actually ssh sending.
		//
		// For the restore path we use a simpler direct approach: run ssh as the
		// "sender" and local zfs recv as the "receiver". We cannot reuse
		// execPipedZFSSend directly since it always does local-send | ssh-recv.
		// Instead we implement the same pipe pattern inline.
		sshSendArgs := append([]string{}, sshArgs...)
		sshSendArgs = append(sshSendArgs, sshTarget, "zfs")
		sshSendArgs = append(sshSendArgs, remoteSendCmd...)

		_, restoreErr := execPipedRestore(j, sshSendArgs, recvArgs)
		if restoreErr != nil {
			j.Fail(fmt.Sprintf("Restore failed: %v", restoreErr))
			return
		}

		// Post-receive integrity checks.
		// 1. Verify the expected snapshot exists on the local dataset. ZFS recv
		//    exits 0 even on a partial transfer if the stream terminates cleanly,
		//    so we confirm the snapshot the caller requested is actually present.
		snapParts := strings.SplitN(req.RemoteSnapshot, "@", 2)
		if len(snapParts) == 2 {
			localSnap := req.LocalDataset + "@" + snapParts[1]
			snapCheck, snapCheckErr := executeCommandWithTimeout(TimeoutFast, "zfs",
				[]string{"list", "-H", "-t", "snapshot", "-o", "name", localSnap})
			if snapCheckErr != nil || strings.TrimSpace(snapCheck) != localSnap {
				j.Fail(fmt.Sprintf("Restore integrity check failed: expected snapshot %q not found after receive (partial transfer?)", localSnap))
				return
			}
			j.Log(fmt.Sprintf("Integrity check passed: snapshot %q confirmed present", localSnap))

			// 2. Check for a resume token. A resume token on the dataset means
			//    the receive stream was interrupted and ZFS is holding partial
			//    state for a future resume. This is not an error for the snapshot
			//    that DID land, but we surface it so the operator knows.
			tokenOut, _ := executeCommandWithTimeout(TimeoutFast, "zfs",
				[]string{"get", "-H", "-o", "value", "receive_resume_token", req.LocalDataset})
			if token := strings.TrimSpace(tokenOut); token != "" && token != "-" && token != "none" {
				j.Log(fmt.Sprintf("WARN: resume token present on %s - a prior or concurrent receive was interrupted; run another restore to complete it", req.LocalDataset))
			}
		}

		j.Done(map[string]any{
			"remote_snapshot": req.RemoteSnapshot,
			"local_dataset":   req.LocalDataset,
			"peer":            remote.Name,
		})
		DispatchAlert("info", "replication.restore_complete", req.LocalDataset,
			fmt.Sprintf("Restore of %s from %s completed successfully", req.RemoteSnapshot, remote.Name))
	})

	respondOK(w, map[string]any{"success": true, "job_id": jobID})
}

// execPipedRestore executes: ssh <sshArgs> [remote zfs send] | zfs recv <recvArgs>
// This is the inverse of execPipedZFSSend: the remote is the sender, local is the receiver.
func execPipedRestore(j *jobs.Job, sshArgs []string, recvArgs []string) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := exec.CommandContext(ctx, "ssh", sshArgs...)
	senderOut, err := sender.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("ssh stdout pipe: %w", err)
	}
	senderErr, err := sender.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("ssh stderr pipe: %w", err)
	}

	receiver := exec.CommandContext(ctx, "zfs", recvArgs...)
	receiver.Stdin = senderOut
	var recvOut, recvErr bytes.Buffer
	receiver.Stdout = &recvOut
	receiver.Stderr = &recvErr

	if err := sender.Start(); err != nil {
		return "", fmt.Errorf("start ssh: %w", err)
	}

	go func() {
		scanner := bufio.NewScanner(senderErr)
		var st zfs.SendProgressState
		for scanner.Scan() {
			if up, ok := zfs.FeedSendProgressLine(scanner.Text(), &st, 500*time.Millisecond); ok {
				j.Progress(up)
			}
		}
	}()

	if err := receiver.Start(); err != nil {
		sender.Wait() //nolint
		return "", fmt.Errorf("start zfs recv: %w", err)
	}
	sender.Wait() //nolint
	if err := receiver.Wait(); err != nil {
		return recvErr.String(), fmt.Errorf("zfs recv failed: %w\n%s", err, recvErr.String())
	}
	return recvOut.String(), nil
}

