package orchestrate

import (
	"encoding/json"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The write side of the parked-call rule. The read-side block (core/notes)
// tells the model how to interpret a note it finds; this tells it what not to
// write in the first place, which is where the "pending task: get_top_stories
// with category=all" note came from.
func TestUpdateNotesRefusesToBeAToolQueue(t *testing.T) {
	desc := (&chatTurn{}).updateNotesToolDef().Tool.Description

	for _, want := range []string{
		"NEVER park a tool call",
		"pending task", // the exact shape observed, so the model recognizes it
		"Record the GOAL",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("update_notes must warn against %q:\n%s", want, desc)
		}
	}
	// store_fact stays the named alternative for durable rules — the warning
	// must not leave the model with nowhere to put anything.
	if !strings.Contains(desc, "store_fact") {
		t.Errorf("the description must still route durable rules to store_fact:\n%s", desc)
	}
}

// Working notes were the only memory layer with no owner-facing panel. Facts,
// Graph and Reference Memory each had one; notes appeared solely as a colour in
// the cross-layer text search. They are also the layer the model rewrites on
// its own, unreviewed, and the one that renders nearest the top of the prompt
// — so a stale note steered every turn and there was nowhere to go and read it.
// That is how "pending task: get_top_stories with category=all" survived across
// sessions and kept driving the same failure.
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

// TestCanEnableIsTrueForAnOwnedAgent — an ordinary agent has an editor its
// owner can open, so the panel is right to explain the setting and point at it.
func TestCanEnableIsTrueForAnOwnedAgent(t *testing.T) {
	app, _, user := notesPanelApp(t)
	udb := UserDB(app.DB, user)
	rec, err := saveAgent(udb, AgentRecord{Name: "Mine", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if got := getNotes(t, app, user, rec.ID); got["can_enable"] != true {
		t.Errorf("an owned agent reports can_enable=%v — the panel would hide a section "+
			"its owner can actually turn on", got["can_enable"])
	}
}

// TestAppAgentsCannotBeEnabledFromThePanel — the reason this field exists.
//
// An app agent's flags come from its code-registered spec and its record is
// hidden from the pickers, so there is no editor to send anyone to. Servitor
// surfaced exactly this: a Working Notes section on its investigator saying
// "turned off — enable them in the agent editor", naming a place the reader
// cannot get to for an agent they cannot see.
func TestAppAgentsCannotBeEnabledFromThePanel(t *testing.T) {
	app, _, user := notesPanelApp(t)
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-notes-probe", OwningApp: "Test", Name: "Probe", Prompt: "p", Hidden: true,
	})
	udb := UserDB(app.DB, user)
	if _, err := saveAgent(udb, AgentRecord{ID: "app-notes-probe", Name: "Probe",
		Owner: seedOwner, OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	got := getNotes(t, app, user, "app-notes-probe")
	if got["can_enable"] != false {
		t.Errorf("an app agent reports can_enable=%v — the panel would tell the reader "+
			"to go to an editor that does not exist for it", got["can_enable"])
	}
	// Reading must still work: a leftover note from before the flag existed is
	// the owner's to see, whatever the panel decides to render.
	if _, ok := got["text"]; !ok {
		t.Error("the app agent's notes are not readable at all")
	}
}

func notesTurn(t *testing.T) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	return &chatTurn{
		udb:   UserDB(root, "u"),
		agent: AgentRecord{ID: "a1", Name: "Wren", Owner: "u", EnableNotes: true},
	}
}

func callNotes(t *testing.T, turn *chatTurn, args map[string]any) (string, error) {
	t.Helper()
	return turn.updateNotesToolDef().Handler(args)
}

func storedNotes(t *testing.T, turn *chatTurn) string {
	t.Helper()
	return LoadOperatingNotes(turn.udb, factsNamespace(turn.agent.ID)).Text
}

// The register the agent names. Writing one part must leave every other part
// exactly as it was — that is the whole reason this exists, because a rewrite
// of the whole document to change one line is how a model drops the rest.
func TestASectionWriteLeavesTheRestAlone(t *testing.T) {
	turn := notesTurn(t)
	if _, err := callNotes(t, turn, map[string]any{"section": "in flight", "text": "Drafting section 3."}); err != nil {
		t.Fatal(err)
	}
	if _, err := callNotes(t, turn, map[string]any{"section": "quirks", "text": "llama slots rotate by LRU."}); err != nil {
		t.Fatal(err)
	}
	msg, err := callNotes(t, turn, map[string]any{"section": "in flight", "text": "Now on section 4."})
	if err != nil {
		t.Fatal(err)
	}
	got := storedNotes(t, turn)
	if !strings.Contains(got, "Now on section 4.") || strings.Contains(got, "section 3") {
		t.Errorf("the section was not replaced: %q", got)
	}
	if !strings.Contains(got, "llama slots rotate by LRU.") {
		t.Errorf("a section write disturbed another section: %q", got)
	}
	// The reply has to say which register was written, or the agent cannot tell
	// a section write from the whole-block rewrite it did not mean to make.
	if !strings.Contains(msg, "in flight") || !strings.Contains(msg, "unchanged") {
		t.Errorf("reply must name the section and say the rest stands: %q", msg)
	}
}

// Empty text with a section removes just that one; empty text alone still
// clears everything, which is what "rewrite the whole block" has always meant.
func TestRemovingASectionVersusClearingTheBlock(t *testing.T) {
	turn := notesTurn(t)
	callNotes(t, turn, map[string]any{"section": "a", "text": "one"})
	callNotes(t, turn, map[string]any{"section": "b", "text": "two"})

	if _, err := callNotes(t, turn, map[string]any{"section": "a", "text": ""}); err != nil {
		t.Fatal(err)
	}
	got := storedNotes(t, turn)
	if strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("removed the wrong thing: %q", got)
	}
	if _, err := callNotes(t, turn, map[string]any{"text": ""}); err != nil {
		t.Fatal(err)
	}
	if got := storedNotes(t, turn); got != "" {
		t.Errorf("a sectionless clear must empty the block, got %q", got)
	}
}

// A sectionless write still replaces everything, sections included. An agent
// correcting a mess needs a way to say so.
func TestAWholeBlockWriteStillReplacesSections(t *testing.T) {
	turn := notesTurn(t)
	callNotes(t, turn, map[string]any{"section": "a", "text": "one"})
	if _, err := callNotes(t, turn, map[string]any{"text": "Starting over."}); err != nil {
		t.Fatal(err)
	}
	if got := storedNotes(t, turn); got != "Starting over." {
		t.Errorf("got %q, want the whole block replaced", got)
	}
}

// Sections COMPETE for the one budget rather than adding to it, and an
// over-cap write is refused with the biggest named — never truncated. A memory
// layer that quietly loses its middle is worse than one that says no: the agent
// cannot tell it was cut, and reads back a sentence that stops.
func TestAnOverCapSectionIsRefusedAndNamesTheCost(t *testing.T) {
	turn := notesTurn(t)
	callNotes(t, turn, map[string]any{"section": "hoard", "text": strings.Repeat("x", OperatingNotesCap-200)})

	_, err := callNotes(t, turn, map[string]any{"section": "more", "text": strings.Repeat("y", 400)})
	if err == nil {
		t.Fatal("a write past the cap must be refused")
	}
	if !strings.Contains(err.Error(), "hoard") {
		t.Errorf("the refusal must name what is costing: %v", err)
	}
	// And nothing was written: a refused write is not a half-write.
	if got := storedNotes(t, turn); strings.Contains(got, "yyy") {
		t.Errorf("the refused section was stored anyway: %q", got)
	}
	if !strings.Contains(storedNotes(t, turn), "xxx") {
		t.Error("the refusal took the existing block with it")
	}
}

// A seeded agent whose store is still empty splices into the SEED, not into a
// blank document — otherwise the first section write silently discards the
// working notes the agent was configured with.
func TestASectionWriteSplicesIntoTheSeed(t *testing.T) {
	turn := notesTurn(t)
	turn.agent.SeedNotes = "## standing\nReport to Craig each morning.\n"
	if _, err := callNotes(t, turn, map[string]any{"section": "in flight", "text": "Drafting."}); err != nil {
		t.Fatal(err)
	}
	got := storedNotes(t, turn)
	if !strings.Contains(got, "Report to Craig each morning.") {
		t.Errorf("the seed was discarded by the first section write: %q", got)
	}
	if !strings.Contains(got, "## in flight") {
		t.Errorf("the new section is missing: %q", got)
	}
}

// The owner's path and the agent's path go through one implementation. Two of
// them is how they come to disagree about where a note lives, and the symptom
// is a panel showing something other than what the model reads.
func TestTheOwnerEndpointTakesASectionToo(t *testing.T) {
	raw, err := os.ReadFile("notes.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "Section string `json:\"section\"`") {
		t.Error("POST …/notes must accept an optional section")
	}
	if strings.Count(src, "notes.ApplyNoteSection(") != 2 {
		t.Error("both the tool and the endpoint must splice through ApplyNoteSection — a second implementation drifts")
	}
	// The over-cap refusal is worded once, in core/notes, so the person
	// trimming the block and the model trimming it read the same sentence.
	if strings.Count(src, "notes.OverCapAdvice(") != 2 {
		t.Error("both surfaces must quote the same over-cap advice")
	}
	// And the panel is measured by the same parser rather than a browser-side
	// count that disagrees the first time a heading sits inside a code fence.
	if !strings.Contains(src, "notes.SectionSizes(") {
		t.Error("GET …/notes must serve the section sizes the panel shows")
	}
}

// The block tells the agent the section rule exists. A parameter nothing in the
// prompt mentions is one the model finds only by reading its own tool schema
// closely, which is not how it decides what to do.
func TestTheNotesBlockNamesTheSectionRule(t *testing.T) {
	block := RenderOperatingNotesBlock(OperatingNotes{Text: "## in flight\nDrafting.\n"})
	if !strings.Contains(block, "update_notes(section:") {
		t.Error("the working-notes block must say how to update one part")
	}
	if !strings.Contains(block, "compete") {
		t.Error("and that sections share the one budget rather than adding to it")
	}
	// Still nothing when there are no notes: an empty block must render empty,
	// or every agent with none pays for the instructions to a feature it is
	// not using.
	if RenderOperatingNotesBlock(OperatingNotes{}) != "" {
		t.Error("no notes, no block")
	}
}
