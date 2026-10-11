// Package uevent receives the kernel's device events (the stream udev reads)
// directly over netlink, so the daemon notices disks being added and removed
// without udev rules, helper scripts or an HTTP endpoint.
package uevent

import (
	"errors"
	"strings"
)

// ErrUnsupported: this platform has no kernel uevents (anything but Linux).
var ErrUnsupported = errors.New("kernel device events are only available on Linux")

// Event is one kernel uevent.
type Event struct {
	Action    string // add, remove, change, ...
	DevPath   string // /devices/pci0000:00/.../block/sdb
	Subsystem string // block
	DevType   string // disk, partition
	DevName   string // sdb
	Env       map[string]string
}

// Parse decodes a kernel uevent message:
// "add@/devices/...\0ACTION=add\0DEVPATH=/devices/...\0SUBSYSTEM=block\0...".
// It returns false for anything else (udev's own "libudev" messages too).
func Parse(msg []byte) (Event, bool) {
	parts := strings.Split(strings.TrimRight(string(msg), "\x00"), "\x00")
	if len(parts) < 2 || !strings.Contains(parts[0], "@") || strings.HasPrefix(parts[0], "libudev") {
		return Event{}, false
	}
	ev := Event{Env: make(map[string]string, len(parts)-1)}
	for _, kv := range parts[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		ev.Env[k] = v
	}
	ev.Action = ev.Env["ACTION"]
	ev.DevPath = ev.Env["DEVPATH"]
	ev.Subsystem = ev.Env["SUBSYSTEM"]
	ev.DevType = ev.Env["DEVTYPE"]
	ev.DevName = ev.Env["DEVNAME"]
	if ev.Action == "" || ev.DevPath == "" {
		return Event{}, false
	}
	return ev, true
}
