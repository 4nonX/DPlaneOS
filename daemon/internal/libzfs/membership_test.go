package libzfs

import "testing"

const statusLP = `  pool: tank
 state: ONLINE
config:

	NAME               STATE     READ WRITE CKSUM
	tank               ONLINE       0     0     0
	  mirror-0         ONLINE       0     0     0
	    /dev/sdb1      ONLINE       0     0     0
	    /dev/nvme0n1p1 ONLINE       0     0     0

errors: No known data errors

  pool: backup
 state: ONLINE
config:

	NAME        STATE     READ WRITE CKSUM
	backup      ONLINE       0     0     0
	  /dev/sdaa1  ONLINE       0     0     0
`

func TestPoolMembership(t *testing.T) {
	for dev, want := range map[string]string{
		"sdb":       "tank", // whole disk: its partition is the member
		"sdb1":      "tank", // the partition itself
		"nvme0n1":   "tank", // NVMe: partition suffix p1
		"nvme0n1p1": "tank",
		"sdaa":      "backup", // not confused with sda
		"sda":       "",
		"sdb2":      "",
		"sdc":       "",
		"nvme0n2":   "",
		"nvme0n":    "",
		"tank":      "", // the pool's own row is not a device
	} {
		m := poolMembership(statusLP, dev)
		if m.PoolName != want || m.InPool != (want != "") {
			t.Errorf("%s: got %+v, want pool %q", dev, m, want)
		}
	}
}

func TestIsPartitionOf(t *testing.T) {
	for _, c := range []struct {
		part, disk string
		want       bool
	}{
		{"sdb1", "sdb", true}, {"sdb12", "sdb", true}, {"sdb", "sdb", false}, {"sdba1", "sdb", false},
		{"nvme0n1p3", "nvme0n1", true}, {"nvme0n11", "nvme0n1", false}, {"nvme0n1p", "nvme0n1", false},
		{"mmcblk0p1", "mmcblk0", true}, {"sda1", "", false},
	} {
		if got := isPartitionOf(c.part, c.disk); got != c.want {
			t.Errorf("isPartitionOf(%q, %q) = %v", c.part, c.disk, got)
		}
	}
}
