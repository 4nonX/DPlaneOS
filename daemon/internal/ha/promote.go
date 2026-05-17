package ha

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/libzfs"
)

// ExecutePromotion formally transitions the storage and application states
// on a standby node to primary, forcing pool imports and reloading services.
func ExecutePromotion(candidate, leader string) {
	// 1. Force import any detached storage pools.
	// libzfs.PoolImportAll walks /dev/disk/by-id directly via the kernel
	// ioctl path instead of spawning a subprocess, so it is resilient to
	// PATH issues during early failover.
	log.Printf("HA Failover: Forcing import of all pools...")
	if err := libzfs.PoolImportAll("/dev/disk/by-id"); err != nil {
		log.Printf("HA Failover Error: zpool import failed: %v", err)
	}

	// 2. Elevate replication datasets to writable (Shared-Nothing / ZFS Replication).
	log.Printf("HA Failover: Promoting ZFS datasets to writable...")
	out, err := cmdutil.RunFast("zfs_list_names", "list", "-H", "-o", "name")
	if err != nil {
		log.Printf("HA Failover Error: zfs list failed: %v", err)
		return
	}
	datasets := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, ds := range datasets {
		if ds == "" {
			continue
		}

		// 2a. Set readonly=off if the dataset is currently read-only.
		ro, err := libzfs.DatasetGet(ds, "readonly")
		if err != nil {
			log.Printf("HA Failover: could not read readonly prop for %s: %v", ds, err)
			continue
		}
		if strings.TrimSpace(ro) == "on" {
			log.Printf("HA Failover: Setting %s to readonly=off", ds)
			if err := libzfs.DatasetSet(ds, "readonly", "off"); err != nil {
				log.Printf("HA Failover: readonly=off failed for %s: %v", ds, err)
			}
		}

		// 2b. Promote clones to full datasets.
		origin, err := libzfs.DatasetGet(ds, "origin")
		if err != nil {
			log.Printf("HA Failover: could not read origin for %s: %v", ds, err)
			continue
		}
		if strings.TrimSpace(origin) != "-" {
			log.Printf("HA Failover: Promoting clone %s", ds)
			if err := libzfs.DatasetPromote(ds); err != nil {
				log.Printf("HA Failover: promote failed for %s: %v", ds, err)
			}
		}
	}

	// 3. Reload NAS services that mount or share these pools.
	log.Printf("HA Failover: Reloading storage services (SMB, NFS, Docker)...")
	cmdutil.RunMedium("systemctl_ha_smbd", "reload-or-restart", "smbd")
	cmdutil.RunMedium("systemctl_ha_nmbd", "reload-or-restart", "nmbd")
	cmdutil.RunMedium("systemctl_ha_nfs", "reload-or-restart", "nfs-server")
	cmdutil.RunMedium("systemctl_restart_docker", "restart", "docker")

	// 4. Force Patroni to failover via REST API (if not already primary).
	log.Printf("HA Failover: Triggering Patroni failover via REST API (candidate=%s, leader=%s)...", candidate, leader)
	client := &http.Client{Timeout: 3 * time.Second}
	payload := map[string]string{
		"candidate": candidate,
		"leader":    leader,
	}
	body, _ := json.Marshal(payload)
	_, err = client.Post("http://localhost:8008/failover", "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("HA Failover Error: Patroni failover request failed: %v", err)
	}

	log.Printf("HA Failover: Promotion sequence complete.")
}
