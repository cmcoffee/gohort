package orchestrate

// The collection check and held pages (collection_vet.go,
// collection_held.go): what a machine picks is checked before it goes in, a
// page that contradicts the collection waits for its owner, and the owner
// settles it by hand or has it argued out first.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// vetReply is the check's reply, as the model writes it.
func vetReply(answer string, excerpt int, against string) string {
	return `{"fit":"it fits","excerpt":` + strconv.Itoa(excerpt) + `,"against":"` + against + `","why":"because","answer":"` + answer + `"}`
}

// vetFixture is alice's collection "col-v" with one document, the model
// scripted with turns, retrieval answering with that document, and pages
// fetched from pages instead of the network.
type vetFix struct {
	T     *OrchestrateApp
	llm   *FakeLLM
	store func(string) Database
	c     Collection
}

func vetFixture(t *testing.T, pages map[string]string, turns ...FakeTurn) *vetFix {
	t.Helper()
	store := stewardStores(t)
	llm := &FakeLLM{Turns: turns}
	f := &vetFix{T: &OrchestrateApp{AppCore: AppCore{DB: CollectionsDB(), LLM: llm}}, llm: llm, store: store}
	f.c = Collection{ID: "col-v", Name: "Tool reference", Owner: "alice", Description: "Reference for the example tool.", CuratorAgent: "app-knowledge-curator"}
	saveCollection(store("alice"), f.c)
	IngestDocument(context.Background(), VectorDB, collectionSource(f.c.ID), "doc-old", "Configuration reference", "## Configuration reference\n\n"+strings.Repeat("The audit log is off by default. ", 20))
	prevEx, prevFetch := collectionExcerpts, ingestFetch
	collectionExcerpts = func(context.Context, Collection, string, string) []vetExcerpt {
		return []vetExcerpt{{DocID: "doc-old", Title: "Configuration reference", Text: "The audit log is off by default."}}
	}
	ingestFetch = func(_ context.Context, u string) (string, string, []byte, string, error) {
		text, ok := pages[u]
		if !ok {
			return "", "", nil, "", errors.New("no such page")
		}
		return "Page " + u, text, []byte(text), "text/plain", nil
	}
	t.Cleanup(func() { collectionExcerpts, ingestFetch = prevEx, prevFetch })
	return f
}

func (f *vetFix) call(method, sid string) *jsonapi.Call {
	r := httptest.NewRequest(method, "/api/collections/col-v/held?sid="+sid, nil)
	r.SetPathValue("id", "col-v")
	return &jsonapi.Call{W: httptest.NewRecorder(), R: r, User: "alice", UDB: f.store("alice")}
}

func (f *vetFix) docs() map[string]int {
	out := map[string]int{}
	for _, ch := range ChunksWhere(VectorDB, func(x EmbeddedChunk) bool { return strings.HasPrefix(x.Source, collectionSource(f.c.ID)) }) {
		out[ch.ReportID]++
	}
	return out
}

// The check's answers: what goes in, what it is against, and that one it
// could not read, or no model at all, adds nothing.
func TestTheCheckSaysWhatGoesInAndWhatItContradicts(t *testing.T) {
	f := vetFixture(t, nil,
		FakeTurn{Content: vetReply("conflicts", 1, "page: on; excerpt: off")},
		FakeTurn{Content: vetReply("supersedes", 0, "")},
		FakeTurn{Content: vetReply("drop", 0, "")},
		FakeTurn{Content: "not json"}, FakeTurn{Content: "still not"})
	cand := vetCandidate{URL: "https://example.com/a", Title: "A", Text: "The audit log is on by default."}
	res := vetPage(context.Background(), f.llm, f.c, "alice", cand)
	if res.Answer != vetConflicts || res.AgainstDoc != "doc-old" || res.AgainstTitle != "Configuration reference" || res.addsIt() {
		t.Errorf("a conflict should name what it is against and not go in: %+v", res)
	}
	sent := f.llm.Config(0).SystemPrompt + f.llm.Prompt(0)
	for _, want := range []string{"[1] Configuration reference: The audit log is off by default.", "UNTRUSTED", "Reference for the example tool.", `"answer"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("the check was not shown %q", want)
		}
	}
	if res := vetPage(context.Background(), f.llm, f.c, "alice", cand); res.Answer != vetAdds {
		t.Errorf("a replacement naming nothing to replace is an add, not a removal proposal: %+v", res)
	}
	if res := vetPage(context.Background(), f.llm, f.c, "alice", cand); res.Answer != vetDrop || res.addsIt() {
		t.Errorf("drop: %+v", res)
	}
	if res := vetPage(context.Background(), f.llm, f.c, "alice", cand); res.addsIt() || res.Err == nil {
		t.Errorf("an unreadable reply must add nothing: %+v", res)
	}
	if res := vetPage(context.Background(), nil, f.c, "alice", cand); res.addsIt() || res.Err == nil {
		t.Errorf("no model must add nothing: %+v", res)
	}
	// With nothing in the collection on it, there is nothing to contradict.
	nothing := &FakeLLM{Turns: []FakeTurn{{Content: vetReply("conflicts", 0, "made up")}}}
	if res := vetAgainst(context.Background(), nothing, f.c, cand, nil); res.Answer != vetAdds {
		t.Errorf("a conflict with nothing to contradict should be an add: %+v", res)
	}
	if got := heldText(vetResult{Against: "on vs off"}); strings.Contains(got, `""`) || !strings.Contains(got, "what the collection holds") {
		t.Errorf("a conflict naming no document reads %q", got)
	}
}

// The check's cases go through its own prompt and reading.
func TestTheVetCasesRunThroughTheCheck(t *testing.T) {
	caseAnswers(t, collectionVetKey, []string{vetAdds, vetSupersedes, vetConflicts, vetDrop},
		caseReply{"contradicts", vetConflicts, []string{vetReply("conflicts", 1, "on vs off")}},
		caseReply{"newer-version", vetSupersedes, []string{vetReply("supersedes", 1, "v4 on, v3 off")}})
}

// The agent in charge adds through its scoped tool, and what it adds is
// checked: a contradicting page is held, not added, kept from the curator's
// queue, and refused if tried again; one that fits goes in. Builder's adds,
// made at the user's word, are not checked.
func TestAnAgentsAddIsCheckedAndAConflictHeld(t *testing.T) {
	const bad, good = "https://example.com/bad", "https://example.com/good"
	f := vetFixture(t, map[string]string{bad: "The audit log is on by default.", good: "Backups go to a dated archive."},
		FakeTurn{Content: vetReply("conflicts", 1, "page: on; reference: off")},
		FakeTurn{Content: vetReply("adds", 0, "")})
	tool := f.T.stewardToolWithQueue([]Collection{f.c}).(*GroupedTool)
	add, _ := tool.Action("add_url")
	sess := &ToolSession{DB: f.store("alice"), Username: "alice"}

	out, err := add.Handler(map[string]any{"url": bad}, sess)
	if err != nil || !strings.Contains(out, "Not added") || !strings.Contains(out, "held for the owner") {
		t.Fatalf("a conflicting page: %q %v", out, err)
	}
	if saved, _ := loadCollection(f.store("alice"), "alice", "col-v"); len(saved.IngestedURLs) != 0 {
		t.Errorf("a held page went in: %v", saved.IngestedURLs)
	}
	held := f.T.listHeld(f.c)
	if len(held) != 1 || held[0].Source != bad || held[0].AgainstDoc != "doc-old" || !strings.Contains(held[0].Text, "Configuration reference") {
		t.Fatalf("held: %+v", held)
	}
	if n := len(f.T.listSuggestions(f.c)); n != 0 {
		t.Errorf("a held page is in the curator's queue: %d", n)
	}
	resolve, _ := tool.Action("resolve_suggestion")
	if _, err := resolve.Handler(map[string]any{"suggestion_id": held[0].ID, "outcome": "applied"}, sess); err == nil {
		t.Error("the curator closed a held page")
	}
	calls := f.llm.Calls()
	if out, _ := add.Handler(map[string]any{"url": bad}, sess); !strings.Contains(out, "already held") || f.llm.Calls() != calls {
		t.Errorf("a held page tried again should be refused without asking again: %q", out)
	}

	if out, err := add.Handler(map[string]any{"url": good}, sess); err != nil || !strings.Contains(out, "Ingested") {
		t.Fatalf("a page that fits: %q %v", out, err)
	}

	calls = f.llm.Calls()
	builder := collectionsListTool().(*GroupedTool)
	badd, _ := builder.Action("add_url")
	if out, err := badd.Handler(map[string]any{"id": "col-v", "url": bad}, sess); err != nil || !strings.Contains(out, "Ingested") || f.llm.Calls() != calls {
		t.Errorf("Builder's add at the user's word should go in unchecked: %q %v", out, err)
	}
}

// Text the agent in charge writes is checked too, and turned away when it
// contradicts, since there is no page to hold.
func TestTheCuratorsTextIsChecked(t *testing.T) {
	f := vetFixture(t, nil, FakeTurn{Content: vetReply("conflicts", 1, "on vs off")})
	tool := f.T.stewardToolWithQueue([]Collection{f.c}).(*GroupedTool)
	add, _ := tool.Action("add_text")
	out, err := add.Handler(map[string]any{"title": "Note", "text": "The audit log is on by default."}, &ToolSession{DB: f.store("alice"), Username: "alice"})
	if err != nil || !strings.Contains(out, "Not added") {
		t.Fatalf("%q %v", out, err)
	}
	if len(f.T.listHeld(f.c)) != 0 || f.docs()["doc-old"] == 0 || len(f.docs()) != 1 {
		t.Errorf("text that contradicts should be neither held nor added: held %d, docs %v", len(f.T.listHeld(f.c)), f.docs())
	}
}

// Auto-fill on a curated collection checks each page: one that contradicts
// is held and counted, one that could not be checked is not added. On a
// hand-curated collection without the box ticked nothing is asked.
func TestAutofillChecksWhatItAdds(t *testing.T) {
	const bad, unread = "https://example.com/bad", "https://example.com/unread"
	f := vetFixture(t, map[string]string{bad: "The audit log is on by default.", unread: "Something."},
		FakeTurn{Content: vetReply("conflicts", 1, "on vs off")}, FakeTurn{Content: "x"}, FakeTurn{Content: "x"})
	prev := collectionWebSearch
	collectionWebSearch = func(string) string {
		return "1. Bad\n   " + bad + "\n   x\n\n2. Unread\n   " + unread + "\n   x\n"
	}
	t.Cleanup(func() { collectionWebSearch = prev })

	pass := &autofillPass{ctx: context.Background(), udb: f.store("alice"), c: &f.c, maxDocs: 5}
	pass.search(f.T, []string{"q"})
	if pass.held != 1 || pass.added != 0 || pass.failed != 1 {
		t.Fatalf("held %d added %d failed %d: %+v", pass.held, pass.added, pass.failed, pass.results)
	}
	if len(f.T.listHeld(f.c)) != 1 {
		t.Error("the contradicting page was not held")
	}

	hand := Collection{ID: "col-h", Name: "Hand", Owner: "alice"}
	saveCollection(f.store("alice"), hand)
	calls := f.llm.Calls()
	pass = &autofillPass{ctx: context.Background(), udb: f.store("alice"), c: &hand, maxDocs: 5}
	pass.search(f.T, []string{"q"})
	if f.llm.Calls() != calls || pass.added != 2 {
		t.Errorf("an unticked hand-curated collection was checked: calls %d, added %d", f.llm.Calls()-calls, pass.added)
	}
}

// The owner settles a held page by hand: keep the document, add it anyway,
// or add it in place of the document it contradicts.
func TestTheOwnerSettlesAHeldPageByHand(t *testing.T) {
	const page = "https://example.com/new"
	f := vetFixture(t, map[string]string{page: strings.Repeat("The audit log is on by default. ", 20)})
	hold := func() collectionSuggestion {
		if err := f.T.holdPage(f.c, vetCandidate{URL: page, Title: "New"}, vetResult{Answer: vetConflicts, Against: "on vs off", AgainstDoc: "doc-old", AgainstTitle: "Configuration reference"}); err != nil {
			t.Fatal(err)
		}
		h := f.T.listHeld(f.c)
		return h[len(h)-1]
	}
	s := hold()
	got, err := f.T.collectionHeld(f.call(http.MethodGet, ""))
	if err != nil {
		t.Fatal(err)
	}
	rows := got.(map[string]any)["held"].([]map[string]any)
	if len(rows) != 1 || rows[0]["has_doc"] != true || rows[0]["source_link"] != page {
		t.Fatalf("rows: %+v", rows)
	}

	if _, err := f.T.collectionHeldKeep(f.call(http.MethodPost, s.ID), struct{}{}); err != nil || len(f.T.listHeld(f.c)) != 0 {
		t.Fatalf("keep: %v", err)
	}
	if f.docs()["doc-old"] == 0 {
		t.Error("keeping the document removed it")
	}

	s = hold()
	if _, err := f.T.collectionHeldReplace(f.call(http.MethodPost, s.ID), struct{}{}); err != nil {
		t.Fatal(err)
	}
	docs := f.docs()
	if docs["doc-old"] != 0 || len(docs) != 1 || len(f.T.listHeld(f.c)) != 0 {
		t.Errorf("replace should add the page and remove the old one: %v", docs)
	}

	// An ordinary suggestion is not a held page: these refuse it.
	plain, _ := f.T.addSuggestion(f.c, collectionSuggestion{Text: "an update", Source: page})
	if _, err := f.T.collectionHeldAdd(f.call(http.MethodPost, plain.ID), struct{}{}); err == nil {
		t.Error("a held-page action took an ordinary suggestion")
	}
}

// Settle it argues a held page out (two voices, their citations checked,
// then a ruling) and records the ruling on the row, with progress while it
// runs; nothing in the collection changes.
func TestSettleItRulesAndChangesNothing(t *testing.T) {
	const page = "https://example.com/new"
	footer := strings.Repeat("Unrelated footer about something else entirely. ", 80)
	f := vetFixture(t, map[string]string{page: "Intro.\n\nAs of version 4 the audit log is on by default.\n\n" + footer},
		FakeTurn{Content: `{"page_case":"v4 docs say on","document_case":"v3 docs say off","why":"The vendor's version 4 page [1] decides it.","answer":"page"}`})
	curations.mu.Lock()
	prevRuns := curations.runs
	curations.runs = map[string]*curateRun{}
	curations.mu.Unlock()
	t.Cleanup(func() {
		curations.mu.Lock()
		running := curations.runs
		curations.runs = prevRuns
		curations.mu.Unlock()
		for _, run := range running {
			run.cancel()
			<-run.done
		}
	})
	if err := f.T.holdPage(f.c, vetCandidate{URL: page, Title: "New"}, vetResult{Answer: vetConflicts, Against: "on vs off", AgainstDoc: "doc-old", AgainstTitle: "Configuration reference"}); err != nil {
		t.Fatal(err)
	}
	s := f.T.listHeld(f.c)[0]

	var input string
	var def PipelineDef
	prev := settleRunPipeline
	settleRunPipeline = func(_ *OrchestrateApp, ctx context.Context, _ string, d PipelineDef, in string, h PipelineHooks) (string, error) {
		def, input = d, in
		if _, err := h.Dispatch(ctx, settleVoices[0], "argue"); !errors.Is(err, ErrNoSuchAgent) {
			t.Errorf("a voice should answer as a role, not an agent: %v", err)
		}
		h.Status("panel argue: round 1")
		return h.Dispatch(ctx, settleJudge, "## The dispute\n"+in)
	}
	t.Cleanup(func() { settleRunPipeline = prev })

	before := f.docs()
	if _, err := f.T.collectionHeldSettleStart(f.call(http.MethodPost, s.ID), struct{}{}); err != nil {
		t.Fatal(err)
	}
	var end map[string]any
	for i := 0; i < 400 && end == nil; i++ {
		out, _ := f.T.collectionHeldSettleProgress(f.call(http.MethodGet, s.ID))
		if m := out.(map[string]any); m["outcome"] != nil {
			end = m
		}
		if end == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if end == nil || !strings.Contains(end["outcome"].(string), "The page is right") {
		t.Fatalf("outcome: %v", end)
	}
	if !strings.Contains(input, "As of version 4") || strings.Contains(input, "Unrelated footer") || !strings.Contains(input, "audit log is off") {
		t.Errorf("each side should get the part of its text that bears on the dispute:\n%s", input)
	}
	if err := def.Validate(); err != nil || def.Stages[0].Kind != StagePanel || def.Stages[1].Kind != StageVerify || def.Stages[2].Agent != settleJudge {
		t.Errorf("the settle pipeline: %v %+v", err, def.Stages)
	}
	got, _ := f.T.findSuggestion(f.c, s.ID)
	if got.Ruling != settlePage || !strings.Contains(rulingLine(got), "replace \"Configuration reference\"") {
		t.Errorf("the ruling should be on the row: %+v", got)
	}
	if after := f.docs(); len(after) != len(before) || after["doc-old"] != before["doc-old"] {
		t.Errorf("settling changed the collection: %v -> %v", before, after)
	}
	items, _ := f.T.collectionHeldSettleItems(f.call(http.MethodGet, s.ID))
	if h := items.([]map[string]any)[0]["history"].(string); !strings.Contains(h, "The page is right") {
		t.Errorf("the row should say how it was ruled: %q", h)
	}
}

// Without web search the voices argue from the two texts alone.
func TestSettleLooksThingsUpOnlyWhenItCan(t *testing.T) {
	if d := settleDef(false); d.Stages[0].Research != 0 || len(d.Stages[0].Tools) != 0 {
		t.Errorf("no search: %+v", d.Stages[0])
	}
	if d := settleDef(true); d.Stages[0].Research == 0 || len(d.Stages[0].Tools) != 2 {
		t.Errorf("search: %+v", d.Stages[0])
	}
}
