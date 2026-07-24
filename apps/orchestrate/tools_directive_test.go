package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The tools-available directive is a template behind an operator prompt key.
// Default must render BYTE-IDENTICAL to the legacy digest (zero-risk when no
// override is set); a {tool_names} override renders the slim variant for the
// A/B without a rebuild.
func TestToolsDirectiveTemplate(t *testing.T) {
	tools := []AgentToolDef{
		{Tool: Tool{Name: "web_search", Description: "Search the web.\nMore detail."}},
		{Tool: Tool{Name: "moltbook", Description: strings.Repeat("x", 250)}},
	}

	// Default = legacy shape, byte-identical.
	legacy := "## Tools available\n\n" +
		"- **web_search** — Search the web.\n" +
		"- **moltbook** — " + strings.Repeat("x", 200) + "…\n"
	if got := buildToolUseDirective(tools); got != legacy {
		t.Fatalf("default must render byte-identical to legacy.\ngot:  %q\nwant: %q", got, legacy)
	}

	// Slim override via the prompt-override store.
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	SetPromptOverride("framework.tools_directive",
		"## Tools available\n\nPrefer a tool over guessing. Available: {tool_names}")
	got := buildToolUseDirective(tools)
	want := "## Tools available\n\nPrefer a tool over guessing. Available: web_search, moltbook"
	if got != want {
		t.Fatalf("slim override:\ngot:  %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "Search the web") {
		t.Error("slim variant must not carry per-tool descriptions")
	}
}
