package sourcehooks

// The public-only client checks the address it actually dials, so a name that
// resolves inward, or a public page that redirects inward, is refused.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestThePublicClientRefusesInwardAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("internal")) }))
	defer srv.Close()
	if _, err := NewPublicHTTPClient().Get(srv.URL); err == nil {
		t.Fatal("the public client reached a loopback server")
	}
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "169.254.169.254": true, "100.100.1.1": true,
		"::1": true, "fd00::1": true, "64:ff9b::a00:1": true, "0.0.0.0": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700:4700::1111": false,
	} {
		if got := NonPublicIP(net.ParseIP(addr)); got != want {
			t.Errorf("NonPublicIP(%s) = %v, want %v", addr, got, want)
		}
	}
}
