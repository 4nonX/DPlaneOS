package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dplaned/internal/nvmet"
)

// A zvol is exported once: two exports of the same block device (two NVMe-oF
// subsystems, two iSCSI targets, or one of each) let two hosts write it with
// file systems that do not know of each other. LIO and nvmet accept it.

// sameDevice compares device paths through their symlinks (/dev/zvol/...).
// lioCoreDir is LIO's configfs directory of storage objects.
var lioCoreDir = "/sys/kernel/config/target/core"

func sameDevice(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// blockDeviceExportedBy names the export that already uses dev ("" = none).
// skipNVMe / skipISCSI: the NVMe subsystem or iSCSI storage object being
// changed (its own device is fine).
func blockDeviceExportedBy(dev, skipNVMe, skipISCSI string) string {
	if exports, err := nvmet.LoadExports(nvmet.TargetsFile); err == nil {
		for _, e := range exports {
			if e.SubsystemNQN != skipNVMe && sameDevice(nvmet.ZvolDevicePath(e.Zvol), dev) {
				return "NVMe-oF subsystem " + e.SubsystemNQN
			}
		}
	}
	// The kernel's iSCSI storage objects (the saved configuration can be
	// stale): <lioCoreDir>/iblock_N/<name>/udev_path.
	objs, _ := filepath.Glob(filepath.Join(lioCoreDir, "iblock_*", "*", "udev_path"))
	for _, p := range objs {
		name := filepath.Base(filepath.Dir(p))
		if b, err := os.ReadFile(p); err == nil && name != skipISCSI {
			if d := strings.TrimSpace(string(b)); d != "" && sameDevice(d, dev) {
				return "an iSCSI target (storage object " + name + ")"
			}
		}
	}
	return ""
}

func errBlockDeviceInUse(dev, by string) error {
	return fmt.Errorf("%s is already exported by %s: a block device is exported once (two hosts writing it corrupt its data)", dev, by)
}
