package netgate

// The admin allowlist holds for everyone but this process's own calls, and
// those have to prove it: a reverse proxy on the same machine that sends no
// X-Forwarded-For makes every visitor a loopback peer, so "looks local" let
// anyone past it.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheAdminAllowlistExemptsOnlyProvenInternalCalls(t *testing.T) {
	prevList, prevInternal := LoadAdminAllowedIPsFunc, InternalRequestFunc
	t.Cleanup(func() { LoadAdminAllowedIPsFunc, InternalRequestFunc = prevList, prevInternal })
	list := "10.0.0.0/8"
	LoadAdminAllowedIPsFunc = func() string { return list }
	internal := false
	InternalRequestFunc = func(*http.Request) bool { return internal }

	req := func(remote string) *http.Request {
		r := httptest.NewRequest("GET", "/admin/", nil)
		r.RemoteAddr = remote
		return r
	}
	if IsAdminAllowed(req("127.0.0.1:5000")) {
		t.Error("a loopback peer with no proof passed the allowlist: a same-host proxy makes every visitor one")
	}
	internal = true
	if !IsAdminAllowed(req("127.0.0.1:5000")) {
		t.Error("this process's own proven call was refused")
	}
	internal = false
	if !IsAdminAllowed(req("10.1.2.3:5000")) {
		t.Error("an address on the list was refused")
	}
	list = "10.0.0.0/8, 127.0.0.1"
	if !IsAdminAllowed(req("127.0.0.1:5000")) {
		t.Error("127.0.0.1, listed (an SSH tunnel from this machine), was refused")
	}
	InternalRequestFunc = nil
	list = "10.0.0.0/8"
	if IsAdminAllowed(req("127.0.0.1:5000")) {
		t.Error("with no proof wired, a loopback peer passed")
	}
}

// A proxy on another host, once listed in trusted_proxies, is believed for
// who the client is and whether it came over HTTPS; one not listed is not.
func TestARemoteProxyIsBelievedOnlyWhenTrusted(t *testing.T) {
	prev := TrustedProxiesFunc
	t.Cleanup(func() { TrustedProxiesFunc = prev })
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.2:5000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	r.Header.Set("X-Forwarded-Proto", "https")

	TrustedProxiesFunc = nil
	if ClientIP(r).String() != "172.18.0.2" || ForwardedHTTPS(r) {
		t.Error("an unlisted remote proxy was believed")
	}
	TrustedProxiesFunc = func() string { return "172.18.0.0/16" }
	if got := ClientIP(r).String(); got != "198.51.100.7" {
		t.Errorf("a trusted remote proxy's client = %s, want 198.51.100.7", got)
	}
	if !ForwardedHTTPS(r) {
		t.Error("a trusted remote proxy's HTTPS was not believed, so the session cookie would lose Secure")
	}
}
