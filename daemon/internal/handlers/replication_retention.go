// replication_retention.go - ZFS snapshot retention policy engine
//
// Snapshot retention is the local counterpart to zfs send/receive replication.
// Once a snapshot has been successfully replicated to a remote, the local copy
// of older snapshots can be pruned to reclaim pool space without losing the
// backup. Retention policies define how many snapshots to keep per time bucket
// (hourly, daily, weekly, monthly) for a given dataset.
//
// Only snapshots whose names start with the policy Prefix are pruned, so manual
// snapshots (created ad hoc from the UI or CLI) are never touched. A policy
// without a prefix prunes nothing. The snapshot a replication schedule uses as
// its incremental base is never pruned either: losing it forces a full resend.
//
// Keep counts follow the usual convention (sanoid, zettarepl): keep_daily = 7
// keeps the newest snapshot of each of the 7 most recent days that have one.
// The tiers add up; a snapshot kept by any tier is kept.

package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// SnapshotRetentionPolicy describes how many snapshots to keep per time bucket
// for a dataset and its children. A zero count leaves that tier unused; with
// every count zero the policy keeps everything.
type SnapshotRetentionPolicy struct {
	ID      string `json:"id"`
	Dataset string `json:"dataset"` // the dataset; its children are covered too, each on its own
	Prefix  string `json:"prefix"`  // only prune snapshots whose @name starts with this prefix
	// How many buckets of each tier to retain the newest snapshot of. 0 = tier unused.
	KeepHourly  int  `json:"keep_hourly"`
	KeepDaily   int  `json:"keep_daily"`
	KeepWeekly  int  `json:"keep_weekly"`
	KeepMonthly int  `json:"keep_monthly"`
	KeepYearly  int  `json:"keep_yearly"`
	Enabled     bool `json:"enabled"`
}

const retentionPoliciesFile = "snapshot-retention-policies.json"

func loadRetentionPolicies() ([]SnapshotRetentionPolicy, error) {
	replStateMu.RLock()
	defer replStateMu.RUnlock()
	data, err := os.ReadFile(configPath(retentionPoliciesFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []SnapshotRetentionPolicy{}, nil
		}
		return nil, err
	}
	var policies []SnapshotRetentionPolicy
	return policies, json.Unmarshal(data, &policies)
}

func saveRetentionPolicies(policies []SnapshotRetentionPolicy) error {
	replStateMu.Lock()
	defer replStateMu.Unlock()
	data, err := json.MarshalIndent(policies, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(retentionPoliciesFile), data, 0644)
}

// HandleRetentionPolicies handles GET (list) and POST (create/update/delete).
func HandleRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		policies, err := loadRetentionPolicies()
		if err != nil {
			respondErrorSimple(w, "Failed to load policies: "+err.Error(), http.StatusInternalServerError)
			return
		}
		respondOK(w, map[string]any{"success": true, "policies": policies})

	case http.MethodPost:
		var req struct {
			Action string                  `json:"action"` // "create" | "update" | "delete"
			Policy SnapshotRetentionPolicy `json:"policy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
			return
		}
		// A delete names the policy by ID only (the web UI sends nothing else).
		if req.Action != "delete" {
			if !isValidDataset(req.Policy.Dataset) {
				respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(req.Policy.Prefix) == "" {
				respondErrorSimple(w, "A prefix is required (e.g. auto-): without one the policy would prune manual snapshots", http.StatusBadRequest)
				return
			}
		}
		policies, err := loadRetentionPolicies()
		if err != nil {
			respondErrorSimple(w, "Failed to load policies", http.StatusInternalServerError)
			return
		}
		switch req.Action {
		case "create":
			req.Policy.ID = fmt.Sprintf("ret-%d", time.Now().UnixNano())
			policies = append(policies, req.Policy)
		case "update":
			found := false
			for i, p := range policies {
				if p.ID == req.Policy.ID {
					policies[i] = req.Policy
					found = true
					break
				}
			}
			if !found {
				respondErrorSimple(w, "Policy not found", http.StatusNotFound)
				return
			}
		case "delete":
			newPolicies := policies[:0]
			for _, p := range policies {
				if p.ID != req.Policy.ID {
					newPolicies = append(newPolicies, p)
				}
			}
			policies = newPolicies
		default:
			respondErrorSimple(w, "Unknown action", http.StatusBadRequest)
			return
		}
		if err := saveRetentionPolicies(policies); err != nil {
			respondErrorSimple(w, "Failed to save policies", http.StatusInternalServerError)
			return
		}
		respondOK(w, map[string]any{"success": true})

	default:
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// retSnap is a named snapshot with its creation time.
type retSnap struct {
	name    string
	created time.Time
}

// ApplyRetentionPolicy prunes the snapshots of dataset and its children
// according to the given policy, each dataset on its own: a child's snapshots
// never take a bucket from the parent's. It only touches snapshots whose @name
// starts with policy.Prefix and never a replication base (see
// replicationBaseSnapshots). Returns the names of the pruned snapshots.
func ApplyRetentionPolicy(dataset string, policy SnapshotRetentionPolicy) ([]string, error) {
	if strings.TrimSpace(policy.Prefix) == "" {
		return nil, fmt.Errorf("policy %s has no prefix: nothing is pruned", policy.ID)
	}
	out, err := executeCommandWithTimeout(TimeoutFast, "ret_zfs_list_snapshots",
		[]string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,creation", "-r", "-s", "creation", dataset})
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	byDataset := map[string][]retSnap{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ds, snapName, ok := strings.Cut(fields[0], "@")
		if !ok || !strings.HasPrefix(snapName, policy.Prefix) {
			continue
		}
		created, parseErr := parseZFSCreation(fields[1:])
		if parseErr != nil {
			continue
		}
		byDataset[ds] = append(byDataset[ds], retSnap{name: fields[0], created: created})
	}

	protected, err := replicationBaseSnapshots()
	if err != nil {
		return nil, err
	}
	var pruned []string
	for _, snaps := range byDataset {
		// Newest first, so the keep logic retains the most recent snapshots.
		sort.Slice(snaps, func(i, j int) bool {
			return snaps[i].created.After(snaps[j].created)
		})
		keep := computeSnapKeepSet(snaps, policy)
		for _, s := range snaps {
			if keep[s.name] || protected[s.name] {
				continue
			}
			if _, delErr := executeCommandWithTimeout(TimeoutMedium, "ret_zfs_destroy_snapshot",
				[]string{"destroy", s.name}); delErr != nil {
				log.Printf("retention: failed to destroy %s: %v", s.name, delErr)
			} else {
				pruned = append(pruned, s.name)
				log.Printf("retention: pruned %s", s.name)
			}
		}
	}
	return pruned, nil
}

// replicationBaseSnapshots returns the snapshots replication schedules use as
// their incremental base. Pruning one breaks the chain and forces a full send.
// Without the schedules nothing may be pruned, so a read error is returned.
func replicationBaseSnapshots() (map[string]bool, error) {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		return nil, fmt.Errorf("cannot read replication schedules, so nothing is pruned: %w", err)
	}
	protected := map[string]bool{}
	for _, s := range schedules {
		if s.LastReplicatedSnapshot != "" {
			protected[s.LastReplicatedSnapshot] = true
		}
	}
	return protected, nil
}

// computeSnapKeepSet returns the set of snapshot names to retain, given
// snapshots sorted newest first. For each tier with a keep count N, the newest
// snapshot of each of the N most recent buckets (hours, days, ISO weeks,
// months, years) that hold a snapshot is kept. With every count zero all
// snapshots are kept.
func computeSnapKeepSet(snaps []retSnap, policy SnapshotRetentionPolicy) map[string]bool {
	keep := map[string]bool{}
	if policy.KeepHourly == 0 && policy.KeepDaily == 0 &&
		policy.KeepWeekly == 0 && policy.KeepMonthly == 0 && policy.KeepYearly == 0 {
		for _, s := range snaps {
			keep[s.name] = true
		}
		return keep
	}

	tiers := []struct {
		count  int
		bucket func(time.Time) string
	}{
		{policy.KeepHourly, func(t time.Time) string { return t.Format("2006-01-02T15") }},
		{policy.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") }},
		{policy.KeepWeekly, func(t time.Time) string {
			y, w := t.ISOWeek()
			return fmt.Sprintf("%04d-W%02d", y, w)
		}},
		{policy.KeepMonthly, func(t time.Time) string { return t.Format("2006-01") }},
		{policy.KeepYearly, func(t time.Time) string { return t.Format("2006") }},
	}
	for _, tier := range tiers {
		if tier.count <= 0 {
			continue
		}
		taken := map[string]bool{}
		for _, s := range snaps {
			b := tier.bucket(s.created)
			if taken[b] {
				continue // an earlier (newer) snapshot already holds this bucket
			}
			if len(taken) == tier.count {
				break
			}
			taken[b] = true
			keep[s.name] = true
		}
	}
	return keep
}

// RunRetentionPolicies applies all enabled retention policies. Called from the
// replication monitor after each successful replication cycle.
func RunRetentionPolicies() {
	policies, err := loadRetentionPolicies()
	if err != nil {
		log.Printf("retention: failed to load policies: %v", err)
		return
	}
	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		pruned, err := ApplyRetentionPolicy(p.Dataset, p)
		if err != nil {
			log.Printf("retention: error applying policy %s (dataset=%s): %v", p.ID, p.Dataset, err)
		} else if len(pruned) > 0 {
			log.Printf("retention: pruned %d snapshot(s) from %s", len(pruned), p.Dataset)
		}
	}
}

// parseZFSCreation parses the creation time fields from `zfs list -o name,creation`.
// ZFS outputs a human-readable date; we parse common formats.
func parseZFSCreation(fields []string) (time.Time, error) {
	// Try Unix timestamp (from zfs list -p)
	if len(fields) == 1 {
		var ts int64
		if _, err := fmt.Sscanf(fields[0], "%d", &ts); err == nil {
			return time.Unix(ts, 0), nil
		}
	}
	// Try "Mon Jan 2 15:04 2006" (zfs list without -p)
	joined := strings.Join(fields, " ")
	for _, layout := range []string{
		"Mon Jan 2 15:04 2006",
		"Mon Jan  2 15:04 2006",
		"2006-01-02",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, joined); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse creation time: %q", joined)
}
