package gitops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func replDir(t *testing.T) string {
	dir := t.TempDir()
	SetConfigDir(dir)
	t.Cleanup(func() { SetConfigDir("/etc/dplaneos") })
	peers := `[{"id":"p-1","name":"backup-nas","host":"10.0.0.9","user":"root","port":22}]`
	if err := os.WriteFile(filepath.Join(dir, "replication-remotes.json"), []byte(peers), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReplicationApplyWritesHandlerFormat(t *testing.T) {
	dir := replDir(t)
	dr := &DesiredReplication{Name: "offsite", SourceDataset: "tank/data", Remote: "backup-nas", RemotePool: "backup",
		Interval: "daily", Incremental: true, Enabled: true}
	if err := reconcileReplication("offsite", dr); err != nil {
		t.Fatal(err)
	}
	// What the replication handlers read: snake_case records with remote_id.
	b, _ := os.ReadFile(filepath.Join(dir, "replication-schedules.json"))
	var recs []map[string]any
	if err := json.Unmarshal(b, &recs); err != nil || len(recs) != 1 {
		t.Fatalf("records %s %v", b, err)
	}
	if recs[0]["remote_id"] != "p-1" || recs[0]["id"] == "" || recs[0]["interval"] != "daily" {
		t.Errorf("record %v", recs[0])
	}

	// Runtime fields the handlers keep survive an update.
	recs[0]["last_replicated_snapshot"] = "tank/data@x"
	b, _ = json.Marshal(recs)
	_ = os.WriteFile(filepath.Join(dir, "replication-schedules.json"), b, 0644)
	dr.Interval = "hourly"
	if err := reconcileReplication("offsite", dr); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "replication-schedules.json"))
	if !strings.Contains(string(b), "tank/data@x") || !strings.Contains(string(b), `"hourly"`) {
		t.Errorf("after update: %s", b)
	}

	live, err := readLiveReplication()
	if err != nil || len(live) != 1 || live[0].Remote != "backup-nas" || live[0].Interval != "hourly" {
		t.Fatalf("live %+v %v", live, err)
	}
	if err := deleteReplication("offsite"); err != nil {
		t.Fatal(err)
	}
	if live, _ := readLiveReplication(); len(live) != 0 {
		t.Errorf("not deleted: %+v", live)
	}
}

func TestReplicationApplyRefusesBadSpecs(t *testing.T) {
	replDir(t)
	for _, dr := range []DesiredReplication{
		{Name: "x", SourceDataset: "tank/data", Remote: "nope", RemotePool: "backup", Interval: "daily"},
		{Name: "x", SourceDataset: "tank/data", Remote: "backup-nas", RemotePool: "backup", Interval: "yearly"},
		{Name: "x", SourceDataset: "-a", Remote: "backup-nas", RemotePool: "backup", Interval: "daily"},
	} {
		if err := reconcileReplication("x", &dr); err == nil {
			t.Errorf("accepted %+v", dr)
		}
	}
}

// Schedules made in the web interface have no remote_port: reading them
// used to panic (type assertion on a missing number).
func TestReadLiveReplicationHandlerRecords(t *testing.T) {
	dir := replDir(t)
	rec := `[{"id":"s1","name":"web","source_dataset":"tank/a","remote_id":"p-1","remote_pool":"b","interval":"daily","enabled":true}]`
	_ = os.WriteFile(filepath.Join(dir, "replication-schedules.json"), []byte(rec), 0644)
	live, err := readLiveReplication()
	if err != nil || len(live) != 1 || live[0].Remote != "backup-nas" {
		t.Fatalf("%+v %v", live, err)
	}
}
