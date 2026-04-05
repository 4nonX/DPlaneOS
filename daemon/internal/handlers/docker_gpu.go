package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/hardware"
)

// HandleDockerGPUPassthroughReport exposes host GPU / Docker runtime facts and compose hints.
// GET /api/docker/gpu — requires docker:read.
func HandleDockerGPUPassthroughReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rep, err := hardware.BuildGPUPassthroughReport()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"report":  rep,
	})
}
