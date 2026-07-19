package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCredentialAllowlist covers the deny-list → allow-list flip: dual-path
// enforcement, migration that bakes in current access, and the actual leak
// fix — a newly-registered credential is denied to a migrated (allow-list) agent.
func TestCredentialAllowlist(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	// Two open creds + one secured. no_auth is auto-registered by the store.
	for _, n := range []string{"a", "b"} {
		if err := Secure().Save(SecureCredential{Name: n, Type: SecureCredBearer, BaseURL: "https://" + n + ".test"}, "t"); err != nil {
			t.Fatalf("save %s: %v", n, err)
		}
	}
	if err := Secure().Save(SecureCredential{Name: "sec", Type: SecureCredBearer, BaseURL: "https://s.test", Secured: true}, "t"); err != nil {
		t.Fatalf("save sec: %v", err)
	}

	// Legacy agent (nil allow-list, denies b) → deny-list path: deny = {b}.
	legacy := AgentRecord{DisabledCredentials: []string{"b"}}
	deny := credentialDenySet(legacy)
	if !deny["b"] || deny["a"] {
		t.Fatalf("legacy deny-list path: got %v", deny)
	}

	// Allow-list agent (CredAllowlist, allows only a) → deny every open cred not
	// allowed; a SECURED cred is never scope-gated.
	allow := AgentRecord{CredAllowlist: true, EnabledCredentials: []string{"a"}}
	deny = credentialDenySet(allow)
	if !deny["b"] || deny["a"] || deny["sec"] {
		t.Fatalf("allow-list path (secured excluded): got %v", deny)
	}

	// Migrate the legacy agent → marks it, bakes current access (open − denied, no
	// secured), clears the deny-list, idempotent.
	m := migrateCredScope(legacy)
	if !m.CredAllowlist || len(m.DisabledCredentials) != 0 {
		t.Fatalf("migrate must mark allow-list + clear deny-list; allowlist=%v disabled=%v", m.CredAllowlist, m.DisabledCredentials)
	}
	if !containsString(m.EnabledCredentials, "a") || containsString(m.EnabledCredentials, "b") || containsString(m.EnabledCredentials, "sec") {
		t.Fatalf("migrate must bake open−denied and exclude secured; got %v", m.EnabledCredentials)
	}
	if again := migrateCredScope(m); len(again.EnabledCredentials) != len(m.EnabledCredentials) {
		t.Fatal("migrate must be idempotent")
	}

	// The leak fix: a credential registered AFTER migration is denied by default.
	if err := Secure().Save(SecureCredential{Name: "newcred", Type: SecureCredBearer, BaseURL: "https://n.test"}, "t"); err != nil {
		t.Fatal(err)
	}
	if deny := credentialDenySet(m); !deny["newcred"] {
		t.Fatal("a newly-registered credential must be DENIED to a migrated allow-list agent (the whole point)")
	}
}
