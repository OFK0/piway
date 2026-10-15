package auth

import (
	"net"
	"net/http"
	"strings"
)

// A reverse proxy on the Pi, such as "tailscale serve" for remote access
// (docs/REMOTE.md), connects to piway from 127.0.0.1 on behalf of clients
// elsewhere. The proxy says so in its headers; trusting them only when
// the connection itself is from loopback means a remote client cannot
// forge them.

// proxyHeaders mark a request that a proxy forwarded.
var proxyHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "Tailscale-User-Login"}

// local reports whether r comes from the Pi itself, not through a proxy:
// the only requests trusted while no password is set.
func local(r *http.Request) bool {
	if !isLoopback(r.RemoteAddr) {
		return false
	}
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	return true
}

// clientAddr is the address wrong passwords are counted against: the
// connection's, or for a request a local proxy forwarded, the address the
// proxy saw (the last X-Forwarded-For entry, which the proxy appended).
func clientAddr(r *http.Request) string {
	host := clientKey(r.RemoteAddr)
	if !isLoopback(r.RemoteAddr) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return host
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
		return ip.String()
	}
	return host
}

// secure reports whether the browser reached piway over HTTPS, directly
// or through a local proxy that terminated TLS, so the session cookie can
// be marked Secure.
func secure(r *http.Request) bool {
	return r.TLS != nil || (isLoopback(r.RemoteAddr) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}
