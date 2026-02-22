package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// ZFSSend sends ZFS snapshot for replication
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

	output, err := cmdutil.RunZFS("zfs", "send", "-R", req.Snapshot)

	audit.LogActivity(user, "zfs_send", map[string]interface{}{
		"snapshot": req.Snapshot,
		"success":  err == nil,
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
}

// ZFSSendIncremental sends incremental ZFS snapshot
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

	output, err := cmdutil.RunZFS("zfs", "send", "-R", "-i", req.BaseSnapshot, req.NewSnapshot)

	audit.LogActivity(user, "zfs_send_incremental", map[string]interface{}{
		"base_snapshot": req.BaseSnapshot,
		"new_snapshot":  req.NewSnapshot,
		"success":       err == nil,
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
}

// ZFSReceive receives ZFS snapshot
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

	output, err := cmdutil.RunZFS("zfs", "receive", "-F", req.Dataset)

	audit.LogActivity(user, "zfs_receive", map[string]interface{}{
		"dataset": req.Dataset,
		"success": err == nil,
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
}
