//go:build !linux

package persistguard

// Start is a no-op on non-Linux builds (development hosts).
func Start() {}
