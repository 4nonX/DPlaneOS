//go:build !linux

package nvmet

import "fmt"

// Apply is a no-op on non-Linux builds.
func Apply(exports []Export) error {
	if len(exports) == 0 {
		return nil
	}
	return fmt.Errorf("NVMe-oF target is only supported on Linux")
}
