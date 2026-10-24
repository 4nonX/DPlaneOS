package groups

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSharedPoolNames(t *testing.T) {
	gs := []Group{
		{Name: "a", Topology: Shared, Pools: []PoolRef{{Name: "tank"}, {Name: "fast"}}},
		{Name: "b", Topology: Standalone, Pools: []PoolRef{{Name: "scratch"}}},
		{Name: "c", Topology: Replicated, Pools: []PoolRef{{Name: "copy"}}},
		{Name: "d", Topology: Shared, Pools: []PoolRef{{Name: "tank"}, {Name: ""}}},
	}
	if got := sharedPoolNames(gs); !reflect.DeepEqual(got, []string{"fast", "tank"}) {
		t.Errorf("got %v", got)
	}
}

func TestWriteSharedPools(t *testing.T) {
	p := filepath.Join(t.TempDir(), "shared-pools")
	if err := writeSharedPools(p, []string{"fast", "tank"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "fast\ntank\n" {
		t.Errorf("file: %q", b)
	}
	if err := writeSharedPools(p, nil); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || len(b) != 0 {
		t.Errorf("empty list: %q %v", b, err)
	}
}

// Every member records the pools of every shared group (also the groups it
// does not own), and rewrites the list when it changes.
func TestRecordSharedPools(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, _ := maintenanceManager(t, db, tr, []string{"ka", "kb"})
	var writes [][]string
	m.ops.SharedPools = func(n []string) error { writes = append(writes, n); return nil }

	m.recordSharedPools()
	if _, err := m.CreateWith("data", Shared, []string{"tank"}, []string{"kb"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWith("local", Standalone, []string{"scratch"}, nil, 0, false); err != nil {
		t.Fatal(err)
	}
	m.recordSharedPools()
	m.recordSharedPools() // unchanged: not written again
	want := [][]string{nil, {"tank"}}
	if !reflect.DeepEqual(writes, want) {
		t.Errorf("writes %v, want %v", writes, want)
	}
}
