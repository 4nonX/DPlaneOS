package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNotify(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if ok, err := Notify("READY=1"); ok || err != nil {
		t.Errorf("outside systemd: %v %v", ok, err)
	}

	sock := filepath.Join(t.TempDir(), "notify")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Skipf("unixgram not available: %v", err)
	}
	defer l.Close()
	t.Setenv("NOTIFY_SOCKET", sock)
	if ok, err := Notify("WATCHDOG=1"); !ok || err != nil {
		t.Fatalf("notify: %v %v", ok, err)
	}
	buf := make([]byte, 64)
	_ = l.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := l.ReadFromUnix(buf)
	if err != nil || string(buf[:n]) != "WATCHDOG=1" {
		t.Errorf("received %q, %v", buf[:n], err)
	}
}

func TestWatchdogInterval(t *testing.T) {
	t.Setenv("WATCHDOG_PID", "")
	t.Setenv("WATCHDOG_USEC", "60000000")
	if d := WatchdogInterval(); d != time.Minute {
		t.Errorf("interval %v", d)
	}
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()+1))
	if d := WatchdogInterval(); d != 0 {
		t.Errorf("watchdog of another process: %v", d)
	}
	t.Setenv("WATCHDOG_PID", "")
	t.Setenv("WATCHDOG_USEC", "")
	if d := WatchdogInterval(); d != 0 {
		t.Errorf("no watchdog: %v", d)
	}
}
