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
