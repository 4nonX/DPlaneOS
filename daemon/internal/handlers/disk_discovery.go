package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DiskInfo is the enriched representation of a single block device.
// ByIDPath and ByPathPath are stable kernel-assigned symlink paths that
// survive disk re-ordering across reboots.  Always use ByIDPath for pool
// create / replace operations; fall back to "/dev/"+Name only when absent.
type DiskInfo struct {
	Name       string `json:"name"`
	DevPath    string `json:"dev_path"`     // /dev/sda
	ByIDPath   string `json:"by_id_path"`   // /dev/disk/by-id/wwn-0x...
	ByPathPath string `json:"by_path_path"` // /dev/disk/by-path/...
	WWN        string `json:"wwn,omitempty"`
	Size       string `json:"size"`
	SizeBytes  uint64 `json:"size_bytes"`
	Type       string `json:"type"` // NVMe, SSD, HDD, SAS, USB
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	RPM        int    `json:"rpm,omitempty"`
	InUse      bool   `json:"in_use"`
	PoolName   string `json:"pool_name,omitempty"` // which pool this disk belongs to
	MountPoint string `json:"mount_point,omitempty"`
	Health     string `json:"health,omitempty"` // ONLINE/FAULTED/DEGRADED from zpool status
	Temp       int    `json:"temp_c,omitempty"` // degrees C from smartctl, 0 if unavailable
}

// PoolSuggestion is a pre-configured pool topology the UI can offer the user.
type PoolSuggestion struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Disks      []string `json:"disks"`
	TotalSize  string   `json:"total_size"`
	UsableSize string   `json:"usable_size"`
	Redundancy string   `json:"redundancy"`
}

// blockDevice mirrors the JSON structure produced by lsblk -J.
type blockDevice struct {
	Name       string        `json:"name"`
	Size       string        `json:"size"`
	Type       string        `json:"type"`
	Model      string        `json:"model"`
	Serial     string        `json:"serial"`
	MountPoint string        `json:"mountpoint"`
	Children   []blockDevice `json:"children,omitempty"`
}

// HandleDiskDiscovery serves GET /api/system/disks.
func HandleDiskDiscovery(w http.ResponseWriter, r *http.Request) {
	disks, err := discoverDisks()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Operation failed", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"disks":       disks,
		"suggestions": generatePoolSuggestions(disks),
	})
}

// discoverDisks enumerates block devices via lsblk and enriches each disk
// with stable identifiers, size in bytes, disk type, temperature, and pool
// membership.  A single `zpool status` call is shared across all disks to
// avoid redundant subprocess spawning.
func discoverDisks() ([]DiskInfo, error) {
	lsblkOut, err := runFast("lsblk", "-J", "-o", "NAME,SIZE,TYPE,MODEL,SERIAL,MOUNTPOINT")
	if err != nil {
		return nil, err
	}

	var result struct {
		BlockDevices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(lsblkOut, &result); err != nil {
		return nil, err
	}

	// Fetch zpool status once; pass to all disks.
	zpoolOut, _ := runFast("zpool", "status", "-P", "-v")
	zpoolStatus := string(zpoolOut)

	var disks []DiskInfo
	for _, dev := range result.BlockDevices {
		if dev.Type != "disk" {
			continue
		}

		byID := findByIDPath(dev.Name)
		byPath := findByPathPath(dev.Name)
		wwn := readWWN(dev.Name)
		sizeBytes := readSizeBytes(dev.Name)
		diskType := detectDiskTypeEnhanced(dev.Name)
		temp := readDiskTempFast(dev.Name)

		poolName, health := getPoolForDisk(byID, dev.Name, zpoolStatus)
		inUse := hasMountPoint(dev) || poolName != ""

		disks = append(disks, DiskInfo{
			Name:       dev.Name,
			DevPath:    "/dev/" + dev.Name,
			ByIDPath:   byID,
			ByPathPath: byPath,
			WWN:        wwn,
			Size:       dev.Size,
			SizeBytes:  sizeBytes,
			Type:       diskType,
			Model:      dev.Model,
			Serial:     dev.Serial,
			InUse:      inUse,
			PoolName:   poolName,
			MountPoint: dev.MountPoint,
			Health:     health,
			Temp:       temp,
		})
	}

	return disks, nil
}

// ── Stable-identifier helpers ──────────────────────────────────────────────────

// findByIDPath scans /dev/disk/by-id/ and returns the first symlink whose
// resolved basename matches devName.  Prefers wwn- > ata- > scsi- prefixes.
func findByIDPath(devName string) string {
	return findSymlinkInDir("/dev/disk/by-id", devName, []string{"wwn-", "ata-", "scsi-"})
}

// findByPathPath scans /dev/disk/by-path/ and returns the first matching symlink.
func findByPathPath(devName string) string {
	return findSymlinkInDir("/dev/disk/by-path", devName, nil)
}

// findSymlinkInDir scans dir for symlinks whose resolved target basename
// matches devName.  If preferPrefixes is non-nil the first entry with a
// matching prefix wins; otherwise the lexicographically first match is used.
func findSymlinkInDir(dir, devName string, preferPrefixes []string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	// Collect all matching entries.
	var matches []string
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		full := filepath.Join(dir, e.Name())
		target, err := os.Readlink(full)
		if err != nil {
			continue
		}
		// Resolve relative targets (they are typically "../../sda").
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		if filepath.Base(target) == devName {
			matches = append(matches, filepath.Join(dir, e.Name()))
		}
	}

	if len(matches) == 0 {
		return ""
	}
	if len(preferPrefixes) == 0 {
		return matches[0]
	}
	for _, pfx := range preferPrefixes {
		for _, m := range matches {
			if strings.HasPrefix(filepath.Base(m), pfx) {
				return m
			}
		}
	}
	return matches[0]
}

// readWWN returns the World Wide Name for a disk.
// It first tries /sys/block/<name>/device/wwid; if that fails it extracts
// the hex part from the wwn- by-id symlink name.
func readWWN(devName string) string {
	wwid, err := os.ReadFile("/sys/block/" + devName + "/device/wwid")
	if err == nil {
		return strings.TrimSpace(string(wwid))
	}
	// Fall back to parsing the by-id path.
	byID := findByIDPath(devName)
	base := filepath.Base(byID)
	if strings.HasPrefix(base, "wwn-") {
		return strings.TrimPrefix(base, "wwn-")
	}
	return ""
}

// ── Disk-type detection ────────────────────────────────────────────────────────

// detectDiskTypeEnhanced classifies a disk as NVMe, SSD, HDD, SAS, or USB.
func detectDiskTypeEnhanced(devName string) string {
	// NVMe is identified by device name prefix alone.
	if strings.HasPrefix(devName, "nvme") {
		return "NVMe"
	}

	// Check if this device is connected via USB (e.g., thumb drive, USB HDD).
	// The sysfs subsystem symlink for USB devices resolves to a path containing "usb".
	subsysLink := "/sys/block/" + devName + "/device/subsystem"
	if subsys, err := os.Readlink(subsysLink); err == nil {
		if strings.Contains(subsys, "usb") {
			return "USB"
		}
	}
	// Also check two levels up for USB hubs.
	subsysLink2 := "/sys/block/" + devName + "/../../subsystem"
	if subsys2, err := os.Readlink(subsysLink2); err == nil {
		if strings.Contains(subsys2, "usb") {
			return "USB"
		}
	}

	// Check vendor field for SAS disks.
	if vendor, err := os.ReadFile("/sys/block/" + devName + "/device/vendor"); err == nil {
		v := strings.TrimSpace(strings.ToUpper(string(vendor)))
		if strings.Contains(v, "SAS") {
			return "SAS"
		}
	}

	// Rotational flag distinguishes SSD from HDD.
	rotData, err := os.ReadFile("/sys/block/" + devName + "/queue/rotational")
	if err != nil {
		return "Unknown"
	}
	if strings.TrimSpace(string(rotData)) == "0" {
		return "SSD"
	}
	return "HDD"
}

// ── Size helper ────────────────────────────────────────────────────────────────

// readSizeBytes reads the kernel's sector count from sysfs and converts to
// bytes (each sector is 512 bytes per the kernel ABI, regardless of physical
// sector size).
func readSizeBytes(devName string) uint64 {
	data, err := os.ReadFile("/sys/block/" + devName + "/size")
	if err != nil {
		return 0
	}
	sectors, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return sectors * 512
}

// ── Temperature helper ─────────────────────────────────────────────────────────

// smartctlJSON is a minimal subset of the smartctl -j output we care about.
type smartctlJSON struct {
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	ATASmartAttributes struct {
		Table []struct {
			Name  string `json:"name"`
			Value int    `json:"value"`
			Raw   struct {
				Value int `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// readDiskTempFast runs `smartctl -A -j /dev/<name>` with a 3-second timeout
// and parses the temperature from the JSON output.  Returns 0 on any error or
// when smartctl is unavailable.
func readDiskTempFast(devName string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "smartctl", "-A", "-j", "/dev/"+devName)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return 0
	}

	var s smartctlJSON
	if jsonErr := json.Unmarshal(out, &s); jsonErr != nil {
		return 0
	}

	// NVMe and some SATA drives report via the top-level temperature object.
	if s.Temperature.Current > 0 {
		return s.Temperature.Current
	}

	// Legacy ATA SMART attributes: look for Temperature_Celsius (id 194) or
	// Airflow_Temperature_Cel (id 190).
	for _, attr := range s.ATASmartAttributes.Table {
		if attr.Name == "Temperature_Celsius" || attr.Name == "Airflow_Temperature_Cel" {
			if attr.Raw.Value > 0 {
				return attr.Raw.Value
			}
			return attr.Value
		}
	}
	return 0
}

// ── Pool membership ────────────────────────────────────────────────────────────

// getPoolForDisk parses `zpool status -P -v` output to determine which pool
// (if any) owns this disk, and what health state the vdev is in.
// It matches against both the by-id path and the bare /dev/<name> path.
func getPoolForDisk(byIDPath, devName string, zpoolStatus string) (poolName, health string) {
	if zpoolStatus == "" {
		return "", ""
	}

	// Split output into per-pool sections at "pool:" lines.
	lines := strings.Split(zpoolStatus, "\n")
	currentPool := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			continue
		}
		if currentPool == "" {
			continue
		}
		// Check if this vdev line references our disk.
		matched := false
		if byIDPath != "" && strings.Contains(trimmed, byIDPath) {
			matched = true
		}
		if !matched && strings.Contains(trimmed, "/dev/"+devName) {
			matched = true
		}
		// Also match bare device names (e.g., "sda" in older zpool output).
		if !matched {
			fields := strings.Fields(trimmed)
			if len(fields) > 0 && fields[0] == devName {
				matched = true
			}
		}
		if matched {
			// The vdev state is the second whitespace-separated field on the line.
			fields := strings.Fields(trimmed)
			state := ""
			if len(fields) >= 2 {
				state = fields[1]
			}
			return currentPool, state
		}
	}
	return "", ""
}

// hasMountPoint returns true if the device or any of its partitions is mounted.
func hasMountPoint(dev blockDevice) bool {
	if strings.TrimSpace(dev.MountPoint) != "" {
		return true
	}
	for _, child := range dev.Children {
		if hasMountPoint(child) {
			return true
		}
	}
	return false
}

// isInZFSPool is kept for backwards compatibility; discoverDisks() now uses
// getPoolForDisk instead.
func isInZFSPool(diskName string) bool {
	zpoolOut, err := runFast("zpool", "status", "-P")
	if err != nil {
		return false
	}
	return diskNameInZpoolStatus(string(zpoolOut), diskName)
}

func diskNameInZpoolStatus(status, diskName string) bool {
	if diskName == "" {
		return false
	}
	pattern := regexp.MustCompile(`(^|[^[:alnum:]])` + regexp.QuoteMeta(diskName) + `(p?[0-9]+)?([^[:alnum:]]|$)`)
	return pattern.MatchString(status)
}

// detectDiskType is the original simple classifier; kept so existing callers compile.
func detectDiskType(name string) string {
	return detectDiskTypeEnhanced(name)
}

// runFast is a thin wrapper around exec with a 10-second timeout, returning
// the combined stdout+stderr output as a byte slice.
func runFast(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// ── Pool suggestions ───────────────────────────────────────────────────────────

// generatePoolSuggestions proposes common ZFS topologies for the available
// (not-in-use) disks.  Disk identifiers in suggestions use the stable ByIDPath
// when present, falling back to "/dev/"+Name.
func generatePoolSuggestions(disks []DiskInfo) []PoolSuggestion {
	var suggestions []PoolSuggestion

	var available []DiskInfo
	for _, d := range disks {
		if !d.InUse {
			available = append(available, d)
		}
	}
	if len(available) == 0 {
		return suggestions
	}

	// stableID returns the best available stable path for a disk.
	stableID := func(d DiskInfo) string {
		if d.ByIDPath != "" {
			return d.ByIDPath
		}
		return "/dev/" + d.Name
	}

	if len(available) >= 1 {
		suggestions = append(suggestions, PoolSuggestion{
			Name:       "tank",
			Type:       "Single",
			Disks:      []string{stableID(available[0])},
			TotalSize:  available[0].Size,
			UsableSize: available[0].Size,
			Redundancy: "None - Data loss if disk fails",
		})
	}

	if len(available) >= 2 {
		suggestions = append(suggestions, PoolSuggestion{
			Name:       "tank",
			Type:       "Mirror",
			Disks:      []string{stableID(available[0]), stableID(available[1])},
			TotalSize:  available[0].Size + " (mirrored)",
			UsableSize: available[0].Size,
			Redundancy: "1 disk failure",
		})
	}

	if len(available) >= 3 {
		numDisks := 3
		if len(available) >= 4 {
			numDisks = 4
		}
		var diskPaths []string
		for i := 0; i < numDisks && i < len(available); i++ {
			diskPaths = append(diskPaths, stableID(available[i]))
		}
		suggestions = append(suggestions, PoolSuggestion{
			Name:       "tank",
			Type:       "RAID-Z1",
			Disks:      diskPaths,
			TotalSize:  fmt.Sprintf("%s x %d", available[0].Size, len(diskPaths)),
			UsableSize: fmt.Sprintf("%s x %d", available[0].Size, len(diskPaths)-1),
			Redundancy: "1 disk failure",
		})
	}

	if len(available) >= 4 {
		numDisks := len(available)
		if numDisks > 6 {
			numDisks = 6
		}
		var diskPaths []string
		for i := 0; i < numDisks; i++ {
			diskPaths = append(diskPaths, stableID(available[i]))
		}
		suggestions = append(suggestions, PoolSuggestion{
			Name:       "tank",
			Type:       "RAID-Z2",
			Disks:      diskPaths,
			TotalSize:  fmt.Sprintf("%s x %d", available[0].Size, len(diskPaths)),
			UsableSize: fmt.Sprintf("%s x %d", available[0].Size, len(diskPaths)-2),
			Redundancy: "2 disk failures (Recommended)",
		})
	}

	if len(available) >= 5 {
		var diskPaths []string
		for i := range available {
			diskPaths = append(diskPaths, stableID(available[i]))
		}
		suggestions = append(suggestions, PoolSuggestion{
			Name:       "tank",
			Type:       "RAID-Z3",
			Disks:      diskPaths,
			TotalSize:  fmt.Sprintf("%s x %d", available[0].Size, len(diskPaths)),
			UsableSize: fmt.Sprintf("%s x %d", available[0].Size, len(diskPaths)-3),
			Redundancy: "3 disk failures (Maximum protection)",
		})
	}

	return suggestions
}

// ── Pool create handler ────────────────────────────────────────────────────────

// HandlePoolCreate serves POST /api/system/pool/create.
// It validates that all submitted disk paths are stable by-id or by-path
// references (or short names that can be resolved to a by-id path via the
// disk registry).  Short names without a registry entry are rejected with a
// descriptive error.
func HandlePoolCreate(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name  string   `json:"name"`
		Type  string   `json:"type"`
		Disks []string `json:"disks"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request", err)
		return
	}

	if request.Name == "" {
		respondErrorSimple(w, "pool name is required", http.StatusBadRequest)
		return
	}
	if len(request.Disks) == 0 {
		respondErrorSimple(w, "at least one disk is required", http.StatusBadRequest)
		return
	}

	// Validate and resolve disk paths.
	resolvedDisks, err := resolveAndValidateDiskPaths(request.Disks)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	args := []string{"create", "-f", request.Name}

	switch request.Type {
	case "", "Single":
		// stripe/single vdev — no extra keyword
	case "Mirror":
		args = append(args, "mirror")
	case "RAID-Z1":
		args = append(args, "raidz")
	case "RAID-Z2":
		args = append(args, "raidz2")
	case "RAID-Z3":
		args = append(args, "raidz3")
	default:
		respondErrorSimple(w, "invalid pool type", http.StatusBadRequest)
		return
	}

	args = append(args, resolvedDisks...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "zpool", args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": string(output),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "created",
		"name":   request.Name,
	})
}

// resolveAndValidateDiskPaths converts each submitted disk path to a stable
// /dev/disk/by-id/ identifier when possible, and rejects anything that is
// neither already a stable path nor a resolvable short name.
func resolveAndValidateDiskPaths(paths []string) ([]string, error) {
	resolved := make([]string, 0, len(paths))
	for _, p := range paths {
		// Already a stable absolute path — accept as-is.
		if strings.HasPrefix(p, "/dev/disk/by-id/") || strings.HasPrefix(p, "/dev/disk/by-path/") {
			resolved = append(resolved, p)
			continue
		}

		// Full /dev/<name> path — strip to bare name and look up.
		name := p
		if strings.HasPrefix(p, "/dev/") {
			name = strings.TrimPrefix(p, "/dev/")
		}

		// name must not contain slashes or path separators (injection guard).
		if strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("disk paths must use stable /dev/disk/by-id/ identifiers (got: %s)", p)
		}

		// Try to resolve to a by-id path via sysfs symlinks.
		byID := findByIDPath(name)
		if byID != "" {
			resolved = append(resolved, byID)
			continue
		}

		// Also try registry lookup if a db is available.
		if registryDB != nil {
			rec, err := GetDiskByDevName(registryDB, name)
			if err == nil && rec != nil && rec.ByIDPath != "" {
				resolved = append(resolved, rec.ByIDPath)
				continue
			}
		}

		// Could not resolve — reject with a clear error.
		return nil, fmt.Errorf(
			"disk paths must use stable /dev/disk/by-id/ identifiers (could not resolve %q to a by-id path)", p,
		)
	}
	return resolved, nil
}
