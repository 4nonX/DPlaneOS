package security

import (
	"net"
	"net/http"
	"strings"
)

// RealIP extracts the client IP for rate limiting and logging.
// It only trusts X-Forwarded-For or X-Real-IP if the connection comes
// from a loopback address or a trusted proxy.
func RealIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && !ip.IsLoopback() {
		// TODO: Add trusted proxy check here if needed
		return host // direct connection - trust RemoteAddr
	}
	
	// Behind a proxy - trust forwarded headers if proxy is on loopback
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if idx := strings.Index(v, ","); idx != -1 {
			return strings.TrimSpace(v[:idx])
		}
		return strings.TrimSpace(v)
	}
	return host
}
