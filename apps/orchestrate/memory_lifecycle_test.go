package orchestrate

// Memory that knows what time it is: a past event leaves the saved notes for
// reference memory, an idle open item is asked about once and then closed, an
// aged event finding stops being offered as a recall hint, and every move the
// daily pass makes can be put back.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/provenance"
	"github.com/cmcoffee/snugforge/kvlite"
)

// putFact writes a saved note as it would stand after being saved `age` ago.
func putFact(db Database, agentID, id, note string, kind provenance.MemKind, age time.Duration, edit func(*MemoryFact)) MemoryFact {
	at := time.Now().Add(-age)
	f := MemoryFact{Namespace: factsNamespace(agentID), ID: id, Note: note, Created: at, Updated: at,
		MemoryProvenance: MemoryProvenance{AsOf: at, MemKind: kind, Source: MemSourceUserStated}}
	if kind == provenance.MemKindEvent {
		f.EventAt = at
	}
	if edit != nil {
		edit(&f)
	}
	db.Set(MemoryFactsTable, factsNamespace(agentID)+"/"+id, f)
	return f
}

func liveIDs(db Database, agentID string) string {
	var ids []string
	for _, f := range ListMemoryFacts(db, factsNamespace(agentID)) {
		ids = append(ids, f.ID)
	}
	return strings.Join(ids, ",")
}

func TestTheDailyPassMovesPastEventsAndClosesUnansweredItems(t *testing.T) {
	root := depStores(t, "u")
	InvalidateChunkCache()
	t.Cleanup(InvalidateChunkCache)
	udb := UserDB(root, "u")
	day := 24 * time.Hour
	putFact(udb, "ag", "ev-old", "Just got back from a trip to Oregon", provenance.MemKindEvent, 20*day, nil)
	putFact(udb, "ag", "ev-new", "Went to a concert with a friend", provenance.MemKindEvent, 3*day, nil)
	putFact(udb, "ag", "open-asked", "Pending edits on the portrait", provenance.MemKindOpenItem, 30*day,
		func(f *MemoryFact) { f.AskedAt = time.Now().Add(-8 * day) })
	putFact(udb, "ag", "open-idle", "Wants to look into the backup schedule later", provenance.MemKindOpenItem, 30*day, nil)
	putFact(udb, "ag", "pref", "Prefers dark mode", provenance.MemKindFact, 90*day, nil)

	runMemoryLifecycle(context.Background())

	if got := liveIDs(udb, "ag"); got != "ev-new,open-idle,pref" && !sameSet(got, "ev-new,open-idle,pref") {
		t.Fatalf("live notes after the pass = %s", got)
	}
	moved, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "ev-old")
	if moved.Reason != provenance.RetirePast || moved.Successor == "" {
		t.Fatalf("the old trip should be retired as past, pointing at its finding: %+v", moved.MemoryProvenance)
	}
	chunks := ChunksWhere(VectorDB, func(c EmbeddedChunk) bool { return c.ReportID == moved.Successor })
	if len(chunks) == 0 || chunks[0].Kind != chunkKindPastEvent || !strings.Contains(chunks[0].Text, "Oregon") ||
		!strings.Contains(chunks[0].Text, "Past event (around ") {
		t.Fatalf("the trip should be a dated past-event finding: %+v", chunks)
	}
	if !strings.HasPrefix(chunks[0].Source, knowledgeSource("u", "ag", "")) {
		t.Errorf("the finding belongs to the agent's own corpus, got %q", chunks[0].Source)
	}
	closed, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "open-asked")
	if closed.Reason != provenance.RetireNotPursued {
		t.Fatalf("an item asked about and left should close: %+v", closed.MemoryProvenance)
	}

	moves := listMemoryMoves(udb, "ag")
	if len(moves) != 2 {
		t.Fatalf("both moves should be listed for undo, got %+v", moves)
	}
	for _, m := range moves {
		if err := undoMemoryMove(udb, "ag", m.ID); err != nil {
			t.Fatalf("undo %s: %v", m.Kind, err)
		}
	}
	back, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "ev-old")
	if back.Retired() || back.MemKind != provenance.MemKindFact {
		t.Errorf("an undone past event comes back as a standing note: %+v", back.MemoryProvenance)
	}
	if n := len(ChunksWhere(VectorDB, func(c EmbeddedChunk) bool { return c.ReportID == moved.Successor })); n != 0 {
		t.Errorf("undo should remove the finding it became, %d chunk(s) left", n)
	}
	reopened, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "open-asked")
	if reopened.Retired() || !reopened.AskedAt.IsZero() {
		t.Errorf("an undone close comes back live and unasked: %+v", reopened.MemoryProvenance)
	}
	// Put back means put back: the next pass leaves both alone.
	runMemoryLifecycle(context.Background())
	if got := liveIDs(udb, "ag"); !sameSet(got, "ev-old,ev-new,open-asked,open-idle,pref") {
		t.Errorf("the next pass moved something that was put back: live = %s", got)
	}
}

func sameSet(a, b string) bool {
	as, bs := strings.Split(a, ","), strings.Split(b, ",")
	if len(as) != len(bs) {
		return false
	}
	have := map[string]bool{}
	for _, x := range as {
		have[x] = true
	}
	for _, x := range bs {
		if !have[x] {
			return false
		}
	}
	return true
}

func TestAnAgedEventFindingStopsBeingARecallHint(t *testing.T) {
	saved := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = saved; InvalidateChunkCache() })
	now := time.Now()
	src := knowledgeSource("u", "ag", "general")
	old := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	VectorDB.Set(EmbeddedChunks, "c-old", EmbeddedChunk{ID: "c-old", Source: src, ReportID: "orch-know-ag-u-1", Section: "## Trip", Date: old,
		Text: "The user just returned from a trip to Oregon and said it was good"})
	VectorDB.Set(EmbeddedChunks, "c-new", EmbeddedChunk{ID: "c-new", Source: src, ReportID: "orch-know-ag-u-2", Section: "## Visit", Date: recent,
		Text: "The user visited family last weekend"})
	VectorDB.Set(EmbeddedChunks, "c-fact", EmbeddedChunk{ID: "c-fact", Source: src, ReportID: "orch-know-ag-u-3", Section: "## Setup", Date: old,
		Text: "The main model runs on the larger GPU"})
	InvalidateChunkCache()

	if n := dateEventFindings(now); n != 2 {
		t.Fatalf("both event findings should be dated, changed %d", n)
	}
	kinds := map[string]string{}
	var hits []SearchHit
	for _, c := range ChunksWhere(VectorDB, func(EmbeddedChunk) bool { return true }) {
		kinds[c.ID] = c.Kind
		hits = append(hits, SearchHit{ID: c.ID, ReportID: c.ReportID, Section: c.Section, Text: c.Text, Date: c.Date, Kind: c.Kind, Score: 0.9})
	}
	if kinds["c-old"] != chunkKindPastEvent || kinds["c-new"] != chunkKindEvent || kinds["c-fact"] != "" {
		t.Fatalf("kinds = %v", kinds)
	}
	var offered []string
	for _, h := range memoryHints(hits, 0.7) {
		offered = append(offered, h.label)
	}
	all := strings.Join(offered, " ")
	if strings.Contains(all, "Trip") || !strings.Contains(all, "Visit") || !strings.Contains(all, "Setup") {
		t.Errorf("only the aged event should drop out of the hints, got %v", offered)
	}
}

func TestAnIdleOpenItemIsAskedAboutOnce(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := driftTurn(db)
	ct.session = &ChatSession{ID: "s-open-" + UUIDv4()}
	item := putFact(db, "ag", "open-1", "Pending edits on the portrait", provenance.MemKindOpenItem, 20*24*time.Hour, nil)
	putFact(db, "ag", "open-2", "Wants to look into the backup schedule later", provenance.MemKindOpenItem, 16*24*time.Hour, nil)

	note := ct.openItemTurnNote("what's new?")
	if !strings.Contains(note, "fact:"+item.ID) || !strings.Contains(note, "Pending edits on the portrait") {
		t.Fatalf("the oldest idle item should be asked about by id:\n%s", note)
	}
	if f, _ := GetMemoryFactByID(db, factsNamespace("ag"), item.ID); f.AskedAt.IsZero() {
		t.Error("asking should be recorded, so it is asked once")
	}
	if again := ct.openItemTurnNote("what's new?"); again != note {
		t.Error("every round of one turn should carry the same ask")
	}

	// Declined: forget closes it rather than deleting it.
	out, err := ct.forgetToolDef().Handler(context.Background(), map[string]any{"id": "fact:" + item.ID})
	if err != nil || !strings.Contains(out, "Closed the open item") {
		t.Fatalf("forget on an open item should close it: %q %v", out, err)
	}
	if f, _ := GetMemoryFactByID(db, factsNamespace("ag"), item.ID); f.Reason != provenance.RetireNotPursued {
		t.Errorf("closed, not deleted: %+v", f.MemoryProvenance)
	}
	if len(listMemoryMoves(db, "ag")) != 1 {
		t.Error("a close is a move the owner can undo")
	}

	// Next turn: the other item, once; incognito: nothing.
	if next := ct.openItemTurnNote("anything else?"); !strings.Contains(next, "backup schedule") {
		t.Errorf("the next turn may ask about the next item, got %q", next)
	}
	if next := ct.openItemTurnNote("and now?"); next != "" {
		t.Errorf("nothing left to ask about, got %q", next)
	}
	clean := driftTurn(db)
	clean.session = &ChatSession{ID: "s-clean", Incognito: true}
	putFact(db, "ag", "open-3", "Pending reply to the landlord", provenance.MemKindOpenItem, 20*24*time.Hour, nil)
	if n := clean.openItemTurnNote("hi"); n != "" {
		t.Errorf("a clean-room session touches no durable memory: %q", n)
	}
}

func TestTheMemoryPanelListsMovesAndUndoesThem(t *testing.T) {
	app, req, udb := authedApp(t)
	prev := orchestrateBaseDB
	orchestrateBaseDB = app.DB
	t.Cleanup(func() { orchestrateBaseDB = prev })
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	f := putFact(udb, "agent-1", "open-1", "Pending edits on the portrait", provenance.MemKindOpenItem, 30*24*time.Hour, nil)
	closeOpenItem(udb, "agent-1", f, time.Now())

	w := httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodGet, "/api/agents/agent-1/memaudit", nil))
	var got struct {
		Moves []memoryMove `json:"moves"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Moves) != 1 || got.Moves[0].Note != f.Note {
		t.Fatalf("the panel should list the close: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/memaudit", map[string]any{"undo": got.Moves[0].ID}))
	if w.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(liveIDs(udb, "agent-1"), "open-1") {
		t.Error("undo should put the note back")
	}
}

// The worker rewrites a moved event as dated history; a rewrite that lost the
// date gets it back, and one that came back as something else is not trusted.
func TestAPastEventIsRewrittenAsDatedHistory(t *testing.T) {
	prev := orchRef
	t.Cleanup(func() { orchRef = prev })
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	note := "Just got back from a trip to Oregon"
	for _, c := range []struct{ reply, want string }{
		{"Took a trip to Oregon, back around August 24, 2026.", "Took a trip to Oregon, back around August 24, 2026."},
		{"Took a trip to Oregon.", "Took a trip to Oregon. (around 2026-08-24)"},
		{"Sure! Here is the sentence:\nTook a trip to Oregon.", "Past event (around 2026-08-24): " + note},
	} {
		orchRef = &OrchestrateApp{AppCore: AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{Content: c.reply}}}}}
		if got := pastTenseFinding(context.Background(), note, at); got != c.want {
			t.Errorf("reply %q: got %q, want %q", c.reply, got, c.want)
		}
	}
	orchRef = nil
	if got := pastTenseFinding(context.Background(), note, at); got != "Past event (around 2026-08-24): "+note {
		t.Errorf("no worker: %q", got)
	}
}

// Notes saved before kinds existed are classified once by the first pass: an
// old trip moves, an old pending item waits to be asked about (its idle clock
// intact), and a plain preference is left exactly as it was.
func TestTheFirstPassClassifiesNotesSavedBeforeKinds(t *testing.T) {
	root := depStores(t, "u")
	InvalidateChunkCache()
	t.Cleanup(InvalidateChunkCache)
	udb := UserDB(root, "u")
	day := 24 * time.Hour
	putFact(udb, "ag", "old-trip", "Just got back from a trip to Oregon", provenance.MemKindUnknown, 30*day, nil)
	pending := putFact(udb, "ag", "old-pending", "Pending edits on the portrait", provenance.MemKindUnknown, 50*day, nil)
	putFact(udb, "ag", "old-pref", "Prefers dark mode", provenance.MemKindUnknown, 50*day, nil)

	runMemoryLifecycle(context.Background())

	if got := liveIDs(udb, "ag"); !sameSet(got, "old-pending,old-pref") {
		t.Fatalf("live after the first pass = %s", got)
	}
	f, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "old-pending")
	if f.MemKind != provenance.MemKindOpenItem || !f.Updated.Equal(pending.Updated) {
		t.Fatalf("an old pending note is an open item, its dates untouched: %+v (updated %v)", f.MemoryProvenance, f.Updated)
	}
	if p, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "old-pref"); p.MemKind != provenance.MemKindFact {
		t.Errorf("a preference is classified a standing fact: %+v", p.MemoryProvenance)
	}
	ct := driftTurn(udb)
	ct.agent.ID = "ag"
	ct.session = &ChatSession{ID: "s-legacy-" + UUIDv4()}
	if note := ct.openItemTurnNote("hi"); !strings.Contains(note, "idle 50 days") {
		t.Errorf("the old item should be asked about with its real age:\n%s", note)
	}
}
