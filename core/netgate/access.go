package netgate

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
	if IsGenuineLocalRequest(r) {
		return true
	}
	ip := ClientIP(r)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsLoopbackRequest reports whether a request genuinely originates from the
// local machine (an internal inter-app RPC). Internal calls loop back to
// localhost, so gates that protect against external abuse whitelist loopback —
// otherwise the server blocks its own RPCs. This is a SECURITY decision, so it
// uses IsGenuineLocalRequest (the real TCP peer), NOT ClientIP: an external
// client can set "X-Forwarded-For: 127.0.0.1" and ClientIP would trust it.
func IsLoopbackRequest(r *http.Request) bool {
	return IsGenuineLocalRequest(r)
}

// directPeerIP returns the IP of the actual TCP peer (r.RemoteAddr), ignoring
// forwarding headers. This is the only trustworthy origin for a security
// decision — X-Forwarded-For / X-Real-IP are client-supplied and spoofable.
func directPeerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// IsGenuineLocalRequest reports whether a request truly originated on this
// machine as an internal RPC — safe to use for an auth bypass. It requires BOTH
// that the real TCP peer is loopback AND that no forwarding header is present:
// an internal call connects directly over loopback and never sets X-Forwarded-
// For / X-Real-IP / Forwarded, whereas anything arriving through a proxy (even
// one co-located on localhost) carries one. This closes the spoof where an
// external client sends "X-Forwarded-For: 127.0.0.1" to impersonate loopback.
func IsGenuineLocalRequest(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Real-IP") != "" ||
		r.Header.Get("Forwarded") != "" {
		return false
	}
	ip := directPeerIP(r)
	return ip != nil && ip.IsLoopback()
}

// IsBrowserRequest reports whether a request came from a web browser rather
// than from an internal HTTP client. It exists to qualify the loopback auth
// bypass: an internal RPC looping back over localhost should skip auth, but a
// person pointing a browser at 127.0.0.1 is indistinguishable from one at the
// TCP layer and should not.
//
// The primary signal is the Fetch Metadata headers. Every current browser
// sends them on EVERY request — navigations, fetch/XHR, EventSource, images —
// and Go's http.Client sends none of them, so the split is clean in both
// directions. Testing Accept for text/html alone would not be: a browser's
// XHR asks for */* and would have kept the bypass, leaving the page behind a
// login while its API calls stayed open.
//
// The Accept check remains as a fallback for browsers predating Fetch
// Metadata, where a document navigation is still the case that matters most.
func IsBrowserRequest(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") != "" ||
		r.Header.Get("Sec-Fetch-Mode") != "" ||
		r.Header.Get("Sec-Fetch-Dest") != "" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
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

// TrustedProxiesFunc returns a comma-separated list of CIDR blocks (or bare
// IPs) of reverse proxies whose X-Forwarded-For / X-Real-IP are believed, in
// addition to loopback. Empty or unset: loopback only. Set by the application
// from stored config.
var TrustedProxiesFunc func() string

// trustedProxy reports whether ip is a proxy whose forwarding headers count.
func trustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if TrustedProxiesFunc == nil {
		return false
	}
	for _, n := range parseCIDRList(TrustedProxiesFunc()) {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the originating client IP.
//
// Forwarding headers are believed ONLY from a trusted proxy (loopback, or
// TrustedProxiesFunc), because anybody can send them: believing the FIRST
// X-Forwarded-For hop, as this used to, let any client name its own address,
// which walked straight past the login lockout (a fresh address per guess)
// and the admin IP allowlist (send an allowlisted address). A proxy appends
// the address it saw to the RIGHT of whatever the client sent, so the client
// is the rightmost hop that is not itself a trusted proxy.
func ClientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(strings.TrimSpace(host))
	if !trustedProxy(peer) {
		return peer
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(hops[i]))
			if ip == nil {
				break // malformed from here left: stop at what the proxies wrote
			}
			if !trustedProxy(ip) {
				return ip
			}
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		if ip := net.ParseIP(strings.TrimSpace(xr)); ip != nil {
			return ip
		}
	}
	return peer
}
