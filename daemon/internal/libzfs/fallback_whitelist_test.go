//go:build !linux || !cgo || !libzfs

package libzfs

import (
	"testing"

	"dplaned/internal/cmdutil"
)

// Every operation of the cgo-free build (the one NixOS ships) must get its
// command past the security whitelist with ordinary arguments. DatasetGet
// did not: zfs_get allowed one argument and it passes two.
func TestFallbackCommandsPassWhitelist(t *testing.T) {
	ran := 0
	restore := cmdutil.SetFakeForTest(func(c cmdutil.Call) ([]byte, error) {
		ran++
		if c.Key == "zpool_status" {
			return []byte("  pool: tank\n config:\n\ttank\n\t  /dev/sdb1\n"), nil
		}
		return []byte("value\n"), nil
	})
	defer restore()

	ops := map[string]func() error{
		"PoolExport":      func() error { return PoolExport("tank", true) },
		"DatasetCreate":   func() error { return DatasetCreate("tank/data") },
		"DatasetGet":      func() error { _, err := DatasetGet("tank/data", "acltype"); return err },
		"DatasetGetUser":  func() error { _, err := DatasetGet("tank/data", "com.sun:auto-snapshot"); return err },
		"DatasetSet":      func() error { return DatasetSet("tank/data", "acltype", "posix") },
		"DatasetSetQuota": func() error { return DatasetSet("tank/data", "refquota", "10G") },
		"DatasetPromote":  func() error { return DatasetPromote("tank/clone") },
		"VdevDetach":      func() error { return VdevDetach("tank", "/dev/sdb") },
		"VdevOnline":      func() error { return VdevOnline("tank", "/dev/sdb") },
		"VdevOffline":     func() error { return VdevOffline("tank", "/dev/sdb", true) },
		"PoolClear":       func() error { return PoolClear("tank") },
		"PoolSetProperty": func() error { return PoolSetProperty("tank", "autoexpand", "on") },
		"DatasetDestroy":  func() error { return DatasetDestroy("tank/data", true) },
		"SnapshotHold":    func() error { return SnapshotHold("keep", "tank/data@s1") },
		"SnapshotRelease": func() error { return SnapshotRelease("keep", "tank/data@s1") },
		"SnapshotCreate":  func() error { return SnapshotCreate("tank/data@s1") },
		"SnapshotDestroy": func() error { return SnapshotDestroy("tank/data@s1") },
		"SnapshotClone":   func() error { return SnapshotClone("tank/data@s1", "tank/clone") },
		// Snapshots of a pool's root dataset.
		"SnapshotCreateRoot":  func() error { return SnapshotCreate("tank@auto-daily-20261018-0000") },
		"SnapshotDestroyRoot": func() error { return SnapshotDestroy("tank@auto-daily-20261018-0000") },
		"SnapshotHoldRoot":    func() error { return SnapshotHold("keep", "tank@s1") },
		"DatasetRename":       func() error { return DatasetRename("tank/a", "tank/b") },
		"VdevRemove":          func() error { return VdevRemove("tank", "/dev/sdb") },
		"PoolSplit":           func() error { return PoolSplit("tank", "tank2") },
		"PoolDestroy":         func() error { return PoolDestroy("tank") },
	}
	for name, op := range ops {
		before := ran
		err := op()
		if ran == before {
			t.Errorf("%s: command refused before it ran: %v", name, err)
		}
	}
}
