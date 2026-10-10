package handlers

import (
	"database/sql"
	"strings"
	"testing"
)

func TestSMARTSchedules(t *testing.T) {
	db := testDB(t)
	prevDB, prevRegen := ReconcilerDB, regenerateSMARTTimers
	regenerated := 0
	ReconcilerDB = db
	regenerateSMARTTimers = func(*sql.DB) error { regenerated++; return nil }
	t.Cleanup(func() { ReconcilerDB, regenerateSMARTTimers = prevDB, prevRegen })

	for name, body := range map[string]map[string]any{
		"cron syntax": {"device": "sda", "type": "short", "schedule": "0 0 * * *"},
		"bad device":  {"device": "../etc/passwd", "type": "short", "schedule": "daily"},
		"bad type":    {"device": "sda", "type": "nuke", "schedule": "daily"},
		"injection":   {"device": "sda", "type": "short", "schedule": "daily\nExecStart=/bin/sh"},
	} {
		if r := call(t, AddSMARTSchedule, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	// The disk list names disks without /dev/.
	if r := call(t, AddSMARTSchedule, req{method: "POST", body: map[string]any{"device": "sda", "type": "short", "schedule": "Sun *-*-* 02:00:00"}}); !r.ok() {
		t.Fatalf("add: %s", r)
	}
	if regenerated != 1 {
		t.Errorf("timers regenerated %d times, want 1", regenerated)
	}
	list := call(t, ListSMARTSchedules, req{})
	if !list.ok() || !strings.Contains(list.raw, `"/dev/sda"`) || !strings.Contains(list.raw, `"enabled":true`) {
		t.Fatalf("the schedule is not listed: %s", list)
	}
	if r := call(t, DeleteSMARTSchedule, req{method: "DELETE", path: "/api/hardware/smart/schedules?device=sda&type=short"}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	if r := call(t, DeleteSMARTSchedule, req{method: "DELETE", path: "/api/hardware/smart/schedules?device=sda&type=short"}); r.code != 404 {
		t.Errorf("second delete: %s", r)
	}
	if regenerated != 2 {
		t.Errorf("timers regenerated %d times, want 2", regenerated)
	}
}

func TestSMARTRunNow(t *testing.T) {
	cmds := fakeCommands(t, nil)
	if r := call(t, RunSMARTNow, req{method: "POST", body: map[string]any{"device": "sdb", "type": "long"}}); !r.ok() {
		t.Fatalf("run now: %s", r)
	}
	if !cmds.ran("smartctl_test", "-t", "long", "/dev/sdb") {
		t.Errorf("smartctl not run: %v", cmds.keys())
	}
}
