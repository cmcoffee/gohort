package orchestrate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// store_credential_secret lands a first key, but a working key is only
// replaced when the person approves the card; keeping it discards the held
// key, and a held key expires.
func TestReplacingAWorkingKeyWaitsForApproval(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	if err := Secure().SaveAPIDraft(SecureCredential{Name: "forge", Type: SecureCredBearer, BaseURL: "https://forge.example", Owner: user}); err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{user: user, sse: &sseWriter{live: &bytes.Buffer{}}}
	current := func() string {
		c, _ := Secure().LoadUser(user, "forge")
		_, _, has := Secure().CredentialStatusOwned(user, c.Name)
		if !has {
			return ""
		}
		return "set"
	}

	if out, err := turn.storeAgentKey("forge", "first-key"); err != nil || !strings.Contains(out, "Key stored") || current() != "set" {
		t.Fatalf("a first key lands: %q %v", out, err)
	}
	out, err := turn.storeAgentKey("forge", "new-key")
	if err != nil || !strings.Contains(out, "NOT replaced") || !strings.Contains(out, "approve") {
		t.Fatalf("a working key waits for the person: %q %v", out, err)
	}

	answer := func(approve bool) *httptest.ResponseRecorder {
		body := `{"name":"forge","approve":` + map[bool]string{true: "true", false: "false"}[approve] + `}`
		w := httptest.NewRecorder()
		T.handleKeyReplacement(w, asUser(httptest.NewRequest(http.MethodPost, "/api/console/credential-key/replace", strings.NewReader(body)), user))
		return w
	}
	if w := answer(false); w.Code != http.StatusNoContent {
		t.Fatalf("keeping the key: %d %s", w.Code, w.Body.String())
	}
	if w := answer(true); w.Code != http.StatusGone {
		t.Errorf("once kept, the held key is gone: %d", w.Code)
	}

	turn.storeAgentKey("forge", "new-key")
	if w := answer(true); w.Code != http.StatusNoContent {
		t.Fatalf("approving: %d %s", w.Code, w.Body.String())
	}
	if _, held := takeKeyReplacement(user, "forge"); held {
		t.Error("an answered replacement is not held any longer")
	}

	// With no chat to show the card in, nothing is held: nobody could answer.
	quiet := &chatTurn{user: user}
	if out, _ := quiet.storeAgentKey("forge", "another-key"); !strings.Contains(out, "no chat") {
		t.Errorf("a run with no chat says the user sets it: %q", out)
	}
	if _, held := takeKeyReplacement(user, "forge"); held {
		t.Error("a key was held for a card no one could see")
	}

	holdKeyReplacement(user, "forge", "late-key")
	RootDB.CryptSet(keyReplacementTable, keyReplacementID(user, "forge"), heldKeyReplacement{Secret: "late-key", At: time.Now().Add(-2 * keyReplacementTTL)})
	if w := answer(true); w.Code != http.StatusGone {
		t.Errorf("an expired replacement is not written: %d", w.Code)
	}
}
