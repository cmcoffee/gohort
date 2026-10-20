package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// ToolHolders names the agents a tool is scoped to and the ones whose tool
// list names it, so a delete can say who loses it.
func TestToolHoldersNamesWhoHasTheTool(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)
	(&OrchestrateApp{}).wireAgentTools()
	if err := bundleAgentToolByID(sess.DB, sess.Username, parent.ID, TempTool{Name: "cal", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	listed, err := saveAgent(sess.DB, AgentRecord{Name: "Lister", Owner: sess.Username, OrchestratorPrompt: "p", AllowedTools: []string{"cal"}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ToolHolders(sess.DB, sess.Username, "cal"), ",")
	if !strings.Contains(got, parent.Name) || !strings.Contains(got, listed.Name) || strings.Contains(got, helper.Name) {
		t.Errorf("holders = %q, want %s and %s only", got, parent.Name, listed.Name)
	}
	if h := ToolHolders(sess.DB, sess.Username, "nothing"); len(h) != 0 {
		t.Errorf("a tool nobody has has holders: %v", h)
	}
}
