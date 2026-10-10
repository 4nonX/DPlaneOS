package gitops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"dplaned/internal/security"

	"github.com/google/uuid"
)

// Replication schedules live where the replication handlers keep them
// (<config dir>/replication-schedules.json, records keyed by id and
// referencing a peer by remote_id). In state.yaml a schedule names its peer
// ("remote"), which reads well and stays the same on every node.

var (
	replConfigMu  sync.Mutex
	replConfigDir = "/etc/dplaneos"
)

// SetConfigDir points GitOps at the daemon's config directory (--config-dir).
func SetConfigDir(dir string) {
	replConfigMu.Lock()
	defer replConfigMu.Unlock()
	replConfigDir = dir
}

func replFile(name string) string {
	replConfigMu.Lock()
	defer replConfigMu.Unlock()
	return filepath.Join(replConfigDir, name)
}

var validReplIntervals = map[string]bool{"hourly": true, "daily": true, "weekly": true, "manual": true}

// readScheduleRecords returns the stored schedules as raw records, so fields
// GitOps does not manage (last run, last snapshot, ...) are kept on write.
func readScheduleRecords() ([]map[string]any, error) {
	data, err := os.ReadFile(replFile("replication-schedules.json"))
	if os.IsNotExist(err) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &recs); err != nil {
			return nil, fmt.Errorf("replication-schedules.json: %w", err)
		}
	}
	return recs, nil
}

func writeScheduleRecords(recs []map[string]any) error {
	path := replFile("replication-schedules.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path+".gitops.tmp", path, data)
}

// peerNames maps peer id -> name and name -> id (replication-remotes.json).
func peerNames() (byID, byName map[string]string, err error) {
	byID, byName = map[string]string{}, map[string]string{}
	data, err := os.ReadFile(replFile("replication-remotes.json"))
	if os.IsNotExist(err) {
		return byID, byName, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var peers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &peers); err != nil {
			return nil, nil, fmt.Errorf("replication-remotes.json: %w", err)
		}
	}
	for _, p := range peers {
		byID[p.ID] = p.Name
		byName[p.Name] = p.ID
	}
	return byID, byName, nil
}

func str(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func boolean(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }

func integer(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func readLiveReplication() ([]LiveReplication, error) {
	recs, err := readScheduleRecords()
	if err != nil {
		return nil, err
	}
	byID, _, err := peerNames()
	if err != nil {
		return nil, err
	}
	var out []LiveReplication
	for _, m := range recs {
		remote := byID[str(m, "remote_id")]
		if remote == "" {
			remote = str(m, "remote_id") // peer removed: show its id
		}
		out = append(out, LiveReplication{
			Name:              str(m, "name"),
			SourceDataset:     str(m, "source_dataset"),
			Remote:            remote,
			RemotePool:        str(m, "remote_pool"),
			Interval:          str(m, "interval"),
			TriggerOnSnapshot: boolean(m, "trigger_on_snapshot"),
			Incremental:       boolean(m, "incremental"),
			Compress:          boolean(m, "compress"),
			RateLimitMB:       integer(m, "rate_limit_mb"),
			Enabled:           boolean(m, "enabled"),
		})
	}
	return out, nil
}

func reconcileReplication(name string, dr *DesiredReplication) error {
	if dr == nil {
		return fmt.Errorf("no desired replication spec for %q", name)
	}
	if !validReplIntervals[dr.Interval] {
		return fmt.Errorf("replication %q: interval must be hourly, daily, weekly or manual", name)
	}
	if security.ValidateDatasetName(dr.SourceDataset) != nil || security.ValidateDatasetName(dr.RemotePool) != nil {
		return fmt.Errorf("replication %q: invalid source_dataset or remote_pool", name)
	}
	_, byName, err := peerNames()
	if err != nil {
		return err
	}
	remoteID, ok := byName[dr.Remote]
	if !ok {
		return fmt.Errorf("replication %q: no peer named %q (add it under Replication > Peers first)", name, dr.Remote)
	}
	recs, err := readScheduleRecords()
	if err != nil {
		return err
	}
	var rec map[string]any
	for _, m := range recs {
		if str(m, "name") == name {
			rec = m
		}
	}
	if rec == nil {
		rec = map[string]any{"id": uuid.New().String()}
		recs = append(recs, rec)
	}
	rec["name"] = name
	rec["source_dataset"] = dr.SourceDataset
	rec["remote_id"] = remoteID
	rec["remote_pool"] = dr.RemotePool
	rec["interval"] = dr.Interval
	rec["trigger_on_snapshot"] = dr.TriggerOnSnapshot
	rec["incremental"] = dr.Incremental
	rec["compress"] = dr.Compress
	rec["rate_limit_mb"] = dr.RateLimitMB
	rec["enabled"] = dr.Enabled
	return writeScheduleRecords(recs)
}

func deleteReplication(name string) error {
	recs, err := readScheduleRecords()
	if err != nil {
		return err
	}
	kept := recs[:0]
	for _, m := range recs {
		if str(m, "name") != name {
			kept = append(kept, m)
		}
	}
	return writeScheduleRecords(kept)
}
