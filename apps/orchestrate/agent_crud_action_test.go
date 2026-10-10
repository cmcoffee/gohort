package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// create_agent called the way the action-taking authoring tools are called
// says where listing is, instead of only that a name is missing.
func TestCreateAgentWithAnActionPointsToTheList(t *testing.T) {
	sess := &ToolSession{Username: "u", DB: &DBase{Store: kvlite.MemStore()}}
	_, err := createAgentTool{}.RunWithSession(map[string]any{"action": "list"}, sess)
	if err == nil || !strings.Contains(err.Error(), `agents(action="list")`) || !strings.Contains(err.Error(), "only creates") {
		t.Errorf("action=list: %v", err)
	}
	_, err = createAgentTool{}.RunWithSession(map[string]any{}, sess)
	if err == nil || !strings.Contains(err.Error(), "orchestrator_prompt and allowed_tools") {
		t.Errorf("no name: %v", err)
	}
}
