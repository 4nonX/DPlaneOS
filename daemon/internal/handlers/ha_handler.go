package handlers

import (
	"context"
	"database/sql"
	"dplaned/internal/bmc"
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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

// GetFencingConfig returns the fencing settings that are not about one node.
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

// ConfigureFencing stores them.
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
		"message": "Fencing settings saved",
	})
}

// PowerTargetNodes returns the nodes this one can be asked to power off:
// the nodes it is paired with. Set at startup.
var PowerTargetNodes = func() map[string]string { return nil }

// ListPowerTargets returns how this node powers each other node off, and
// the nodes that have nothing configured yet.
// GET /api/ha/power-targets
func (h *HAHandler) ListPowerTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := ha.ListPowerTargets(h.db)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read the power targets", err)
		return
	}
	names := PowerTargetNodes()
	type row struct {
		ha.PowerTarget
		Name    string `json:"name"`
		Unknown bool   `json:"unknown_node"` // not (any more) a node this one is paired with
	}
	rows := []row{}
	have := map[string]bool{}
	for _, t := range targets {
		have[t.Node] = true
		name, known := names[t.Node]
		if !known {
			name = t.Node
		}
		rows = append(rows, row{PowerTarget: t, Name: name, Unknown: !known})
	}
	type node struct {
		Node string `json:"node"`
		Name string `json:"name"`
	}
	missing := []node{}
	for key, name := range names {
		if !have[key] {
			missing = append(missing, node{key, name})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Name < missing[j].Name })
	respondOK(w, map[string]any{"success": true, "targets": rows, "nodes_without": missing})
}

// SavePowerTarget stores how this node powers one node off. The password
// is kept when the field is left out.
// PUT /api/ha/power-targets/{node}
func (h *HAHandler) SavePowerTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ha.PowerTarget
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.Node = mux.Vars(r)["node"]
	if req.Node == "" {
		respondErrorSimple(w, "The node is required", http.StatusBadRequest)
		return
	}
	if _, known := PowerTargetNodes()[req.Node]; !known {
		respondErrorSimple(w, "This node is not paired with that node: pair them first (System, Configuration Sync)", http.StatusBadRequest)
		return
	}
	req.Host, req.Username = strings.TrimSpace(req.Host), strings.TrimSpace(req.Username)
	req.OffURL, req.StatusURL = strings.TrimSpace(req.OffURL), strings.TrimSpace(req.StatusURL)
	if err := ha.SavePowerTarget(h.db, req.PowerTarget, req.Password); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondOK(w, map[string]any{"success": true, "message": "Saved. Use Test to see that it works."})
}

// DeletePowerTarget removes a node's power target.
// DELETE /api/ha/power-targets/{node}
func (h *HAHandler) DeletePowerTarget(w http.ResponseWriter, r *http.Request) {
	if err := ha.DeletePowerTarget(h.db, mux.Vars(r)["node"]); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to remove the power target", err)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// TestPowerTarget tries a node's power target without switching anything:
// signs in to the BMC and reads the power state, or asks the PDU for its
// status.
// POST /api/ha/power-targets/{node}/test
func (h *HAHandler) TestPowerTarget(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res := ha.TestPowerTarget(ctx, h.db, mux.Vars(r)["node"])
	respondOK(w, map[string]any{"success": true, "result": res})
}

// EnrollPowerTarget pins the TLS certificate of a node's BMC (Redfish):
// after that the sign-in and the power-off go only to the machine that
// showed this certificate.
// POST /api/ha/power-targets/{node}/enroll   {"reset": true} forgets it
func (h *HAHandler) EnrollPowerTarget(w http.ResponseWriter, r *http.Request) {
	node := mux.Vars(r)["node"]
	var req struct {
		Reset bool `json:"reset"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Reset {
		if err := bmc.ClearTLSFingerprint(h.db, node); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to forget the certificate", err)
			return
		}
		respondOK(w, map[string]any{"success": true, "message": "The certificate is forgotten"})
		return
	}
	creds, _, err := bmc.LoadNode(h.db, node)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusPreconditionFailed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pin, info, err := bmc.EnrollCertificate(ctx, creds)
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": "The BMC did not answer over Redfish: " + err.Error() + ". Without a pinned certificate IPMI is used."})
		return
	}
	if err := bmc.SaveTLSFingerprint(h.db, node, pin); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to store the certificate", err)
		return
	}
	respondOK(w, map[string]any{"success": true, "fingerprint": pin.Fingerprint, "bmc_info": info})
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
	// SCSI disks (SAS, SATA, iSCSI, FC) hang off a SCSI device; NVMe and
	// virtual disks do not. (The scsi_generic companion would say the same,
	// but only while the sg module is loaded.)
	_, serr := os.Stat(filepath.Join(entry, "device", "scsi_device"))
	return "/dev/" + name, serr == nil, nil
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
