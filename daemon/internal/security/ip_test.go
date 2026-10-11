package security

import (
	"net/http"
	"testing"
)

func TestRealIP(t *testing.T) {
	for _, c := range []struct {
		name, remote, realIP, xff, want string
		local                           bool
	}{
		{"through the web server", "@", "203.0.113.7", "", "203.0.113.7", false},
		{"through the web server, forwarded-for only", "", "", "203.0.113.8, 10.0.0.1", "203.0.113.8", false},
		{"on this machine, over the socket", "@", "", "", Local, true},
		{"on this machine, through the web server", "@", "127.0.0.1", "", "127.0.0.1", true},
		{"a header that is no address", "@", "<script>", "also not", Local, true},
		{"direct, public peer: headers are not believed", "198.51.100.9:4000", "10.1.1.1", "", "198.51.100.9", false},
		{"direct, from a proxy in a private range", "10.0.0.5:4000", "203.0.113.9", "", "203.0.113.9", false},
		{"direct from loopback", "127.0.0.1:5000", "", "", "127.0.0.1", true},
	} {
		r, _ := http.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.realIP != "" {
			r.Header.Set("X-Real-IP", c.realIP)
		}
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := RealIP(r); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if got := IsLocal(r); got != c.local {
			t.Errorf("%s: local=%v", c.name, got)
		}
	}
}
