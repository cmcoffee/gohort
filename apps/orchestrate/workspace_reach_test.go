package orchestrate

// The per-agent ceiling on what its workspace may dial.
//
// ForcePrivate was the only way to say "no network", and it takes the agent's
// tools and its model with it: right for a compliance bot, useless for
// "process this text locally and do not phone anywhere from in there".

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/netgate"
)

// readRepoFile reads a source file outside this package, for the guards that
// are about two files agreeing.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// It inherits DOWNWARD and only narrows, which is the direction every
// restriction here inherits. Building a sub-agent is otherwise how you launder
// one.
func TestTheCeilingInheritsDownADispatch(t *testing.T) {
	open := AgentRecord{ID: "child"}
	closed := AgentRecord{ID: "child", WorkspaceNoNetwork: true}

	// A parent that may dial, dispatching to a child that may not.
	ctx, _ := applyForcePrivateToDispatch(context.Background(), nil, nil, closed)
	if netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("the child's own ceiling was ignored")
	}

	// A parent that may NOT dial, dispatching to a child with no ceiling of
	// its own: the child inherits it and cannot widen.
	parent := netgate.WithWorkspaceNetwork(context.Background(), false)
	ctx, _ = applyForcePrivateToDispatch(parent, nil, nil, open)
	if netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("a sub-agent laundered its parent's ceiling")
	}

	// Neither restricted: unchanged.
	ctx, _ = applyForcePrivateToDispatch(context.Background(), nil, nil, open)
	if !netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("an unrestricted dispatch lost its network")
	}
}

// Both doors out of the sandbox. Closing the namespace and leaving the proxy
// hook open would just move a script from curl to gohort.fetch, which is the
// route the sandbox docs tell it to prefer.
func TestBothWaysOutAreClosed(t *testing.T) {
	src := readRepoFile(t, "../../core/sandbox_hook.go")
	if !strings.Contains(src, "!h.Sess.WorkspaceNetworkAllowed()") {
		t.Error("the gohort.fetch hook does not honour the workspace ceiling")
	}
	exec := readRepoFile(t, "../../core/sandbox/exec.go")
	if !strings.Contains(exec, "netgate.WorkspaceNetworkFrom(ctx)") {
		t.Error("the sandbox namespace does not honour the workspace ceiling")
	}
	if strings.Contains(exec, "allowNetwork := netgate.NetworkAllowedFromContext(ctx)") {
		t.Error("the namespace reads the privacy connector alone again")
	}
}

// The setting is on screen and saveable. A toggle that stores nothing is worse
// than no toggle.
func TestTheCeilingIsOfferedAndSaveable(t *testing.T) {
	src := packageSource(t)
	// Offered in SECURITY, not the editor. It was in both, which is worse than
	// being in the wrong one: two controls over one fact drift, and the one
	// you did not use is the one you go on believing.
	//
	// Named per FILE rather than over the package, because both files are in
	// it: asking the package whether the toggle exists cannot tell which page
	// is offering it, which is the entire question.
	editor := mustReadFile(t, "page_agent.go")
	security := mustReadFile(t, "page_agent_access.go")
	if strings.Contains(editor, `Field: "workspace_n`) {
		t.Error("the editor offers it again, so there are two controls over one fact")
	}
	// A tri-state SELECT, not a toggle: a bool cannot hold "not decided here,
	// use the default for all agents", and that third state is the whole
	// point of a default.
	if !strings.Contains(security, `Field: "workspace_network", Type: "select"`) {
		t.Error("the Security page does not offer the setting, so it is offered nowhere")
	}
	if !strings.Contains(security, "Use the default for all agents") {
		t.Error("the agent cannot be returned to the default once it has answered")
	}
	if !strings.Contains(src, `"workspace:" + ag.ID + ":network"`) {
		t.Error("the Security window's own row is gone, so its state is not reviewable")
	}
	// Writable from there, whichever way it is currently set.
	if !strings.Contains(src, `case "workspace":`) {
		t.Error("the setting is shown but nothing writes it back")
	}
	if !patchAgentFields["workspace_no_network"] {
		t.Error("the PATCH allowlist drops it, so an import or a form update cannot carry it")
	}
	// Set where the turn's context is assembled, or nothing downstream sees it.
	// Resolved, not read off one field: the agent's own answer, then a record
	// written before the tri-state, then the owner's default for all agents.
	if !strings.Contains(src, "netgate.WithWorkspaceNetwork(ctx, agentWorkspaceNetwork(") {
		t.Error("the live turn never attaches the ceiling")
	}
	if strings.Contains(src, "WithWorkspaceNetwork(ctx, !agent.WorkspaceNoNetwork)") {
		t.Error("the live turn reads the legacy bool alone, so a fleet default reaches nothing")
	}
}

// A restriction on a parent reaches its children. Building a sub-agent is
// otherwise how you launder one — the same direction guardrails and the
// never-unattended mark already travel.
func TestWithheldActionsInheritDownTheOwnerChain(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)

	if _, err := saveAgent(udb, AgentRecord{
		ID: "parent", Owner: "alice", Name: "Parent", OrchestratorPrompt: "p",
		DisabledToolActions: []string{"workspace/run"},
	}); err != nil {
		t.Fatal(err)
	}
	child := AgentRecord{
		ID: "child", Owner: "alice", Name: "Child", OrchestratorPrompt: "p",
		OwnedBy:             "parent",
		DisabledToolActions: []string{"workspace/write"},
	}
	if _, err := saveAgent(udb, child); err != nil {
		t.Fatal(err)
	}

	turn := &chatTurn{agent: child, user: "alice", udb: udb}
	got := map[string]bool{}
	for _, p := range turn.withheldToolActions() {
		got[p] = true
	}
	if !got["workspace/write"] {
		t.Error("the child lost its own restriction")
	}
	if !got["workspace/run"] {
		t.Error("a sub-agent laundered its parent's restriction")
	}

	// A top-level agent carries only its own.
	top := &chatTurn{agent: AgentRecord{ID: "solo", Owner: "alice",
		DisabledToolActions: []string{"workspace/run"}}, user: "alice", udb: udb}
	if list := top.withheldToolActions(); len(list) != 1 || list[0] != "workspace/run" {
		t.Errorf("a top-level agent picked up something: %v", list)
	}
}

// The setting is offered over the actions that DO something, and saveable.
func TestTheNarrowingIsOfferedAndSaveable(t *testing.T) {
	src := packageSource(t)
	if !strings.Contains(src, `Field: "disabled_tool_actions", Type: "checklist"`) {
		t.Error("the narrowing is not offered in the editor")
	}
	if !patchAgentFields["disabled_tool_actions"] {
		t.Error("it is offered but the PATCH allowlist drops it")
	}
	// Read actions are the reason a tool was granted; offering to withhold one
	// is a decision with no upside and a longer list.
	if !strings.Contains(src, "if capsAreReadOnly(caps[action]) {") {
		t.Error("the option list offers read-only actions")
	}
	// Handed to the session beside the tool names, or nothing downstream sees it.
	if !strings.Contains(src, "sess.SetWithheldActions(t.withheldToolActions())") {
		t.Error("the turn never tells the session what is withheld")
	}
}

// mustReadFile reads one source file of this package, for an assertion that is
// about WHICH file something lives in.
func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
