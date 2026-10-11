package handlers

import (
	"log"
	"strings"

	"dplaned/internal/resource"
)

// EventSystemResources: the system disk, memory or the daemon's file
// descriptors ran low, or recovered.
const EventSystemResources = "system.resources"

// AlertResourceChange turns a status change of the resource watcher into an
// alert on every configured channel.
func AlertResourceChange(prev, cur resource.ResourceStatus) {
	level, msg := resourceAlert(prev, cur)
	log.Printf("RESOURCES: %s -> %s: %s", prev.Status, cur.Status, msg)
	DispatchAlert(level, EventSystemResources, "system", msg)
}

func resourceAlert(prev, cur resource.ResourceStatus) (level, msg string) {
	switch cur.Status {
	case "CRITICAL":
		return "critical", "Critical: " + strings.Join(cur.Warnings, "; ")
	case "DEGRADED":
		return "warning", "Low: " + strings.Join(cur.Warnings, "; ")
	default:
		return "info", "Back to normal (was: " + strings.Join(prev.Warnings, "; ") + ")"
	}
}
