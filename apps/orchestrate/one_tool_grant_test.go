package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// fakeOneTool is an MCP server offered as one tool, and fakeOneProxy one of
// its tools as it would be named separately.
type fakeOneTool struct{}

func (fakeOneTool) Name() string                       { return "fakeonesrv" }
func (fakeOneTool) Desc() string                       { return "d" }
func (fakeOneTool) Params() map[string]ToolParam       { return nil }
func (fakeOneTool) Run(map[string]any) (string, error) { return "", nil }
func (fakeOneTool) ActionCaps() map[string][]Capability {
	return map[string][]Capability{"search": {CapRead}, "create_ticket": {CapRead}, "delete_ticket": {CapRead}}
}

type fakeOneProxy struct{ name, action string }

func (p fakeOneProxy) Name() string                     { return p.name }
func (fakeOneProxy) Desc() string                       { return "d" }
func (fakeOneProxy) Params() map[string]ToolParam       { return nil }
func (fakeOneProxy) Run(map[string]any) (string, error) { return "", nil }
func (p fakeOneProxy) RedirectAction() (string, string) { return "fakeonesrv", p.action }

// An agent granted one of a one-tool MCP server's tools reaches that action
// and no other. The grant used to reach the whole server, so an agent given
// search could call create_ticket through the one tool.
func TestAGrantOfOneToolReachesOnlyItsAction(t *testing.T) {
	RegisterChatTool(fakeOneTool{})
	RegisterChatTool(fakeOneProxy{"fakeonesrv_search", "search"})
	RegisterChatTool(fakeOneProxy{"fakeonesrv_create_ticket", "create_ticket"})

	some := &chatTurn{user: "u", agent: AgentRecord{ID: "a", AllowedTools: []string{"fakeonesrv_search"}}}
	got := strings.Join(some.partialOneToolWithheld(), ",")
	if got != "fakeonesrv/create_ticket,fakeonesrv/delete_ticket" {
		t.Errorf("withheld = %q, want every action but search", got)
	}
	whole := &chatTurn{user: "u", agent: AgentRecord{ID: "a", AllowedTools: []string{"fakeonesrv"}}}
	if w := whole.partialOneToolWithheld(); len(w) != 0 {
		t.Errorf("a grant of the one tool itself keeps every action, withheld %v", w)
	}
	pool := &chatTurn{user: "u", agent: AgentRecord{ID: "a"}}
	if w := pool.partialOneToolWithheld(); len(w) != 0 {
		t.Errorf("an agent on the default pool has nothing withheld, got %v", w)
	}
	if !strings.Contains(strings.Join(some.withheldToolActions(), ","), "fakeonesrv/create_ticket") {
		t.Error("the turn's withheld actions must carry it")
	}
}
