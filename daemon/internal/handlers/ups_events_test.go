package handlers

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUPSEvents(t *testing.T) {
	var mu sync.Mutex
	var got []string
	SetAlertDispatchers(nil, nil, nil, func(event string, data any, level string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, level+" "+event)
	})
	t.Cleanup(func() { SetAlertDispatchers(nil, nil, nil, nil) })

	sock := filepath.Join(t.TempDir(), "ups.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go StartUPSEventListener(ctx, sock, "")
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if info, err := os.Stat(sock); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", info, err)
	}
	send := func(lines ...string) {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte(strings.Join(lines, "\n") + "\n")) //nolint:errcheck
		c.Close()
	}
	send("ups_event:ONBATT:ups:UPS ups@localhost on battery",
		"ups_event:LOWBATT:ups:UPS ups@localhost battery is low",
		"ups_event:BOGUS:ups:x",       // unknown type
		"zfs_event:critical:tank:x:y", // not a UPS line
		"ups_event:ONLINE::no name")   // no UPS name
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(got, "; ")
	if len(got) != 2 || !strings.Contains(joined, "warning") || !strings.Contains(joined, "critical") {
		t.Fatalf("alerts: %v", got)
	}
	if !strings.Contains(upsEventMessage("ONBATT", "ups", ""), "on battery") {
		t.Error("ONBATT message")
	}
}
