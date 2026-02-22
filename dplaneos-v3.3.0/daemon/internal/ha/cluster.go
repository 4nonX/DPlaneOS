// Package ha provides cluster node registration and health monitoring for D-PlaneOS.
//
// Architecture: passive active/standby. One node holds ZFS pools ("active").
// Standby nodes heartbeat the active node. If heartbeat fails, standby alerts
// and can be manually (or auto-) promoted via the API.
//
// This is NOT Pacemaker/Corosync — it is a lightweight coordination layer
// that prevents accidental pool imports on standby nodes and provides a clean
// manual failover workflow.
package ha

import (
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
	ActiveNode  *ClusterNode   `json:"active_node"`  // which node currently holds the active role
	LastUpdated time.Time      `json:"last_updated"`
}

// Manager owns the cluster state for this node.
type Manager struct {
	db        *sql.DB
	localID   string
	localAddr string
	version   string

	mu    sync.RWMutex
	nodes map[string]*ClusterNode // keyed by node ID

	stopCh chan struct{}
}

// NewManager creates a cluster manager for this daemon instance.
//   localID   — unique ID for this node (use hostname or UUID from /etc/machine-id)
//   localAddr — how peers reach this daemon, e.g. "http://10.0.0.1:5050"
//   version   — daemon version string
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
	go m.heartbeatLoop()
	log.Printf("HA: cluster manager started (local=%s)", m.localID)
}

// Stop halts background goroutines.
func (m *Manager) Stop() { close(m.stopCh) }

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
	_, err := m.db.Exec("DELETE FROM ha_nodes WHERE node_id = ?", id)
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

	return &ClusterStatus{
		LocalNode:   local,
		Peers:       peers,
		Quorum:      quorum,
		ActiveNode:  activeNode,
		LastUpdated: time.Now(),
	}
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

// heartbeatLoop pings all peers every 15 seconds.
func (m *Manager) heartbeatLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.pingAllPeers()
		}
	}
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

	if err != nil || resp.StatusCode != http.StatusOK {
		node.MissedBeats++
		if node.MissedBeats >= 2 {
			node.State = StateUnreachable
			log.Printf("HA: peer %s is UNREACHABLE (missed %d beats)", id, node.MissedBeats)
		}
	} else {
		resp.Body.Close()
		// Try to parse version from health response
		node.State = StateHealthy
		node.LastSeen = time.Now()
		node.LastSeenUnix = time.Now().Unix()
		node.MissedBeats = 0
	}
	// Persist updated state
	go m.persistNode(node)
}

// ── Database ────────────────────────────────────────────────────────────────

func (m *Manager) ensureSchema() error {
	_, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS ha_nodes (
			node_id       TEXT PRIMARY KEY,
			name          TEXT NOT NULL DEFAULT '',
			address       TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'standby',
			state         TEXT NOT NULL DEFAULT 'unknown',
			version       TEXT NOT NULL DEFAULT '',
			last_seen     INTEGER NOT NULL DEFAULT 0,
			missed_beats  INTEGER NOT NULL DEFAULT 0,
			registered_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	return err
}

func (m *Manager) persistNode(n *ClusterNode) error {
	_, err := m.db.Exec(`
		INSERT INTO ha_nodes (node_id, name, address, role, state, version, last_seen, missed_beats, registered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			name=excluded.name, address=excluded.address, role=excluded.role,
			state=excluded.state, version=excluded.version,
			last_seen=excluded.last_seen, missed_beats=excluded.missed_beats
	`, n.ID, n.Name, n.Address, string(n.Role), string(n.State),
		n.Version, n.LastSeen.Unix(), n.MissedBeats, n.RegisteredAt.Format(time.RFC3339))
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
		var registeredStr string
		rows.Scan(&n.ID, &n.Name, &n.Address, (*string)(&n.Role), (*string)(&n.State),
			&n.Version, &lastSeenUnix, &n.MissedBeats, &registeredStr)
		n.LastSeen = time.Unix(lastSeenUnix, 0)
		n.LastSeenUnix = lastSeenUnix
		n.RegisteredAt, _ = time.Parse(time.RFC3339, registeredStr)
		// Mark as unknown on load — we'll re-ping immediately
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
