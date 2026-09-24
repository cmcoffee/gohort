package core

// A credentialed request does not follow a redirect off its credential's host
// or outside its allowed endpoints.

import (
	"net/http"
	"net/url"
	"testing"
)

func TestACredentialedRequestStaysOnItsHost(t *testing.T) {
	check := credentialRedirectCheck(SecureCredential{
		Name: "crm", BaseURL: "https://api.crm.example", AllowedEndpoints: []string{"/v1/*"},
		DeniedURLPatterns: []string{"https://api.crm.example/v1/billing*"},
	})
	first := &http.Request{URL: mustURL(t, "https://api.crm.example/v1/items")}
	for target, ok := range map[string]bool{
		"https://api.crm.example/v1/items?page=2": true,
		"https://evil.example/v1/items":           false,
		"https://api.crm.example/admin":           false,
		"https://api.crm.example/v1/billing":      false,
	} {
		err := check(&http.Request{URL: mustURL(t, target)}, []*http.Request{first})
		if (err == nil) != ok {
			t.Errorf("redirect to %s: allowed=%v, want %v (%v)", target, err == nil, ok, err)
		}
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
