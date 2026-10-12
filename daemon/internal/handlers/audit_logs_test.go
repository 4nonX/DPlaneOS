package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The audit log as the Audit Log page reads it: newest first, in pages,
// searched, and narrowed to failures.
func TestAuditLogsListing(t *testing.T) {
	db := testDB(t)
	h := NewAuditRotationHandler(db, "", t.TempDir()+"/audit.key")
	for i, e := range []struct {
		actor, action, details string
		ok                     int
	}{
		{"admin", "login", "Signed in", 1},
		{"mallory", "login_failed", "Wrong password", 0},
		{"admin", "dataset_create", "tank/data", 1},
	} {
		if _, err := db.Exec(`INSERT INTO audit_logs (timestamp, actor, action, resource, details, ip_address, success)
			VALUES ($1, $2, $3, '', $4, '192.0.2.10', $5)`, 1790000000+i, e.actor, e.action, e.details, e.ok); err != nil {
			t.Fatal(err)
		}
	}
	list := func(query string) []map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h.GetAuditLogs(w, httptest.NewRequest(http.MethodGet, "/api/system/audit/logs"+query, nil))
		var out struct {
			Success bool             `json:"success"`
			Logs    []map[string]any `json:"logs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !out.Success {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
		return out.Logs
	}

	all := list("")
	if len(all) != 3 || all[0]["action"] != "dataset_create" || all[2]["action"] != "login" {
		t.Fatalf("all, newest first: %v", all)
	}
	if all[1]["success"] != false || all[1]["ip_address"] != "192.0.2.10" || all[1]["timestamp"] == "" {
		t.Errorf("entry: %v", all[1])
	}
	if got := list("?level=error"); len(got) != 1 || got[0]["actor"] != "mallory" {
		t.Errorf("failures only: %v", got)
	}
	if got := list("?search=TANK"); len(got) != 1 || got[0]["details"] != "tank/data" {
		t.Errorf("search is case-insensitive over the details: %v", got)
	}
	if got := list("?search=mallory&level=error"); len(got) != 1 {
		t.Errorf("search and failures together: %v", got)
	}
	if got := list("?limit=2&offset=2"); len(got) != 1 || got[0]["action"] != "login" {
		t.Errorf("second page: %v", got)
	}
	// Nothing matching is an empty list, not null: the page reads its length.
	w := httptest.NewRecorder()
	h.GetAuditLogs(w, httptest.NewRequest(http.MethodGet, "/api/system/audit/logs?search=nobody-by-that-name", nil))
	var raw map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &raw)
	if string(raw["logs"]) != "[]" {
		t.Errorf("no match: logs = %s", raw["logs"])
	}
}
