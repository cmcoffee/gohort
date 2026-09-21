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

// The endpoint answers with the GATE's own tiering, not a second opinion.
//
// privilegeToolRows is that answer: "consequential" there means "would stop
// for approval on an unattended run", decided by the credential's own toggle.
// A separate computation here would drift, and the drift shows up as a control
// offering to grant something nothing withholds.
func TestTheToolPolicyEndpointUsesTheGatesOwnTiering(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, `if action == "tool-policy" {`)
	if i < 0 {
		t.Fatal("no tool-policy endpoint")
	}
	body := src[i:]
	if j := strings.Index(body, `if action == "reach/credential"`); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "privilegeToolRows(sess, ask, scoped)") {
		t.Error("the endpoint tiers tools itself instead of asking privilegeToolRows")
	}
	if !strings.Contains(body, "row.Policy") {
		t.Error("the endpoint does not return the policy")
	}
	// Owner-only: it reports how the owner's own credentials are configured.
	if !strings.Contains(body, "agent.Owner != user") {
		t.Error("the endpoint is not owner-gated")
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

// An agent on the DEFAULT POOL stores an empty allowlist — empty means "every
// catalog tool", not "no tools" — and privilegeToolRows lists from that field.
//
// Asking it about the agent directly therefore returned a map with nothing in
// it, every row in the modal fell to the first state of its ladder, and the
// whole thing read as Queues on every agent that had never curated its tools.
// Which is most of them, and is what "all the tools on all the agents" looks
// like from the outside.
func TestTheToolPolicyIsAskedOverTheToolsTheModalDraws(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, `if action == "tool-policy" {`)
	if i < 0 {
		t.Fatal("no tool-policy endpoint")
	}
	body := src[i:]
	if j := strings.Index(body, `if action == "reach/credential"`); j >= 0 {
		body = body[:j]
	}
	// The agent's own record must NOT be the thing asked about: its allowlist
	// is empty on the default pool.
	if strings.Contains(body, "privilegeToolRows(sess, agent,") {
		t.Error("asked about the agent's allowlist, which is empty on the default pool")
	}
	if !strings.Contains(body, "availableWorkerToolOptions(user)") {
		t.Error("the catalog is not part of the question, so a pool tool gets no policy")
	}
	if !strings.Contains(body, "AgentScopedTools(udb, user, agent.ID)") {
		t.Error("the agent's own tools are not part of the question")
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
