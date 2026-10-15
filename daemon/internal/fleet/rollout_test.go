package fleet

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func view(wave int, doneAt *time.Time) RolloutView {
	return RolloutView{State: RolloutRunning, Waves: 2, CurrentWave: wave, SoakSecs: 300, AutoAdvance: true, WaveDoneAt: doneAt}
}

func TestPlanWaves(t *testing.T) {
	targets := []Target{
		{RemoteID: 1, Name: "canary", Wave: 0, State: TargetPending},
		{RemoteID: 2, Name: "b", Wave: 1, State: TargetPending},
		{RemoteID: 3, Name: "c", Wave: 1, State: TargetPending},
	}
	// Wave 1 (the canary) first; later waves wait.
	st := Plan(view(0, nil), targets, t0)
	if len(st.Apply) != 1 || st.Apply[0] != 1 || st.Advance || st.Finish {
		t.Fatalf("canary: %+v", st)
	}
	// Canary done: record the time, then soak.
	targets[0].State = TargetDone
	if st := Plan(view(0, nil), targets, t0); !st.MarkWaveDone || len(st.Apply) != 0 {
		t.Fatalf("wave done: %+v", st)
	}
	if st := Plan(view(0, &t0), targets, t0.Add(4*time.Minute)); st.Advance {
		t.Fatalf("advanced during the soak: %+v", st)
	}
	if st := Plan(view(0, &t0), targets, t0.Add(5*time.Minute)); !st.Advance {
		t.Fatalf("not advanced after the soak: %+v", st)
	}
	// Without auto-advance an operator continues.
	v := view(0, &t0)
	v.AutoAdvance = false
	if st := Plan(v, targets, t0.Add(time.Hour)); st.Advance {
		t.Fatalf("advanced without auto-advance: %+v", st)
	}
	// Wave 2: both; when done, the rollout finishes.
	if st := Plan(view(1, nil), targets, t0); len(st.Apply) != 2 {
		t.Fatalf("wave 2: %+v", st)
	}
	targets[1].State, targets[2].State = TargetDone, TargetDone
	if st := Plan(view(1, &t0), targets, t0); !st.Finish {
		t.Fatalf("finish: %+v", st)
	}
}

func TestPlanPausesOnFailure(t *testing.T) {
	targets := []Target{
		{RemoteID: 1, Name: "a", Wave: 0, State: TargetFailed, Attempts: 3},
		{RemoteID: 2, Name: "b", Wave: 0, State: TargetPending},
	}
	st := Plan(view(0, nil), targets, t0)
	if st.Pause == "" || !strings.Contains(st.Pause, "a") || len(st.Apply) != 0 {
		t.Fatalf("failure must pause the rollout and apply nothing more: %+v", st)
	}
	if st := Plan(RolloutView{State: RolloutPaused, Waves: 1}, targets, t0); len(st.Apply) != 0 || st.Pause != "" {
		t.Fatalf("a paused rollout does nothing: %+v", st)
	}
}

func TestPlanAppliesThroughOneClusterMember(t *testing.T) {
	targets := []Target{
		{RemoteID: 1, Name: "nas1", Wave: 0, State: TargetPending, Cluster: "c1"},
		{RemoteID: 2, Name: "nas2", Wave: 0, State: TargetPending, Cluster: "c1", Coordinator: true},
		{RemoteID: 3, Name: "solo", Wave: 0, State: TargetPending},
	}
	st := Plan(view(0, nil), targets, t0)
	// The coordinator is the representative; nas1 waits for it.
	if len(st.Apply) != 2 || st.Apply[0] != 2 || st.Apply[1] != 3 {
		t.Fatalf("apply: %+v", st)
	}
	targets[1].State = TargetDone
	st = Plan(view(0, nil), targets, t0)
	if st.Through[1] != "nas2" || len(st.Apply) != 1 || st.Apply[0] != 3 {
		t.Fatalf("through: %+v", st)
	}
	targets[1].State = TargetFailed
	if st := Plan(view(0, nil), targets, t0); st.Pause == "" {
		t.Fatalf("the cluster's failure must pause: %+v", st)
	}
}
