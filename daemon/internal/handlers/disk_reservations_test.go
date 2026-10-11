package handlers

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dplaned/internal/diskres"
)

func TestSummarizeReservation(t *testing.T) {
	disk := func(state, holderNode string) reservedDisk {
		d := reservedDisk{Disk: diskres.Disk{Device: "/dev/sdx", State: state}, HolderNode: holderNode}
		if holderNode != "" {
			d.Holder = "00000000000000aa"
		}
		return d
	}
	for _, c := range []struct {
		name  string
		mine  bool
		disks []reservedDisk
		state string
		says  string
	}{
		{"the owner holds every disk", true, []reservedDisk{disk(diskres.Held, "this node"), disk(diskres.Held, "this node")}, "reserved", "refuse writes from any other node"},
		{"seen from the other node", false, []reservedDisk{disk(diskres.Foreign, "nas-a"), disk(diskres.Foreign, "nas-a")}, "reserved", "reserved for nas-a"},
		{"the owner was shut out", true, []reservedDisk{disk(diskres.Foreign, "nas-b"), disk(diskres.Foreign, "nas-b")}, "shut_out", "nas-b holds the disks"},
		{"disks without reservations", true, []reservedDisk{disk(diskres.Unsupported, ""), disk(diskres.Unsupported, "")}, "unsupported", "watchdog"},
		{"one disk not reserved yet", true, []reservedDisk{disk(diskres.Held, "this node"), disk(diskres.Unclaimed, "")}, "partial", "1 held, 1 unclaimed"},
		{"nobody holds them", false, []reservedDisk{disk(diskres.Unclaimed, ""), disk(diskres.Unclaimed, "")}, "unreserved", "none is held"},
		{"no disks here", false, nil, "unreserved", "No disks"},
	} {
		state, detail := summarizeReservation(c.mine, c.disks)
		if state != c.state || !strings.Contains(detail, c.says) {
			t.Errorf("%s: %s %q", c.name, state, detail)
		}
	}
}

func TestPoolMemberDevices(t *testing.T) {
	status := `  pool: tank
 state: ONLINE
config:

	NAME                                      STATE     READ WRITE CKSUM
	tank                                      ONLINE       0     0     0
	  mirror-0                                ONLINE       0     0     0
	    /dev/sdb1                             ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0x6001405abc-part1  ONLINE       0     0     0
	logs
	  /dev/nvme0n1p1                          ONLINE       0     0     0

errors: No known data errors
`
	want := []string{"/dev/sdb1", "/dev/disk/by-id/wwn-0x6001405abc-part1", "/dev/nvme0n1p1"}
	if got := poolMemberDevices(status); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

// ZFS puts its data on partition 1 of a whole disk; reservations are a
// matter of the disk.
func TestWholeDisk(t *testing.T) {
	root := t.TempDir()
	dev, sys, class := filepath.Join(root, "dev"), filepath.Join(root, "sys"), filepath.Join(root, "class")
	mk := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch := func(p string) {
		mk(filepath.Dir(p))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		mk(filepath.Dir(name))
		if err := os.Symlink(target, name); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
	}
	// A SCSI disk with a partition, an NVMe disk with one.
	mk(filepath.Join(sys, "sdb", "device", "generic"))
	touch(filepath.Join(sys, "sdb", "sdb1", "partition"))
	mk(filepath.Join(sys, "nvme0n1", "device"))
	touch(filepath.Join(sys, "nvme0n1", "nvme0n1p1", "partition"))
	link(filepath.Join(sys, "sdb"), filepath.Join(class, "sdb"))
	link(filepath.Join(sys, "sdb", "sdb1"), filepath.Join(class, "sdb1"))
	link(filepath.Join(sys, "nvme0n1"), filepath.Join(class, "nvme0n1"))
	link(filepath.Join(sys, "nvme0n1", "nvme0n1p1"), filepath.Join(class, "nvme0n1p1"))
	for _, n := range []string{"sdb", "sdb1", "nvme0n1p1"} {
		touch(filepath.Join(dev, n))
	}
	link(filepath.Join(dev, "sdb1"), filepath.Join(dev, "by-id", "wwn-0x6001405abc-part1"))

	for _, c := range []struct {
		in, disk string
		scsi     bool
	}{
		{filepath.Join(dev, "sdb1"), "/dev/sdb", true},
		{filepath.Join(dev, "sdb"), "/dev/sdb", true},
		{filepath.Join(dev, "by-id", "wwn-0x6001405abc-part1"), "/dev/sdb", true},
		{filepath.Join(dev, "nvme0n1p1"), "/dev/nvme0n1", false},
	} {
		disk, scsi, err := wholeDisk(class, c.in)
		if err != nil || disk != c.disk || scsi != c.scsi {
			t.Errorf("%s: %s scsi=%v %v", c.in, disk, scsi, err)
		}
	}
	if _, _, err := wholeDisk(class, filepath.Join(dev, "gone")); err == nil {
		t.Error("a device that does not exist was accepted")
	}
}
