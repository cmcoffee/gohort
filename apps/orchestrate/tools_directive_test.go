package orchestrate

// The "## Tools available" digest. It used to re-list every tool's
// description first-line even though the model receives the full schemas
// in the same request — measured on a real traced body at 15,203 chars
// (~3,800 tok) on a 70-tool agent, with 38 of 38 named tools also
// carrying a schema and none prose-only.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func directiveTools(n int) []AgentToolDef {
	out := make([]AgentToolDef, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, AgentToolDef{Tool: Tool{
			Name: string(rune('a'+i)) + "_tool",
			Description: "First line that repeats what the schema already says. " +
				strings.Repeat("padding padding padding ", 8),
		}})
	}
	return out
}

func TestToolUseDirective_SlimByDefault(t *testing.T) {
	tools := directiveTools(20)
	got := buildToolUseDirective(tools)

	// Every tool is still NAMED — the index property the fat form was
	// kept for.
	for _, td := range tools {
		if !strings.Contains(got, td.Tool.Name) {
			t.Errorf("slim digest dropped tool name %q", td.Tool.Name)
		}
	}
	// But descriptions are gone — that is the duplication.
	if strings.Contains(got, "padding padding") {
		t.Error("slim digest should not restate descriptions the schema carries")
	}
	// The load-bearing nudge survives.
	if !strings.Contains(got, "Prefer calling a tool") {
		t.Errorf("the prefer-a-tool nudge must survive the cut: %s", got)
	}
	// And it is dramatically smaller than the fat form it replaced.
	if fat := renderDirectiveTemplate(toolsDirectiveFat, tools); len(got)*4 > len(fat) {
		t.Errorf("slim (%d) should be far smaller than fat (%d)", len(got), len(fat))
	}
	if len(got) > 1500 {
		t.Errorf("slim digest is %d chars for 20 tools — too close to the fat form", len(got))
	}
}

func TestToolUseDirective_FatFormStillRenders(t *testing.T) {
	// Reverting is an admin prompt-key edit with no rebuild, so the fat
	// template has to keep working.
	tools := directiveTools(3)
	out := renderDirectiveTemplate(toolsDirectiveFat, tools)
	for _, td := range tools {
		if !strings.Contains(out, td.Tool.Name) {
			t.Errorf("fat digest dropped %q", td.Tool.Name)
		}
	}
	if !strings.Contains(out, "First line that repeats") {
		t.Error("fat digest should still carry description first-lines")
	}
}

// The roster describes the CATALOG, never the worker subset.
//
// It used to be built from pr.cat.workerTools while the request carried
// pr.allTools, and the block asserts "every tool named here is live and
// callable this turn". A prompt that says that about the wrong list does not
// merely omit a tool, it overrides the schemas shipped alongside it: the model
// reads the sentence, not the payload.
//
// Live cost: nine names in the roster against thirty-five schemas. The agent
// reported accurately and repeatedly that knowledge_search was not callable,
// refused to call it, and when a guardrail forced the call and it returned real
// documentation, told the user that result was not genuine.
func TestTheToolRosterNamesTheWholeCatalogNotTheWorkerSubset(t *testing.T) {
	pr := &planRun{
		allTools: []AgentToolDef{
			{Tool: Tool{Name: "knowledge_search", Description: "search the corpus"}},
			{Tool: Tool{Name: "web_search", Description: "search the web"}},
		},
		cat: catalogState{workerTools: []AgentToolDef{
			{Tool: Tool{Name: "web_search", Description: "search the web"}},
		}},
	}
	pr.appendCatalogPromptBlocks()

	if !strings.Contains(pr.sys, "knowledge_search") {
		t.Errorf("a tool the model can call must be named in the roster:\n%s", pr.sys)
	}
	if !strings.Contains(pr.sys, "web_search") {
		t.Errorf("the worker tools are part of the catalog too:\n%s", pr.sys)
	}
}

// And it is written after the machine-phase narrowing, so a phase that removed
// a tool does not leave it advertised. Same false-roster failure, pointed the
// other way: naming a tool the phase just took away teaches the model to call
// it and be refused.
func TestTheToolRosterFollowsTheNarrowedCatalog(t *testing.T) {
	pr := &planRun{allTools: []AgentToolDef{
		{Tool: Tool{Name: "web_search", Description: "search the web"}},
	}}
	pr.appendCatalogPromptBlocks()

	if strings.Contains(pr.sys, "knowledge_search") {
		t.Errorf("a tool absent from the final catalog must not be advertised:\n%s", pr.sys)
	}
	if !strings.Contains(pr.sys, "web_search") {
		t.Errorf("what survived narrowing must still be named:\n%s", pr.sys)
	}
}
