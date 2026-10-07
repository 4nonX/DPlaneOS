package groups

import (
	"strings"
	"testing"
	"time"
)

func failoverView(self string, online ...string) View {
	return View{Self: self, Quorum: true, Quorate: true, Online: online, AutoFailover: true,
		FenceOK: true, FenceDelay: 45 * time.Second}
}

func TestPlanFailover(t *testing.T) {
	g := shared() // owner a, candidates a, b
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	// Owner online: nothing.
	if d := planFailover(g, failoverView("b", "a", "b"), time.Time{}, t0); d.Action != "none" || d.Reason != "" {
		t.Errorf("owner online: %+v", d)
	}
	// Owner gone: wait the fencing delay, then take over.
	d := planFailover(g, failoverView("b", "b"), t0, t0.Add(10*time.Second))
	if d.Action != "wait" || !d.WaitUntil.Equal(t0.Add(45*time.Second)) {
		t.Errorf("waiting: %+v", d)
	}
	if d := planFailover(g, failoverView("b", "b"), t0, t0.Add(46*time.Second)); d.Action != "takeover" {
		t.Errorf("after delay: %+v", d)
	}
	// Two votes: no automatic failover, manual takeover suggested.
	v := failoverView("b", "b")
	v.AutoFailover, v.AutoFailoverReason = false, "no third vote"
	if d := planFailover(g, v, t0, t0.Add(time.Hour)); d.Action != "none" || !strings.Contains(d.Reason, "manually") {
		t.Errorf("two votes: %+v", d)
	}
	// No fencing method: none.
	v = failoverView("b", "b")
	v.FenceOK, v.FenceReason = false, "enable the watchdog"
	if d := planFailover(g, v, t0, t0.Add(time.Hour)); d.Action != "none" || !strings.Contains(d.Reason, "watchdog") {
		t.Errorf("no fencing: %+v", d)
	}
	// Three candidates: only the first online one in priority order takes over.
	g3 := g
	g3.Candidates = []string{"a", "b", "c"}
	if d := planFailover(g3, failoverView("c", "b", "c"), t0, t0.Add(time.Hour)); d.Action != "none" {
		t.Errorf("c must leave it to b: %+v", d)
	}
	if d := planFailover(g3, failoverView("b", "b", "c"), t0, t0.Add(time.Hour)); d.Action != "takeover" {
		t.Errorf("b takes over: %+v", d)
	}
	// Replicated groups: only with automatic failover switched on for the group.
	r := g
	r.Topology = Replicated
	if d := planFailover(r, failoverView("b", "b"), t0, t0.Add(time.Hour)); d.Action != "none" || !strings.Contains(d.Reason, "replicated") {
		t.Errorf("replicated, off: %+v", d)
	}
	r.AutoFailover = true
	if d := planFailover(r, failoverView("b", "b"), t0, t0.Add(time.Hour)); d.Action != "takeover" {
		t.Errorf("replicated, on: %+v", d)
	}
	// The owner itself and non-candidates never act.
	if d := planFailover(g, failoverView("a", "a"), t0, t0.Add(time.Hour)); d.Action != "none" {
		t.Errorf("owner: %+v", d)
	}
	if d := planFailover(g, failoverView("x", "x"), t0, t0.Add(time.Hour)); d.Action != "none" {
		t.Errorf("non-candidate: %+v", d)
	}
}
