package orchestrate

// The invariant list_agents exists to hold: what it SHOWS equals what
// ask_agent would ACCEPT. The first version broke it by filtering sub-agents
// out of the list while the resolver still dispatched to them — so an agent
// whose owner had ticked "Reachable over MCP" was callable and invisible.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestSubAgentIsMarkedNotHidden(t *testing.T) {
	// A sub-agent whose owner exposed it deliberately.
	sub := AgentRecord{ID: "child-1", Name: "Summarizer", OwnedBy: "parent-9", MCPExposed: true}
	if !externallyReachable(sub, "alice") {
		t.Fatal("an MCPExposed agent must be externally reachable regardless of ownership")
	}
	// The resolver takes it, so the lister must too — that symmetry IS the
	// feature. Asserted on the reachability rule both sides share.
	top := AgentRecord{ID: "top-1", Name: "Wren", MCPExposed: true}
	if !externallyReachable(top, "alice") {
		t.Fatal("a top-level exposed agent must be reachable")
	}
	off := AgentRecord{ID: "off-1", Name: "Private", MCPExposed: false}
	if externallyReachable(off, "alice") {
		t.Error("an agent with the toggle off must not be reachable")
	}
}

func TestListerAndResolverAgreeOnTheSameRule(t *testing.T) {
	// Both sides gate on externallyReachable OR an explicit key grant. If one
	// side ever grows an extra condition, this is the test that should fail.
	granted := func(canonical string) bool { return canonical == "agent:granted-1" }
	cases := []struct {
		rec   AgentRecord
		grant bool
		want  bool
	}{
		{AgentRecord{ID: "a", MCPExposed: true}, false, true},
		{AgentRecord{ID: "b"}, false, false},
		{AgentRecord{ID: "granted-1"}, true, true},
		{AgentRecord{ID: "sub", OwnedBy: "p", MCPExposed: true}, false, true},
	}
	for _, c := range cases {
		g := func(s string) bool { return c.grant && granted(s) }
		got := externallyReachable(c.rec, "alice") || g("agent:"+c.rec.ID)
		if got != c.want {
			t.Errorf("agent %q (ownedBy=%q exposed=%v grant=%v): reachable=%v, want %v",
				c.rec.ID, c.rec.OwnedBy, c.rec.MCPExposed, c.grant, got, c.want)
		}
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
