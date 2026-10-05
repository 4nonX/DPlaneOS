package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
	"dplaned/internal/ha"
	"dplaned/internal/jobs"
	"dplaned/internal/scsipr"
	"dplaned/internal/security"
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
	witnessCfg, _ := h.mgr.GetWitnessConfig()

	// Compute granular disabled reasons so the UI triage panel can show
	// the specific condition (e.g. "VERSION_MISMATCH") instead of a
	// generic "HA is broken" message.
	rawReasons := h.mgr.ClusterDisabledReasons()
	type reasonDetail struct {
		Code        ha.DisabledReason `json:"code"`
		Description string            `json:"description"`
	}
	details := make([]reasonDetail, 0, len(rawReasons))
	for _, r := range rawReasons {
		details = append(details, reasonDetail{
			Code:        r,
			Description: ha.DisabledReasonDescription(r),
		})
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"cluster":          status,
		"witness":          witnessCfg,
		"disabled_reasons": details,
	})
}

// BecomeStandby handles the graceful demotion path: export all ZFS pools
// within the deadline, then yield. If export times out, the node reboots
// itself to prevent split-brain. Called by:
//   - Keepalived notify scripts on BACKUP state transition
//   - Operator-initiated planned failover via the UI
//
// POST /api/ha/standby
func (h *HAHandler) BecomeStandby(w http.ResponseWriter, r *http.Request) {
	log.Printf("HA STANDBY: graceful demotion requested by %s", r.RemoteAddr)
	go func() {
		if err := ha.BecomeStandby(); err != nil {
			log.Printf("HA STANDBY: demotion error: %v", err)
		}
	}()
	// Respond immediately so the caller (Keepalived notify script) does not
	// block waiting. The actual export happens asynchronously; if it fails the
	// node will reboot before the new primary imports the pools.
	respondJSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"message": "Graceful standby transition initiated - node will export pools and yield within 4 seconds or reboot",
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
	respondJSON(w, http.StatusCreated, map[string]any{
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
	respondJSON(w, http.StatusOK, map[string]any{
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
	if !h.mgr.HandleHeartbeat(hb) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Reply with our own identity so peers can detect our role
	info := h.mgr.LocalInfo()
	respondJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"node_id":  info["id"],
		"address":  info["address"],
		"version":  info["version"],
	})
}

// GetClusterSecretConfig reports whether a cluster peer secret is active.
// The secret itself is never returned.
// GET /api/ha/cluster-secret/configure
func (h *HAHandler) GetClusterSecretConfig(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"configured": h.mgr.IsClusterSecretConfigured(),
	})
}

// SetClusterSecretConfig saves a new cluster peer secret to the database and
// applies it to the running manager without requiring a restart.
// POST /api/ha/cluster-secret/configure
// Body: { "secret": "..." }
func (h *HAHandler) SetClusterSecretConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if err := h.mgr.SaveClusterSecretToDB(req.Secret); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save cluster secret", err)
		return
	}
	if req.Secret == "" {
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Cluster secret cleared - peer authentication disabled"})
	} else {
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Cluster secret updated - takes effect immediately"})
	}
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
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Peer role updated to " + req.Role,
	})
}

// LocalNodeInfo returns this node's identity (no auth required - used by peers to auto-discover).
// GET /api/ha/local
func (h *HAHandler) LocalNodeInfo(w http.ResponseWriter, r *http.Request) {
	info := h.mgr.LocalInfo()
	respondJSON(w, http.StatusOK, map[string]any{
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
	respondJSON(w, http.StatusOK, map[string]any{
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
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Fencing configuration updated successfully",
	})
}

// GetReplicationConfig fetches continuous active-to-standby ZFS sync parameters.
// GET /api/ha/replication/configure
func (h *HAHandler) GetReplicationConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.mgr.GetReplicationConfig()
	respondJSON(w, http.StatusOK, map[string]any{
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
	
	if req.IntervalSecs < 10 {
		req.IntervalSecs = 30
	}
	if req.RemoteUser == "" {
		req.RemoteUser = "root"
	}
	if req.RemotePort == 0 {
		req.RemotePort = 22
	}

	if err := ha.ValidateReplicationConfig(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid replication configuration", err)
		return
	}

	if err := h.mgr.SetReplicationConfig(&req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save HA replication config", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

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
				j.Log(fmt.Sprintf("HA Promote: STONITH fencing failed - aborting to prevent split-brain: %v", err))
				j.Fail("Fencing failed: " + err.Error())
				return
			}
			j.Log("HA Promote: Leader node confirmed fenced (chassis dark). Proceeding with promotion.")
		} else if req.Leader != "" {
			j.Log("HA Promote: WARNING - fencing is not configured. Ensure the leader node is fully offline to avoid split-brain before continuing.")
		}

		// ── Step 2: Promotion orchestration ─────────────────────────────────
		j.Log(fmt.Sprintf("HA Promote: Promoting candidate %q (leader %q)...", req.Candidate, req.Leader))
		res := ha.ExecutePromotion(req.Candidate, req.Leader)
		for _, s := range res.Steps {
			switch {
			case s.Skipped:
				j.Log(fmt.Sprintf("HA Promote: %s: skipped (%s)", s.Name, s.Detail))
			case s.OK:
				j.Log(fmt.Sprintf("HA Promote: %s: ok", s.Name))
			default:
				j.Log(fmt.Sprintf("HA Promote: %s: FAILED: %s", s.Name, s.Detail))
			}
		}
		switch {
		case res.Failed:
			j.Fail("Promotion failed, this node does not serve the data: " + res.Err().Error())
		case res.Degraded:
			j.Fail("Pools imported, but some steps failed (fix them before relying on this node): " + res.Err().Error())
		default:
			j.Log("HA Promote: Promotion sequence complete.")
			j.Done(map[string]any{
				"candidate": req.Candidate,
				"leader":    req.Leader,
				"steps":     res.Steps,
			})
		}
	})

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Failover promotion initiated. Monitor progress via job " + jobID + ".",
		"job_id":  jobID,
	})
}

// Switchover performs a graceful Patroni primary handoff to the standby node.
// POST /api/ha/switchover
//
// This is the user-facing operation for "I want the standby to become primary".
// Unlike Promote (which performs STONITH fencing), Switchover asks the current
// primary to voluntarily demote itself so the standby can take over with zero
// data loss and no fencing required.
//
// Use cases:
//   - Before disabling HA on the primary node
//   - Rolling maintenance (disable-HA on primary, upgrade, re-enable)
//   - Testing failover behaviour without a real failure
func (h *HAHandler) Switchover(w http.ResponseWriter, r *http.Request) {
	if !isPatroniPrimary() {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   "This node is not the Patroni primary. Switchover can only be initiated from the primary.",
			"code":    "not_primary",
		})
		return
	}

	jobID := jobs.Start("ha_switchover", func(j *jobs.Job) {
		j.Log("Initiating graceful primary handoff to standby node...")
		j.Log("The standby will take over as PostgreSQL primary. Client connections will briefly pause during the transition (typically under 5 seconds).")

		if err := demotePatroni(); err != nil {
			j.Log(fmt.Sprintf("Switchover failed: %v", err))
			j.Fail("Primary handoff failed. The standby may not have been ready to take over. Check that the standby node is healthy and replication lag is low, then try again.")
			return
		}

		j.Log("Primary handoff complete. The standby node is now the active PostgreSQL primary.")
		j.Log("You can now safely disable HA on this node if desired.")
		j.Done(map[string]any{"switched": true})
	})

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Primary handoff initiated. Standby node is taking over as PostgreSQL primary.",
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

	respondJSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"message": "Fencing sequence initiated asynchronously for Node " + req.NodeID,
	})
}

// ToggleHA arms or disarms the NixOS HA cluster modules.
// POST /api/ha/toggle {"enable": true/false, "force": false}
//
// Safety checks before enable:
//   - No fencing method configured → warning (not blocked; operator may configure after)
//   - Fencing/STONITH in progress → blocked
//
// Safety checks before disable:
//   - Fencing/STONITH in progress → blocked; disabling mid-fence causes split-brain
//   - This node is the Patroni primary → blocked unless force:true; client connections
//     would be severed abruptly. Operator should switchover first.
//   - Failover in progress → blocked
//
// On failure of nixos-rebuild: the NixWriter JSON is reverted to the previous
// value so that ha_enabled in the UI reflects the actual applied state.
func (h *HAHandler) ToggleHA(w http.ResponseWriter, r *http.Request) {
	if NixWriter == nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   "High Availability requires NixOS. This system is not running a DPlaneOS NixOS appliance.",
		})
		return
	}

	var req struct {
		Enable bool `json:"enable"`
		Force  bool `json:"force"` // override primary-node block on disable
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// ── Safety guards ───────────────────────────────────────────────────────
	// Guard: block if a STONITH fencing sequence is in progress.
	// Disabling HA mid-fence would leave the peer fenced but unable to recover;
	// enabling mid-fence would corrupt the fencing state machine.
	if h.mgr.IsFencingInProgress() {
		respondJSON(w, http.StatusConflict, map[string]any{
			"success": false,
			"error":   "Cannot change HA state while a STONITH fencing sequence is in progress. Wait for it to complete or abort it first.",
			"code":    "fencing_in_progress",
		})
		return
	}

	var warnings []string

	if req.Enable {
		// Pre-flight for ENABLE: surface a structured block so the UI can render
		// a guided workflow, not just a warning toast.
		fencingCfg, _ := ha.GetFencingConfig(h.mgr.DB())
		pduCfg, _ := ha.GetPDUConfig(h.mgr.DB())
		if !fencingCfg.Enable && !pduCfg.Enable {
			// Return 200 with success:false so the frontend onSuccess handler
			// renders the guided panel (same pattern as patroni_primary).
			respondJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "No fencing method is configured.",
				"guide":   "Without IPMI or PDU fencing, automatic failover is disabled. If the peer becomes unreachable, this node will not promote itself - it cannot confirm the peer is offline and must stay passive to avoid split-brain. Configure at least one fencing method, then enable HA.",
				"code":    "no_fencing",
				"action":  "configure_fencing",
			})
			return
		}
	} else {
		// Pre-flight for DISABLE: check if this node is the Patroni primary.
		// Disabling HA while primary causes an abrupt loss of the PostgreSQL primary
		// without replication catchup or client draining. Operator should run
		// 'patronictl switchover' first to hand off leadership gracefully.
		if !req.Force && isPatroniPrimary() {
			// Return 200 OK so the frontend onSuccess handler receives the code
			// and can surface the guided switchover workflow.
			respondJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "This node is currently the active database primary.",
				"guide":   "Hand off the primary role to the standby node first using the 'Switch Primary to Standby' button below. This transfers active database connections gracefully with no data loss. After the handoff completes, disable HA.",
				"code":    "patroni_primary",
				"action":  "switchover",
			})
			return
		}
		if req.Force && isPatroniPrimary() {
			warnings = append(warnings, "Proceeding without graceful primary handoff. Active database connections will be dropped when Patroni stops. The standby node will promote itself after detecting the primary is gone (typically 15-45 seconds).")
		}
	}

	// ── Acquire gitops lock ─────────────────────────────────────────────────
	if !gitops.TryLock() {
		respondJSON(w, http.StatusLocked, map[string]any{
			"success": false,
			"error":   "A reconciliation is already in progress. Please wait for the current operation to finish.",
		})
		return
	}

	// Record the previous state so we can revert on rebuild failure.
	previousHAEnable := NixWriter.State().HAEnable

	if err := NixWriter.SetHA(req.Enable); err != nil {
		gitops.Unlock()
		respondError(w, http.StatusInternalServerError, "Failed to update NixOS state", err)
		return
	}

	action := "disabling"
	if req.Enable {
		action = "enabling"
	}

	jobID := jobs.Start("ha_toggle", func(j *jobs.Job) {
		defer gitops.Unlock()

		for _, w := range warnings {
			j.Log("WARNING: " + w)
		}

		if !req.Enable {
			// ── Disable sequence: graceful shutdown before nixos-rebuild ────────
			//
			// Step 1: Demote Patroni primary.
			// If this node is the PostgreSQL primary, initiate a graceful switchover
			// so the peer becomes primary before we stop services. Without this,
			// stopping Patroni here causes an abrupt primary loss - the peer cannot
			// promote until etcd quorum is re-established (may take 10-30s), during
			// which PostgreSQL is completely unavailable.
			if isPatroniPrimary() {
				j.Log("This node is the Patroni primary. Initiating graceful demotion...")
				if err := demotePatroni(); err != nil {
					if req.Force {
						j.Log(fmt.Sprintf("WARNING: graceful Patroni demotion failed (%v). Proceeding with force disable - active DB connections will be dropped.", err))
					} else {
						j.Fail(fmt.Sprintf("Primary handoff failed during HA disable: %v. Use the 'Switch Primary to Standby' action on the HA page to complete the handoff, then try disabling HA again. If the standby is offline, use Force Disable to proceed without a handoff.", err))
						if revertErr := NixWriter.SetHA(previousHAEnable); revertErr != nil {
							j.Log("WARNING: could not revert ha_enable flag: " + revertErr.Error())
						}
						return
					}
				} else {
					j.Log("Patroni primary demoted. Peer is now the PostgreSQL primary.")
				}
			} else {
				j.Log("This node is not the Patroni primary - no demotion needed.")
			}

			// Step 2: Leave the etcd cluster gracefully.
			// If this node leaves cleanly, the remaining two members (peer + witness)
			// maintain quorum and the cluster stays healthy. If we just stop etcd,
			// the cluster transitions to a degraded state and may lose quorum
			// temporarily while the member-leave timeout fires.
			j.Log("Leaving etcd cluster...")
			if err := leaveEtcdCluster(); err != nil {
				// Non-fatal: etcd will remove the member after its peer timeout.
				// Log it prominently but don't block the disable.
				j.Log(fmt.Sprintf("WARNING: etcd graceful leave failed (%v). etcd will remove this member after its election timeout (~5s). Proceeding.", err))
			} else {
				j.Log("This node removed from etcd cluster. Remaining members maintain quorum.")
			}

			// Note: ZFS pools are NOT exported here. When disabling HA, this node
			// continues as a standalone system and needs its pools. Pool export
			// happens during a failover (when yielding to a peer), not during
			// a planned HA disable.
			j.Log("ZFS pools remain imported. This node will continue serving data as a standalone system.")
		}

		// ── Apply NixOS configuration ────────────────────────────────────────
		j.Log(fmt.Sprintf("Applying NixOS configuration (%s HA)...", action))
		out, err := cmdutil.RunExtreme("nixos-rebuild", "switch")
		if err != nil {
			log.Printf("HA TOGGLE: NixOS rebuild failed: %v\nOutput: %s", err, string(out))
			j.Log(fmt.Sprintf("ERROR: NixOS reconfiguration failed: %v", err))

			// Revert the JSON flag so ha_enabled in the UI reflects reality.
			// nixos-rebuild rolls back to the previous generation automatically on
			// failure, so the running system is still the old config.
			if revertErr := NixWriter.SetHA(previousHAEnable); revertErr != nil {
				j.Log(fmt.Sprintf("WARNING: could not revert ha_enable flag: %v - manual correction may be needed", revertErr))
			} else {
				j.Log(fmt.Sprintf("ha_enable reverted to %v to match the active NixOS generation.", previousHAEnable))
			}

			j.Fail(err.Error())
			DispatchAlert("critical", "HA_REBUILD_FAILED", "system", fmt.Sprintf("HA %s failed: %v", action, err))
			return
		}

		log.Printf("HA TOGGLE: NixOS rebuild succeeded. HA is now %v", req.Enable)
		j.Log(fmt.Sprintf("NixOS reconfiguration completed. HA is now %s.", map[bool]string{true: "ENABLED", false: "DISABLED"}[req.Enable]))
		j.Done(map[string]any{"ha_enabled": req.Enable})
	})

	resp := map[string]any{
		"success": true,
		"message": fmt.Sprintf("HA %s started. System reconfiguration in progress.", action),
		"job_id":  jobID,
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	respondJSON(w, http.StatusOK, resp)
}

// isPatroniPrimary returns true if the local Patroni instance is the cluster primary.
// Uses the Patroni REST API (localhost:8008/primary) which returns 200 if primary,
// 503 otherwise. A failed request is treated as not-primary (Patroni not running).
func isPatroniPrimary() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:8008/primary", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// demotePatroni initiates a graceful Patroni switchover and waits for this node
// to no longer be primary (max 20 seconds). The peer is promoted as the new
// PostgreSQL primary before HA services are stopped.
//
// Patroni REST API:
//
//	POST /demote  - triggers a graceful demotion (Patroni coordinates with replica)
//	GET  /primary - returns 200 if this node is primary, 503 if not
func demotePatroni() error {
	hc := &http.Client{Timeout: 5 * time.Second}

	// Initiate demotion
	resp, err := hc.Post("http://localhost:8008/demote", "application/json", strings.NewReader(`{}`))
	if err != nil {
		return fmt.Errorf("POST /demote: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("POST /demote returned %d", resp.StatusCode)
	}

	// Poll until this node is no longer primary (max 20s)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		checkReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:8008/primary", nil)
		cancel()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		checkResp, err := hc.Do(checkReq)
		if err != nil || checkResp.StatusCode == http.StatusServiceUnavailable {
			if checkResp != nil {
				io.Copy(io.Discard, checkResp.Body)
				checkResp.Body.Close()
			}
			return nil // No longer primary - demotion succeeded
		}
		io.Copy(io.Discard, checkResp.Body)
		checkResp.Body.Close()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout: node still reports as Patroni primary after 20s demotion attempt")
}

// leaveEtcdCluster removes this node from the etcd cluster using the etcd v3 HTTP API.
// This allows the remaining members (peer + witness) to maintain clean quorum state
// without waiting for the election timeout to remove the departed member.
//
// etcd v3 gRPC-gateway endpoints:
//
//	POST /v3/cluster/member/list   - returns cluster membership
//	POST /v3/cluster/member/remove - removes a member by ID
func leaveEtcdCluster() error {
	hc := &http.Client{Timeout: 5 * time.Second}
	const etcdEndpoint = "http://localhost:2379"

	// Step 1: Get the member list to find this node's member ID
	listResp, err := hc.Post(etcdEndpoint+"/v3/cluster/member/list",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("cannot reach etcd: %w", err)
	}
	defer listResp.Body.Close()

	var memberList struct {
		Members []struct {
			ID       string   `json:"ID"`       // uint64 as string in etcd v3 API
			Name     string   `json:"name"`
			PeerURLs []string `json:"peerURLs"`
		} `json:"members"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&memberList); err != nil {
		return fmt.Errorf("decode member list: %w", err)
	}

	hostname, _ := os.Hostname()

	// Find this node's member ID by hostname match
	var myMemberID string
	for _, m := range memberList.Members {
		if m.Name == hostname {
			myMemberID = m.ID
			break
		}
	}
	if myMemberID == "" {
		// Try matching by peer URL containing localhost or the node's address
		for _, m := range memberList.Members {
			for _, purl := range m.PeerURLs {
				if strings.Contains(purl, "localhost") || strings.Contains(purl, "127.0.0.1") {
					myMemberID = m.ID
					break
				}
			}
			if myMemberID != "" {
				break
			}
		}
	}
	if myMemberID == "" {
		return fmt.Errorf("this node (%s) not found in etcd cluster (%d members)", hostname, len(memberList.Members))
	}

	// Step 2: Remove this member.
	// etcd v3 gRPC-gateway encodes uint64 IDs as JSON strings to avoid
	// JavaScript precision loss. The remove request must send the ID as a
	// string, not a number: {"ID":"12345678901234567890"}.
	removeBody, _ := json.Marshal(map[string]string{"ID": myMemberID})
	removeResp, err := hc.Post(etcdEndpoint+"/v3/cluster/member/remove",
		"application/json", bytes.NewReader(removeBody))
	if err != nil {
		return fmt.Errorf("remove member %s: %w", myMemberID, err)
	}
	io.Copy(io.Discard, removeResp.Body)
	removeResp.Body.Close()

	if removeResp.StatusCode != http.StatusOK {
		return fmt.Errorf("member remove returned HTTP %d", removeResp.StatusCode)
	}
	return nil
}

// GetWitnessConfig returns the current quorum witness configuration.
// GET /api/ha/witness/configure
func (h *HAHandler) GetWitnessConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.mgr.GetWitnessConfig()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read witness config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"config":  cfg,
	})
}

// ConfigureWitness saves a new quorum witness configuration.
// POST /api/ha/witness/configure
func (h *HAHandler) ConfigureWitness(w http.ResponseWriter, r *http.Request) {
	var req ha.WitnessConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.Enable && len(req.Witnesses) == 0 {
		respondErrorSimple(w, "at least one witness entry is required when witness is enabled", http.StatusBadRequest)
		return
	}
	if err := h.mgr.SaveWitnessConfig(req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save witness config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Quorum witness configuration saved",
	})
}

// TestWitness probes all configured witnesses and returns per-entry results.
// An optional JSON body of type WitnessConfig may override the stored config for ad-hoc testing.
// POST /api/ha/witness/test
func (h *HAHandler) TestWitness(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.mgr.GetWitnessConfig()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read witness config", err)
		return
	}

	// Allow caller to supply an ad-hoc config (e.g. for testing before saving).
	var req ha.WitnessConfig
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr == nil {
		if len(req.Witnesses) > 0 {
			cfg.Witnesses = req.Witnesses
		}
		if req.TimeoutSecs > 0 {
			cfg.TimeoutSecs = req.TimeoutSecs
		}
		if req.RequiredHealthy > 0 {
			cfg.RequiredHealthy = req.RequiredHealthy
		}
	}

	if len(cfg.Witnesses) == 0 {
		respondErrorSimple(w, "No witness entries configured", http.StatusBadRequest)
		return
	}

	timeout := cfg.TimeoutSecs
	if timeout <= 0 {
		timeout = 5
	}
	timeoutDur := time.Duration(timeout) * time.Second

	type probeResult struct {
		URL       string `json:"url"`
		Reachable bool   `json:"reachable"`
	}
	results := make([]probeResult, len(cfg.Witnesses))
	var wg sync.WaitGroup
	for i, entry := range cfg.Witnesses {
		wg.Add(1)
		go func(idx int, e ha.WitnessEntry) {
			defer wg.Done()
			results[idx] = probeResult{
				URL:       e.URL,
				Reachable: ha.ProbeWitnessEntry(e, timeoutDur),
			}
		}(i, entry)
	}
	wg.Wait()

	healthy := 0
	for _, r := range results {
		if r.Reachable {
			healthy++
		}
	}
	required := cfg.RequiredHealthy
	if required <= 0 {
		required = 1
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"quorum_satisfied": healthy >= required,
		"healthy":          healthy,
		"required":         required,
		"results":          results,
	})
}

// GetPDUConfig fetches the PDU outlet-fencing configuration.
// GET /api/ha/pdu/configure
func (h *HAHandler) GetPDUConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.mgr.GetPDUConfig()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read PDU config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"config":  cfg,
	})
}

// ConfigurePDU saves the PDU outlet-fencing configuration.
// POST /api/ha/pdu/configure
func (h *HAHandler) ConfigurePDU(w http.ResponseWriter, r *http.Request) {
	var req ha.PDUConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.Enable && req.OutletOffURL == "" {
		respondErrorSimple(w, "outlet_off_url is required when PDU fencing is enabled", http.StatusBadRequest)
		return
	}
	if err := h.mgr.SavePDUConfig(req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save PDU config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "PDU fencing configuration saved",
	})
}

// GetSyncStatus returns this node's ZFS pool TXG state for peer startup reconciliation.
// This endpoint is deliberately public - peer daemons call it at boot time before
// they have authenticated sessions.
// GET /api/ha/sync/status
func (h *HAHandler) GetSyncStatus(w http.ResponseWriter, r *http.Request) {
	status := h.mgr.GetSyncStatus()
	respondJSON(w, http.StatusOK, status)
}

// ClearFault resets the hysteresis timer and subordinate mode, re-enabling auto-failover.
// Call this after investigating and resolving the root cause of a failover or zombie boot.
// POST /api/ha/clear_fault
func (h *HAHandler) ClearFault(w http.ResponseWriter, r *http.Request) {
	h.mgr.ClearFault()
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Fault cleared. Hysteresis and Subordinate Mode reset. Auto-failover re-enabled.",
	})
}

// GetSBDConfig returns the SBD ZFS lease fencing configuration.
// GET /api/ha/sbd/configure
func (h *HAHandler) GetSBDConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.mgr.GetSBDConfig()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read SBD config", err)
		return
	}
	var lastRenewalUnix int64
	lastOK := ha.GlobalSBD.LastOK()
	if !lastOK.IsZero() {
		lastRenewalUnix = lastOK.Unix()
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"config":            cfg,
		"lease_active":      ha.GlobalSBD.IsLive(),
		"last_renewal_unix": lastRenewalUnix,
	})
}

// ConfigureSBD saves the SBD ZFS lease fencing configuration and restarts the lease manager.
// POST /api/ha/sbd/configure
func (h *HAHandler) ConfigureSBD(w http.ResponseWriter, r *http.Request) {
	var req ha.SBDConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.Pool != "" && req.Dataset == "" {
		respondErrorSimple(w, "dataset is required when pool is set", http.StatusBadRequest)
		return
	}
	if req.Pool != "" {
		if err := security.ValidatePoolName(req.Pool); err != nil {
			respondError(w, http.StatusBadRequest, "Invalid pool name", err)
			return
		}
		if err := security.ValidateDatasetName(req.Pool + "/" + req.Dataset); err != nil {
			respondError(w, http.StatusBadRequest, "Invalid dataset name", err)
			return
		}
	}
	if err := h.mgr.SaveSBDConfig(req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save SBD config", err)
		return
	}
	// Restart lease manager with the new config (no-op if pool is now empty).
	ha.GlobalSBD.Restart(req)
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "SBD fencing configuration saved",
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

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("Maintenance mode %s. Fencing suspended for %d seconds.", status, req.Seconds),
	})
}

// GetClusterTiming returns the current HA timing configuration.
// GET /api/ha/timing
func (h *HAHandler) GetClusterTiming(w http.ResponseWriter, r *http.Request) {
	cfg := ha.GetClusterTimingConfig(h.mgr.DB())
	respondJSON(w, http.StatusOK, map[string]any{
		"success":                    true,
		"failover_after_seconds":     int(cfg.FailoverAfter.Seconds()),
		"hysteresis_window_minutes":  int(cfg.HysteresisWindow.Minutes()),
		"heartbeat_interval_seconds": int(cfg.HeartbeatInterval.Seconds()),
		"note": "Changes to failover_after_seconds and heartbeat_interval_seconds take effect after daemon restart. hysteresis_window_minutes takes effect immediately.",
	})
}

// SaveClusterTiming updates HA timing parameters.
// POST /api/ha/timing
// Body: { "failover_after_seconds": 45, "hysteresis_window_minutes": 60, "heartbeat_interval_seconds": 15 }
func (h *HAHandler) SaveClusterTiming(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FailoverAfterSeconds    int `json:"failover_after_seconds"`
		HysteresisWindowMinutes int `json:"hysteresis_window_minutes"`
		HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg := ha.ClusterTimingConfig{
		FailoverAfter:     time.Duration(req.FailoverAfterSeconds) * time.Second,
		HysteresisWindow:  time.Duration(req.HysteresisWindowMinutes) * time.Minute,
		HeartbeatInterval: time.Duration(req.HeartbeatIntervalSeconds) * time.Second,
	}
	if cfg.FailoverAfter == 0 {
		cfg.FailoverAfter = ha.DefaultFailoverAfter
	}
	if cfg.HysteresisWindow == 0 {
		cfg.HysteresisWindow = ha.DefaultHysteresisWindow
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = ha.DefaultHeartbeatInterval
	}
	if err := ha.SaveClusterTimingConfig(h.mgr.DB(), cfg); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Timing configuration saved. Restart the daemon for heartbeat_interval_seconds and failover_after_seconds to take effect.",
	})
}

// DetectHAHardware performs a quick (non-destructive) hardware capability scan and
// returns a provisional HA path recommendation. It checks watchdog availability and
// queries dplane-fenced for current reservation status but does NOT run the PROUT
// write round-trip - that is the operator's explicit action via POST /api/ha/scsi/probe.
//
// Path A' (shared_storage): SCSI-3 PR capable disks are in use - hardware arbitrates writes.
// Path B (replicated):      No PR hardware - ZFS replication + watchdog self-fence.
//
// GET /api/ha/hardware/detect
func (h *HAHandler) DetectHAHardware(w http.ResponseWriter, r *http.Request) {
	// 1. Watchdog availability - check common device paths.
	watchdogAvail := false
	watchdogDevice := ""
	for _, dev := range []string{"/dev/watchdog", "/dev/watchdog0"} {
		if _, err := os.Stat(dev); err == nil {
			watchdogAvail = true
			watchdogDevice = dev
			break
		}
	}

	// 2. dplane-fenced status (quick socket call, no disk I/O).
	fencedRunning := false
	fencedDevices := []string{}
	if status, err := ha.FencedStatus(); err == nil {
		fencedRunning = true
		if devs, ok := status["devices"].([]any); ok {
			for _, d := range devs {
				if s, ok2 := d.(string); ok2 {
					fencedDevices = append(fencedDevices, s)
				}
			}
		}
	}

	// 3. Count SG devices from pool (no probe write - just enumeration).
	poolSGDevices, _ := enumPoolSGDevices()

	// 4. Determine provisional recommendation.
	// "shared_storage" if fenced is running with active reservations, or if we
	// can see SG devices that could support PR. Confirmed only after the probe.
	provPath := "replicated"
	provLabel := "Path B: Replicated ZFS (Watchdog + Replication)"
	provReason := "No SAS/SCSI pool disks found. Use ZFS replication with hardware watchdog self-fence and a quorum witness."
	probeRequired := false

	if len(poolSGDevices) > 0 || fencedRunning {
		provPath = "shared_storage"
		provLabel = "Path A': Shared Storage (SCSI-3 PR + Watchdog)"
		probeRequired = true
		if fencedRunning && len(fencedDevices) > 0 {
			provReason = fmt.Sprintf("dplane-fenced holds reservations on %d device(s). Run the PROUT probe to confirm these devices reject writes from a second node.", len(fencedDevices))
		} else {
			provReason = fmt.Sprintf("%d SAS/SCSI pool disk(s) found. Run the PROUT probe to confirm SCSI-3 PR write support before enabling shared-storage HA.", len(poolSGDevices))
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":               true,
		"watchdog_available":    watchdogAvail,
		"watchdog_device":       watchdogDevice,
		"fenced_running":        fencedRunning,
		"fenced_devices":        fencedDevices,
		"pool_sg_devices":       poolSGDevices,
		"provisional_path":      provPath,
		"provisional_path_label": provLabel,
		"provisional_reason":    provReason,
		"probe_required":        probeRequired,
	})
}

// GetSCSIStatus returns the current SCSI-3 PR reservation state from dplane-fenced.
// The response includes which /dev/sgN devices are currently reserved, the
// reservation key in use, and whether the fenced daemon is reachable.
// GET /api/ha/scsi/status
func (h *HAHandler) GetSCSIStatus(w http.ResponseWriter, r *http.Request) {
	status, err := ha.FencedStatus()
	if err != nil {
		// dplane-fenced not running or socket unavailable - not an error, just not configured
		respondJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"running": false,
			"message": "dplane-fenced not reachable: " + err.Error(),
			"devices": []string{},
		})
		return
	}
	devices, _ := status["devices"].([]any)
	devStrs := make([]string, 0, len(devices))
	for _, d := range devices {
		if s, ok := d.(string); ok {
			devStrs = append(devStrs, s)
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"running": true,
		"key":     status["key"],
		"devices": devStrs,
	})
}

// ProbeSCSIDevices runs the full PROUT round-trip probe (SupportsReservations)
// on the specified devices. If no devices are provided, the endpoint auto-
// enumerates /dev/sgN devices from current ZFS pool members.
//
// This is the correct PR capability check: it tests whether a drive will
// actually accept a PERSISTENT RESERVE OUT REGISTER command, not just
// whether it answers a PRIN READ KEYS query. Drives that pass the read-only
// probe but reject writes produce false positives that arm the cluster with
// broken fencing - this endpoint detects those before setup.
//
// POST /api/ha/scsi/probe
// Body: { "devices": ["/dev/sg0"] }  // optional; empty = auto-enumerate pool disks
func (h *HAHandler) ProbeSCSIDevices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Devices []string `json:"devices"`
	}
	// Decode is best-effort; empty body is valid (triggers auto-enumerate)
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck

	devices := req.Devices
	autoEnumerated := false
	if len(devices) == 0 {
		autoEnumerated = true
		var err error
		devices, err = enumPoolSGDevices()
		if err != nil {
			respondErrorSimple(w, "failed to enumerate pool disks: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if len(devices) == 0 {
			respondJSON(w, http.StatusOK, map[string]any{
				"success":        true,
				"auto_enumerated": true,
				"results":        []any{},
				"all_supported":  true,
				"message":        "No ZFS pool member disks found - pool may not be imported or no SAS/SATA disks present",
			})
			return
		}
	}

	type probeResult struct {
		Device    string `json:"device"`
		Supported bool   `json:"supported"`
		Error     string `json:"error,omitempty"`
	}

	results := make([]probeResult, len(devices))
	allSupported := true
	for i, dev := range devices {
		if err := scsipr.SupportsReservations(dev); err != nil {
			results[i] = probeResult{Device: dev, Supported: false, Error: err.Error()}
			allSupported = false
		} else {
			results[i] = probeResult{Device: dev, Supported: true}
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"auto_enumerated": autoEnumerated,
		"results":         results,
		"all_supported":   allSupported,
		"device_count":    len(devices),
	})
}

// enumPoolSGDevices returns /dev/sgN paths for all current ZFS pool member disks.
// Mirrors the enumeration logic in dplane-fenced so setup probes and runtime
// fencing operate on the same device set.
func enumPoolSGDevices() ([]string, error) {
	out, err := exec.Command("zpool", "status", "-P").Output()
	if err != nil {
		return nil, fmt.Errorf("zpool status: %w", err)
	}

	var sgDevs []string
	seen := make(map[string]bool)

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "/dev/") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		blockDev := fields[0]

		sg, err := blockDevToSG(blockDev)
		if err != nil {
			continue
		}
		if seen[sg] {
			continue
		}
		seen[sg] = true
		sgDevs = append(sgDevs, sg)
	}
	return sgDevs, nil
}

// blockDevToSG resolves a block device path to its /dev/sgN counterpart via sysfs.
func blockDevToSG(blockDev string) (string, error) {
	resolved, err := filepath.EvalSymlinks(blockDev)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", blockDev, err)
	}
	base := filepath.Base(resolved)
	if strings.HasPrefix(base, "nvme") {
		return "", fmt.Errorf("NVMe devices do not use SG_IO (%s)", base)
	}
	genericLink := fmt.Sprintf("/sys/class/block/%s/device/generic", base)
	sgTarget, err := filepath.EvalSymlinks(genericLink)
	if err != nil {
		return "", fmt.Errorf("no scsi_generic for %s: %w", base, err)
	}
	return "/dev/" + filepath.Base(sgTarget), nil
}

// GetWatchdogConfig returns the hardware watchdog self-fence configuration.
// GET /api/ha/watchdog/configure
func (h *HAHandler) GetWatchdogConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.mgr.GetWatchdogConfig()
	if err != nil {
		// Row may not exist yet; return safe defaults.
		cfg = ha.WatchdogConfig{
			Enable:         false,
			Device:         "/dev/watchdog",
			TimeoutSecs:    30,
			PetIntervalSec: 10,
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"config":  cfg,
	})
}

// SaveWatchdogConfig saves the hardware watchdog self-fence configuration and
// starts or stops the watchdog device according to the new enable flag.
// POST /api/ha/watchdog/configure
func (h *HAHandler) SaveWatchdogConfig(w http.ResponseWriter, r *http.Request) {
	var req ha.WatchdogConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.Device == "" {
		req.Device = "/dev/watchdog"
	}
	if req.TimeoutSecs <= 0 {
		req.TimeoutSecs = 30
	}
	if req.PetIntervalSec <= 0 {
		req.PetIntervalSec = 10
	}
	if req.Enable && req.TimeoutSecs < 10 {
		respondErrorSimple(w, "timeout_secs must be >= 10 when watchdog is enabled", http.StatusBadRequest)
		return
	}
	if req.Enable && req.PetIntervalSec*2 > req.TimeoutSecs {
		// Pet interval must be <= timeout/2. The kernel needs to fire the watchdog
		// after the timeout, and the daemon needs at least two pet intervals before
		// that to confirm it is still alive. pet_interval*2 > timeout means a single
		// missed pet could expire the watchdog without the daemon getting a second chance.
		respondErrorSimple(w, "pet_interval_sec must be <= timeout_secs/2 (allows two missed pets before reset)", http.StatusBadRequest)
		return
	}
	if err := h.mgr.SaveWatchdogConfig(req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save watchdog config", err)
		return
	}
	if req.Enable {
		h.mgr.StartWatchdog(req)
	} else {
		h.mgr.StopWatchdog()
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Watchdog self-fence configuration saved",
	})
}

// ALUAStandby sets all ALUA-enabled iSCSI targets to Standby access state.
// This must be called BEFORE POST /api/ha/standby during a planned failover so
// that initiators see a clean path-state transition (Optimized -> Standby) rather
// than an abrupt path loss when the VIP moves.
//
// The sequence enforced by the Keepalived notify_backup script is:
//   1. POST /api/ha/alua-standby  - mark all iSCSI paths Standby, wait for initiators
//   2. POST /api/ha/standby       - export ZFS pools and yield the VIP
//
// POST /api/ha/alua-standby
func (h *HAHandler) ALUAStandby(w http.ResponseWriter, r *http.Request) {
	// Enumerate all iSCSI targets via targetcli.
	out, err := executeCommandWithTimeout(TimeoutFast, "targetcli", []string{"/iscsi", "ls"})
	if err != nil {
		// targetcli unavailable - not fatal; log and return success so the
		// failover sequence continues. Pool export is the hard safety boundary.
		log.Printf("HA ALUA-STANDBY: targetcli unavailable (%v); skipping ALUA transition", err)
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "skipped": true, "reason": "targetcli unavailable"})
		return
	}

	var set, failed []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "iqn.") {
			continue
		}
		iqn := strings.Fields(line)[0]
		tpgPath := fmt.Sprintf("/iscsi/%s/tpg1", iqn)
		// Try to set ALUA Standby. Non-fatal: targets without ALUA support will
		// return an error from targetcli which we log and skip.
		if _, err := executeCommandWithTimeout(TimeoutFast, "targetcli",
			[]string{tpgPath, "set", "attribute", "alua_support=1"}); err != nil {
			// ALUA not enabled on this target; skip silently.
			continue
		}
		aluaPath := fmt.Sprintf("%s/alua/default_tg_pt_gp", tpgPath)
		if _, err := executeCommandWithTimeout(TimeoutFast, "targetcli",
			[]string{aluaPath, "set", fmt.Sprintf("alua_access_state=%d", ALUAStandby)}); err != nil {
			log.Printf("HA ALUA-STANDBY: failed to set Standby on %s: %v", iqn, err)
			failed = append(failed, iqn)
		} else {
			set = append(set, iqn)
		}
	}

	// Save targetcli config after bulk state change.
	executeCommandWithTimeout(TimeoutFast, "targetcli", []string{"/", "saveconfig"}) //nolint:errcheck

	log.Printf("HA ALUA-STANDBY: set Standby on %d target(s), %d failed", len(set), len(failed))
	respondJSON(w, http.StatusOK, map[string]any{
		"success":  len(failed) == 0,
		"set":      set,
		"failed":   failed,
	})
}

