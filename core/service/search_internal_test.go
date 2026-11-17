package service

import "testing"

// A connector's address is inside the network when every address it has is
// private or loopback; one that does not resolve is not, so a query to it is
// still checked.
func TestAnAddressInsideTheNetworkIsInternal(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:8080/search": true,
		"https://10.1.2.3":             true,
		"http://[::1]:9000":            true,
		"https://8.8.8.8/":             false,
		"":                             false,
		"not a url":                    false,
		"https://name.invalid":         false,
	} {
		if got := addressIsInternal(raw); got != want {
			t.Errorf("%q: %v, want %v", raw, got, want)
		}
	}
}
