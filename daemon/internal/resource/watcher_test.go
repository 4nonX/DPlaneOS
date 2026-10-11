package resource

import (
	"sync"
	"testing"
	"time"
)

func testWatcher(disk map[string]int, mem, fdOpen int) *Watcher {
	w := NewWatcher(time.Hour, nil)
	for p := range disk {
		w.diskPaths = append(w.diskPaths, p)
	}
	w.diskUsage = func(p string) (int, float64, bool) { return disk[p], 1.5, true }
	w.memUsage = func() (int, bool) { return mem, true }
	w.fdUsage = func() (int, int, bool) { return fdOpen, 1000, true }
	return w
}

func TestGrading(t *testing.T) {
	cases := []struct {
		name          string
		disk          map[string]int
		mem, fd       int
		want          string
		wantWarnings  int
		wantDiskPath  string
		wantDiskUsage int
	}{
		{"all fine", map[string]int{"/": 40, "/var/lib/dplaneos": 10}, 50, 100, "OK", 0, "/", 40},
		{"fullest file system counts", map[string]int{"/": 40, "/var/lib/dplaneos": 93}, 50, 100, "DEGRADED", 1, "/var/lib/dplaneos", 93},
		{"disk critical", map[string]int{"/": 98}, 50, 100, "CRITICAL", 1, "/", 98},
		{"memory degraded", map[string]int{"/": 10}, 92, 100, "DEGRADED", 1, "/", 10},
		{"descriptors critical", map[string]int{"/": 10}, 50, 950, "CRITICAL", 1, "/", 10},
		{"critical is not lowered by a later degraded", map[string]int{"/": 99}, 91, 100, "CRITICAL", 2, "/", 99},
	}
	for _, c := range cases {
		st := testWatcher(c.disk, c.mem, c.fd).check()
		if st.Status != c.want || len(st.Warnings) != c.wantWarnings || st.DiskPath != c.wantDiskPath || st.DiskUsagePercent != c.wantDiskUsage {
			t.Errorf("%s: %+v", c.name, st)
		}
	}
}

func TestChangeIsReportedOnce(t *testing.T) {
	disk := map[string]int{"/": 10}
	w := testWatcher(disk, 10, 10)
	w.lastStatus = w.check()
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 4)
	w.OnChange(func(prev, cur ResourceStatus) {
		mu.Lock()
		got = append(got, prev.Status+">"+cur.Status)
		mu.Unlock()
		done <- struct{}{}
	})
	w.Tick() // unchanged
	disk["/"] = 98
	w.Tick()
	<-done
	w.Tick() // still critical: no second report
	disk["/"] = 20
	w.Tick()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "OK>CRITICAL" || got[1] != "CRITICAL>OK" {
		t.Errorf("reports: %v", got)
	}
}

func TestParseDf(t *testing.T) {
	out := "Filesystem     1-blocks       Used  Available Capacity Mounted on\n/dev/nvme0n1p2 100000000000 90000000000 10000000000      90% /\n"
	pct, avail, ok := parseDf(out)
	if !ok || pct != 90 || avail < 9.3 || avail > 9.4 {
		t.Errorf("df: %d %f %v", pct, avail, ok)
	}
	if _, _, ok := parseDf("garbage"); ok {
		t.Error("garbage must not parse")
	}
}

// 16 GB machine, 1 GB available, ARC holds 12 GB of which 11 GB can be
// released: 4 GB are really in use, not 15.
func TestParseMemoryCountsReclaimableARC(t *testing.T) {
	meminfo := "MemTotal:       16777216 kB\nMemFree:          524288 kB\nMemAvailable:    1048576 kB\n"
	arc := "name type data\nc_min 4 1073741824\nsize 4 12884901888\n"
	pct, ok := parseMemory(meminfo, arc)
	if !ok || pct != 25 {
		t.Errorf("with ARC: %d %v", pct, ok)
	}
	pct, ok = parseMemory(meminfo, "")
	if !ok || pct != 93 {
		t.Errorf("without ARC: %d %v", pct, ok)
	}
	if _, ok := parseMemory("nothing", ""); ok {
		t.Error("missing fields must not parse")
	}
}
