package nvmet

import (
	"errors"
	"log"
	"os"
	"time"
)

// RestoreAtStartup applies the saved exports when the daemon starts. The
// kernel target lives in configfs, which is empty after every boot; until now
// exports came back only with the next edit, GitOps apply or storage-group
// move, so a standalone node exported nothing after a reboot.
//
// It leaves the target alone when DPlaneOS subsystems are already configured
// (a daemon restart, or a storage group that took over first), waits up to
// wait for the zvols (udev creates /dev/zvol links after the pool import) and
// applies only the exports whose zvol exists here: on an HA node the pool of
// a group this node does not own is not imported, and its exports belong to
// the owner.
func RestoreAtStartup(path string, wait time.Duration) {
	exports, err := LoadExports(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(exports) == 0) {
		return
	}
	if err != nil {
		log.Printf("WARN: nvmet: cannot read %s: %v", path, err)
		return
	}
	if dplaneConfigured() {
		return
	}
	deadline := time.Now().Add(wait)
	for _, e := range exports {
		for !deviceExists(ZvolDevicePath(e.Zvol)) && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
		}
	}
	present := availableExports(exports, deviceExists)
	if skipped := len(exports) - len(present); skipped > 0 {
		log.Printf("nvmet: %d export(s) not restored: their zvol is not on this node", skipped)
	}
	if len(present) == 0 {
		return
	}
	if err := Apply(present); err != nil {
		log.Printf("ERROR: nvmet: restoring the exports failed: %v", err)
		return
	}
	log.Printf("nvmet: restored %d export(s)", len(present))
}

// availableExports keeps the exports whose zvol device exists.
func availableExports(exports []Export, exists func(dev string) bool) []Export {
	var out []Export
	for _, e := range exports {
		if exists(ZvolDevicePath(e.Zvol)) {
			out = append(out, e)
		}
	}
	return out
}

// dplaneConfigured reports whether DPlaneOS subsystems are in the target.
func dplaneConfigured() bool { return len(configuredNQNs()) > 0 }

func deviceExists(dev string) bool {
	_, err := os.Stat(dev)
	return err == nil
}
