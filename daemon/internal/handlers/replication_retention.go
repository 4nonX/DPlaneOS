// replication_retention.go - ZFS snapshot retention policy engine
//
// Snapshot retention is the local counterpart to zfs send/receive replication.
// Once a snapshot has been successfully replicated to a remote, the local copy
// of older snapshots can be pruned to reclaim pool space without losing the
// backup. Retention policies define how many snapshots to keep per time bucket
// (hourly, daily, weekly, monthly) for a given dataset.
//
// A DPlaneOS snapshot name is expected to follow the convention:
//
//	<dataset>@auto-YYYY-MM-DDTHH:MM:SS  (ISO-8601 suffix)
//	<dataset>@<prefix>-YYYY-MM-DD       (date-only suffix)
//	<dataset>@<anything>                (manual - never pruned by this engine)
//
// Only snapshots whose names start with the policy Prefix are pruned.
// Manual snapshots (created ad hoc from the UI or CLI) are never touched.

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
// for a given dataset. Zero values mean "keep all" for that bucket.
type SnapshotRetentionPolicy struct {
	ID      string `json:"id"`
	Dataset string `json:"dataset"` // exact dataset name or prefix with trailing /
	Prefix  string `json:"prefix"`  // only prune snapshots whose @name starts with this prefix
	// How many of each bucket to retain. 0 = unlimited.
	KeepHourly  int `json:"keep_hourly"`
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
	KeepYearly  int `json:"keep_yearly"`
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
		if !isValidDataset(req.Policy.Dataset) {
			respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
			return
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

// ApplyRetentionPolicy prunes snapshots for one dataset according to the given policy.
// It only touches snapshots whose @name starts with policy.Prefix.
// Returns a list of pruned snapshot names and any errors encountered.
func ApplyRetentionPolicy(dataset string, policy SnapshotRetentionPolicy) ([]string, error) {
	out, err := executeCommandWithTimeout(TimeoutFast, "ret_zfs_list_snapshots",
		[]string{"list", "-H", "-t", "snapshot", "-o", "name,creation", "-r", "-s", "creation", dataset})
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	var snaps []retSnap

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		// Only consider snapshots matching the prefix.
		atIdx := strings.Index(name, "@")
		if atIdx < 0 {
			continue
		}
		snapSuffix := name[atIdx+1:]
		if policy.Prefix != "" && !strings.HasPrefix(snapSuffix, policy.Prefix) {
			continue
		}
		// Parse creation time from ZFS (format: "Mon Jan 2 15:04 2006" or Unix timestamp).
		// zfs list -o creation with -p returns a Unix timestamp.
		created, parseErr := parseZFSCreation(fields[1:])
		if parseErr != nil {
			continue
		}
		snaps = append(snaps, retSnap{name: name, created: created})
	}

	// Sort newest-first so keep logic retains the most recent snapshots.
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].created.After(snaps[j].created)
	})

	keep := computeSnapKeepSet(snaps, policy)
	var pruned []string
	for _, s := range snaps {
		if keep[s.name] {
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
	return pruned, nil
}

// computeSnapKeepSet returns the set of snapshot names to retain.
// It applies bucket-based retention: keep the N most recent per bucket.
func computeSnapKeepSet(snaps []retSnap, policy SnapshotRetentionPolicy) map[string]bool {
	keep := map[string]bool{}

	type bucketKey struct{ bucket, key string }
	seen := map[bucketKey]int{}

	for _, s := range snaps {
		t := s.created

		hourKey := fmt.Sprintf("%04d-%02d-%02dT%02d", t.Year(), t.Month(), t.Day(), t.Hour())
		dayKey := fmt.Sprintf("%04d-%02d-%02d", t.Year(), t.Month(), t.Day())
		weekKey := fmt.Sprintf("%04d-W%02d", t.Year(), isoWeek(t))
		monthKey := fmt.Sprintf("%04d-%02d", t.Year(), t.Month())
		yearKey := fmt.Sprintf("%04d", t.Year())

		retain := false

		if policy.KeepHourly > 0 {
			k := bucketKey{"hourly", hourKey}
			seen[k]++
			if seen[k] <= policy.KeepHourly {
				retain = true
			}
		}
		if policy.KeepDaily > 0 {
			k := bucketKey{"daily", dayKey}
			seen[k]++
			if seen[k] <= policy.KeepDaily {
				retain = true
			}
		}
		if policy.KeepWeekly > 0 {
			k := bucketKey{"weekly", weekKey}
			seen[k]++
			if seen[k] <= policy.KeepWeekly {
				retain = true
			}
		}
		if policy.KeepMonthly > 0 {
			k := bucketKey{"monthly", monthKey}
			seen[k]++
			if seen[k] <= policy.KeepMonthly {
				retain = true
			}
		}
		if policy.KeepYearly > 0 {
			k := bucketKey{"yearly", yearKey}
			seen[k]++
			if seen[k] <= policy.KeepYearly {
				retain = true
			}
		}
		// If all keep values are 0, retain everything (no limits configured).
		if policy.KeepHourly == 0 && policy.KeepDaily == 0 &&
			policy.KeepWeekly == 0 && policy.KeepMonthly == 0 && policy.KeepYearly == 0 {
			retain = true
		}
		if retain {
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

func isoWeek(t time.Time) int {
	_, w := t.ISOWeek()
	return w
}
