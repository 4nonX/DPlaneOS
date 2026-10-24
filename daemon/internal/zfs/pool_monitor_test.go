package zfs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type sent struct{ level, event, pool, msg string }

func TestPoolMonitor(t *testing.T) {
	var got []sent
	SetAlertDispatch(func(l, e, p, m string) { got = append(got, sent{l, e, p, m}) })
	defer SetAlertDispatch(nil)
	health := map[string]string{"tank": "ONLINE", "backup": "ONLINE"}
	dockerStops := 0
	m := NewPoolMonitor()
	m.StopDockerOnFailure = true
	m.listHealth = func(context.Context) (map[string]string, error) {
		c := map[string]string{}
		for k, v := range health {
			c[k] = v
		}
		return c, nil
	}
	m.stopDocker = func() error { dockerStops++; return nil }
	ctx := context.Background()

	m.Check(ctx)
	if len(got) != 0 {
		t.Fatalf("healthy pools alerted: %v", got)
	}
	// A mirror with one disk gone: DEGRADED is a warning, Docker keeps running.
	health["tank"] = "DEGRADED"
	m.Check(ctx)
	m.Check(ctx) // no repeat
	if len(got) != 1 || got[0].level != "warning" || got[0].event != "pool.degraded" || dockerStops != 0 {
		t.Errorf("degraded: %v, docker stops %d", got, dockerStops)
	}
	// A pool imported later is watched too.
	health["new"] = "ONLINE"
	m.Check(ctx)
	health["new"] = "FAULTED"
	m.Check(ctx)
	if last := got[len(got)-1]; last.event != "docker.stopped" || dockerStops != 1 {
		t.Errorf("faulted: %v, docker stops %d", got, dockerStops)
	}
	if got[len(got)-2].level != "critical" || got[len(got)-2].pool != "new" {
		t.Errorf("critical alert: %v", got)
	}
	health["new"] = "ONLINE"
	health["tank"] = "ONLINE"
	got = nil
	m.Check(ctx)
	if len(got) != 2 || got[0].event != "pool.recovered" {
		t.Errorf("recovery: %v", got)
	}
}

func TestScanFinding(t *testing.T) {
	clean := `  pool: tank
 state: ONLINE
  scan: scrub repaired 0B in 00:00:01 with 0 errors on Sun Oct 18 03:35:21 2026
config:

	NAME        STATE     READ WRITE CKSUM
	tank        ONLINE       0     0     0
	  mirror-0  ONLINE       0     0     0
	    sda     ONLINE       0     0     0
	    sdb     ONLINE       0     0     0

errors: No known data errors
`
	if l, m := ScanFinding("tank", clean); l != "" {
		t.Errorf("clean: %s %s", l, m)
	}
	repaired := strings.Replace(strings.Replace(clean, "repaired 0B", "repaired 8.03M", 1), "sda     ONLINE       0     0     0", "sda     ONLINE       0     0    93", 1)
	if l, m := ScanFinding("tank", repaired); l != "warning" || !strings.Contains(m, "8.03M") || !strings.Contains(m, "sda (read 0, write 0, checksum 93)") {
		t.Errorf("repaired: %s %s", l, m)
	}
	damaged := strings.Replace(clean, "errors: No known data errors", "errors: 64 data errors, use '-v' for a list", 1)
	if l, m := ScanFinding("tank", damaged); l != "critical" || !strings.Contains(m, "64 data errors") {
		t.Errorf("damaged: %s %s", l, m)
	}
}

// A failed stop is tried again while the pool is still failed.
func TestPoolMonitorRetriesDockerStop(t *testing.T) {
	var got []sent
	SetAlertDispatch(func(l, e, p, m string) { got = append(got, sent{l, e, p, m}) })
	defer SetAlertDispatch(nil)
	m := NewPoolMonitor()
	m.StopDockerOnFailure = true
	m.listHealth = func(context.Context) (map[string]string, error) {
		return map[string]string{"tank": "FAULTED"}, nil
	}
	calls, fail := 0, true
	m.stopDocker = func() error {
		calls++
		if fail {
			return errors.New("systemctl: timeout")
		}
		return nil
	}
	ctx := context.Background()
	m.Check(ctx)
	m.Check(ctx)
	if calls != 2 {
		t.Fatalf("stop attempts %d, want 2", calls)
	}
	fail = false
	m.Check(ctx)
	m.Check(ctx) // stopped: not again
	if calls != 3 || got[len(got)-1].event != "docker.stopped" {
		t.Errorf("attempts %d, alerts %v", calls, got)
	}
}
