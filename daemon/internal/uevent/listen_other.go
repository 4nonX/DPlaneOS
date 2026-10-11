//go:build !linux

package uevent

import "context"

// Listen is only available on Linux.
func Listen(ctx context.Context, fn func(Event)) error { return ErrUnsupported }
