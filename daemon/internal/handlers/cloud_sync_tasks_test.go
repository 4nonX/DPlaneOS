package handlers

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// cloudTasksEnv: a pool folder, a configured remote "b2", and a recorder in
// place of rclone's transfers.
type cloudTasksEnv struct {
	root string
	mu   sync.Mutex
	runs [][]string
	fail string // rclone's last words when it fails
	done chan struct{}

	alerts []string
}

func newCloudTasksEnv(t *testing.T) *cloudTasksEnv {
	t.Helper()
	e := &cloudTasksEnv{root: shareRoot(t, "data", "restore"), done: make(chan struct{}, 16)}
	prevDir, prevRun, prevHook, prevConf := ConfigDir, runRcloneTransfer, webhookAlertFn, rcloneConfig
	ConfigDir = t.TempDir()
	rcloneConfig = filepath.Join(ConfigDir, "rclone.conf")
	runRcloneTransfer = func(args ...string) ([]byte, error) {
		e.mu.Lock()
		e.runs = append(e.runs, args)
		fail := e.fail
		e.mu.Unlock()
		defer func() { e.done <- struct{}{} }()
		if fail != "" {
			return []byte("Transferred: 0 B\n" + fail + "\n"), errors.New("exit status 1")
		}
		return []byte("Transferred: 12 MiB\n"), nil
	}
	webhookAlertFn = func(level, event, resource, message string) {
		e.mu.Lock()
		e.alerts = append(e.alerts, event+" "+resource+": "+message)
		e.mu.Unlock()
	}
	t.Cleanup(func() {
		ConfigDir, runRcloneTransfer, webhookAlertFn, rcloneConfig = prevDir, prevRun, prevHook, prevConf
	})
	fakeCommands(t, map[string]func([]string) ([]byte, error){"rclone": out("b2:\nnas2:\n")})
	return e
}

// wait blocks until a started transfer has finished and its result is saved.
func (e *cloudTasksEnv) wait(t *testing.T) {
	t.Helper()
	select {
	case <-e.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the transfer did not run")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cloudTasksMu.Lock()
		busy := len(cloudTasksRunning) > 0
		cloudTasksMu.Unlock()
		if !busy {
			time.Sleep(50 * time.Millisecond) // the saved result follows the flag
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the task did not finish")
}

func (e *cloudTasksEnv) lastRun() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.runs) == 0 {
		return nil
	}
	return e.runs[len(e.runs)-1]
}

func taskID(t *testing.T, name string) string {
	t.Helper()
	tasks, err := loadCloudTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Name == name {
			return task.ID
		}
	}
	t.Fatalf("no task %q", name)
	return ""
}

func TestCloudSyncTaskNeverWorksOnAWholeRemote(t *testing.T) {
	e := newCloudTasksEnv(t)
	h := NewCloudSyncHandler()
	data := filepath.Join(e.root, "data")
	for name, body := range map[string]map[string]any{
		"no remote path":   {"name": "t", "remote": "b2", "local_path": data},
		"only slashes":     {"name": "t", "remote": "b2", "remote_path": "//", "local_path": data},
		"parent":           {"name": "t", "remote": "b2", "remote_path": "bucket/../other", "local_path": data},
		"an option":        {"name": "t", "remote": "b2", "remote_path": "--delete-excluded", "local_path": data},
		"another remote":   {"name": "t", "remote": "b2", "remote_path": "x:bucket", "local_path": data},
		"unknown remote":   {"name": "t", "remote": "nowhere", "remote_path": "bucket", "local_path": data},
		"outside the pool": {"name": "t", "remote": "b2", "remote_path": "bucket", "local_path": "/etc"},
		"no local path":    {"name": "t", "remote": "b2", "remote_path": "bucket"},
		"direction":        {"name": "t", "remote": "b2", "remote_path": "bucket", "local_path": data, "direction": "pull"},
		"mode":             {"name": "t", "remote": "b2", "remote_path": "bucket", "local_path": data, "mode": "sync"},
		"interval":         {"name": "t", "remote": "b2", "remote_path": "bucket", "local_path": data, "interval": "* * * * *"},
		"hour":             {"name": "t", "remote": "b2", "remote_path": "bucket", "local_path": data, "interval": "daily", "hour": 24},
		"name":             {"name": "../x", "remote": "b2", "remote_path": "bucket", "local_path": data},
	} {
		if r := call(t, h.CreateCloudSyncTask, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if tasks, _ := loadCloudTasks(); len(tasks) != 0 {
		t.Fatalf("an invalid task was saved: %+v", tasks)
	}
}

func TestCloudSyncTaskCopiesUnlessToldToMirror(t *testing.T) {
	e := newCloudTasksEnv(t)
	h := NewCloudSyncHandler()
	data := filepath.Join(e.root, "data")
	if r := call(t, h.CreateCloudSyncTask, req{method: "POST", body: map[string]any{"name": "offsite", "remote": "b2", "remote_path": "/my-bucket/nas/", "local_path": data}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	id := taskID(t, "offsite")
	if r := call(t, h.CreateCloudSyncTask, req{method: "POST", body: map[string]any{"name": "Offsite", "remote": "b2", "remote_path": "other", "local_path": data}}); r.code != 409 {
		t.Errorf("same name twice: %s", r)
	}

	// A dry run changes nothing, and is not the task's "last run".
	if r := call(t, RunCloudSyncTask, req{method: "POST", vars: map[string]string{"id": id}, body: map[string]any{"dry_run": true}}); !r.ok() {
		t.Fatalf("dry run: %s", r)
	}
	e.wait(t)
	if got := strings.Join(e.lastRun(), " "); got != "copy "+data+" b2:my-bucket/nas --stats-one-line --stats 30s --dry-run" {
		t.Fatalf("dry run ran: %s", got)
	}
	if tasks, _ := loadCloudTasks(); tasks[0].LastRun != nil {
		t.Fatal("a dry run counted as a run")
	}

	// The default copies: nothing is deleted at the destination.
	if r := call(t, RunCloudSyncTask, req{method: "POST", vars: map[string]string{"id": id}}); !r.ok() {
		t.Fatalf("run: %s", r)
	}
	e.wait(t)
	if got := e.lastRun(); got[0] != "copy" || got[1] != data || got[2] != "b2:my-bucket/nas" {
		t.Fatalf("ran: %v", got)
	}
	tasks, _ := loadCloudTasks()
	if tasks[0].LastStatus != "done" || tasks[0].LastRun == nil || tasks[0].LastError != "" {
		t.Fatalf("after the run: %+v", tasks[0])
	}

	// Mirror, and the other direction: the remote folder is the source.
	restore := filepath.Join(e.root, "restore")
	if r := call(t, h.UpdateCloudSyncTask, req{method: "PUT", vars: map[string]string{"id": id}, body: map[string]any{"name": "offsite", "remote": "b2", "remote_path": "my-bucket/nas", "local_path": restore, "direction": "download", "mode": "mirror"}}); !r.ok() {
		t.Fatalf("update: %s", r)
	}
	if r := call(t, RunCloudSyncTask, req{method: "POST", vars: map[string]string{"id": id}}); !r.ok() {
		t.Fatalf("run: %s", r)
	}
	e.wait(t)
	if got := e.lastRun(); got[0] != "sync" || got[1] != "b2:my-bucket/nas" || got[2] != restore {
		t.Fatalf("mirror download ran: %v", got)
	}
	// The update kept what is known of the earlier runs.
	if tasks, _ := loadCloudTasks(); tasks[0].LastRun == nil {
		t.Fatal("the update forgot the last run")
	}

	// A remote that tasks use is not removed from under them.
	r := call(t, h.HandleCloudSync, req{method: "POST", body: map[string]any{"action": "delete", "remote": "b2"}})
	if r.code != 409 || !strings.Contains(r.raw, "offsite") {
		t.Fatalf("removing a remote in use: %s", r)
	}
	// The old way in, a sync to the root of a remote, is gone.
	if r := call(t, h.HandleCloudSync, req{method: "POST", body: map[string]any{"action": "sync", "remote": "b2", "local_path": data}}); r.code != 400 {
		t.Fatalf("one-shot sync to a whole remote: %s", r)
	}

	if r := call(t, DeleteCloudSyncTask, req{method: "DELETE", vars: map[string]string{"id": id}}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	if r := call(t, RunCloudSyncTask, req{method: "POST", vars: map[string]string{"id": id}}); r.code != 404 {
		t.Errorf("run of a deleted task: %s", r)
	}
}

func TestCloudSyncTaskFailureIsKeptAndAlerted(t *testing.T) {
	e := newCloudTasksEnv(t)
	h := NewCloudSyncHandler()
	if r := call(t, h.CreateCloudSyncTask, req{method: "POST", body: map[string]any{"name": "offsite", "remote": "b2", "remote_path": "bucket", "local_path": filepath.Join(e.root, "data")}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	id := taskID(t, "offsite")
	e.fail = "Failed to copy: AccessDenied: the key may not write to this bucket"
	if r := call(t, RunCloudSyncTask, req{method: "POST", vars: map[string]string{"id": id}}); !r.ok() {
		t.Fatalf("run: %s", r)
	}
	e.wait(t)
	tasks, _ := loadCloudTasks()
	if tasks[0].LastStatus != "failed" || !strings.Contains(tasks[0].LastError, "AccessDenied") {
		t.Fatalf("the failure was not kept with its reason: %+v", tasks[0])
	}
	e.mu.Lock()
	alerts := strings.Join(e.alerts, "\n")
	e.mu.Unlock()
	if !strings.Contains(alerts, EventBackupFailed) || !strings.Contains(alerts, "AccessDenied") {
		t.Fatalf("no alert for the failed task: %q", alerts)
	}
	r := call(t, ListCloudSyncTasks, req{})
	if !r.ok() || !strings.Contains(r.raw, `"last_status":"failed"`) {
		t.Errorf("list: %s", r)
	}
}

func TestCloudTaskDue(t *testing.T) {
	loc := time.Local
	at := func(day, hour, min int) time.Time { return time.Date(2026, 10, day, hour, min, 0, 0, loc) } // 1 Oct 2026 is a Thursday
	p := func(tm time.Time) *time.Time { return &tm }
	base := CloudSyncTask{Enabled: true, CreatedAt: at(1, 9, 30)}
	with := func(f func(*CloudSyncTask)) CloudSyncTask { c := base; f(&c); return c }

	for name, c := range map[string]struct {
		task CloudSyncTask
		now  time.Time
		want bool
	}{
		"manual never":                  {with(func(c *CloudSyncTask) { c.Interval = "manual" }), at(9, 0, 0), false},
		"disabled never":                {with(func(c *CloudSyncTask) { c.Interval = "hourly"; c.Enabled = false }), at(9, 0, 0), false},
		"hourly, not in its first hour": {with(func(c *CloudSyncTask) { c.Interval = "hourly" }), at(1, 9, 45), false},
		"hourly, next hour":             {with(func(c *CloudSyncTask) { c.Interval = "hourly" }), at(1, 10, 0), true},
		"hourly, ran this hour":         {with(func(c *CloudSyncTask) { c.Interval = "hourly"; c.LastRun = p(at(1, 10, 0)) }), at(1, 10, 30), false},
		"daily, before the hour":        {with(func(c *CloudSyncTask) { c.Interval = "daily"; c.Hour = 23 }), at(1, 22, 59), false},
		"daily, at the hour":            {with(func(c *CloudSyncTask) { c.Interval = "daily"; c.Hour = 23 }), at(1, 23, 0), true},
		"daily, created after the hour": {with(func(c *CloudSyncTask) { c.Interval = "daily"; c.Hour = 3 }), at(1, 12, 0), false},
		"daily, next morning":           {with(func(c *CloudSyncTask) { c.Interval = "daily"; c.Hour = 3 }), at(2, 3, 0), true},
		"daily, ran today":              {with(func(c *CloudSyncTask) { c.Interval = "daily"; c.Hour = 3; c.LastRun = p(at(2, 3, 0)) }), at(2, 15, 0), false},
		"daily, missed while off":       {with(func(c *CloudSyncTask) { c.Interval = "daily"; c.Hour = 3; c.LastRun = p(at(2, 3, 0)) }), at(5, 11, 0), true},
		"weekly, wrong day":             {with(func(c *CloudSyncTask) { c.Interval = "weekly"; c.DayOfWeek = 0; c.Hour = 2 }), at(3, 2, 0), false},
		"weekly, its day":               {with(func(c *CloudSyncTask) { c.Interval = "weekly"; c.DayOfWeek = 0; c.Hour = 2 }), at(4, 2, 0), true},
		"weekly, ran this week":         {with(func(c *CloudSyncTask) { c.Interval = "weekly"; c.DayOfWeek = 0; c.Hour = 2; c.LastRun = p(at(4, 2, 0)) }), at(8, 12, 0), false},
		"weekly, next week":             {with(func(c *CloudSyncTask) { c.Interval = "weekly"; c.DayOfWeek = 0; c.Hour = 2; c.LastRun = p(at(4, 2, 0)) }), at(11, 2, 1), true},
	} {
		if got := cloudTaskDue(c.task, c.now); got != c.want {
			t.Errorf("%s: due = %v, want %v", name, got, c.want)
		}
	}
}
