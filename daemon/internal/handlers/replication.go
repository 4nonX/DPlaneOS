package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/security"
)

// ZFSSend enqueues a ZFS send operation and returns a job ID immediately.
// Poll GET /api/jobs/{id} for status.
func ZFSSend(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Snapshot string `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateSnapshotName(req.Snapshot); err != nil {
		http.Error(w, "Invalid snapshot name: "+err.Error(), http.StatusBadRequest)
		return
	}

	snapshot := req.Snapshot
	id := jobs.Start("zfs_send", func(j *jobs.Job) {
		output, err := cmdutil.RunSlow("zfs", "send", "-R", snapshot)
		audit.LogActivity(user, "zfs_send", map[string]interface{}{
			"snapshot": snapshot,
			"success":  err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{"output": string(output)})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}

// ZFSSendIncremental enqueues an incremental ZFS send and returns a job ID.
func ZFSSendIncremental(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		BaseSnapshot string `json:"base_snapshot"`
		NewSnapshot  string `json:"new_snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateSnapshotName(req.BaseSnapshot); err != nil {
		http.Error(w, "Invalid base snapshot: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := security.ValidateSnapshotName(req.NewSnapshot); err != nil {
		http.Error(w, "Invalid new snapshot: "+err.Error(), http.StatusBadRequest)
		return
	}

	base, newsnap := req.BaseSnapshot, req.NewSnapshot
	id := jobs.Start("zfs_send_incremental", func(j *jobs.Job) {
		output, err := cmdutil.RunSlow("zfs", "send", "-R", "-i", base, newsnap)
		audit.LogActivity(user, "zfs_send_incremental", map[string]interface{}{
			"base_snapshot": base,
			"new_snapshot":  newsnap,
			"success":       err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{"output": string(output)})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}

// ZFSReceive enqueues a ZFS receive and returns a job ID.
func ZFSReceive(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Dataset string `json:"dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDatasetName(req.Dataset); err != nil {
		http.Error(w, "Invalid dataset name: "+err.Error(), http.StatusBadRequest)
		return
	}

	dataset := req.Dataset
	id := jobs.Start("zfs_receive", func(j *jobs.Job) {
		output, err := cmdutil.RunSlow("zfs", "receive", "-F", dataset)
		audit.LogActivity(user, "zfs_receive", map[string]interface{}{
			"dataset": dataset,
			"success": err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{"output": string(output)})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}
