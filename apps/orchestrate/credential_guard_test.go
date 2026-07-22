package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestDraftRefusesReDraftOverWorkingCred guards the CalDAV incident: the LLM,
// misdiagnosing a config bug, re-drafted the user's enabled apple_caldav
// credential — which disables it and (with a manual delete) forced the user to
// re-enter their app-specific password. draft_api_credential must refuse to
// re-draft over the user's OWN working (enabled/secret-bearing) credential, and
// steer to update_api_credential instead.
func TestDraftRefusesReDraftOverWorkingCred(t *testing.T) {
	prev := AuthDB
	AuthDB = func() Database { return &DBase{Store: kvlite.MemStore()} }
	defer func() { AuthDB = prev }()

	// A live, user-owned credential (enabled, real secret).
	if err := Secure().Save(SecureCredential{
		Name: "apple_caldav", Type: SecureCredBasicAuth,
		BaseURL: "https://p188-caldav.icloud.com", Owner: "alice",
	}, "alice@example.com:app-specific-pw"); err != nil {
		t.Fatalf("save cred: %v", err)
	}

	turn := &chatTurn{user: "alice"}
	out, err := draftAPICredentialToolDef(turn).Handler(map[string]any{
		"name": "apple_caldav", "type": "basic_auth",
		"base_url": "https://p188-caldav.icloud.com/195178399",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(out, "NOT re-drafting") || !strings.Contains(out, "update_api_credential") {
		t.Fatalf("re-draft over a working cred should refuse and steer to update; got:\n%s", out)
	}

	// A user with NO such credential can still draft fresh (no refusal). Refusal
	// path returns before the SSE card, so a nil-sse turn is fine here.
	bob := &chatTurn{user: "bob"}
	out, _ = updateAPICredentialToolDef(bob).Handler(map[string]any{"name": "nope", "base_url": "https://x"})
	if !strings.Contains(out, "No credential named") {
		t.Fatalf("update on a missing cred should say so; got:\n%s", out)
	}
}

// TestUpdateProposesDiff covers the approve-update tool's diff logic: a real
// base_url change is proposed (card emitted, secret untouched); a no-op change
// is reported as nothing to do.
func TestUpdateProposesDiff(t *testing.T) {
	prev := AuthDB
	AuthDB = func() Database { return &DBase{Store: kvlite.MemStore()} }
	defer func() { AuthDB = prev }()

	if err := Secure().Save(SecureCredential{
		Name: "apple_caldav", Type: SecureCredBasicAuth,
		BaseURL: "https://p188-caldav.icloud.com", Owner: "alice",
	}, "alice@example.com:pw"); err != nil {
		t.Fatalf("save cred: %v", err)
	}
	turn := &chatTurn{user: "alice"}

	out, err := updateAPICredentialToolDef(turn).Handler(map[string]any{
		"name": "apple_caldav", "base_url": "https://p188-caldav.icloud.com/195178399",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(out, "Proposed 1 config change") {
		t.Fatalf("a real base_url change should be proposed; got:\n%s", out)
	}

	// Same value → no change proposed.
	out, _ = updateAPICredentialToolDef(turn).Handler(map[string]any{
		"name": "apple_caldav", "base_url": "https://p188-caldav.icloud.com",
	})
	if !strings.Contains(out, "No config changes") {
		t.Fatalf("a no-op change should report nothing to do; got:\n%s", out)
	}

	// The secret is untouched by proposing an update.
	if _, enabled, hasSecret := Secure().CredentialStatusOwned("alice", "apple_caldav"); !enabled || !hasSecret {
		t.Fatalf("proposing an update must not disable or wipe the credential (enabled=%v hasSecret=%v)", enabled, hasSecret)
	}
}
