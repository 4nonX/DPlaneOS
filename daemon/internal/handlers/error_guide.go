// error_guide.go - Plain-English error enrichment for the top operational failure modes.
//
// The system produces correct HTTP status codes and machine-readable error fields.
// This layer adds a human-readable "guide" field with a plain-English next step
// aimed at the operator or support technician reading the response body.
//
// Apply respondGuided() in place of respondErrorSimple() / respondJSON() when the
// error state has a well-understood resolution path. The "guide" field is surfaced
// in the UI's error toast and in the support bundle.

package handlers

import (
	"fmt"
	"net/http"
)

// guidedError is the standard enriched error response body.
type guidedError struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`            // technical description
	Guide   string `json:"guide"`            // plain-English next step
	Code    string `json:"code,omitempty"`   // machine-readable code for UI routing
	Action  string `json:"action,omitempty"` // optional UI action hint
}

func respondGuided(w http.ResponseWriter, status int, e guidedError) {
	respondJSON(w, status, e)
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

// ─── ZFS Quota ────────────────────────────────────────────────────────────────

func guidedQuotaBelowUsage(w http.ResponseWriter, dataset, quota, current string) {
	respondGuided(w, http.StatusBadRequest, guidedError{
		Error: fmt.Sprintf("The quota %s is below the dataset's current usage %s.", quota, current),
		Guide: fmt.Sprintf(
			"Dataset %q currently has %s in use (compressed). Setting a quota below this would make the dataset immediately read-only. "+
				"Set the quota above %s, or run 'zfs get referenced %s' to see the exact compressed usage. "+
				"If compression is enabled, logical usage may be higher than the referenced value shown.",
			dataset, current, current, dataset),
		Code: "quota_below_usage",
	})
}

// ─── Pool destroy ─────────────────────────────────────────────────────────────

func guidedPoolDestroyBlocked(w http.ResponseWriter, pool string, deps []string) {
	guide := fmt.Sprintf(
		"Pool %q has %d active service(s) using it. Stop them first:\n", pool, len(deps))
	for _, d := range deps {
		guide += fmt.Sprintf("  - %s\n", d)
	}
	guide += "After stopping all services, retry the destroy. If a service entry is stuck and cannot be removed, use force=true to bypass the soft check."
	respondGuided(w, http.StatusConflict, guidedError{
		Error: fmt.Sprintf("Pool %q cannot be destroyed while services are using it.", pool),
		Guide: guide,
		Code:  "pool_has_dependencies",
	})
}

// ─── HA blocked ──────────────────────────────────────────────────────────────

// ─── Persist mount ────────────────────────────────────────────────────────────
