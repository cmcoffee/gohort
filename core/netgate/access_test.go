package netgate

import (
	"net/http"
	"testing"
)

func TestIsBrowserRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		// Internal callers: Go's http.Client sets neither Fetch Metadata nor
		// Accept unless asked, so these keep the loopback bypass.
		{"bare go client", nil, false},
		{"internal json rpc", map[string]string{"Content-Type": "application/json"}, false},
		{"internal accepting json", map[string]string{"Accept": "application/json"}, false},

		// Browser navigations.
		{"chrome navigation", map[string]string{
			"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document",
			"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		}, true},

		// The case an Accept-only test would have missed: a browser's XHR
		// asks for */*, so without Fetch Metadata the page would sit behind
		// a login while its API calls stayed wide open.
		{"browser xhr", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
			"Accept": "*/*",
		}, true},
		{"browser eventsource", map[string]string{
			"Sec-Fetch-Dest": "empty", "Accept": "text/event-stream",
		}, true},

		// Pre-Fetch-Metadata browser: the navigation still asks for HTML.
		{"legacy browser navigation", map[string]string{
			"Accept": "text/html,application/xhtml+xml",
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest("GET", "http://127.0.0.1:8080/", nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := IsBrowserRequest(r); got != tc.want {
				t.Errorf("IsBrowserRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The loopback bypass is only safe in combination with the browser test, so
// pin the combination rather than each half on its own.
func TestLoopbackBypassExcludesBrowsers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		wantBypass bool
	}{
		{"internal rpc over loopback", "127.0.0.1:54321", nil, true},
		{"browser on loopback", "127.0.0.1:54321", map[string]string{
			"Sec-Fetch-Mode": "navigate", "Accept": "text/html"}, false},
		{"remote client", "192.168.0.31:54321", nil, false},
		{"remote client claiming loopback", "192.168.0.31:54321", map[string]string{
			"X-Forwarded-For": "127.0.0.1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest("GET", "http://127.0.0.1:8080/", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			got := IsGenuineLocalRequest(r) && !IsBrowserRequest(r)
			if got != tc.wantBypass {
				t.Errorf("bypass = %v, want %v", got, tc.wantBypass)
			}
		})
	}
}
