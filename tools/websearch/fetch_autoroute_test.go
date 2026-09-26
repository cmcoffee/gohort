package websearch

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestFetchURLAutoRoutesInternalHostBeforeSSRF pins the fix for the second
// instance of the auto-route ordering bug (the LLM-callable fetch_url tool):
// a credential-covered INTERNAL host (.local) must dispatch through its
// credential, not get refused by the SSRF guard first. Uses a DENIED credential
// so the assertion needs no network — reaching the credential-denied branch for
// a .local host is only possible if the auto-route ran BEFORE the SSRF refusal.
func TestFetchURLAutoRoutesInternalHostBeforeSSRF(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()

	if err := Secure().Save(SecureCredential{
		Name: "ts3_api", Type: SecureCredBearer,
		BaseURL: "http://chat-server.example.local:10080",
	}, "tok"); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	// Session that DENIES ts3_api. No network is attempted — the denied branch
	// returns before dispatch.
	sess := &ToolSession{Username: "alice", DeniedCredentials: map[string]bool{"ts3_api": true}}
	tool := &FetchURLTool{}

	_, err := tool.runImpl(map[string]any{"url": "http://chat-server.example.local:10080/clientlist"}, sess)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "refusing to fetch non-public host") {
		t.Fatalf("SSRF guard fired BEFORE the credential auto-route — the ordering bug: %v", err)
	}
	if !strings.Contains(err.Error(), "not allowed to use") {
		t.Fatalf("expected the credential-denied error (proves auto-route ran first); got: %v", err)
	}

	// Control: an UNCOVERED internal host is still refused — SSRF guard intact.
	_, err2 := tool.runImpl(map[string]any{"url": "http://other.internal:9000/x"}, sess)
	if err2 == nil || !strings.Contains(err2.Error(), "refusing to fetch non-public host") {
		t.Fatalf("an uncovered internal host must still be refused; got: %v", err2)
	}
}

// A URL carrying a credential is refused before any routing or request: the
// key never leaves, whether a credential covers the host or not.
func TestFetchURLRefusesASecretInTheURL(t *testing.T) {
	_, err := (&FetchURLTool{}).Run(map[string]any{
		"url": "https://generativelanguage.googleapis.com/v1beta/models?key=AIzaSyA0000000000000000000000000000000",
	})
	if err == nil || !strings.Contains(err.Error(), "Nothing was sent") || !strings.Contains(err.Error(), "fetch_url_<name>") {
		t.Fatalf("a key in the URL should be refused before sending, pointing at the credential's tool: %v", err)
	}
}
