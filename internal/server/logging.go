package server

import (
	"net"
	"net/http"
	"strings"
)

// Only the loopback reverse proxy may supply a forwarded client address.
// Caddy appends its peer at the right, so left-hand user-supplied entries
// cannot impersonate another address. Never log raw forwarding headers.
func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return "unknown"
	}
	if peer.IsLoopback() {
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if ip := net.ParseIP(strings.TrimSpace(forwarded[len(forwarded)-1])); ip != nil {
			return ip.String()
		}
	}
	return peer.String()
}
