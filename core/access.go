package core

import (
	"net"
	"net/http"
	"strings"
)

// LoadAdminAllowedIPsFunc returns a comma-separated list of CIDR
// blocks (or bare IPs) permitted to access the Administrator panel.
// Empty string means no IP restriction (auth-only). Set by the
// application from stored config.
var LoadAdminAllowedIPsFunc func() string

// IsAdminAllowed reports whether the request originates from an IP
// in the configured Admin allowlist. An empty or unconfigured
// allowlist allows everyone (auth-only, no IP restriction).
func IsAdminAllowed(r *http.Request) bool {
	if LoadAdminAllowedIPsFunc == nil {
		return true
	}
	list := strings.TrimSpace(LoadAdminAllowedIPsFunc())
	if list == "" {
		return true
	}
	nets := parseCIDRList(list)
	if len(nets) == 0 {
		return true
	}
	ip := clientIP(r)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsLoopbackRequest reports whether a request originates from the
// local machine (127.0.0.0/8, ::1). Internal inter-app HTTP calls
// loop back to localhost, so gates that protect against external
// abuse should whitelist loopback — otherwise the server blocks its
// own RPCs. Uses the same clientIP extraction as IsAdminAllowed so
// X-Forwarded-For + X-Real-IP are honored in proxied deployments.
func IsLoopbackRequest(r *http.Request) bool {
	ip := clientIP(r)
	return ip != nil && ip.IsLoopback()
}

// parseCIDRList parses a comma-separated list of CIDRs or bare IPs into
// IPNets. Bare IPs are treated as /32 (v4) or /128 (v6).
func parseCIDRList(list string) []*net.IPNet {
	var out []*net.IPNet
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			if strings.Contains(s, ":") {
				s += "/128"
			} else {
				s += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// clientIP extracts the originating client IP from the request. It honors
// X-Forwarded-For (first hop) and X-Real-IP for proxied deployments.
func clientIP(r *http.Request) net.IP {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		if ip := net.ParseIP(strings.TrimSpace(xr)); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}
