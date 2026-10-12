package gitops

import (
	"reflect"
	"strings"
	"testing"
)

// `zpool status -P` of six pools: a pool of two mirrors with special, log,
// cache and spare disks; a stripe; a raidz2 with one member that is a
// partition somebody chose; one with a disk being replaced; one with a
// missing disk; a dRAID.
const statusSample = `  pool: tank
 state: ONLINE
  scan: scrub repaired 0B in 00:00:01 with 0 errors on Sun Oct 11 00:24:02 2026
config:

	NAME                                        STATE     READ WRITE CKSUM
	tank                                        ONLINE       0     0     0
	  mirror-0                                  ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_A-part1         ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_B-part1         ONLINE       0     0     0
	  mirror-1                                  ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_C-part1         ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_D-part1         ONLINE       0     0     0
	special	
	  mirror-2                                  ONLINE       0     0     0
	    /dev/disk/by-id/nvme-S1-part1           ONLINE       0     0     0
	    /dev/disk/by-id/nvme-S2-part1           ONLINE       0     0     0
	logs	
	  /dev/disk/by-id/nvme-L1-part1             ONLINE       0     0     0
	cache
	  /dev/disk/by-id/nvme-C1-part1             ONLINE       0     0     0
	  /dev/disk/by-id/nvme-C2-part1             ONLINE       0     0     0
	spares
	  /dev/disk/by-id/ata-WDC_S-part1           AVAIL

errors: No known data errors

  pool: scratch
 state: ONLINE
config:

	NAME                                STATE     READ WRITE CKSUM
	scratch                             ONLINE       0     0     0
	  /dev/disk/by-id/ata-X-part1       ONLINE       0     0     0
	  /dev/disk/by-id/ata-Y-part1       ONLINE       0     0     0

errors: No known data errors

  pool: z2
 state: ONLINE
config:

	NAME                                STATE     READ WRITE CKSUM
	z2                                  ONLINE       0     0     0
	  raidz2-0                          ONLINE       0     0     0
	    /dev/disk/by-id/ata-R1-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-R2-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-R3-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-R4-part3    ONLINE       0     0     0

errors: No known data errors

  pool: busy
 state: DEGRADED
status: One or more devices is currently being resilvered.
config:

	NAME                                  STATE     READ WRITE CKSUM
	busy                                  DEGRADED     0     0     0
	  mirror-0                            DEGRADED     0     0     0
	    replacing-0                       DEGRADED     0     0     0
	      /dev/disk/by-id/ata-OLD-part1   OFFLINE      0     0     0
	      /dev/disk/by-id/ata-NEW-part1   ONLINE       0     0     0  (resilvering)
	    /dev/disk/by-id/ata-KEEP-part1    ONLINE       0     0     0

errors: No known data errors

  pool: lost
 state: DEGRADED
config:

	NAME                                STATE     READ WRITE CKSUM
	lost                                DEGRADED     0     0     0
	  mirror-0                          DEGRADED     0     0     0
	    /dev/disk/by-id/ata-M1-part1    ONLINE       0     0     0
	    12345678901234567890            UNAVAIL      0     0     0  was /dev/disk/by-id/ata-M2-part1

errors: No known data errors

  pool: dr
 state: ONLINE
config:

	NAME                                STATE     READ WRITE CKSUM
	dr                                  ONLINE       0     0     0
	  draid2:3d:6c:1s-0                 ONLINE       0     0     0
	    /dev/disk/by-id/ata-D1-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-D2-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-D3-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-D4-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-D5-part1    ONLINE       0     0     0
	    /dev/disk/by-id/ata-D6-part1    ONLINE       0     0     0
	spares
	  draid2-0-0                        AVAIL

errors: No known data errors
`

// ZFS made partitions 1 and 9 on every disk it was given whole. R4 is the
// exception: its third partition was given to ZFS as it is.
func wholeDisks(t *testing.T) {
	t.Helper()
	old := pathExists
	pathExists = func(p string) bool { return strings.HasSuffix(p, "-part9") && !strings.Contains(p, "ata-R4") }
	t.Cleanup(func() { pathExists = old })
}

func TestPoolLayoutsAreReadAsBuilt(t *testing.T) {
	wholeDisks(t)
	layouts := parsePoolLayouts(statusSample)
	id := func(names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = "/dev/disk/by-id/" + n
		}
		return out
	}

	tank := layouts["tank"]
	if tank == nil || !tank.Settled {
		t.Fatalf("tank: %+v", tank)
	}
	want := PoolTopology{
		Data:    []VdevGroup{{Type: "mirror", Disks: id("ata-WDC_A", "ata-WDC_B")}, {Type: "mirror", Disks: id("ata-WDC_C", "ata-WDC_D")}},
		Special: []VdevGroup{{Type: "mirror", Disks: id("nvme-S1", "nvme-S2")}},
		Log:     []VdevGroup{{Type: "stripe", Disks: id("nvme-L1")}},
		Cache:   []VdevGroup{{Type: "stripe", Disks: id("nvme-C1", "nvme-C2")}},
		Spare:   []VdevGroup{{Type: "stripe", Disks: id("ata-WDC_S")}},
	}
	if !reflect.DeepEqual(tank.Topology, want) {
		t.Errorf("tank:\n got  %+v\n want %+v", tank.Topology, want)
	}
	if len(tank.Disks) != 10 {
		t.Errorf("tank has 10 disks, found %d: %v", len(tank.Disks), tank.Disks)
	}

	if l := layouts["scratch"]; l == nil || !l.Settled || !reflect.DeepEqual(l.Topology.Data, []VdevGroup{{Type: "stripe", Disks: id("ata-X", "ata-Y")}}) {
		t.Errorf("scratch: %+v", l)
	}
	// The partition somebody gave ZFS stays a partition.
	if l := layouts["z2"]; l == nil || !l.Settled || !reflect.DeepEqual(l.Topology.Data, []VdevGroup{{Type: "raidz2", Disks: id("ata-R1", "ata-R2", "ata-R3", "ata-R4-part3")}}) {
		t.Errorf("z2: %+v", l)
	}
	if l := layouts["dr"]; l == nil || !l.Settled || len(l.Topology.Data) != 1 || l.Topology.Data[0].Type != "draid" ||
		l.Topology.Data[0].DraidSpec != "draid2:3d:6c:1s" || len(l.Topology.Data[0].Disks) != 6 || len(l.Topology.Spare) != 0 {
		t.Errorf("dr: %+v", l)
	}

	// Not settled: all disks are still known, the layout is not offered.
	busy := layouts["busy"]
	if busy == nil || busy.Settled || !strings.Contains(busy.Reason, "replaced") || len(busy.Disks) != 3 {
		t.Errorf("busy: %+v", busy)
	}
	lost := layouts["lost"]
	if lost == nil || lost.Settled || !strings.Contains(lost.Reason, "missing") {
		t.Errorf("lost: %+v", lost)
	}

	// What is read can be written as a pool again: the arguments of zpool
	// create say mirror where the pool has mirrors.
	args, err := ZpoolCreateVdevArgs(tank.Topology)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, part := range []string{
		"mirror /dev/disk/by-id/ata-WDC_A /dev/disk/by-id/ata-WDC_B mirror /dev/disk/by-id/ata-WDC_C",
		"special mirror /dev/disk/by-id/nvme-S1", "log /dev/disk/by-id/nvme-L1",
		"cache /dev/disk/by-id/nvme-C1 /dev/disk/by-id/nvme-C2", "spare /dev/disk/by-id/ata-WDC_S",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("zpool create arguments lack %q: %s", part, got)
		}
	}
}

// The file the system writes to the repository says what the pool is, and
// applying that file to the same system plans nothing for the pool.
func TestSnapshotRecordsThePoolAsBuilt(t *testing.T) {
	wholeDisks(t)
	layouts := parsePoolLayouts(statusSample)
	var live LiveState
	for _, name := range []string{"tank", "z2"} {
		top := layouts[name].Topology
		live.Pools = append(live.Pools, LivePool{Name: name, Health: "ONLINE", GUID: "1234", Disks: layouts[name].Disks, Layout: &top})
	}
	// A pool that is being rebuilt is not written.
	live.Pools = append(live.Pools, LivePool{Name: "busy", Health: "DEGRADED", Disks: layouts["busy"].Disks, LayoutNote: layouts["busy"].Reason})

	yaml := GenerateStateYAML(&live)
	if strings.Contains(yaml, "busy") {
		t.Errorf("a pool without a settled layout was written:\n%s", yaml)
	}
	if strings.Contains(yaml, "-part1") {
		t.Errorf("whole disks are written as partitions:\n%s", yaml)
	}
	desired, err := ParseStateYAML(yaml)
	if err != nil {
		t.Fatalf("the snapshot does not parse: %v\n%s", err, yaml)
	}
	if len(desired.Pools) != 2 {
		t.Fatalf("pools in the snapshot: %d\n%s", len(desired.Pools), yaml)
	}
	for _, dp := range desired.Pools {
		if !reflect.DeepEqual(dp.Topology, layouts[dp.Name].Topology) {
			t.Errorf("%s is written differently from how it is built:\n got  %+v\n want %+v", dp.Name, dp.Topology, layouts[dp.Name].Topology)
		}
		for _, lp := range live.Pools {
			if lp.Name == dp.Name {
				if changes := diffPool(dp, lp); len(changes) != 0 {
					t.Errorf("%s: its own snapshot plans changes: %v", dp.Name, changes)
				}
			}
		}
	}
	if desired.Pools[0].Topology.Data[0].Type != "mirror" {
		t.Errorf("a mirror must be recorded as a mirror: %+v", desired.Pools[0].Topology.Data)
	}
}

// A pool declared with its disks matches itself although ZFS reports the
// partitions; a disk that really is not in the pool is still seen.
func TestDeclaredDisksMatchThePartitionsZFSReports(t *testing.T) {
	wholeDisks(t)
	l := parsePoolLayouts(statusSample)["scratch"]
	live := LivePool{Name: "scratch", Health: "ONLINE", Disks: l.Disks}
	declared := DesiredPool{Name: "scratch", Topology: PoolTopology{Data: []VdevGroup{{Type: "stripe",
		Disks: []string{"/dev/disk/by-id/ata-X", "/dev/disk/by-id/ata-Y"}}}}}
	if changes := diffPool(declared, live); len(changes) != 0 {
		t.Errorf("declared with whole disks: %v", changes)
	}
	// Written the way older snapshots wrote it (the partitions): the same pool.
	declared.Topology.Data[0].Disks = []string{"/dev/disk/by-id/ata-X-part1", "/dev/disk/by-id/ata-Y-part1"}
	if changes := diffPool(declared, live); len(changes) != 0 {
		t.Errorf("declared with the partitions: %v", changes)
	}
	declared.Topology.Data[0].Disks = []string{"/dev/disk/by-id/ata-X", "/dev/disk/by-id/ata-Z"}
	if changes := strings.Join(diffPool(declared, live), "; "); !strings.Contains(changes, "ata-Y") || !strings.Contains(changes, "topology-drift") {
		t.Errorf("another disk must be noticed: %s", changes)
	}
}

// Pool options: each is set where ZFS keeps it, and only a real difference
// is planned.
func TestPoolOptionsGoWhereZFSKeepsThem(t *testing.T) {
	dp := DesiredPool{Name: "tank", Ashift: 12,
		Topology: SimpleMirrorTopology("/dev/disk/by-id/a", "/dev/disk/by-id/b"),
		Options:  map[string]string{"compression": "lz4", "atime": "off", "autotrim": "on", "recordsize": "1m", "normalization": "formD"}}
	args, err := ZpoolCreateFullArgs(dp, false)
	if err != nil {
		t.Fatal(err)
	}
	got := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" -o autotrim=on ", " -O compression=lz4 ", " -O atime=off ", " -O normalization=formD ", " -o ashift=12 "} {
		if !strings.Contains(got, want) {
			t.Errorf("zpool create lacks %q: %s", want, got)
		}
	}
	live := LivePool{Name: "tank", Health: "ONLINE", Disks: []string{"/dev/disk/by-id/a", "/dev/disk/by-id/b"},
		Options: map[string]string{"compression": "lz4", "atime": "off", "autotrim": "on", "recordsize": "1M"}}
	if changes := diffPool(dp, live); len(changes) != 0 {
		t.Errorf("nothing differs, but planned: %v", changes)
	}
	live.Options["compression"] = "off"
	live.Options["autotrim"] = "off"
	changes := strings.Join(diffPool(dp, live), "; ")
	if !strings.Contains(changes, "compression=lz4") || !strings.Contains(changes, "autotrim=on") || strings.Contains(changes, "normalization") {
		t.Errorf("planned: %s", changes)
	}
	if !IsPoolProperty("autotrim") || IsPoolProperty("compression") {
		t.Error("autotrim is a pool property, compression one of the root dataset")
	}

	if _, err := ParseStateYAML("version: \"6\"\npools:\n  - name: tank\n    topology:\n      data:\n        - type: mirror\n          disks:\n            - /dev/disk/by-id/a\n            - /dev/disk/by-id/b\n    options:\n      compresion: lz4\n"); err == nil || !strings.Contains(err.Error(), "compresion") {
		t.Errorf("a misspelt pool option must be refused: %v", err)
	}
}

func TestPropertyLinesKeepSpaces(t *testing.T) {
	m := parsePropertyLines("comment\tmain pool, rack 2\nautotrim\ton\nfailmode\twait\ncomment2\t-\n")
	if m["comment"] != "main pool, rack 2" || m["autotrim"] != "on" || m["comment2"] != "" {
		t.Errorf("%v", m)
	}
}
