package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func sizedTool(name string, bytes int) AgentToolDef {
	return AgentToolDef{Tool: Tool{Name: name, Description: strings.Repeat("d", bytes)}}
}

func fitNames(tools []AgentToolDef) []string {
	var out []string
	for _, td := range tools {
		out = append(out, td.Tool.Name)
	}
	return out
}

// Builder's prompt and catalog filled a 64k model before any history. On a
// small window the largest schemas step down behind load_tool until the fixed
// part fits; on a big one nothing moves.
func TestTheCatalogStepsDownToFitTheWindow(t *testing.T) {
	tools := []AgentToolDef{
		sizedTool("app_def", 120000), // ~30k tokens
		sizedTool("create_agent", 52000),
		sizedTool("workspace", 15000),
		sizedTool("load_tool", 3500),
		sizedTool("ask_user", 9000),
	}
	keep := func(n string) bool { return catalogKeepAlways[n] }

	sent, held, over := fitToolsToWindow(tools, 13000, 204800, keep)
	if len(held) != 0 || len(sent) != len(tools) || over != 0 {
		t.Errorf("a 200k window held back %v", fitNames(held))
	}
	if sent, held, _ := fitToolsToWindow(tools, 13000, 0, keep); len(held) != 0 || len(sent) != len(tools) {
		t.Error("an unknown window must hold nothing back")
	}

	sent, held, over = fitToolsToWindow(tools, 13000, 64000, keep)
	if got := strings.Join(fitNames(held), ","); got != "app_def,create_agent" {
		t.Errorf("held %q, want the two largest", got)
	}
	if got := strings.Join(fitNames(sent), ","); got != "workspace,load_tool,ask_user" {
		t.Errorf("sent %q, in catalog order", got)
	}
	if over != 0 {
		t.Errorf("still over by %d", over)
	}

	// A kept tool is never held, even when it is the largest.
	keepApp := func(n string) bool { return keep(n) || n == "app_def" }
	_, held, over = fitToolsToWindow(tools, 13000, 64000, keepApp)
	for _, td := range held {
		if td.Tool.Name == "app_def" || td.Tool.Name == "load_tool" || td.Tool.Name == "ask_user" {
			t.Errorf("held a kept tool: %s", td.Tool.Name)
		}
	}
	if over == 0 {
		t.Error("with app_def kept the catalog cannot fit 64k: the overage must be reported")
	}
}
