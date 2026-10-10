// Package handlers provides HTTP request handlers.
// health.go: Phase 3.3 - Health check HTTP endpoints
package handlers

import (
	"encoding/json"
	"net/http"
	"dplaned/internal/monitoring"
)

// HealthHandler wraps a health checker for HTTP endpoints.
type HealthHandler struct {
	checker *monitoring.Checker
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(checker *monitoring.Checker) *HealthHandler {
	return &HealthHandler{checker: checker}
}

// SystemHealthHandler returns GET /api/health with overall system status.
func (h *HealthHandler) SystemHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	health := h.checker.GetLastHealth()

	// The report itself always answers 200 (the state is in the body, the
	// web interface reads it); /api/health/live and /ready are the probes
	// whose status code follows the health.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"overall":    health.Overall,
		"subsystems": health.Subsystems,
		"checked_at": health.CheckedAt,
	})
}

// SubsystemHealthHandler returns GET /api/health/:subsystem for specific subsystem.
func (h *HealthHandler) SubsystemHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract subsystem name from URL path
	subsystem := r.URL.Query().Get("subsystem")
	if subsystem == "" {
		http.Error(w, "Missing 'subsystem' query parameter", http.StatusBadRequest)
		return
	}

	health := h.checker.GetLastHealth()
	sh, exists := health.Subsystems[subsystem]

	if !exists {
		http.Error(w, "Subsystem not found: "+subsystem, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Set HTTP status based on subsystem health
	switch sh.Status {
	case monitoring.HealthOK:
		w.WriteHeader(http.StatusOK)
	case monitoring.HealthDegraded:
		w.WriteHeader(http.StatusOK)
	case monitoring.HealthUnavailable:
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}

	json.NewEncoder(w).Encode(sh)
}

// LivenessProbe returns 200 if daemon is responding, 503 if critical subsystems down.
// Used by Kubernetes-style liveness probes.
func (h *HealthHandler) LivenessProbe(w http.ResponseWriter, r *http.Request) {
	health := h.checker.GetLastHealth()

	if health.Overall == monitoring.HealthUnavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("unavailable"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("alive"))
}

// ReadinessProbe returns 200 only if all critical subsystems are OK.
// Used by Kubernetes-style readiness probes.
func (h *HealthHandler) ReadinessProbe(w http.ResponseWriter, r *http.Request) {
	health := h.checker.GetLastHealth()

	ready := health.Overall == monitoring.HealthOK
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}
