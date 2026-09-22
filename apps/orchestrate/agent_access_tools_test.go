package orchestrate

// The page built to answer "what can this agent do" listed tools out of
// rec.AllowedTools, which is EMPTY on a default-pool agent because empty means
// "every catalog tool". So the commonest kind of agent showed no tools at all,
// on the one surface whose whole job is that question.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The regression, stated as the thing that was wrong: an agent with an empty
// allowlist has the WHOLE catalog, and must not read as having nothing.
func TestADefaultPoolAgentResolvesItsWholeCatalog(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a1", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"} // AllowedTools nil = default pool
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("a default-pool agent resolved to no tools, which is what the old AllowedTools read did")
	}
	// Sanity that these are real resolved names, not a placeholder.
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			t.Error("a resolved row has no name")
		}
	}
}

// A tool in the owner's pool can carry a flag; a framework tool has no record
// to hold one. The page has to say which, or it offers controls that do
// nothing and hides ones that would work.
func TestOnlyToolsWithARecordReadAsGovernable(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "access_row_tool", CommandTemplate: "echo hi", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a2", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	var mine, framework int
	for _, r := range rows {
		if r.Name == "access_row_tool" {
			mine++
			if !r.Governable {
				t.Error("a tool in the owner's pool does not read as governable, so its controls are hidden")
			}
			if !r.Asks {
				t.Error("the ask-before-every-call flag did not reach the row")
			}
			if r.Origin != "your tools" {
				t.Errorf("origin should say where it came from, got %q", r.Origin)
			}
		}
		if r.Origin == "framework" {
			framework++
			if r.Governable {
				t.Errorf("%q reads as governable but has no record to carry a flag", r.Name)
			}
		}
	}
	if mine == 0 {
		t.Error("the owner's own tool is missing from the agent's resolved catalog")
	}
	if framework == 0 {
		t.Error("no framework tools resolved, so the governable check proved nothing")
	}
}

// Governable rows sort first. They are what somebody opened this page to act
// on, and burying them under the framework catalog is how a control surface
// becomes a list nobody scrolls.
func TestTheRowsYouCanActOnComeFirst(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "zzz_last_alphabetically", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a3", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 || !rows[0].Governable {
		t.Fatalf("a tool the owner can act on is not first; got %+v", rows[0])
	}
	seenPlain := false
	for _, r := range rows {
		if !r.Governable {
			seenPlain = true
		} else if seenPlain {
			t.Errorf("%q is governable but sorts after a row that is not", r.Name)
		}
	}
}

// Both controls write through the SAME setters the Permissions page uses.
// Two surfaces storing one fact two ways is how they start disagreeing, and
// this pair has to agree: a tool set to ask here must read as asking there.
func TestTheAccessPageWritesThroughTheSameSetters(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "shared_row_tool", CommandTemplate: "curl x"}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a9", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch,
			"/api/agent-access/tool?agent=a9&name=shared_row_tool", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleAgentAccessTool(w, asUser(r, "alice"))
		return w.Code
	}
	if code := post(`{"asks":true}`); code != http.StatusNoContent {
		t.Fatalf("setting ask: %d", code)
	}
	// The Permissions page is the other reader of that same fact.
	if row := rowByDetail(permRowsFor(t, app, "alice"), "Asks before every call"); row == nil {
		t.Error("a tool set to ask from the access page does not appear on the Permissions page")
	}
	if code := post(`{"unattended":"block"}`); code != http.StatusNoContent {
		t.Fatalf("setting unattended: %d", code)
	}
	if row := rowByDetail(permRowsFor(t, app, "alice"), "Never unattended"); row == nil {
		t.Error("an unattended decision made here is not reviewable on the Permissions page")
	}
}

// A control that takes a click and stores nothing is worse than one that is
// not offered: the row shows the new state until the next reload, then
// quietly reverts.
func TestSettingAskOnAToolWithNoRecordIsRefused(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a10", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch,
		"/api/agent-access/tool?agent=a10&name=web_search", strings.NewReader(`{"asks":true}`))
	w := httptest.NewRecorder()
	app.handleAgentAccessTool(w, asUser(r, "alice"))
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404 for a framework tool with no record, got %d %s", w.Code, w.Body.String())
	}
}

// An unattended value the gate does not recognize is refused, not stored. A
// stored nonsense policy reads as SOME state on the page and behaves as none.
func TestAnUnknownUnattendedValueIsRefused(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a11", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch,
		"/api/agent-access/tool?agent=a11&name=web_search", strings.NewReader(`{"unattended":"sometimes"}`))
	w := httptest.NewRecorder()
	app.handleAgentAccessTool(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d %s", w.Code, w.Body.String())
	}
}

// A RowAction's PostTo placeholders are FIELD NAMES resolved against the row.
// One naming no field resolves to EMPTY rather than erroring, so the control
// renders, takes the click, and posts to a URL with no tool in it. The
// RowAction doc said "{row_key} substituted", which is not what the
// substituter does, and that is exactly how this shipped dead once.
func TestTheToolControlsPostToAFieldThatExists(t *testing.T) {
	src, err := os.ReadFile("page_agent_access.go")
	if err != nil {
		t.Fatal(err)
	}
	// The ASSIGNMENT, not the file: the comment above it names the wrong
	// placeholder on purpose, to say why it is wrong.
	var assign string
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "toolWrite :=") {
			assign = line
		}
	}
	if assign == "" {
		t.Fatal("the tool write URL is gone; the controls have nowhere to post")
	}
	if strings.Contains(assign, "{row_key}") {
		t.Error("the write URL uses {row_key}, which names no field on the row and resolves to empty")
	}
	if !strings.Contains(assign, "name={name}") {
		t.Errorf("the write URL does not carry the tool name, so every row would post the same request: %s", strings.TrimSpace(assign))
	}
	// The placeholder has to match a field the rows actually carry.
	if !strings.Contains(mustRead(t, "agent_access_tools.go"), "`json:\"name\"`") {
		t.Error("rows do not expose a name field for the placeholder to resolve")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
