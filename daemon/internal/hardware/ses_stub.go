//go:build !linux

package hardware

import "fmt"

// GetSESElements is not supported on non-Linux platforms.
func GetSESElements(_ string) ([]SESElement, error) {
	return nil, fmt.Errorf("SES ioctl not supported on this platform")
}
