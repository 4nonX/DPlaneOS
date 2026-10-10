package handlers

import (
	"strings"
	"sync"
	"testing"
	"time"

	"dplaned/internal/jobs"
	"dplaned/internal/storageops"
)

func withRegistryDB(t *testing.T) {
	db := testDB(t)
	prev := registryDB
	registryDB = db
	t.Cleanup(func() { registryDB = prev })
}

// Concurrent starts on one target: exactly one wins (the database enforces it).
func TestStorageOpsOnePendingPerTarget(t *testing.T) {
	withRegistryDB(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := storageops.Begin(registryDB, storageops.OpReplace, "tank:/dev/sda"); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			} else if !strings.Contains(err.Error(), "operation blocked") {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent operations started on one target", won)
	}
	if _, err := storageops.Begin(registryDB, storageops.OpReplace, "tank:/dev/sdb"); err != nil {
		t.Errorf("another target is independent: %v", err)
	}
}

func TestAddVdevToPool(t *testing.T) {
	withRegistryDB(t)
	cmds := fakeCommands(t, nil)
	for name, body := range map[string]map[string]any{
		"no disks":    {"pool": "tank", "vdev_type": "mirror"},
		"bad disk":    {"pool": "tank", "vdev_type": "mirror", "disks": []string{"/dev/sdc", "/etc/passwd"}},
		"bad type":    {"pool": "tank", "vdev_type": "raidz9", "disks": []string{"/dev/sdc"}},
		"bad pool":    {"pool": "-f", "vdev_type": "mirror", "disks": []string{"/dev/sdc"}},
		"option disk": {"pool": "tank", "disks": []string{"-f"}},
	} {
		if r := call(t, AddVdevToPool, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if len(cmds.keys()) != 0 {
		t.Fatalf("zpool ran for invalid requests: %v", cmds.keys())
	}
	r := call(t, AddVdevToPool, req{method: "POST", body: map[string]any{"pool": "tank", "vdev_type": "mirror", "disks": []string{"/dev/sdc", "/dev/sdd"}}})
	if !r.ok() {
		t.Fatalf("add: %s", r)
	}
	if !cmds.ran("zpool_add", "add", "tank", "mirror", "/dev/sdc", "/dev/sdd") {
		t.Errorf("zpool add: %v", cmds.keys())
	}
	var state string
	_ = registryDB.QueryRow(`SELECT state FROM storage_operations WHERE target = 'tank' ORDER BY id DESC LIMIT 1`).Scan(&state)
	if state != "committed" {
		t.Errorf("operation state %q", state)
	}

	fakeCommands(t, map[string]func([]string) ([]byte, error){"zpool_add": fail("mismatched replication level")})
	if r := call(t, AddVdevToPool, req{method: "POST", body: map[string]any{"pool": "tank", "disks": []string{"/dev/sde"}}}); r.code != 409 {
		t.Errorf("refused add: %s", r)
	}
	_ = registryDB.QueryRow(`SELECT state FROM storage_operations WHERE target = 'tank' ORDER BY id DESC LIMIT 1`).Scan(&state)
	if state != "failed" {
		t.Errorf("failed operation recorded as %q", state)
	}
}

func waitJob(t *testing.T, id string) jobs.JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j := jobs.Get(id); j != nil {
			if s := j.Snapshot(); s.Status != "running" && s.Status != "pending" {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return jobs.JobSnapshot{}
}

func TestReplaceDisk(t *testing.T) {
	withRegistryDB(t)
	cmds := fakeCommands(t, nil)
	if r := call(t, ReplaceDisk, req{method: "POST", body: map[string]any{"pool": "tank", "old_disk": "/dev/sda", "new_disk": "sde;reboot"}}); r.code != 400 {
		t.Errorf("bad new disk: %s", r)
	}
	r := call(t, ReplaceDisk, req{method: "POST", body: map[string]any{"pool": "tank", "old_disk": "/dev/sda", "new_disk": "/dev/sde"}})
	if !r.ok() {
		t.Fatalf("replace: %s", r)
	}
	if s := waitJob(t, r.body["job_id"].(string)); s.Status != "done" && s.Status != "completed" {
		t.Errorf("job: %+v", s)
	}
	if !cmds.ran("zpool_replace", "replace", "tank", "/dev/sda", "/dev/sde") || cmds.ran("zpool_replace", "replace", "-f") {
		t.Errorf("zpool replace: %v", cmds.keys())
	}

	fakeCommands(t, map[string]func([]string) ([]byte, error){"zpool_replace": fail("device is too small")})
	r = call(t, ReplaceDisk, req{method: "POST", body: map[string]any{"pool": "tank", "old_disk": "/dev/sdb", "new_disk": "/dev/sdf"}})
	if s := waitJob(t, r.body["job_id"].(string)); s.Status != "failed" || !strings.Contains(s.Error, "too small") {
		t.Errorf("failed replace: %+v", s)
	}
}
