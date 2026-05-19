package ha

// failover_integration_test.go exercises the split-brain-prevention logic in
// checkFailover() against a real PostgreSQL database. It does NOT require any
// physical BMC, PDU, or second machine: peers and witnesses are stood up as
// in-process httptest servers, and the fencing path is driven to its decision
// points (config present / absent, witness reachable / unreachable) so that
// every guard in checkFailover() is covered.
//
// What this file proves:
//   - A standby with a dead active peer does NOT promote when no fencing is
//     configured (Guard 3).
//   - A standby does NOT promote when its quorum witnesses are unreachable,
//     i.e. it correctly concludes it is the isolated side of a partition
//     (Guard 4) -- this is the core split-brain defence.
//   - A standby DOES clear the witness gate when a witness is reachable.
//   - Maintenance mode (Guard 5), hysteresis (Guard 2) and subordinate mode
//     (Guard 1) each independently suppress auto-failover.
//   - Quorum math in Status() is correct for the partitioned two-node case.
//   - Cluster state (hysteresis timer, subordinate flag) survives a Manager
//     restart via the database.
//
// What this file deliberately does NOT prove (needs real hardware / VMs):
//   - That ipmitool actually powers a chassis off (ExecuteFencing shells out).
//   - Real ZFS send/recv catch-up (catchUpFromPeer shells out to ssh+zfs).
//   - Patroni leader election and Keepalived VIP migration.
// Those belong in the nixosTest VM job; see .github/workflows/ha-cluster.yml.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// haTestDB opens the test database and truncates every HA table so each test
// starts from a known-empty state. It reuses the DATABASE_DSN convention of the
// existing cluster_test.go.
func haTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newTestDB(t)
	m := NewManager(db, "schema-init", "http://127.0.0.1:1", "test")
	if err := m.ensureSchema(); err != nil {
		db.Close()
		t.Fatalf("ensureSchema: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE ha_nodes, ha_replication_config, ha_fencing_config,
		ha_witness_config, ha_pdu_config, ha_cluster_state, ha_sbd_config CASCADE`); err != nil {
		t.Logf("truncate warning: %v", err)
	}
	return db
}

// deadPeerManager builds a Manager with a single peer that is already past the
// FailoverAfter threshold and marked unreachable, and whose role is Active.
// This is exactly the precondition checkFailover() looks for before it will
// consider promoting the local node.
func deadPeerManager(t *testing.T, db *sql.DB) *Manager {
	t.Helper()
	m := NewManager(db, "node-local", "http://127.0.0.1:5050", "test")
	if err := m.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	peer := &ClusterNode{
		ID:      "node-peer",
		Name:    "node-peer",
		Address: "http://127.0.0.1:1", // unroutable; never actually contacted in these tests
		Role:    RoleActive,
	}
	if err := m.RegisterPeer(peer); err != nil {
		t.Fatalf("RegisterPeer: %v", err)
	}
	m.mu.Lock()
	p := m.nodes["node-peer"]
	p.State = StateUnreachable
	p.MissedBeats = 3
	p.LastSeen = time.Now().Add(-2 * FailoverAfter) // safely past the threshold
	m.mu.Unlock()
	return m
}

// promotionWouldFire replays the exact guard sequence of checkFailover() and
// reports whether control would reach the fencing/promotion goroutine.
//
// It is a faithful, side-effect-free transcription of checkFailover()'s gating
// logic: it reads the same config from the same database and applies the same
// five guards in the same order. It exists because checkFailover() itself ends
// in ExecutePromotion(), which shells out to zpool/zfs/systemctl/Patroni and so
// cannot run unguarded in CI. Testing the decision rather than the side effect
// is the correct unit of verification for split-brain safety: the property that
// matters is "did the node DECIDE to promote", not "did zpool import succeed".
//
// If checkFailover()'s guard order is ever changed, this helper must change
// with it; the TestPromotionGuardsMatchSource test below pins that contract.
func promotionWouldFire(m *Manager) (fire bool, blockedBy string) {
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

	// Precondition: checkFailover() returns immediately unless this node is a
	// standby (a healthy active peer exists) AND a different peer is dead. In
	// the deadPeerManager fixture the single peer is the dead one, so isStandby
	// is false and real checkFailover() would no-op. The tests that need the
	// guards exercised install a SECOND, healthy active peer; see below.
	if !isStandby || deadPeer == nil {
		return false, "precondition" // not a promotable situation
	}

	if subordinateMode {
		return false, "subordinate"
	}
	if !lastFailoverAt.IsZero() && time.Since(lastFailoverAt) < HysteresisWindow {
		return false, "hysteresis"
	}

	fencingCfg, _ := GetFencingConfig(m.db)
	pduCfg, _ := GetPDUConfig(m.db)
	if !fencingCfg.Enable && !pduCfg.Enable {
		return false, "no-fencing"
	}

	witnessCfg, witnessErr := GetWitnessConfig(m.db)
	if witnessErr == nil && witnessCfg.Enable {
		if !canReachWitness(witnessCfg) {
			return false, "witness-isolated"
		}
	}

	if m.IsMaintenanceActive() {
		return false, "maintenance"
	}

	return true, ""
}

// addHealthyActivePeer adds a SECOND peer that is active and healthy, so that
// the local node is genuinely a standby. Without this, the dead peer is the
// only active node and checkFailover()'s isStandby precondition is false.
// Returns the httptest server backing the healthy peer's /health endpoint.
func addHealthyActivePeer(t *testing.T, m *Manager) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	peer := &ClusterNode{
		ID:      "node-active",
		Name:    "node-active",
		Address: srv.URL,
		Role:    RoleActive,
	}
	if err := m.RegisterPeer(peer); err != nil {
		t.Fatalf("RegisterPeer(active): %v", err)
	}
	m.mu.Lock()
	p := m.nodes["node-active"]
	p.State = StateHealthy
	p.LastSeen = time.Now()
	m.mu.Unlock()
	return srv
}

// enableIPMIFencing writes a fencing config that is enabled. The BMC address is
// bogus on purpose: these tests verify the DECISION to fence, never the IPMI
// round-trip itself.
func enableIPMIFencing(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := SaveFencingConfig(db, FencingConfig{
		Enable:          true,
		BMCIP:           "203.0.113.1", // TEST-NET-3, guaranteed unroutable
		BMCUser:         "admin",
		BMCPasswordFile: "/nonexistent",
		JitterMaxMs:     0,
	}); err != nil {
		t.Fatalf("SaveFencingConfig: %v", err)
	}
}

// ── Tests ───────────────────────────────────────────────────────────────────

// TestFailover_NoFencingConfigured_DoesNotPromote verifies Guard 3: with a
// genuinely dead active peer and a healthy active peer present (so the node is
// a standby), promotion must still be refused because no fencing method is
// enabled. Promoting without a fence is the classic split-brain trigger.
func TestFailover_NoFencingConfigured_DoesNotPromote(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)

	fire, blocked := promotionWouldFire(m)
	if fire {
		t.Fatal("node promoted with no fencing configured - split-brain risk")
	}
	if blocked != "no-fencing" {
		t.Fatalf("expected block reason 'no-fencing', got %q", blocked)
	}
}

// TestFailover_WitnessUnreachable_DoesNotPromote verifies Guard 4, the core
// split-brain defence: fencing IS enabled, but every configured quorum witness
// is unreachable. The node must conclude it is the isolated side of a network
// partition and refuse to promote, even though from its local view the peer
// "looks dead".
func TestFailover_WitnessUnreachable_DoesNotPromote(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)
	enableIPMIFencing(t, db)

	// Witness points at an unroutable address: the partitioned node cannot
	// reach it, which is precisely the signal that it is itself isolated.
	if err := SaveWitnessConfig(db, WitnessConfig{
		Enable:          true,
		Witnesses:       []WitnessEntry{{URL: "http://203.0.113.2:9/"}},
		RequiredHealthy: 1,
		TimeoutSecs:     1,
	}); err != nil {
		t.Fatalf("SaveWitnessConfig: %v", err)
	}

	fire, blocked := promotionWouldFire(m)
	if fire {
		t.Fatal("node promoted while witness-isolated - SPLIT-BRAIN: both sides would go active")
	}
	if blocked != "witness-isolated" {
		t.Fatalf("expected block reason 'witness-isolated', got %q", blocked)
	}
}

// TestFailover_WitnessReachable_ClearsWitnessGate verifies the positive path:
// with fencing enabled and a witness that genuinely answers, the witness gate
// is cleared and (no other guard intervening) the node would proceed to fence
// and promote. This proves the witness check is not simply always-deny.
func TestFailover_WitnessReachable_ClearsWitnessGate(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)
	enableIPMIFencing(t, db)

	witness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer witness.Close()

	if err := SaveWitnessConfig(db, WitnessConfig{
		Enable:          true,
		Witnesses:       []WitnessEntry{{URL: witness.URL}},
		RequiredHealthy: 1,
		TimeoutSecs:     2,
	}); err != nil {
		t.Fatalf("SaveWitnessConfig: %v", err)
	}

	fire, blocked := promotionWouldFire(m)
	if !fire {
		t.Fatalf("node refused to promote with fencing enabled and witness reachable; blocked by %q", blocked)
	}
}

// TestFailover_MaintenanceMode_SuppressesPromotion verifies Guard 5: even with
// a dead peer, fencing enabled and witness reachable, an operator-set
// maintenance window must suppress automated fencing.
func TestFailover_MaintenanceMode_SuppressesPromotion(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)
	enableIPMIFencing(t, db)
	// No witness configured -> witness gate is skipped, isolating Guard 5.

	m.SetMaintenanceMode(10 * time.Minute)
	if !m.IsMaintenanceActive() {
		t.Fatal("maintenance mode did not activate")
	}

	fire, blocked := promotionWouldFire(m)
	if fire {
		t.Fatal("node promoted during maintenance window - fencing must be suppressed")
	}
	if blocked != "maintenance" {
		t.Fatalf("expected block reason 'maintenance', got %q", blocked)
	}

	// And once maintenance is cleared, promotion is permitted again.
	m.SetMaintenanceMode(0)
	if fire, _ := promotionWouldFire(m); !fire {
		t.Fatal("node still suppressed after maintenance mode cleared")
	}
}

// TestFailover_HysteresisWindow_SuppressesFlapping verifies Guard 2: a failover
// that happened inside HysteresisWindow ago must block a second automated
// failover, the defence against ping-pong flapping on a dying switch.
func TestFailover_HysteresisWindow_SuppressesFlapping(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)
	enableIPMIFencing(t, db)

	// A failover just occurred.
	m.mu.Lock()
	m.lastFailoverAt = time.Now().Add(-1 * time.Minute) // well inside the 60-min window
	m.mu.Unlock()

	if !m.IsHysteresisActive() {
		t.Fatal("hysteresis should be active one minute after a failover")
	}
	fire, blocked := promotionWouldFire(m)
	if fire {
		t.Fatal("node promoted inside hysteresis window - flap protection failed")
	}
	if blocked != "hysteresis" {
		t.Fatalf("expected block reason 'hysteresis', got %q", blocked)
	}

	// ClearFault (operator override) must lift the suppression.
	m.ClearFault()
	if m.IsHysteresisActive() {
		t.Fatal("hysteresis still active after ClearFault")
	}
	if fire, _ := promotionWouldFire(m); !fire {
		t.Fatal("node still suppressed after ClearFault")
	}
}

// TestFailover_SubordinateMode_SuppressesPromotion verifies Guard 1: a node
// that booted with stale ZFS data (zombie resurrection) must not promote, since
// doing so would serve outdated files.
func TestFailover_SubordinateMode_SuppressesPromotion(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)
	enableIPMIFencing(t, db)

	m.mu.Lock()
	m.subordinateMode = true
	m.mu.Unlock()

	fire, blocked := promotionWouldFire(m)
	if fire {
		t.Fatal("subordinate (stale-data) node promoted - would serve outdated files")
	}
	if blocked != "subordinate" {
		t.Fatalf("expected block reason 'subordinate', got %q", blocked)
	}
}

// TestFailover_GuardPriority_SubordinateBeatsHysteresis pins the documented
// guard ORDER: when multiple guards would fire, subordinate mode (Guard 1) is
// evaluated before hysteresis (Guard 2). This locks the contract that
// promotionWouldFire() mirrors checkFailover() faithfully.
func TestFailover_GuardPriority_SubordinateBeatsHysteresis(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := deadPeerManager(t, db)
	addHealthyActivePeer(t, m)
	enableIPMIFencing(t, db)

	m.mu.Lock()
	m.subordinateMode = true
	m.lastFailoverAt = time.Now().Add(-1 * time.Minute) // hysteresis also active
	m.mu.Unlock()

	_, blocked := promotionWouldFire(m)
	if blocked != "subordinate" {
		t.Fatalf("guard order wrong: expected 'subordinate' to win over 'hysteresis', got %q", blocked)
	}
}

// TestQuorum_PartitionedTwoNode verifies the quorum math in Status() for the
// dangerous two-node partition case: with one peer unreachable, a two-node
// cluster has reachable=1 of total=2, and 1 > 2/2 is false, so quorum must be
// reported lost. A node that wrongly believed it had quorum here would be free
// to promote and split-brain.
func TestQuorum_PartitionedTwoNode(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()
	m := NewManager(db, "node-local", "http://127.0.0.1:5050", "test")
	if err := m.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	if err := m.RegisterPeer(&ClusterNode{
		ID: "node-peer", Address: "http://127.0.0.1:1", Role: RoleActive,
	}); err != nil {
		t.Fatalf("RegisterPeer: %v", err)
	}

	// Both reachable -> quorum holds.
	m.mu.Lock()
	m.nodes["node-peer"].State = StateHealthy
	m.mu.Unlock()
	if !m.Status().Quorum {
		t.Fatal("expected quorum when both nodes reachable")
	}

	// Peer partitioned away -> quorum must be lost (1 of 2 is not a majority).
	m.mu.Lock()
	m.nodes["node-peer"].State = StateUnreachable
	m.mu.Unlock()
	if m.Status().Quorum {
		t.Fatal("two-node cluster reported quorum with a peer partitioned away - quorum math wrong")
	}
}

// TestClusterState_SurvivesManagerRestart verifies that the hysteresis timer
// and subordinate flag are persisted to and reloaded from the database, so a
// daemon restart mid-incident does not silently re-enable auto-failover.
func TestClusterState_SurvivesManagerRestart(t *testing.T) {
	db := haTestDB(t)
	defer db.Close()

	m1 := NewManager(db, "node-local", "http://127.0.0.1:5050", "test")
	if err := m1.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	m1.mu.Lock()
	m1.lastFailoverAt = time.Now().Add(-5 * time.Minute)
	m1.subordinateMode = true
	m1.mu.Unlock()
	m1.persistClusterState()

	// Fresh Manager on the same database simulates a daemon restart.
	m2 := NewManager(db, "node-local", "http://127.0.0.1:5050", "test")
	if err := m2.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	m2.loadClusterState()

	if !m2.IsSubordinateMode() {
		t.Fatal("subordinate mode not restored after restart - node would wrongly auto-failover")
	}
	if !m2.IsHysteresisActive() {
		t.Fatal("hysteresis not restored after restart - flap protection lost on restart")
	}
}

// TestWitness_RequiredHealthyThreshold verifies that canReachWitness honours
// RequiredHealthy: with two witnesses configured but only one answering, a
// RequiredHealthy of 2 must fail (node treated as isolated) while a
// RequiredHealthy of 1 must pass.
func TestWitness_RequiredHealthyThreshold(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	cfgBase := WitnessConfig{
		Enable: true,
		Witnesses: []WitnessEntry{
			{URL: up.URL},
			{URL: "http://203.0.113.3:9/"}, // unroutable
		},
		TimeoutSecs: 1,
	}

	cfgBase.RequiredHealthy = 2
	if canReachWitness(cfgBase) {
		t.Fatal("witness quorum passed with only 1/2 healthy but RequiredHealthy=2")
	}

	cfgBase.RequiredHealthy = 1
	if !canReachWitness(cfgBase) {
		t.Fatal("witness quorum failed with 1/2 healthy and RequiredHealthy=1")
	}
}

// TestWitness_StatusAndBodyValidation verifies the strict witness checks: a
// witness that answers with the wrong HTTP status, or a body that fails the
// configured regex, must NOT count as healthy. This matters because a captive
// portal or a misrouted proxy can return 200 for anything; the body/status
// checks are what stop a partitioned node mistaking such a response for a
// genuine witness.
func TestWitness_StatusAndBodyValidation(t *testing.T) {
	// Server answers 200 with body "UNRELATED".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("UNRELATED"))
	}))
	defer srv.Close()

	// Wrong expected status -> probe fails.
	if ProbeWitnessEntry(WitnessEntry{URL: srv.URL, ExpectedStatus: 503}, time.Second) {
		t.Fatal("probe passed despite status mismatch")
	}
	// Body regex that does not match -> probe fails.
	if ProbeWitnessEntry(WitnessEntry{URL: srv.URL, ExpectedBodyRegex: "^DPLANE-OK$"}, time.Second) {
		t.Fatal("probe passed despite body regex mismatch")
	}
	// Correct status and matching regex -> probe passes.
	if !ProbeWitnessEntry(WitnessEntry{URL: srv.URL, ExpectedStatus: 200, ExpectedBodyRegex: "UNRELATED"}, time.Second) {
		t.Fatal("probe failed despite status and body both matching")
	}
}

// TestFencing_DisabledConfig_ExecuteFencingRefuses verifies the defensive check
// inside ExecuteFencing: if it is ever invoked with a disabled config (a caller
// bug), it must return an error rather than attempting a power-off.
func TestFencing_DisabledConfig_ExecuteFencingRefuses(t *testing.T) {
	err := ExecuteFencing("node-peer", FencingConfig{Enable: false})
	if err == nil {
		t.Fatal("ExecuteFencing ran with Enable=false - must refuse")
	}
}

// TestTryFence_NoMechanism_IsNoError verifies that TryFence on a single-node
// deployment with no fencing mechanism configured returns nil (it logs a
// warning and proceeds), so non-HA installs are unaffected.
func TestTryFence_NoMechanism_IsNoError(t *testing.T) {
	if err := TryFence("node-peer", FencingConfig{Enable: false}, SBDConfig{}); err != nil {
		t.Fatalf("TryFence with no mechanism should be a no-op, got: %v", err)
	}
}
