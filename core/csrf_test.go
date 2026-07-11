package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func csrfReq(host, origin, referer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://"+host+"/x", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	return r
}

func TestSameOriginRequest(t *testing.T) {
	cases := []struct {
		name            string
		host            string
		origin, referer string
		want            bool
	}{
		{"same origin", "app.example.com", "https://app.example.com", "", true},
		{"cross origin", "app.example.com", "https://evil.com", "", false},
		{"port mismatch is cross-origin", "app.example.com:8181", "https://app.example.com:9999", "", false},
		{"same host:port", "app.example.com:8181", "https://app.example.com:8181", "", true},
		{"referer fallback same", "app.example.com", "", "https://app.example.com/page", true},
		{"referer fallback cross", "app.example.com", "", "https://evil.com/page", false},
		{"origin wins over referer", "app.example.com", "https://app.example.com", "https://evil.com/x", true},
		{"neither present fails open", "app.example.com", "", "", true},
		{"malformed origin rejected", "app.example.com", "://bad", "", false},
		{"host compare is case-insensitive", "App.Example.com", "https://app.example.com", "", true},
		// gohort-desktop webview: loopback origin → remote server host. Allowed.
		{"desktop loopback 127.0.0.1", "gohort.example.com", "http://127.0.0.1:34567", "", true},
		{"desktop localhost", "gohort.example.com", "http://localhost:8080", "", true},
		{"desktop wails.localhost", "gohort.example.com", "http://wails.localhost", "", true},
		{"desktop ipv6 loopback", "gohort.example.com", "http://[::1]:9000", "", true},
		// A remote site can never present a loopback origin — normal cross-origin stays blocked.
		{"remote evil not loopback", "gohort.example.com", "https://evil.com", "", false},
	}
	for _, c := range cases {
		if got := SameOriginRequest(csrfReq(c.host, c.origin, c.referer)); got != c.want {
			t.Errorf("%s: SameOriginRequest = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsStateChangingMethod(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !IsStateChangingMethod(m) {
			t.Errorf("%s should be state-changing", m)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if IsStateChangingMethod(m) {
			t.Errorf("%s should be treated as safe", m)
		}
	}
}
