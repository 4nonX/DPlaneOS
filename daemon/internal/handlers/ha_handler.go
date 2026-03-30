package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/ha"
	"dplaned/internal/jobs"
	"dplaned/internal/gitops"
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
	if NixWriter != nil {
		status.HAEnabled = NixWriter.State().HAEnable
	}
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
		"message": "Peer registered - heartbeat will begin within 15 seconds",
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

// LocalNodeInfo returns this node's identity (no auth required - used by peers to auto-discover).
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

// GetFencingConfig fetches STONITH parameters.
// GET /api/ha/fencing/configure
func (h *HAHandler) GetFencingConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.mgr.GetFencingConfig()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read fencing config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"config":  cfg,
	})
}

// ConfigureFencing configures STONITH parameters.
// POST /api/ha/fencing/configure
func (h *HAHandler) ConfigureFencing(w http.ResponseWriter, r *http.Request) {
	var req ha.FencingConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if err := h.mgr.SaveFencingConfig(req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save fencing config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Fencing configuration updated successfully",
	})
}

// GetReplicationConfig fetches continuous active-to-standby ZFS sync parameters.
// GET /api/ha/replication/configure
func (h *HAHandler) GetReplicationConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.mgr.GetReplicationConfig()
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"config":  cfg,
	})
}

// ConfigureHAReplication sets up continuous active-to-standby ZFS sync.
// POST /api/ha/replication/configure
func (h *HAHandler) ConfigureHAReplication(w http.ResponseWriter, r *http.Request) {
	var req ha.ReplicationConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	
	// Default interval to 30 if zero or negative
	if req.IntervalSecs < 10 {
		req.IntervalSecs = 30
	}

	if req.LocalPool == "" || req.RemotePool == "" || req.RemoteHost == "" || req.SSHKeyPath == "" {
		respondErrorSimple(w, "local_pool, remote_pool, remote_host, and ssh_key_path are required", http.StatusBadRequest)
		return
	}

	if req.RemoteUser == "" {
		req.RemoteUser = "root"
	}
	if req.RemotePort == 0 {
		req.RemotePort = 22
	}

	if err := h.mgr.SetReplicationConfig(&req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save HA replication config", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "HA replication configured and background loop started (if active)",
	})
}

// Promote triggers the manual failover orchestration on a standby node.
// If STONITH fencing is configured and a leader is provided, the leader is
// fenced (BMC chassis power off, polled to confirmed dark) before promotion
// begins, preventing split-brain. Without fencing the operator must ensure
// the primary is offline before calling this endpoint.
// POST /api/ha/promote
func (h *HAHandler) Promote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Candidate string `json:"candidate"`
		Leader    string `json:"leader"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Candidate == "" {
		req.Candidate = LocalNodeID()
	}

	// Read fencing config synchronously so the job can act on it without
	// a DB call inside the goroutine.
	fencingCfg, _ := h.mgr.GetFencingConfig()

	jobID := jobs.Start("ha_promote", func(j *jobs.Job) {
		// ── Step 1: STONITH fencing ──────────────────────────────────────────
		if fencingCfg.Enable && req.Leader != "" {
			j.Log(fmt.Sprintf("HA Promote: Fencing leader %q at BMC %s before promotion...", req.Leader, fencingCfg.BMCIP))
			if err := ha.ExecuteFencing(req.Leader, fencingCfg); err != nil {
				j.Log(fmt.Sprintf("HA Promote: STONITH fencing failed — aborting to prevent split-brain: %v", err))
				j.Fail("Fencing failed: " + err.Error())
				return
			}
			j.Log("HA Promote: Leader node confirmed fenced (chassis dark). Proceeding with promotion.")
		} else if req.Leader != "" {
			j.Log("HA Promote: WARNING — fencing is not configured. Ensure the leader node is fully offline to avoid split-brain before continuing.")
		}

		// ── Step 2: Promotion orchestration ─────────────────────────────────
		j.Log(fmt.Sprintf("HA Promote: Promoting candidate %q (leader %q)...", req.Candidate, req.Leader))
		ha.ExecutePromotion(req.Candidate, req.Leader)
		j.Log("HA Promote: Promotion sequence complete.")
		j.Done(map[string]interface{}{
			"candidate": req.Candidate,
			"leader":    req.Leader,
		})
	})

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Failover promotion initiated. Monitor progress via job " + jobID + ".",
		"job_id":  jobID,
	})
}

// TriggerFence fires a manual STONITH request against a given peer.
// POST /api/ha/fence
func (h *HAHandler) TriggerFence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	cfg, err := h.mgr.GetFencingConfig()
	if err != nil || !cfg.Enable {
		respondError(w, http.StatusBadRequest, "Fencing is not enabled or properly configured", err)
		return
	}

	// Trigger fencing asynchronously since it could take up to 60s
	go func() {
		if err := ha.ExecuteFencing(req.NodeID, cfg); err != nil {
			// Already logged to audit in ExecuteFencing
		}
	}()

	respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"success": true,
		"message": "Fencing sequence initiated asynchronously for Node " + req.NodeID,
	})
}

// ToggleHA arms or disarms the NixOS HA cluster modules.
// POST /api/ha/toggle {"enable": true/false}
func (h *HAHandler) ToggleHA(w http.ResponseWriter, r *http.Request) {
	if NixWriter == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "High Availability requires NixOS",
		})
		return
	}
	var req struct {
		Enable bool `json:"enable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !gitops.TryLock() {
		respondJSON(w, 423, map[string]interface{}{
			"success": false,
			"error":   "A reconciliation is already in progress. Please wait for the current operation to finish.",
		})
		return
	}

	if err := NixWriter.SetHA(req.Enable); err != nil {
		gitops.Unlock()
		respondError(w, http.StatusInternalServerError, "Failed to update NixOS state", err)
		return
	}

	// Trigger nixos-rebuild switch via the jobs system for frontend visibility
	jobID := jobs.Start("nixos_rebuild", func(j *jobs.Job) {
		defer gitops.Unlock()
		action := "disabling"
		if req.Enable {
			action = "enabling"
		}
		j.Log(fmt.Sprintf("HA: User is %s HA - triggering nixos-rebuild switch", action))
		
		// Run rebuild using the whitelisted key
		out, err := cmdutil.RunExtreme("nixos-rebuild", "switch")
		if err != nil {
			log.Printf("HA: NixOS rebuild failed: %v\nOutput: %s", err, string(out))
			j.Log(fmt.Sprintf("ERROR: NixOS reconfiguration failed: %v", err))
			j.Fail(err.Error())
			DispatchAlert("critical", "HA_REBUILD_FAILED", "system", fmt.Sprintf("NixOS reconfig failed: %v", err))
		} else {
			log.Printf("HA: NixOS rebuild success. HA is now %v", req.Enable)
			j.Log("NixOS reconfiguration completed successfully.")
			j.Done(map[string]interface{}{"output": string(out)})
		}
	})

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "HA state updated. System reconfiguration started.",
		"job_id":  jobID,
	})
}

// RegisterMaintenance sets the cluster into maintenance mode for a given duration.
// POST /api/ha/maintenance {"seconds": 300}
func (h *HAHandler) RegisterMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Seconds int `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Seconds = 300 // default
	}
	if req.Seconds < 0 {
		req.Seconds = 0
	} else if req.Seconds > 3600 {
		req.Seconds = 3600 // Cap at 1 hour for safety
	}

	h.mgr.SetMaintenanceMode(time.Duration(req.Seconds) * time.Second)

	status := "enabled"
	if req.Seconds == 0 {
		status = "disabled"
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Maintenance mode %s. Fencing suspended for %d seconds.", status, req.Seconds),
	})
}

