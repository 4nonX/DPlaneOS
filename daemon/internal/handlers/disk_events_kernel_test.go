package handlers

import (
	"sync"
	"testing"
	"time"

	"dplaned/internal/uevent"
)

func blockEvent(action, name, devType, devPath string) uevent.Event {
	return uevent.Event{Action: action, Subsystem: "block", DevType: devType, DevName: name, DevPath: devPath}
}

func TestDiskEventOf(t *testing.T) {
	for _, c := range []struct {
		ev         uevent.Event
		action, ty string
	}{
		{blockEvent("add", "sdb", "disk", "/devices/pci0000:00/ata2/host1/block/sdb"), "added", "sata"},
		{blockEvent("remove", "sdaa", "disk", "/devices/x/block/sdaa"), "removed", "sata"},
		{blockEvent("add", "nvme1n1", "disk", "/devices/pci0000:00/nvme/nvme1/nvme1n1"), "added", "nvme"},
		{blockEvent("add", "sdc", "disk", "/devices/pci0000:00/usb2/2-1/host6/block/sdc"), "added", "usb"},
		{blockEvent("add", "vdb", "disk", "/devices/pci0000:00/virtio3/block/vdb"), "added", "virtual"},
	} {
		req, ok := diskEventOf(c.ev)
		if !ok || req.Action != c.action || req.DeviceType != c.ty || req.Device != "/dev/"+c.ev.DevName {
			t.Errorf("%+v: %+v %v", c.ev, req, ok)
		}
	}
	for name, ev := range map[string]uevent.Event{
		"partition":     blockEvent("add", "sdb1", "partition", "/devices/x/block/sdb/sdb1"),
		"zvol":          blockEvent("add", "zd16", "disk", "/devices/virtual/block/zd16"),
		"loop":          blockEvent("add", "loop0", "disk", "/devices/virtual/block/loop0"),
		"device mapper": blockEvent("add", "dm-0", "disk", "/devices/virtual/block/dm-0"),
		"optical":       blockEvent("add", "sr0", "disk", "/devices/x/block/sr0"),
		"change event":  blockEvent("change", "sdb", "disk", "/devices/x/block/sdb"),
		"network":       {Action: "add", Subsystem: "net", DevPath: "/devices/x/net/eth1"},
		"nvme ctrl":     blockEvent("add", "nvme0", "disk", "/devices/x/nvme/nvme0"),
	} {
		if req, ok := diskEventOf(ev); ok {
			t.Errorf("%s must not be a disk event: %+v", name, req)
		}
	}
}

func TestDiskEventsDispatch(t *testing.T) {
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 8)
	record := func(what string) {
		mu.Lock()
		got = append(got, what)
		mu.Unlock()
		done <- struct{}{}
	}
	release := make(chan struct{})
	prevS, prevA, prevR, prevM := diskSettled, onDiskAdded, onDiskRemoved, onEventsMissed
	t.Cleanup(func() { diskSettled, onDiskAdded, onDiskRemoved, onEventsMissed = prevS, prevA, prevR, prevM })
	diskSettled = func(name string) {
		if name == "sdc" {
			<-release // sdc takes long to settle
		}
	}
	onDiskAdded = func(name string, req diskEventRequest) { record("added " + name + " " + req.DeviceType) }
	onDiskRemoved = func(name string, req diskEventRequest) { record("removed " + name) }
	onEventsMissed = func() { record("missed") }

	d := &diskEvents{}
	wait := func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("event not handled")
		}
	}

	d.handle(blockEvent("add", "sdb", "disk", "/devices/x/block/sdb"))
	wait()
	d.handle(blockEvent("add", "sdb1", "partition", "/devices/x/block/sdb/sdb1")) // ignored
	d.handle(blockEvent("remove", "sdb", "disk", "/devices/x/block/sdb"))
	wait()

	// A disk that is pulled again before it settled is not reported as added.
	d.handle(blockEvent("add", "sdc", "disk", "/devices/x/block/sdc"))
	d.handle(blockEvent("remove", "sdc", "disk", "/devices/x/block/sdc"))
	wait()
	close(release)

	d.handle(uevent.Event{Action: "overflow"})
	wait()

	time.Sleep(100 * time.Millisecond) // a stale "added sdc" would arrive now
	mu.Lock()
	defer mu.Unlock()
	want := []string{"added sdb sata", "removed sdb", "removed sdc", "missed"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
