package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tool of the user's own that is scoped to one agent shadows a same-named
// tool they took from a colleague only on THAT agent. Every other agent is
// outside the scoped row, so it skips that row; it must still get the taken
// tool, not neither.
func TestAScopedToolShadowsATakenToolOnlyOnItsAgents(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })

	if err := AdminPersistTempTool(root, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo lent"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(root, "lender", "wiki_read", []string{"u"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(root, "u", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(udb, "u", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo own"}); err != nil {
		t.Fatal(err)
	}
	if !SetUserToolScopeAgents(udb, "u", "wiki_read", []string{"agent-1"}) {
		t.Fatal("scope write failed")
	}

	load := func(agentID string) string {
		turn := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: agentID, Owner: "u"}}
		sess := &ToolSession{}
		turn.loadAgentTempTools(sess, "u", root)
		for _, tt := range sess.TempTools {
			if tt.Name == "wiki_read" {
				return tt.CommandTemplate
			}
		}
		return ""
	}
	if got := load("agent-1"); got != "echo own" {
		t.Errorf("the agent the own copy is scoped to should run it; got %q", got)
	}
	if got := load("agent-2"); got != "echo lent" {
		t.Errorf("an agent outside the own copy's scope should run the taken tool; got %q", got)
	}
}

// The framework tools assembled per turn (several behind load_tool) are
// reserved, so a custom tool cannot take their names, and one that already did
// is dropped from the catalog rather than answering for the framework's.
func TestFrameworkToolNamesAreReserved(t *testing.T) {
	for _, n := range []string{
		"tool_def", "create_agent", "update_agent", "agents", "recall",
		"remember", "forget", "query_source", "load_tool", "request_build",
		"hand_to_builder", "collection", "recurring", "app_def",
	} {
		if !IsReservedToolName(n) {
			t.Errorf("%q is a framework tool but not reserved", n)
		}
	}
	// Every tool in Builder's authoring catalog, deferred behind load_tool on
	// other authoring agents, is reserved or statically registered: a new one
	// added there without a reservation fails here, not in a user's catalog.
	sess := &ToolSession{Username: "u", DB: &DBase{Store: kvlite.MemStore()}}
	for _, td := range builderAuthoringTools(sess, nil) {
		n := td.Tool.Name
		if strings.HasPrefix(n, "fetch_url_") {
			continue // named by a credential; guarded where those mint
		}
		if _, builtin := LookupChatTool(n); !builtin && !IsReservedToolName(n) {
			t.Errorf("authoring tool %q is neither registered nor reserved: a custom tool could shadow it", n)
		}
	}

	shadow := &ToolSession{}
	if err := shadow.AppendTempTool(&TempTool{Name: "tool_def", Description: "stub", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if defs := temptool.BuildAgentToolDefs(shadow); len(defs) != 0 {
		t.Errorf("a custom tool named after a framework tool still reached the catalog: %d def(s)", len(defs))
	}
}

// A direct call to a framework tool that sits behind load_tool resolves to the
// framework's handler, even if a lazy custom tool of the same name exists
// (authored before the name was reserved).
func TestADirectCallReachesTheFrameworkToolFirst(t *testing.T) {
	handler := func(label string) ToolHandlerFunc {
		return func(context.Context, map[string]any) (string, error) { return label, nil }
	}
	turn := &chatTurn{
		deferredAuthoringDefs:   map[string]AgentToolDef{"tool_def": {Tool: Tool{Name: "tool_def"}, Handler: handler("framework")}},
		deferredAuthoringLoaded: map[string]bool{},
		lazyCustomToolDefs:      map[string]AgentToolDef{"tool_def": {Tool: Tool{Name: "tool_def"}, Handler: handler("custom")}},
		loadedCustomTools:       map[string]bool{},
	}
	h, ok := turn.lazyToolFallback("tool_def")
	if !ok {
		t.Fatal("the direct call did not resolve")
	}
	if got, _ := h(context.Background(), nil); got != "framework" {
		t.Errorf("a direct call to tool_def reached the %s tool", got)
	}
	if turn.loadedCustomTools["tool_def"] {
		t.Error("the shadowing custom tool was marked loaded")
	}
}
