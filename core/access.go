package core

import (
	"net"
	"net/http"
	"strings"
)

// LoadTechwriterAllowedIPsFunc returns a comma-separated list of CIDR
// blocks (or bare IPs) permitted to access TechWriter and "Push to
// TechWriter" actions in other apps. Empty string means open (default).
// Set by the application from stored config.
var LoadTechwriterAllowedIPsFunc func() string

// LoadAdminAllowedIPsFunc returns a comma-separated list of CIDR
// blocks (or bare IPs) permitted to access the Administrator panel.
// Empty string means no IP restriction (auth-only). Set by the
// application from stored config.
var LoadAdminAllowedIPsFunc func() string

// IsTechwriterAllowed reports whether the request originates from an IP
// in the configured TechWriter allowlist. An empty or unconfigured
// allowlist allows everyone (backwards-compatible default).
func IsTechwriterAllowed(r *http.Request) bool {
	if LoadTechwriterAllowedIPsFunc == nil {
		return true
	}
	list := strings.TrimSpace(LoadTechwriterAllowedIPsFunc())
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
	// Always allow localhost — internal API calls between apps.
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
