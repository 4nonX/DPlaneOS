package diskres

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"dplaned/internal/scsipr"
)

// fakeDisk behaves like a disk with persistent reservations of the type
// "write exclusive, registrants only", as far as these commands go.
type fakeDisk struct {
	keys      map[string]scsipr.RegistrationKey // node → registered key
	holder    string                            // node holding the reservation
	supported bool
	broken    bool
}

type fakeSAN struct{ disks map[string]*fakeDisk }

var errConflict = &scsipr.SCSIError{Op: "test", Status: 0x18}
var errIllegal = &scsipr.SCSIError{Op: "test", Status: 0x02, Key: 0x05, ASC: 0x20}

func (s *fakeSAN) disk(dev string) (*fakeDisk, error) {
	d := s.disks[dev]
	switch {
	case d == nil:
		return nil, errors.New("no such device")
	case d.broken:
		return nil, errors.New("input/output error")
	case !d.supported:
		return nil, errIllegal
	}
	return d, nil
}

// backend returns the commands as node issues them.
func (s *fakeSAN) backend(node string) Backend {
	return Backend{
		Register: func(dev string, key scsipr.RegistrationKey) error {
			d, err := s.disk(dev)
			if err != nil {
				return err
			}
			d.keys[node] = key
			return nil
		},
		Unregister: func(dev string, key scsipr.RegistrationKey) error {
			d, err := s.disk(dev)
			if err != nil {
				return err
			}
			if d.keys[node] != key {
				return errConflict
			}
			delete(d.keys, node)
			if d.holder == node {
				d.holder = ""
			}
			return nil
		},
		Reserve: func(dev string, key scsipr.RegistrationKey) error {
			d, err := s.disk(dev)
			if err != nil {
				return err
			}
			if k, ok := d.keys[node]; !ok || k != key || (d.holder != "" && d.holder != node) {
				return errConflict
			}
			d.holder = node
			return nil
		},
		Preempt: func(dev string, ours, victim scsipr.RegistrationKey) error {
			d, err := s.disk(dev)
			if err != nil {
				return err
			}
			if k, ok := d.keys[node]; !ok || k != ours {
				return errConflict
			}
			for n, k := range d.keys {
				if k == victim && n != node {
					delete(d.keys, n)
					if d.holder == n {
						d.holder = node
					}
				}
			}
			return nil
		},
		ReadKeys: func(dev string) (scsipr.PRStatus, error) {
			d, err := s.disk(dev)
			if err != nil {
				return scsipr.PRStatus{}, err
			}
			var st scsipr.PRStatus
			for _, k := range d.keys {
				st.Keys = append(st.Keys, k)
			}
			if d.holder != "" {
				st.Reserved, st.HolderKey = true, d.keys[d.holder]
			}
			return st, nil
		},
	}
}

// mayWrite is what the disk decides for a write from node.
func (s *fakeSAN) mayWrite(dev, node string) bool {
	d := s.disks[dev]
	if d.holder == "" {
		return true
	}
	_, registered := d.keys[node]
	return registered
}

func newSAN(devs ...string) *fakeSAN {
	s := &fakeSAN{disks: map[string]*fakeDisk{}}
	for _, d := range devs {
		s.disks[d] = &fakeDisk{keys: map[string]scsipr.RegistrationKey{}, supported: true}
	}
	return s
}

func (s *fakeSAN) node(name string, tolerance int) *Reserver {
	return &Reserver{
		Key:     scsipr.KeyFor(name),
		Backend: s.backend(name),
		Find: func(string) ([]string, error) {
			var out []string
			for d := range s.disks {
				out = append(out, d)
			}
			return out, nil
		},
		Tolerance: func() int { return tolerance },
	}
}

func TestOwnerHoldsTheDisksAlone(t *testing.T) {
	san := newSAN("/dev/sda", "/dev/sdb")
	a, b := san.node("a", 0), san.node("b", 0)

	rep, err := a.Acquire("1")
	if err != nil || rep.Count(Held) != 2 || !rep.Active() {
		t.Fatalf("acquire: %+v %v", rep, err)
	}
	for dev := range san.disks {
		if !san.mayWrite(dev, "a") || san.mayWrite(dev, "b") {
			t.Errorf("%s: a must be the only writer", dev)
		}
	}
	// The other node sees disks it has no part in, and its checks change nothing.
	if rep, _ := b.Status("1"); rep.Count(Foreign) != 2 {
		t.Errorf("b sees %+v", rep)
	}
	if _, lost, _ := a.Verify("1"); lost {
		t.Error("the owner believes it was shut out")
	}

	// Planned move: a releases, b takes.
	if err := a.Release("1"); err != nil {
		t.Fatal(err)
	}
	if rep, _ := a.Status("1"); rep.Count(Unclaimed) != 2 {
		t.Errorf("after release: %+v", rep)
	}
	if rep, err := b.Acquire("1"); err != nil || rep.Count(Held) != 2 {
		t.Fatalf("b acquire: %+v %v", rep, err)
	}
	if san.mayWrite("/dev/sda", "a") {
		t.Error("a can still write after the move")
	}
}

// The reason all this exists: an owner that is hung or cut off loses the
// disks to the node that takes over, and cannot get them back by itself.
func TestTakeoverShutsThePreviousOwnerOut(t *testing.T) {
	san := newSAN("/dev/sda", "/dev/sdb", "/dev/sdc")
	a, b := san.node("a", 0), san.node("b", 0)
	if _, err := a.Acquire("1"); err != nil {
		t.Fatal(err)
	}

	if rep, err := b.Acquire("1"); err != nil || rep.Count(Held) != 3 {
		t.Fatalf("takeover: %+v %v", rep, err)
	}
	for dev := range san.disks {
		if san.mayWrite(dev, "a") || !san.mayWrite(dev, "b") {
			t.Errorf("%s: the previous owner can still write", dev)
		}
	}

	// The previous owner notices, and its periodic check takes nothing back.
	rep, lost, err := a.Verify("1")
	if err != nil || !lost || rep.Count(Foreign) != 3 {
		t.Fatalf("a after the takeover: %+v lost=%v %v", rep, lost, err)
	}
	for dev := range san.disks {
		if san.mayWrite(dev, "a") {
			t.Errorf("%s: the check gave the previous owner its access back", dev)
		}
	}
	// Releasing as the shut-out node does not touch the new owner either.
	if err := a.Release("1"); err != nil {
		t.Fatal(err)
	}
	if rep, _ := b.Status("1"); rep.Count(Held) != 3 {
		t.Errorf("b after the release by a: %+v", rep)
	}
}

func TestDisksWithoutReservations(t *testing.T) {
	san := newSAN("/dev/vda", "/dev/vdb")
	for _, d := range san.disks {
		d.supported = false
	}
	a := san.node("a", 0)
	rep, err := a.Acquire("1")
	if err != nil || rep.Active() || rep.Count(Unsupported) != 2 {
		t.Fatalf("disks without reservations must not stop an import: %+v %v", rep, err)
	}
	if _, lost, err := a.Verify("1"); lost || err != nil {
		t.Errorf("verify: lost=%v %v", lost, err)
	}
	if err := a.Release("1"); err != nil {
		t.Errorf("release: %v", err)
	}
}

func TestFailuresAndTolerance(t *testing.T) {
	devs := []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf", "/dev/sdg", "/dev/sdh", "/dev/sdi", "/dev/sdj"}
	san := newSAN(devs...)
	san.disks["/dev/sdj"].broken = true

	// One of ten fails: refused without tolerance, accepted with 10%.
	if _, err := san.node("a", 0).Acquire("1"); err == nil || !strings.Contains(err.Error(), "/dev/sdj") {
		t.Errorf("no tolerance: %v", err)
	}
	rep, err := san.node("a", 10).Acquire("1")
	if err != nil || rep.Count(Held) != 9 || rep.Count(Failed) != 1 {
		t.Errorf("10%% tolerance: %+v %v", rep, err)
	}
	san.disks["/dev/sdi"].broken = true
	if _, err := san.node("a", 10).Acquire("1"); err == nil {
		t.Error("two of ten failed disks accepted with a tolerance of 10%")
	}
}

// A disk added to a pool is reserved by the periodic check of the owner.
func TestNewDiskIsReserved(t *testing.T) {
	san := newSAN("/dev/sda")
	a := san.node("a", 0)
	if _, err := a.Acquire("1"); err != nil {
		t.Fatal(err)
	}
	san.disks["/dev/sdb"] = &fakeDisk{keys: map[string]scsipr.RegistrationKey{}, supported: true}
	rep, lost, err := a.Verify("1")
	if err != nil || lost || rep.Count(Held) != 2 {
		t.Fatalf("%+v lost=%v %v", rep, lost, err)
	}
}

func TestParseLsblk(t *testing.T) {
	out := `NAME="/dev/sda" PKNAME="" TYPE="disk" FSTYPE="" UUID=""
NAME="/dev/sda1" PKNAME="/dev/sda" TYPE="part" FSTYPE="zfs_member" UUID="1234567890"
NAME="/dev/sda9" PKNAME="/dev/sda" TYPE="part" FSTYPE="" UUID=""
NAME="/dev/sdb" PKNAME="" TYPE="disk" FSTYPE="zfs_member" UUID="1234567890"
NAME="/dev/sdc1" PKNAME="/dev/sdc" TYPE="part" FSTYPE="zfs_member" UUID="999"
NAME="/dev/dm-0" PKNAME="" TYPE="mpath" FSTYPE="zfs_member" UUID="1234567890"
NAME="/dev/nvme0n1p1" PKNAME="/dev/nvme0n1" TYPE="part" FSTYPE="zfs_member" UUID="1234567890"
NAME="/dev/sdd2" PKNAME="/dev/sdd" TYPE="part" FSTYPE="ext4" UUID="1234567890"`
	got := parseLsblk(out, "1234567890")
	want := []string{"/dev/sda", "/dev/sdb", "/dev/nvme0n1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
