// Package libzfs provides a thin abstraction over ZFS operations.
//
// When built with CGO_ENABLED=1 on Linux (the production NixOS path), the
// functions in this package call into libzfs directly via cgo, avoiding
// subprocess spawning for every ZFS operation. When CGO is unavailable or
// the target is not Linux, all functions fall back to cmdutil subprocess
// calls so the binary still compiles and works (dev machines, CI).
//
// Build variants:
//
//	zfs_cgo.go      – //go:build linux && cgo  – libzfs direct (production)
//	zfs_fallback.go – //go:build !linux || !cgo – cmdutil subprocess (dev/CI)
//
// Flake.nix wires the CGO variant via mkDaemonCGO; the existing static musl
// build (mkDaemon, CGO_ENABLED=0) continues to use the fallback path.
package libzfs

import "fmt"

// PoolMembership reports whether a block device is a member of any active
// ZFS pool, and if so, which pool.
type PoolMembership struct {
	InPool   bool
	PoolName string
}

// Error carries a libzfs operation name and the underlying error message
// returned by libzfs or the fallback subprocess.
type Error struct {
	Op  string
	Msg string
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("libzfs %s: %s", e.Op, e.Msg)
	}
	return fmt.Sprintf("libzfs %s failed", e.Op)
}

func libzfsErr(op, msg string) error { return &Error{Op: op, Msg: msg} }
