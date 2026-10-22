// Package sdnotify sends service status to systemd (sd_notify(3)) without
// cgo or dependencies: READY=1 for Type=notify units and WATCHDOG=1 keep-alives
// for units with WatchdogSec=. Without a NOTIFY_SOCKET (not started by
// systemd) every call is a no-op.
package sdnotify

import (
	"net"
	"os"
	"strconv"
	"time"
)

// Notify sends one state string ("READY=1", "WATCHDOG=1", ...). It reports
// whether systemd listens (false, nil outside systemd).
func Notify(state string) (bool, error) {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return false, nil
	}
	if addr[0] == '@' { // abstract namespace
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(state)); err != nil {
		return false, err
	}
	return true, nil
}

// WatchdogInterval returns the unit's WatchdogSec (from WATCHDOG_USEC), or 0
// when no watchdog is set or it is meant for another process (WATCHDOG_PID).
func WatchdogInterval() time.Duration {
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	us, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || us <= 0 {
		return 0
	}
	return time.Duration(us) * time.Microsecond
}
