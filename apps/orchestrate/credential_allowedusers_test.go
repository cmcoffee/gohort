package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCredentialAllowedUsers covers the two-tier credential access model:
// tier 1 (a credential's AllowedUsers vs the SESSION user) and tier 2 (the
// agent's per-agent DisabledCredentials opt-out), and their union.
func TestCredentialAllowedUsers(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()

	// "open" grants everyone (empty AllowedUsers); "team" only alice.
	if err := Secure().Save(SecureCredential{Name: "open", Type: SecureCredBearer, BaseURL: "https://o.test"}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "team", Type: SecureCredBearer, BaseURL: "https://t.test", AllowedUsers: []string{"alice"}}, "t"); err != nil {
		t.Fatal(err)
	}

	agent := AgentRecord{Name: "A", Owner: "alice"}

	// (User-owned credential isolation is enforced by Secure().Resolve — a
	// non-owner's namespace can't reach it — not by the deny-set. Covered by
	// core TestCredentialUserNamespaceStore.)

	// Tier 1: alice (granted) is denied nothing; bob (not granted) is denied "team",
	// never "open".
	if deny := credentialDenySet(agent, "alice"); deny["team"] || deny["open"] {
		t.Fatalf("granted user must not be denied; got %v", deny)
	}
	if deny := credentialDenySet(agent, "bob"); !deny["team"] || deny["open"] {
		t.Fatalf("bob must be denied only the user-restricted cred; got %v", deny)
	}

	// The session user drives it, not agent.Owner: a system-owned seed running in
	// bob's session is denied "team" too.
	seed := AgentRecord{Name: "Builder", Owner: "system"}
	if deny := credentialDenySet(seed, "bob"); !deny["team"] {
		t.Fatal("tier-1 must key on the SESSION user, not agent.Owner")
	}

	// Empty session user (headless/unknown) → tier 1 is skipped; tier 2 still applies.
	optOut := AgentRecord{Name: "A", Owner: "alice", DisabledCredentials: []string{"open"}}
	deny := credentialDenySet(optOut, "alice")
	if !deny["open"] {
		t.Fatal("tier-2 per-agent opt-out must be denied")
	}
	if deny["team"] {
		t.Fatal("alice is granted team; tier-2 didn't opt it out")
	}
}

// TestSecuredCredentialIgnoresPerAgentOptOut pins the contract the agent
// editor states out loud: "Secured credentials aren't listed: their access
// follows their tool bindings, not per-agent scope."
//
// The picker (userScopableCredentials) drops secured creds, so an opt-out
// recorded BEFORE a credential was secured becomes invisible and unclearable
// from the UI. While credentialDenySet still honored it, that leftover kept
// dropping every tool bound to the cred out of the agent's kit and blocking its
// fetch_url auto-route, with nowhere for the owner to look.
func TestSecuredCredentialIgnoresPerAgentOptOut(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()

	for _, n := range []string{"locked", "open"} {
		if err := Secure().Save(SecureCredential{Name: n, Type: SecureCredBearer, BaseURL: "https://" + n + ".test"}, "t"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Secure().SetSecured("locked", true); err != nil {
		t.Fatal(err)
	}

	// The stale opt-out names BOTH; only the open one may still be denied.
	agent := AgentRecord{Name: "A", Owner: "alice", DisabledCredentials: []string{"locked", "open"}}
	deny := credentialDenySet(agent, "alice")
	if deny["locked"] {
		t.Error("a SECURED credential must not be denied by a per-agent opt-out the picker hides — its access follows tool bindings")
	}
	if !deny["open"] {
		t.Error("an OPEN credential's opt-out must still deny")
	}

	// A user-owned secured cred shadowing an open global reads as secured too —
	// Resolve is the same user-shadows-global lookup dispatch uses.
	if err := Secure().Save(SecureCredential{Name: "open", Type: SecureCredBearer, BaseURL: "https://mine.test", Owner: "alice"}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().SetSecuredOwned("alice", "open", true); err != nil {
		t.Fatal(err)
	}
	if credentialDenySet(agent, "alice")["open"] {
		t.Error("alice's own SECURED credential shadows the open global; the opt-out must not deny it")
	}
	// bob resolves the same name to the OPEN global, so his deny is unchanged.
	if !credentialDenySet(agent, "bob")["open"] {
		t.Error("another user still resolves the open global; the opt-out must deny there")
	}
}
