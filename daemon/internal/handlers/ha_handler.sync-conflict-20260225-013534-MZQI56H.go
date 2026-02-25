package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"dplaned/internal/ha"
	"github.com/gorilla/mux"
)

// HAHandler provides HTTP endpoints for cluster HA management.
type HAHandler struct {
	mgr *ha.Manager
}

// NewHAHandler creates a handler backed by the given cluster manager.
func NewHAHandler(mgr *ha.Manager) *HAHandler {
	return &HAHandler{mgr: mgr}
}

// GetStatus returns the full cluster status.
// GET /api/ha/status
func (h *HAHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	status := h.mgr.Status()
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"cluster": status,
	})
}

// RegisterPeer adds a new peer node to this cluster.
// POST /api/ha/peers
// Body: { "id": "node2", "name": "NAS-B", "address": "http://10.0.0.2:5050", "role": "standby" }
func (h *HAHandler) RegisterPeer(w http.ResponseWriter, r *http.Request) {
	var req ha.ClusterNode
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.ID == "" || req.Address == "" {
		respondErrorSimple(w, "id and address are required", http.StatusBadRequest)
		return
	}
	if err := h.mgr.RegisterPeer(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Failed to register peer", err)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"success": true,
		"message": "Peer registered — heartbeat will begin within 15 seconds",
		"peer_id": req.ID,
	})
}

// RemovePeer removes a peer from the cluster.
// DELETE /api/ha/peers/{id}
func (h *HAHandler) RemovePeer(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if err := h.mgr.RemovePeer(id); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to remove peer", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Peer removed",
	})
}

// PeerHeartbeat is called by peer daemons to report their liveness.
// POST /api/ha/heartbeat
// Body: { "node_id": "...", "address": "...", "role": "...", "version": "..." }
func (h *HAHandler) PeerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb ha.HeartbeatPayload
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid heartbeat payload", err)
		return
	}
	if hb.NodeID == "" {
		respondErrorSimple(w, "node_id is required", http.StatusBadRequest)
		return
	}
	h.mgr.HandleHeartbeat(hb)

	// Reply with our own identity so peers can detect our role
	info := h.mgr.LocalInfo()
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"node_id":  info["id"],
		"address":  info["address"],
		"version":  info["version"],
	})
}

// SetPeerRole updates a peer's role (e.g. promote standby → active for manual failover).
// POST /api/ha/peers/{id}/role
// Body: { "role": "active" }
func (h *HAHandler) SetPeerRole(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	role := ha.NodeRole(strings.ToLower(req.Role))
	if role != ha.RoleActive && role != ha.RoleStandby {
		respondErrorSimple(w, "role must be 'active' or 'standby'", http.StatusBadRequest)
		return
	}
	if err := h.mgr.SetPeerRole(id, role); err != nil {
		respondError(w, http.StatusNotFound, "Failed to update role", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Peer role updated to " + req.Role,
	})
}

// LocalNodeInfo returns this node's identity (no auth required — used by peers to auto-discover).
// GET /api/ha/local
func (h *HAHandler) LocalNodeInfo(w http.ResponseWriter, r *http.Request) {
	info := h.mgr.LocalInfo()
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"node":    info,
	})
}

// localNodeID returns the machine ID from /etc/machine-id, falling back to hostname.
func LocalNodeID() string {
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		id := strings.TrimSpace(string(data))
		if len(id) >= 8 {
			return id[:8] // use first 8 chars as short ID
		}
	}
	host, _ := os.Hostname()
	return host
}
