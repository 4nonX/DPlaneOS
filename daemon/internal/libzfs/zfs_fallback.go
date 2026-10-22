//go:build !linux || !cgo || !libzfs

package libzfs

// Fallback implementations using cmdutil subprocess calls.
// Active when CGO is disabled (static musl build, dev machines, CI).
// All functions match the signatures in zfs_cgo.go exactly.

import (
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// PoolIsMember reports whether device is a member of any active ZFS pool.
// Uses `zpool status -P` and scans the output for the device path.
// The cgo variant does this via a direct vdev-tree nvlist walk.
func PoolIsMember(device string) (PoolMembership, error) {
	out, err := cmdutil.RunFast("zpool_status", "status", "-P")
	if err != nil {
		return PoolMembership{}, cmdErr("PoolIsMember", out, err)
	}
	status := string(out)
	bare := strings.TrimPrefix(device, "/dev/")

	var currentPool string
	for _, line := range strings.Split(status, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			continue
		}
		if strings.Contains(trimmed, device) || strings.Contains(trimmed, bare) {
			if currentPool != "" {
				return PoolMembership{InPool: true, PoolName: currentPool}, nil
			}
		}
	}
	return PoolMembership{InPool: false}, nil
}

// PoolImportAll imports all importable pools from /dev/disk/by-id.
// The path is hardcoded by design: the security whitelist entry "zpool_import_all"
// permits only this path. By-id paths are stable across reboots, bus re-enumeration,
// and kernel updates, and cannot be redirected via API call.
func PoolImportAll() error {
	out, err := cmdutil.RunSlow("zpool_import_all", "import", "-a", "-f", "-d", "/dev/disk/by-id")
	if err != nil {
		return cmdErr("PoolImportAll", out, err)
	}
	return nil
}

// PoolExport exports a ZFS pool, optionally with -f to force.
func PoolExport(name string, force bool) error {
	if err := security.ValidatePoolName(name); err != nil {
		return libzfsErr("PoolExport", err.Error())
	}
	args := []string{"export"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	out, err := cmdutil.RunMedium("zpool_export", args...)
	if err != nil {
		return cmdErr("PoolExport", out, err)
	}
	return nil
}

// DatasetCreate creates a ZFS filesystem dataset.
func DatasetCreate(name string) error {
	if err := security.ValidateDatasetName(name); err != nil {
		return libzfsErr("DatasetCreate", err.Error())
	}
	out, err := cmdutil.RunZFS("zfs_create", "create", name)
	if err != nil {
		return cmdErr("DatasetCreate", out, err)
	}
	return nil
}

// DatasetGet returns the value of a ZFS property on a dataset.
func DatasetGet(dataset, prop string) (string, error) {
	if err := security.ValidateDatasetName(dataset); err != nil {
		return "", libzfsErr("DatasetGet", err.Error())
	}
	out, err := cmdutil.RunFast("zfs_get", "get", "-H", "-o", "value", prop, dataset)
	if err != nil {
		return "", cmdErr("DatasetGet", out, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DatasetSet sets a ZFS property on a dataset.
func DatasetSet(dataset, prop, value string) error {
	if err := security.ValidateDatasetName(dataset); err != nil {
		return libzfsErr("DatasetSet", err.Error())
	}
	kv := prop + "=" + value
	out, err := cmdutil.RunMedium("zfs_set_property", "set", kv, dataset)
	if err != nil {
		return cmdErr("DatasetSet", out, err)
	}
	return nil
}

// DatasetPromote promotes a ZFS clone to a full dataset.
func DatasetPromote(dataset string) error {
	if err := security.ValidateDatasetName(dataset); err != nil {
		return libzfsErr("DatasetPromote", err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_promote", "promote", dataset)
	if err != nil {
		return cmdErr("DatasetPromote", out, err)
	}
	return nil
}

// VdevDetach detaches a device from a ZFS mirror.
func VdevDetach(pool, device string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevDetach", err.Error())
	}
	if err := security.ValidateDevicePath(device); err != nil {
		return libzfsErr("VdevDetach", err.Error())
	}
	out, err := cmdutil.RunMedium("zpool_detach", "detach", pool, device)
	if err != nil {
		return cmdErr("VdevDetach", out, err)
	}
	return nil
}

// VdevOnline brings a ZFS pool device online.
func VdevOnline(pool, device string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevOnline", err.Error())
	}
	if err := security.ValidateDevicePath(device); err != nil {
		return libzfsErr("VdevOnline", err.Error())
	}
	out, err := cmdutil.RunMedium("zpool_online", "online", pool, device)
	if err != nil {
		return cmdErr("VdevOnline", out, err)
	}
	return nil
}

// VdevOffline takes a ZFS pool device offline.
// If temporary is true, the device returns online after the next pool export/import.
func VdevOffline(pool, device string, temporary bool) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("VdevOffline", err.Error())
	}
	if err := security.ValidateDevicePath(device); err != nil {
		return libzfsErr("VdevOffline", err.Error())
	}
	args := []string{"offline"}
	if temporary {
		args = append(args, "-t")
	}
	args = append(args, pool, device)
	out, err := cmdutil.RunMedium("zpool_offline", args...)
	if err != nil {
		return cmdErr("VdevOffline", out, err)
	}
	return nil
}

// PoolClear clears error counters on a pool device.
func PoolClear(pool string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("PoolClear", err.Error())
	}
	out, err := cmdutil.RunMedium("zpool_clear", "clear", pool)
	if err != nil {
		return cmdErr("PoolClear", out, err)
	}
	return nil
}

// PoolSetProperty sets a pool-level property.
func PoolSetProperty(pool, key, value string) error {
	if err := security.ValidatePoolName(pool); err != nil {
		return libzfsErr("PoolSetProperty", err.Error())
	}
	out, err := cmdutil.RunMedium("zpool_set_property", "set", key+"="+value, pool)
	if err != nil {
		return cmdErr("PoolSetProperty", out, err)
	}
	return nil
}

// DatasetDestroy destroys a ZFS dataset. Pass recursive=true to destroy all
// child datasets and snapshots (use with extreme caution).
func DatasetDestroy(name string, recursive bool) error {
	if err := security.ValidateDatasetName(name); err != nil {
		return libzfsErr("DatasetDestroy", err.Error())
	}
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, name)
	out, err := cmdutil.RunMedium("zfs_destroy", args...)
	if err != nil {
		return cmdErr("DatasetDestroy", out, err)
	}
	return nil
}

// SnapshotHold adds a user hold on a snapshot to prevent it from being deleted.
func SnapshotHold(tag, snapshot string) error {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return libzfsErr("SnapshotHold", err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_hold", "hold", tag, snapshot)
	if err != nil {
		return cmdErr("SnapshotHold", out, err)
	}
	return nil
}

// SnapshotRelease removes a user hold from a snapshot.
func SnapshotRelease(tag, snapshot string) error {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return libzfsErr("SnapshotRelease", err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_release", "release", tag, snapshot)
	if err != nil {
		return cmdErr("SnapshotRelease", out, err)
	}
	return nil
}

// SnapshotCreate creates a ZFS snapshot.
func SnapshotCreate(name string) error {
	if err := security.ValidateSnapshotName(name); err != nil {
		return libzfsErr("SnapshotCreate", err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_snapshot", "snapshot", name)
	if err != nil {
		return cmdErr("SnapshotCreate", out, err)
	}
	return nil
}

// SnapshotDestroy destroys a ZFS snapshot.
func SnapshotDestroy(name string) error {
	if err := security.ValidateSnapshotName(name); err != nil {
		return libzfsErr("SnapshotDestroy", err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_destroy_snapshot", "destroy", name)
	if err != nil {
		return cmdErr("SnapshotDestroy", out, err)
	}
	return nil
}

// SnapshotClone clones a ZFS snapshot into a new dataset.
func SnapshotClone(snapshot, clone string) error {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return libzfsErr("SnapshotClone", err.Error())
	}
	if err := security.ValidateDatasetName(clone); err != nil {
		return libzfsErr("SnapshotClone", err.Error())
	}
	out, err := cmdutil.RunMedium("zfs_clone", "clone", snapshot, clone)
	if err != nil {
		return cmdErr("SnapshotClone", out, err)
	}
	return nil
}
