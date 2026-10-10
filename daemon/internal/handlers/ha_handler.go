package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dplaned/internal/ha"
	"dplaned/internal/scsipr"
)

// HAHandler serves the node-level protection settings used by storage groups:
// watchdog, power fencing (IPMI/Redfish, PDU), and hardware detection.
type HAHandler struct {
	db     *sql.DB
	keeper *ha.Keeper
}

// NewHAHandler creates the handler.
func NewHAHandler(db *sql.DB, keeper *ha.Keeper) *HAHandler {
	return &HAHandler{db: db, keeper: keeper}
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
	cfg, err := ha.GetFencingConfig(h.db)
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
	if err := ha.SaveFencingConfig(h.db, req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save fencing config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Fencing configuration updated successfully",
	})
}

// GetPDUConfig fetches the PDU outlet-fencing configuration.
// GET /api/ha/pdu/configure
func (h *HAHandler) GetPDUConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := ha.GetPDUConfig(h.db)
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
	if err := ha.SavePDUConfig(h.db, req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save PDU config", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "PDU fencing configuration saved",
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
	cfg, err := ha.GetWatchdogConfig(h.db)
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
	if err := ha.SaveWatchdogConfig(h.db, req); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save watchdog config", err)
		return
	}
	h.keeper.Restart()
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Watchdog self-fence configuration saved",
	})
}
