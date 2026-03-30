package security

import (
	"net"
	"net/http"
	"strings"
)

// trustedProxyNets is the set of CIDR blocks whose HTTP requests are allowed to
// set X-Forwarded-For / X-Real-IP headers.  Only loopback and RFC 1918 private
// ranges are trusted; anything else is treated as a direct client connection and
// RemoteAddr is used as-is.
var trustedProxyNets []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 ULA (fd00::/8 and fc00::/8)
	} {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			trustedProxyNets = append(trustedProxyNets, network)
		}
	}
}

// isTrustedProxy returns true if ip falls within a trusted proxy range.
func isTrustedProxy(ip net.IP) bool {
	for _, network := range trustedProxyNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// RealIP extracts the real client IP for rate limiting and logging.
// X-Forwarded-For and X-Real-IP headers are only honoured when the direct
// connection comes from a trusted proxy (loopback or RFC 1918).  All other
// connections use RemoteAddr directly, preventing header spoofing.
func RealIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !isTrustedProxy(ip) {
		return host // direct connection or untrusted source – use RemoteAddr
	}

	// Request arrived from a trusted proxy – honour forwarded headers.
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
