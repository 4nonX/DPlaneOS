package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Events raised by code in this package that are not declared elsewhere.
const (
	EventReplicationFailed    = "replication.failed"
	EventReplicationCompleted = "replication.completed"
	EventUPSOnBattery         = "ups.on_battery"
	EventUPSLowBattery        = "ups.low_battery"
	EventUPSOnline            = "ups.online"
	EventUPSUnreachable       = "ups.unreachable"
	EventUpdateInstalled      = "update.installed"
	EventUpdateReverted       = "update.reverted"
)

// AlertEvent describes one kind of alert the system raises.
type AlertEvent struct {
	// Event is the name sent with the alert. A name ending in ".*" stands
	// for a family (zfs.* covers zfs.critical.statechange and so on).
	Event       string `json:"event"`
	Level       string `json:"level"` // info, warning, critical (the usual one)
	Description string `json:"description"`
}

// AlertCatalog lists every alert that is actually raised: the webhook form
// offers these to subscribe to, and docs/admin/ALERTS.md is written from
// this list. An event that is not here is not sent.
//
// Channels: webhooks get every level (each webhook can pick its events),
// e-mail gets warning and critical, Telegram gets critical.
var AlertCatalog = []AlertEvent{
	{"zfs.*", "warning", "ZFS reports a problem with a pool or one of its devices: a state change (degraded, faulted, removed), checksum or I/O errors, data errors found by a scrub. The exact event is zfs.<level>.<what ZFS calls it>."},
	{"pool.critical", "critical", "A pool is suspended or unavailable, or can no longer be written to."},
	{EventPoolDegraded, "warning", "A pool is degraded: a device has failed and should be replaced."},
	{"pool.resumed", "warning", "A suspended pool was resumed after its missing device came back."},
	{"pool.imported", "info", "A known pool was imported after its device came back."},
	{EventCapacityWarning, "warning", "A pool is filling up (first threshold of the capacity guardian)."},
	{EventCapacityCritical, "critical", "A pool is nearly full."},
	{EventCapacityEmergency, "critical", "A pool is so full that writes will fail soon."},
	{EventDiskSmartFail, "critical", "A disk reports failing health (SMART), or a monitored attribute crossed its threshold."},
	{EventSystemResources, "warning", "The system disk, memory or file handles are running out (or are fine again)."},
	{EventReplicationFailed, "critical", "A scheduled replication failed."},
	{EventReplicationCompleted, "info", "A scheduled replication completed."},
	{"replication.restore_complete", "info", "A restore from a replication target finished."},
	{"ha.takeover", "critical", "A storage group is now served by another node than before."},
	{"ha.takeover_failed", "critical", "The owner of a storage group is gone and this node could not take the group over."},
	{EventUPSOnBattery, "warning", "The UPS runs on battery: mains power is out."},
	{EventUPSLowBattery, "critical", "The UPS battery is low; the system shuts down as configured."},
	{EventUPSOnline, "info", "Mains power is back."},
	{EventUPSUnreachable, "warning", "The UPS no longer answers (cable, driver, or the server it is attached to)."},
	{EventUpdateInstalled, "info", "A system update was installed and kept."},
	{EventUpdateReverted, "critical", "A system update was removed again; the system runs the previous version."},
	{EventAuthFailedBurst, "warning", "Repeated failed sign-in attempts from one address."},
}

// GetAlertEvents GET /api/alerts/events
func GetAlertEvents(w http.ResponseWriter, r *http.Request) {
	respondOK(w, map[string]any{"success": true, "events": AlertCatalog})
}

// alertEventMatches reports whether a subscription entry ("pool.critical",
// or a family such as "zfs.*") covers an event.
func alertEventMatches(subscription, event string) bool {
	if subscription == event || subscription == "*" {
		return true
	}
	if prefix, ok := strings.CutSuffix(subscription, "*"); ok {
		return strings.HasPrefix(event, prefix)
	}
	return false
}

// ── UPS ──────────────────────────────────────────────────────────────────────

// upsStatusOf asks NUT for the UPS status flags ("OL", "OB LB", ...).
var upsStatusOf = func(target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "upsc", target, "ups.status").Output()
	return strings.TrimSpace(string(out)), err
}

// upsAlertFor decides what to say when the UPS state changes from prev to
// cur ("" = not known yet, "unreachable", or NUT status flags).
func upsAlertFor(prev, cur string) (level, event, message string) {
	has := func(s, flag string) bool {
		for _, f := range strings.Fields(s) {
			if f == flag {
				return true
			}
		}
		return false
	}
	switch {
	case cur == "unreachable":
		if prev != "" && prev != "unreachable" {
			return "warning", EventUPSUnreachable, "The UPS no longer answers. Until it does, the system does not notice a power failure."
		}
	case has(cur, "LB") && !has(prev, "LB"):
		return "critical", EventUPSLowBattery, "The UPS battery is low. The system shuts down as configured."
	case has(cur, "OB") && !has(prev, "OB"):
		return "warning", EventUPSOnBattery, "Mains power is out: the UPS runs on battery."
	case has(cur, "OL") && (has(prev, "OB") || prev == "unreachable"):
		return "info", EventUPSOnline, "The UPS is on mains power again."
	}
	return "", "", ""
}

// StartUPSAlerts watches the UPS set up on the UPS page and raises an alert
// when it goes on battery, runs low, comes back, or stops answering.
func StartUPSAlerts(ctx context.Context) {
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		prev, failures := "", 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if !onNixOS() {
				continue
			}
			c := NixWriter.State().UPS()
			if c.Mode == "" {
				prev, failures = "", 0
				continue
			}
			target := c.Name + "@localhost"
			if c.Mode == "remote" {
				target = c.Name + "@" + c.Host
			}
			cur, err := upsStatusOf(target)
			if err != nil || cur == "" {
				// A driver that is just starting is not an outage.
				if failures++; failures < 4 {
					continue
				}
				cur = "unreachable"
			} else {
				failures = 0
			}
			if level, event, msg := upsAlertFor(prev, cur); event != "" {
				DispatchAlert(level, event, c.Name, msg)
			}
			prev = cur
		}
	}()
}

// ── System updates ───────────────────────────────────────────────────────────

// NotifyUpdateResult raises an alert for the outcome of the last system
// update, once: the update is decided while the system restarts, when
// nobody is on the Updates page to see it.
func NotifyUpdateResult(db *sql.DB) {
	if !updateToolAvailable() {
		return
	}
	go func() {
		// The check of an update on trial runs a few minutes after boot.
		for attempt := 0; attempt < 40; attempt++ {
			out, _, err := runUpdateTool(30*time.Second, "status")
			if err != nil {
				return
			}
			st := map[string]any{}
			if json.Unmarshal([]byte(out), &st) != nil {
				return
			}
			if st["pending"] != nil {
				time.Sleep(30 * time.Second)
				continue
			}
			res, _ := st["last_result"].(map[string]any)
			if res == nil {
				return
			}
			at, _ := res["at"].(string)
			outcome, _ := res["outcome"].(string)
			version, _ := res["version"].(string)
			message, _ := res["message"].(string)
			var seen string
			_ = db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key = 'update_result_notified'`).Scan(&seen)
			if at == "" || seen == at {
				return
			}
			if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('update_result_notified', $1)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, at); err != nil {
				log.Printf("UPDATE: recording the notified result: %v", err)
				return
			}
			if outcome == "installed" {
				DispatchAlert("info", EventUpdateInstalled, version, fmt.Sprintf("DPlaneOS %s is installed.", version))
			} else {
				DispatchAlert("critical", EventUpdateReverted, version, message)
			}
			return
		}
	}()
}
