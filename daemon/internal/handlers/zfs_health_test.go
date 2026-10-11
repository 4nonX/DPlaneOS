package handlers

import (
	"errors"
	"testing"
)

// The pool's own row and the vdev rows repeat the sums of their disks: only
// the disks are listed, with the slow I/O column of "zpool status -s".
func TestPoolHealthDisks(t *testing.T) {
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zpool": func([]string) ([]byte, error) {
			return []byte("  pool: tank\n state: ONLINE\n  scan: scrub repaired 0B in 00:10:00 with 0 errors\nconfig:\n\n" +
				"\tNAME        STATE     READ WRITE CKSUM  SLOW\n" +
				"\ttank        ONLINE       0     0     3     2\n" +
				"\t  raidz2-0  ONLINE       0     0     3     2\n" +
				"\t    sda1    ONLINE       0     0     0     0\n" +
				"\t    sdb1    ONLINE       0     0     3     2\n" +
				"\tlogs\n" +
				"\t  mirror-1  ONLINE       0     0     0     0\n" +
				"\t    nvme0n1p1  ONLINE    0     0     0     0\n" +
				"\nerrors: No known data errors\n"), nil
		},
	})
	r := call(t, (&ZFSHealthHandler{}).GetPoolHealth, req{})
	if !r.ok() {
		t.Fatalf("health: %s", r)
	}
	pools := r.body["pools"].([]any)
	if len(pools) != 1 {
		t.Fatalf("pools: %v", pools)
	}
	pool := pools[0].(map[string]any)
	disks := pool["disks"].([]any)
	if len(disks) != 3 {
		t.Fatalf("want the 3 disks without pool and vdev rows, got %v", disks)
	}
	sdb := disks[1].(map[string]any)
	if sdb["device"] != "sdb1" || sdb["checksum_errors"].(float64) != 3 || sdb["slow_ios"].(float64) != 2 || sdb["risk"] != "medium" {
		t.Errorf("sdb1: %v", sdb)
	}
	if pool["overall_risk"] != "medium" || pool["errors"] != "No known data errors" {
		t.Errorf("pool: %v", pool)
	}
}

func TestPoolHealthUnavailable(t *testing.T) {
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zpool": func([]string) ([]byte, error) { return []byte("no zpool"), errors.New("exit status 1") },
	})
	if r := call(t, (&ZFSHealthHandler{}).GetPoolHealth, req{}); r.body["success"] != false || r.body["error"] == nil {
		t.Errorf("a failed zpool status must not read as healthy: %s", r)
	}
}
