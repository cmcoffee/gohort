package mcpserver

// list_agents exists because ask_agent's schema asks for an agent id and
// nothing told the caller which ids exist. The property that matters is that
// what it SHOWS equals what ask_agent would ACCEPT — wider discloses agents the
// owner kept off this surface, narrower sends the caller guessing.

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestListAgentsIsAdvertised(t *testing.T) {
	raw, err := json.Marshal(toolDefs())
	if err != nil {
		t.Fatal(err)
	}
	var defs []map[string]any
	if err := json.Unmarshal(raw, &defs); err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	for _, d := range defs {
		if d["name"] == "list_agents" {
			found = d
		}
	}
	if found == nil {
		t.Fatal("list_agents is not in tools/list, so no client can discover it")
	}
	desc, _ := found["description"].(string)
	// The description has to point at ask_agent's argument by name, or a client
	// gets a list and no idea what to do with it.
	for _, want := range []string{"ask_agent", "id"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not mention %q: %q", want, desc)
		}
	}
	if _, ok := found["inputSchema"]; !ok {
		t.Error("no inputSchema; some clients refuse a tool without one")
	}
}

// Nothing registered ⇒ the answer says which switch turns it on. An empty list
// otherwise reads as "you have no agents", which is almost never true.
func TestEmptyListExplainsWhy(t *testing.T) {
	saved := ListExternalReachableAgentsFn
	ListExternalReachableAgentsFn = func(db Database, owner string, granted func(string) bool) []ExternalAgentInfo {
		return nil
	}
	t.Cleanup(func() { ListExternalReachableAgentsFn = saved })

	out, err := (&MCPServer{}).listAgents("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Reachable over MCP") {
		t.Errorf("an empty list must name the setting that changes it; got %q", out)
	}
}

func TestListRowsCarryTheIDAskAgentTakes(t *testing.T) {
	saved := ListExternalReachableAgentsFn
	ListExternalReachableAgentsFn = func(db Database, owner string, granted func(string) bool) []ExternalAgentInfo {
		return []ExternalAgentInfo{
			{ID: "45dbd021", Name: "Wren", Description: "General assistant"},
			{ID: "seed-research", Name: "Research"},
		}
	}
	t.Cleanup(func() { ListExternalReachableAgentsFn = saved })

	out, err := (&MCPServer{}).listAgents("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"45dbd021", "Wren", "General assistant", "seed-research"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// An agent with no description must still list — it is dispatchable.
	if strings.Count(out, "id: ") != 2 {
		t.Errorf("expected both agents listed:\n%s", out)
	}
}

// A key narrowed to one tool must neither be SHOWN the others nor able to call
// them. The listing filter is a courtesy; the call gate is the gate.
func TestToolsListAndCallHonorTheKeyScope(t *testing.T) {
	narrow := &AccountToken{Scope: &TokenScope{Tools: &[]string{"ask_agent"}}}
	defs := allowedToolDefs(toolDefs(), narrow)
	if len(defs) != 1 {
		t.Fatalf("listing showed %d tool(s) to a key narrowed to one", len(defs))
	}
	if name, _ := defs[0]["name"].(string); name != "ask_agent" {
		t.Errorf("wrong tool listed: %q", name)
	}
	// Unnarrowed keys still see everything.
	if all := allowedToolDefs(toolDefs(), &AccountToken{}); len(all) != len(toolDefs()) {
		t.Errorf("an unrestricted key saw %d of %d tools", len(all), len(toolDefs()))
	}
	// And a key narrowed to nothing sees nothing.
	if none := allowedToolDefs(toolDefs(), &AccountToken{Scope: &TokenScope{Tools: &[]string{}}}); len(none) != 0 {
		t.Errorf("a key narrowed to no tools was shown %d", len(none))
	}
}
