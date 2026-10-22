package handlers

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func snapList(pairs ...string) []replSnap {
	var s []replSnap
	for i := 0; i+1 < len(pairs); i += 2 {
		s = append(s, replSnap{name: pairs[i], guid: pairs[i+1]})
	}
	return s
}

func TestPlanReplication(t *testing.T) {
	local := snapList("tank/data@a", "1", "tank/data@b", "2", "tank/data@c", "3")
	cases := []struct {
		name         string
		remote       []replSnap
		exists, from bool
		want         replPlan
		wantErr      bool
	}{
		{"remote missing: full send", nil, false, false, replPlan{}, false},
		{"remote has the newest: nothing to do", snapList("bk/data@c", "3"), true, false, replPlan{upToDate: true}, false},
		{"newest common snapshot is the base", snapList("bk/data@a", "1", "bk/data@b", "2"), true, false, replPlan{base: "tank/data@b"}, false},
		// The backup kept @old that the source pruned: still incremental from @b.
		{"backup keeps older history", snapList("bk/data@old", "9", "bk/data@b", "2"), true, false, replPlan{base: "tank/data@b"}, false},
		// Same names, other dataset (two sources sending to one remote name).
		{"same names, other GUIDs: refused", snapList("bk/data@a", "71", "bk/data@b", "72"), true, false, replPlan{}, true},
		{"same names, other GUIDs, from scratch allowed", snapList("bk/data@a", "71"), true, true, replPlan{destroyRemote: true}, false},
		{"remote exists without snapshots: refused", nil, true, false, replPlan{}, true},
	}
	for _, c := range cases {
		got, err := planReplication(local, c.remote, c.exists, c.from)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if err != nil && !errors.Is(err, errNoCommonSnapshot) {
			t.Errorf("%s: error %v is not errNoCommonSnapshot", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: plan %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A backup is received unmounted, without its mountpoint, read-only and
// without -F (which would mirror source pruning onto the backup).
func TestBackupRecvArgs(t *testing.T) {
	args := strings.Join(backupRecvArgs("bk/data"), " ")
	for _, want := range []string{"-s", "-u", "-x mountpoint", "-o readonly=on"} {
		if !strings.Contains(args, want) {
			t.Errorf("recv args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "-F") {
		t.Errorf("recv args %q must not contain -F", args)
	}
	if r := strings.Join(restoreRecvArgs("tank/data", false), " "); !strings.Contains(r, "-x readonly") || strings.Contains(r, "-F") {
		t.Errorf("restore args %q", r)
	}
}

func TestIsSendDiagnostic(t *testing.T) {
	progress := []string{"full\ttank/data@a\t1234", "incremental\ttank/data@a\ttank/data@b\t99", "size\t1234", "12:00:01\t5000\ttank/data@a"}
	for _, l := range progress {
		if isSendDiagnostic(l) {
			t.Errorf("%q is progress", l)
		}
	}
	if !isSendDiagnostic("cannot send tank/secret@s1: encrypted dataset tank/secret may not be sent with properties without the raw flag") {
		t.Error("an error line must be kept")
	}
}

// Run now claims the schedule before the job starts; a second run is refused
// while it runs, and a restart clears a stale "running".
func TestStartReplicationScheduleClaimsFirst(t *testing.T) {
	prev := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = prev })
	if err := os.WriteFile(configPath(replScheduleFile), []byte(`[{"id":"rs-1","name":"x","source_dataset":"tank/data","enabled":true}]`), 0644); err != nil {
		t.Fatal(err)
	}
	fakeCommands(t, nil)
	// No peer configured: the job fails at once and must stay "failed".
	if _, err := startReplicationSchedule("rs-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s, _ := loadReplicationSchedules()
		return len(s) == 1 && s[0].LastStatus == "failed"
	})
	if _, err := startReplicationSchedule("nope"); !errors.Is(err, errScheduleNotFound) {
		t.Errorf("unknown schedule: %v", err)
	}

	_ = atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		all[0].LastStatus = "running"
		return all, nil
	})
	if _, err := startReplicationSchedule("rs-1"); !errors.Is(err, errScheduleRunning) {
		t.Errorf("a running schedule must not start twice: %v", err)
	}
	ResetInterruptedReplicationSchedules()
	if s, _ := loadReplicationSchedules(); s[0].LastStatus != "interrupted" {
		t.Errorf("after restart: status %q, want interrupted", s[0].LastStatus)
	}
}
