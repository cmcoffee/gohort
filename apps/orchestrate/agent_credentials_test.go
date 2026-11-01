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

// A connection an agent's tools reach is a per-agent scope target under the
// name its key goes by at a call, for a user it is not limited away from;
// an agent's opt-out of it is carried into the run's deny set.
func TestAgentReachableConnectionsAreScopable(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return secStore }
	RootDB = &DBase{Store: kvlite.MemStore()}
	defer func() { AuthDB, RootDB = prevAuth, prevRoot }()
	RootDB.Set("connectors", "pics", Connector{Name: "pics", Kind: "service", ID: "c_pics", DisplayName: "Pictures"})
	RootDB.Set("connectors", "chat", Connector{Name: "chat", Kind: "service", ID: "c_chat"})
	RootDB.Set(ImageTable, "agents", []string{"pics"})
	RootDB.Set(LLMTable, "provider", "llm:chat")

	has := func(user, name string) bool {
		for _, c := range userScopableCredentials(user) {
			if c.Name == name {
				return true
			}
		}
		return false
	}
	if !has("alice", "connection:pics") {
		t.Fatal("an image connection offered to agents is not scopable")
	}
	if has("alice", "connection:chat") {
		t.Error("a connection serving the whole instance is offered as a per-agent scope")
	}
	c, _ := GetConnector(RootDB, "pics")
	c.SetAccess(RootDB, []string{"bob"})
	if has("alice", "connection:pics") || !has("bob", "connection:pics") {
		t.Error("a connection limited to bob is scopable by alice, or not by bob")
	}
	if deny := credentialDenySet(AgentRecord{ID: "a1", DisabledCredentials: []string{"connection:pics"}}, "bob"); !deny["connection:pics"] {
		t.Errorf("the agent's opt-out is not in its deny set: %v", deny)
	}
}
