package configstore

import "testing"

func TestDefaultWinnerGroupScope(t *testing.T) {
	owner := Revision{Scope: ScopeGroup, ScopeID: "tank", Epoch: 4}
	former := Revision{Scope: ScopeGroup, ScopeID: "tank", Epoch: 3}
	if w, why := DefaultWinner(owner, former); w != WinnerLocal || why == "" {
		t.Fatalf("owner's version must win here: %q %q", w, why)
	}
	// The other node computes the mirror verdict from the same revisions.
	if w, _ := DefaultWinner(former, owner); w != WinnerRemote {
		t.Fatalf("mirror: %q", w)
	}
	// Written before the pool was in a group (epoch 0): the owner's wins.
	if w, _ := DefaultWinner(Revision{Scope: ScopeGroup, ScopeID: "tank"}, owner); w != WinnerRemote {
		t.Fatalf("epoch 0 against an owner: %q", w)
	}
	if w, _ := DefaultWinner(owner, owner); w != WinnerNone {
		t.Fatalf("equal epochs go to review: %q", w)
	}
}

func TestDefaultWinnerClusterScope(t *testing.T) {
	coord := Revision{Scope: ScopeCluster, Epoch: 0x21}
	member := Revision{Scope: ScopeCluster}
	later := Revision{Scope: ScopeCluster, Epoch: 0x30}
	if w, _ := DefaultWinner(member, coord); w != WinnerRemote {
		t.Fatalf("the coordinator's version must win: %q", w)
	}
	if w, _ := DefaultWinner(coord, later); w != WinnerRemote {
		t.Fatalf("the later term must win: %q", w)
	}
	// Neither written by a coordinator (paired nodes without a cluster, or
	// both changed while isolated): the operator decides.
	if w, _ := DefaultWinner(member, member); w != WinnerNone {
		t.Fatalf("no authority on either side: %q", w)
	}
}

func TestDefaultWinnerNeverForNodeScopeOrMixedScopes(t *testing.T) {
	if w, _ := DefaultWinner(Revision{Scope: ScopeNode, Epoch: 2}, Revision{Scope: ScopeNode, Epoch: 1}); w != WinnerNone {
		t.Fatalf("node scope: %q", w)
	}
	if w, _ := DefaultWinner(Revision{Scope: ScopeGroup, Epoch: 2}, Revision{Scope: ScopeCluster, Epoch: 1}); w != WinnerNone {
		t.Fatalf("mixed scopes: %q", w)
	}
}

func TestEpochForStampsCoordinatorTermOnClusterScopeOnly(t *testing.T) {
	prevTerm, prevPool := CoordinatorTerm, EpochForPool
	defer func() { CoordinatorTerm, EpochForPool = prevTerm, prevPool }()
	CoordinatorTerm = func() int64 { return 9 }
	EpochForPool = func(pool string) int64 { return 5 }
	if got := epochFor(ScopeCluster, ""); got != 9 {
		t.Errorf("cluster: %d", got)
	}
	if got := epochFor(ScopeGroup, "tank"); got != 5 {
		t.Errorf("group: %d", got)
	}
	if got := epochFor(ScopeGroup, ""); got != 0 {
		t.Errorf("group without pool: %d", got)
	}
	if got := epochFor(ScopeNode, "n"); got != 0 {
		t.Errorf("node: %d", got)
	}
}
