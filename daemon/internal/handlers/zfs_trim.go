package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var trimPoolRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
var trimRateRe = regexp.MustCompile(`^[0-9]+[KMGT]$`)

// PoolTrimStatus describes TRIM state for a single pool.
type PoolTrimStatus struct {
	Pool    string `json:"pool"`
	Running bool   `json:"running"`
	Status  string `json:"status"`
}

// StartTrim handles POST /api/zfs/trim/start
func StartTrim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
		Rate string `json:"rate,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !trimPoolRe.MatchString(req.Pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}
	args := []string{"trim"}
	if req.Rate != "" {
		if !trimRateRe.MatchString(strings.ToUpper(req.Rate)) {
			respondErrorSimple(w, "invalid rate format: use e.g. 400M, 1G", http.StatusBadRequest)
			return
		}
		args = append(args, "-r", req.Rate)
	}
	args = append(args, req.Pool)
	if _, err := executeCommandWithTimeout(TimeoutFast, "zpool", args); err != nil {
		respondErrorSimple(w, "Failed to start TRIM: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "TRIM started for pool " + req.Pool})
}

// StopTrim handles POST /api/zfs/trim/stop
func StopTrim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !trimPoolRe.MatchString(req.Pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}
	if _, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"trim", "-s", req.Pool}); err != nil {
		respondErrorSimple(w, "Failed to stop TRIM: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "TRIM stopped for pool " + req.Pool})
}

// GetTrimStatus handles GET /api/zfs/trim/status
func GetTrimStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	args := []string{"status", "-t"}
	if pool != "" {
		if !trimPoolRe.MatchString(pool) {
			respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
			return
		}
		args = append(args, pool)
	}
	out, err := executeCommandWithTimeout(TimeoutFast, "zpool", args)
	if err != nil {
		respondErrorSimple(w, "Failed to get TRIM status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	statuses := parseTrimStatus(out, pool)
	respondOK(w, map[string]interface{}{"success": true, "pools": statuses})
}

func parseTrimStatus(out, filterPool string) []PoolTrimStatus {
	var result []PoolTrimStatus
	currentPool := ""
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "pool: ") {
			currentPool = strings.TrimPrefix(line, "pool: ")
			if filterPool == "" || currentPool == filterPool {
				result = append(result, PoolTrimStatus{Pool: currentPool})
			}
		} else if strings.HasPrefix(line, "trim:") && currentPool != "" {
			if filterPool != "" && currentPool != filterPool {
				continue
			}
			trimLine := strings.TrimSpace(strings.TrimPrefix(line, "trim:"))
			if len(result) > 0 && result[len(result)-1].Pool == currentPool {
				result[len(result)-1].Running = strings.Contains(trimLine, "in progress")
				result[len(result)-1].Status = trimLine
			}
		}
	}
	return result
}
