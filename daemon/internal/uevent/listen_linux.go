//go:build linux

package uevent

import (
	"context"
	"fmt"
	"syscall"
)

// Listen calls fn for every kernel uevent until ctx ends. It returns an
// error when the socket cannot be opened or fails; a caller that needs the
// events should call it again.
func Listen(ctx context.Context, fn func(Event)) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return fmt.Errorf("uevent socket: %w", err)
	}
	// Group 1: events as the kernel sends them (group 2 is udev's output).
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: 1}); err != nil {
		syscall.Close(fd)
		return fmt.Errorf("uevent bind: %w", err)
	}
	// A burst (a shelf of disks powering on) must not overflow the buffer.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			syscall.Close(fd) // unblocks Recvfrom
		case <-done:
		}
	}()

	buf := make([]byte, 64<<10)
	for {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.ENOBUFS {
				// Events were dropped: tell the caller to look at everything.
				fn(Event{Action: "overflow"})
				continue
			}
			syscall.Close(fd)
			return fmt.Errorf("uevent receive: %w", err)
		}
		// Only the kernel (port 0) is a valid sender; any local process can
		// send a unicast netlink message to this socket.
		if nl, ok := from.(*syscall.SockaddrNetlink); !ok || nl.Pid != 0 {
			continue
		}
		if ev, ok := Parse(buf[:n]); ok {
			fn(ev)
		}
	}
}
