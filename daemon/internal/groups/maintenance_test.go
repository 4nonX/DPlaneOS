package groups

import (
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Where the groups of a node go when it enters maintenance.
func TestPlanMaintenance(t *testing.T) {
	sharedAB := shared() // owner a, candidates a, b
	three := shared()
	three.Name, three.Candidates = "three", []string{"a", "b", "c"}
	alone := Group{Name: "local", Topology: Standalone, Pools: []PoolRef{{Name: "p", GUID: "1"}}, Candidates: []string{"a"}, Owner: "a"}
	theirs := shared()
	theirs.Name, theirs.Owner = "theirs", "b"
	gs := []Group{sharedAB, three, alone, theirs}

	none := func(string) bool { return false }
	view := View{Self: "a", Quorum: true, Quorate: true, Online: []string{"a", "b", "c"}}

	plan := planMaintenance(gs, view, none)
	got := map[string]MaintStep{}
	for _, s := range plan {
		got[s.Group] = s
	}
	if len(plan) != 3 {
		t.Fatalf("a group of another node is in the plan: %+v", plan)
	}
	if s := got["data"]; s.Action != "move" || s.Target != "b" {
		t.Errorf("data: %+v", s)
	}
	if s := got["local"]; s.Action != "stays" {
		t.Errorf("a standalone group has nowhere to go and says so: %+v", s)
	}

	// b is in maintenance itself: the group with a third candidate goes
	// there, the pair's group has nowhere to go.
	bDown := func(n string) bool { return n == "b" }
	got = map[string]MaintStep{}
	for _, s := range planMaintenance(gs, view, bDown) {
		got[s.Group] = s
	}
	if s := got["three"]; s.Action != "move" || s.Target != "c" {
		t.Errorf("three, b in maintenance: %+v", s)
	}
	if s := got["data"]; s.Action != "blocked" || !strings.Contains(s.Detail, "maintenance") {
		t.Errorf("data, b in maintenance: %+v", s)
	}
	// b unreachable: blocked, and the reason is that one.
	view.Online = []string{"a", "c"}
	for _, s := range planMaintenance(gs, view, none) {
		if s.Group == "data" && (s.Action != "blocked" || !strings.Contains(s.Detail, "not reachable")) {
			t.Errorf("data, b unreachable: %+v", s)
		}
	}
}

// A node in maintenance takes nothing over, and the others pass it over.
func TestFailoverRespectsMaintenance(t *testing.T) {
	g := shared()
	g.Candidates = []string{"a", "b", "c"}
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)

	// b is next in line and in maintenance: it does not take over ...
	v := failoverView("b", "b", "c")
	v.Maintenance = map[string]bool{"b": true}
	if d := planFailover(g, v, t0, later); d.Action != "none" || !strings.Contains(d.Reason, "maintenance") {
		t.Errorf("b in maintenance: %+v", d)
	}
	// ... and c, which would otherwise wait for b, does.
	v = failoverView("c", "b", "c")
	v.Maintenance = map[string]bool{"b": true}
	if d := planFailover(g, v, t0, later); d.Action != "takeover" {
		t.Errorf("c with b in maintenance: %+v", d)
	}
	// Without maintenance c leaves it to b.
	if d := planFailover(g, failoverView("c", "b", "c"), t0, later); d.Action != "none" {
		t.Errorf("c with b available: %+v", d)
	}
}

func TestEnterAndLeaveMaintenance(t *testing.T) {
	db := groupsTestDB(t)
	a, rec, _, tr := testManager(t, db, "a", map[string]string{"pool-77": "77", "pool-5": "5"})
	// The view is read by the manager's background work too: the test
	// changes who is online through this, not by replacing the function.
	var online atomic.Value
	online.Store([]string{"a", "b"})
	a.view = func() View { return View{Quorum: true, Quorate: true, Online: online.Load().([]string)} }
	if _, err := a.CreateWith("g", Shared, []string{"pool-77"}, []string{"b"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateWith("local", Standalone, []string{"pool-5"}, nil, 0, false); err != nil {
		t.Fatal(err)
	}
	rec.take()

	if st, _ := GetMaintenance(db); st.Active {
		t.Fatal("in maintenance from the start")
	}
	steps, err := a.EnterMaintenance("firmware update", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := GetMaintenance(db)
	if !st.Active || st.Reason != "firmware update" || st.SetBy != "admin" || st.Since == nil {
		t.Errorf("state: %+v", st)
	}
	moved := map[string]MaintStep{}
	for _, s := range steps {
		moved[s.Group] = s
	}
	if s := moved["g"]; !s.Done || s.Target != "b" {
		t.Errorf("g: %+v", s)
	}
	if s := moved["local"]; s.Action != "stays" || s.Done {
		t.Errorf("local: %+v", s)
	}
	if g, _ := Get(db, "g"); g.Owner != "b" {
		t.Errorf("g is still owned by %s", g.Owner)
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"export pool-77", "release 77"}) {
		t.Errorf("storage operations of the move: %v", got)
	}

	// In maintenance: no takeover on request, no group handed to it.
	if _, err := a.Takeover("g"); !errors.Is(err, ErrInMaintenance) && !errors.Is(err, ErrOwnerOnline) {
		t.Errorf("takeover in maintenance: %v", err)
	}
	online.Store([]string{"a"})
	if _, err := a.Takeover("g"); !errors.Is(err, ErrInMaintenance) {
		t.Errorf("takeover in maintenance with the owner away: %v", err)
	}
	back, _ := Get(db, "g")
	back.Owner, back.Epoch = "a", back.Epoch+1
	if err := a.Receive(Update{Group: *back}); !errors.Is(err, ErrInMaintenance) {
		t.Errorf("a group was handed to a node in maintenance: %v", err)
	}

	if err := a.LeaveMaintenance(); err != nil {
		t.Fatal(err)
	}
	if st, _ := GetMaintenance(db); st.Active || st.Reason != "" {
		t.Errorf("after leaving: %+v", st)
	}
	// Nothing came back by itself.
	if g, _ := Get(db, "g"); g.Owner != "b" {
		t.Errorf("leaving maintenance moved the group: owner %s", g.Owner)
	}

	// A group with nowhere to go: refused, and nothing is changed; with
	// force the node enters maintenance and the group stays.
	if err := a.Receive(Update{Group: *back}); err != nil {
		t.Fatal(err)
	}
	online.Store([]string{"a"})
	if _, err := a.EnterMaintenance("x", "admin", false); !errors.Is(err, ErrMaintenanceBlocked) {
		t.Fatalf("want ErrMaintenanceBlocked, got %v", err)
	}
	if st, _ := GetMaintenance(db); st.Active {
		t.Error("a refused request left the node in maintenance")
	}
	steps, err = a.EnterMaintenance("x", "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := GetMaintenance(db); !st.Active {
		t.Error("force did not enter maintenance")
	}
	if g, _ := Get(db, "g"); g.Owner != "a" {
		t.Errorf("a blocked group left anyway: %+v %+v", g, steps)
	}

	// A move that the target refuses: the group stays, the step says why.
	_ = a.LeaveMaintenance()
	online.Store([]string{"a", "b"})
	tr.pushErr = errors.New("b says no")
	steps, err = a.EnterMaintenance("y", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Group == "g" && (s.Done || !strings.Contains(s.Error, "b says no")) {
			t.Errorf("failed move: %+v", s)
		}
	}
	if g, _ := Get(db, "g"); g.Owner != "a" {
		t.Errorf("after a refused move the group is owned by %s", g.Owner)
	}
}
