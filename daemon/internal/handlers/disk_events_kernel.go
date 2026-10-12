package handlers

import (
	"context"
	"errors"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/uevent"
)

// Disk hot-plug: the daemon reads the kernel's device events itself
// (internal/uevent) and runs handleDiskAdded / handleDiskRemoved. No udev
// rule, helper script or HTTP endpoint is involved.

// Whole disks that can hold pool data. Partitions, zvols (zd*), loop,
// device-mapper, md and optical drives are not disk hot-plug events.
var wholeDiskRe = regexp.MustCompile(`^(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+|xvd[a-z]+)$`)

// diskEventOf maps a kernel event to a disk event, or reports false.
func diskEventOf(ev uevent.Event) (diskEventRequest, bool) {
	if ev.Subsystem != "block" || ev.DevType != "disk" || !wholeDiskRe.MatchString(ev.DevName) {
		return diskEventRequest{}, false
	}
	req := diskEventRequest{Device: "/dev/" + ev.DevName}
	switch ev.Action {
	case "add":
		req.Action = "added"
	case "remove":
		req.Action = "removed"
	default:
		return diskEventRequest{}, false
	}
	switch {
	case strings.HasPrefix(ev.DevName, "nvme"):
		req.DeviceType = "nvme"
	case strings.Contains(ev.DevPath, "/usb"):
		req.DeviceType = "usb"
	case strings.HasPrefix(ev.DevName, "vd"), strings.HasPrefix(ev.DevName, "xvd"):
		req.DeviceType = "virtual"
	default:
		req.DeviceType = "sata"
	}
	return req, true
}

// Replaced in tests.
var (
	diskSettled    = waitForDiskSettled
	onDiskAdded    = handleDiskAdded
	onDiskRemoved  = handleDiskRemoved
	onEventsMissed = func() { broadcastPoolHealthChanged() }
)

// diskEvents serialises the events of one disk: an add that is still waiting
// for the disk to settle is dropped when the disk is removed again.
type diskEvents struct {
	mu  sync.Mutex
	gen map[string]int
}

func (d *diskEvents) handle(ev uevent.Event) {
	if ev.Action == "overflow" {
		// The kernel dropped events: at least refresh what the UI shows.
		missed := onEventsMissed
		go missed()
		return
	}
	req, ok := diskEventOf(ev)
	if !ok {
		return
	}
	name := strings.TrimPrefix(req.Device, "/dev/")
	d.mu.Lock()
	if d.gen == nil {
		d.gen = map[string]int{}
	}
	d.gen[name]++
	gen := d.gen[name]
	d.mu.Unlock()

	// The handlers as they are now: the goroutine below may still be waiting
	// for a disk to settle when a test puts the real ones back.
	settled, added, removed := diskSettled, onDiskAdded, onDiskRemoved
	go func() {
		if req.Action == "removed" {
			removed(name, req)
			return
		}
		// The kernel announces the disk before its size is read and before
		// udev has created the /dev/disk/by-id links.
		settled(name)
		d.mu.Lock()
		current := d.gen[name] == gen
		d.mu.Unlock()
		if !current {
			return // removed (or re-added) while waiting
		}
		added(name, req)
	}()
}

// waitForDiskSettled waits until the disk reports a size and has a by-id
// link, at most about 15 seconds.
func waitForDiskSettled(name string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile("/sys/block/" + name + "/size")
		if err == nil {
			if n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); n > 0 && findByIDPath(name) != "" {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// StartDiskEventListener listens for disks being added and removed until ctx
// ends. A failing listener is restarted.
func StartDiskEventListener(ctx context.Context) {
	d := &diskEvents{}
	go func() {
		for ctx.Err() == nil {
			err := uevent.Listen(ctx, d.handle)
			if errors.Is(err, uevent.ErrUnsupported) {
				log.Printf("DISK EVENT: %v; disk hot-plug detection is off", err)
				return
			}
			if err != nil {
				log.Printf("DISK EVENT: listener failed, restarting in 5s: %v", err)
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()
	log.Printf("DISK EVENT: listening for disk hot-plug events from the kernel")
}
