package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A watch polls a Builder-made tool only on a call that reads: an API tool's
// caps say only that it reaches the network, so the method decides, the
// called action's for a toolbox. A script tool runs in the agent's own
// workspace, which is what a watch on it is for.
func TestAWatchPollsOnlyCallsThatRead(t *testing.T) {
	for _, tc := range []struct {
		tt   TempTool
		args map[string]any
		want bool
	}{
		{TempTool{Mode: TempToolModeAPI, Method: "GET"}, nil, true},
		{TempTool{Mode: TempToolModeAPI}, nil, true},
		{TempTool{Mode: TempToolModeAPI, Method: "POST"}, nil, false},
		{TempTool{Mode: TempToolModeAPI, Method: "delete"}, nil, false},
		{TempTool{Mode: TempToolModeToolbox, Actions: []TempToolAction{{Name: "list", Method: "GET"}, {Name: "create", Method: "POST"}}}, map[string]any{"action": "list"}, true},
		{TempTool{Mode: TempToolModeToolbox, Actions: []TempToolAction{{Name: "list", Method: "GET"}, {Name: "create", Method: "POST"}}}, map[string]any{"action": "create"}, false},
		{TempTool{Mode: TempToolModeToolbox, Actions: []TempToolAction{{Name: "list", Method: "GET"}}}, map[string]any{"action": "nope"}, false},
		{TempTool{Mode: TempToolModeShell}, nil, true},
	} {
		if got := watchableTempTool(tc.tt, tc.args); got != tc.want {
			t.Errorf("%+v %v: got %v, want %v", tc.tt.Method, tc.args, got, tc.want)
		}
	}
}

// The watch invoker resolves within what the agent has: the Operator's tools
// for the Operator (or a watch for none), only read-only operator and channel
// tools, and no other agent's scoped tools. Asserted on the source because
// the invoker is a registered closure.
func TestAWatchResolvesWithinTheAgentsOwnTools(t *testing.T) {
	src := readSourceFile(t, "operator_wake.go")
	for _, want := range []string{
		"if agentID == \"\" || agentID == defaultConsoleAgent {",
		"if len(p.ScopeAgents) > 0 && !p.ScopedToAgent(agentID) {",
		"if len(p.ScopeAgents) == 0 && off[p.Tool.Name] {",
		"if p.Tool.Name == toolName && !watchableTempTool(p.Tool, toolArgs) {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the watch invoker lost %q", want)
		}
	}
	if n := strings.Count(src, "if !watchableTool(td) {"); n < 3 {
		t.Errorf("only %d branches refuse a tool that makes changes; operator, channel and provided tools all must", n)
	}
}
