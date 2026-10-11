package handlers

import (
	"context"
	"testing"
	"time"

	"dplaned/internal/monitoring"
	"dplaned/internal/resource"
)

func resetLDAPBreaker(t *testing.T) {
	t.Helper()
	ldapCBSuccess()
	t.Cleanup(ldapCBSuccess)
}

// The status endpoint reports the breaker the login path uses.
func TestCircuitBreakerStatusFollowsLoginBreaker(t *testing.T) {
	resetLDAPBreaker(t)
	if r := call(t, GetCircuitBreakerStatus, req{}); r.body["state"] != "closed" || r.body["failures"].(float64) != 0 {
		t.Fatalf("fresh: %s", r)
	}
	for i := 0; i < ldapCBThreshold; i++ {
		ldapCBFailure()
	}
	if ldapCBAllow() {
		t.Fatal("logins must fail fast while the breaker is open")
	}
	r := call(t, GetCircuitBreakerStatus, req{})
	if r.body["state"] != "open" || r.body["failures"].(float64) != ldapCBThreshold || r.body["open_until"] == nil {
		t.Fatalf("after %d failures: %s", ldapCBThreshold, r)
	}

	// After the reset time the next login is let through.
	ldapCBMu.Lock()
	ldapCBOpenUntil = time.Now().Add(-time.Second)
	ldapCBMu.Unlock()
	if r := call(t, GetCircuitBreakerStatus, req{}); r.body["state"] != "half-open" {
		t.Fatalf("after the reset time: %s", r)
	}

	if r := call(t, ResetCircuitBreaker, req{method: "POST"}); !r.ok() {
		t.Fatalf("reset: %s", r)
	}
	if r := call(t, GetCircuitBreakerStatus, req{}); r.body["state"] != "closed" || !ldapCBAllow() {
		t.Fatalf("after reset: %s", r)
	}
}

func TestDirectoryHealthCheck(t *testing.T) {
	db := testDB(t)
	resetLDAPBreaker(t)
	check := DirectoryHealthCheck(db)

	if _, err := db.Exec(`INSERT INTO ldap_config (id, enabled) VALUES (1, 0) ON CONFLICT (id) DO UPDATE SET enabled = 0`); err != nil {
		t.Fatal(err)
	}
	if h := check(context.Background()); h.Status != monitoring.HealthOK || h.Reason != "No directory configured" {
		t.Errorf("no directory: %+v", h)
	}

	if _, err := db.Exec(`UPDATE ldap_config SET enabled = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if h := check(context.Background()); h.Status != monitoring.HealthOK {
		t.Errorf("enabled, reachable: %+v", h)
	}

	if _, err := db.Exec(`UPDATE ldap_config SET last_sync_at = '2026-10-10T00:00:00Z', last_sync_ok = 0, last_sync_msg = 'bind failed' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if h := check(context.Background()); h.Status != monitoring.HealthDegraded {
		t.Errorf("failed sync: %+v", h)
	}

	for i := 0; i < ldapCBThreshold; i++ {
		ldapCBFailure()
	}
	if h := check(context.Background()); h.Status != monitoring.HealthUnavailable {
		t.Errorf("breaker open: %+v", h)
	}
}

func TestResourceAlertLevels(t *testing.T) {
	ok := resource.ResourceStatus{Status: "OK"}
	low := resource.ResourceStatus{Status: "DEGRADED", Warnings: []string{"system disk (/) 92% full, 3.0 GB free"}}
	crit := resource.ResourceStatus{Status: "CRITICAL", Warnings: []string{"system disk (/) 98% full, 0.4 GB free"}}
	for _, c := range []struct {
		prev, cur resource.ResourceStatus
		level     string
	}{{ok, low, "warning"}, {low, crit, "critical"}, {crit, ok, "info"}} {
		level, msg := resourceAlert(c.prev, c.cur)
		if level != c.level || msg == "" {
			t.Errorf("%s -> %s: %s %q", c.prev.Status, c.cur.Status, level, msg)
		}
	}
	// Dispatch reaches the configured channels.
	var got []string
	SetAlertDispatchers(func(_, event, res, msg string) { got = append(got, event+" "+res) }, nil, nil, nil)
	t.Cleanup(func() { SetAlertDispatchers(nil, nil, nil, nil) })
	AlertResourceChange(ok, crit)
	if len(got) != 1 || got[0] != "system.resources system" {
		t.Errorf("dispatched: %v", got)
	}
}
