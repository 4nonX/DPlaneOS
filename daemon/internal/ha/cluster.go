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
// # Startup split-brain guard
//
// When HA is enabled, the daemon queries Patroni before automatic ZFS pool discovery: a non-200
// response from the local /health endpoint suppresses that discovery path so a replica does not
// import pools. Hot-plug import attempts also consult Patroni before importing.
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
	LocalNode          *ClusterNode   `json:"local_node"`
	Peers              []*ClusterNode `json:"peers"`
	Quorum             bool           `json:"quorum"`              // true if majority of nodes are reachable
	ActiveNode         *ClusterNode   `json:"active_node"`         // which node currently holds the active role
	HAEnabled          bool           `json:"ha_enabled"`          // true if Patroni/HAProxy is configured in NixOS
	MaintenanceActive  bool           `json:"maintenance_active"`
	MaintenanceUntil   int64          `json:"maintenance_until"`   // unix timestamp
	SubordinateMode    bool           `json:"subordinate_mode"`    // true if catching up stale data post-zombie boot
	HysteresisActive   bool           `json:"hysteresis_active"`   // true if flap-guard is suppressing auto-failover
	LastFailoverAt     int64          `json:"last_failover_at"`    // unix timestamp of last automated failover; 0 = never
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

	// Hysteresis: time of the last automated failover. Auto-failover is suppressed
	// for HysteresisWindow after a failover to prevent flapping on unstable networks.
	lastFailoverAt time.Time

	// subordinateMode is set at boot when this node detects it has stale ZFS data
	// vs an already-active peer. Auto-failover is disabled until catch-up completes.
	subordinateMode bool

	stopCh chan struct{}
	wg     sync.WaitGroup

	replProgressMu     sync.Mutex
	replProgressReport func(map[string]interface{})
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
	m.loadClusterState()
	m.StartupReconciliation()

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

	var lastFailoverUnix int64
	hysteresisActive := false
	if !m.lastFailoverAt.IsZero() {
		lastFailoverUnix = m.lastFailoverAt.Unix()
		hysteresisActive = time.Since(m.lastFailoverAt) < HysteresisWindow
	}

	return &ClusterStatus{
		LocalNode:         local,
		Peers:             peers,
		Quorum:            quorum,
		ActiveNode:        activeNode,
		HAEnabled:         m.replConfig != nil,
		MaintenanceActive: maintenanceActive,
		MaintenanceUntil:  m.maintenanceUntil.Unix(),
		SubordinateMode:   m.subordinateMode,
		HysteresisActive:  hysteresisActive,
		LastFailoverAt:    lastFailoverUnix,
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

// HysteresisWindow is the minimum time that must pass after a failover before another
// automated failover is permitted. Prevents the "ping-pong" flapping scenario where
// a dying switch causes repeated failovers and data stream interruptions.
// Operators can reset this early via POST /api/ha/clear_fault.
const HysteresisWindow = 60 * time.Minute

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
			cp := *n
			deadPeer = &cp
		}
	}
	lastFailoverAt := m.lastFailoverAt
	subordinateMode := m.subordinateMode
	m.mu.RUnlock()

	if !isStandby || deadPeer == nil {
		return
	}

	// Guard 1: Subordinate Mode — this node is still catching up stale data from
	// a zombie boot. Promoting while behind would serve outdated files.
	if subordinateMode {
		if deadPeer.MissedBeats == 3 {
			log.Printf("HA SUBORDINATE: Failover suppressed — node is in Subordinate (catch-up) Mode. Data sync must complete before auto-failover is safe.")
		}
		return
	}

	// Guard 2: Hysteresis — suppress auto-failover for HysteresisWindow after the last
	// failover to prevent flapping on an unstable network (e.g. dying core switch).
	if !lastFailoverAt.IsZero() && time.Since(lastFailoverAt) < HysteresisWindow {
		if deadPeer.MissedBeats == 3 {
			log.Printf("HA HYSTERESIS: Failover suppressed — last failover was %v ago (window: %v). Use POST /api/ha/clear_fault to override.",
				time.Since(lastFailoverAt).Truncate(time.Second), HysteresisWindow)
		}
		return
	}

	// Guard 3: Require at least one fencing method enabled.
	// IPMI and PDU are fetched now and captured for the goroutine.
	fencingCfg, _ := GetFencingConfig(m.db)
	pduCfg, _ := GetPDUConfig(m.db)
	if !fencingCfg.Enable && !pduCfg.Enable {
		if deadPeer.MissedBeats == 3 {
			log.Printf("HA STONITH: Peer %s breached FailoverThreshold but no fencing method (IPMI or PDU) is enabled. Automatic promotion aborted.", deadPeer.ID)
		}
		return
	}

	// Guard 4: Quorum Witness — prove this node is not isolated.
	witnessCfg, witnessErr := GetWitnessConfig(m.db)
	if witnessErr == nil && witnessCfg.Enable {
		if !canReachWitness(witnessCfg) {
			if deadPeer.MissedBeats == 3 {
				log.Printf("HA WITNESS: Peer %s unreachable but %d/%d witnesses also unreachable — node may be isolated. Automatic failover SUSPENDED.",
					deadPeer.ID, witnessCfg.RequiredHealthy, len(witnessCfg.Witnesses))
			}
			return
		}
		log.Printf("HA WITNESS: Peer %s unreachable, quorum witness confirmed reachable (%d/%d) — proceeding with automated failover.",
			deadPeer.ID, witnessCfg.RequiredHealthy, len(witnessCfg.Witnesses))
	}

	// Guard 5: Maintenance Mode.
	if m.IsMaintenanceActive() {
		if deadPeer.MissedBeats == 3 {
			log.Printf("HA STONITH: Peer %s breached FailoverThreshold but fencing SUSPENDED — active maintenance mode.", deadPeer.ID)
		}
		return
	}

	// Rate-limit: only one fencing sequence at a time.
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

		// Try IPMI BMC first; fall back to PDU if configured.
		fenced := false
		if fencingCfg.Enable {
			log.Printf("HA STONITH: Attempting IPMI fencing against dead peer %s", deadPeer.ID)
			if err := ExecuteFencing(deadPeer.ID, fencingCfg); err != nil {
				log.Printf("HA STONITH: IPMI fencing failed: %v. Trying PDU fallback.", err)
			} else {
				fenced = true
			}
		}
		if !fenced && pduCfg.Enable {
			log.Printf("HA STONITH: Attempting PDU outlet fencing against dead peer %s", deadPeer.ID)
			if err := ExecutePDUFencing(deadPeer.ID, pduCfg); err != nil {
				log.Printf("HA STONITH: PDU fencing also failed: %v. Aborting failover — refusing to promote without confirmed fence.", err)
				return
			}
			fenced = true
		}
		if !fenced {
			log.Printf("HA STONITH: No fencing method succeeded for peer %s. Aborting failover.", deadPeer.ID)
			return
		}

		// Record the failover timestamp for hysteresis tracking.
		m.mu.Lock()
		m.lastFailoverAt = time.Now()
		m.mu.Unlock()
		go m.persistClusterState()

		log.Printf("HA STONITH: Peer %s confirmed fenced. Promoting local node to active role.", deadPeer.ID)
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
	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_witness_config (
			id               INTEGER PRIMARY KEY CHECK (id = 1),
			enable           BOOLEAN NOT NULL DEFAULT FALSE,
			witnesses_json   TEXT    NOT NULL DEFAULT '[]',
			required_healthy INTEGER NOT NULL DEFAULT 1,
			timeout_secs     INTEGER NOT NULL DEFAULT 5
		)
	`); err != nil {
		return err
	}

	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_pdu_config (
			id              INTEGER PRIMARY KEY CHECK (id = 1),
			enable          BOOLEAN NOT NULL DEFAULT FALSE,
			outlet_off_url  TEXT    NOT NULL DEFAULT '',
			method          TEXT    NOT NULL DEFAULT 'GET',
			username        TEXT    NOT NULL DEFAULT '',
			password_file   TEXT    NOT NULL DEFAULT '',
			timeout_secs    INTEGER NOT NULL DEFAULT 10,
			expected_status INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return err
	}
	if _, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_cluster_state (
			id               INTEGER PRIMARY KEY CHECK (id = 1),
			last_failover_at TIMESTAMPTZ,
			subordinate_mode BOOLEAN NOT NULL DEFAULT FALSE
		)
	`); err != nil {
		return err
	}

	// Schema migrations: safe no-ops if columns already exist.
	m.db.Exec(`ALTER TABLE ha_fencing_config ADD COLUMN IF NOT EXISTS jitter_max_ms INTEGER NOT NULL DEFAULT 3000`)
	m.db.Exec(`ALTER TABLE ha_witness_config ADD COLUMN IF NOT EXISTS witnesses_json TEXT NOT NULL DEFAULT '[]'`)
	m.db.Exec(`ALTER TABLE ha_witness_config ADD COLUMN IF NOT EXISTS required_healthy INTEGER NOT NULL DEFAULT 1`)

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
		if err := rows.Scan(&n.ID, &n.Name, &n.Address, (*string)(&n.Role), (*string)(&n.State),
			&n.Version, &lastSeenUnix, &n.MissedBeats, &n.RegisteredAt); err != nil {
			log.Printf("HA: skipping malformed peer row: %v", err)
			continue
		}
		n.LastSeen = time.Unix(lastSeenUnix, 0)
		n.LastSeenUnix = lastSeenUnix
		// Re-ping will update State immediately; start as Unknown so we don't
		// inherit a stale StateUnreachable from a previous crash.
		n.State = StateUnknown
		m.nodes[n.ID] = n
	}
	if err := rows.Err(); err != nil {
		log.Printf("HA: error iterating persisted peers: %v", err)
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

// SetReplicationProgressReporter wires HA ZFS send/recv progress to the WebSocket hub (optional).
func (m *Manager) SetReplicationProgressReporter(f func(map[string]interface{})) {
	m.replProgressMu.Lock()
	m.replProgressReport = f
	m.replProgressMu.Unlock()
}

func (m *Manager) reportReplicationProgress(payload map[string]interface{}) {
	m.replProgressMu.Lock()
	f := m.replProgressReport
	m.replProgressMu.Unlock()
	if f == nil {
		return
	}
	cp := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		cp[k] = v
	}
	go f(cp)
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

// GetWitnessConfig exposes quorum witness read access on the Manager.
func (m *Manager) GetWitnessConfig() (WitnessConfig, error) {
	return GetWitnessConfig(m.db)
}

// SaveWitnessConfig exposes quorum witness write access on the Manager.
func (m *Manager) SaveWitnessConfig(cfg WitnessConfig) error {
	return SaveWitnessConfig(m.db, cfg)
}

// GetPDUConfig exposes PDU fencing read access on the Manager.
func (m *Manager) GetPDUConfig() (PDUConfig, error) {
	return GetPDUConfig(m.db)
}

// SavePDUConfig exposes PDU fencing write access on the Manager.
func (m *Manager) SavePDUConfig(cfg PDUConfig) error {
	return SavePDUConfig(m.db, cfg)
}

// GetSyncStatus returns the TXG state of all local ZFS pools for startup reconciliation.
func (m *Manager) GetSyncStatus() SyncStatus {
	m.mu.RLock()
	isActive := true
	for _, n := range m.nodes {
		if n.Role == RoleActive && n.State != StateUnreachable {
			isActive = false // we are standby if a healthy active peer exists
			break
		}
	}
	m.mu.RUnlock()
	return GetLocalSyncStatus(isActive)
}

// ClearFault resets the hysteresis timer and subordinate mode, re-enabling auto-failover.
// Called by the operator after investigating and resolving the root cause of a failover.
func (m *Manager) ClearFault() {
	m.mu.Lock()
	m.lastFailoverAt = time.Time{}
	m.subordinateMode = false
	m.mu.Unlock()
	m.persistClusterState()
	log.Printf("HA: Fault cleared by operator. Hysteresis and Subordinate Mode reset. Auto-failover re-enabled.")
}

// IsHysteresisActive reports whether the flap-guard is currently suppressing failovers.
func (m *Manager) IsHysteresisActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.lastFailoverAt.IsZero() && time.Since(m.lastFailoverAt) < HysteresisWindow
}

// IsSubordinateMode reports whether this node is in zombie catch-up mode.
func (m *Manager) IsSubordinateMode() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.subordinateMode
}

// loadClusterState restores hysteresis and subordinate mode state from the database.
func (m *Manager) loadClusterState() {
	var lastFailoverAt sql.NullTime
	var subordinateMode bool
	err := m.db.QueryRow(`
		SELECT last_failover_at, subordinate_mode
		FROM ha_cluster_state WHERE id = 1
	`).Scan(&lastFailoverAt, &subordinateMode)
	if err != nil {
		return
	}
	m.mu.Lock()
	if lastFailoverAt.Valid {
		m.lastFailoverAt = lastFailoverAt.Time
	}
	m.subordinateMode = subordinateMode
	m.mu.Unlock()
	if subordinateMode {
		log.Printf("HA: Loaded persisted Subordinate Mode — node was in catch-up state when last shut down.")
	}
	if !m.lastFailoverAt.IsZero() && time.Since(m.lastFailoverAt) < HysteresisWindow {
		log.Printf("HA: Hysteresis active — last failover was %v ago, auto-failover suppressed for %v more.",
			time.Since(m.lastFailoverAt).Truncate(time.Second),
			(HysteresisWindow - time.Since(m.lastFailoverAt)).Truncate(time.Second))
	}
}

// persistClusterState saves hysteresis and subordinate mode to the database.
func (m *Manager) persistClusterState() {
	m.mu.RLock()
	lastFailoverAt := m.lastFailoverAt
	subordinateMode := m.subordinateMode
	m.mu.RUnlock()

	var lastFailoverParam interface{}
	if !lastFailoverAt.IsZero() {
		lastFailoverParam = lastFailoverAt
	}

	m.db.Exec(`
		INSERT INTO ha_cluster_state (id, last_failover_at, subordinate_mode)
		VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET
			last_failover_at = EXCLUDED.last_failover_at,
			subordinate_mode = EXCLUDED.subordinate_mode
	`, lastFailoverParam, subordinateMode)
}


