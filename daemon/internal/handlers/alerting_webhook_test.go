package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type received struct {
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
}

func webhookReceiver(t *testing.T, status int) (*httptest.Server, *received) {
	rec := &received{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(b))
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *received) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.bodies)
		r.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("webhook not received (%d of %d)", len(r.bodies), n)
}

func TestValidateWebhookURL(t *testing.T) {
	for _, ok := range []string{"https://ntfy.sh/topic", "http://192.168.1.10:8123/api/webhook/x"} {
		if validateWebhookURL(ok) != nil {
			t.Errorf("%s rejected", ok)
		}
	}
	for _, bad := range []string{"ftp://x", "http://", "https:///path", "javascript:alert(1)"} {
		if validateWebhookURL(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestWebhookDialControl(t *testing.T) {
	for _, bad := range []string{"169.254.169.254:80", "[fe80::1]:80", "0.0.0.0:80", "224.0.0.1:80"} {
		if webhookDialControl("tcp", bad, nil) == nil {
			t.Errorf("%s allowed", bad)
		}
	}
	for _, ok := range []string{"127.0.0.1:8080", "192.168.1.10:443", "[2001:db8::1]:443"} {
		if err := webhookDialControl("tcp", ok, nil); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
}

// A JSON body template gets JSON-escaped values: a message with quotes must
// not break the body (the alert would be rejected and lost).
func TestWebhookTemplateEscapesJSON(t *testing.T) {
	srv, rec := webhookReceiver(t, 200)
	cfg := webhookConfig{URL: srv.URL, ContentType: "application/json",
		BodyTemplate: `{"event":"{{event}}","message":"{{message}}"}`}
	msg := `pool "tank" is DEGRADED` + "\n" + `cannot open '/dev/sdb'`
	if err := dispatchWebhook(cfg, webhookPayload{Event: "pool.degraded", Message: msg}); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(rec.bodies[0]), &got); err != nil {
		t.Fatalf("invalid JSON body %q: %v", rec.bodies[0], err)
	}
	if got["message"] != msg {
		t.Errorf("message %q", got["message"])
	}
	// Plain-text templates are not escaped.
	cfg = webhookConfig{URL: srv.URL, ContentType: "text/plain", BodyTemplate: "{{message}}"}
	if err := dispatchWebhook(cfg, webhookPayload{Message: msg}); err != nil {
		t.Fatal(err)
	}
	if rec.bodies[1] != msg {
		t.Errorf("text body %q", rec.bodies[1])
	}
}

func TestWebhookFromWebUIFires(t *testing.T) {
	db := testDB(t)
	h := NewWebhookHandler(db, "test")
	srv, rec := webhookReceiver(t, 200)

	// What the Alerts page sends: headers map, method, no enabled flag.
	r := call(t, h.SaveWebhook, req{method: "POST", body: map[string]any{
		"name": "ntfy", "url": srv.URL, "method": "POST",
		"headers":       map[string]string{"Content-Type": "application/json", "Authorization": "Bearer abc"},
		"body_template": `{"message":"{{message}}"}`,
	}})
	if !r.ok() {
		t.Fatalf("save: %s", r)
	}
	var enabled int
	var hdr, val string
	_ = db.QueryRow(`SELECT enabled, secret_header, secret_value FROM webhook_configs WHERE name = 'ntfy'`).Scan(&enabled, &hdr, &val)
	if enabled != 1 || hdr != "Authorization" || val != "Bearer abc" {
		t.Fatalf("stored enabled=%d header=%q value=%q", enabled, hdr, val)
	}

	SendWebhookAlert(db, "pool.degraded", "critical", `pool "tank" degraded`, map[string]any{"pool": "tank"})
	rec.wait(t, 1)
	if rec.headers[0].Get("Authorization") != "Bearer abc" {
		t.Errorf("custom header not sent: %v", rec.headers[0])
	}

	list := call(t, h.ListWebhooks, req{})
	if strings.Contains(list.raw, "Bearer abc") || !strings.Contains(list.raw, `"Authorization":"********"`) {
		t.Errorf("list must mask the header value: %s", list)
	}

	for name, body := range map[string]map[string]any{
		"GET":            {"name": "x", "url": srv.URL, "method": "GET"},
		"two headers":    {"name": "x", "url": srv.URL, "headers": map[string]string{"A": "1", "B": "2"}},
		"header newline": {"name": "x", "url": srv.URL, "headers": map[string]string{"A": "1\r\nX-Evil: 1"}},
		"bad url":        {"name": "x", "url": "http://"},
	} {
		if r := call(t, h.SaveWebhook, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	// Explicitly disabled webhooks are kept and do not fire.
	if r := call(t, h.SaveWebhook, req{method: "POST", body: map[string]any{"name": "off", "url": srv.URL, "enabled": false}}); !r.ok() {
		t.Fatalf("save disabled: %s", r)
	}
	_ = db.QueryRow(`SELECT enabled FROM webhook_configs WHERE name = 'off'`).Scan(&enabled)
	if enabled != 0 {
		t.Error("enabled=false not stored")
	}
}

func TestTestWebhookReportsFailures(t *testing.T) {
	db := testDB(t)
	h := NewWebhookHandler(db, "test")
	srv, _ := webhookReceiver(t, 500)
	r := call(t, h.SaveWebhook, req{method: "POST", body: map[string]any{"name": "broken", "url": srv.URL}})
	if !r.ok() {
		t.Fatalf("save: %s", r)
	}
	var id int
	_ = db.QueryRow(`SELECT id FROM webhook_configs WHERE name = 'broken'`).Scan(&id)
	res := call(t, h.TestWebhook, req{method: "POST", vars: map[string]string{"id": strconv.Itoa(id)}})
	if res.ok() || !strings.Contains(res.raw, "500") {
		t.Errorf("a 500 from the receiver must be reported: %s", res)
	}
}
