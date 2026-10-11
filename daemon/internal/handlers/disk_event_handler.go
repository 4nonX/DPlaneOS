package handlers

// disk_event_handler.go - what happens when a disk is added or removed.
// The events come from the kernel (disk_events_kernel.go).
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
	"log"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/libzfs"
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

// diskEventRequest describes one disk event.
type diskEventRequest struct {
	Action     string // "added" or "removed"
	Device     string // "/dev/sda"
	DeviceType string // "sata", "usb", "nvme", "virtual"
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

	zpoolOut, _ := runFast("zpool", "status", "-L", "-P")
	zpoolStatus := string(zpoolOut)

	poolName, health := libzfs.MemberOf(zpoolStatus, devName)

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
		attemptPoolImport(diskInfo)
		checkAndSuggestReplacement(diskInfo)
	}()
}

// FaultedVdev describes a single faulted vdev extracted from zpool status output.
type FaultedVdev struct {
	Pool     string `json:"pool"`
	VdevPath string `json:"path"`
	State    string `json:"state"`
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
				result = append(result, FaultedVdev{
					Pool:     currentPool,
					VdevPath: vdevName,
					State:    state,
				})
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
		if markErr := MarkDiskRemoved(registryDB, byID, devName); markErr != nil {
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
	RequestDiskRegistrySync()
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

// parseImportablePools parses the output of `zpool import -d /dev/disk/by-id`
// and returns a map of pool name → pool guid for importable pools.
func parseImportablePools(importOutput string) map[string]string {
	result := make(map[string]string)
	currentPool := ""
	for _, line := range strings.Split(importOutput, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			continue
		}
		if currentPool != "" && strings.HasPrefix(trimmed, "id:") {
			guid := strings.TrimSpace(strings.TrimPrefix(trimmed, "id:"))
			if guid != "" {
				result[currentPool] = guid
			}
			currentPool = ""
		}
	}
	return result
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

// runWithTimeout runs a command with the given timeout and returns the combined output.
func runWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}
