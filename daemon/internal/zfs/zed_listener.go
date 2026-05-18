package zfs

import (
	"bufio"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StartZEDListener creates a Unix socket to listen for events from the ZED hook script.
// If the socket directory does not exist, it logs a warning and returns (safe fallback).
// On receiving an event, it calls the provided callbacks:
//   - broadcast: forwards events to the WebSocket hub
//   - dispatchAlert: routes warning/critical events to the alert subsystem
//   - refreshPoolHealth: called when a ZED event indicates pool state may have changed;
//     the caller should run zpool status and broadcast pool_health_change
//
// The function returns when ctx is cancelled, allowing clean daemon shutdown.
func StartZEDListener(
	ctx context.Context,
	socketPath string,
	broadcast func(eventType string, data interface{}, level string),
	dispatchAlert func(event, pool, msg string),
	refreshPoolHealth func(),
) {
	dir := filepath.Dir(socketPath)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Printf("Warning: ZED listener socket directory %s does not exist. Falling back to polling.", dir)
		return
	}

	// Remove existing socket if it exists
	os.Remove(socketPath)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Printf("Warning: Failed to listen on ZED socket %s: %v", socketPath, err)
		return
	}
	defer l.Close()

	if err := os.Chmod(socketPath, 0600); err != nil {
		log.Printf("Warning: Failed to chmod ZED socket: %v", err)
	}

	ul := l.(*net.UnixListener)
	for {
		// Poll the context by applying a short Accept deadline.
		// This lets the loop exit cleanly when the daemon shuts down.
		ul.SetDeadline(time.Now().Add(1 * time.Second))
		conn, err := ul.Accept()
		if err != nil {
			// Context cancelled - clean exit.
			select {
			case <-ctx.Done():
				log.Printf("ZED listener: context cancelled, shutting down")
				return
			default:
			}
			// Deadline exceeded - loop and check ctx again.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("ZED socket accept error: %v", err)
			continue
		}

		go handleZEDConnection(conn, broadcast, dispatchAlert, refreshPoolHealth)
	}
}

func handleZEDConnection(
	conn net.Conn,
	broadcast func(eventType string, data interface{}, level string),
	dispatchAlert func(event, pool, msg string),
	refreshPoolHealth func(),
) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()

		// Expected format: zfs_event:severity:pool:subclass:state
		parts := strings.Split(line, ":")
		if len(parts) < 5 || parts[0] != "zfs_event" {
			continue
		}

		severity := parts[1]
		pool := parts[2]
		subclass := parts[3]
		state := parts[4]

		log.Printf("ZED Event received: Pool=%s Subclass=%s State=%s Severity=%s", pool, subclass, state, severity)

		// Generic broadcast - the frontend can subscribe to zfs.event.* for raw events.
		eventData := map[string]string{
			"pool":     pool,
			"subclass": subclass,
			"state":    state,
		}

		var wsLevel string
		var alertMsg string

		switch severity {
		case "critical":
			wsLevel = "error"
			alertMsg = "CRITICAL ZFS EVENT\nPool: " + pool + "\nEvent: " + subclass + "\nState: " + state
		case "warning":
			wsLevel = "warning"
			alertMsg = "ZFS WARNING EVENT\nPool: " + pool + "\nEvent: " + subclass + "\nState: " + state
		default:
			wsLevel = "info"
		}

		broadcast("zfs.event."+subclass, eventData, wsLevel)

		// Alert dispatch for warning and critical events.
		if severity == "critical" || severity == "warning" {
			dispatchAlert("zfs."+severity+"."+subclass, pool, alertMsg)
		}

		// Typed dispatch: translate ZED subclasses into the named WS events that
		// the frontend already handles (matching zfs_operations.go + disk_event_handler.go).
		zedTypedDispatch(pool, subclass, state, wsLevel, broadcast, refreshPoolHealth)
	}
}

// zedTypedDispatch emits named WebSocket events for specific ZED subclasses,
// supplementing the generic zfs.event.{subclass} broadcast above.
// Pool-health events call refreshPoolHealth (wired to BroadcastPoolHealthChanged
// in main.go) rather than running zpool status inline, keeping this package
// free of direct I/O side-effects beyond GetPoolScanLine.
func zedTypedDispatch(
	pool, subclass, state, level string,
	broadcast func(string, interface{}, string),
	refreshPoolHealth func(),
) {
	switch subclass {

	// ── Scrub ────────────────────────────────────────────────────────────────

	case "scrub_start":
		broadcast("scrub_started", map[string]interface{}{"pool": pool}, "info")
		go zedFastProgressPoll(pool, "zfs.scrub.progress", broadcast)

	case "scrub_finish":
		broadcast("scrub_completed", map[string]interface{}{"pool": pool}, "info")
		refreshPoolHealth()

	case "scrub_abort":
		broadcast("scrub_aborted", map[string]interface{}{"pool": pool}, "warning")

	// ── Resilver ─────────────────────────────────────────────────────────────

	case "resilver_start":
		broadcast("resilver_started", map[string]interface{}{"pool": pool}, "info")
		go zedFastProgressPoll(pool, "zfs.resilver.progress", broadcast)

	case "resilver_finish":
		broadcast("resilver_completed", map[string]interface{}{"pool": pool}, "info")
		refreshPoolHealth()

	// ── TRIM ─────────────────────────────────────────────────────────────────

	case "trim_start":
		broadcast("trim_started", map[string]interface{}{"pool": pool}, "info")
		go zedTrimProgressPoll(pool, broadcast)

	case "trim_finish":
		broadcast("trim_completed", map[string]interface{}{"pool": pool}, "info")
		refreshPoolHealth()

	case "trim_abort":
		broadcast("trim_aborted", map[string]interface{}{"pool": pool}, "warning")

	// ── State changes and device events ──────────────────────────────────────

	case "statechange":
		// Refresh pool health for any degradation; ONLINE recoveries are handled
		// by the monitoring background ticker and don't need immediate refresh.
		switch state {
		case "FAULTED", "UNAVAIL", "REMOVED", "DEGRADED":
			refreshPoolHealth()
		}

	case "pool_destroy", "vdev_remove", "device_removal":
		refreshPoolHealth()

	case "vdev_clear":
		broadcast("vdev_errors_cleared", map[string]interface{}{"pool": pool}, "info")
		refreshPoolHealth()

	case "vdev_online":
		broadcast("vdev_recovered", map[string]interface{}{"pool": pool}, "info")
		refreshPoolHealth()

	case "pool_import":
		broadcast("pool_imported", map[string]interface{}{"pool": pool}, "info")
		refreshPoolHealth()

	// ── Data loss and system errors ───────────────────────────────────────────

	case "data_loss":
		broadcast("zfs.data_loss", map[string]interface{}{
			"pool":  pool,
			"state": state,
		}, "error")
		refreshPoolHealth()

	case "deadman":
		broadcast("zfs.deadman", map[string]interface{}{
			"pool":  pool,
			"state": state,
		}, "error")

	// ── I/O and checksum errors ───────────────────────────────────────────────
	// Already dispatched as alert above; also emit a structured WS event so the
	// frontend can show a non-modal notification without polling the audit log.

	case "io_failure":
		broadcast("zfs.io_error", map[string]interface{}{
			"pool":  pool,
			"state": state,
			"kind":  "io",
		}, level)

	case "checksum_failure":
		broadcast("zfs.io_error", map[string]interface{}{
			"pool":  pool,
			"state": state,
			"kind":  "checksum",
		}, level)
	}
}

// zedTrimProgressPoll polls zpool status every 2 seconds while a TRIM is in
// flight, broadcasting progress events. It exits when the operation finishes
// or times out; the ZED trim_finish event provides the completion broadcast.
func zedTrimProgressPoll(pool string, broadcast func(string, interface{}, string)) {
	const pollInterval = 2 * time.Second
	const maxRuntime = 12 * time.Hour

	deadline := time.Now().Add(maxRuntime)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		rawTrim, err := GetPoolTrimLine(pool)
		if err != nil || rawTrim == "" {
			continue
		}

		parsed := ParseTrimLine(rawTrim)
		if !parsed.InProgress {
			return
		}

		broadcast("zfs.trim.progress", map[string]interface{}{
			"pool":         pool,
			"percent_done": parsed.PercentDone,
			"eta":          parsed.ETA,
			"bytes_done":   parsed.BytesDone,
		}, "info")
	}

	log.Printf("ZED trim progress poll for pool %s timed out after 12h", pool)
}

// zedFastProgressPoll runs a goroutine that polls zpool status every 2 seconds
// and broadcasts scan progress events while the operation is in flight.
// It exits silently when the operation finishes; the ZED finish event provides
// the completion broadcast, so this function does not emit one itself.
func zedFastProgressPoll(pool, eventType string, broadcast func(string, interface{}, string)) {
	const pollInterval = 2 * time.Second
	const maxRuntime = 48 * time.Hour

	deadline := time.Now().Add(maxRuntime)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		rawScan, err := GetPoolScanLine(pool)
		if err != nil || rawScan == "" {
			continue
		}

		parsed := ParseScanLine(rawScan)
		if !parsed.InProgress {
			// Operation finished; ZED resilver_finish / scrub_finish will broadcast completion.
			return
		}

		broadcast(eventType, map[string]interface{}{
			"pool":         pool,
			"percent_done": parsed.PercentDone,
			"eta":          parsed.ETA,
			"bytes_done":   parsed.BytesDone,
		}, "info")
	}

	log.Printf("ZED fast progress poll for pool %s timed out after 48h", pool)
}
