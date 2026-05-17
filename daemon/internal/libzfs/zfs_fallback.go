//go:build !linux || !cgo || !libzfs

package libzfs

// Fallback implementations using cmdutil subprocess calls.
// Active when CGO is disabled (static musl build, dev machines, CI).
// All functions match the signatures in zfs_cgo.go exactly.

import (
	"fmt"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// PoolIsMember reports whether device is a member of any active ZFS pool.
// Uses `zpool status -P` and scans the output for the device path.
// The cgo variant does this via a direct vdev-tree nvlist walk.
func PoolIsMember(device string) (PoolMembership, error) {
	out, err := cmdutil.RunFast("zpool", "status", "-P")
	if err != nil {
		return PoolMembership{}, libzfsErr("PoolIsMember", string(out))
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

// PoolImportAll forces import of all available pools from the given search
// path. Mirrors `zpool import -a -f -d <path>`.
func PoolImportAll(searchPath string) error {
	// Whitelist entry "zpool_import_all" hardcodes /dev/disk/by-id.
	// Accept any path in the fallback but validate it is reasonable.
	if searchPath == "" {
		searchPath = "/dev/disk/by-id"
	}
	if err := security.ValidateDevicePath(searchPath); err != nil {
		// ValidateDevicePath rejects non-/dev paths; call directly for dirs.
		if !strings.HasPrefix(searchPath, "/dev/") {
			return libzfsErr("PoolImportAll", fmt.Sprintf("invalid search path: %s", searchPath))
		}
	}
	out, err := cmdutil.RunSlow("zpool_import_all", "import", "-a", "-f", "-d", "/dev/disk/by-id")
	if err != nil {
		return libzfsErr("PoolImportAll", string(out))
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
		return libzfsErr("PoolExport", string(out))
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
		return libzfsErr("DatasetCreate", string(out))
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
		return "", libzfsErr("DatasetGet", string(out))
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
		return libzfsErr("DatasetSet", string(out))
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
		return libzfsErr("DatasetPromote", string(out))
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
		return libzfsErr("VdevDetach", string(out))
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
		return libzfsErr("VdevOnline", string(out))
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
		return libzfsErr("VdevOffline", string(out))
	}
	return nil
}
