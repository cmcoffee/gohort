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
	// "mine" is USER-OWNED by alice (her namespace) — only her sessions may use it.
	if err := Secure().Save(SecureCredential{Name: "mine", Type: SecureCredBearer, BaseURL: "https://m.test", Owner: "alice"}, "t"); err != nil {
		t.Fatal(err)
	}

	agent := AgentRecord{Name: "A", Owner: "alice"}

	// Namespace classification: alice may use her own "mine"; bob may not.
	if deny := credentialDenySet(agent, "alice"); deny["mine"] {
		t.Fatalf("owner must be able to use their own credential; got %v", deny)
	}
	if deny := credentialDenySet(agent, "bob"); !deny["mine"] {
		t.Fatalf("a user-owned credential must be denied to non-owners; got %v", deny)
	}

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
