package handlers

import "testing"

func TestParseIOStats(t *testing.T) {
	out := "tank\t1000000\t9000000\t12\t34\t5600\t7800\t1500000\t2500000\t100\t200\t-\t-\t-\t-\t-\t-\n" +
		"rpool\t1\t2\t0\t0\t0\t0\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\n"
	stats := parseIOStats(out)
	if len(stats) != 2 {
		t.Fatalf("got %d rows: %v", len(stats), stats)
	}
	tank := stats[0]
	if tank["device"] != "tank" || tank["write_bw"].(int64) != 7800 || tank["read_wait_ns"].(int64) != 1500000 || tank["write_ops"].(int64) != 34 {
		t.Errorf("tank: %v", tank)
	}
	if stats[1]["read_wait_ns"].(int64) != 0 {
		t.Errorf("a missing value must be 0: %v", stats[1])
	}
}

func TestDiskReadTest(t *testing.T) {
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zpool": func([]string) ([]byte, error) {
			return []byte("  pool: tank\n config:\n\n\tNAME        STATE     READ WRITE CKSUM\n\ttank        ONLINE       0     0     0\n\t  mirror-0  ONLINE       0     0     0\n\t    sda1    ONLINE       0     0     0\n\t    sdb1    ONLINE       0     0     0\n\nerrors: No known data errors\n"), nil
		},
		"hdparm": func(args []string) ([]byte, error) {
			if args[len(args)-1] == "/dev/sdb1" {
				return []byte("/dev/sdb1:\n Timing O_DIRECT disk reads:  18 MB in  3.10 seconds =   5.80 MB/sec\n"), nil
			}
			return []byte("/dev/sda1:\n Timing O_DIRECT disk reads: 512 MB in  3.00 seconds = 170.61 MB/sec\n"), nil
		},
	})
	r := call(t, (&ZombieWatcherHandler{}).CheckDiskLatency, req{})
	if !r.ok() || r.body["overall"] != "slow" {
		t.Fatalf("a healthy disk and a slow one: %s", r)
	}
	for _, d := range r.body["disks"].([]any) {
		disk := d.(map[string]any)
		want := "ok"
		if disk["device"] == "sdb1" {
			want = "slow"
		}
		if disk["state"] != want {
			t.Errorf("%v: state %v, want %s", disk["device"], disk["state"], want)
		}
	}
}
