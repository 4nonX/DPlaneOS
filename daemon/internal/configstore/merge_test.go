package configstore

import "testing"

func rev(uid, base, merge string, payload map[string]any) *Revision {
	return &Revision{UID: uid, BaseUID: base, MergeUID: merge, Payload: payload}
}

func TestClassify(t *testing.T) {
	v1 := map[string]any{"comment": "one"}
	v2 := map[string]any{"comment": "two"}
	v3 := map[string]any{"comment": "three"}

	// a <- b (remote changed) ; a <- c (local changed)
	a := rev("a", "", "", v1)
	b := rev("b", "a", "", v2)
	c := rev("c", "a", "", v3)
	cSame := rev("c2", "a", "", v2)
	m := rev("m", "c", "b", v3) // local resolved the conflict

	anc := Ancestry{}
	for _, r := range []*Revision{a, b, c, cSame, m} {
		anc.Add(*r)
	}

	cases := []struct {
		name          string
		local, remote *Revision
		want          MergeAction
	}{
		{"same revision", a, a, MergeNone},
		{"remote changed, local unchanged", a, b, MergeFastForward},
		{"local changed, remote unchanged", c, a, MergeNone},
		{"both changed differently", c, b, MergeConflict},
		{"both changed to the same value", cSame, b, MergeConverged},
		{"unknown here", nil, b, MergeFastForward},
		{"deleted there, unknown here", nil, rev("d", "", "", nil), MergeRecord},
		{"local merge includes remote", m, b, MergeNone},
		{"remote merge kept our value", c, m, MergeRecord},
		{"remote merge with an edited value", c, rev("m2", "c", "b", map[string]any{"comment": "edited"}), MergeFastForward},
		{"remote ahead with equal payload", a, rev("e", "a", "", v1), MergeRecord},
		{"independent histories, equal", rev("x", "", "", v1), rev("y", "", "", v1), MergeConverged},
		{"independent histories, different", rev("x", "", "", v1), rev("y", "", "", v2), MergeConflict},
		{"local deleted, remote changed", rev("del", "a", "", nil), b, MergeConflict},
	}
	for _, tc := range cases {
		anc.Add(*tc.remote)
		if tc.local != nil {
			anc.Add(*tc.local)
		}
		if got := Classify(tc.local, tc.remote, anc); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestAncestryCycleSafe(t *testing.T) {
	anc := Ancestry{"a": {"b", ""}, "b": {"a", ""}}
	if anc.IsAncestor("z", "a") {
		t.Error("unexpected ancestor")
	}
}

func TestCaptureSkipsPoolsNotImported(t *testing.T) {
	latest := map[string]Revision{
		"dataset/tank/media": {ID: 1, Kind: KindDataset, Key: "tank/media", Scope: ScopeGroup, ScopeID: "tank", Payload: map[string]any{"name": "tank/media"}},
		"dataset/fast/vm":    {ID: 2, Kind: KindDataset, Key: "fast/vm", Scope: ScopeGroup, ScopeID: "fast", Payload: map[string]any{"name": "fast/vm"}},
	}
	// Pool "tank" exported: its datasets are out of view, not deleted.
	// Pool "fast" imported and the dataset is gone: deleted.
	changes := changesAgainst(nil, latest, map[string]bool{"fast": true})
	if len(changes) != 1 || changes[0].Key != "fast/vm" || changes[0].Payload != nil {
		t.Fatalf("changes = %+v, want only the deletion of fast/vm", changes)
	}
}

func TestStaleWriterRefused(t *testing.T) {
	old := EpochForPool
	defer func() { EpochForPool = old }()
	EpochForPool = func(pool string) int64 {
		if pool == "tank" {
			return 3
		}
		return 0
	}
	cases := []struct {
		r     Revision
		stale bool
	}{
		{Revision{Scope: ScopeGroup, ScopeID: "tank", Epoch: 2}, true},  // former owner
		{Revision{Scope: ScopeGroup, ScopeID: "tank", Epoch: 3}, false}, // current owner
		{Revision{Scope: ScopeGroup, ScopeID: "tank", Epoch: 0}, false}, // written before the group existed
		{Revision{Scope: ScopeGroup, ScopeID: "fast", Epoch: 1}, false}, // pool in no group
		{Revision{Scope: ScopeCluster, ScopeID: "local", Epoch: 1}, false},
	}
	for i, c := range cases {
		if got := staleWriter(c.r) != ""; got != c.stale {
			t.Errorf("case %d: stale=%v, want %v", i, got, c.stale)
		}
	}
	if epochFor(ScopeGroup, "tank") != 3 || epochFor(ScopeNode, "tank") != 0 {
		t.Error("stamping uses the group epoch for group scope only")
	}
}
