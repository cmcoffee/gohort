package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestUserScopableCredentials covers the candidate set for the agent editor's
// per-agent credential picker: a user sees the global creds their tier-1 grant
// admits plus their own, minus secured / disabled creds — the ones that make
// sense as per-agent scope targets.
func TestUserScopableCredentials(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()

	mk := func(c SecureCredential) {
		if err := Secure().Save(c, "tok"); err != nil {
			t.Fatalf("save %s: %v", c.Name, err)
		}
	}
	mk(SecureCredential{Name: "open", Type: SecureCredBearer, BaseURL: "https://x.test"})
	mk(SecureCredential{Name: "bob_only", Type: SecureCredBearer, BaseURL: "https://x.test", AllowedUsers: []string{"bob"}})
	mk(SecureCredential{Name: "secured", Type: SecureCredBearer, BaseURL: "https://x.test"})
	mk(SecureCredential{Name: "off", Type: SecureCredBearer, BaseURL: "https://x.test"})
	mk(SecureCredential{Name: "alice_own", Type: SecureCredBearer, BaseURL: "https://x.test", Owner: "alice"})
	// Secured / disabled are owned by SetSecured / SetDisabled, not Save.
	if err := Secure().SetSecured("secured", true); err != nil {
		t.Fatal(err)
	}
	if err := Secure().SetDisabled("off", true); err != nil {
		t.Fatal(err)
	}

	names := func(cs []SecureCredential) map[string]bool {
		m := map[string]bool{}
		for _, c := range cs {
			m[c.Name] = true
		}
		return m
	}

	a := names(userScopableCredentials("alice"))
	if !a["open"] {
		t.Fatal("an open global cred must be scopable by alice")
	}
	if a["bob_only"] {
		t.Fatal("a cred allowlisted to bob must be excluded for alice")
	}
	if a["secured"] {
		t.Fatal("secured creds are not per-agent scope targets (access follows tool bindings)")
	}
	if a["off"] {
		t.Fatal("a disabled cred must be excluded")
	}
	if !a["alice_own"] {
		t.Fatal("alice's own credential must be scopable")
	}

	b := names(userScopableCredentials("bob"))
	if !b["bob_only"] {
		t.Fatal("bob is allowlisted on bob_only, so it must be scopable by bob")
	}
	if b["alice_own"] {
		t.Fatal("bob must not see alice's own credential")
	}
}
