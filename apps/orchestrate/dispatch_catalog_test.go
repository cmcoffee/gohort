package orchestrate

// A dispatched run's catalog gets the passes a web turn's does over all of
// it: tools wrapped once each for the result scan, the session told what the
// caller has, and the actions the owner turned off withheld.

import (
	"context"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestADispatchedCatalogGetsTheWebTurnsPasses(t *testing.T) {
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "a1", Name: "Worker",
		DisabledToolActions: []string{"workspace/delete"}}}
	calls := 0
	plain := AgentToolDef{Tool: Tool{Name: "web_search"}, Handler: func(context.Context, map[string]any) (string, error) {
		calls++
		return "ok", nil
	}}
	custom := AgentToolDef{Tool: Tool{Name: "my_tool"}, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }}
	sess := &ToolSession{Username: "u"}
	turn.wrapToolsForActivity(sess, []AgentToolDef{custom}, turn.agent) // as setupCustomTools does

	tools := turn.finalizeDispatchCatalog(sess, []AgentToolDef{plain, custom})
	if !turn.activityWrapped["web_search"] {
		t.Error("a built-in was left unwrapped, so its result is never scanned")
	}
	if _, err := tools[0].Handler(context.Background(), map[string]any{}); err != nil || calls != 1 {
		t.Errorf("the wrapped tool should still run once: calls=%d err=%v", calls, err)
	}
	if !sess.HasTool("web_search") || !sess.HasTool("my_tool") {
		t.Error("the session was not told which tools this run has")
	}
	if !sess.ActionWithheld("workspace", "delete") {
		t.Error("an action the owner turned off was not withheld on a dispatched run")
	}
}

// Every dispatch path reads the agent's saved notes through the turn's own
// read, which honours the Explicit Memory switch; a raw list put them in the
// prompt of an agent that had them turned off.
func TestDispatchPromptsReadNotesThroughTheTurn(t *testing.T) {
	for _, file := range []string{"agent_dispatch.go", "agents_grouped_tool.go", "scheduled_updates.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Skip("source unavailable")
		}
		if strings.Contains(string(src), "ListMemoryFacts(runtimeDB, factsNamespace(target.ID))") ||
			strings.Contains(string(src), "ListMemoryFacts(t.udb, factsNamespace(target.ID))") ||
			strings.Contains(string(src), "ListMemoryFacts(udb, factsNamespace(agent.ID))") {
			t.Errorf("%s reads a dispatched agent's notes raw, past its Explicit Memory switch", file)
		}
	}
	off := &chatTurn{user: "u", agent: AgentRecord{ID: "a1", DisableExplicit: true}}
	if len(off.facts()) != 0 {
		t.Error("an agent with Explicit Memory off still has notes")
	}
}
