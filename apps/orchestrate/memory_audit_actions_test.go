package orchestrate

// Remove deletes the entry a finding is about, and only that; Ignore sets a
// finding aside until the text behind it changes; Restore undoes Ignore.

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func findingIn(t *testing.T, found []MemoryFinding, layer string) MemoryFinding {
	t.Helper()
	for _, f := range found {
		if f.Layer == layer {
			return f
		}
	}
	t.Fatalf("no %s finding in %v", layer, kinds(found))
	return MemoryFinding{}
}

func TestIgnoreSetsAFindingAsideUntilItsTextChanges(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	ns := factsNamespace(rec.ID)
	note, _, _ := StoreMemoryFact(udb, ns, "Capture gotchas via store_fact")
	f := findingIn(t, auditOf(t, app, udb, rec, user), "Saved facts")
	if f.ID == "" || f.Remove == "" {
		t.Fatalf("a finding should carry an id and what Remove does: %+v", f)
	}
	if again := findingIn(t, auditOf(t, app, udb, rec, user), "Saved facts"); again.ID != f.ID {
		t.Fatal("a finding's id must be stable across audits, or Ignore cannot hold")
	}

	if err := app.actOnFinding(udb, user, rec, "ignore", f.ID); err != nil {
		t.Fatal(err)
	}
	shown, ignored := splitIgnoredFindings(udb, rec.ID, auditOf(t, app, udb, rec, user))
	if len(shown) != 0 || len(ignored) != 1 || !ignored[0].Ignored {
		t.Fatalf("an ignored finding leaves the list and is listed as ignored: shown=%v ignored=%v", kinds(shown), kinds(ignored))
	}

	// The fact is edited: a different text is a different finding.
	ForgetMemoryFactByID(udb, ns, note.ID)
	StoreMemoryFact(udb, ns, "Capture gotchas and edge cases via store_fact")
	shown, _ = splitIgnoredFindings(udb, rec.ID, auditOf(t, app, udb, rec, user))
	if len(shown) != 1 {
		t.Errorf("a changed entry comes back: %v", kinds(shown))
	}

	// Restore puts an ignored one back.
	if err := app.actOnFinding(udb, user, rec, "ignore", shown[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := app.actOnFinding(udb, user, rec, "restore", shown[0].ID); err != nil {
		t.Fatal(err)
	}
	if shown, _ = splitIgnoredFindings(udb, rec.ID, auditOf(t, app, udb, rec, user)); len(shown) != 1 {
		t.Errorf("restore should bring it back: %v", kinds(shown))
	}
}

func TestRemoveDeletesOnlyWhatTheFindingIsAbout(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	ns := factsNamespace(rec.ID)
	vdb := &DBase{Store: kvlite.MemStore()}
	prevV := VectorDB
	VectorDB = vdb
	t.Cleanup(func() { VectorDB = prevV })

	// One of each: a fact, a line of working notes, a graph attribute, two
	// saved findings.
	StoreMemoryFact(udb, ns, "Capture gotchas via store_fact")
	StoreMemoryFact(udb, ns, "Prefers dark mode")
	SaveOperatingNotes(udb, ns, "working on the release notes\nuse knowledge_search for docs\nnext: review")
	udb.Set(GraphEntityTable, ns+"/thing:brief", GraphEntity{Namespace: ns, ID: "thing:brief", Kind: "thing", Name: "Brief",
		Attrs: map[string]string{"built_from": "use recall_history", "owner": "the user"}})
	src := agentKnowledgePrefix(user, rec.ID)
	for i, text := range []string{"expand_history finds old turns", "expand_history again", "unrelated finding"} {
		vdb.Set(EmbeddedChunks, fmt.Sprintf("c%d", i), EmbeddedChunk{ID: fmt.Sprintf("c%d", i), Source: src, ReportID: fmt.Sprintf("orch-know-%d", i), Text: text})
	}
	InvalidateChunkCache()
	t.Cleanup(InvalidateChunkCache)

	for _, layer := range []string{"Saved facts", "Working notes", "Graph Memory", "Reference Memory"} {
		f := findingIn(t, auditOf(t, app, udb, rec, user), layer)
		if err := app.actOnFinding(udb, user, rec, "remove", f.ID); err != nil {
			t.Fatalf("%s: %v", layer, err)
		}
	}
	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Errorf("every finding's entry should be gone: %v", kinds(found))
	}
	facts := ListMemoryFacts(udb, ns)
	if len(facts) != 1 || facts[0].Note != "Prefers dark mode" {
		t.Errorf("only the offending fact goes: %+v", facts)
	}
	if notes := LoadOperatingNotes(udb, ns).Text; notes != "working on the release notes\nnext: review" {
		t.Errorf("only the offending line goes: %q", notes)
	}
	e, ok := GetGraphEntity(udb, ns, "thing:brief")
	if !ok || e.Attrs["owner"] != "the user" || e.Attrs["built_from"] != "" {
		t.Errorf("only the attribute naming the tool goes: %+v", e)
	}
	var left []string
	for _, c := range ChunksWhere(VectorDB, func(EmbeddedChunk) bool { return true }) {
		left = append(left, c.Text)
	}
	if strings.Join(left, "|") != "unrelated finding" {
		t.Errorf("only the findings naming the tool go: %v", left)
	}
}

// The endpoint takes an action and an id, never a target: the server finds
// the finding again and acts on what it found.
func TestTheMemoryPanelIgnoresAndRestoresThroughTheEndpoint(t *testing.T) {
	app, req, udb := authedApp(t)
	prev := orchestrateBaseDB
	orchestrateBaseDB = app.DB
	t.Cleanup(func() { orchestrateBaseDB = prev })
	rec, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	StoreMemoryFact(udb, factsNamespace(rec.ID), "Capture gotchas via store_fact")
	f := findingIn(t, app.auditAgentMemory(udb, "alice", rec.ID, rec), "Saved facts")

	post := func(body map[string]any) int {
		w := httptest.NewRecorder()
		app.handleAgentOne(w, req("POST", "/api/agents/agent-1/memaudit", body))
		return w.Code
	}
	if code := post(map[string]any{"action": "ignore", "id": f.ID}); code != 200 {
		t.Fatalf("ignore: %d", code)
	}
	if code := post(map[string]any{"action": "remove", "id": "not-a-finding"}); code == 200 {
		t.Error("an id that names no finding must not act")
	}
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("GET", "/api/agents/agent-1/memaudit", nil))
	if !strings.Contains(w.Body.String(), `"ignored":[{`) || !strings.Contains(w.Body.String(), `"count":0`) {
		t.Errorf("the ignored finding should be listed apart: %s", w.Body.String())
	}
}

// Working notes that are switched off never reach a prompt and are not shown
// in the panel, so nothing in them is a finding either.
func TestNotesThatAreOffAreNotAudited(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "pending task: use knowledge_search for docs")
	if len(auditOf(t, app, udb, rec, user)) == 0 {
		t.Fatal("precondition: with notes on, the note is a finding")
	}
	rec.EnableNotes = false
	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Errorf("notes that are off should not be audited: %v", kinds(found))
	}
}
