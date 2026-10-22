package orchestrate

// Memory modes: Cartographer is lessons like Agent mode plus a map it keeps in
// graph memory, and the two questions asked of a mode differ only on an unset
// one.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestACartographerIsToldToKeepItsMap(t *testing.T) {
	mapper := AgentRecord{MemoryMode: memoryModeCartographer}
	sys := prependAgentContext("persona", mapper, nil, OperatingNotes{})
	for _, want := range []string{"## Your map", "link_entities", "recall_about", "forget_graph", "replace=true"} {
		if !strings.Contains(sys, want) {
			t.Errorf("a cartographer's prompt lacks %q:\n%s", want, sys)
		}
	}
	if strings.Index(sys, "## Your map") > strings.Index(sys, "persona") {
		t.Error("the map section should sit above the persona")
	}
	if memoryModeCopy(memoryModeCartographer).Header != memoryModeCopy("agent").Header {
		t.Error("a cartographer's pinned notes should read as Agent mode's lessons")
	}
	for _, a := range []AgentRecord{{}, {MemoryMode: "agent"}, {MemoryMode: "chatbot"}, {MemoryMode: memoryModeCartographer, DisableExplicit: true}} {
		if s := prependAgentContext("persona", a, nil, OperatingNotes{}); strings.Contains(s, "## Your map") {
			t.Errorf("told to keep a map: %+v", a)
		}
	}
	// Focused like Agent mode: no Cortex reading by default, no session recall.
	if newAgentReadsCortex(mapper) || sessionRecallOn(mapper) {
		t.Error("a cartographer reads like a chat assistant")
	}
	rec := AgentRecord{}
	if !applyWizardMemory(&rec, wizardRequest{Memory: "map"}) || rec.MemoryMode != memoryModeCartographer {
		t.Errorf("the wizard's Cartographer choice: %+v", rec)
	}
}

// The Memory window's graph list says how big the graph may grow beside how
// big it is.
func TestTheGraphListSaysItsCap(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u-graph-list")
	UpsertGraphEntity(udb, factsNamespace("a-1"), "module", "api", nil, nil)
	w := httptest.NewRecorder()
	app.handleAgentGraph(w, httptest.NewRequest(http.MethodGet, "/x", nil), "u-graph-list", "a-1")
	var got struct {
		Counts map[string]int `json:"counts"`
		Caps   map[string]int `json:"caps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Counts["entities"] != 1 || got.Caps["entities"] != TuneInt(TunableGraphEntityCap) || got.Caps["edges"] != TuneInt(TunableGraphEdgeCap) {
		t.Errorf("the graph list: %s", w.Body.String())
	}
}

// The two questions asked of a mode agree on every mode but the unset one,
// where session recall stays off and a new agent from Builder reads its Cortex.
func TestTheMemoryModeQuestions(t *testing.T) {
	for _, c := range []struct {
		mode        string
		people, job bool
	}{
		{memoryModeChatbot, true, false},
		{"Chatbot ", true, false},
		{memoryModeAgent, false, true},
		{memoryModeCartographer, false, true},
		{"", false, false},
	} {
		if memoryForPeople(c.mode) != c.people || memoryForAJob(c.mode) != c.job {
			t.Errorf("%q: for people %v, for a job %v", c.mode, memoryForPeople(c.mode), memoryForAJob(c.mode))
		}
	}
	if sessionRecallOn(AgentRecord{}) || !newAgentReadsCortex(AgentRecord{}) {
		t.Error("an unset mode's two defaults moved")
	}
	if memoryModeCopy(memoryModeCartographer).Header != memoryModeCopy(memoryModeAgent).Header ||
		memoryModeCopy(memoryModeChatbot).Header == memoryModeCopy(memoryModeAgent).Header {
		t.Error("the pinned-notes wording per mode")
	}
}
