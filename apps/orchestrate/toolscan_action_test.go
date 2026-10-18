package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// What one call reaches decides whether its result is fenced as outside
// content. A grouped tool's capabilities are the union of its actions, so
// collections fenced its own "Removed document" because add_url fetches a
// page, and machine fenced its own help because run can reach the network.
func TestAnActionThatStaysInsideIsNotFenced(t *testing.T) {
	turn := scanningTurn(t, ToolScanVerdict{Status: ScanClean})
	coll := ChatToolToAgentToolDefWithSession(collectionsListTool(), nil).Tool
	mach := turn.machineGroupedToolDef().Tool
	for _, tc := range []struct {
		tool   Tool
		action string
		fenced bool
	}{
		{coll, "remove_doc", false},
		{coll, "docs", false},
		{coll, "add_url", true},
		{mach, "help", false},
		{mach, "get", false},
		{mach, "run", true},
		{mach, "something_new", true}, // an action the tool does not describe keeps the fence
	} {
		p := toolResultPolicyFor(AgentRecord{}, tc.tool)
		out, _ := turn.applyToolResultPolicy(tc.tool.Name, p, map[string]any{"action": tc.action}, "result text", nil)
		if got := strings.HasPrefix(out, untrustedContentFence); got != tc.fenced {
			t.Errorf("%s(%s): fenced=%v, want %v", tc.tool.Name, tc.action, got, tc.fenced)
		}
	}
	// A plain network tool is fenced on every call, as before.
	fetch := Tool{Name: "fetch_url", Caps: []Capability{CapNetwork}}
	if out, _ := turn.applyToolResultPolicy("fetch_url", toolResultPolicyFor(AgentRecord{}, fetch), map[string]any{"url": "x"}, "page", nil); !strings.HasPrefix(out, untrustedContentFence) {
		t.Error("a network tool with no per-action declaration lost its fence")
	}
	// A tool that says it fetches external content keeps the fence on every action.
	ext := Tool{Name: "importer", Caps: []Capability{CapRead}, FetchesExternal: true, ActionCaps: map[string][]Capability{"list": {CapRead}}}
	if out, _ := turn.applyToolResultPolicy("importer", toolResultPolicyFor(AgentRecord{}, ext), map[string]any{"action": "list"}, "items", nil); !strings.HasPrefix(out, untrustedContentFence) {
		t.Error("a tool declared to fetch external content lost its fence")
	}
}
