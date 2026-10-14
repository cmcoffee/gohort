package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// fakeMCPTool stands in for a registered <server>_<tool> proxy.
type fakeMCPTool struct {
	name    string
	confirm bool
	ran     *int
}

func (f fakeMCPTool) Name() string { return f.name }
func (f fakeMCPTool) Desc() string { return "Fetch a page." }
func (f fakeMCPTool) Params() map[string]ToolParam {
	return map[string]ToolParam{"page_id": {Type: "string", Description: "The page."}}
}
func (f fakeMCPTool) Run(args map[string]any) (string, error) {
	*f.ran++
	return "page " + stringArg(args, "page_id"), nil
}
func (f fakeMCPTool) Category() string   { return MCPToolCategory("mcptest") }
func (f fakeMCPTool) NeedsConfirm() bool { return f.confirm }

var mcpTestRuns int

func init() {
	RegisterChatTool(fakeMCPTool{name: "mcptest_get_page", ran: &mcpTestRuns})
	RegisterChatTool(fakeMCPTool{name: "mcptest_create_page", confirm: true, ran: &mcpTestRuns})
}

func mcpRun(t *testing.T, sess *ToolSession, tool string) (string, error) {
	t.Helper()
	return mcpToolDef(sess).Handler(context.Background(), map[string]any{"action": "run", "tool": tool, "args": map[string]any{"page_id": "42"}})
}

// Builder calls an MCP tool itself to learn what it returns; one that reads
// runs, as the person running Builder.
func TestBuilderRunsAnMCPToolThatReads(t *testing.T) {
	before := mcpTestRuns
	out, err := mcpRun(t, &ToolSession{Username: "u"}, "mcptest_get_page")
	if err != nil || out != "page 42" || mcpTestRuns != before+1 {
		t.Fatalf("run: %q %v (ran %d)", out, err, mcpTestRuns-before)
	}
}

// One that changes something asks first: approved, it runs; declined, it does
// not; with nobody watching, it is refused.
func TestAnMCPToolThatChangesSomethingAsksFirst(t *testing.T) {
	asked := ""
	sess := &ToolSession{Username: "u", AskInChat: func(prompt, detail string, yes []string) string {
		asked = prompt
		return "Run it"
	}}
	before := mcpTestRuns
	if out, err := mcpRun(t, sess, "mcptest_create_page"); err != nil || out != "page 42" || mcpTestRuns != before+1 {
		t.Fatalf("an approved run: %q %v", out, err)
	}
	if !strings.Contains(asked, "mcptest_create_page") {
		t.Errorf("the question does not name the tool: %q", asked)
	}
	sess.AskInChat = func(string, string, []string) string { return "" }
	if out, _ := mcpRun(t, sess, "mcptest_create_page"); !strings.Contains(out, "Not run") || mcpTestRuns != before+1 {
		t.Errorf("a declined run ran: %q", out)
	}
	sess.AskInChat = nil
	if _, err := mcpRun(t, sess, "mcptest_create_page"); err == nil || mcpTestRuns != before+1 {
		t.Error("a run with nobody to ask went ahead")
	}
}

// A Builder run another agent sent can look but not run: through it, any
// agent able to reach Builder would reach every MCP tool.
func TestADelegatedBuilderDoesNotRunMCPTools(t *testing.T) {
	before := mcpTestRuns
	if _, err := mcpRun(t, &ToolSession{Username: "u", DispatchParentAgentID: "other"}, "mcptest_get_page"); err == nil || mcpTestRuns != before {
		t.Error("a delegated Builder ran an MCP tool")
	}
}

// run reaches MCP tools only: a registered tool of any other kind is not
// found through it, so mcp is not a way around an agent's tool list.
func TestMCPRunReachesOnlyMCPTools(t *testing.T) {
	RegisterChatTool(plainTestTool{})
	if _, err := mcpRun(t, &ToolSession{Username: "u"}, "plain_test_tool"); err == nil {
		t.Error("mcp ran a tool that is not an MCP server's")
	}
	if _, err := mcpRun(t, &ToolSession{Username: "u"}, "mcptest_nope"); err == nil {
		t.Error("an unknown tool was not refused")
	}
}

type plainTestTool struct{}

func (plainTestTool) Name() string                            { return "plain_test_tool" }
func (plainTestTool) Desc() string                            { return "Not MCP." }
func (plainTestTool) Params() map[string]ToolParam            { return nil }
func (plainTestTool) Run(args map[string]any) (string, error) { return "ran", nil }

// Builder has the tool and its instructions say to use it, rather than
// dispatching to another agent to try an MCP tool.
func TestBuilderHasTheMCPToolAndIsToldToUseIt(t *testing.T) {
	found := false
	for _, td := range builderAuthoringTools(&ToolSession{Username: "u"}, nil) {
		if td.Tool.Name == mcpToolName {
			found = true
		}
	}
	if !found {
		t.Error("Builder's authoring tools lack mcp")
	}
	md, err := os.ReadFile("builtin/builder.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`mcp(action="run", tool, args)`, "Do not dispatch to another agent just to try one of its MCP tools"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("builder.md lacks %q", want)
		}
	}
	if out, _ := mcpToolDef(&ToolSession{Username: "u"}).Handler(context.Background(), map[string]any{"action": "get", "server": "nope"}); out != "" {
		t.Errorf("an unknown server answered: %q", out)
	}
}
