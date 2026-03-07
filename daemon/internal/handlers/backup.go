package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/security"
)

// ExecuteRsync handles GET (list tasks) and POST (enqueue rsync backup job).
// POST returns {"job_id": "..."} immediately; poll GET /api/jobs/{id} for status.
func ExecuteRsync(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// GET: return empty task list (kept for UI compatibility)
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"tasks":   []interface{}{},
		})
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	src, dst := req.Source, req.Destination
	id := jobs.Start("rsync_backup", func(j *jobs.Job) {
		output, err := cmdutil.RunSlow("rsync", "-avz", "--progress", src, dst)
		audit.LogActivity(user, "rsync_backup", map[string]interface{}{
			"source":      src,
			"destination": dst,
			"success":     err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{"output": string(output)})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}
