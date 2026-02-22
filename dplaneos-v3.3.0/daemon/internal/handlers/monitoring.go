package handlers

import (
	"encoding/json"
	"net/http"
	
	"dplaned/internal/monitoring"
)

type MonitoringHandler struct{}

func NewMonitoringHandler() *MonitoringHandler {
	return &MonitoringHandler{}
}

// GetInotifyStats returns current inotify usage statistics
func (h *MonitoringHandler) GetInotifyStats(w http.ResponseWriter, r *http.Request) {
	stats, err := monitoring.GetInotifyStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
