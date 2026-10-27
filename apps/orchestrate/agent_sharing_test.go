package orchestrate

import (
	"os"
	"strings"
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
	// A framework seed is never surfaced, even flagged Exposed or shared.
	if reachableAgent(AgentRecord{ID: "seed-research", ShowOnDashboard: true}) {
		t.Error("no framework seed may surface on /agents/, even with a stale Exposed flag")
	}
	if reachableAgent(AgentRecord{ID: "seed-research", AllowedUsers: []string{"bob"}}) {
		t.Error("no framework seed may surface on /agents/, even peer-shared")
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

// A shared agent's "never unattended" marks are its owner's restriction: run
// unattended by the person it was shared with, they still hold.
func TestASharedAgentKeepsItsOwnersUnattendedMarks(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a1", Owner: "alice", Name: "Mailer", OrchestratorPrompt: "mail",
		AllowedUsers: []string{"bob"}, NoUnattendedTools: []string{"send_email"}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := app.newAutonomousGate("bob", "a1", nil)
	if !g.noUnattended["send_email"] {
		t.Error("bob's unattended run of alice's agent dropped her never-unattended mark")
	}
}

// A key's explicit grant reaches its owner's own agents, never an agent
// someone else shared with them (that one needs ITS owner to expose it).
func TestAKeyGrantCoversOnlyOwnAgents(t *testing.T) {
	if !ownAgentFor(AgentRecord{Owner: "alice"}, "alice") || !ownAgentFor(AgentRecord{}, "alice") {
		t.Error("an own agent is not covered")
	}
	if ownAgentFor(AgentRecord{Owner: "bob"}, "alice") {
		t.Error("another owner's shared agent is covered by alice's key grant")
	}
}

// An agent shared with a group reaches its members, dispatch included, and
// nobody outside it; the owner's page names the group rather than its id.
func TestAnAgentSharedWithAGroupReachesItsMembers(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	base := orchestrateBaseDB
	adb := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return adb }
	SaveAppGroup(adb, AppGroup{ID: "g-ops", Name: "Ops", Shareable: true})
	t.Cleanup(func() { DeleteAppGroup(adb, "g-ops"); AuthDB = prev })
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:dana", AuthUser{Username: "dana"})

	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Researcher", OrchestratorPrompt: "research",
		AllowedUsers: []string{"group:g-ops"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := SharedAgentsFor(base, "bob"); len(got) != 1 || got[0].ID != "a1" {
		t.Fatalf("bob, in Ops, does not have it: %+v", got)
	}
	if a, ok := findAgentByNameOrID(UserDB(base, "bob"), "bob", "Researcher"); !ok || a.Owner != "alice" {
		t.Error("bob cannot dispatch to it by name")
	}
	if got := SharedAgentsFor(base, "dana"); len(got) != 0 {
		t.Errorf("dana, outside Ops, has it: %+v", got)
	}
	a, _ := loadAgent(udb, "a1")
	if r := agentReachOf(udb, "alice", a); r.Audience != "Shared with the Ops team" || len(r.recipients) != 1 || r.recipients[0] != "bob" {
		t.Errorf("reach = %q, recipients %v", r.Audience, r.recipients)
	}
}

// Another app adds and removes one of an agent's Sources through the hook,
// and reads them back; somebody else's agent is refused.
func TestAnotherAppEditsAnAgentsSources(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Wren", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	sel := ReferenceSelection{Kind: "system", ItemID: "lab-box"}
	for i := 0; i < 2; i++ { // twice: idempotent
		if err := setAgentSource("alice", "a1", sel, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := agentSourcesFor("alice", "a1"); len(got) != 1 || got[0] != sel {
		t.Fatalf("sources = %+v", got)
	}
	if err := setAgentSource("mallory", "a1", sel, false); err == nil {
		t.Error("somebody else changed the agent's Sources")
	}
	if err := setAgentSource("alice", "a1", sel, false); err != nil {
		t.Fatal(err)
	}
	if got := agentSourcesFor("alice", "a1"); len(got) != 0 {
		t.Errorf("the Source was not removed: %+v", got)
	}
}

// A shared agent is somebody else's record. You run it; you do not change it.
//
// The HTTP editor and agent_crud_tools both said so already. The tools that
// attach something TO an agent by name did not: each resolved its target with
// the shared-agent fallback and wrote, which put a fork of somebody else's
// agent in the caller's own store — shadowing the shared record from then on,
// so it stopped following the owner's edits while everything on screen still
// said it was theirs.

func TestSomebodyElsesAgentIsNotYoursToChange(t *testing.T) {
	shared := AgentRecord{ID: "a1", Name: "Troubleshooter", Owner: "alice"}
	if msg := agentEditRefusal(shared, "bob"); msg == "" {
		t.Error("a recipient may edit the agent that was shared with them")
	} else {
		// It has to say whose it is and what to do instead, or the model
		// tries the same call again with a different spelling.
		for _, want := range []string{"alice", "Duplicate"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal does not mention %q: %s", want, msg)
			}
		}
	}
	if msg := agentEditRefusal(shared, "alice"); msg != "" {
		t.Errorf("the owner was refused their own agent: %s", msg)
	}
}

// A framework seed is shadow-cloned per user and an unowned record predates
// ownership. Neither is another user's, and both must keep behaving as before.
func TestSeedsAndUnownedRecordsAreUnaffected(t *testing.T) {
	if msg := agentEditRefusal(AgentRecord{ID: "s", Name: "Operator", Owner: seedOwner}, "bob"); msg != "" {
		t.Errorf("a framework seed was refused: %s", msg)
	}
	if msg := agentEditRefusal(AgentRecord{ID: "old", Name: "Legacy"}, "bob"); msg != "" {
		t.Errorf("an unowned record was refused: %s", msg)
	}
}

// Every tool that resolves an agent with the SHARED fallback and then writes
// has to consult the guard. Structural, because the bug is a missing call: a
// new attach-to-agent tool written next year would reintroduce it, and no
// behavioural test of the tools that exist today would notice.
func TestEveryAttachToolChecksOwnership(t *testing.T) {
	for _, file := range []string{
		"skill_def_tool.go", "machine_def_tool.go", "pipeline_def_tool.go", "add_tool.go",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		src := string(raw)
		if !strings.Contains(src, "findAgentByNameOrID(") {
			continue // no longer resolves an agent at all
		}
		// agentChangeGate runs agentEditRefusal, then the lock.
		if !strings.Contains(src, "agentChangeGate(") {
			t.Errorf("%s resolves an agent by name (which finds shared ones) and never checks whether it is the caller's to change, or whether it is locked", file)
		}
	}
	// The own-agent resolver too: it skips shared agents, so ownership is not
	// its question, but the lock still is (bulletins wires boards with it).
	for _, file := range []string{"bulletins_tool.go", "machine_def_tool.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		src := string(raw)
		if strings.Contains(src, "ownAgentByNameOrID(") && !strings.Contains(src, "agentChangeGate(") {
			t.Errorf("%s resolves one of the caller's agents and never checks whether it is locked", file)
		}
	}
}
