package handlers

import (
	"encoding/json"
	"net/http"
)

type SystemSettings struct {
	ARCLimitGB          int  `json:"arc_limit_gb"`
	Swappiness          int  `json:"swappiness"`
	RealtimeEnabled     bool `json:"realtime_enabled"`
	PeriodicEnabled     bool `json:"periodic_enabled"`
	InotifyWarnThreshold int `json:"inotify_warn_threshold"`
	MemoryWarnThreshold  int `json:"memory_warn_threshold"`
	IOWaitWarnThreshold  int `json:"iowait_warn_threshold"`
	WebSocketAlerts     bool `json:"websocket_alerts"`
}

type SystemMetrics struct {
	Inotify InotifyMetrics `json:"inotify"`
	Memory  MemoryMetrics  `json:"memory"`
	ARC     ARCMetrics     `json:"arc"`
	IOWait  int            `json:"iowait"`
}

type InotifyMetrics struct {
	Used    int     `json:"used"`
	Limit   int     `json:"limit"`
	Percent float64 `json:"percent"`
}

type MemoryMetrics struct {
	Used    uint64  `json:"used"`
	Total   uint64  `json:"total"`
	Percent float64 `json:"percent"`
}

type ARCMetrics struct {
	Used    uint64  `json:"used"`
	Limit   uint64  `json:"limit"`
	Percent float64 `json:"percent"`
}

func HandleSystemSettings(w http.ResponseWriter, r *http.Request) {
	// This would load/save settings from config file
	// Placeholder implementation
	
	if r.Method == "GET" {
		settings := SystemSettings{
			ARCLimitGB:          8,
			Swappiness:          10,
			RealtimeEnabled:     true,
			PeriodicEnabled:     true,
			InotifyWarnThreshold: 80,
			MemoryWarnThreshold:  85,
			IOWaitWarnThreshold:  20,
			WebSocketAlerts:     true,
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(settings)
		
	} else if r.Method == "POST" {
		var settings SystemSettings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		
		// Save settings
		// Apply to system (update sysctl, ZFS, etc.)
		
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
	}
}

func HandleSystemMetrics(w http.ResponseWriter, r *http.Request) {
	// Collect real-time metrics
	metrics := SystemMetrics{
		Inotify: InotifyMetrics{
			Used:    125000,
			Limit:   524288,
			Percent: 23.8,
		},
		Memory: MemoryMetrics{
			Used:    12 * 1024 * 1024 * 1024,
			Total:   32 * 1024 * 1024 * 1024,
			Percent: 37.5,
		},
		ARC: ARCMetrics{
			Used:    6 * 1024 * 1024 * 1024,
			Limit:   8 * 1024 * 1024 * 1024,
			Percent: 75.0,
		},
		IOWait: 8,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}
