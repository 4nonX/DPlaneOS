package configstore

import (
	"testing"

	"dplaned/internal/gitops"
)

func latestOf(rs []Resource) map[string]Revision {
	out := map[string]Revision{}
	for i, r := range rs {
		out[r.ID()] = Revision{ID: int64(i + 1), Kind: r.Kind, Key: r.Key, Scope: r.Scope, ScopeID: r.ScopeID, Payload: normalize(r.Payload)}
	}
	return out
}

func TestChangesAgainst(t *testing.T) {
	before := Extract(&gitops.DesiredState{
		Pools:    []gitops.DesiredPool{{Name: "tank", Topology: gitops.SimpleMirrorTopology("/dev/disk/by-id/a", "/dev/disk/by-id/b")}},
		Datasets: []gitops.DesiredDataset{{Name: "tank/media", Quota: "1T"}, {Name: "tank/old"}},
		Users:    []gitops.DesiredUser{{Username: "alice", Role: "admin"}},
	}, "n")
	latest := latestOf(before)

	// Unchanged: nothing to record (pools are never part of a capture).
	same := Extract(&gitops.DesiredState{
		Datasets: []gitops.DesiredDataset{{Name: "tank/media", Quota: "1T"}, {Name: "tank/old"}},
		Users:    []gitops.DesiredUser{{Username: "alice", Role: "admin"}},
	}, "n")
	if c := changesAgainst(same, latest); len(c) != 0 {
		t.Fatalf("no change expected, got %+v", c)
	}

	after := Extract(&gitops.DesiredState{
		Datasets: []gitops.DesiredDataset{{Name: "tank/media", Quota: "2T"}, {Name: "tank/new"}},
		Users:    []gitops.DesiredUser{{Username: "alice", Role: "admin"}},
	}, "n")
	got := map[string]bool{}
	for _, c := range changesAgainst(after, latest) {
		got[c.ID()] = c.Payload == nil
	}
	want := map[string]bool{"dataset/tank/media": false, "dataset/tank/new": false, "dataset/tank/old": true}
	if len(got) != len(want) {
		t.Fatalf("changes %v, want %v", got, want)
	}
	for id, deleted := range want {
		if d, ok := got[id]; !ok || d != deleted {
			t.Errorf("%s: recorded=%v deleted=%v, want deleted=%v", id, ok, d, deleted)
		}
	}
}

func TestDiffPayloads(t *testing.T) {
	a := toMap(gitops.DesiredDataset{Name: "tank/media", Quota: "1T", Compression: "lz4"})
	b := toMap(gitops.DesiredDataset{Name: "tank/media", Quota: "2T", Compression: "lz4", Atime: "off"})
	changes := DiffPayloads(a, b)
	got := map[string][2]any{}
	for _, c := range changes {
		got[c.Path] = [2]any{c.Old, c.New}
	}
	if len(got) != 2 || got["quota"] != [2]any{"1T", "2T"} || got["atime"] != [2]any{"", "off"} {
		t.Errorf("unexpected changes: %+v", changes)
	}
	if c := DiffPayloads(a, a); len(c) != 0 {
		t.Errorf("identical payloads differ: %+v", c)
	}
	created := DiffPayloads(nil, a)
	if len(created) == 0 {
		t.Error("a created resource should list its fields")
	}
	nested := DiffPayloads(
		toMap(gitops.DesiredGroup{Name: "family", Members: []string{"alice", "bob"}}),
		toMap(gitops.DesiredGroup{Name: "family", Members: []string{"alice", "carol"}}))
	if len(nested) != 1 || nested[0].Path != "members[1]" {
		t.Errorf("list change: %+v", nested)
	}
}
