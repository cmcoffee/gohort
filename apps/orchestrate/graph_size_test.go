package orchestrate

// A graph is Large when its owner says so: an agent by its own setting, a
// scoped graph (another app's) by the size that app set on it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAGraphIsLargeWhenItsOwnerSaysSo(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u-graph")
	mapper, err := saveAgent(udb, AgentRecord{Name: "Mapper", OrchestratorPrompt: "Map it.", GraphSize: GraphSizeLarge})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := saveAgent(udb, AgentRecord{Name: "Chat", OrchestratorPrompt: "Chat."})
	if err != nil {
		t.Fatal(err)
	}
	if !graphIsLarge(udb, factsNamespace(mapper.ID)) {
		t.Error("an agent set to Large has a standard graph")
	}
	if graphIsLarge(udb, factsNamespace(chat.ID)) {
		t.Error("an agent left at standard has a Large graph")
	}
	if graphIsLarge(UserDB(root, "someone-else"), factsNamespace(mapper.ID)) {
		t.Error("a store the agent is not in sized its graph by it")
	}

	scope := AgentScope{AgentID: "app-test-investigator", ScopeUser: "app:test:sys-1"}
	sdb := UserDB(root, scope.ScopeUser)
	if graphIsLarge(sdb, factsNamespace(scope.AgentID)) {
		t.Error("a scoped graph nobody sized is Large")
	}
	app.SetScopedGraphSize(scope, true)
	if !graphIsLarge(sdb, factsNamespace(scope.AgentID)) {
		t.Error("a scoped graph set to Large is not")
	}
	app.SetScopedGraphSize(scope, false)
	if graphIsLarge(sdb, factsNamespace(scope.AgentID)) {
		t.Error("set back to standard, it stayed Large")
	}
	if graphSizeArg("LARGE") != GraphSizeLarge || graphSizeArg("huge") != "" || graphSizeLabel("") != "standard" {
		t.Error("the agent tools' sizes")
	}
}

// An agent set to a Large graph is told where its map lives; every other agent,
// and one with no graph tools, is not.
func TestALargeGraphAgentIsToldToKeepItsMap(t *testing.T) {
	mapper := AgentRecord{GraphSize: GraphSizeLarge}
	sys := prependAgentContext("persona", mapper, nil, OperatingNotes{})
	if !strings.Contains(sys, "## Your map") || !strings.Contains(sys, "link_entities") || !strings.Contains(sys, "recall_about") ||
		!strings.Contains(sys, "forget_graph") || !strings.Contains(sys, "replace=true") {
		t.Errorf("a mapping agent is not told to keep its map:\n%s", sys)
	}
	if strings.Index(sys, "## Your map") > strings.Index(sys, "persona") {
		t.Error("the map section should sit above the persona")
	}
	if renderMapPromptSection(mapper) != renderMapPromptSection(mapper) {
		t.Error("the section changes between turns, which breaks the prompt cache")
	}
	for _, a := range []AgentRecord{{}, {GraphSize: GraphSizeLarge, DisableExplicit: true}} {
		if s := prependAgentContext("persona", a, nil, OperatingNotes{}); strings.Contains(s, "## Your map") {
			t.Errorf("told to keep a map: %+v", a)
		}
	}
}

// The Memory window's graph list says how big the graph may grow and whether
// it is Large, beside how big it is.
func TestTheGraphListSaysItsSize(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u-graph-list")
	mapper, err := saveAgent(udb, AgentRecord{Name: "Mapper", OrchestratorPrompt: "Map it.", GraphSize: GraphSizeLarge})
	if err != nil {
		t.Fatal(err)
	}
	UpsertGraphEntity(udb, factsNamespace(mapper.ID), "module", "api", nil, nil)
	w := httptest.NewRecorder()
	app.handleAgentGraph(w, httptest.NewRequest(http.MethodGet, "/x", nil), "u-graph-list", mapper.ID)
	var got struct {
		Counts map[string]int `json:"counts"`
		Caps   map[string]int `json:"caps"`
		Large  bool           `json:"large"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Large || got.Counts["entities"] != 1 || got.Caps["entities"] != TuneInt(TunableGraphEntityCapLarge) || got.Caps["edges"] != TuneInt(TunableGraphEdgeCapLarge) {
		t.Errorf("the graph list: %s", w.Body.String())
	}
}
