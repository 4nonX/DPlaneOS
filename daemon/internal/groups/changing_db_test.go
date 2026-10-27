package groups

import (
	"errors"
	"strings"
	"testing"

	"dplaned/internal/nvmet"
)

func status(t *testing.T, m *Manager, name string) GroupStatus {
	t.Helper()
	sts, err := m.Statuses()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sts {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s", name)
	return GroupStatus{}
}

// While a takeover imports the pools (with multihost that takes over half a
// minute), the status shows the change, not "this node is not the owner but
// has these shared pools imported" (the CI failover test read it there).
func TestTakeoverShowsTheChangeWhileImporting(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, imported := maintenanceManager(t, db, tr, []string{"ka"})
	delete(imported, "tank")
	if err := put(db, Group{Name: "data", Topology: Shared, Pools: []PoolRef{{Name: "tank", GUID: "11"}},
		Candidates: []string{"ka", "kb"}, Owner: "kb", Epoch: 1, Version: 1}); err != nil {
		t.Fatal(err)
	}
	// zpool list waits for the import and times out meanwhile.
	importing := false
	list := m.ops.ImportedPools
	m.ops.ImportedPools = func() (map[string]string, error) {
		if importing {
			return nil, errors.New("zpool list: timed out")
		}
		return list()
	}
	var during GroupStatus
	m.ops.ImportForce = func(string) error {
		importing = true
		imported["tank"] = "11"
		during = status(t, m, "data")
		importing = false
		return nil
	}
	if _, err := m.currentView(); err != nil { // as FailoverTick does first
		t.Fatal(err)
	}
	if err := m.takeover("data", "automatic failover"); err != nil {
		t.Fatal(err)
	}
	if !during.Changing || during.CanWrite || len(during.Release) != 0 || len(during.Problems) != 0 {
		t.Errorf("during the import: %+v", during.Status)
	}
	after := status(t, m, "data")
	if after.Owner != "ka" || after.Epoch != 2 || after.Changing || !after.CanWrite || len(after.Problems) != 0 {
		t.Errorf("after the takeover: owner %s epoch %d %+v", after.Owner, after.Epoch, after.Status)
	}
}

// A move to this node that cannot import every pool changes nothing here:
// the pools already imported go back (the sender takes them back), and the
// group's stacks and exports are not written to this node.
func TestRefusedMoveLeavesNothingHere(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, imported := maintenanceManager(t, db, tr, []string{"ka", "kb"})
	delete(imported, "tank")
	delete(imported, "scratch")
	g := Group{Name: "data", Topology: Shared, Pools: []PoolRef{{Name: "tank", GUID: "11"}, {Name: "scratch", GUID: "22"}},
		Candidates: []string{"ka", "kb"}, Owner: "kb", Epoch: 1, Version: 1}
	if err := put(db, g); err != nil {
		t.Fatal(err)
	}
	if err := storeResources(db, "data", Resources{Stacks: []StackDef{{Name: "web", YAML: "services: {}"}},
		NVMe: []nvmet.Export{{SubsystemNQN: "nqn.2014-08.org.example:web"}}}); err != nil {
		t.Fatal(err)
	}
	m.ops.Import = func(guid string) error {
		if guid == "22" {
			return errors.New("pool in use elsewhere")
		}
		imported["tank"] = "11"
		return nil
	}
	written := 0
	m.res.WriteStack = func(StackDef) error { written++; return nil }
	m.res.SaveExports = func([]nvmet.Export) error { written++; return nil }

	moved := g
	moved.Owner, moved.Epoch = "ka", 2
	if err := m.Receive(Update{Group: moved}); err == nil {
		t.Fatal("the move must be refused")
	}
	if len(imported) != 0 {
		t.Errorf("pools left imported here: %v", imported)
	}
	if written != 0 {
		t.Errorf("the group's stacks or exports were written here (%d)", written)
	}
	if cur, _ := Get(db, "data"); cur.Owner != "kb" || cur.Epoch != 1 {
		t.Errorf("group changed: owner %s epoch %d", cur.Owner, cur.Epoch)
	}
	if st := status(t, m, "data"); st.Changing {
		t.Errorf("still marked as changing: %+v", st.Status)
	}
}

// A pool the refused target cannot give back is named in the refusal, so the
// sender does not report it free while the target still holds it.
func TestRefusedMoveNamesAPoolItCannotGiveBack(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, imported := maintenanceManager(t, db, tr, []string{"ka", "kb"})
	delete(imported, "tank")
	delete(imported, "scratch")
	g := Group{Name: "data", Topology: Shared, Pools: []PoolRef{{Name: "tank", GUID: "11"}, {Name: "scratch", GUID: "22"}},
		Candidates: []string{"ka", "kb"}, Owner: "kb", Epoch: 1, Version: 1}
	if err := put(db, g); err != nil {
		t.Fatal(err)
	}
	m.ops.Import = func(guid string) error {
		if guid == "22" {
			return errors.New("pool in use elsewhere")
		}
		imported["tank"] = "11"
		return nil
	}
	m.ops.Export = func(string) error { return errors.New("pool is busy") }

	moved := g
	moved.Owner, moved.Epoch = "ka", 2
	err := m.Receive(Update{Group: moved})
	if err == nil || !strings.Contains(err.Error(), "importing scratch") || !strings.Contains(err.Error(), "pool tank stays imported on the target") {
		t.Fatalf("refusal: %v", err)
	}
	if st := status(t, m, "data"); len(st.Release) != 1 || st.Release[0] != "tank" {
		t.Errorf("the pool left here must show as one to release: %+v", st.Status)
	}
}

// A move to this node as its first operation: no status was listed before,
// and the listing times out during the import; the status still shows the
// change.
func TestMoveHereShowsTheChangeWhileImporting(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, imported := maintenanceManager(t, db, tr, []string{"ka", "kb"})
	delete(imported, "tank")
	g := Group{Name: "data", Topology: Shared, Pools: []PoolRef{{Name: "tank", GUID: "11"}},
		Candidates: []string{"ka", "kb"}, Owner: "kb", Epoch: 1, Version: 1}
	if err := put(db, g); err != nil {
		t.Fatal(err)
	}
	importing := false
	list := m.ops.ImportedPools
	m.ops.ImportedPools = func() (map[string]string, error) {
		if importing {
			return nil, errors.New("zpool list: timed out")
		}
		return list()
	}
	var during GroupStatus
	m.ops.Import = func(string) error {
		importing = true
		imported["tank"] = "11"
		during = status(t, m, "data")
		importing = false
		return nil
	}
	moved := g
	moved.Owner, moved.Epoch = "ka", 2
	if err := m.Receive(Update{Group: moved}); err != nil {
		t.Fatal(err)
	}
	if !during.Changing || during.CanWrite || len(during.Problems) != 0 {
		t.Errorf("during the import: %+v", during.Status)
	}
	if after := status(t, m, "data"); after.Owner != "ka" || after.Changing || !after.CanWrite {
		t.Errorf("after the move: owner %s %+v", after.Owner, after.Status)
	}
}
