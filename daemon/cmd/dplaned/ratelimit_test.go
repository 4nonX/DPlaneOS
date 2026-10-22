package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dplaned/internal/middleware"
)

// 100 requests per minute per address; the 101st gets 429. The chunks of an
// upload by a signed-in user are not counted (a 1 GB file is 100 chunks), but
// an anonymous request to the upload path is.
func TestRateLimitMiddleware(t *testing.T) {
	rateLimitMu.Lock()
	requestCounts = map[string][]time.Time{}
	rateLimitMu.Unlock()
	h := rateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	send := func(path, ip string, signedIn bool) int {
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = ip + ":1234"
		if signedIn {
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, &middleware.User{Username: "admin"}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	for i := 0; i < 300; i++ {
		if code := send("/api/files/upload", "192.0.2.10", true); code != 200 {
			t.Fatalf("upload chunk %d: %d", i, code)
		}
	}
	for i := 0; i < 100; i++ {
		if code := send("/api/zfs/pools", "192.0.2.10", true); code != 200 {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if code := send("/api/zfs/pools", "192.0.2.10", true); code != http.StatusTooManyRequests {
		t.Errorf("101st request: %d, want 429", code)
	}
	if code := send("/api/files/upload", "192.0.2.10", false); code != http.StatusTooManyRequests {
		t.Errorf("anonymous upload over the limit: %d, want 429", code)
	}
	if code := send("/api/zfs/pools", "192.0.2.11", true); code != 200 {
		t.Errorf("another address: %d", code)
	}

	// Idle addresses are dropped.
	rateLimitMu.Lock()
	requestCounts["192.0.2.99"] = []time.Time{time.Now().Add(-2 * time.Minute)}
	rateLimitSweep = time.Time{}
	rateLimitMu.Unlock()
	send("/api/zfs/pools", "192.0.2.12", true)
	rateLimitMu.Lock()
	_, kept := requestCounts["192.0.2.99"]
	rateLimitMu.Unlock()
	if kept {
		t.Error("an idle address was not swept")
	}
}
