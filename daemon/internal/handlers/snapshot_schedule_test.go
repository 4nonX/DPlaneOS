package handlers

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateSnapshotSchedules(t *testing.T) {
	ok := []SnapshotSchedule{
		{Dataset: "tank/data", Frequency: "frequent", Retention: 4},
		{Dataset: "tank/data", Frequency: "hourly", Retention: 24},
		{Dataset: "tank/data", Frequency: "daily", Retention: 7, RetentionDays: 30, Recursive: true},
	}
	if err := validateSnapshotSchedules(ok); err != nil {
		t.Errorf("valid schedules refused: %v", err)
	}
	bad := map[string][]SnapshotSchedule{
		// The UI used to send prefix "auto" for every frequency: the hourly
		// pruning then deleted the daily snapshots.
		"shared prefix":            {{Dataset: "tank/data", Frequency: "hourly", Retention: 24, Prefix: "auto"}, {Dataset: "tank/data", Frequency: "daily", Retention: 7, Prefix: "auto"}},
		"bad frequency":            {{Dataset: "tank/data", Frequency: "yearly", Retention: 1}},
		"retention days":           {{Dataset: "tank/data", Frequency: "daily", Retention: 1, RetentionDays: -1}},
		"trigger without schedule": {{Dataset: "tank/data", Frequency: "daily", Retention: 1, TriggerReplication: true}},
		"snapshot as dataset":      {{Dataset: "tank/data@x", Frequency: "daily", Retention: 1}},
	}
	for name, s := range bad {
		if err := validateSnapshotSchedules(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSnapshotScheduleCalendars(t *testing.T) {
	if snapshotCalendars["frequent"] != "*-*-* *:00/15:00" {
		t.Errorf("15-minute tier: %q", snapshotCalendars["frequent"])
	}
	if p := (SnapshotSchedule{Frequency: "frequent"}).effectivePrefix(); p != "auto-frequent" {
		t.Errorf("default prefix %q", p)
	}
}

// Count- and age-based pruning of one schedule's snapshots: other prefixes
// (auto-daily vs auto), the newest snapshot and replication bases survive.
func TestPruneOldSnapshots(t *testing.T) {
	prev := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = prev })
	_ = os.WriteFile(configPath(replScheduleFile), []byte(`[{"id":"rs","last_replicated_snapshot":"tank/data/child@auto-20261001-0200"}]`), 0644)

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	var lines []string
	for d := 1; d <= 17; d++ {
		ts := time.Date(2026, 10, d, 2, 0, 0, 0, time.Local)
		lines = append(lines, fmt.Sprintf("tank/data@auto-%s\t%d", ts.Format("20060102-1504"), ts.Unix()))
		lines = append(lines, fmt.Sprintf("tank/data@auto-daily-%s\t%d", ts.Format("20060102-1504"), ts.Unix()))
	}
	lines = append(lines, "tank/data@manual-before-upgrade\t1")
	listing := strings.Join(lines, "\n")

	destroyed := func(c *fakeCmds) []string {
		var d []string
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, call := range c.calls {
			if len(call.Args) > 0 && call.Args[0] == "destroy" {
				d = append(d, strings.Join(call.Args[1:], " "))
			}
		}
		return d
	}

	// 17 matching, keep 10: the 7 oldest go; "auto-daily-..." and manual stay.
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": out(listing)})
	pruneOldSnapshots("tank/data", "auto", 10, 0, false, now)
	got := destroyed(cmds)
	if len(got) != 7 || got[0] != "tank/data@auto-20261001-0200" || got[6] != "tank/data@auto-20261007-0200" {
		t.Errorf("count pruning destroyed %v", got)
	}
	for _, d := range got {
		if strings.Contains(d, "daily") || strings.Contains(d, "manual") {
			t.Errorf("destroyed a snapshot of another prefix: %s", d)
		}
	}

	// Count 100 but 5 days: everything older than Oct 12 12:00 goes.
	cmds = fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": out(listing)})
	pruneOldSnapshots("tank/data", "auto", 100, 5, false, now)
	if got := destroyed(cmds); len(got) != 12 {
		t.Errorf("age pruning destroyed %d (%v), want 12 (Oct 1-12)", len(got), got)
	}

	// Recursive: -r, and the snapshot whose child copy is a replication base stays.
	cmds = fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": out(listing)})
	pruneOldSnapshots("tank/data", "auto", 10, 0, true, now)
	got = destroyed(cmds)
	if len(got) != 6 || !strings.HasPrefix(got[0], "-r ") || strings.Contains(strings.Join(got, ","), "20261001") {
		t.Errorf("recursive pruning destroyed %v", got)
	}

	// Never the newest, even when it is past the age limit.
	cmds = fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": out(listing)})
	pruneOldSnapshots("tank/data", "auto", 1, 1, false, now.AddDate(0, 1, 0))
	for _, d := range destroyed(cmds) {
		if d == "tank/data@auto-20261017-0200" {
			t.Error("the newest snapshot was destroyed")
		}
	}
}
