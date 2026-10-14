package groups

import (
	"strings"
	"testing"
	"time"
)

func threeWay() Group {
	return Group{Name: "data", Topology: Shared, Pools: []PoolRef{{Name: "tank", GUID: "1"}},
		Candidates: []string{"a", "b", "c"}, Owner: "a", Epoch: 3, Version: 1}
}

func TestFailoverSkipsNodesInMaintenance(t *testing.T) {
	g := threeWay()
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	// b has priority over c, but b is in maintenance: c takes over.
	v := failoverView("c", "b", "c")
	v.Maintenance = map[string]bool{"b": true}
	if d := planFailover(g, v, t0, t0.Add(time.Hour)); d.Action != "takeover" {
		t.Fatalf("c must take over while b is in maintenance: %+v", d)
	}
	// On b itself: maintenance, so it never takes over.
	v = failoverView("b", "b", "c")
	v.Maintenance = map[string]bool{"b": true}
	if d := planFailover(g, v, t0, t0.Add(time.Hour)); d.Action != "none" || !strings.Contains(d.Reason, "maintenance") {
		t.Fatalf("b in maintenance: %+v", d)
	}
	// Without maintenance b still has priority.
	if d := planFailover(g, failoverView("c", "b", "c"), t0, t0.Add(time.Hour)); d.Action != "none" {
		t.Fatalf("b has priority: %+v", d)
	}
}

func TestPlanMaintenance(t *testing.T) {
	shared := threeWay()
	solo := Group{Name: "local", Topology: Standalone, Pools: []PoolRef{{Name: "scratch", GUID: "2"}},
		Candidates: []string{"a"}, Owner: "a", Epoch: 1, Version: 1}
	other := threeWay()
	other.Name, other.Owner = "theirs", "b"

	v := failoverView("a", "a", "b", "c")
	v.Maintenance = map[string]bool{"b": true}
	moves, ok := planMaintenance([]Group{shared, solo, other}, v)
	if !ok || len(moves) != 2 {
		t.Fatalf("plan: ok=%v %+v", ok, moves)
	}
	byGroup := map[string]MaintenanceMove{}
	for _, m := range moves {
		byGroup[m.Group] = m
	}
	// b is in maintenance itself: the group goes to c.
	if m := byGroup["data"]; m.Result != "planned" || m.Target != "c" {
		t.Errorf("data: %+v", m)
	}
	if m := byGroup["local"]; m.Result != "stays" {
		t.Errorf("standalone group must stay: %+v", m)
	}
	if _, ok := byGroup["theirs"]; ok {
		t.Error("a group owned by another node is not part of the plan")
	}

	// c is offline too: nowhere to go, and nothing must change.
	v = failoverView("a", "a", "b")
	v.Maintenance = map[string]bool{"b": true}
	moves, ok = planMaintenance([]Group{shared}, v)
	if ok || len(moves) != 1 || moves[0].Result != "failed" {
		t.Fatalf("no target: ok=%v %+v", ok, moves)
	}

	// Without a cluster (no quorum view) the priority order decides.
	moves, ok = planMaintenance([]Group{shared}, View{Self: "a"})
	if !ok || moves[0].Target != "b" {
		t.Fatalf("no quorum: ok=%v %+v", ok, moves)
	}
}
