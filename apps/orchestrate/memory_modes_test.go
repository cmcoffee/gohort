package orchestrate

// Memory modes: the two questions asked of a mode differ only on an unset one,
// and the graph list says how big a graph may grow.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
		{"", false, false},
	} {
		if memoryForPeople(c.mode) != c.people || memoryForAJob(c.mode) != c.job {
			t.Errorf("%q: for people %v, for a job %v", c.mode, memoryForPeople(c.mode), memoryForAJob(c.mode))
		}
	}
	if sessionRecallOn(AgentRecord{}) || !newAgentReadsCortex(AgentRecord{}) {
		t.Error("an unset mode's two defaults moved")
	}
	if memoryModeCopy(memoryModeChatbot).Header == memoryModeCopy(memoryModeAgent).Header {
		t.Error("the pinned-notes wording per mode")
	}
}
