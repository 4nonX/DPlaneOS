// Package libzfs provides a thin abstraction over ZFS operations.
//
// When built with CGO_ENABLED=1 on Linux (the production NixOS path), the
// functions in this package call into libzfs directly via cgo, avoiding
// subprocess spawning for every ZFS operation. When CGO is unavailable or
// the target is not Linux, all functions fall back to cmdutil subprocess
// calls so the binary still compiles and works (dev machines, CI).
//
// Build variants:
//
//	zfs_cgo.go      – //go:build linux && cgo  – libzfs direct (production)
//	zfs_fallback.go – //go:build !linux || !cgo – cmdutil subprocess (dev/CI)
//
// Flake.nix wires the CGO variant via mkDaemonCGO; the existing static musl
// build (mkDaemon, CGO_ENABLED=0) continues to use the fallback path.
package libzfs

import (
	"fmt"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// PoolMembership reports whether a block device is a member of any active
// ZFS pool, and if so, which pool.
type PoolMembership struct {
	InPool   bool
	PoolName string
}

// HoldEntry represents a single hold on a ZFS snapshot.
type HoldEntry struct {
	Tag       string `json:"tag"`
	Timestamp string `json:"timestamp"`
}

// Error carries a libzfs operation name and the underlying error message
// returned by libzfs or the fallback subprocess.
type Error struct {
	Op  string
	Msg string
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("libzfs %s: %s", e.Op, e.Msg)
	}
	return fmt.Sprintf("libzfs %s failed", e.Op)
}

func libzfsErr(op, msg string) error { return &Error{Op: op, Msg: msg} }

// ── Shared operations (subprocess in all build variants) ──────────────────
// These operations map to complex CLI commands where native libzfs bindings
// would require significant C nvlist construction. Using the subprocess path
// in all variants is correct and consistent with how OpenZFS tools approach
// the same operations.

// VdevAdd adds a new vdev to an existing pool. vdevType is the topology type
// (mirror, raidz, raidz2, raidz3, cache, log, special, spare) or empty for a
// single-device stripe.
func VdevAdd(pool, vdevType string, devices []string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevAdd", err.Error())
	}
	for _, d := range devices {
		if err := security.ValidateDevicePath(d); err != nil {
			return libzfsErr("VdevAdd", "invalid device "+d+": "+err.Error())
		}
	}
	args := []string{"add", pool}
	if vdevType != "" {
		args = append(args, vdevType)
	}
	args = append(args, devices...)
	out, err := cmdutil.RunMedium("zpool_add", args...)
	if err != nil {
		return libzfsErr("VdevAdd", string(out))
	}
	return nil
}

// VdevAttach attaches newDevice alongside existingDevice to create or extend a
// mirror. Starts a resilver automatically.
func VdevAttach(pool, existingDevice, newDevice string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevAttach", err.Error())
	}
	for _, d := range []string{existingDevice, newDevice} {
		if err := security.ValidateDevicePath(d); err != nil {
			return libzfsErr("VdevAttach", "invalid device "+d+": "+err.Error())
		}
	}
	out, err := cmdutil.RunMedium("zpool_attach", "attach", pool, existingDevice, newDevice)
	if err != nil {
		return libzfsErr("VdevAttach", string(out))
	}
	return nil
}

// VdevReplace replaces oldDevice with newDevice in pool. Pass force=true to
// override size-mismatch checks.
func VdevReplace(pool, oldDevice, newDevice string, force bool) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevReplace", err.Error())
	}
	for _, d := range []string{oldDevice, newDevice} {
		if err := security.ValidateDevicePath(d); err != nil {
			return libzfsErr("VdevReplace", "invalid device "+d+": "+err.Error())
		}
	}
	args := []string{"replace"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, pool, oldDevice, newDevice)
	out, err := cmdutil.RunMedium("zpool_replace", args...)
	if err != nil {
		return libzfsErr("VdevReplace", string(out))
	}
	return nil
}

// VdevRemove removes a top-level vdev (cache, log, or spare) from pool.
func VdevRemove(pool, device string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevRemove", err.Error())
	}
	if err := security.ValidateDevicePath(device); err != nil {
		return libzfsErr("VdevRemove", err.Error())
	}
	out, err := cmdutil.RunMedium("zpool_remove_device", "remove", pool, device)
	if err != nil {
		return libzfsErr("VdevRemove", string(out))
	}
	return nil
}

// PoolSplit splits a mirrored pool into two separate pools.
func PoolSplit(pool, newPool string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("PoolSplit", err.Error())
	}
	if err := security.ValidatePoolName(newPool); err != nil {
		return libzfsErr("PoolSplit", err.Error())
	}
	out, err := cmdutil.RunMedium("zpool_split", "split", pool, newPool)
	if err != nil {
		return libzfsErr("PoolSplit", string(out))
	}
	return nil
}

// PoolCreate creates a new ZFS pool. args is the full argument list for
// `zpool create` (i.e. ["create", "-f", "-o", "ashift=12", "tank", "mirror",
// "/dev/disk/by-id/...", ...]). The argument list is validated via the
// zpool_create whitelist entry before execution.
func PoolCreate(args []string) error {
	out, err := cmdutil.RunSlow("zpool_create", args...)
	if err != nil {
		return libzfsErr("PoolCreate", string(out))
	}
	return nil
}

// PoolDestroy destroys a ZFS pool.
func PoolDestroy(pool string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("PoolDestroy", err.Error())
	}
	out, err := cmdutil.RunSlow("zpool_destroy", "destroy", pool)
	if err != nil {
		return libzfsErr("PoolDestroy", string(out))
	}
	return nil
}

// DatasetRename renames a ZFS dataset. Old and new names must share the same
// pool (cross-pool rename is not supported).
func DatasetRename(oldName, newName string) error {
	if err := security.ValidateDatasetName(oldName); err != nil {
		return libzfsErr("DatasetRename", "old: "+err.Error())
	}
	if err := security.ValidateDatasetName(newName); err != nil {
		return libzfsErr("DatasetRename", "new: "+err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_rename", "rename", oldName, newName)
	if err != nil {
		return libzfsErr("DatasetRename", string(out))
	}
	return nil
}

// SnapshotListHolds lists all holds on a snapshot. Returns an empty slice if
// the snapshot has no holds.
func SnapshotListHolds(snapshot string) ([]HoldEntry, error) {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return nil, libzfsErr("SnapshotListHolds", err.Error())
	}
	out, err := cmdutil.RunFast("zfs_holds", "holds", "-H", snapshot)
	if err != nil {
		return nil, libzfsErr("SnapshotListHolds", string(out))
	}

	var holds []HoldEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 {
			holds = append(holds, HoldEntry{Tag: parts[1], Timestamp: parts[2]})
		}
	}
	return holds, nil
}

// DatasetCreateWithProps creates a ZFS filesystem dataset and sets initial
// properties. Properties are applied sequentially after creation; if setting a
// property fails, the dataset still exists with any already-applied properties.
func DatasetCreateWithProps(name string, props map[string]string) error {
	if err := DatasetCreate(name); err != nil {
		return err
	}
	for k, v := range props {
		if err := DatasetSet(name, k, v); err != nil {
			return err
		}
	}
	return nil
}
