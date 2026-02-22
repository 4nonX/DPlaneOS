package ha

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

func TestNewManager_LocalID(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	info := m.LocalInfo()
	if info["id"] != "node1" {
		t.Errorf("expected id=node1, got %q", info["id"])
	}
}

func TestRegisterPeer_Basic(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	if err := m.ensureSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}

	peer := &ClusterNode{
		ID:      "node2",
		Name:    "NAS-B",
		Address: "http://10.0.0.2:5050",
		Role:    RoleStandby,
	}
	if err := m.RegisterPeer(peer); err != nil {
		t.Fatalf("RegisterPeer: %v", err)
	}

	got, ok := m.GetPeer("node2")
	if !ok {
		t.Fatal("peer not found after register")
	}
	if got.Name != "NAS-B" {
		t.Errorf("expected NAS-B, got %q", got.Name)
	}
}

func TestRegisterPeer_RejectsSelf(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()

	err := m.RegisterPeer(&ClusterNode{ID: "node1", Address: "http://10.0.0.1:5050"})
	if err == nil {
		t.Fatal("expected error registering self, got nil")
	}
}

func TestRegisterPeer_RequiresAddress(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()

	err := m.RegisterPeer(&ClusterNode{ID: "node2"}) // no address
	if err == nil {
		t.Fatal("expected error for missing address, got nil")
	}
}

func TestRemovePeer(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()
	m.RegisterPeer(&ClusterNode{ID: "node2", Address: "http://10.0.0.2:5050"})

	if err := m.RemovePeer("node2"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	if _, ok := m.GetPeer("node2"); ok {
		t.Fatal("peer still present after remove")
	}
}

func TestStatus_NoPeers_LocalIsActive(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()

	s := m.Status()
	if s.LocalNode.Role != RoleActive {
		t.Errorf("expected local to be active with no peers, got %q", s.LocalNode.Role)
	}
	if !s.Quorum {
		t.Error("expected quorum with single node")
	}
	if s.ActiveNode == nil {
		t.Fatal("ActiveNode should not be nil")
	}
	if s.ActiveNode.ID != "node1" {
		t.Errorf("expected active node = node1, got %q", s.ActiveNode.ID)
	}
}

func TestHeartbeatReceived_UpdatesState(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()

	// Register peer first
	m.RegisterPeer(&ClusterNode{ID: "node2", Address: "http://10.0.0.2:5050", Role: RoleStandby})

	// Simulate received heartbeat
	m.HandleHeartbeat(HeartbeatPayload{
		NodeID:  "node2",
		Address: "http://10.0.0.2:5050",
		Role:    "standby",
		Version: "3.1.0",
	})

	peer, ok := m.GetPeer("node2")
	if !ok {
		t.Fatal("peer not found")
	}
	if peer.State != StateHealthy {
		t.Errorf("expected healthy after heartbeat, got %q", peer.State)
	}
	if peer.MissedBeats != 0 {
		t.Errorf("expected 0 missed beats, got %d", peer.MissedBeats)
	}
	if time.Since(peer.LastSeen) > 5*time.Second {
		t.Error("LastSeen should be recent")
	}
}

func TestHeartbeat_AutoRegistersUnknownPeer(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()

	// Heartbeat from unknown peer — should auto-register
	m.HandleHeartbeat(HeartbeatPayload{
		NodeID:  "node3",
		Address: "http://10.0.0.3:5050",
		Role:    "standby",
	})

	peer, ok := m.GetPeer("node3")
	if !ok {
		t.Fatal("peer should be auto-registered on heartbeat")
	}
	if peer.State != StateHealthy {
		t.Errorf("expected healthy, got %q", peer.State)
	}
}

func TestSetPeerRole(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()
	m.RegisterPeer(&ClusterNode{ID: "node2", Address: "http://10.0.0.2:5050", Role: RoleStandby})

	if err := m.SetPeerRole("node2", RoleActive); err != nil {
		t.Fatalf("SetPeerRole: %v", err)
	}
	peer, _ := m.GetPeer("node2")
	if peer.Role != RoleActive {
		t.Errorf("expected active, got %q", peer.Role)
	}
}

func TestSetPeerRole_UnknownPeer(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	m := NewManager(db, "node1", "http://10.0.0.1:5050", "3.1.0")
	m.ensureSchema()

	err := m.SetPeerRole("nonexistent", RoleActive)
	if err == nil {
		t.Fatal("expected error for unknown peer")
	}
}
