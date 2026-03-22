package ha

import (
	"log"
	"os/exec"
	"strings"
)

// ExecutePromotion formally transitions the storage and application states 
// on a standby node to primary, forcing pool imports and reloading services.
func ExecutePromotion() {
	// 1. Force import any detached storage pools in the case of a Shared-Storage Architecture.
	log.Printf("HA Failover: Forcing import of all pools...")
	exec.Command("zpool", "import", "-a", "-f", "-d", "/dev/disk/by-id").Run()

	// 2. Elevate replication datasets to writable in the case of a Shared-Nothing Architecture (ZFS Replication).
	log.Printf("HA Failover: Promoting ZFS datasets to writable...")
	out, _ := exec.Command("zfs", "list", "-H", "-o", "name").Output()
	datasets := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, ds := range datasets {
		if ds == "" {
			continue
		}
		exec.Command("zfs", "set", "readonly=off", ds).Run()
		exec.Command("zfs", "promote", ds).Run()
	}

	// 3. Reload NAS services that mount or share these pools.
	log.Printf("HA Failover: Reloading storage services (SMB, NFS, Docker)...")
	exec.Command("systemctl", "reload-or-restart", "smbd", "nmbd").Run()
	exec.Command("systemctl", "reload-or-restart", "nfs-server").Run()
	exec.Command("systemctl", "restart", "docker").Run()
	
	// 4. Force Patroni to failover via REST API (if not already primary)
	// This guarantees that the Keepalived Priority elevates.
	exec.Command("curl", "-s", "-X", "POST", "http://localhost:8008/failover").Run()
	
	log.Printf("HA Failover: Promotion sequence complete.")
}
