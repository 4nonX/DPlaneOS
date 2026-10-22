package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Endpoints called before a session exists must pass the session middleware
// (their handlers authenticate the request themselves); everything else
// without a session is refused.
func TestSessionMiddlewarePublicPaths(t *testing.T) {
	reached := false
	h := sessionMiddleware(nil, "tok")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	for _, c := range []struct {
		method, path string
		public       bool
	}{
		{"POST", "/api/auth/login", true},
		{"POST", "/api/auth/totp/verify", true}, // second login step: pending token, no session yet
		{"POST", "/api/gitops/webhook", true},   // HMAC signature or token
		{"POST", "/api/config/sync/peer/gitops", true},
		{"POST", "/api/git-sync/repos/webhook/3", true},       // stack push notification: signature
		{"POST", "/api/git-sync/repos/webhook-secret", false}, // creating the secret needs a session
		{"POST", "/api/auth/totp/setup", false},
		{"GET", "/api/gitops/status", false},
		{"POST", "/api/gitops/../auth/totp/setup", false},
	} {
		reached = false
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(c.method, c.path, nil))
		if reached != c.public {
			t.Errorf("%s %s: reached=%v (HTTP %d), want public=%v", c.method, c.path, reached, rr.Code, c.public)
		}
	}
}
