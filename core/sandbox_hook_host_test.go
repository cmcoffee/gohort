package core

import "testing"

func TestIsNonPublicHost(t *testing.T) {
	nonPublic := []string{"", "localhost", "127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "0.0.0.0", "foo.local", "bar.internal"}
	for _, h := range nonPublic {
		if !IsNonPublicHost(h) {
			t.Errorf("%q should be non-public", h)
		}
	}
	public := []string{"i.redd.it", "example.com", "8.8.8.8", "1.1.1.1", "graph.microsoft.com"}
	for _, h := range public {
		if IsNonPublicHost(h) {
			t.Errorf("%q should be public", h)
		}
	}
}
