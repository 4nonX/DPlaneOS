package ha

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// ExecutePromotion formally transitions the storage and application states 
// on a standby node to primary, forcing pool imports and reloading services.
func ExecutePromotion(candidate, leader string) {
	// 1. Force import any detached storage pools in the case of a Shared-Storage Architecture.
	log.Printf("HA Failover: Forcing import of all pools...")
	_, err := cmdutil.RunSlow("zpool_import_all", "import", "-a", "-f", "-d", "/dev/disk/by-id")
	if err != nil {
		log.Printf("HA Failover Error: zpool import failed: %v", err)
	}

	// 2. Elevate replication datasets to writable in the case of a Shared-Nothing Architecture (ZFS Replication).
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
		
		// 2a. Check if already writable
		ro, _ := cmdutil.RunFast("zfs_get_property", "get", "-H", "-o", "value", "readonly", ds)
		if strings.TrimSpace(string(ro)) == "on" {
			log.Printf("HA Failover: Setting %s to readonly=off", ds)
			cmdutil.RunMedium("zfs_set_property", "set", "readonly=off", ds)
		}

		// 2b. Check if it's a clone (needs promotion)
		origin, _ := cmdutil.RunFast("zfs_get_property", "get", "-H", "-o", "value", "origin", ds)
		if strings.TrimSpace(string(origin)) != "-" {
			log.Printf("HA Failover: Promoting clone %s", ds)
			cmdutil.RunMedium("zfs_promote", "promote", ds)
		}
	}

	// 3. Reload NAS services that mount or share these pools.
	log.Printf("HA Failover: Reloading storage services (SMB, NFS, Docker)...")
	cmdutil.RunMedium("systemctl_ha_smbd", "reload-or-restart", "smbd")
	cmdutil.RunMedium("systemctl_ha_nmbd", "reload-or-restart", "nmbd")
	cmdutil.RunMedium("systemctl_ha_nfs", "reload-or-restart", "nfs-server")
	cmdutil.RunMedium("systemctl_restart_docker", "restart", "docker")
	
	// 4. Force Patroni to failover via REST API (if not already primary)
	// This guarantees that the Keepalived Priority elevates.
	log.Printf("HA Failover: Triggering Patroni failover via REST API (candidate=%s, leader=%s)...", candidate, leader)
	client := &http.Client{Timeout: 3 * time.Second}
	
	// Patroni failover body requires candidate and leader
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
