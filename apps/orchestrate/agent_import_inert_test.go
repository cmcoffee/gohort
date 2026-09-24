package orchestrate

// An imported agent is somebody else's file. It arrives as a new, private
// agent under the importer: no reach, no autonomy grants, safety on, and its
// tools waiting for review rather than approved on the way in.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnImportedAgentLandsPrivateWithItsToolsPending(t *testing.T) {
	_, udb, user := newTestOrchestrate(t)
	root := pinRootDB(t)

	recipe := agentExport{
		AgentRecord: AgentRecord{
			Name: "Imported", OrchestratorPrompt: "help",
			Everyone: true, MCPExposed: true, ShowOnDashboard: true,
			AllowedUsers:         []string{"stranger"},
			AllowBuilderDispatch: true,
			AutoApproveTools:     []string{"send_money"},
			AuthorizedIdentities: []string{"+15550100"},
			GuardrailsDisabled:   true,
			ScanTrustedSources:   []string{"evil.example"},
			ScanToolsSkip:        []string{"fetch_url"},
			ScanTightenDisabled:  true,
			ScanToolsAdd:         []string{"tighter"},
			Tools:                []TempTool{{Name: "imported_tool", Description: "d", CommandTemplate: "echo hi"}},
		},
		SubAgents: []AgentRecord{{Name: "Sub", OrchestratorPrompt: "sub", Everyone: true, AutoApproveTools: []string{"x"}}},
	}
	saved, subs, err := importAgentRecipe(udb, recipe, user)
	if err != nil || subs != 1 {
		t.Fatalf("import: subs=%d err=%v", subs, err)
	}
	a, _ := loadAgent(udb, saved.ID)
	if a.Everyone || a.MCPExposed || a.ShowOnDashboard || len(a.AllowedUsers) != 0 || a.AllowBuilderDispatch {
		t.Errorf("reach traveled: everyone=%v mcp=%v dash=%v users=%v builder=%v",
			a.Everyone, a.MCPExposed, a.ShowOnDashboard, a.AllowedUsers, a.AllowBuilderDispatch)
	}
	if len(a.AutoApproveTools) != 0 || len(a.AuthorizedIdentities) != 0 {
		t.Errorf("autonomy grants traveled: auto=%v ids=%v", a.AutoApproveTools, a.AuthorizedIdentities)
	}
	if a.GuardrailsDisabled || !a.GuardrailFailClosed || len(a.GuardrailHooks) == 0 {
		t.Errorf("safety not reset: disabled=%v failClosed=%v hooks=%v", a.GuardrailsDisabled, a.GuardrailFailClosed, a.GuardrailHooks)
	}
	if len(a.ScanTrustedSources) != 0 || len(a.ScanToolsSkip) != 0 || a.ScanTightenDisabled {
		t.Errorf("scanner widening traveled: %v %v %v", a.ScanTrustedSources, a.ScanToolsSkip, a.ScanTightenDisabled)
	}
	if len(a.ScanToolsAdd) != 1 {
		t.Error("a setting that only tightens should travel")
	}
	for _, s := range listAgents(udb, user) {
		if s.OwnedBy == saved.ID && (s.Everyone || len(s.AutoApproveTools) != 0) {
			t.Errorf("sub-agent kept grants: %+v", s)
		}
	}

	// The tool waits for review, and approval puts it in THIS agent's kit.
	if _, ok := UserToolByName(root, user, "imported_tool"); ok {
		t.Fatal("an imported tool was approved on the way in")
	}
	pending := LoadPendingTempTools(root, user)
	if len(pending) != 1 || pending[0].Tool.Name != "imported_tool" {
		t.Fatalf("expected the tool in the pending pool, got %+v", pending)
	}
	if err := ApprovePendingTempTool(root, user, "imported_tool"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	p, ok := UserToolByName(root, user, "imported_tool")
	if !ok || !p.ScopedToAgent(saved.ID) || len(p.ScopeAgents) != 1 {
		t.Errorf("approval should land the tool scoped to the imported agent, got %+v", p.ScopeAgents)
	}
}

// The whole-record save cannot publish either: everyone and inbound MCP change
// only through the publish door, which files a request for a non-admin.
func TestAWholeRecordSaveCannotPublishAnAgent(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	savedAdmin := requestIsAdminAgent
	requestIsAdminAgent = func(*http.Request) bool { return false }
	t.Cleanup(func() { requestIsAdminAgent = savedAdmin })

	post := func(rec AgentRecord) AgentRecord {
		t.Helper()
		body, _ := json.Marshal(rec)
		w := httptest.NewRecorder()
		T.handleAgentList(w, asUser(httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body)), user))
		if w.Code != http.StatusOK {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		var out AgentRecord
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	created := post(AgentRecord{Name: "New", OrchestratorPrompt: "p", Everyone: true, MCPExposed: true})
	if got, _ := loadAgent(udb, created.ID); got.Everyone || got.MCPExposed {
		t.Fatalf("a new agent was published by its create: everyone=%v mcp=%v", got.Everyone, got.MCPExposed)
	}
	post(AgentRecord{ID: created.ID, Name: "New", OrchestratorPrompt: "p", Everyone: true, MCPExposed: true})
	if got, _ := loadAgent(udb, created.ID); got.Everyone || got.MCPExposed {
		t.Fatalf("an edit published the agent: everyone=%v mcp=%v", got.Everyone, got.MCPExposed)
	}

	// Already published (by an admin's approval): an unrelated edit keeps it.
	live, _ := loadAgent(udb, created.ID)
	live.Everyone = true
	if _, err := saveAgent(udb, live); err != nil {
		t.Fatal(err)
	}
	post(AgentRecord{ID: created.ID, Name: "Renamed", OrchestratorPrompt: "p"})
	if got, _ := loadAgent(udb, created.ID); !got.Everyone {
		t.Error("an unrelated edit unpublished an approved agent")
	}
}
