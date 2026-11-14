package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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

// The main agent is where an ask with no agent named goes, so a key can
// always be given it: it is offered first in a key's Can reach list with
// its "Reachable over MCP" switch off, where other built-in agents are not.
// Ticked on a key, it resolves through that key.
func TestTheMainAgentIsAlwaysOfferedToAKey(t *testing.T) {
	app := pipelineArtifactApp(t)
	saved := orchRef
	t.Cleanup(func() { orchRefMu.Lock(); orchRef = saved; orchRefMu.Unlock() })
	orchRefMu.Lock()
	orchRef = nil
	orchRefMu.Unlock()

	targets := externalTargetsFor(app.DB, "alice")
	if len(targets) == 0 || targets[0].Value != "agent:"+defaultConsoleAgent || !strings.Contains(targets[0].Label, "main agent") {
		t.Fatalf("the main agent should come first, marked: %+v", targets)
	}
	main, ok := findAgentByNameOrID(UserDB(app.DB, "alice"), "alice", defaultConsoleAgent)
	if !ok || main.MCPExposed {
		t.Fatalf("the test needs the main agent with its switch off: ok=%v exposed=%v", ok, main.MCPExposed)
	}
	granted := func(c string) bool { return c == "agent:"+defaultConsoleAgent }
	if id, ok := ResolveExternalAgentGranted(app.DB, "alice", defaultConsoleAgent, granted); !ok || id != defaultConsoleAgent {
		t.Errorf("a key that ticks the main agent should reach it: %q %v", id, ok)
	}
	if _, ok := ResolveExternalAgentGranted(app.DB, "alice", defaultConsoleAgent, nil); ok {
		t.Error("with no tick and the switch off, the main agent should stay unreachable")
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
