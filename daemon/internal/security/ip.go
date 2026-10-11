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

// Local is what RealIP returns for a request made on this machine itself
// through the daemon's socket (a timer, a command), not through the proxy.
const Local = "local"

// forwardedIP returns the client address a proxy put into the headers, if
// it is an address at all.
func forwardedIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" && net.ParseIP(v) != nil {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if idx := strings.Index(v, ","); idx != -1 {
			v = v[:idx]
		}
		if v = strings.TrimSpace(v); net.ParseIP(v) != nil {
			return v
		}
	}
	return ""
}

// RealIP returns the address of the client, for rate limiting and logging.
//
// On an installed system every request arrives through the web server on
// this machine, over the daemon's socket: the connection itself has no
// address then, and the web server passes the client's in X-Real-IP (it
// overwrites whatever the client sent). Without that header the request
// was made on this machine.
//
// On a TCP connection the headers are honoured only when the peer is a
// trusted proxy (loopback or a private range); any other peer is the client.
func RealIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The daemon's socket ("@" or empty): only the web server and
		// local administrators can connect to it.
		if v := forwardedIP(r); v != "" {
			return v
		}
		return Local
	}
	if !isTrustedProxy(ip) {
		return host
	}
	if v := forwardedIP(r); v != "" {
		return v
	}
	return host
}

// IsLocal reports whether the request was made on this machine itself.
func IsLocal(r *http.Request) bool {
	v := RealIP(r)
	if v == Local {
		return true
	}
	ip := net.ParseIP(v)
	return ip != nil && ip.IsLoopback()
}
