package fleet

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ApplyParallel is how many remotes the manager applies to at once.
const ApplyParallel = 8

// Manager runs the fleet manager role: inventory polling and rollouts.
type Manager struct {
	db       *sql.DB
	nodeName func() string
	mu       sync.Mutex
	lastPoll time.Time
	trigger  chan struct{}
}

// NewManager creates the manager.
func NewManager(db *sql.DB, nodeName func() string) *Manager {
	return &Manager{db: db, nodeName: nodeName, trigger: make(chan struct{}, 1)}
}

// Trigger asks for a tick soon (a rollout was created or continued).
func (m *Manager) Trigger() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

// Start ticks every 10 seconds (rollouts) and polls the inventory every minute.
func (m *Manager) Start() {
	go func() {
		for {
			select {
			case <-time.After(10 * time.Second):
			case <-m.trigger:
			}
			m.Tick()
		}
	}()
}

// Tick polls the inventory when due and advances running rollouts.
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := GetSettings(m.db)
	if err != nil || !s.Enabled {
		return
	}
	if time.Since(m.lastPoll) >= time.Minute {
		m.poll(s)
		m.lastPoll = time.Now()
	}
	rollouts, err := Rollouts(m.db, 50)
	if err != nil {
		log.Printf("FLEET: %v", err)
		return
	}
	for _, r := range rollouts {
		if r.State == RolloutRunning {
			m.advance(s, r)
		}
	}
}

// PollNow refreshes the inventory immediately.
func (m *Manager) PollNow() {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := GetSettings(m.db)
	if err != nil || !s.Enabled {
		return
	}
	m.poll(s)
	m.lastPoll = time.Now()
}

func (m *Manager) poll(s Settings) {
	remotes, err := Remotes(m.db)
	if err != nil {
		log.Printf("FLEET: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, r := range remotes {
		wg.Add(1)
		go func(r Remote) {
			defer wg.Done()
			st, err := r.Client(s.Name, m.nodeName()).Status()
			recordStatus(m.db, r.ID, st, err)
		}(r)
	}
	wg.Wait()
}

// advance executes one planning step of a rollout.
func (m *Manager) advance(s Settings, r Rollout) {
	remotes, err := Remotes(m.db)
	if err != nil {
		return
	}
	byID := map[int64]Remote{}
	for _, rm := range remotes {
		byID[rm.ID] = rm
	}
	var ts []Target
	for _, t := range r.Targets {
		tg := Target{RemoteID: t.RemoteID, Name: t.Name, Wave: t.Wave, State: t.State, Attempts: t.Attempts}
		if rm, ok := byID[t.RemoteID]; ok && rm.Status != nil {
			tg.Cluster, tg.Coordinator = rm.Status.Cluster, rm.Status.Coordinator
		}
		ts = append(ts, tg)
	}
	view := RolloutView{State: r.State, Waves: len(r.Waves), CurrentWave: r.CurrentWave, SoakSecs: r.SoakSecs,
		AutoAdvance: r.AutoAdvance, WaveDoneAt: r.WaveDoneAt}
	step := Plan(view, ts, time.Now())
	for id, via := range step.Through {
		m.setTarget(r.ID, id, TargetDone, "applied through "+via+" (same cluster)", false)
	}
	tag := fmt.Sprintf("%d %s", r.ID, r.Name)
	// The remotes of a step are independent: apply in parallel, so one slow
	// remote does not hold up the others.
	var wg sync.WaitGroup
	slots := make(chan struct{}, ApplyParallel)
	for _, id := range step.Apply {
		rm, ok := byID[id]
		if !ok {
			m.setTarget(r.ID, id, TargetSkipped, "the remote was removed from the fleet", false)
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(id int64, rm Remote) {
			defer func() { <-slots; wg.Done() }()
			// An apply may legitimately take minutes; a remote cut off from
			// the manager is found by the inventory call's short timeout
			// instead of waiting out the apply's.
			c := rm.Client(s.Name, m.nodeName())
			if _, err := c.Status(); err != nil {
				m.setTarget(r.ID, id, TargetPending, "not reachable: "+err.Error(), true)
				return
			}
			results, err := c.Apply(s.Name, m.nodeName(), tag, r.Resources)
			switch {
			case err != nil:
				m.setTarget(r.ID, id, TargetPending, err.Error(), true)
			case failedResults(results) != "":
				m.setTarget(r.ID, id, TargetPending, failedResults(results), true)
			default:
				m.setTarget(r.ID, id, TargetDone, summarize(results), false)
			}
		}(id, rm)
	}
	wg.Wait()
	switch {
	case step.Pause != "":
		_ = SetRolloutState(m.db, r.ID, RolloutPaused, step.Pause)
		log.Printf("FLEET: rollout %s paused: %s", tag, step.Pause)
	case step.MarkWaveDone:
		_, _ = m.db.Exec(`UPDATE fleet_rollouts SET wave_done_at = NOW(), updated_at = NOW() WHERE id = $1`, r.ID)
	case step.Advance:
		_, _ = m.db.Exec(`UPDATE fleet_rollouts SET current_wave = current_wave + 1, wave_done_at = NULL, updated_at = NOW() WHERE id = $1`, r.ID)
	case step.Finish:
		_, _ = m.db.Exec(`UPDATE fleet_rollouts SET state = 'done', updated_at = NOW() WHERE id = $1`, r.ID)
		log.Printf("FLEET: rollout %s done", tag)
	}
}

// setTarget records a target's outcome. A failed attempt is counted; after
// MaxAttempts the target fails (and the rollout pauses on the next step).
func (m *Manager) setTarget(rollout, remote int64, state, detail string, attempt bool) {
	if attempt {
		_, _ = m.db.Exec(`UPDATE fleet_rollout_targets SET attempts = attempts + 1, detail = $3,
			state = CASE WHEN attempts + 1 >= $4 THEN 'failed' ELSE 'pending' END
			WHERE rollout_id = $1 AND remote_id = $2`, rollout, remote, detail, MaxAttempts)
		return
	}
	_, _ = m.db.Exec(`UPDATE fleet_rollout_targets SET state = $3, detail = $4,
		applied_at = CASE WHEN $3 = 'done' THEN NOW() ELSE applied_at END
		WHERE rollout_id = $1 AND remote_id = $2`, rollout, remote, state, detail)
}

func failedResults(rs []ApplyResult) string {
	var bad []string
	for _, r := range rs {
		if r.Result == "failed" {
			bad = append(bad, fmt.Sprintf("%s/%s: %s", r.Kind, r.Key, r.Detail))
		}
	}
	return strings.Join(bad, "; ")
}

func summarize(rs []ApplyResult) string {
	applied, unchanged, proposals := 0, 0, 0
	for _, r := range rs {
		switch r.Result {
		case "applied":
			applied++
		case "unchanged":
			unchanged++
		}
		if r.Proposal {
			proposals++
		}
	}
	s := fmt.Sprintf("%d applied, %d unchanged", applied, unchanged)
	if proposals > 0 {
		s += fmt.Sprintf("; %d local change(s) kept as proposals on the remote", proposals)
	}
	return s
}
