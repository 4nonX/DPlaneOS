package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// ExecuteRsync handles GET (list tasks — returns empty, rsync is fire-and-forget)
// and POST (execute rsync backup)
func ExecuteRsync(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// GET: return empty task list (rsync jobs are not persisted)
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

	output, err := cmdutil.RunSlow("rsync", "-avz", "--progress", req.Source, req.Destination)

	audit.LogActivity(user, "rsync_backup", map[string]interface{}{
		"source":      req.Source,
		"destination": req.Destination,
		"success":     err == nil,
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
