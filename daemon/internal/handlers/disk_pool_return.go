package handlers

import (
	"log"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// When a disk comes back, the pool it belonged to (disk registry) is brought
// back with it:
//
//   - the pool is still imported but suspended (all paths to its data were
//     gone, e.g. an enclosure lost power): I/O is resumed with "zpool clear";
//   - the pool is not imported (the disks were missing at boot, or it was
//     unplugged cleanly): it is imported.
//
// A pool that was exported or destroyed through DPlaneOS is forgotten
// (ForgetPoolDisks) and is not imported again.

// knownPoolOf returns the pool the registry remembers for the disk.
func knownPoolOf(disk DiskInfo) string {
	if registryDB == nil {
		return ""
	}
	if disk.ByIDPath != "" {
		if rec, _ := GetDiskByByID(registryDB, disk.ByIDPath); rec != nil {
			return rec.PoolName
		}
		return ""
	}
	if rec, _ := GetDiskByDevName(registryDB, disk.Name); rec != nil && rec.ByIDPath == "" {
		return rec.PoolName
	}
	return ""
}

// Commands, replaced in tests.
var (
	poolStates = func() (map[string]string, error) {
		out, err := runWithTimeout(30*time.Second, "zpool", "status", "-P")
		if err != nil {
			return nil, err
		}
		return parsePoolHealthSummary(string(out)), nil
	}
	importablePoolsByID = func() map[string]string {
		out, _ := runWithTimeout(30*time.Second, "zpool", "import", "-d", "/dev/disk/by-id")
		return parseImportablePools(string(out))
	}
)

// attemptPoolImport brings back the pool of a returned disk. It returns what
// it did ("", "resumed", "imported") for the log and the tests.
func attemptPoolImport(disk DiskInfo) string {
	defer broadcastPoolHealthChanged()

	pool := knownPoolOf(disk)
	if pool == "" {
		return ""
	}
	states, err := poolStates()
	if err != nil {
		log.Printf("DISK EVENT: zpool status failed while bringing back %q: %v", pool, err)
		return ""
	}

	if state, imported := states[pool]; imported {
		// Imported: only a pool that stopped because its devices were gone
		// needs help. A degraded pool gets its device back from ZFS itself.
		if state != "SUSPENDED" && state != "UNAVAIL" && state != "FAULTED" {
			return ""
		}
		log.Printf("DISK EVENT: pool %q is %s and /dev/%s is back: resuming it (zpool clear)", pool, state, disk.Name)
		out, err := cmdutil.Run(2*time.Minute, "zpool_clear", "clear", pool)
		if err != nil {
			log.Printf("DISK EVENT: resuming pool %q failed: %v - %s", pool, err, strings.TrimSpace(string(out)))
			return ""
		}
		DispatchAlert("warning", "pool.resumed", pool,
			"Pool "+pool+" had stopped ("+state+") because its disks were gone. /dev/"+disk.Name+" is back and the pool was resumed. Check its status and run a scrub.")
		return "resumed"
	}

	guid, importable := importablePoolsByID()[pool]
	if !importable {
		return "" // not all of its disks are back yet, or it is gone
	}
	// A pool of a shared storage group is imported by its owner only.
	if ok, why := importAllowed(guid, pool); !ok {
		log.Printf("DISK EVENT: not importing pool %q: %s", pool, why)
		return ""
	}
	log.Printf("DISK EVENT: importing pool %q (GUID %s): its disk /dev/%s is back", pool, guid, disk.Name)
	// By GUID: names can collide. The GUID is positional ("zpool import" has no -g).
	out, err := cmdutil.Run(2*time.Minute, "zpool_import", "import", "-d", "/dev/disk/by-id", guid)
	if err != nil {
		log.Printf("DISK EVENT: importing pool %q failed: %v - %s", pool, err, strings.TrimSpace(string(out)))
		return ""
	}
	log.Printf("DISK EVENT: pool %q imported", pool)
	DispatchAlert("info", "pool.imported", pool, "Pool "+pool+" was imported: its disk /dev/"+disk.Name+" came back.")
	return "imported"
}
