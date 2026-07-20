package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestAgentShareHelpers covers the pure peer-share predicates: only a user's own
// top-level agent is shareable (seeds + sub-agents excluded), and a non-owner may
// run a shared agent iff they're in its recipient list.
func TestAgentShareHelpers(t *testing.T) {
	owned := AgentRecord{ID: "a1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if !isShareableAgent(owned, "alice") {
		t.Fatal("a user's own top-level agent must be shareable")
	}
	if isShareableAgent(AgentRecord{ID: "a1", Owner: "alice", OwnedBy: "parent"}, "alice") {
		t.Fatal("a sub-agent (OwnedBy set) must not be shareable")
	}
	if isShareableAgent(AgentRecord{ID: "seed-chat", Owner: "alice"}, "alice") {
		t.Fatal("a seed must not be shareable")
	}
	if isShareableAgent(owned, "bob") {
		t.Fatal("only the OWNER can share — a non-owner match must be false")
	}
	if !userCanRunSharedAgent(owned, "bob") {
		t.Fatal("bob is a recipient — must be allowed")
	}
	if userCanRunSharedAgent(owned, "carol") {
		t.Fatal("carol is not a recipient — must be denied")
	}
	if userCanRunSharedAgent(owned, "") {
		t.Fatal("anonymous is never allowed")
	}
}

// TestReachableAgent covers the /agents/ surface pool gate: an agent is reachable
// there if it's published (Exposed) OR peer-shared (AllowedUsers), but never a
// clone-only seed, and not a plain private agent.
func TestReachableAgent(t *testing.T) {
	if !reachableAgent(AgentRecord{ID: "custom-x", Exposed: true}) {
		t.Fatal("a published agent must be reachable")
	}
	if !reachableAgent(AgentRecord{ID: "custom-x", AllowedUsers: []string{"bob"}}) {
		t.Fatal("a peer-shared agent must be reachable")
	}
	if reachableAgent(AgentRecord{ID: "custom-x"}) {
		t.Fatal("a private, unshared agent must NOT be reachable")
	}
	// A clone-only template seed is never surfaced, even flagged Exposed.
	if reachableAgent(AgentRecord{ID: "seed-kb", Exposed: true}) {
		t.Fatal("a clone-only seed must never be reachable on /agents/")
	}
	if reachableAgent(AgentRecord{ID: "seed-kb", AllowedUsers: []string{"bob"}}) {
		t.Fatal("a clone-only seed must never be reachable even if peer-shared")
	}
}

// TestListAndRevokeUserOwnedAgents covers the admin governance hooks: the
// cross-user enumeration surfaces each user's own agents with their recipient
// list (seeds/sub-agents excluded), and revoke clears the recipients while the
// agent itself survives.
func TestListAndRevokeUserOwnedAgents(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = saved })

	AuthSetUser(root, "alice", "pw", false)
	AuthSetUser(root, "bob", "pw", false)

	mk := func(owner, id, name string, allowed []string) {
		udb := UserDB(root, owner)
		rec := AgentRecord{ID: id, Owner: owner, Name: name, OrchestratorPrompt: "p", AllowedUsers: allowed}
		if _, err := saveAgent(udb, rec); err != nil {
			t.Fatalf("save %s/%s: %v", owner, id, err)
		}
	}
	mk("alice", "a-shared", "Shared One", []string{"bob"})
	mk("alice", "a-priv", "Private One", nil)
	mk("bob", "b-priv", "Bobs Agent", nil)
	// A sub-agent must NOT appear as a shareable user-owned agent.
	subUDB := UserDB(root, "alice")
	if _, err := saveAgent(subUDB, AgentRecord{ID: "a-sub", Owner: "alice", Name: "Sub", OrchestratorPrompt: "p", OwnedBy: "a-priv"}); err != nil {
		t.Fatal(err)
	}

	rows := listUserOwnedAgentsForAdmin(root)
	byID := map[string]UserOwnedAgentRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if _, ok := byID["a-sub"]; ok {
		t.Fatal("a sub-agent must be excluded from the governance list")
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 user-owned agents; got %d: %v", len(rows), rows)
	}
	if !byID["a-shared"].Shared || byID["a-shared"].SharedWith != "bob" {
		t.Fatalf("a-shared must be shared with bob; got %+v", byID["a-shared"])
	}
	if byID["a-priv"].Shared {
		t.Fatalf("a-priv must not be shared; got %+v", byID["a-priv"])
	}

	// Revoke clears the recipient list; the agent survives.
	if err := revokeAgentShareForAdmin(root, "alice", "a-shared"); err != nil {
		t.Fatal(err)
	}
	a, ok := loadAgent(UserDB(root, "alice"), "a-shared")
	if !ok {
		t.Fatal("agent must survive a share revoke")
	}
	if len(a.AllowedUsers) != 0 {
		t.Fatalf("revoke must clear recipients; got %v", a.AllowedUsers)
	}
}
