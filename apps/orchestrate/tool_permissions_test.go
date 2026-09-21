package orchestrate

// The permission on a tool lives on the row that grants the tool.
//
// It used to be two checklists in the agent editor — "Pre-approved tools" and
// "Never unattended" — over the same option set, on a page that never let you
// pick a tool at all. You ticked a name there and went to the Tools list to
// find out whether the agent could even call it.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// One place asks it now. A checklist reappearing in the editor is the
// regression this guards.
func TestTheEditorNoLongerAsksAboutToolPermissions(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "func agentFormFields")
	if i < 0 {
		i = strings.Index(src, `Field: "daily_spend_usd"`)
	}
	if i < 0 {
		t.Fatal("cannot find the editor's field list")
	}
	for _, gone := range []string{
		`{Field: "auto_approve_tools", Type: "checklist"`,
		`{Field: "no_unattended_tools", Type: "checklist"`,
	} {
		if strings.Contains(src, gone) {
			t.Errorf("the editor asks again: %s", gone)
		}
	}
}

// The ladder is offered on exactly the tools that could stop and ask. Two
// definitions of that would drift into a row offering a setting the gate never
// consults, so the names come from the options.
func TestTheLadderIsOfferedOnWhatCanActuallyPrompt(t *testing.T) {
	opts := approvableToolOptions("")
	names := approvableToolNames("")
	if len(names) != len(opts) {
		t.Fatalf("names = %d, options = %d; they must be the same pool", len(names), len(opts))
	}
	for i, o := range opts {
		if names[i] != o.Value {
			t.Errorf("name %d = %q, want %q", i, names[i], o.Value)
		}
	}
}

// The page hands the modal that pool, or every row renders as read-only and
// the ladder never appears.
func TestThePageShipsTheApprovablePool(t *testing.T) {
	src := packageSource(t)
	if !strings.Contains(src, "window.ORCH_APPROVABLE_TOOLS = ") {
		t.Error("the modal is never told which tools can prompt")
	}
	if !strings.Contains(src, "approvableToolNames(user)") {
		t.Error("the pool is not built from the shared predicate")
	}
}

// A tool change is a real change to what the agent may do, so it belongs in
// the version history like any other edit. What it must not be is unlabelled:
// a history of identical "update" rows is one nobody can scan for the version
// worth rolling back to.
func TestAToolsModalSaveIsLabelledInTheHistory(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "fromToolsModal := r.URL.Query()")
	if i < 0 {
		t.Fatal("cannot find the tools-modal save path")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, `reason = "changed tools"`) {
		t.Error("a tools-modal save records an unlabelled revision")
	}
	if !strings.Contains(body, "saveAgentAs(udb, req, reason)") {
		t.Error("the reason is computed and then not used")
	}
}

// The union really does produce rows for both kinds. privilegeToolRows takes
// the allowlist AND the bundled set, and a tool in either must come back.
func TestPrivilegeRowsCoverBothTheAllowlistAndTheBundledSet(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	prev := ListUserAgentTools
	ListUserAgentTools = func(Database, string) []TempTool { return nil }
	defer func() { ListUserAgentTools = prev }()

	rec := AgentRecord{ID: "tp2", AllowedTools: []string{"from_catalog"}}
	bundled := []TempTool{{Name: "from_agent", CommandTemplate: "echo hi"}}
	rows := privilegeToolRows(sess, rec, bundled)

	seen := map[string]string{}
	for _, r := range rows {
		seen[r.Name] = r.Policy
	}
	if _, ok := seen["from_catalog"]; !ok {
		t.Error("a tool named in the allowlist got no policy")
	}
	if _, ok := seen["from_agent"]; !ok {
		t.Error("a tool bundled with the agent got no policy")
	}
	// A benign shell tool is not withheld, so the modal must show it Runs
	// rather than offering an approval that grants nothing.
	if seen["from_agent"] != "auto" {
		t.Errorf("a tool nothing withholds tiered as %q", seen["from_agent"])
	}
}

// The policy comes from the GATE, with a nil session, exactly as the schedule
// pre-flight reads it.
//
// The first cut went through privilegeToolRows with a ToolSession built out of
// nothing here. classifyPrivilegeTool answers "unresolved, therefore
// consequential" for anything it cannot resolve, and a bare session resolves
// very little — so every catalog tool came back consequential and the whole
// modal read as Queues while the tools ran perfectly well.
func TestTheToolPolicyComesFromTheGate(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "func (T *OrchestrateApp) toolPolicyFor")
	if i < 0 {
		t.Fatal("no toolPolicyFor")
	}
	body := src[i:]
	if j := strings.Index(body[10:], "\nfunc "); j >= 0 {
		body = body[:j+10]
	}
	if !strings.Contains(body, "T.newAutonomousGate(user, agent.ID, nil)") {
		t.Error("the policy is not read from the gate")
	}
	for _, want := range []string{"gate.neverUnattended(name)", "gate.allows(name)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the gate is not asked: %s", want)
		}
	}
	if strings.Contains(body, "privilegeToolRows") {
		t.Error("back on the classifier, which reports unresolvable as consequential")
	}
	// Over the tools the modal draws, not the agent's allowlist.
	if !strings.Contains(body, "availableWorkerToolOptions(user)") ||
		!strings.Contains(body, "AgentScopedTools(udb, user, agent.ID)") {
		t.Error("the question is not posed over the set the modal renders")
	}
}

// A tool nothing withholds is not offered an approval that grants nothing, and
// a pre-approved one can still be taken back from the same control.
func TestTheOfferedStatesMatchWhatWouldChangeTheTool(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "func (T *OrchestrateApp) toolPolicyFor")
	body := src[i:]
	if j := strings.Index(body[10:], "\nfunc "); j >= 0 {
		body = body[:j+10]
	}
	if !strings.Contains(body, "Options: []string{toolPermRuns, toolPermAttended}") {
		t.Error("a freely-running tool is offered approvals that grant nothing")
	}
	if !strings.Contains(body, "Options: []string{toolPermQueues, toolPermAlways, toolPermAttended}") {
		t.Error("a pre-approved tool cannot be taken back from the control that shows it")
	}
}
