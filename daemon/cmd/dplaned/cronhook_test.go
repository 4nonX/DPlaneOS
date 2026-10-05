package main

import (
	"net/http/httptest"
	"testing"
)

func TestIsInternalCronHook(t *testing.T) {
	const tok = "abc123"
	cases := []struct {
		name, path, remote, token string
		want                      bool
	}{
		{"unix socket", "/api/zfs/snapshots/cron-hook", "@", tok, true},
		{"unix socket empty addr", "/api/backup/rsync/cron-hook", "", tok, true},
		{"loopback v4", "/api/hardware/smart/cron-hook", "127.0.0.1:5555", tok, true},
		{"loopback v6", "/api/zfs/snapshots/cron-hook", "[::1]:5555", tok, true},
		{"remote", "/api/zfs/snapshots/cron-hook", "192.168.1.9:5555", tok, false},
		{"loopback lookalike", "/api/zfs/snapshots/cron-hook", "127.0.0.100:1", tok, false},
		{"wrong token", "/api/zfs/snapshots/cron-hook", "@", "nope", false},
		{"no token", "/api/zfs/snapshots/cron-hook", "@", "", false},
		{"other path", "/api/zfs/snapshots", "@", tok, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", c.path, nil)
		r.RemoteAddr = c.remote
		if c.token != "" {
			r.Header.Set("X-Internal-Token", c.token)
		}
		if got := isInternalCronHook(r, tok); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	r := httptest.NewRequest("POST", "/api/zfs/snapshots/cron-hook", nil)
	r.RemoteAddr = "@"
	if isInternalCronHook(r, "") {
		t.Error("empty daemon token must never match")
	}
}
