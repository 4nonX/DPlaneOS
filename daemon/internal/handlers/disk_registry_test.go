package handlers

import (
	"database/sql"
	"testing"
)

func registryRow(t *testing.T, db *sql.DB, byID string) (devName, pool string, removed bool) {
	t.Helper()
	var removedAt sql.NullTime
	if err := db.QueryRow(`SELECT dev_name, COALESCE(pool_name,''), removed_at FROM disk_registry WHERE by_id_path = $1`, byID).
		Scan(&devName, &pool, &removedAt); err != nil {
		t.Fatalf("registry row %s: %v", byID, err)
	}
	return devName, pool, removedAt.Valid
}

func useRegistry(t *testing.T, db *sql.DB) {
	t.Helper()
	prev := registryDB
	registryDB = db
	t.Cleanup(func() { registryDB = prev })
}

// A disk keeps its registry entry (and its pool) when it comes back under
// another kernel name; that used to fail on the by-id unique constraint.
func TestRegistryFollowsDiskAcrossNames(t *testing.T) {
	db := testDB(t)
	const id = "/dev/disk/by-id/ata-WDC_1"
	if err := UpsertDisk(db, DiskInfo{Name: "sdb", ByIDPath: id, PoolName: "tank", Health: "ONLINE"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkDiskRemoved(db, id, "sdb"); err != nil {
		t.Fatal(err)
	}
	if _, pool, removed := registryRow(t, db, id); !removed || pool != "tank" {
		t.Fatalf("after removal: pool %q removed %v", pool, removed)
	}
	// Back as sdd, pool not imported at that moment.
	if err := UpsertDisk(db, DiskInfo{Name: "sdd", ByIDPath: id}); err != nil {
		t.Fatalf("re-adding under another name: %v", err)
	}
	name, pool, removed := registryRow(t, db, id)
	if name != "sdd" || pool != "tank" || removed {
		t.Fatalf("after return: %s pool %q removed %v", name, pool, removed)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM disk_registry`).Scan(&n)
	if n != 1 {
		t.Errorf("%d rows for one disk", n)
	}

	// Two disks without a stable path do not collide.
	for _, d := range []string{"vda", "vdb", "vda"} {
		if err := UpsertDisk(db, DiskInfo{Name: d}); err != nil {
			t.Fatalf("disk without by-id %s: %v", d, err)
		}
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM disk_registry WHERE by_id_path IS NULL`).Scan(&n)
	if n != 2 {
		t.Errorf("%d rows for two disks without a stable path", n)
	}

	// Exported on purpose: forgotten.
	ForgetPoolDisks(db, "tank")
	if _, pool, _ := registryRow(t, db, id); pool != "" {
		t.Errorf("pool after export: %q", pool)
	}
}

func TestSyncDiskRegistry(t *testing.T) {
	db := testDB(t)
	prev := discover
	t.Cleanup(func() { discover = prev })
	present := []DiskInfo{
		{Name: "sda", ByIDPath: "/dev/disk/by-id/a", PoolName: "tank"},
		{Name: "sdb", ByIDPath: "/dev/disk/by-id/b"},
	}
	discover = func() ([]DiskInfo, error) { return present, nil }
	if err := SyncDiskRegistry(db); err != nil {
		t.Fatal(err)
	}
	// sdb joined the pool; sda was pulled.
	present = []DiskInfo{{Name: "sdb", ByIDPath: "/dev/disk/by-id/b", PoolName: "tank"}}
	if err := SyncDiskRegistry(db); err != nil {
		t.Fatal(err)
	}
	if _, pool, removed := registryRow(t, db, "/dev/disk/by-id/a"); !removed || pool != "tank" {
		t.Errorf("pulled disk: pool %q removed %v", pool, removed)
	}
	if _, pool, removed := registryRow(t, db, "/dev/disk/by-id/b"); removed || pool != "tank" {
		t.Errorf("present disk: pool %q removed %v", pool, removed)
	}
}

func TestPoolComesBackWithItsDisk(t *testing.T) {
	db := testDB(t)
	useRegistry(t, db)
	cmds := fakeCommands(t, nil)
	prevS, prevI, prevG := poolStates, importablePoolsByID, GroupImportGuard
	t.Cleanup(func() { poolStates, importablePoolsByID, GroupImportGuard = prevS, prevI, prevG })

	const id = "/dev/disk/by-id/usb-Enclosure_1"
	disk := DiskInfo{Name: "sdc", ByIDPath: id}
	states := map[string]string{}
	importable := map[string]string{}
	poolStates = func() (map[string]string, error) { return states, nil }
	importablePoolsByID = func() map[string]string { return importable }
	GroupImportGuard = nil

	// Unknown disk: nothing happens, even with an importable pool around.
	importable["backup"] = "1234567890"
	if got := attemptPoolImport(disk); got != "" || len(cmds.keys()) != 0 {
		t.Fatalf("unknown disk: %q %v", got, cmds.keys())
	}

	if err := UpsertDisk(db, DiskInfo{Name: "sdc", ByIDPath: id, PoolName: "backup"}); err != nil {
		t.Fatal(err)
	}

	// Not imported, importable: imported by GUID.
	if got := attemptPoolImport(disk); got != "imported" || !cmds.ran("zpool_import", "import", "-d", "/dev/disk/by-id", "1234567890") {
		t.Fatalf("known pool: %q %v", got, cmds.keys())
	}

	// Imported and healthy or degraded: left to ZFS.
	cmds.calls = nil
	states["backup"] = "DEGRADED"
	if got := attemptPoolImport(disk); got != "" || len(cmds.keys()) != 0 {
		t.Fatalf("degraded pool: %q %v", got, cmds.keys())
	}

	// Imported but suspended: resumed.
	states["backup"] = "SUSPENDED"
	if got := attemptPoolImport(disk); got != "resumed" || !cmds.ran("zpool_clear", "clear", "backup") {
		t.Fatalf("suspended pool: %q %v", got, cmds.keys())
	}

	// A pool of a storage group owned elsewhere is not imported here.
	cmds.calls = nil
	delete(states, "backup")
	GroupImportGuard = func(guid, name string) (bool, string) { return false, "owned by another node" }
	if got := attemptPoolImport(disk); got != "" || cmds.ran("zpool_import") {
		t.Fatalf("pool owned elsewhere: %q %v", got, cmds.keys())
	}

	// Exported on purpose: not imported again.
	GroupImportGuard = nil
	ForgetPoolDisks(db, "backup")
	if got := attemptPoolImport(disk); got != "" || cmds.ran("zpool_import") {
		t.Fatalf("exported pool: %q %v", got, cmds.keys())
	}
}
