package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPIKeyUserAcceptsBothEnvelopes: gohort's own clients send X-API-Key, but
// most third-party integrations send "Authorization: Bearer" and some can send
// nothing else — an OpenAI-compatible client library, a voice platform's
// custom-LLM config. Same token, different envelope; both must resolve.
func TestAPIKeyUserAcceptsBothEnvelopes(t *testing.T) {
	const good = "tok-good"
	prev := apiKeyValidators
	apiKeyValidators = nil
	RegisterAPIKeyValidator(func(k string) (string, bool) {
		if k == good {
			return "alice", true
		}
		return "", false
	})
	t.Cleanup(func() { apiKeyValidators = prev })

	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"x-api-key", map[string]string{"X-API-Key": good}, "alice"},
		{"bearer", map[string]string{"Authorization": "Bearer " + good}, "alice"},
		{"bearer lowercase scheme", map[string]string{"Authorization": "bearer " + good}, "alice"},
		{"bearer with padding", map[string]string{"Authorization": "Bearer   " + good + "  "}, "alice"},
		// X-API-Key wins when both are present — gohort's own clients keep
		// their path even behind a proxy that adds an Authorization header.
		{"both", map[string]string{"X-API-Key": good, "Authorization": "Bearer nope"}, "alice"},
		{"unknown token", map[string]string{"Authorization": "Bearer nope"}, ""},
		{"no credential", nil, ""},
		// A non-Bearer scheme is not a token — Basic auth must not be read as one.
		{"basic auth ignored", map[string]string{"Authorization": "Basic " + good}, ""},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}, ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := APIKeyUser(r); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
