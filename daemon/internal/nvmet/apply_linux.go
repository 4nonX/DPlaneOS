//go:build linux

package nvmet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// One Apply at a time: the API and the storage groups both call it.
var applyMu sync.Mutex

// Apply brings the kernel's NVMe target in line with exports.
//
// An export that is already served as described is not touched: its
// initiators stay connected while others are added, changed or removed.
// An export whose volume is not there (its pool is not imported) is left as
// it is and reported in the error; the others are applied all the same.
func Apply(exports []Export) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	list := make([]Export, len(exports))
	copy(list, exports)
	seen := map[string]bool{}
	for i := range list {
		if err := ValidateSpec(&list[i]); err != nil {
			return fmt.Errorf("export %q: %w", list[i].SubsystemNQN, err)
		}
		if seen[list[i].SubsystemNQN] {
			return fmt.Errorf("export %q is listed twice", list[i].SubsystemNQN)
		}
		seen[list[i].SubsystemNQN] = true
	}
	root := filepath.Join(ConfigfsRoot, "nvmet")
	if len(list) == 0 {
		// Nothing to serve: without the target loaded there is nothing to remove.
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return nil
		}
	} else if err := modprobe(); err != nil {
		return err
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("the kernel's NVMe target is not available (%s is missing)", root)
	}
	return reconcile(root, list)
}

func modprobe() error {
	for _, m := range []string{"nvmet", "nvmet-tcp"} {
		out, err := exec.Command("modprobe", m).CombinedOutput()
		if err != nil {
			return fmt.Errorf("modprobe %s: %w: %s", m, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
