// Package ha provides cluster node registration and health monitoring for D-PlaneOS.
//
// # Architecture
//
// High Availability Nexus (v7.1.0) providing cluster-wide health monitoring,
// automated fencing (STONITH), and managed promotion for D-PlaneOS.
//
// This is a lightweight coordination layer that works alongside Patroni
// and HAProxy to provide a unified active/standby orchestration experience.
//
// # Major Features (v7.1.0)
//
//   - AUTOMATIC FENCING: Supported via BMC (ipmitool).
//   - AUTOMATIC PROMOTION: Triggered when peer breaches FailoverAfter threshold.
//   - SHARED-NOTHING REPLICATION: Automatic ZFS replication coordination.
//
// # Known Limitations
//
//   - [v7.1.0] Startup Split-Brain Guard: While Patroni handles multi-master 
//     prevention, a hard daemon-level check against the Patroni /health API 
//     on startup is planned for v7.2.0 to provide a secondary safety net 
//     for ZFS pool imports. (See cluster.go header backlog)
//
//   - [Backlog v7.2.0]: Implement non-blocking startup health check to Patroni.
package ha

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// NodeRole is either Active or Standby.
type NodeRole string

const (
	RoleActive  NodeRole = "active"
	RoleStandby NodeRole = "standby"
)

// NodeState is the reported health of a node.
type NodeState string

const (
	StateHealthy    NodeState = "healthy"
	StateDegraded   NodeState = "degraded"
	StateUnreachable NodeState = "unreachable"
	StateUnknown    NodeState = "unknown"
)

// ClusterNode represents a peer in the cluster.
type ClusterNode struct {
	ID           string    `json:"id"`           // unique node identifier (hostname or UUID)
	Name         string    `json:"name"`         // human-readable label
	Address      string    `json:"address"`      // http(s)://host:port of peer daemon
	Role         NodeRole  `json:"role"`         // active | standby
	State        NodeState `json:"state"`        // health from last heartbeat
	LastSeen     time.Time `json:"last_seen"`
	LastSeenUnix int64     `json:"last_seen_unix"`
	MissedBeats  int       `json:"missed_beats"`  // consecutive missed heartbeats
	Version      string    `json:"version"`
	RegisteredAt time.Time `json:"registered_at"`
}

// ClusterStatus summarises the full cluster view.
type ClusterStatus struct {
	LocalNode   *ClusterNode   `json:"local_node"`
	Peers       []*ClusterNode `json:"peers"`
	Quorum      bool           `json:"quorum"`       // true if majority of nodes are reachable
	ActiveNode         *ClusterNode   `json:"active_node"`  // which node currently holds the active role
	HAEnabled          bool           `json:"ha_enabled"`   // true if Patroni/HAProxy is configured in NixOS
	MaintenanceActive  bool           `json:"maintenance_active"`
	MaintenanceUntil   int64          `json:"maintenance_until"` // unix timestamp
	LastUpdated        time.Time      `json:"last_updated"`
}

// Manager owns the cluster state for this node.
type Manager struct {
	db        *sql.DB
	localID   string
	localAddr string
	version   string

	mu    sync.RWMutex
	nodes map[string]*ClusterNode // keyed by node ID

	replConfig *ReplicationConfig
	// fencingInProgress prevents multiple concurrent STONITH sequences.
	fencingInProgress bool

	replCancel context.CancelFunc

	maintenanceUntil time.Time

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewManager creates a cluster manager for this daemon instance.
//   localID   - unique ID for this node (use hostname or UUID from /etc/machine-id)
//   localAddr - how peers reach this daemon, e.g. "http://10.0.0.1:5050"
//   version   - daemon version string
func NewManager(db *sql.DB, localID, localAddr, version string) *Manager {
	m := &Manager{
		db:        db,
		localID:   localID,
		localAddr: localAddr,
		version:   version,
		nodes:     make(map[string]*ClusterNode),
		stopCh:    make(chan struct{}),
	}
	return m
}

// Start loads persisted peers and begins the heartbeat loop.
func (m *Manager) Start() {
	if err := m.ensureSchema(); err != nil {
		log.Printf("HA: schema error: %v", err)
		return
	}
	m.loadPersistedNodes()
	m.loadPersistedReplication()

	m.wg.Add(1)
	go m.heartbeatLoop()
	log.Printf("HA: cluster manager started (local=%s)", m.localID)
}

// Stop halts background goroutines and waits for them to exit.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.mu.Lock()
	if m.replCancel != nil {
		m.replCancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}


// RegisterPeer adds or updates a peer node in the cluster.
// Persists to DB so peers survive restarts.
func (m *Manager) RegisterPeer(peer *ClusterNode) error {
	if peer.ID == "" || peer.Address == "" {
		return fmt.Errorf("peer ID and Address are required")
	}
	if peer.ID == m.localID {
		return fmt.Errorf("cannot register self as peer")
	}

	m.mu.Lock()
	peer.RegisteredAt = time.Now()
	peer.State = StateUnknown
	m.nodes[peer.ID] = peer
	m.mu.Unlock()

	return m.persistNode(peer)
}

// RemovePeer removes a peer from the cluster.
func (m *Manager) RemovePeer(id string) error {
	m.mu.Lock()
	delete(m.nodes, id)
	m.mu.Unlock()
	_, err := m.db.Exec("DELETE FROM ha_nodes WHERE node_id = $1", id)
	return err
}

// Status returns the full cluster view from this node's perspective.
func (m *Manager) Status() *ClusterStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Build local node record
	local := &ClusterNode{
		ID:           m.localID,
		Name:         m.localID,
		Address:      m.localAddr,
		Version:      m.version,
		State:        StateHealthy,
		LastSeen:     time.Now(),
		LastSeenUnix: time.Now().Unix(),
	}

	// Determine local role: active if no peer is active, or if we're explicitly active
	localRole := RoleActive
	var activeNode *ClusterNode
	peers := make([]*ClusterNode, 0, len(m.nodes))
	for _, n := range m.nodes {
		cp := *n
		peers = append(peers, &cp)
		if n.Role == RoleActive && n.State != StateUnreachable {
			activeNode = &cp
			localRole = RoleStandby
		}
	}
	local.Role = localRole
	if activeNode == nil {
		activeNode = local
	}

	// Quorum: majority of registered nodes (including self) must be reachable
	total := len(m.nodes) + 1 // include self
	reachable := 1             // self is always reachable
	for _, n := range m.nodes {
		if n.State != StateUnreachable {
			reachable++
		}
	}
	quorum := reachable > total/2

	maintenanceActive := time.Now().Before(m.maintenanceUntil)

	return &ClusterStatus{
		LocalNode:         local,
		Peers:             peers,
		Quorum:            quorum,
		ActiveNode:        activeNode,
		HAEnabled:         m.replConfig != nil, // Approximation if NixWriter not available
		MaintenanceActive: maintenanceActive,
		MaintenanceUntil:  m.maintenanceUntil.Unix(),
		LastUpdated:       time.Now(),
	}
}

// SetMaintenanceMode suspends automated fencing for the given duration.
func (m *Manager) SetMaintenanceMode(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if duration <= 0 {
		m.maintenanceUntil = time.Time{}
		log.Printf("HA: Maintenance mode DISABLED.")
	} else {
		m.maintenanceUntil = time.Now().Add(duration)
		log.Printf("HA: Maintenance mode ENABLED for %v (until %v). Fencing is SUSPENDED.", duration, m.maintenanceUntil.Format("15:04:05"))
	}
}

// IsMaintenanceActive returns true if fencing should be suppressed.
func (m *Manager) IsMaintenanceActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Now().Before(m.maintenanceUntil)
}

// GetPeer returns a specific peer by ID.
func (m *Manager) GetPeer(id string) (*ClusterNode, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	if !ok {
		return nil, false
	}
	cp := *n
	return &cp, true
}

// FailoverAfter defines the threshold over which a missed heartbeat triggers STONITH/Promotion.
const FailoverAfter = 45 * time.Second

// heartbeatLoop pings all peers every 15 seconds.
func (m *Manager) heartbeatLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.pingAllPeers()
			m.checkFailover()
		}
	}
}

// checkFailover assesses peer health and triggers fencing/promotion if STONITH is enabled.
func (m *Manager) checkFailover() {
	m.mu.RLock()
	isStandby := false
	var deadPeer *ClusterNode

	for _, n := range m.nodes {
		if n.Role == RoleActive && n.State != StateUnreachable {
			isStandby = true
		}
		if n.State == StateUnreachable && time.Since(n.LastSeen) > FailoverAfter {
			// Found a dead peer that has breached the 45s margin.
			cp := *n
			deadPeer = &cp
		}
	}
	m.mu.RUnlock()

	if !isStandby || deadPeer == nil {
		return
	}

	// 2. Fetch fencing configuration from the database.
	fencingCfg, err := GetFencingConfig(m.db)
	if err != nil || !fencingCfg.Enable {
		// Just log on the transition edge, but avoid log spam every 15s.
		if deadPeer.MissedBeats == 3 {
			log.Printf("HA STONITH: Peer %s breached FailoverThreshold (45s), but Fencing is DISABLED. Automatic promotion aborted.", deadPeer.ID)
		}
		return
	}

	// 3. Check Maintenance Mode
	if m.IsMaintenanceActive() {
		if deadPeer.MissedBeats == 3 {
			log.Printf("HA STONITH: Peer %s breached FailoverThreshold (45s), but fencing is SUSPENDED due to ACTIVE MAINTENANCE MODE.", deadPeer.ID)
		}
		return
	}

	// 4. Initiate Fencing & Promotion asynchronously (rate limited via flag).
	m.mu.Lock()
	if m.fencingInProgress {
		m.mu.Unlock()
		return
	}
	m.fencingInProgress = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.fencingInProgress = false
			m.mu.Unlock()
		}()

		log.Printf("HA STONITH: Triggering automated fencing against dead peer %s", deadPeer.ID)
		if err := ExecuteFencing(deadPeer.ID, fencingCfg); err != nil {
			log.Printf("HA STONITH: Fencing failed, aborting failover: %v", err)
			return
		}

		// 4. Fencing was successful! Promote this node.
		log.Printf("HA STONITH: Fencing successful. Promoting local node to active role.")
		ExecutePromotion(m.localID, deadPeer.ID)
	}()
}

// pingAllPeers checks health of every registered peer concurrently.
func (m *Manager) pingAllPeers() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.nodes))
	for id := range m.nodes {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			m.pingPeer(nodeID)
		}(id)
	}
	wg.Wait()
}

// pingPeer does a GET /health on the peer daemon with a 5-second timeout.
func (m *Manager) pingPeer(id string) {
	m.mu.RLock()
	node, ok := m.nodes[id]
	if !ok {
		m.mu.RUnlock()
		return
	}
	addr := node.Address
	m.mu.RUnlock()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(addr + "/health")

	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok = m.nodes[id]
	if !ok {
		return
	}

	if err != nil {
		node.MissedBeats++
		if node.MissedBeats >= 2 {
			node.State = StateUnreachable
			log.Printf("HA: peer %s is UNREACHABLE (missed %d beats)", id, node.MissedBeats)
		}
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			node.MissedBeats++
			if node.MissedBeats >= 2 {
				node.State = StateUnreachable
				log.Printf("HA: peer %s is UNREACHABLE (missed %d beats)", id, node.MissedBeats)
			}
		} else {
			// Try to parse version from health response
			node.State = StateHealthy
			node.LastSeen = time.Now()
			node.LastSeenUnix = time.Now().Unix()
			node.MissedBeats = 0
		}
	}
	// Persist updated state
	go m.persistNode(node)
}

// ── Database ────────────────────────────────────────────────────────────────

func (m *Manager) ensureSchema() error {
	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_nodes (
			node_id       TEXT PRIMARY KEY,
			name          TEXT NOT NULL DEFAULT '',
			address       TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'standby',
			state         TEXT NOT NULL DEFAULT 'unknown',
			version       TEXT NOT NULL DEFAULT '',
			last_seen     BIGINT NOT NULL DEFAULT 0,
			missed_beats  INTEGER NOT NULL DEFAULT 0,
			registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return err
	}
	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_replication_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			local_pool TEXT NOT NULL,
			remote_pool TEXT NOT NULL,
			remote_host TEXT NOT NULL,
			remote_user TEXT NOT NULL,
			remote_port INTEGER NOT NULL DEFAULT 22,
			ssh_key_path TEXT NOT NULL,
			interval_secs INTEGER NOT NULL DEFAULT 30
		)
	`); err != nil {
		return err
	}
	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_fencing_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			enable BOOLEAN NOT NULL DEFAULT FALSE,
			bmc_ip TEXT NOT NULL DEFAULT '',
			bmc_user TEXT NOT NULL DEFAULT '',
			bmc_password_file TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		return err
	}
	return nil
}

func (m *Manager) persistNode(n *ClusterNode) error {
	_, err := m.db.Exec(`
		INSERT INTO ha_nodes (node_id, name, address, role, state, version, last_seen, missed_beats, registered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT(node_id) DO UPDATE SET
			name=excluded.name, address=excluded.address, role=excluded.role,
			state=excluded.state, version=excluded.version,
			last_seen=excluded.last_seen, missed_beats=excluded.missed_beats
	`, n.ID, n.Name, n.Address, string(n.Role), string(n.State),
		n.Version, n.LastSeen.Unix(), n.MissedBeats, n.RegisteredAt)
	return err
}

func (m *Manager) loadPersistedNodes() {
	rows, err := m.db.Query(`
		SELECT node_id, name, address, role, state, version, last_seen, missed_beats, registered_at
		FROM ha_nodes
	`)
	if err != nil {
		return
	}
	defer rows.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	for rows.Next() {
		n := &ClusterNode{}
		var lastSeenUnix int64
		rows.Scan(&n.ID, &n.Name, &n.Address, (*string)(&n.Role), (*string)(&n.State),
			&n.Version, &lastSeenUnix, &n.MissedBeats, &n.RegisteredAt)
		// Mark as unknown on load - we'll re-ping immediately
		n.State = StateUnknown
		m.nodes[n.ID] = n
	}
	log.Printf("HA: loaded %d persisted peers", len(m.nodes))
}

// HeartbeatPayload is what peers send to each other.
type HeartbeatPayload struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
	Role    string `json:"role"`
	Version string `json:"version"`
}

// HandleHeartbeat processes an inbound heartbeat from a peer daemon.
func (m *Manager) HandleHeartbeat(hb HeartbeatPayload) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[hb.NodeID]
	if !ok {
		// Auto-register on first heartbeat received
		node = &ClusterNode{
			ID:           hb.NodeID,
			Name:         hb.NodeID,
			Address:      hb.Address,
			RegisteredAt: time.Now(),
		}
		m.nodes[hb.NodeID] = node
	}
	node.Address = hb.Address
	node.Role = NodeRole(hb.Role)
	node.Version = hb.Version
	node.State = StateHealthy
	node.LastSeen = time.Now()
	node.LastSeenUnix = time.Now().Unix()
	node.MissedBeats = 0
	go m.persistNode(node)
}

// SetPeerRole updates the role of a peer (e.g. promote to active).
func (m *Manager) SetPeerRole(id string, role NodeRole) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[id]
	if !ok {
		return fmt.Errorf("peer %s not found", id)
	}
	node.Role = role
	return m.persistNode(node)
}

// LocalInfo returns the identity information about this node.
func (m *Manager) LocalInfo() map[string]string {
	return map[string]string{
		"id":      m.localID,
		"address": m.localAddr,
		"version": m.version,
	}
}

// MarshalStatus is a JSON-serializable form used by the API handler.
func MarshalStatus(s *ClusterStatus) ([]byte, error) {
	return json.Marshal(s)
}

// GetReplicationConfig returns the active replication configuration.
func (m *Manager) GetReplicationConfig() *ReplicationConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.replConfig
}

// SetReplicationConfig stores the new sync rules and restarts the loop.
func (m *Manager) SetReplicationConfig(cfg *ReplicationConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.db.Exec(`
		INSERT INTO ha_replication_config (id, local_pool, remote_pool, remote_host, remote_user, remote_port, ssh_key_path, interval_secs)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(id) DO UPDATE SET
			local_pool=excluded.local_pool, remote_pool=excluded.remote_pool,
			remote_host=excluded.remote_host, remote_user=excluded.remote_user,
			remote_port=excluded.remote_port, ssh_key_path=excluded.ssh_key_path,
			interval_secs=excluded.interval_secs
	`, cfg.LocalPool, cfg.RemotePool, cfg.RemoteHost, cfg.RemoteUser, cfg.RemotePort, cfg.SSHKeyPath, cfg.IntervalSecs)
	
	if err != nil {
		return err
	}

	m.replConfig = cfg
	
	// Restart loop
	if m.replCancel != nil {
		m.replCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.replCancel = cancel
	go m.startReplicationLoop(ctx, cfg)

	return nil
}

func (m *Manager) loadPersistedReplication() {
	m.mu.Lock()
	defer m.mu.Unlock()

	var cfg ReplicationConfig
	err := m.db.QueryRow(`
		SELECT local_pool, remote_pool, remote_host, remote_user, remote_port, ssh_key_path, interval_secs
		FROM ha_replication_config WHERE id = 1
	`).Scan(&cfg.LocalPool, &cfg.RemotePool, &cfg.RemoteHost, &cfg.RemoteUser, &cfg.RemotePort, &cfg.SSHKeyPath, &cfg.IntervalSecs)
	
	if err == nil {
		m.replConfig = &cfg
		ctx, cancel := context.WithCancel(context.Background())
		m.replCancel = cancel
		go m.startReplicationLoop(ctx, &cfg)
		log.Printf("HA: loaded persisted continuous replication configuration")
	}
}

// IsPatroniPrimary queries the local Patroni REST API to definitively determine PostgreSQL lock ownership.
func (m *Manager) IsPatroniPrimary() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:8008/primary")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// GetFencingConfig exposes STONITH read access on the Manager.
func (m *Manager) GetFencingConfig() (FencingConfig, error) {
	return GetFencingConfig(m.db)
}

// SaveFencingConfig exposes STONITH write access on the Manager.
func (m *Manager) SaveFencingConfig(cfg FencingConfig) error {
	return SaveFencingConfig(m.db, cfg)
}


