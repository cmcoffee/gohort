package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// addToolTestSess builds the minimal ToolSession add_tool needs, plus a parent
// agent and a helper sub-agent, and leaves authoring focus pointing at the
// HELPER — reproducing the state after "author a parent, then create_agent two
// helpers for it", which is where the real-world mis-attach happened.
func addToolTestSess(t *testing.T) (*ToolSession, AgentRecord, AgentRecord) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	const sid = "sess-1"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}
	parent, err := saveAgent(udb, AgentRecord{
		Name: "OSINT Parent", OrchestratorPrompt: "the intended target", Owner: owner,
	})
	if err != nil {
		t.Fatalf("saveAgent parent: %v", err)
	}
	helper, err := saveAgent(udb, AgentRecord{
		Name: "Helper Sub", OrchestratorPrompt: "a sub-agent built for the parent", Owner: owner,
	})
	if err != nil {
		t.Fatalf("saveAgent helper: %v", err)
	}
	// create_agent stamps focus onto whatever it just made — the helper.
	saveAuthoringInProgress(udb, sid, helper.ID)
	return &ToolSession{Username: owner, DB: udb, ChatSessionID: sid}, parent, helper
}

func shellToolArgs(extra map[string]any) map[string]any {
	// No script_body: these tests are about WHICH AGENT the tool lands on, and
	// shipping a script would drag in workspace minting for no added coverage.
	// The script_body path is exercised in the temptool package, which owns the
	// shared PrepareScriptBody seam.
	args := map[string]any{
		"name":             "sentiment_analyzer",
		"mode":             "shell",
		"description":      "analyze text",
		"command_template": "echo hi",
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

// TestAddToolExplicitAgentBeatsStolenFocus is the regression for the observed
// bug: with focus sitting on a helper sub-agent, naming the parent explicitly
// must attach there — not wherever focus happens to point.
func TestAddToolExplicitAgentBeatsStolenFocus(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)

	out, err := addToolTool{}.RunWithSession(shellToolArgs(map[string]any{"agent": "OSINT Parent"}), sess)
	if err != nil {
		t.Fatalf("add_tool with explicit agent: %v (out=%s)", err, out)
	}

	gotParent, ok := loadAgent(sess.DB, parent.ID)
	if !ok {
		t.Fatal("loadAgent parent failed")
	}
	if !agentHasTool(gotParent, "sentiment_analyzer") {
		t.Fatalf("tool did not land on the explicitly named agent %q; its tools=%v", parent.Name, toolNames(gotParent))
	}
	gotHelper, ok := loadAgent(sess.DB, helper.ID)
	if !ok {
		t.Fatal("loadAgent helper failed")
	}
	if agentHasTool(gotHelper, "sentiment_analyzer") {
		t.Fatal("tool ALSO landed on the focused helper — explicit agent must not fall through to focus")
	}
}

// TestAddToolResolvesAgentByID proves the explicit argument takes an id as well
// as a name (parity with agents run/run_tool, which document "Name or id").
func TestAddToolResolvesAgentByID(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)

	if _, err := (addToolTool{}).RunWithSession(shellToolArgs(map[string]any{"agent": parent.ID}), sess); err != nil {
		t.Fatalf("add_tool by id: %v", err)
	}
	got, _ := loadAgent(sess.DB, parent.ID)
	if !agentHasTool(got, "sentiment_analyzer") {
		t.Fatal("tool did not land on the agent named by id")
	}
}

// TestAddToolFallsBackToFocus keeps the ergonomic default honest: with no
// agent argument, focus still governs.
func TestAddToolFallsBackToFocus(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)

	if _, err := (addToolTool{}).RunWithSession(shellToolArgs(nil), sess); err != nil {
		t.Fatalf("add_tool via focus: %v", err)
	}
	gotHelper, _ := loadAgent(sess.DB, helper.ID)
	if !agentHasTool(gotHelper, "sentiment_analyzer") {
		t.Fatal("tool did not land on the focused agent when no agent argument was passed")
	}
	gotParent, _ := loadAgent(sess.DB, parent.ID)
	if agentHasTool(gotParent, "sentiment_analyzer") {
		t.Fatal("tool leaked onto the unfocused parent")
	}
}

// TestAddToolUnknownAgentErrors — a typo'd name must fail loudly rather than
// silently falling back to focus and attaching somewhere unintended.
func TestAddToolUnknownAgentErrors(t *testing.T) {
	sess, _, helper := addToolTestSess(t)

	if _, err := (addToolTool{}).RunWithSession(shellToolArgs(map[string]any{"agent": "No Such Agent"}), sess); err == nil {
		t.Fatal("expected an error for an unknown agent name, got nil")
	}
	gotHelper, _ := loadAgent(sess.DB, helper.ID)
	if agentHasTool(gotHelper, "sentiment_analyzer") {
		t.Fatal("unknown agent name fell back to focus and attached the tool — must error instead")
	}
}

func agentHasTool(a AgentRecord, name string) bool {
	for _, t := range a.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func toolNames(a AgentRecord) []string {
	var out []string
	for _, t := range a.Tools {
		out = append(out, t.Name)
	}
	return out
}
