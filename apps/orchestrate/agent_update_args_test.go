package orchestrate

// Regressions from an observed Builder session: update_agent called with no id
// was told agent "<nil>" did not exist, a call that re-sent only the agent's
// existing name reported AGENT_UPDATED ok and ordered the turn to end, and the
// model then told the user an edit had landed that it never sent.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

func TestAMissingIdSaysSoInsteadOfLookingUpNil(t *testing.T) {
	sess, _, _ := addToolTestSess(t)
	for _, tool := range []interface {
		RunWithSession(map[string]any, *ToolSession) (string, error)
	}{updateAgentTool{}, cloneAgentTool{}, deleteAgentTool{}} {
		_, err := tool.RunWithSession(map[string]any{"description": "x"}, sess)
		if err == nil {
			t.Fatalf("%T with no id succeeded", tool)
		}
		if strings.Contains(err.Error(), "<nil>") || !strings.Contains(err.Error(), "needs id") {
			t.Errorf("%T: %v", tool, err)
		}
	}
}

// agents() and add_tool call the target "agent"; update_agent calls it "id".
// The model carries one into the other, so both are read.
func TestUpdateAgentTakesAgentAsWellAsId(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	out, err := updateAgentTool{}.RunWithSession(map[string]any{"agent": parent.Name, "description": "now covers more"}, sess)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "Changed: description") {
		t.Errorf("the result should name what changed:\n%s", out)
	}
	if got, _ := loadAgent(sess.DB, parent.ID); got.Description != "now covers more" {
		t.Errorf("description = %q", got.Description)
	}
}

func TestAnUpdateThatChangesNothingSaysSo(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	before, _ := loadAgent(sess.DB, parent.ID)
	out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": parent.ID, "name": parent.Name}, sess)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "NOTHING CHANGED") || strings.Contains(out, "END THE TURN") {
		t.Errorf("a no-op update must say nothing changed and must not end the turn:\n%s", out)
	}
	if !strings.Contains(out, "name") {
		t.Errorf("it should say which fields the call set:\n%s", out)
	}
	if after, _ := loadAgent(sess.DB, parent.ID); !after.Updated.Equal(before.Updated) {
		t.Error("a no-op update was saved anyway")
	}
}

func TestACloneWithNoNameIsNotNamedNil(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	out, err := cloneAgentTool{}.RunWithSession(map[string]any{"id": parent.ID}, sess)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(out, "<nil>") {
		t.Errorf("the clone was named after a missing argument:\n%s", out)
	}
}
