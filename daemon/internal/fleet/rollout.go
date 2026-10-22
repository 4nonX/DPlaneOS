// Package fleet is the fleet overlay (Design 0001 section 5.7, phase 6;
// ADR-0005): an optional role any node can take. The fleet manager registers
// remotes (standalone nodes and cluster members) with an API token of each,
// shows their inventory and health, holds fleet-wide configuration and rolls
// it out wave by wave (a canary first). There is no consensus across the
// fleet: a remote never depends on the manager and works the same while it
// is down; the manager only talks to the remotes' APIs.
package fleet

import (
	"fmt"
	"sort"
	"time"
)

// Rollout states.
const (
	RolloutRunning   = "running"
	RolloutPaused    = "paused"
	RolloutDone      = "done"
	RolloutFailed    = "failed"
	RolloutCancelled = "cancelled"
)

// Target states.
const (
	TargetPending = "pending"
	TargetDone    = "done"
	TargetFailed  = "failed"
	TargetSkipped = "skipped"
)

// MaxAttempts is how often an unreachable or failing remote is tried before
// its wave fails and the rollout pauses.
const MaxAttempts = 3

// Target is one remote of a rollout, as the planner sees it.
type Target struct {
	RemoteID int64
	Name     string
	Wave     int
	State    string
	Attempts int
	// Cluster groups members of one cluster: the change is applied through
	// one of them (the coordinator if it is a target) and reaches the others
	// by configuration sync. "" = standalone.
	Cluster     string
	Coordinator bool
}

// RolloutView is the rollout state the planner needs.
type RolloutView struct {
	State       string
	Waves       int
	CurrentWave int
	SoakSecs    int
	AutoAdvance bool
	WaveDoneAt  *time.Time
}

// Step is what to do now.
type Step struct {
	Apply   []int64          // remotes to apply to now
	Through map[int64]string // remotes covered by a cluster member: remote → member name
	// Exactly one of the following (or none: wait).
	MarkWaveDone bool   // the current wave just completed: record the time (soak starts)
	Advance      bool   // go to the next wave
	Finish       bool   // last wave completed
	Pause        string // a remote failed: pause with this reason
}

// Plan decides the next step of a running rollout (no side effects).
func Plan(r RolloutView, targets []Target, now time.Time) Step {
	st := Step{Through: map[int64]string{}}
	if r.State != RolloutRunning {
		return st
	}
	var wave []Target
	for _, t := range targets {
		if t.Wave == r.CurrentWave {
			wave = append(wave, t)
		}
	}
	sort.Slice(wave, func(i, j int) bool { return wave[i].RemoteID < wave[j].RemoteID })

	// One representative per cluster in this wave: the coordinator if it is
	// part of the wave, otherwise the first member.
	rep := map[string]Target{}
	for _, t := range wave {
		if t.Cluster == "" {
			continue
		}
		cur, ok := rep[t.Cluster]
		if !ok || (t.Coordinator && !cur.Coordinator) {
			rep[t.Cluster] = t
		}
	}
	pending, failed := 0, []string{}
	for _, t := range wave {
		switch t.State {
		case TargetFailed:
			failed = append(failed, t.Name)
			continue
		case TargetDone, TargetSkipped:
			continue
		}
		if t.Cluster != "" && rep[t.Cluster].RemoteID != t.RemoteID {
			r := rep[t.Cluster]
			switch r.State {
			case TargetDone:
				st.Through[t.RemoteID] = r.Name
				continue
			case TargetFailed:
				failed = append(failed, t.Name)
				continue
			}
			pending++ // waits for its cluster's representative
			continue
		}
		pending++
		st.Apply = append(st.Apply, t.RemoteID)
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		st.Apply, st.Through = nil, map[int64]string{}
		st.Pause = fmt.Sprintf("wave %d failed on %v: the rollout is paused; fix the cause and continue, or cancel (nothing is reverted)", r.CurrentWave+1, failed)
		return st
	}
	if pending > 0 {
		return st
	}
	// The wave is complete.
	if r.WaveDoneAt == nil {
		st.MarkWaveDone = true
		return st
	}
	if r.CurrentWave+1 >= r.Waves {
		st.Finish = true
		return st
	}
	// No soak: advance at once (a wall-clock comparison could miss by a step
	// of the clock).
	if r.AutoAdvance && (r.SoakSecs <= 0 || !now.Before(r.WaveDoneAt.Add(time.Duration(r.SoakSecs)*time.Second))) {
		st.Advance = true
	}
	return st
}
