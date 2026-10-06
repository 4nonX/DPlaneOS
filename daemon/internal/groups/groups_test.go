package groups

import (
	"testing"
)

func shared() Group {
	return Group{Name: "data", Topology: Shared, Pools: []PoolRef{{Name: "tank", GUID: "123"}},
		Candidates: []string{"a", "b"}, Owner: "a", Epoch: 1, Version: 1}
}

func TestValidate(t *testing.T) {
	if err := shared().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Group){
		func(g *Group) { g.Owner = "c" },
		func(g *Group) { g.Topology = "magic" },
		func(g *Group) { g.Pools = nil },
		func(g *Group) { g.Pools[0].GUID = "x;rm" },
		func(g *Group) { g.Candidates = []string{"a"} },
		func(g *Group) { g.Topology = Standalone },
		func(g *Group) { g.Epoch = 0 },
	}
	for i, f := range bad {
		g := shared()
		g.Pools = append([]PoolRef(nil), g.Pools...)
		f(&g)
		if g.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestNewer(t *testing.T) {
	a := shared()
	b := a
	b.Version = 5
	c := a
	c.Epoch = 2
	if !b.Newer(a) || !c.Newer(b) || a.Newer(a) || b.Newer(c) {
		t.Error("epoch must win over version, version over nothing")
	}
}

func TestEvaluate(t *testing.T) {
	g := shared()
	// Owner with pools, quorate: writes.
	st := Evaluate(g, View{Self: "a", Quorum: true, Quorate: true, ImportedHere: map[string]bool{"tank": true}})
	if st.Role != RoleOwner || !st.CanWrite || len(st.Problems) != 0 {
		t.Errorf("owner: %+v", st)
	}
	// Owner without quorum: must not write.
	st = Evaluate(g, View{Self: "a", Quorum: true, Quorate: false, ImportedHere: map[string]bool{"tank": true}})
	if st.CanWrite || len(st.Problems) == 0 {
		t.Errorf("owner without quorum: %+v", st)
	}
	// Owner whose pool is not imported.
	st = Evaluate(g, View{Self: "a", ImportedHere: map[string]bool{}})
	if st.CanWrite {
		t.Errorf("owner without pool: %+v", st)
	}
	// Standby with the shared pool imported (stale owner): release.
	st = Evaluate(g, View{Self: "b", ImportedHere: map[string]bool{"tank": true}})
	if st.Role != RoleStandby || st.CanWrite || len(st.Release) != 1 {
		t.Errorf("stale standby: %+v", st)
	}
	// Replicated standby holds its copy imported: expected.
	r := g
	r.Topology = Replicated
	st = Evaluate(r, View{Self: "b", ImportedHere: map[string]bool{"tank": true}})
	if len(st.Release) != 0 || len(st.Problems) != 0 {
		t.Errorf("replicated standby: %+v", st)
	}
}

func TestPlanAdopt(t *testing.T) {
	g := shared()
	g.Candidates = []string{"a", "b", "c"}

	// Planned move to b: b imports.
	g.Owner, g.Epoch = "b", 2
	imp, exp := planAdopt(g, "b", map[string]string{})
	if len(imp) != 1 || imp[0].GUID != "123" || len(exp) != 0 {
		t.Errorf("move target: imports %v exports %v", imp, exp)
	}
	// Already imported (e.g. adopted twice): nothing to do.
	imp, exp = planAdopt(g, "b", map[string]string{"tank": "123"})
	if len(imp) != 0 || len(exp) != 0 {
		t.Errorf("already imported: %v %v", imp, exp)
	}
	// Stale owner a learns (via pull) that c owns the group at epoch 3 while
	// it still has the shared pool imported: it releases it.
	g.Owner, g.Epoch = "c", 3
	imp, exp = planAdopt(g, "a", map[string]string{"tank": "123"})
	if len(imp) != 0 || len(exp) != 1 || exp[0] != "tank" {
		t.Errorf("stale owner: %v %v", imp, exp)
	}
	// Replicated standby keeps its copy.
	g.Topology = Replicated
	imp, exp = planAdopt(g, "a", map[string]string{"tank": "999"})
	if len(imp) != 0 || len(exp) != 0 {
		t.Errorf("replicated standby: %v %v", imp, exp)
	}
}
