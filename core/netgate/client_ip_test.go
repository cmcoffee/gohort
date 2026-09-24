package netgate

// A client cannot name its own address: forwarding headers count only from a
// trusted proxy, and then the client is the rightmost hop the proxies did not
// write themselves.

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPBelievesOnlyTrustedProxies(t *testing.T) {
	prev := TrustedProxiesFunc
	t.Cleanup(func() { TrustedProxiesFunc = prev })
	TrustedProxiesFunc = func() string { return "10.0.0.5" }

	cases := []struct {
		name, remote, xff, real, want string
	}{
		{"direct client, spoofed header", "203.0.113.9:4000", "192.0.2.1", "", "203.0.113.9"},
		{"direct client, spoofed real-ip", "203.0.113.9:4000", "", "192.0.2.1", "203.0.113.9"},
		{"local proxy, honest chain", "127.0.0.1:5000", "198.51.100.7", "", "198.51.100.7"},
		{"local proxy, client prepended a lie", "127.0.0.1:5000", "192.0.2.1, 198.51.100.7", "", "198.51.100.7"},
		{"two trusted proxies", "127.0.0.1:5000", "192.0.2.1, 198.51.100.7, 10.0.0.5", "", "198.51.100.7"},
		{"configured proxy with real-ip", "10.0.0.5:5000", "", "198.51.100.8", "198.51.100.8"},
		{"untrusted LAN peer", "10.0.0.6:5000", "198.51.100.7", "", "10.0.0.6"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.real != "" {
			r.Header.Set("X-Real-IP", c.real)
		}
		if got := ClientIP(r).String(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
