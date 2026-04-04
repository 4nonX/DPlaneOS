package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"dplaned/internal/jobs"
)

// HandleJobStatus serves GET /api/jobs/{id}
// Returns the current state of a background job.
//
// Response while running:
//
//	{"id":"...","type":"zfs_send","status":"running","started_at":"...","progress":{...}}
//
// Response on success:
//
//	{"id":"...","type":"zfs_send","status":"done","result":{...},"started_at":"...","finished_at":"..."}
//
// Response on failure:
//
//	{"id":"...","type":"zfs_send","status":"failed","error":"...","started_at":"...","finished_at":"..."}
func HandleJobStatus(w http.ResponseWriter, r *http.Request) {
	// Extract id from path: /api/jobs/{id}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/")
	id := parts[0]

	if id == "" {
		respondErrorSimple(w, "missing job id", http.StatusBadRequest)
		return
	}

	j := jobs.Get(id)
	if j == nil {
		respondErrorSimple(w, "job not found", http.StatusNotFound)
		return
	}

	snap := j.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}
