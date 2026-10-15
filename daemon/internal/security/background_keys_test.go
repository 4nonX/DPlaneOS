package security

import "testing"

// The background monitors and the kept split copies call zfs/zpool through
// these keys with exactly these argument shapes.
func TestBackgroundAndSplitKeys(t *testing.T) {
	ok := map[string][]string{
		"cap_zpool_list":            {"list", "-Hp", "-o", "name,size,alloc,free,capacity"},
		"cap_zpool_list_capacity":   {"list", "-Hp", "-o", "name,capacity"},
		"cap_zpool_size":            {"list", "-Hp", "-o", "size", "tank"},
		"cap_zfs_get_reservation":   {"get", "-Hp", "-o", "value", "reservation", "tank/dplane-reserved"},
		"cap_zfs_list_reserve":      {"list", "-H", "tank/dplane-reserved"},
		"cap_zfs_create_reserve":    {"create", "tank/dplane-reserved"},
		"cap_zfs_set_reservation":   {"set", "reservation=21474836", "tank/dplane-reserved"},
		"cap_zfs_destroy_reserve":   {"destroy", "tank/dplane-reserved"},
		"zpool_status_one":          {"status", "tank"},
		"zpool_list_names":          {"list", "-H", "-o", "name"},
		"sched_zfs_list_snapshots":  {"list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-r", "tank/data"},
		"sched_zfs_snapshot_exists": {"list", "-t", "snapshot", "-H", "-o", "name", "tank/data@repl-2026-10-15"},
		"ret_zfs_list_snapshots":    {"list", "-H", "-t", "snapshot", "-o", "name,creation", "-r", "-s", "creation", "tank/data"},
		"ret_zfs_destroy_snapshot":  {"destroy", "tank/data@auto-20261015"},
		"prom_zfs_list":             {"list", "-Hp", "-o", "name,used,avail,quota", "-t", "filesystem,volume"},
		"zfs_split_snapshot":        {"snapshot", "-r", "tank@split-20261015-010203"},
		"zfs_split_changed":         {"list", "-H", "-p", "-o", "name,written@dplane-2-1760000000000", "-r", "tank"},
		"zfs_split_create":          {"create", "-p", "tank/dplane-split-20261015-010203-from-a/data"},
		"zfs_split_list":            {"list", "-H", "-p", "-o", "name,used,creation", "-d", "1", "tank"},
		"zfs_split_destroy":         {"destroy", "-r", "tank/dplane-split-20261015-010203-from-a"},
	}
	for k, args := range ok {
		if err := ValidateCommand(k, args); err != nil {
			t.Errorf("%s %v: %v", k, args, err)
		}
	}
	bad := map[string][]string{
		"cap_zfs_destroy_reserve":  {"destroy", "tank/data"},                   // only the reserve
		"cap_zfs_set_reservation":  {"set", "quota=1", "tank/dplane-reserved"}, // only the reservation
		"ret_zfs_destroy_snapshot": {"destroy", "tank/data"},                   // snapshots only
		"zfs_split_destroy":        {"destroy", "-r", "tank"},                  // never the pool
		"zfs_split_create":         {"create", "-p", "tank/other"},             // kept copies only
		"zpool_status_one":         {"status", "tank;reboot"},                  // pool names only
		"sched_zfs_list_snapshots": {"list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-r", "-x"},
	}
	for k, args := range bad {
		if err := ValidateCommand(k, args); err == nil {
			t.Errorf("%s %v accepted", k, args)
		}
	}
}
