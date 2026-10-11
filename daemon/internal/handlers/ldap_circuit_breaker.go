package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"dplaned/internal/monitoring"
)

// Status of the directory login circuit breaker (auth.go: ldapCB*), the one
// the login path uses: after ldapCBThreshold connection failures in a row,
// directory logins fail at once for ldapCBResetAfter instead of each waiting
// for a timeout.

// ldapCBState returns "closed", "open" or "half-open" and the failure count.
func ldapCBState() (string, int, time.Time) {
	ldapCBMu.Lock()
	defer ldapCBMu.Unlock()
	switch {
	case ldapCBFailures < ldapCBThreshold:
		return "closed", ldapCBFailures, ldapCBOpenUntil
	case time.Now().Before(ldapCBOpenUntil):
		return "open", ldapCBFailures, ldapCBOpenUntil
	default:
		return "half-open", ldapCBFailures, ldapCBOpenUntil // the next login is let through
	}
}

// GetCircuitBreakerStatus GET /api/ldap/circuit-breaker
func GetCircuitBreakerStatus(w http.ResponseWriter, r *http.Request) {
	state, failures, openUntil := ldapCBState()
	resp := map[string]any{
		"success":       true,
		"state":         state,
		"failures":      failures,
		"max_failures":  ldapCBThreshold,
		"reset_timeout": ldapCBResetAfter.String(),
	}
	if state == "open" {
		resp["open_until"] = openUntil.UTC().Format(time.RFC3339)
	}
	respondOK(w, resp)
}

// ResetCircuitBreaker POST /api/ldap/circuit-breaker/reset
// The next directory login is tried against the server again.
func ResetCircuitBreaker(w http.ResponseWriter, r *http.Request) {
	ldapCBSuccess()
	respondOK(w, map[string]any{"success": true, "message": "Circuit breaker reset"})
}

// DirectoryHealthCheck is the "directory" subsystem check: whether the
// configured directory server answers logins and the last sync worked.
func DirectoryHealthCheck(db *sql.DB) func(context.Context) monitoring.SubsystemHealth {
	return func(ctx context.Context) monitoring.SubsystemHealth {
		sh := monitoring.SubsystemHealth{Name: "directory", CheckedAt: time.Now(), Status: monitoring.HealthOK}
		var enabled int
		var syncOK sql.NullInt64
		var syncAt, syncMsg sql.NullString
		err := db.QueryRowContext(ctx, `SELECT COALESCE(enabled,0), last_sync_ok, last_sync_at, last_sync_msg FROM ldap_config WHERE id=1`).
			Scan(&enabled, &syncOK, &syncAt, &syncMsg)
		if err == sql.ErrNoRows || (err == nil && enabled == 0) {
			sh.Reason = "No directory configured"
			return sh
		}
		if err != nil {
			sh.Status, sh.Reason = monitoring.HealthUnknown, "Cannot read the directory configuration: "+err.Error()
			return sh
		}
		state, failures, _ := ldapCBState()
		sh.Details = map[string]any{"circuit_breaker": state, "failures": failures}
		switch {
		case state != "closed":
			sh.Status = monitoring.HealthUnavailable
			sh.Reason = "The directory server does not answer: logins of directory users fail, local accounts work"
		case syncAt.Valid && syncAt.String != "" && syncOK.Valid && syncOK.Int64 == 0:
			sh.Status = monitoring.HealthDegraded
			sh.Reason = "The last directory sync failed: " + syncMsg.String
		default:
			sh.Reason = "The directory server answers"
		}
		return sh
	}
}
