package handlers

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsReplicationDue(t *testing.T) {
	now := time.Now()
	hourAgo := now.Add(-61 * time.Minute)
	halfHourAgo := now.Add(-30 * time.Minute)
	dayAgo := now.Add(-25 * time.Hour)
	halfDayAgo := now.Add(-20 * time.Hour)
	weekAgo := now.Add(-8 * 24 * time.Hour)
	monthAgo := now.Add(-30 * 24 * time.Hour)

	tests := []struct {
		name string
		s    ReplicationSchedule
		want bool
	}{
		{"never run - always due", ReplicationSchedule{Interval: "hourly"}, true},
		{"hourly - elapsed >= 1h", ReplicationSchedule{Interval: "hourly", LastRun: &hourAgo}, true},
		{"hourly - elapsed < 1h", ReplicationSchedule{Interval: "hourly", LastRun: &halfHourAgo}, false},
		{"daily - elapsed >= 24h", ReplicationSchedule{Interval: "daily", LastRun: &dayAgo}, true},
		{"daily - elapsed < 24h", ReplicationSchedule{Interval: "daily", LastRun: &halfDayAgo}, false},
		{"weekly - elapsed >= 7d", ReplicationSchedule{Interval: "weekly", LastRun: &weekAgo}, true},
		{"manual - never due regardless of age", ReplicationSchedule{Interval: "manual", LastRun: &monthAgo}, false},
		{"unknown interval - never due", ReplicationSchedule{Interval: "monthly", LastRun: &monthAgo}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReplicationDue(tt.s, now); got != tt.want {
				t.Errorf("isReplicationDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAtomicModifySchedules_ConcurrentClaim verifies the core invariant that
// prevents double-launch: N goroutines simultaneously racing to claim a
// non-running, due schedule result in exactly one claim. This covers both the
// concurrent-tick case (two ticker ticks overlap) and the tick-vs-manual-trigger
// case (runDueReplicationSchedules races TriggerPostSnapshotReplication).
//
// The test operates directly on atomicModifySchedules so it does not require
// a live SSH peer or ZFS environment.
func TestAtomicModifySchedules_ConcurrentClaim(t *testing.T) {
	tmpDir := t.TempDir()
	oldDir := ConfigDir
	ConfigDir = tmpDir
	t.Cleanup(func() { ConfigDir = oldDir })

	past := time.Now().Add(-25 * time.Hour)
	initial := []ReplicationSchedule{{
		ID:       "sched-race-test",
		Name:     "race-test",
		Enabled:  true,
		Interval: "daily",
		LastRun:  &past,
	}}
	data, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(replScheduleFile), data, 0644); err != nil {
		t.Fatal(err)
	}

	const goroutines = 30
	var claimCount int64
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
				for i := range all {
					s := &all[i]
					if s.ID == "sched-race-test" && s.LastStatus != "running" {
						s.LastStatus = "running"
						atomic.AddInt64(&claimCount, 1)
					}
				}
				return all, nil
			}); err != nil {
				t.Errorf("atomicModifySchedules: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()

	if claimCount != 1 {
		t.Errorf("expected exactly 1 goroutine to claim the schedule, got %d (double-launch race not prevented)", claimCount)
	}

	raw, err := os.ReadFile(configPath(replScheduleFile))
	if err != nil {
		t.Fatal(err)
	}
	var final []ReplicationSchedule
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatal(err)
	}
	if len(final) == 0 || final[0].LastStatus != "running" {
		t.Errorf("on-disk LastStatus = %q, want \"running\"", final[0].LastStatus)
	}
}

// TestAtomicModifySchedules_AlreadyRunningIsSkipped verifies that a schedule
// already marked "running" is not re-claimed by a concurrent caller, which is
// the guard condition that runDueReplicationSchedules and
// TriggerPostSnapshotReplication both rely on.
func TestAtomicModifySchedules_AlreadyRunningIsSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	oldDir := ConfigDir
	ConfigDir = tmpDir
	t.Cleanup(func() { ConfigDir = oldDir })

	past := time.Now().Add(-25 * time.Hour)
	initial := []ReplicationSchedule{{
		ID:         "sched-skip-test",
		Name:       "skip-test",
		Enabled:    true,
		Interval:   "daily",
		LastRun:    &past,
		LastStatus: "running",
		LastJobID:  "existing-job-id",
	}}
	data, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(replScheduleFile), data, 0644); err != nil {
		t.Fatal(err)
	}

	var claimCount int64
	if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range all {
			s := &all[i]
			if s.ID == "sched-skip-test" && s.LastStatus != "running" {
				s.LastStatus = "running"
				atomic.AddInt64(&claimCount, 1)
			}
		}
		return all, nil
	}); err != nil {
		t.Fatal(err)
	}

	if claimCount != 0 {
		t.Errorf("already-running schedule should not be claimed again, got %d claims", claimCount)
	}

	raw, _ := os.ReadFile(configPath(replScheduleFile))
	var final []ReplicationSchedule
	json.Unmarshal(raw, &final)
	if len(final) == 0 || final[0].LastJobID != "existing-job-id" {
		t.Errorf("existing job ID should be preserved, got %q", final[0].LastJobID)
	}
}
