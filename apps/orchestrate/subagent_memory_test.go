package orchestrate

// A sub-agent runs without memory unless its author says it keeps its own
// (KeepsMemory) or it inherits its parent's tools. The memory it keeps is
// filed under its own id, so it never reaches the parent's prompt.

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestASubAgentKeepsMemoryOnlyWhenAsked(t *testing.T) {
	app, udb := familyFixture(t)
	hasRemember := func(a AgentRecord) bool {
		turn := &chatTurn{app: app, agent: a, user: "alice", udb: udb}
		for _, td := range turn.corpusToolDefs() {
			if td.Tool.Name == "remember" {
				return true
			}
		}
		return false
	}

	plain, _ := loadAgent(udb, "a-critic")
	if !plain.DisableExplicit || !plain.DisableInferred || hasRemember(plain) {
		t.Errorf("a plain sub-agent should run without memory: %+v", plain)
	}

	// Saved with memory disabled, as a record written before the setting may
	// be: the setting wins, as the posture always has over stored flags.
	plain.KeepsMemory, plain.DisableExplicit, plain.DisableInferred = true, true, true
	if _, err := saveAgent(udb, plain); err != nil {
		t.Fatal(err)
	}
	keeper, _ := loadAgent(udb, "a-critic")
	if keeper.DisableExplicit || keeper.DisableInferred || !hasRemember(keeper) {
		t.Errorf("a sub-agent that keeps its own memory has none: %+v", keeper)
	}
	// Its own: filed under its id, not the parent's.
	if factsNamespace(keeper.ID) == factsNamespace(keeper.OwnedBy) {
		t.Error("a sub-agent's facts share its parent's namespace")
	}

	// Only a sub-agent's posture reads it: on a top-level agent the memory
	// settings are its own, and this changes none of them.
	top := AgentRecord{ID: "a-top", DisableExplicit: true, KeepsMemory: true}
	if got := enforceSubAgentPosture(top); !got.DisableExplicit || got.DisableInferred {
		t.Errorf("a top-level agent's memory settings were changed: %+v", got)
	}
}

// The setting is on the sub-agent's editor, which has no other memory
// settings, and is saved by the editor's PATCH and by the agent tools.
func TestKeepsMemoryCanBeSet(t *testing.T) {
	app, udb := familyFixture(t)
	render := func(id string) string {
		w := httptest.NewRecorder()
		app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", "/agent/"+id, nil), "alice"), "alice", udb, id)
		return w.Body.String()
	}
	if page := render("a-critic"); !strings.Contains(page, `"field":"keeps_memory"`) {
		t.Error("the sub-agent editor has no Keeps its own memory switch")
	}
	if page := render("a-writer"); strings.Contains(page, `"field":"keeps_memory"`) || !strings.Contains(page, `"field":"disable_explicit"`) {
		t.Error("a top-level agent should keep its own memory settings and not the sub-agent switch")
	}
	if !patchAgentFields["keeps_memory"] {
		t.Error("the editor's save would drop keeps_memory")
	}
	props := agentMutationParams(true)
	if _, ok := props["keeps_memory"]; !ok {
		t.Error("the agent tools cannot set keeps_memory")
	}
	if rec := agentRecordFromArgs(map[string]any{"keeps_memory": true}); !rec.KeepsMemory {
		t.Error("creating an agent with keeps_memory did not set it")
	}
	rec := AgentRecord{KeepsMemory: true}
	if mergeAgentArgs(&rec, map[string]any{"name": "x"}); !rec.KeepsMemory {
		t.Error("an update that does not name keeps_memory cleared it")
	}
	if mergeAgentArgs(&rec, map[string]any{"keeps_memory": false}); rec.KeepsMemory {
		t.Error("an update could not turn keeps_memory off")
	}
}
