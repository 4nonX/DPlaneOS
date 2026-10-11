package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// DetectHAHardware reports what this node has for protecting storage
// groups, without touching the disks: a watchdog device, and SCSI disks in
// its pools (the only kind that can have persistent reservations; whether
// they do is what POST /api/ha/scsi/probe finds out).
//
// GET /api/ha/hardware/detect
func (h *HAHandler) DetectHAHardware(w http.ResponseWriter, r *http.Request) {
	watchdogAvail := false
	watchdogDevice := ""
	for _, dev := range []string{"/dev/watchdog", "/dev/watchdog0"} {
		if _, err := os.Stat(dev); err == nil {
			watchdogAvail = true
			watchdogDevice = dev
			break
		}
	}
	poolSGDevices, _ := enumPoolSGDevices()

	provPath := "replicated"
	provLabel := "Replicated pools (watchdog and third vote)"
	provReason := "No SAS/SCSI pool disks found. Use replication between the nodes, with the watchdog and a third vote."
	probeRequired := false
	if len(poolSGDevices) > 0 {
		provPath = "shared_storage"
		provLabel = "Shared disks (disk reservations and watchdog)"
		probeRequired = true
		provReason = fmt.Sprintf("%d SAS/SCSI pool disk(s) found. If both nodes are connected to these disks, a shared storage group can use them; run the reservation probe to see whether the disks can also shut a failed node out by themselves.", len(poolSGDevices))
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":                true,
		"watchdog_available":     watchdogAvail,
		"watchdog_device":        watchdogDevice,
		"pool_sg_devices":        poolSGDevices,
		"provisional_path":       provPath,
		"provisional_path_label": provLabel,
		"provisional_reason":     provReason,
		"probe_required":         probeRequired,
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
				"success":         true,
				"auto_enumerated": true,
				"results":         []any{},
				"all_supported":   false,
				"device_count":    0,
				"message":         "No SCSI disks in the pools imported on this node: nothing to probe. NVMe and virtual disks have no persistent reservations of this kind.",
			})
			return
		}
	}
	for _, dev := range devices {
		if !probeDeviceRe.MatchString(dev) {
			respondErrorSimple(w, "not a SCSI disk device: "+dev, http.StatusBadRequest)
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

// probeDeviceRe: what the probe may be pointed at.
var probeDeviceRe = regexp.MustCompile(`^/dev/(sd[a-z]+|sg[0-9]+)$`)

// poolMemberDevices parses the device paths out of `zpool status -P`.
func poolMemberDevices(status string) []string {
	var devs []string
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "/dev/") {
			devs = append(devs, fields[0])
		}
	}
	return devs
}

// wholeDisk returns the disk a device node belongs to (itself, or the disk
// of a partition: ZFS puts its data on partition 1 of a whole disk), and
// whether that disk takes SCSI commands. sysBlock is /sys/class/block.
func wholeDisk(sysBlock, dev string) (disk string, scsi bool, err error) {
	resolved, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return "", false, err
	}
	name := filepath.Base(resolved)
	entry, err := filepath.EvalSymlinks(filepath.Join(sysBlock, name))
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(filepath.Join(entry, "partition")); err == nil {
		entry = filepath.Dir(entry)
		name = filepath.Base(entry)
	}
	// SCSI disks (SAS, SATA, iSCSI, FC) have a scsi_generic companion;
	// NVMe and virtual disks do not.
	_, gerr := os.Stat(filepath.Join(entry, "device", "generic"))
	return "/dev/" + name, gerr == nil, nil
}

// enumPoolSGDevices returns the SCSI disks (whole disks, /dev/sdX) of the
// pools imported on this node: the disks that can have persistent
// reservations at all.
func enumPoolSGDevices() ([]string, error) {
	out, err := exec.Command("zpool", "status", "-P").Output()
	if err != nil {
		return nil, fmt.Errorf("zpool status: %w", err)
	}
	disks := []string{}
	seen := map[string]bool{}
	for _, dev := range poolMemberDevices(string(out)) {
		disk, scsi, err := wholeDisk("/sys/class/block", dev)
		if err != nil || !scsi || seen[disk] {
			continue
		}
		seen[disk] = true
		disks = append(disks, disk)
	}
	return disks, nil
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
