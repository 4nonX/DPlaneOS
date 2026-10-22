package handlers

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// snapsEvery returns n snapshots of dataset, one every step, newest first,
// the newest created at newest.
func snapsEvery(dataset, prefix string, n int, step time.Duration, newest time.Time) []retSnap {
	var s []retSnap
	for i := 0; i < n; i++ {
		t := newest.Add(-time.Duration(i) * step)
		s = append(s, retSnap{name: fmt.Sprintf("%s@%s%s", dataset, prefix, t.Format("20060102-1504")), created: t})
	}
	return s
}

func keptNames(keep map[string]bool) []string {
	var names []string
	for n := range keep {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// keep_daily = 7 keeps the newest snapshot of each of the 7 most recent days,
// not (as before) up to 7 snapshots in every day forever.
func TestRetentionKeepsNewestPerBucket(t *testing.T) {
	newest := time.Date(2026, 10, 17, 2, 0, 0, 0, time.Local)
	cases := []struct {
		name   string
		snaps  []retSnap
		policy SnapshotRetentionPolicy
		want   int
	}{
		{"30 daily, keep 7 daily", snapsEvery("tank", "auto-daily-", 30, 24*time.Hour, newest), SnapshotRetentionPolicy{KeepDaily: 7}, 7},
		{"72 hourly, keep 24 hourly", snapsEvery("tank", "auto-hourly-", 72, time.Hour, newest), SnapshotRetentionPolicy{KeepHourly: 24}, 24},
		// 24 hourly cover today and yesterday; 7 daily add one per earlier day.
		{"72 hourly, keep 24 hourly + 7 daily", snapsEvery("tank", "auto-hourly-", 72, time.Hour, newest), SnapshotRetentionPolicy{KeepHourly: 24, KeepDaily: 7}, 26},
		{"4 per hour for 3 hours, keep 2 hourly", snapsEvery("tank", "auto-", 12, 15*time.Minute, newest), SnapshotRetentionPolicy{KeepHourly: 2}, 2},
		{"no counts keeps everything", snapsEvery("tank", "auto-", 10, time.Hour, newest), SnapshotRetentionPolicy{}, 10},
		{"fewer snapshots than buckets", snapsEvery("tank", "auto-", 3, 24*time.Hour, newest), SnapshotRetentionPolicy{KeepDaily: 7}, 3},
	}
	for _, c := range cases {
		keep := computeSnapKeepSet(c.snaps, c.policy)
		if len(keep) != c.want {
			t.Errorf("%s: kept %d, want %d: %v", c.name, len(keep), c.want, keptNames(keep))
		}
		// Whatever is kept, the newest snapshot always is.
		if len(c.snaps) > 0 && !keep[c.snaps[0].name] {
			t.Errorf("%s: newest snapshot %s not kept", c.name, c.snaps[0].name)
		}
	}
}

// The newest snapshot of a bucket is the one kept.
func TestRetentionKeepsNewestOfEachDay(t *testing.T) {
	newest := time.Date(2026, 10, 17, 23, 0, 0, 0, time.Local)
	snaps := snapsEvery("tank", "auto-", 48, time.Hour, newest) // 17th 23:00 back to 16th 00:00
	keep := computeSnapKeepSet(snaps, SnapshotRetentionPolicy{KeepDaily: 2})
	want := []string{"tank@auto-20261016-2300", "tank@auto-20261017-2300"}
	if got := keptNames(keep); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kept %v, want %v", got, want)
	}
}

// Weekly buckets use the ISO year: the last days of December can belong to
// week 1 of the next year and must share a bucket with it.
func TestRetentionISOWeekAcrossNewYear(t *testing.T) {
	jan1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)    // Thursday, ISO week 2026-W01
	dec29 := time.Date(2025, 12, 29, 12, 0, 0, 0, time.Local) // Monday, ISO week 2026-W01
	snaps := []retSnap{{"tank@auto-a", jan1}, {"tank@auto-b", dec29}}
	if keep := computeSnapKeepSet(snaps, SnapshotRetentionPolicy{KeepWeekly: 1}); len(keep) != 1 || !keep["tank@auto-a"] {
		t.Errorf("one ISO week, keep_weekly=1: kept %v", keptNames(keep))
	}
}

func retentionTestEnv(t *testing.T, schedules string) {
	t.Helper()
	prev := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = prev })
	if schedules != "" {
		if err := os.WriteFile(configPath(replScheduleFile), []byte(schedules), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// Parent and child are pruned on their own, only prefixed snapshots are
// touched, and a replication base survives although it is past retention.
func TestApplyRetentionPolicyPerDatasetAndProtected(t *testing.T) {
	retentionTestEnv(t, `[{"id":"rs-1","source_dataset":"tank/data","last_replicated_snapshot":"tank/data@auto-20261014-0200"}]`)
	day := func(d int) int64 { return time.Date(2026, 10, d, 2, 0, 0, 0, time.Local).Unix() }
	list := fmt.Sprintf(strings.Join([]string{
		"tank/data@auto-20261014-0200\t%d",
		"tank/data@manual-keep\t%d",
		"tank/data@auto-20261015-0200\t%d",
		"tank/data/child@auto-20261015-0200\t%d",
		"tank/data@auto-20261016-0200\t%d",
		"tank/data/child@auto-20261016-0200\t%d",
		"tank/data@auto-20261017-0200\t%d",
		"tank/data/child@auto-20261017-0200\t%d",
	}, "\n"), day(14), day(14), day(15), day(15), day(16), day(16), day(17), day(17))
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"ret_zfs_list_snapshots": out(list),
	})
	pruned, err := ApplyRetentionPolicy("tank/data", SnapshotRetentionPolicy{ID: "p", Prefix: "auto-", KeepDaily: 2})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(pruned)
	want := []string{"tank/data/child@auto-20261015-0200", "tank/data@auto-20261015-0200"}
	if strings.Join(pruned, ",") != strings.Join(want, ",") {
		t.Errorf("pruned %v, want %v (manual snapshot and replication base must survive)", pruned, want)
	}
	if c := cmds.find("ret_zfs_list_snapshots"); c == nil || !strings.Contains(strings.Join(c.Args, " "), "-p") {
		t.Errorf("snapshots must be listed with -p (parsable creation): %v", c)
	}
}

// Without a prefix a policy would prune manual snapshots: nothing is pruned.
func TestApplyRetentionPolicyRequiresPrefix(t *testing.T) {
	retentionTestEnv(t, "")
	cmds := fakeCommands(t, nil)
	if _, err := ApplyRetentionPolicy("tank/data", SnapshotRetentionPolicy{ID: "p", KeepDaily: 1}); err == nil {
		t.Error("a policy without prefix must not run")
	}
	if cmds.ran("ret_zfs_destroy_snapshot") {
		t.Error("nothing may be destroyed without a prefix")
	}
	r := call(t, HandleRetentionPolicies, req{method: "POST", body: map[string]any{"action": "create", "policy": map[string]any{"dataset": "tank/data", "keep_daily": 7}}})
	if r.code != 400 {
		t.Errorf("creating a policy without prefix must be refused: %s", r)
	}
}

// Create, then delete by ID only, as the web UI does (the delete used to be
// refused: the dataset was validated before the action was looked at).
func TestRetentionPolicyCreateDelete(t *testing.T) {
	retentionTestEnv(t, "")
	r := call(t, HandleRetentionPolicies, req{method: "POST", body: map[string]any{"action": "create",
		"policy": map[string]any{"dataset": "tank/data", "prefix": "auto-", "keep_daily": 7, "enabled": true}}})
	if !r.ok() {
		t.Fatalf("create: %s", r)
	}
	policies, _ := loadRetentionPolicies()
	if len(policies) != 1 {
		t.Fatalf("policies after create: %+v", policies)
	}
	r = call(t, HandleRetentionPolicies, req{method: "POST", body: map[string]any{"action": "delete",
		"policy": map[string]any{"action": "delete", "id": policies[0].ID}}})
	if !r.ok() {
		t.Fatalf("delete by id: %s", r)
	}
	if policies, _ = loadRetentionPolicies(); len(policies) != 0 {
		t.Errorf("policy not deleted: %+v", policies)
	}
}

// Unreadable replication schedules: the bases are unknown, nothing is pruned.
func TestApplyRetentionPolicyStopsWithoutSchedules(t *testing.T) {
	retentionTestEnv(t, "not json")
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"ret_zfs_list_snapshots": out(fmt.Sprintf("tank@auto-a\t%d\ntank@auto-b\t%d", time.Now().Add(-48*time.Hour).Unix(), time.Now().Unix())),
	})
	if _, err := ApplyRetentionPolicy("tank", SnapshotRetentionPolicy{ID: "p", Prefix: "auto-", KeepDaily: 1}); err == nil {
		t.Error("expected an error when the replication schedules cannot be read")
	}
	if cmds.ran("ret_zfs_destroy_snapshot") {
		t.Error("nothing may be destroyed while replication bases are unknown")
	}
}
