package gitops

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"dplaned/internal/cmdutil"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  LIVE STATE READER
//
//  Reads the actual state of the system from ZFS commands and the DB.
//  This is the ground truth against which desired state is diffed.
// ═══════════════════════════════════════════════════════════════════════════════

// LivePool is the observed state of a ZFS pool.
type LivePool struct {
	Name   string
	Health string // ONLINE, DEGRADED, FAULTED, UNAVAIL, REMOVED
	Disks  []string // /dev/disk/by-id/... paths from `zpool status`
}

// LiveDataset is the observed state of a ZFS dataset.
type LiveDataset struct {
	Name        string
	Used        uint64 // bytes — 0 means genuinely empty or unknown
	Avail       uint64
	Compression string
	Atime       string
	Mountpoint  string
	Quota       string // raw string from zfs get, e.g. "2000000000" or "none"
	Encrypted   bool
}

// LiveShare is an SMB share as stored in the daemon DB.
type LiveShare struct {
	ID         int64
	Name       string
	Path       string
	ReadOnly   bool
	ValidUsers string
	Comment    string
	GuestOK    bool
	Enabled    bool
}

// LiveState is the complete observed system state.
type LiveState struct {
	Pools    []LivePool
	Datasets []LiveDataset
	Shares   []LiveShare
}

// ReadLiveState collects all live state from ZFS and the DB.
// Called by the diff engine and the drift detector.
func ReadLiveState(db *sql.DB) (*LiveState, error) {
	state := &LiveState{}
	var err error

	state.Pools, err = readLivePools()
	if err != nil {
		return nil, fmt.Errorf("reading live pools: %w", err)
	}

	state.Datasets, err = readLiveDatasets()
	if err != nil {
		return nil, fmt.Errorf("reading live datasets: %w", err)
	}

	state.Shares, err = readLiveShares(db)
	if err != nil {
		return nil, fmt.Errorf("reading live shares: %w", err)
	}

	return state, nil
}

// readLivePools reads pool state via `zpool list` and disk membership via
// `zpool status` to extract /dev/disk/by-id/ paths.
func readLivePools() ([]LivePool, error) {
	// Pool list: name and health
	out, err := cmdutil.RunZFS("zpool", "list", "-H", "-o", "name,health")
	if err != nil {
		return nil, fmt.Errorf("zpool list: %w", err)
	}

	var pools []LivePool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pools = append(pools, LivePool{
			Name:   fields[0],
			Health: fields[1],
		})
	}

	// Disk membership: `zpool status` with -P flag for full paths
	statusOut, err := cmdutil.RunZFS("zpool", "status", "-P")
	if err == nil {
		disksByPool := parseZpoolStatusPaths(string(statusOut))
		for i, p := range pools {
			if disks, ok := disksByPool[p.Name]; ok {
				pools[i].Disks = disks
			}
		}
	}

	return pools, nil
}

// parseZpoolStatusPaths extracts per-pool disk paths from `zpool status -P`.
// Only lines with /dev/disk/by-id/ paths are kept — everything else (mirror,
// raidz, spares, cache headers) is ignored.
func parseZpoolStatusPaths(output string) map[string][]string {
	result := make(map[string][]string)
	var currentPool string

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)

		// Pool header: "  pool: tank"
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			continue
		}

		// Device line: contains /dev/disk/by-id/ path
		if currentPool != "" && strings.Contains(trimmed, "/dev/disk/by-id/") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 1 {
				result[currentPool] = append(result[currentPool], fields[0])
			}
		}
	}
	return result
}

// readLiveDatasets reads dataset properties via `zfs get`.
// Uses a single `zfs get` call for all properties to minimise ZFS I/O.
func readLiveDatasets() ([]LiveDataset, error) {
	// Get names first
	listOut, err := cmdutil.RunZFS("zfs", "list", "-H", "-t", "filesystem", "-o", "name")
	if err != nil {
		return nil, fmt.Errorf("zfs list: %w", err)
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
		if n := strings.TrimSpace(line); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	// Fetch all properties in one `zfs get -H -p` call
	// -p = machine-parseable (exact bytes for sizes)
	props := "used,avail,compression,atime,mountpoint,quota,encryption"
	args := append([]string{"get", "-H", "-p", "-o", "name,property,value", props}, names...)
	getOut, err := cmdutil.RunZFS("zfs", args...)
	if err != nil {
		return nil, fmt.Errorf("zfs get: %w", err)
	}

	// Build dataset map from output
	type propsMap map[string]string
	dsProps := make(map[string]propsMap)
	for _, line := range strings.Split(string(getOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name, prop, value := fields[0], fields[1], fields[2]
		if _, ok := dsProps[name]; !ok {
			dsProps[name] = make(propsMap)
		}
		dsProps[name][prop] = value
	}

	var datasets []LiveDataset
	for _, name := range names {
		pm := dsProps[name]
		if pm == nil {
			pm = make(propsMap)
		}
		ds := LiveDataset{
			Name:        name,
			Compression: pm["compression"],
			Atime:       pm["atime"],
			Mountpoint:  pm["mountpoint"],
			Quota:       pm["quota"],
			Encrypted:   pm["encryption"] != "" && pm["encryption"] != "off",
		}
		ds.Used, _ = strconv.ParseUint(pm["used"], 10, 64)
		ds.Avail, _ = strconv.ParseUint(pm["avail"], 10, 64)
		datasets = append(datasets, ds)
	}

	return datasets, nil
}

// readLiveShares reads SMB share state from the daemon DB.
func readLiveShares(db *sql.DB) ([]LiveShare, error) {
	rows, err := db.Query(`
		SELECT id, name, path, read_only, valid_users, comment, guest_ok, enabled
		FROM smb_shares ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("query smb_shares: %w", err)
	}
	defer rows.Close()

	var shares []LiveShare
	for rows.Next() {
		var s LiveShare
		var roInt, gokInt, enabledInt int
		if err := rows.Scan(&s.ID, &s.Name, &s.Path, &roInt, &s.ValidUsers, &s.Comment, &gokInt, &enabledInt); err != nil {
			continue
		}
		s.ReadOnly = roInt == 1
		s.GuestOK = gokInt == 1
		s.Enabled = enabledInt == 1
		shares = append(shares, s)
	}
	return shares, nil
}

// HasActiveSMBConnections returns true if smbstatus reports any active connections
// to the named share. Uses cmdutil.RunFast — if smbstatus is unavailable or
// returns no output, conservatively returns false (do not block).
//
// This is the live-connection probe used by the BLOCKED safety check.
func HasActiveSMBConnections(shareName string) bool {
	// smbstatus -S outputs one line per share with connected clients.
	// Format: "sharename  pid  machine  connected-at"
	out, err := cmdutil.RunFast("smbstatus", "-S", "-n")
	if err != nil {
		// smbstatus not available or Samba not running — cannot confirm connections.
		// Conservative: return false (do not incorrectly block).
		return false
	}

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 1 && fields[0] == shareName {
			return true
		}
	}
	return false
}

// DatasetUsedBytes returns the `used` property of a dataset in bytes.
// Returns 0 if the dataset does not exist or the property cannot be read.
func DatasetUsedBytes(name string) uint64 {
	out, err := cmdutil.RunZFS("zfs", "get", "-H", "-p", "-o", "value", "used", name)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return n
}
