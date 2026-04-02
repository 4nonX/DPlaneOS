package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SyncStatus is returned by GET /api/ha/sync/status and consumed by peers
// during startup reconciliation to detect stale (zombie) nodes.
type SyncStatus struct {
	IsActive bool             `json:"is_active"`
	Pools    map[string]int64 `json:"pools"` // pool name → ZFS TXG (transaction group ID)
}

// GetLocalSyncStatus builds a SyncStatus for this node by reading ZFS TXGs
// from all locally visible pools. Higher TXG = more recent data.
func GetLocalSyncStatus(isActive bool) SyncStatus {
	s := SyncStatus{
		IsActive: isActive,
		Pools:    make(map[string]int64),
	}
	out, err := exec.Command("zpool", "list", "-H", "-o", "name").Output()
	if err != nil {
		return s
	}
	for _, pool := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pool = strings.TrimSpace(pool)
		if pool == "" {
			continue
		}
		s.Pools[pool] = localPoolTXG(pool)
	}
	return s
}

// localPoolTXG returns the latest committed ZFS transaction group for a pool.
// Returns 0 on error (pool not imported or non-existent).
func localPoolTXG(pool string) int64 {
	out, err := exec.Command("zfs", "get", "-H", "-p", "-o", "value", "txg", pool).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

// queryPeerSyncStatus fetches the SyncStatus from a peer daemon.
func queryPeerSyncStatus(peerAddr string) (SyncStatus, error) {
	var s SyncStatus
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(peerAddr + "/api/ha/sync/status")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("peer returned %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}

// StartupReconciliation is called once at daemon boot, before the heartbeat loop starts.
// It detects the "zombie resurrection" scenario: a node returning after a failover
// with a ZFS state that is days/weeks behind the currently-active peer.
//
// If a peer is active AND holds a newer TXG for the shared pool, this node enters
// Subordinate Mode: local pool is locked read-only and an async catch-up sync begins.
// Only after the sync completes does this node re-enable auto-failover as a valid Standby.
func (m *Manager) StartupReconciliation() {
	replCfg := m.GetReplicationConfig()
	if replCfg == nil {
		return // No replication configured — nothing to reconcile.
	}

	localTXG := localPoolTXG(replCfg.LocalPool)
	if localTXG == 0 {
		return // Pool not imported yet; boot will handle it.
	}

	m.mu.RLock()
	peerAddrs := make([]string, 0, len(m.nodes))
	for _, n := range m.nodes {
		peerAddrs = append(peerAddrs, n.Address)
	}
	m.mu.RUnlock()

	for _, addr := range peerAddrs {
		status, err := queryPeerSyncStatus(addr)
		if err != nil || !status.IsActive {
			continue
		}

		// Map peer pool name: try RemotePool first, fall back to same name as LocalPool.
		peerTXG := status.Pools[replCfg.RemotePool]
		if peerTXG == 0 {
			peerTXG = status.Pools[replCfg.LocalPool]
		}
		if peerTXG <= localTXG {
			continue // We are current — normal standby boot.
		}

		delta := peerTXG - localTXG
		log.Printf("HA RECONCILE: Zombie boot detected — peer at %s is active (TXG %d vs local %d, Δ%d). Entering Subordinate Mode to prevent stale data serving.",
			addr, peerTXG, localTXG, delta)

		m.mu.Lock()
		m.subordinateMode = true
		m.mu.Unlock()
		go m.persistClusterState()

		// Lock the pool read-only immediately so nothing accidentally writes stale data.
		exec.Command("zfs", "set", "readonly=on", replCfg.LocalPool).Run() //nolint
		log.Printf("HA RECONCILE: Pool %s locked read-only. Starting catch-up sync from active peer...", replCfg.LocalPool)

		go func(cfg *ReplicationConfig) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()

			if err := m.catchUpFromPeer(ctx, cfg); err != nil {
				log.Printf("HA RECONCILE: Catch-up sync failed: %v. Node remains in Subordinate Mode — manual intervention required (zfs recv or /api/ha/clear_fault).", err)
				return
			}

			exec.Command("zfs", "set", "readonly=off", cfg.LocalPool).Run() //nolint
			m.mu.Lock()
			m.subordinateMode = false
			m.mu.Unlock()
			go m.persistClusterState()
			log.Printf("HA RECONCILE: Catch-up complete. Pool %s unlocked. Node is now a valid Standby.", cfg.LocalPool)
		}(replCfg)

		return // Only one active peer will win this race.
	}
}

// catchUpFromPeer performs a reverse-direction ZFS receive: SSH to the active peer,
// stream its latest snapshot, and receive it into the local pool. This is the mirror
// of syncZFS — the peer sends, we receive.
func (m *Manager) catchUpFromPeer(ctx context.Context, cfg *ReplicationConfig) error {
	log.Printf("HA RECONCILE: Receiving catch-up stream from %s@%s (%s → %s)",
		cfg.RemoteUser, cfg.RemoteHost, cfg.RemotePool, cfg.LocalPool)

	// 1. Find the latest snapshot on the remote (active) peer.
	listRemoteArgs := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-i", cfg.SSHKeyPath,
		"-p", fmt.Sprintf("%d", cfg.RemotePort),
		fmt.Sprintf("%s@%s", cfg.RemoteUser, cfg.RemoteHost),
		fmt.Sprintf("zfs list -t snapshot -H -o name -s creation -r %s | tail -n 1", cfg.RemotePool),
	}
	remoteListCmd := exec.CommandContext(ctx, "ssh", listRemoteArgs...)
	remoteOut, err := remoteListCmd.Output()
	if err != nil {
		return fmt.Errorf("list remote snapshots: %w", err)
	}
	latestRemoteSnap := strings.TrimSpace(string(remoteOut))
	if latestRemoteSnap == "" {
		return fmt.Errorf("no snapshots on remote pool %s — cannot catch up", cfg.RemotePool)
	}

	// 2. Find our latest local snapshot to use as incremental base (avoids full resend).
	localListCmd := exec.CommandContext(ctx, "zfs", "list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-r", cfg.LocalPool)
	localOut, _ := localListCmd.Output()
	var baseSnap string
	localSnaps := strings.Split(strings.TrimSpace(string(localOut)), "\n")
	if n := len(localSnaps); n > 0 && localSnaps[n-1] != "" {
		parts := strings.SplitN(localSnaps[n-1], "@", 2)
		if len(parts) == 2 {
			// Translate local snapshot name to remote pool context
			baseSnap = cfg.RemotePool + "@" + parts[1]
		}
	}

	// 3. SSH to peer, stream zfs send, pipe into local zfs recv.
	var sendCmd string
	if baseSnap != "" {
		sendCmd = fmt.Sprintf("zfs send -R -i %s %s", baseSnap, latestRemoteSnap)
	} else {
		sendCmd = fmt.Sprintf("zfs send -R %s", latestRemoteSnap)
	}

	sshSendArgs := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"-i", cfg.SSHKeyPath,
		"-p", fmt.Sprintf("%d", cfg.RemotePort),
		fmt.Sprintf("%s@%s", cfg.RemoteUser, cfg.RemoteHost),
		sendCmd,
	}
	sender := exec.CommandContext(ctx, "ssh", sshSendArgs...)
	senderOut, err := sender.StdoutPipe()
	if err != nil {
		return fmt.Errorf("sender pipe: %w", err)
	}

	receiver := exec.CommandContext(ctx, "zfs", "recv", "-F", "-s", cfg.LocalPool)
	receiver.Stdin = senderOut

	if err := sender.Start(); err != nil {
		return fmt.Errorf("start SSH sender: %w", err)
	}
	if err := receiver.Start(); err != nil {
		sender.Wait() //nolint
		return fmt.Errorf("start zfs recv: %w", err)
	}

	senderErr := sender.Wait()
	recvErr := receiver.Wait()
	if senderErr != nil {
		return fmt.Errorf("SSH send: %w", senderErr)
	}
	if recvErr != nil {
		return fmt.Errorf("zfs recv: %w", recvErr)
	}
	return nil
}
