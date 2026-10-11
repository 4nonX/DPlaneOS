package libzfs

import (
	"path/filepath"
	"strings"

	"dplaned/internal/cmdutil"
)

// poolIsMemberByStatus reports whether device (a disk or a partition, by any of its
// names) is a member of an imported pool. It compares kernel device names:
// "zpool status -L -P" prints them, and the argument's symlinks are resolved,
// so /dev/disk/by-id/... and /dev/sdb name the same disk. A whole disk is a
// member when one of its partitions is.
func poolIsMemberByStatus(device string) (PoolMembership, error) {
	out, err := cmdutil.RunFast("zpool_status", "status", "-L", "-P")
	if err != nil {
		return PoolMembership{}, libzfsErr("PoolIsMember", string(out))
	}
	real := device
	if r, err := filepath.EvalSymlinks(device); err == nil {
		real = r
	}
	m, _ := poolMembership(string(out), filepath.Base(filepath.ToSlash(real)))
	return m, nil
}

// poolMembership finds the kernel device name dev (sdb, sdb1, nvme0n1) in
// "zpool status -L -P" output.
func poolMembership(status, dev string) (PoolMembership, string) {
	var currentPool string
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "pool:" {
			currentPool = f[1]
			continue
		}
		if len(f) == 0 || !strings.HasPrefix(f[0], "/dev/") || currentPool == "" {
			continue
		}
		member := f[0][strings.LastIndex(f[0], "/")+1:]
		if member == dev || isPartitionOf(member, dev) {
			return PoolMembership{InPool: true, PoolName: currentPool}, vdevState(f)
		}
	}
	return PoolMembership{}, ""
}

func vdevState(fields []string) string {
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// MemberOf finds the kernel device name dev (sdb, nvme0n1; a whole disk
// matches through its partitions) in "zpool status -L -P" output and returns
// the pool and the vdev's state (ONLINE, FAULTED, ...), or "" when the
// device is in no imported pool.
func MemberOf(statusLP, dev string) (pool, state string) {
	m, st := poolMembership(statusLP, dev)
	return m.PoolName, st
}

// isPartitionOf: sdb1 of sdb, nvme0n1p2 of nvme0n1, mmcblk0p1 of mmcblk0.
func isPartitionOf(part, disk string) bool {
	rest, ok := strings.CutPrefix(part, disk)
	if !ok || rest == "" || disk == "" {
		return false
	}
	// A disk name ending in a digit takes a "p" before the partition number.
	if last := disk[len(disk)-1]; last >= '0' && last <= '9' {
		if rest, ok = strings.CutPrefix(rest, "p"); !ok || rest == "" {
			return false
		}
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
