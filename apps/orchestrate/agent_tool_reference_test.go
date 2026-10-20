package orchestrate

// From an observed Builder session: asked to fold a set of calendar tools into
// one, Builder re-pointed an agent by sending update_agent the agent's tools
// in the shape agents(action="get") lists them, {name, mode}. Each entry was
// read as a new, EMPTY definition, so the owner was asked to "replace the
// definition" of an unrelated phone-call tool the agent already had, and a yes
// would have wiped it.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestANameOnlyToolEntryAttachesTheToolUnchanged(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)
	calls := TempTool{Name: "phone_calls", Description: "Places calls.", Mode: "api", CommandTemplate: "/calls", Credential: "phone_api", Method: "POST"}
	if err := bundleAgentToolByID(sess.DB, sess.Username, parent.ID, calls); err != nil {
		t.Fatal(err)
	}
	asked := 0
	sess.ApproveChange = func(kind, name, detail string, op ChangeOp) string {
		if kind == "tool" {
			asked++
			return "NOT DONE: the owner declined this change to the tool " + name
		}
		return ""
	}

	// Both agents get it by name: no ask, the definition kept, and the second
	// agent added to its scope.
	for _, a := range []AgentRecord{parent, helper} {
		out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": a.ID, "tools": []any{map[string]any{"name": "phone_calls", "mode": "api"}}}, sess)
		if err != nil || strings.Contains(out, "NOT DONE") {
			t.Fatalf("%s: a name-only entry was refused: %v %s", a.Name, err, out)
		}
	}
	if asked != 0 {
		t.Errorf("a name-only entry asked to replace the tool %d time(s)", asked)
	}
	got, ok := UserToolByName(sess.DB, sess.Username, "phone_calls")
	if !ok || !got.Tool.SameDefinition(calls) {
		t.Fatalf("the tool's definition changed: %+v", got.Tool)
	}
	if !got.ScopedToAgent(parent.ID) || !got.ScopedToAgent(helper.ID) {
		t.Errorf("scope = %v, want both agents", got.ScopeAgents)
	}

	// A name that is no tool of theirs has nothing to attach.
	out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": parent.ID, "tools": []any{map[string]any{"name": "no_such_tool"}}}, sess)
	if err != nil || !strings.Contains(out, "no tool by that name") {
		t.Errorf("an unknown name-only entry: %v %q", err, out)
	}
	if _, ok := UserToolByName(sess.DB, sess.Username, "no_such_tool"); ok {
		t.Error("an empty tool was saved under an unknown name")
	}

	// A real new definition under the same name still asks.
	changed := calls
	changed.CommandTemplate = "/calls/v2"
	out, _ = updateAgentTool{}.RunWithSession(map[string]any{"id": parent.ID, "tools": []any{changed}}, sess)
	if asked != 1 || !strings.Contains(out, "declined") {
		t.Errorf("a changed definition went ahead unasked: asked %d, %q", asked, out)
	}
}
