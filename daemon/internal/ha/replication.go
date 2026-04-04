package ha

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/zfs"
)

// ReplicationConfig holds the active-to-standby ZFS sync parameters.
type ReplicationConfig struct {
	LocalPool    string `json:"local_pool"`
	RemotePool   string `json:"remote_pool"`
	RemoteHost   string `json:"remote_host"`
	RemoteUser   string `json:"remote_user"`
	RemotePort   int    `json:"remote_port"`
	SSHKeyPath   string `json:"ssh_key_path"`
	IntervalSecs int    `json:"interval_secs"`
}

// startReplicationLoop begins continuous ZFS sync to the standby peer
// if this node is acting as the primary.
func (m *Manager) startReplicationLoop(ctx context.Context, cfg *ReplicationConfig) {
	interval := time.Duration(cfg.IntervalSecs) * time.Second
	if interval < 10*time.Second {
		interval = 30 * time.Second // Enforce safe floor
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("HA: continuous replication loop started (%s -> %s@%s:%s)",
		cfg.LocalPool, cfg.RemoteUser, cfg.RemoteHost, cfg.RemotePool)

	for {
		select {
		case <-ctx.Done():
			log.Printf("HA: continuous replication loop stopped")
			return
		case <-ticker.C:
			// Only the primary node replicates to the standby
			if m.Status().LocalNode.Role != RoleActive {
				continue
			}
			
			// We skip the sync if Patroni API definitively says we are NOT the primary database,
			// to avoid split-brain writes during promotion race conditions.
			if !m.IsPatroniPrimary() {
				// Don't log spam, just wait till next loop
				continue
			}

			if err := m.syncZFS(ctx, cfg); err != nil {
				log.Printf("HA Replication Error: %v", err)
			}
		}
	}
}

// syncZFS executes an incremental zfs send/recv securely over SSH.
func (m *Manager) syncZFS(ctx context.Context, cfg *ReplicationConfig) error {
	// First, gather local snapshots
	snapCmd := exec.CommandContext(ctx, "zfs", "list", "-t", "snapshot", "-o", "name", "-H", "-s", "creation", "-r", cfg.LocalPool)
	out, err := snapCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list local snapshots: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		// Nothing to replicate
		return nil
	}
	latestLocalSnap := lines[len(lines)-1]

	// Determine latest remote snapshot via SSH
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-i", cfg.SSHKeyPath,
		"-p", fmt.Sprintf("%d", cfg.RemotePort),
		fmt.Sprintf("%s@%s", cfg.RemoteUser, cfg.RemoteHost),
		fmt.Sprintf("zfs list -t snapshot -o name -H -s creation -r %s | tail -n 1", cfg.RemotePool),
	}

	remoteCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	remoteOut, err := remoteCmd.Output()
	if err != nil {
		// If `zfs list` finds no datasets (empty pool), it exits 1.
		// We should treat this as "no remote snapshots" and proceed with a full send.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			remoteOut = []byte("") // Clear to ensure string is empty
		} else {
			return fmt.Errorf("failed to get remote snapshot state: %w", err)
		}
	}

	latestRemoteSnapLine := strings.TrimSpace(string(remoteOut))
	latestRemoteSnap := ""
	if latestRemoteSnapLine != "" {
		// e.g. tank/data@xyz -> need to extract the snapshot segment
		parts := strings.Split(latestRemoteSnapLine, "@")
		if len(parts) == 2 {
			latestRemoteSnap = parts[1]
		}
	}

	// 3. We only sync if there is a newer local snapshot than what is on the remote.
	// We translate the remote snapshot name to our local pool context to check for existence.
	// Example: remote `tank/data@snap` -> local `pool/data@snap`
	localSnapName := strings.Replace(latestRemoteSnapLine, cfg.RemotePool, cfg.LocalPool, 1)
	
	if latestRemoteSnap == "" {
		// Full send
		return m.executeSendRecv(ctx, cfg, "", latestLocalSnap)
	}

	if latestLocalSnap == localSnapName {
		// Already up to date
		return nil
	}

	// Double check that the remote snapshot base actually exists locally, 
	// otherwise the incremental send will fail gracefully anyway.
	return m.executeSendRecv(ctx, cfg, localSnapName, latestLocalSnap)
}

func (m *Manager) executeSendRecv(ctx context.Context, cfg *ReplicationConfig, baseSnap, targetSnap string) error {
	var sendArgs []string
	if baseSnap != "" {
		sendArgs = []string{"send", "-P", "-R", "-i", baseSnap, targetSnap}
	} else {
		sendArgs = []string{"send", "-P", "-R", targetSnap}
	}

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"-i", cfg.SSHKeyPath,
		"-p", fmt.Sprintf("%d", cfg.RemotePort),
		fmt.Sprintf("%s@%s", cfg.RemoteUser, cfg.RemoteHost),
		"zfs", "recv", "-s", "-F", cfg.RemotePool,
	}

	sender := exec.CommandContext(ctx, "zfs", sendArgs...)
	senderOut, err := sender.StdoutPipe()
	if err != nil {
		return fmt.Errorf("sender stdout pipe: %w", err)
	}
	sendErrPipe, err := sender.StderrPipe()
	if err != nil {
		return fmt.Errorf("sender stderr pipe: %w", err)
	}

	receiver := exec.CommandContext(ctx, "ssh", sshArgs...)
	receiver.Stdin = senderOut
	var recvStderr bytes.Buffer
	receiver.Stdout = io.Discard
	receiver.Stderr = &recvStderr

	if err := sender.Start(); err != nil {
		return fmt.Errorf("start sender: %w", err)
	}

	var st zfs.SendProgressState
	go func() {
		sc := bufio.NewScanner(sendErrPipe)
		for sc.Scan() {
			line := sc.Text()
			if up, ok := zfs.FeedSendProgressLine(line, &st, 500*time.Millisecond); ok {
				up["source"] = "ha_zfs_sync"
				up["local_pool"] = cfg.LocalPool
				up["remote_pool"] = cfg.RemotePool
				up["remote_host"] = cfg.RemoteHost
				m.reportReplicationProgress(up)
			}
		}
	}()

	if err := receiver.Start(); err != nil {
		sender.Wait() //nolint
		return fmt.Errorf("start receiver: %w", err)
	}

	senderErr := sender.Wait()
	receiverErr := receiver.Wait()

	if senderErr != nil {
		return fmt.Errorf("send failed: %w", senderErr)
	}
	if receiverErr != nil {
		errOut := strings.TrimSpace(recvStderr.String())
		if errOut != "" {
			return fmt.Errorf("receive failed: %w (ssh stderr: %s)", receiverErr, errOut)
		}
		return fmt.Errorf("receive failed: %w", receiverErr)
	}
	return nil
}
