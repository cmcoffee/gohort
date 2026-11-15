package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The invariant list_agents exists to hold: what it SHOWS equals what
// ask_agent would ACCEPT. The first version broke it by filtering sub-agents
// out of the list while the resolver still dispatched to them — so an agent
// its owner had ticked on a key was callable and invisible.

func TestSubAgentIsMarkedNotHidden(t *testing.T) {
	lists := func(canonical string) bool { return canonical == "agent:child-1" || canonical == "agent:top-1" }
	sub := AgentRecord{ID: "child-1", Name: "Summarizer", OwnedBy: "parent-9", Owner: "alice"}
	if !reachableThrough(sub, "alice", lists) {
		t.Fatal("a sub-agent the key lists must be reachable through it")
	}
	top := AgentRecord{ID: "top-1", Name: "Wren", Owner: "alice"}
	if !reachableThrough(top, "alice", lists) {
		t.Fatal("a top-level agent the key lists must be reachable")
	}
	off := AgentRecord{ID: "off-1", Name: "Private", Owner: "alice"}
	if reachableThrough(off, "alice", lists) {
		t.Error("an agent the key does not list must not be reachable")
	}
}

// A key reaches what it lists, and the retired "Reachable over MCP" switch's
// stored value changes nothing for a key that lists its agents: on, it does
// not widen the key; off, it does not narrow it.
func TestAKeyReachesWhatItListsWhateverTheOldSwitchSays(t *testing.T) {
	lists := func(canonical string) bool { return canonical == "agent:listed" }
	cases := []struct {
		rec  AgentRecord
		want bool
	}{
		{AgentRecord{ID: "listed", Owner: "alice"}, true},
		{AgentRecord{ID: "listed", Owner: "alice", MCPExposed: true}, true},
		{AgentRecord{ID: "unlisted", Owner: "alice", MCPExposed: true}, false},
		{AgentRecord{ID: "unlisted", Owner: "alice"}, false},
		// Somebody else's agent, not shared with alice: listing it grants nothing.
		{AgentRecord{ID: "listed", Owner: "bob"}, false},
		// Shared with alice: hers to reach while the share stands.
		{AgentRecord{ID: "listed", Owner: "bob", AllowedUsers: []string{"alice"}}, true},
	}
	for _, c := range cases {
		if got := reachableThrough(c.rec, "alice", lists); got != c.want {
			t.Errorf("agent %q (owner=%q shared=%v exposed=%v): reachable=%v, want %v",
				c.rec.ID, c.rec.Owner, c.rec.AllowedUsers, c.rec.MCPExposed, got, c.want)
		}
	}
	if reachableThrough(AgentRecord{ID: "listed", Owner: "alice", MCPExposed: true}, "alice", nil) {
		t.Error("no grant at all must reach no user agent, the old switch included")
	}
}

func TestExternalAgentInfoCarriesParent(t *testing.T) {
	info := ExternalAgentInfo{ID: "child-1", Name: "Summarizer", ParentID: "parent-9"}
	if info.ParentID == "" {
		t.Fatal("ParentID must survive so the caller can be told it is a sub-agent")
	}
	if strings.TrimSpace(info.ID) == "" {
		t.Fatal("the id is what ask_agent takes")
	}
}

// The main agent (main_agent.go) is where an ask with no agent named goes,
// so a key's Can reach list offers it first, marked. An ask naming no agent
// reaches it through a key that lists it, and through no other. The retired
// Chat is nobody's main agent and is not offered.
func TestTheMainAgentIsOfferedFirstAndAnsweredForNoName(t *testing.T) {
	app := pipelineArtifactApp(t)
	saved := orchRef
	t.Cleanup(func() { orchRefMu.Lock(); orchRef = saved; orchRefMu.Unlock() })
	orchRefMu.Lock()
	orchRef = nil
	orchRefMu.Unlock()
	prevBase := orchestrateBaseDB
	orchestrateBaseDB = app.DB
	t.Cleanup(func() { orchestrateBaseDB = prevBase })
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prevAuth })

	if targets := externalTargetsFor(app.DB, "alice"); len(targets) > 0 && targets[0].Value == "agent:"+defaultConsoleAgent {
		t.Fatalf("the retired Chat is offered as the main agent: %+v", targets)
	}
	ada, err := saveAgent(UserDB(app.DB, "alice"), AgentRecord{Owner: "alice", Name: "Ada", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	targets := externalTargetsFor(app.DB, "alice")
	if len(targets) == 0 || targets[0].Value != "agent:"+ada.ID || !strings.Contains(targets[0].Label, "your main agent") {
		t.Fatalf("the main agent should come first, marked: %+v", targets)
	}
	for _, tg := range targets {
		if tg.Value == "agent:"+defaultConsoleAgent {
			t.Errorf("the retired Chat is offered: %+v", tg)
		}
	}
	granted := func(c string) bool { return c == "agent:"+ada.ID }
	if id, ok := ResolveExternalAgentGranted(app.DB, "alice", "", granted); !ok || id != ada.ID {
		t.Errorf("an ask naming no agent, through a key that lists the main one: %q %v", id, ok)
	}
	if _, ok := ResolveExternalAgentGranted(app.DB, "alice", "", nil); ok {
		t.Error("an ask naming no agent reached the main one through a key that does not list it")
	}
	// A second agent of theirs, chosen as the main one, takes its place.
	bo, _ := saveAgent(UserDB(app.DB, "alice"), AgentRecord{Owner: "alice", Name: "Bo", OrchestratorPrompt: "p"})
	if _, ok := mainAgentFor("alice"); ok {
		t.Error("with two agents and none chosen, one was taken as the main agent")
	}
	if err := setMainAgent("alice", bo.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := mainAgentFor("alice"); m.ID != bo.ID {
		t.Errorf("the chosen main agent: %q", m.ID)
	}
	if err := setMainAgent("alice", defaultConsoleAgent); err == nil {
		t.Error("a framework seed was taken as a main agent")
	}
}

// An agent shared with someone is theirs to reach from their own outside
// client: their key editor offers it and a key that lists it reaches it, with
// no switch for the sharer to flip. Unsharing cuts the key off at its next
// call, because the resolver re-reads the share.
func TestAnAgentSharedWithYouIsOnYourKeyWhileTheShareStands(t *testing.T) {
	_, udb, owner := newTestOrchestrate(t)
	pinRootDB(t)
	base := orchestrateBaseDB
	saved := orchRef
	t.Cleanup(func() { orchRefMu.Lock(); orchRef = saved; orchRefMu.Unlock() })
	orchRefMu.Lock()
	orchRef = nil
	orchRefMu.Unlock()

	rec := AgentRecord{ID: "a1", Owner: owner, Name: "Researcher", OrchestratorPrompt: "research",
		AllowedUsers: []string{"bob"}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	offered := false
	for _, tg := range externalTargetsFor(base, "bob") {
		if tg.Value == "agent:a1" {
			offered = strings.Contains(tg.Label, "shared by "+owner)
		}
	}
	if !offered {
		t.Fatalf("the recipient's key editor does not offer the shared agent: %+v", externalTargetsFor(base, "bob"))
	}
	lists := func(c string) bool { return c == "agent:a1" }
	if id, ok := ResolveExternalAgentGranted(base, "bob", "a1", lists); !ok || id != "a1" {
		t.Fatalf("a key that lists the shared agent does not reach it: %q %v", id, ok)
	}
	if _, ok := ResolveExternalAgentGranted(base, "bob", "a1", func(string) bool { return false }); ok {
		t.Error("a key that does not list it reaches it")
	}
	for _, tg := range externalTargetsFor(base, "dana") {
		if tg.Value == "agent:a1" {
			t.Error("someone it is not shared with is offered it")
		}
	}

	rec.AllowedUsers = nil
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveExternalAgentGranted(base, "bob", "a1", lists); ok {
		t.Error("after unsharing, the recipient's key still reaches it")
	}
}

// A key offers the person's own agents to tick, and not what is reached
// through something else: a sub-agent through its parent, an app's agent
// through its app.
func TestAKeyOffersOnlyAgentsReachedOnTheirOwn(t *testing.T) {
	app := pipelineArtifactApp(t)
	saved := orchRef
	t.Cleanup(func() { orchRefMu.Lock(); orchRef = saved; orchRefMu.Unlock() })
	orchRefMu.Lock()
	orchRef = nil
	orchRefMu.Unlock()
	udb := UserDB(app.DB, "alice")
	parent, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Scout", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Helper", OrchestratorPrompt: "p", OwnedBy: parent.ID})
	appOwned, _ := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Tracker brain", OrchestratorPrompt: "p", OwningApp: "/apps/tracker"})
	offered := map[string]bool{}
	for _, tg := range externalTargetsFor(app.DB, "alice") {
		offered[tg.Value] = true
	}
	if !offered["agent:"+parent.ID] {
		t.Error("the person's own agent is not offered")
	}
	if offered["agent:"+sub.ID] {
		t.Error("a sub-agent is offered on its own")
	}
	if offered["agent:"+appOwned.ID] {
		t.Error("an app's agent is offered on its own")
	}
}
