package handlers

// disk_event_handler.go - DPlaneOS disk lifecycle events
//
// POST /api/internal/disk-event
//
// This endpoint is intentionally restricted to localhost callers.  It is
// designed to be called by a udev rule or systemd service running on the same
// host whenever a block device is added or removed.
//
// Request body:
//
//	{
//	    "action":      "added" | "removed",
//	    "device":      "/dev/sda",           // full /dev path
//	    "device_type": "sata" | "usb" | "nvme",
//	    "label":       "optional-label"
//	}
//
// On "added":
//  1. Enrich the device with stable identifiers.
//  2. Upsert the disk registry.
//  3. Broadcast diskAdded WS event.
//  4. Check for FAULTED/UNAVAIL pools that can be re-imported.
//  5. Attempt import; broadcast poolHealthChanged.
//  6. Cross-reference against faulted vdevs; broadcast diskReplacementAvailable
//     if a faulted vdev exists that the new disk could replace.
//
// On "removed":
//  1. Mark disk removed in registry.
//  2. Broadcast diskRemoved WS event.
//  3. Check pool health; broadcast poolHealthChanged if any pool degraded.

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// diskEventBroadcaster is the interface we need from the WS hub.
// *websocket.MonitorHub satisfies this.
type diskEventBroadcaster interface {
	Broadcast(eventType string, data any, level string)
}

// diskEventHub is set via SetDiskEventHub from main.go.
var diskEventHub diskEventBroadcaster

// SetDiskEventHub injects the WebSocket hub so that disk event handlers can
// broadcast events to connected UI clients.
func SetDiskEventHub(hub diskEventBroadcaster) {
	diskEventHub = hub
}

// diskEventRequest is the JSON body for POST /api/internal/disk-event.
type diskEventRequest struct {
	Action     string `json:"action"`      // "added" or "removed"
	Device     string `json:"device"`      // "/dev/sda"
	DeviceType string `json:"device_type"` // "sata", "usb", "nvme"
	Label      string `json:"label"`
}

// HandleDiskEvent handles POST /api/internal/disk-event.
// It validates that the caller is localhost before processing.
func HandleDiskEvent(w http.ResponseWriter, r *http.Request) {
	// ── Localhost-only guard ──────────────────────────────────────────────────
	if !isLocalhostRequest(r) {
		respondErrorSimple(w, "forbidden: disk-event is localhost-only", http.StatusForbidden)
		return
	}

	var req diskEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	req.Device = strings.TrimSpace(req.Device)
	req.Action = strings.TrimSpace(req.Action)

	if req.Device == "" || (req.Action != "added" && req.Action != "removed") {
		respondErrorSimple(w, "device and action (added|removed) are required", http.StatusBadRequest)
		return
	}

	// Strip /dev/ prefix to get the bare device name (e.g. "sda").
	devName := strings.TrimPrefix(req.Device, "/dev/")

	switch req.Action {
	case "added":
		handleDiskAdded(devName, req)
	case "removed":
		handleDiskRemoved(devName, req)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ── Added ─────────────────────────────────────────────────────────────────────

func handleDiskAdded(devName string, req diskEventRequest) {
	log.Printf("DISK EVENT: added /dev/%s (type=%s)", devName, req.DeviceType)

	// 1. Enrich the new device.
	byID := findByIDPath(devName)
	byPath := findByPathPath(devName)
	wwn := readWWN(devName)
	sizeBytes := readSizeBytes(devName)
	diskType := detectDiskTypeEnhanced(devName)
	temp := readDiskTempFast(devName)

	zpoolOut, _ := runFast("zpool", "status", "-P", "-v")
	zpoolStatus := string(zpoolOut)

	poolName, health := getPoolForDisk(byID, devName, zpoolStatus)

	// Which pool this disk belonged to when it was last seen here. Read
	// before the upsert below, which records the current (for a pool that is
	// not imported: empty) pool name.
	lastPool := ""
	if registryDB != nil && byID != "" {
		lastPool = lastKnownPool(registryDB, byID)
	}

	diskInfo := DiskInfo{
		Name:       devName,
		DevPath:    "/dev/" + devName,
		ByIDPath:   byID,
		ByPathPath: byPath,
		WWN:        wwn,
		SizeBytes:  sizeBytes,
		Type:       diskType,
		InUse:      poolName != "",
		PoolName:   poolName,
		Health:     health,
		Temp:       temp,
	}

	// 2. Update registry.
	if registryDB != nil {
		if err := UpsertDisk(registryDB, diskInfo); err != nil {
			log.Printf("WARN: disk registry upsert failed for /dev/%s: %v", devName, err)
		}
	}

	// 3. Broadcast diskAdded.
	broadcastDiskEvent("diskAdded", diskInfo, "info")

	// 4. Check for pools that might be importable now that this disk appeared.
	// Also check for faulted vdevs that this disk could replace.
	go func() {
		// Small delay - give the kernel time to fully register the device.
		time.Sleep(2 * time.Second)
		attemptPoolImport(diskInfo, lastPool)
		checkAndSuggestReplacement(diskInfo)
	}()
}

// attemptPoolImport brings back the pool of a disk that returns:
//   - a member of an imported pool (removed and plugged back in) is put back
//     online, in case ZED has not done it already;
//   - an exported pool is imported when `zpool import` lists the new disk as
//     one of its members and the registry recorded the disk in that very pool
//     here before. A disk from another system never imports its pool.
func attemptPoolImport(disk DiskInfo, lastPool string) {
	if disk.ByIDPath == "" {
		broadcastPoolHealthChanged()
		return
	}
	base := filepath.Base(disk.ByIDPath)

	statusOut, err := runWithTimeout(30*time.Second, "zpool", "status", "-P", "-v")
	if err != nil {
		log.Printf("DISK EVENT: zpool status failed during import check: %v", err)
		return
	}
	for _, fv := range findFaultedVdevs(string(statusOut)) {
		if !vdevIsDisk(filepath.Base(fv.VdevPath), base) && (fv.Was == "" || !vdevIsDisk(filepath.Base(fv.Was), base)) {
			continue
		}
		log.Printf("DISK EVENT: /dev/%s is member %s of pool %q (%s): bringing it online", disk.Name, fv.VdevPath, fv.Pool, fv.State)
		if out, err := cmdutil.RunMedium("zpool_online", "online", fv.Pool, fv.VdevPath); err != nil {
			log.Printf("DISK EVENT: zpool online %s %s: %v: %s", fv.Pool, fv.VdevPath, err, strings.TrimSpace(string(out)))
		}
		broadcastPoolHealthChanged()
		return
	}

	importOut, _ := runWithTimeout(30*time.Second, "zpool", "import", "-d", "/dev/disk/by-id")
	poolName, poolGUID, ok := importDecision(string(importOut), base, lastPool)
	if !ok {
		broadcastPoolHealthChanged()
		return
	}
	// A pool of a shared storage group is imported by its owner only.
	if ok, why := importAllowed(poolGUID, poolName); !ok {
		log.Printf("DISK EVENT: not importing pool %q: %s", poolName, why)
		broadcastPoolHealthChanged()
		return
	}
	log.Printf("DISK EVENT: attempting import of pool %q (GUID %s, disk /dev/%s added)", poolName, poolGUID, disk.Name)
	// Import by GUID: names can collide. The GUID is positional (no -g flag).
	importResult, importErr := cmdutil.Run(2*time.Minute, "zpool_import", "import", "-d", "/dev/disk/by-id", poolGUID)
	if importErr != nil {
		log.Printf("DISK EVENT: pool import %q failed: %v - %s", poolName, importErr, strings.TrimSpace(string(importResult)))
	} else {
		log.Printf("DISK EVENT: pool %q imported successfully", poolName)
	}
	broadcastPoolHealthChanged()
}

// importDecision picks the exported pool to import for a disk that appeared:
// the importable pool that lists the disk (by-id base name) as a member and
// is the pool the registry last saw the disk in. ok is false otherwise.
func importDecision(importOutput, diskByIDBase, lastPool string) (name, guid string, ok bool) {
	if lastPool == "" || diskByIDBase == "" {
		return "", "", false
	}
	for _, p := range parseImportablePools(importOutput) {
		if p.name != lastPool {
			continue
		}
		for _, m := range p.members {
			if vdevIsDisk(filepath.Base(m), diskByIDBase) {
				return p.name, p.guid, true
			}
		}
	}
	return "", "", false
}

// vdevIsDisk reports whether a vdev name in zpool output is the disk with
// this by-id base name or one of its partitions (ZFS partitions whole disks).
func vdevIsDisk(vdev, diskByIDBase string) bool {
	return vdev == diskByIDBase || strings.HasPrefix(vdev, diskByIDBase+"-part")
}

// FaultedVdev describes a single faulted vdev extracted from zpool status output.
type FaultedVdev struct {
	Pool     string `json:"pool"`
	VdevPath string `json:"path"`
	State    string `json:"state"`
	// Was is the device path of a missing vdev that zpool lists by GUID
	// ("12345  UNAVAIL  0 0 0  was /dev/disk/by-id/...").
	Was string `json:"was,omitempty"`
}

// findFaultedVdevs parses `zpool status -P -v` output and returns all vdevs
// whose state is FAULTED, REMOVED, or UNAVAIL.
//
// The relevant section of zpool status looks like:
//
//	pool: tank
//	...
//	config:
//	        NAME              STATE     READ WRITE CKSUM
//	        tank              DEGRADED     0     0     0
//	          mirror-0        DEGRADED     0     0     0
//	            /dev/sda      ONLINE       0     0     0
//	            /dev/sdb      FAULTED      0     0     2
func findFaultedVdevs(poolStatus string) []FaultedVdev {
	var result []FaultedVdev
	currentPool := ""
	inConfig := false

	for _, line := range strings.Split(poolStatus, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			inConfig = false
			continue
		}

		if strings.HasPrefix(trimmed, "config:") {
			inConfig = true
			continue
		}

		if !inConfig || currentPool == "" {
			continue
		}

		// New top-level field ends the config section
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") && trimmed != "" {
			inConfig = false
			continue
		}

		// Parse vdev lines: they contain a path/name + state
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}

		vdevName := fields[0]
		state := fields[1]

		if state == "FAULTED" || state == "REMOVED" || state == "UNAVAIL" {
			// Only report real device paths or short names - skip pool/vdev-group names
			if strings.HasPrefix(vdevName, "/dev/") ||
				(!strings.HasPrefix(vdevName, "mirror") &&
					!strings.HasPrefix(vdevName, "raidz") &&
					!strings.HasPrefix(vdevName, "spare") &&
					vdevName != currentPool) {
				fv := FaultedVdev{
					Pool:     currentPool,
					VdevPath: vdevName,
					State:    state,
				}
				for i := 2; i+1 < len(fields); i++ {
					if fields[i] == "was" {
						fv.Was = fields[i+1]
						break
					}
				}
				result = append(result, fv)
			}
		}
	}

	return result
}

// checkAndSuggestReplacement checks if the newly-added disk can replace any
// faulted vdev and, if so, broadcasts a diskReplacementAvailable WS event.
func checkAndSuggestReplacement(newDisk DiskInfo) {
	if diskEventHub == nil {
		return
	}

	statusOut, err := runWithTimeout(30*time.Second, "zpool", "status", "-P", "-v")
	if err != nil {
		log.Printf("DISK EVENT: zpool status failed during replacement check: %v", err)
		return
	}

	faultedVdevs := findFaultedVdevs(string(statusOut))
	if len(faultedVdevs) == 0 {
		return
	}

	// Exclude the new disk itself from faulted vdevs (it shouldn't be faulted
	// unless re-added after a previous failure, but be safe).
	newDiskPaths := map[string]bool{
		"/dev/" + newDisk.Name: true,
		newDisk.ByIDPath:       true,
		newDisk.ByPathPath:     true,
	}

	var candidates []FaultedVdev
	for _, fv := range faultedVdevs {
		if !newDiskPaths[fv.VdevPath] {
			candidates = append(candidates, fv)
		}
	}

	if len(candidates) == 0 {
		return
	}

	log.Printf("DISK EVENT: new disk /dev/%s may replace %d faulted vdev(s) - broadcasting suggestion",
		newDisk.Name, len(candidates))

	diskEventHub.Broadcast("diskReplacementAvailable", map[string]any{
		"new_disk": map[string]any{
			"dev":   "/dev/" + newDisk.Name,
			"by_id": newDisk.ByIDPath,
			"model": newDisk.Model,
			"size":  newDisk.Size, // already human-readable from lsblk
		},
		"faulted_vdevs": candidates,
	}, "warning")
}

// ── Removed ───────────────────────────────────────────────────────────────────

func handleDiskRemoved(devName string, req diskEventRequest) {
	log.Printf("DISK EVENT: removed /dev/%s", devName)

	// Determine the by-id path from the registry if we no longer have the
	// physical device to read symlinks from.
	byID := findByIDPath(devName) // may return "" if device is already gone
	if byID == "" && registryDB != nil {
		rec, _ := GetDiskByDevName(registryDB, devName)
		if rec != nil {
			byID = rec.ByIDPath
		}
	}

	// 1. Mark removed in registry.
	if registryDB != nil {
		if markErr := MarkDiskRemoved(registryDB, byID); markErr != nil {
			log.Printf("WARN: MarkDiskRemoved failed for %s: %v", byID, markErr)
		}
	}

	// 2. Broadcast diskRemoved.
	broadcastDiskEvent("diskRemoved", map[string]string{
		"device": req.Device,
		"by_id":  byID,
	}, "warning")

	// 3. Check pool health.
	broadcastPoolHealthChanged()
}

// ── Broadcast helpers ─────────────────────────────────────────────────────────

func broadcastDiskEvent(eventType string, data any, level string) {
	if diskEventHub == nil {
		return
	}
	diskEventHub.Broadcast(eventType, data, level)
}

// BroadcastPoolHealthChanged reads the current pool status and broadcasts a
// pool_health_change event to all WS clients.
// Called by disk event handlers and by the ZED listener callback in main.go.
func BroadcastPoolHealthChanged() {
	if diskEventHub == nil {
		return
	}

	statusOut, err := runWithTimeout(30*time.Second, "zpool", "status", "-P")
	if err != nil {
		log.Printf("DISK EVENT: zpool status for health broadcast failed: %v", err)
		return
	}

	poolHealthMap := parsePoolHealthSummary(string(statusOut))
	// Event name matches ws.ts switch case 'pool_health_change'
	diskEventHub.Broadcast("pool_health_change", poolHealthMap, "info")
}

// broadcastPoolHealthChanged is the package-internal alias kept for existing callers.
func broadcastPoolHealthChanged() { BroadcastPoolHealthChanged() }

// ── zpool output parsers ──────────────────────────────────────────────────────

// importablePool is one pool listed by `zpool import`.
type importablePool struct {
	name, guid string
	members    []string // vdev names in its config (by-id base names with -d /dev/disk/by-id)
}

// parseImportablePools parses the output of `zpool import -d /dev/disk/by-id`:
// each pool's name, GUID and the devices of its config section.
func parseImportablePools(importOutput string) []importablePool {
	var pools []importablePool
	inConfig := false
	for _, line := range strings.Split(importOutput, "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		switch {
		case strings.HasPrefix(trimmed, "pool:"):
			pools = append(pools, importablePool{name: strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))})
			inConfig = false
		case len(pools) == 0:
		case strings.HasPrefix(trimmed, "id:"):
			pools[len(pools)-1].guid = strings.TrimSpace(strings.TrimPrefix(trimmed, "id:"))
		case strings.HasPrefix(trimmed, "config:"):
			inConfig = true
		case inConfig && len(fields) >= 2:
			p := &pools[len(pools)-1]
			if fields[0] != p.name && !isVdevGroupName(fields[0]) {
				p.members = append(p.members, fields[0])
			}
		}
	}
	return pools
}

// isVdevGroupName reports whether a zpool config entry is a grouping (mirror-0,
// raidz2-1, logs, ...) rather than a device.
func isVdevGroupName(n string) bool {
	for _, g := range []string{"mirror", "raidz", "draid", "spare", "replacing"} {
		if strings.HasPrefix(n, g) {
			return true
		}
	}
	switch n {
	case "logs", "cache", "spares", "special", "dedup":
		return true
	}
	return false
}

// parsePoolHealthSummary returns a map of poolName → state string extracted
// from the "state:" lines in `zpool status` output.
func parsePoolHealthSummary(statusOutput string) map[string]string {
	result := make(map[string]string)
	currentPool := ""
	for _, line := range strings.Split(statusOutput, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			continue
		}
		if currentPool != "" && strings.HasPrefix(trimmed, "state:") {
			result[currentPool] = strings.TrimSpace(strings.TrimPrefix(trimmed, "state:"))
			currentPool = ""
		}
	}
	return result
}

// ── Misc helpers ──────────────────────────────────────────────────────────────

// isLocalhostRequest returns true if r.RemoteAddr is a loopback address.
// isLocalhostRequest: a caller on this host - the daemon's Unix socket
// (RemoteAddr "" or "@") or loopback TCP. Requests through nginx also come
// over the socket: the session middleware lets this route through only with
// the per-boot internal token.
func isLocalhostRequest(r *http.Request) bool {
	if r.RemoteAddr == "" || r.RemoteAddr == "@" {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// If no port (unlikely), treat the whole string as the host.
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runWithTimeout runs a command with the given timeout and returns the combined output.
func runWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}
