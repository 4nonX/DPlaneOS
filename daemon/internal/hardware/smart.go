package hardware

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"dplaned/internal/systemd"
)

// RegenerateSMARTTimers creates systemd timers for all enabled SMART schedules.
// This is used by both the REST API and the GitOps reconciliation engine.
func RegenerateSMARTTimers(db *sql.DB) error {
	// 1. Clear existing smart timers
	if err := systemd.UninstallAllWithPrefix("dplaneos-smart-"); err != nil {
		log.Printf("ERROR: failed to clear existing SMART timers: %v", err)
	}

	rows, err := db.Query("SELECT device, test_type, schedule FROM smart_schedules WHERE enabled = 1")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var device, testType, schedule string
		if err := rows.Scan(&device, &testType, &schedule); err != nil {
			continue
		}

		// Use the cron-hook internal endpoint. 
		// Note: The curl command calls back into dplaned, ensuring consistency.
		// We use a fixed internal token whitelisted in main.go sessionMiddleware.
		payload := fmt.Sprintf(`{"device":"%s","type":"%s"}`, device, testType)
		mainCmd := fmt.Sprintf(
			`curl -sf -X POST http://127.0.0.1:9000/api/hardware/smart/cron-hook -H 'Content-Type: application/json' -H 'X-Internal-Token: dplaneos-internal-reconciliation-secret-v1' -d '%s'`,
			payload,
		)

		// Sanitize device name for unit file (e.g. /dev/sda -> sda)
		safeDev := strings.ReplaceAll(strings.TrimPrefix(device, "/dev/"), "/", "-")
		unitName := fmt.Sprintf("smart-%s-%s", safeDev, testType)

		err := systemd.InstallTimer(systemd.TimerConfig{
			Name:        unitName,
			Description: fmt.Sprintf("SMART %s test for %s", testType, device),
			Command:     fmt.Sprintf("bash -c \"%s\"", strings.ReplaceAll(mainCmd, "\"", "\\\"")),
			OnCalendar:  schedule,
			Persistent:  true,
		})
		if err != nil {
			log.Printf("ERROR: failed to install SMART timer for %s: %v", device, err)
		}
	}

	return nil
}
