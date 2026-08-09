// Working notes were the only memory layer with no owner-facing panel. Facts,
// Graph and Reference Memory each had one; notes appeared solely as a colour in
// the cross-layer text search. They are also the layer the model rewrites on
// its own, unreviewed, and the one that renders nearest the top of the prompt
// — so a stale note steered every turn and there was nowhere to go and read it.
// That is how "pending task: get_top_stories with category=all" survived across
// sessions and kept driving the same failure.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func notesPanelApp(t *testing.T) (*OrchestrateApp, AgentRecord, string) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prev })
	const user = "craig@example.com"
	udb := UserDB(root, user)
	rec, err := saveAgent(udb, AgentRecord{
		Name: "Wren", Owner: user, OrchestratorPrompt: "p", EnableNotes: true,
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	return &OrchestrateApp{AppCore: AppCore{DB: root}}, rec, user
}

func getNotes(t *testing.T, app *OrchestrateApp, user, id string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	app.handleAgentNotes(w, httptest.NewRequest(http.MethodGet, "/api/notes", nil), user, id)
	if w.Code != http.StatusOK {
		t.Fatalf("GET notes = %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func postNotes(t *testing.T, app *OrchestrateApp, user, id, text string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"text":` + strconv.Quote(text) + `}`)
	w := httptest.NewRecorder()
	app.handleAgentNotes(w, httptest.NewRequest(http.MethodPost, "/api/notes", body), user, id)
	return w
}

// The whole point: an owner can read the note the agent wrote, and delete it.
func TestTheOwnerCanReadAndClearWhatTheAgentWrote(t *testing.T) {
	app, rec, user := notesPanelApp(t)
	udb := UserDB(app.DB, user)
	parked := "pending task: get_top_stories with category=all"
	SaveOperatingNotes(udb, factsNamespace(rec.ID), parked)

	got := getNotes(t, app, user, rec.ID)
	if got["text"] != parked {
		t.Fatalf("the panel must show the agent's note, got %q", got["text"])
	}
	if got["enabled"] != true {
		t.Errorf("notes are on for this agent: %v", got["enabled"])
	}
	if got["from_seed"] != false {
		t.Errorf("this was agent-written, not the seed: %v", got["from_seed"])
	}

	if w := postNotes(t, app, user, rec.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("clear = %d: %s", w.Code, w.Body.String())
	}
	if after := getNotes(t, app, user, rec.ID); after["text"] != "" {
		t.Errorf("clearing must leave nothing, got %q", after["text"])
	}
}

// Clearing a seed-backed note is meaningless — the seed comes back. The panel
// gets told which it is looking at so it can say so rather than offering an
// action that appears to do nothing.
func TestASeededNoteIsLabelledAsSuch(t *testing.T) {
	app, _, user := notesPanelApp(t)
	udb := UserDB(app.DB, user)
	rec, err := saveAgent(udb, AgentRecord{
		Name: "Seeded", Owner: user, OrchestratorPrompt: "p",
		EnableNotes: true, SeedNotes: "start here",
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	got := getNotes(t, app, user, rec.ID)
	if got["text"] != "start here" || got["from_seed"] != true {
		t.Errorf("a seed-backed note must be shown AND flagged: %+v", got)
	}
	// Once the agent writes its own, it is no longer the seed.
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "mid-draft on section 3")
	if got = getNotes(t, app, user, rec.ID); got["from_seed"] != false {
		t.Errorf("agent-written notes are not the seed: %+v", got)
	}
}

// The cap is what update_notes enforces; the panel must hit the same wall
// rather than writing an oversized block that inflates every prompt.
func TestTheCapIsEnforcedOnTheWriteToo(t *testing.T) {
	app, rec, user := notesPanelApp(t)
	w := postNotes(t, app, user, rec.ID, strings.Repeat("x", OperatingNotesCap+1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("over-cap write = %d, want 400", w.Code)
	}
	if got := getNotes(t, app, user, rec.ID); got["text"] != "" {
		t.Errorf("a rejected write must not land: %q", got["text"])
	}
	if got := getNotes(t, app, user, rec.ID); got["cap"] == nil {
		t.Error("the panel needs the cap to show a counter")
	}
}

// Notes are opt-in. The panel still reads, so an owner can see (and clear) a
// note left behind by an agent whose notes were since turned off — but it is
// told the block reaches no prompt.
func TestDisabledNotesAreStillVisibleAndFlagged(t *testing.T) {
	app, _, user := notesPanelApp(t)
	udb := UserDB(app.DB, user)
	rec, err := saveAgent(udb, AgentRecord{Name: "Off", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "left over")
	got := getNotes(t, app, user, rec.ID)
	if got["enabled"] != false {
		t.Errorf("EnableNotes is off: %v", got["enabled"])
	}
	if got["text"] != "left over" {
		t.Errorf("the leftover must still be readable: %q", got["text"])
	}
}

// Another user's agent is not readable through this handler.
func TestNotesAreNotCrossUserReadable(t *testing.T) {
	app, rec, _ := notesPanelApp(t)
	w := httptest.NewRecorder()
	app.handleAgentNotes(w, httptest.NewRequest(http.MethodGet, "/api/notes", nil), "someone@else.test", rec.ID)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user read = %d, want 404", w.Code)
	}
}

// The modal has to actually fetch the endpoint, or the handler is dead code.
func TestTheModalWiresTheNotesPanel(t *testing.T) {
	js := AgentMemoryModalScript("agents_memory_modal", "'api/'")
	for _, want := range []string{"Working notes", "MEMBASE + 'notes'", "from_seed"} {
		if !strings.Contains(js, want) {
			t.Errorf("the modal must carry %q", want)
		}
	}
}
