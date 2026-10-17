package mcpserver

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A scoped key reaches over MCP exactly the agents it lists, as it does over
// the Model API: an agent with "Reachable over MCP" on that the key does not
// list is neither shown nor asked. A key from before scoping still reaches
// every exposed agent.
func TestAScopedKeyReachesOnlyTheAgentsItLists(t *testing.T) {
	saved := ListExternalReachableAgentsFn
	ListExternalReachableAgentsFn = func(db Database, owner string, granted func(string) bool) []ExternalAgentInfo {
		return []ExternalAgentInfo{{ID: "a1", Name: "Helper"}, {ID: "a2", Name: "Other"}}
	}
	t.Cleanup(func() { ListExternalReachableAgentsFn = saved })

	scoped := &AccountToken{Owner: "user-a", Scope: &TokenScope{Features: []string{MCPFeatureKey}, Targets: []string{"agent:a1"}, AgentsBindMCP: true}}
	out, err := (&MCPServer{}).listAgents("user-a", scoped)
	if err != nil || !strings.Contains(out, "id: a1") || strings.Contains(out, "id: a2") {
		t.Fatalf("a scoped key's list should hold only what it lists: %q %v", out, err)
	}
	if keyMayAsk(scoped, "a1") != nil || keyMayAsk(scoped, "a2") == nil {
		t.Error("a scoped key should ask what it lists and nothing else")
	}

	legacy := &AccountToken{Owner: "user-a"}
	out, _ = (&MCPServer{}).listAgents("user-a", legacy)
	if !strings.Contains(out, "id: a2") || keyMayAsk(legacy, "a2") != nil {
		t.Errorf("a key from before scoping lost an exposed agent: %q", out)
	}

	none := &AccountToken{Owner: "user-a", Scope: &TokenScope{Features: []string{MCPFeatureKey}, AgentsBindMCP: true}}
	if out, _ := (&MCPServer{}).listAgents("user-a", none); !strings.Contains(out, "reaches no agents") {
		t.Errorf("a key listing no agents should say how to give it some: %q", out)
	}
}

// An ask with no agent named goes to the main agent when the key reaches
// it, else to the one agent the key lists; a key listing several must name
// one.
func TestAnUnnamedAskGoesWhereTheKeyReaches(t *testing.T) {
	key := func(targets ...string) *AccountToken {
		return &AccountToken{Owner: "u", Scope: &TokenScope{Targets: targets, AgentsBindMCP: true}}
	}
	for _, tc := range []struct {
		tok  *AccountToken
		want string
	}{
		{nil, defaultAgent},
		{&AccountToken{Owner: "u"}, defaultAgent},
		{key("agent:"+defaultAgent, "agent:a1"), defaultAgent},
		{key("worker", "agent:a1"), "a1"},
	} {
		if got, err := defaultAgentFor(tc.tok); err != nil || got != tc.want {
			t.Errorf("default for %+v = %q %v, want %q", tc.tok, got, err, tc.want)
		}
	}
	if _, err := defaultAgentFor(key("agent:a1", "agent:a2")); err == nil {
		t.Error("a key listing several agents and not the main one should have to name one")
	}
	if _, err := defaultAgentFor(key("worker")); err == nil {
		t.Error("a key listing no agents should be refused")
	}
}
