package handlers

import (
	"testing"
	"time"

	"dplaned/internal/configstore"
	"dplaned/internal/groups"
	"dplaned/internal/ha"
	"dplaned/internal/quorum"
)

// The map shows each node once, with what this node knows of it, and each
// group with its owner by name, its place in the takeover order and how far
// its copies are behind.
func TestBuildClusterMap(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	contact := now

	in := mapInput{
		Self: "key-a", SelfName: "nas-a", Now: now,
		Cfg: &quorum.Config{ClusterName: "c", Nodes: []quorum.Node{
			{ID: 1, Name: "nas-a", Addr: "10.0.0.1", NodeKey: "key-a"},
			{ID: 2, Name: "nas-b", Addr: "10.0.0.2", NodeKey: "key-b"},
			{ID: 3, Name: "nas-c", Addr: "10.0.0.3", NodeKey: "key-c"},
			{ID: 4, Name: "pi", Addr: "10.0.0.9", Voter: true},
		}},
		Status: quorum.Status{Running: true, Quorate: true, Members: []quorum.Member{{Name: "nas-a"}, {Name: "nas-b"}, {Name: "pi"}}},
		Online: []string{"key-a", "key-b"},
		Peers: []configstore.Peer{
			{ID: "key-b", Name: "nas-b", LastContact: &contact},
			{ID: "key-c", Name: "nas-c", LastContact: &contact, UnreachableSince: ago(10 * time.Minute)},
			{ID: "key-d", Name: "nas-d", LastContact: &contact}, // paired, not in the cluster
		},
		Targets:            []ha.PowerTarget{{Node: "key-b", Enabled: true}, {Node: "key-c", Enabled: false}},
		PeersInMaintenance: []string{"key-b"},
		Groups: []groups.GroupStatus{
			{Group: groups.Group{Name: "vault", Topology: groups.Replicated, Owner: "key-a", Epoch: 4,
				Candidates: []string{"key-a", "key-b"}, Pools: []groups.PoolRef{{Name: "tank"}}},
				Status: groups.Status{Role: groups.RoleOwner, CanWrite: true},
				Replication: []groups.ReplState{
					{Direction: "out", Peer: "key-b", Pool: "tank", LastOKAt: ago(90 * time.Second)},
					{Direction: "out", Peer: "key-c", Pool: "tank", LastError: "unreachable"},
				}},
			{Group: groups.Group{Name: "archive", Topology: groups.Shared, Owner: "key-c", Epoch: 2,
				Candidates: []string{"key-c", "key-a"}, Pools: []groups.PoolRef{{Name: "cold"}}},
				Status:   groups.Status{Role: groups.RoleStandby, Problems: []string{"the owner is not reachable"}},
				Failover: groups.Decision{Action: "wait", Reason: "taking over at 12:01:00"}},
		},
	}
	m := buildClusterMap(in)

	byName := map[string]MapNode{}
	for _, n := range m.Nodes {
		if _, dup := byName[n.Name]; dup {
			t.Errorf("%s is on the map twice", n.Name)
		}
		byName[n.Name] = n
	}
	if len(m.Nodes) != 5 || !m.Nodes[0].Self {
		t.Fatalf("nodes (this node first): %+v", m.Nodes)
	}
	if n := byName["nas-a"]; n.State != "online" || n.PowerFencing != "" || len(n.Owns) != 1 || n.Owns[0] != "vault" {
		t.Errorf("this node: %+v", n)
	}
	if n := byName["nas-b"]; n.State != "online" || !n.Maintenance || n.PowerFencing != "ok" {
		t.Errorf("nas-b (online, in maintenance, can be powered off): %+v", n)
	}
	if n := byName["nas-c"]; n.State != "away" || n.Since == nil || n.PowerFencing != "off" || len(n.Owns) != 1 {
		t.Errorf("nas-c (away, its power target switched off, owns archive): %+v", n)
	}
	if n := byName["pi"]; !n.Voter || n.State != "online" || n.PowerFencing != "" {
		t.Errorf("the voter: %+v", n)
	}
	if n := byName["nas-d"]; n.State != "paired" {
		t.Errorf("a paired node outside the cluster: %+v", n)
	}

	if len(m.Groups) != 2 || m.Groups[0].Name != "archive" {
		t.Fatalf("groups: %+v", m.Groups)
	}
	archive, vault := m.Groups[0], m.Groups[1]
	if archive.OwnerName != "nas-c" || archive.Serving || archive.Role != "standby" || archive.Candidates[1] != "nas-a" ||
		archive.Failover.Action != "wait" || len(archive.Problems) != 1 {
		t.Errorf("archive: %+v", archive)
	}
	if !vault.Serving || vault.OwnerName != "nas-a" || len(vault.Replication) != 2 {
		t.Fatalf("vault: %+v", vault)
	}
	if r := vault.Replication[0]; r.PeerName != "nas-b" || r.LagSeconds != 90 {
		t.Errorf("copy on nas-b: %+v", r)
	}
	if r := vault.Replication[1]; r.LagSeconds != -1 || r.Error == "" {
		t.Errorf("a copy that never arrived has no lag to show: %+v", r)
	}
	if m.ThirdVote["kind"] != "member" {
		t.Errorf("third vote: %+v", m.ThirdVote)
	}

	// No cluster: this node and the nodes it is paired with.
	alone := buildClusterMap(mapInput{Self: "key-a", SelfName: "nas-a", Now: now,
		Peers: []configstore.Peer{{ID: "key-b", Name: "nas-b"}}})
	if len(alone.Nodes) != 2 || !alone.Nodes[0].Self || alone.Nodes[1].State != "unreachable" || alone.ThirdVote["kind"] != "none" {
		t.Errorf("without a cluster: %+v %+v", alone.Nodes, alone.ThirdVote)
	}
	if alone.Groups == nil {
		t.Error("groups must be an empty list, not null")
	}
}
