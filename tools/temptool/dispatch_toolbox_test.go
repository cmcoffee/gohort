package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A toolbox that could only wrap an API was half a toolbox. "Several related
// commands under one name" is the same shape as "several related endpoints
// under one name" — a local binary is one thing with several verbs, and mapping
// it into loose tools scattered across a catalog loses that.
//
// What is asserted is the HANDOFF, not the execution. A shell action is built
// into an ordinary shell-mode tool and given to the shell path, so quoting,
// sandboxing and workspace handling are the ones that path already has tests
// for; re-testing them here would be testing a copy. The distinction that
// matters is WHICH path it took, and the two fail differently enough to tell:
// the API path refuses without a DB, the shell path goes looking for a sandbox.
func TestToolboxActionTakesTheShellPath(t *testing.T) {
	tb := &TempTool{
		Name: "capture", Mode: TempToolModeToolbox,
		Actions: []TempToolAction{{
			Name: "echo", Description: "echo a value",
			CommandTemplate: "echo {value}",
			Params:          map[string]ToolParam{"value": {Type: "string"}},
			Required:        []string{"value"},
		}},
	}
	// No DB on purpose: an api-mode dispatch refuses outright without one, so
	// that error appearing here would mean the action was treated as HTTP.
	out, err := dispatchToolboxModeTempTool(&ToolSession{Username: "u"}, tb,
		map[string]any{"action": "echo", "value": "mapped"})
	combined := out
	if err != nil {
		combined += err.Error()
	}
	if strings.Contains(combined, "requires a session with DB access") {
		t.Fatalf("a local action was dispatched as an HTTP one: %v", err)
	}
	if !strings.Contains(combined, "workspace") && !strings.Contains(combined, "dispatch") {
		t.Fatalf("expected the shell path's own failure without a workspace, got out=%q err=%v", out, err)
	}
}

// The HTTP action keeps working exactly as before — the branch is additive, and
// a toolbox of endpoints must not start looking for a sandbox.
func TestToolboxHTTPActionIsUnchanged(t *testing.T) {
	tb := &TempTool{
		Name: "api", Mode: TempToolModeToolbox, Credential: "no_auth",
		Actions: []TempToolAction{{
			Name: "get", URLTemplate: "https://example.test/thing",
		}},
	}
	_, err := dispatchToolboxModeTempTool(&ToolSession{Username: "u"}, tb,
		map[string]any{"action": "get"})
	if err == nil || !strings.Contains(err.Error(), "requires a session with DB access") {
		t.Errorf("an endpoint action must still take the api path: %v", err)
	}
}

// An action is an HTTP call or a local command. Both is two actions wearing one
// name, and choosing a winner silently would make the toolbox do something its
// author did not write.
func TestToolboxActionMustDeclareExactlyOneTemplate(t *testing.T) {
	both := &TempTool{Name: "x", Mode: TempToolModeToolbox, Actions: []TempToolAction{{
		Name: "a", URLTemplate: "https://x.test/", CommandTemplate: "echo hi"}}}
	_, err := dispatchToolboxModeTempTool(&ToolSession{Username: "u"}, both, map[string]any{"action": "a"})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("an action with both templates must be refused by name: %v", err)
	}

	neither := &TempTool{Name: "x", Mode: TempToolModeToolbox, Actions: []TempToolAction{{Name: "a"}}}
	_, err = dispatchToolboxModeTempTool(&ToolSession{Username: "u"}, neither, map[string]any{"action": "a"})
	if err == nil || !strings.Contains(err.Error(), "nothing for it to run") {
		t.Errorf("an action with no template must say so plainly: %v", err)
	}
}

// Required args are checked before the command runs, on the ACTION's own
// params — the outer toolbox only enforces that an action was named.
func TestToolboxShellActionChecksItsOwnRequiredArgs(t *testing.T) {
	tb := &TempTool{Name: "capture", Mode: TempToolModeToolbox, Actions: []TempToolAction{{
		Name: "echo", CommandTemplate: "echo {value}",
		Params: map[string]ToolParam{"value": {Type: "string"}}, Required: []string{"value"}}}}

	if _, err := dispatchToolboxModeTempTool(&ToolSession{Username: "u"}, tb,
		map[string]any{"action": "echo"}); err == nil || !strings.Contains(err.Error(), "value") {
		t.Errorf("a missing required arg must be named: %v", err)
	}
	if _, err := dispatchToolboxModeTempTool(&ToolSession{Username: "u"}, tb,
		map[string]any{"action": "echo", "value": "   "}); err == nil {
		t.Error("a whitespace-only required arg is not supplied")
	}
}
