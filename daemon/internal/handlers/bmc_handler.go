// bmc_handler.go - HTTP API for out-of-band hardware management
//
// Endpoints:
//   POST /api/bmc/enroll        - TOFU: capture and store BMC TLS certificate fingerprint
//   GET  /api/bmc/info          - BMC type, firmware version, vendor
//   GET  /api/bmc/health        - temperatures, fans, power, voltages
//   GET  /api/bmc/events        - system event log (last N entries)
//   GET  /api/bmc/power         - current chassis power state
//   POST /api/bmc/power         - power management (on/off/reset/graceful)
//   POST /api/bmc/reset-cert    - clear pinned TLS fingerprint for re-enrollment (AAL2)
//
// All endpoints read BMC credentials from ha_fencing_config (same table as
// STONITH fencing). Redfish is used when available (iLO 5+, iDRAC 9+);
// ipmitool is the fallback for older hardware.

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/bmc"
	"dplaned/internal/ha"
)

// BMCHandler provides HTTP API access to out-of-band hardware management.
type BMCHandler struct {
	db *sql.DB
}

func NewBMCHandler(db *sql.DB) *BMCHandler {
	return &BMCHandler{db: db}
}

// GetConfig returns the settings of this system's own BMC (never the
// password).
// GET /api/bmc/config
func (h *BMCHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	t, _, err := ha.GetPowerTarget(h.db, bmc.Self)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read the BMC settings", err)
		return
	}
	respondOK(w, map[string]any{"success": true, "config": map[string]any{
		"host": t.Host, "username": t.Username, "has_password": t.HasPassword,
		"tls_fingerprint": t.Fingerprint, "tls_pinned_at": t.PinnedAt,
	}})
}

// SaveConfig stores the address and sign-in of this system's own BMC. The
// password is kept when the field is left out.
// PUT /api/bmc/config {"host": "...", "username": "...", "password": "..."}
func (h *BMCHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string  `json:"host"`
		Username string  `json:"username"`
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Host) == "" {
		if _, err := h.db.Exec(`DELETE FROM power_targets WHERE node = ''`); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to remove the BMC settings", err)
			return
		}
		respondOK(w, map[string]any{"success": true, "message": "The BMC settings are removed"})
		return
	}
	t := ha.PowerTarget{Node: bmc.Self, Kind: "bmc", Host: strings.TrimSpace(req.Host), Username: strings.TrimSpace(req.Username)}
	if err := ha.SavePowerTarget(h.db, t, req.Password); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondOK(w, map[string]any{"success": true, "message": "The BMC settings are saved"})
}

// Enroll performs the initial TOFU connection: probes the BMC, captures its
// TLS certificate fingerprint, and stores it. Must be called once before any
// other BMC endpoint will work over Redfish.
//
// POST /api/bmc/enroll
func (h *BMCHandler) Enroll(w http.ResponseWriter, r *http.Request) {
	creds, _, err := bmc.LoadFromDB(h.db)
	if err != nil {
		respondGuided(w, http.StatusPreconditionFailed, guidedError{
			Error:  "BMC not configured: " + err.Error(),
			Guide:  "Enter the address, user name and password of this system's BMC under Hardware, IPMI, then retry.",
			Code:   "bmc_not_configured",
			Action: "configure_bmc",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pin, info, err := bmc.EnrollCertificate(ctx, creds)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]any{
			"success": false,
			"error":   err.Error(),
			"guide":   "Check that the BMC IP is reachable, the credentials are correct, and the management network is accessible from this NAS.",
		})
		return
	}

	if err := bmc.SaveTLSFingerprint(h.db, bmc.Self, pin); err != nil {
		respondErrorSimple(w, "Failed to store certificate fingerprint: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"fingerprint": pin.Fingerprint,
		"pinned_at":   pin.PinnedAt,
		"bmc_info":    info,
		"message":     "BMC certificate enrolled. Future connections will verify this fingerprint. If the BMC firmware is updated (which may regenerate the cert), use POST /api/bmc/reset-cert to re-enroll.",
	})
}

// Info returns the detected BMC type, vendor, model, and firmware version.
// GET /api/bmc/info
func (h *BMCHandler) Info(w http.ResponseWriter, r *http.Request) {
	creds, pin, err := bmc.LoadFromDB(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusPreconditionFailed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	info, err := bmc.Probe(ctx, creds, pin)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"success":  false,
			"protocol": "none",
			"error":    err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"info":    info,
	})
}

// Health returns hardware health: temperatures, fans, power consumption, voltages.
// GET /api/bmc/health
func (h *BMCHandler) Health(w http.ResponseWriter, r *http.Request) {
	creds, pin, err := bmc.LoadFromDB(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusPreconditionFailed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	health, err := bmc.GetHealth(ctx, creds, pin)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"health":  health,
	})
}

// Events returns recent BMC system event log entries.
// GET /api/bmc/events?limit=50
func (h *BMCHandler) Events(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	creds, pin, err := bmc.LoadFromDB(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusPreconditionFailed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	events, err := bmc.GetEvents(ctx, creds, pin, limit)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"events":  events,
		"count":   len(events),
	})
}

// Power returns the current chassis power state or sends a power command.
// GET  /api/bmc/power          → current state
// POST /api/bmc/power {"action": "off|graceful_off|on|reset|graceful_reset"}
func (h *BMCHandler) Power(w http.ResponseWriter, r *http.Request) {
	creds, pin, err := bmc.LoadFromDB(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusPreconditionFailed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		state, err := bmc.GetPowerState(ctx, creds, pin)
		if err != nil {
			respondJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": err.Error()})
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "power_state": string(state)})
		return
	}

	// POST - send power action
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Action == "" {
		respondErrorSimple(w, "action required: off, graceful_off, on, reset, graceful_reset", http.StatusBadRequest)
		return
	}

	if err := bmc.SendPowerAction(ctx, creds, pin, bmc.PowerAction(req.Action)); err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]any{
			"success": false,
			"error":   err.Error(),
			"guide":   "Check BMC connectivity and that the account has power management privileges.",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"action":  req.Action,
		"message": "Power command sent to BMC.",
	})
}

// ResetCert clears the stored TLS fingerprint so the next /enroll call
// re-captures it. Requires AAL2 (applied at route registration).
// POST /api/bmc/reset-cert
func (h *BMCHandler) ResetCert(w http.ResponseWriter, r *http.Request) {
	if err := bmc.ClearTLSFingerprint(h.db, bmc.Self); err != nil {
		respondErrorSimple(w, "Failed to clear certificate: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "BMC certificate fingerprint cleared. Call POST /api/bmc/enroll to re-enroll the new certificate.",
	})
}
