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
	if !reachableAgent(AgentRecord{ID: "custom-x", ShowOnDashboard: true}) {
		t.Fatal("a published agent must be reachable")
	}
	if !reachableAgent(AgentRecord{ID: "custom-x", AllowedUsers: []string{"bob"}}) {
		t.Fatal("a peer-shared agent must be reachable")
	}
	if reachableAgent(AgentRecord{ID: "custom-x"}) {
		t.Fatal("a private, unshared agent must NOT be reachable")
	}
	// A clone-only template seed is never surfaced, even flagged Exposed.
	if reachableAgent(AgentRecord{ID: "seed-research", ShowOnDashboard: true}) {
		t.Error("no framework seed may surface on /agents/, even with a stale Exposed flag")
	}
	if reachableAgent(AgentRecord{ID: "seed-kb", ShowOnDashboard: true}) {
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

// TestPublishAgentForAdmin covers the admin "delegate to users" action: publishing
// flips Exposed on a top-level user agent (leaving its share intact), a sub-agent
// can't be published, and the governance list reflects the published state.
func TestPublishAgentForAdmin(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = saved })

	AuthSetUser(root, "alice", "pw", false)
	udb := UserDB(root, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p", AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}

	if err := publishAgentForAdmin(root, "alice", "a1"); err != nil {
		t.Fatal(err)
	}
	a, _ := loadAgent(udb, "a1")
	if !a.Everyone {
		t.Fatal("publish must set Exposed=true")
	}
	if len(a.AllowedUsers) != 1 || a.AllowedUsers[0] != "bob" {
		t.Fatalf("publish must leave the owner's share intact; got %v", a.AllowedUsers)
	}

	// A sub-agent can't be published.
	if _, err := saveAgent(udb, AgentRecord{ID: "sub", Owner: "alice", Name: "Sub", OrchestratorPrompt: "p", OwnedBy: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := publishAgentForAdmin(root, "alice", "sub"); err == nil {
		t.Fatal("a sub-agent must not be publishable")
	}

	// The governance list reflects the published state.
	for _, r := range listUserOwnedAgentsForAdmin(root) {
		if r.ID == "a1" && !r.Exposed {
			t.Fatal("governance row must reflect the published (Exposed) state")
		}
	}
}

// Recipient-side resolution, which this file's header described as "a separate
// step" for long enough that the ACL was decorative: an owner picked
// recipients, an admin could audit and revoke them, and nothing ever appeared
// for the person named.
func TestASharedAgentReachesItsRecipient(t *testing.T) {
	// newTestOrchestrate wires orchestrateBaseDB, which is what the free
	// functions read; a test that left it nil would exercise a different store
	// than production and skip rather than prove anything.
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	base := orchestrateBaseDB

	rec := AgentRecord{ID: "a1", Owner: "alice", Name: "Researcher", OrchestratorPrompt: "research",
		AllowedUsers: []string{"bob"}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := SharedAgentsFor(base, "bob")
	if len(got) != 1 || got[0].ID != "a1" {
		t.Fatalf("the recipient cannot see what was shared with them: %+v", got)
	}
	// The OWNER travels with it. That is what lets a run resolve in the owner's
	// context while the recipient's own credentials resolve in theirs, so no
	// secret travels with the share.
	if got[0].Owner != "alice" {
		t.Errorf("the record lost its owner: %q", got[0].Owner)
	}
	if got := SharedAgentsFor(base, "dana"); len(got) != 0 {
		t.Errorf("an unnamed user sees it: %+v", got)
	}

	// Dispatch resolves it, by id and by name, for the recipient.
	bobDB := UserDB(base, "bob")
	if a, ok := findAgentByNameOrID(bobDB, "bob", "a1"); !ok || a.Owner != "alice" {
		t.Error("a recipient cannot dispatch to a shared agent by id")
	}
	if a, ok := findAgentByNameOrID(bobDB, "bob", "Researcher"); !ok || a.ID != "a1" {
		t.Error("a recipient cannot dispatch to a shared agent by name")
	}

	// Revoking takes it back, because the index follows the record in the same
	// write and every read re-checks the record.
	rec.AllowedUsers = nil
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := SharedAgentsFor(base, "bob"); len(got) != 0 {
		t.Errorf("a revoked recipient still has it: %+v", got)
	}
	if _, ok := findAgentByNameOrID(bobDB, "bob", "a1"); ok {
		t.Error("a revoked recipient can still dispatch to it")
	}
}

// A sub-agent belongs to its parent and a seed belongs to the framework.
// Neither is shareable, so neither should ever land in the index: an entry for
// one would be a row nothing can act on.
func TestOnlyAShareableAgentIsIndexed(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	base := orchestrateBaseDB
	if _, err := saveAgent(udb, AgentRecord{
		ID: "sub1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "help",
		OwnedBy: "parent", AllowedUsers: []string{"bob"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := SharedAgentsFor(base, "bob"); len(got) != 0 {
		t.Errorf("a sub-agent was shared: %+v", got)
	}
}
