package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// These tests pin the unified remember/recall/forget surface to the legacy
// tools' behaviors — the drift the memory audit flagged as a blocker for a
// fair legacy-vs-unified A/B.

func driftTurn(db Database) *chatTurn {
	return &chatTurn{user: "u", udb: db, agent: AgentRecord{ID: "ag"}}
}

// TestUnifiedRecallPinned_NudgeAndRetiredHole: the [pinned] layer must carry
// legacy search_facts' two affordances — the recall_about nudge when a note
// names a known graph entity, and the tombstone explanation when a query
// matches only RETIRED notes.
func TestUnifiedRecallPinned_NudgeAndRetiredHole(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := driftTurn(db)
	ns := factsNamespace("ag")
	if _, isNew, _ := StoreMemoryFact(db, ns, "Robin works at Acme on the deploy pipeline"); !isNew {
		t.Fatal("seed fact failed")
	}
	live := ListMemoryFacts(db, ns)
	if len(live) != 1 {
		t.Fatalf("expected 1 seeded fact, got %d", len(live))
	}
	UpsertGraphEntity(db, ns, "person", "Robin", nil, nil)

	out, err := ct.recallSearch("deploy pipeline", map[string]any{"layer": "pinned"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(out, `recall_about "Robin"`) {
		t.Fatalf("pinned hit naming a graph entity must carry the recall_about nudge:\n%s", out)
	}
	if !strings.Contains(out, "id: fact:"+live[0].ID) {
		t.Fatalf("pinned hit must keep its fact id:\n%s", out)
	}

	// Retired-only match → the hole explanation, not a bare miss. No exported
	// tombstone writer exists, so seed the row directly (key format is the
	// stable namespace/id join) and self-check the seed took.
	db.Set(MemoryFactsTable, ns+"/old", MemoryFact{
		Namespace: ns, ID: "old", Note: "user lived in Santa Cruz", Created: time.Now().Add(-time.Hour),
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: time.Now()},
	})
	if len(ListRetiredFacts(db, ns)) != 1 {
		t.Fatal("tombstone seed did not land where ListRetiredFacts reads")
	}
	out, err = ct.recallSearch("Santa Cruz", map[string]any{"layer": "pinned"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(out, "previously stored (now retired)") || !strings.Contains(out, "Santa Cruz") {
		t.Fatalf("retired-only match must surface the tombstone explanation:\n%s", out)
	}
}

// TestUnifiedRecallKBudget: k is a TOTAL budget split across searched layers
// (legacy k semantics), not a per-layer multiplier.
func TestUnifiedRecallKBudget(t *testing.T) {
	if got := recallPerLayerBudget(map[string]any{}, 4); got != unifiedRecallPerLayer {
		t.Fatalf("no k must keep the tuned default, got %d", got)
	}
	if got := recallPerLayerBudget(map[string]any{"k": float64(8)}, 4); got != 2 {
		t.Fatalf("k=8 over 4 layers must be 2 per layer, got %d", got)
	}
	if got := recallPerLayerBudget(map[string]any{"k": float64(5)}, 4); got != 2 {
		t.Fatalf("k=5 over 4 layers must ceil to 2 per layer, got %d", got)
	}
	if got := recallPerLayerBudget(map[string]any{"k": float64(8)}, 1); got != 8 {
		t.Fatalf("k=8 over 1 layer must be 8, got %d", got)
	}
}

// TestUnifiedRememberInferredOff_NoPinSteering: with Reference memory off,
// remember(pin=false) must refuse WITHOUT funneling the content into the
// always-in-prompt block, and must say plainly that nothing was saved.
func TestUnifiedRememberInferredOff_NoPinSteering(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ct := driftTurn(db)
	ct.agent.DisableInferred = true

	_, err := ct.rememberToolDef().Handler(context.Background(), map[string]any{"content": "long reference finding about the Acme API"})
	if err == nil {
		t.Fatal("remember(pin=false) with Inferred off must refuse")
	}
	if !strings.Contains(err.Error(), "NOT saved") {
		t.Fatalf("refusal must state nothing was saved: %v", err)
	}
	if got := ListMemoryFacts(db, factsNamespace("ag")); len(got) != 0 {
		t.Fatalf("nothing may land in the pinned store: %+v", got)
	}
}

// TestUnifiedForget_ChunkIDFallback: a mem: ref that isn't a finding's
// ReportID but IS one of the agent's own derived chunk row ids deletes that
// single chunk (legacy memory(forget) mem_id granularity); out-of-scope or
// curated chunks stay refused.
func TestUnifiedForget_ChunkIDFallback(t *testing.T) {
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })

	ct := driftTurn(&DBase{Store: kvlite.MemStore()})
	own := knowledgeSource("u", "ag", "general")
	VectorDB.Set(EmbeddedChunks, "c1", EmbeddedChunk{
		ID: "c1", Source: own, ReportID: "orch-know-1", Text: "own derived"})
	VectorDB.Set(EmbeddedChunks, "c2", EmbeddedChunk{
		ID: "c2", Source: knowledgeSource("someone-else", "ag2", ""), ReportID: "orch-know-2", Text: "not ours"})

	msg, err := ct.forgetFindingByReportID("c1")
	if err != nil || !strings.Contains(msg, "Forgot") {
		t.Fatalf("own derived chunk id must delete: msg=%q err=%v", msg, err)
	}
	var gone EmbeddedChunk
	if VectorDB.Get(EmbeddedChunks, "c1", &gone) {
		t.Fatal("chunk c1 must be deleted")
	}

	msg, err = ct.forgetFindingByReportID("c2")
	if err != nil || strings.Contains(msg, "Forgot") {
		t.Fatalf("someone else's chunk must be refused: msg=%q err=%v", msg, err)
	}
	var kept EmbeddedChunk
	if !VectorDB.Get(EmbeddedChunks, "c2", &kept) {
		t.Fatal("out-of-scope chunk must survive")
	}
}

// TestUnifiedForget_RequiresIDOrQuery: the forget verb accepts id OR query;
// neither is an error, and query-mode with Reference memory off refuses.
func TestUnifiedForget_RequiresIDOrQuery(t *testing.T) {
	ct := driftTurn(&DBase{Store: kvlite.MemStore()})
	if _, err := ct.forgetToolDef().Handler(context.Background(), map[string]any{}); err == nil {
		t.Fatal("forget with neither id nor query must error")
	}
	ct.agent.DisableInferred = true
	if _, err := ct.forgetToolDef().Handler(context.Background(), map[string]any{"query": "acme api"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("query-mode with Inferred off must refuse: %v", err)
	}
}

// TestRewriteMemoryToolNamesUnified: under the collapsed surface each legacy
// token is swapped for its unified equivalent, store_fact keeps its pin=true
// qualifier, and the distinct skill_* tools are never touched.
func TestRewriteMemoryToolNamesUnified(t *testing.T) {

	cases := []struct{ in, want string }{
		{"call knowledge_search first", "call recall first"},
		{"pull the full doc via fetch_knowledge_doc", "pull the full doc via recall"},
		{"search_facts / recall_history / expand_history / list_facts", "recall / recall / recall / recall"},
		{"capture gotchas via store_fact", "capture gotchas via remember (pin=true)"},
		{"stash a finding via memory_save", "stash a finding via remember"},
		{"drop it with forget_fact or memory_forget", "drop it with forget or forget"},
	}
	for _, c := range cases {
		if got := rewriteMemoryToolNames(c.in); got != c.want {
			t.Errorf("rewrite\n in: %q\ngot: %q\nwant: %q", c.in, got, c.want)
		}
	}

	// Collision safety: skill_knowledge_search and skill_knowledge_fetch_doc are
	// DIFFERENT tools that exist in both modes. The word-boundary regex must not
	// touch them (the underscore before "knowledge" blocks the boundary).
	skills := "read_skill / skill_knowledge_search / skill_knowledge_fetch_doc"
	if got := rewriteMemoryToolNames(skills); got != skills {
		t.Fatalf("skill_* tools must be untouched\n in: %q\nout: %q", skills, got)
	}
	if strings.Contains(rewriteMemoryToolNames(skills), "skill_recall") {
		t.Fatal("rewrite corrupted a skill_ tool name")
	}
}

// TestRewriteMemoryToolNamesIdempotent: the two call sites (prependAgentContext
// and appendAgentCapabilityBlocks) may both run over the same string, so a
// second pass must be a no-op — the replacements carry no legacy token.
func TestRewriteMemoryToolNamesIdempotent(t *testing.T) {
	in := "call knowledge_search then store_fact the result"
	once := rewriteMemoryToolNames(in)
	twice := rewriteMemoryToolNames(once)
	if once != twice {
		t.Fatalf("rewrite not idempotent\nonce:  %q\ntwice: %q", once, twice)
	}
}

// A finding is usually a note about an EXTERNAL system — an API's request
// shape, an endpoint path, a library quirk — and those change without telling
// us. The hint used to be a bare date, which left the model to do the
// arithmetic and draw the conclusion; acting on a stale finding costs a wrong
// tool and a debugging detour that never suspects the memory.
func TestRecallAgeNoteGradesByAge(t *testing.T) {
	stamp := func(daysAgo int) string {
		return recallAgeNote(time.Now().AddDate(0, 0, -daysAgo).Format(time.RFC3339))
	}

	// Recent findings carry the date and nothing else — most are fine, and a
	// warning on everything is a warning on nothing.
	for _, d := range []int{0, 10, 89} {
		got := stamp(d)
		if !strings.HasPrefix(got, "(saved ") {
			t.Errorf("%dd: missing the saved date: %q", d, got)
		}
		if strings.Contains(got, "re-verify") || strings.Contains(got, "may have changed") {
			t.Errorf("%dd old should not be cautioned: %q", d, got)
		}
	}

	// A season on: flag that it may have moved, without alarm.
	mid := stamp(120)
	if !strings.Contains(mid, "may have changed since") {
		t.Errorf("120d should note possible change: %q", mid)
	}
	if strings.Contains(mid, "re-verify") {
		t.Errorf("120d should not yet demand re-verification: %q", mid)
	}

	// A year on: say what to do about it.
	old := stamp(400)
	if !strings.Contains(old, "re-verify before relying") {
		t.Errorf("400d should say what the age MEANS: %q", old)
	}
	if !strings.Contains(old, "external system") {
		t.Errorf("400d should name the class of finding at risk: %q", old)
	}
}

func TestRecallAgeNoteHandlesBadInput(t *testing.T) {
	for _, in := range []string{"", "not-a-date", "2026/07/28"} {
		if got := recallAgeNote(in); got != "" {
			t.Errorf("unparseable date %q should yield no hint, got %q", in, got)
		}
	}
	// A well-formed date always yields at least the stamp.
	if got := recallAgeNote(time.Now().Format(time.RFC3339)); got == "" {
		t.Error("a valid date should always produce the saved stamp")
	}
}

// Only findings get the hint — curated knowledge cites via its own locator,
// and dating a reference document says nothing about whether it is still true.
func TestOnlyFindingsCarryTheAgeHint(t *testing.T) {
	old := time.Now().AddDate(-2, 0, 0).Format(time.RFC3339)
	hits := []SearchHit{{ReportID: "r1", Title: "Endpoint shape", Text: "POST /v1/things", Date: old}}

	finding := renderRecallChunks("finding", "mem", hits)
	if !strings.Contains(finding, "re-verify") {
		t.Errorf("a two-year-old finding must carry the caution: %q", finding)
	}
	knowledge := renderRecallChunks("knowledge", "kb", hits)
	if strings.Contains(knowledge, "saved ") {
		t.Errorf("curated knowledge should not be age-stamped: %q", knowledge)
	}
}

// The live failure, 2026-09-03: an agent three messages into a session with no
// recall behind it called recall(id:"2bd98b45-…") — a UUID nobody gave it, for
// "that research" that did not exist. The old error described the FORMAT, so
// the model corrected the format and re-sent the same invented reference twice.
func TestAnInventedRecallIDIsNamedAsInvented(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "tell me a story"},
		{Role: "assistant", Content: "I'd love to. What genre?"},
		{Role: "user", Content: "science fiction"},
	}}}

	_, err := turn.recallFetch("2bd98b45-04b0-4fc4-8de4-df71f9a570e8")
	if err == nil {
		t.Fatal("an id nobody issued must not resolve")
	}
	// The instruction that ends the loop is the name of the tool that produces
	// real ids, not a restatement of the id grammar.
	if !strings.Contains(err.Error(), "query") {
		t.Errorf("the error must send the model to a query: %v", err)
	}
	if !strings.Contains(err.Error(), "never constructed") {
		t.Errorf("the error must say ids are not constructed: %v", err)
	}

	// The second and third calls of the live transcript: same fabrication, one
	// with a prefix that was not even in the list the model had just been shown,
	// one with a real prefix. Both must land on the same answer.
	for _, id := range []string{
		"knowledge:2bd98b45-04b0-4fc4-8de4-df71f9a570e8",
		"doc:2bd98b45-04b0-4fc4-8de4-df71f9a570e8",
	} {
		_, err := turn.recallFetch(id)
		if err == nil || !strings.Contains(err.Error(), "never constructed") {
			t.Errorf("%s: a better-formed fabrication is still a fabrication, got %v", id, err)
		}
	}
}

// An id the agent was actually handed is not a fabrication, however it arrived.
// A false negative here would refuse a legitimate lookup, so the scan is
// deliberately generous: the bare reference counts, and a user typing an id
// counts.
func TestAnIssuedRecallIDIsNotTreatedAsInvented(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "what do we know about drones?"},
		{Role: "assistant", ToolCalls: []PersistedToolCall{{
			Name:   "recall",
			Result: "- [knowledge] Autonomous systems\n  id: doc:abc123\n  Some excerpt.",
		}}},
	}}}

	if !turn.recallIDWasIssued("doc:abc123") {
		t.Error("an id printed in a recall result was issued")
	}
	// Re-prefixing a real reference is malformed, not invented — the agent has
	// the ref, it just dressed it wrong.
	if !turn.recallIDWasIssued("abc123") {
		t.Error("the bare reference counts as issued")
	}
	if !turn.recallIDWasIssued("wrongkind:abc123") {
		t.Error("a real ref under a wrong prefix is malformed, not invented")
	}
	// So that one gets the FORMAT error, which is the correct advice for it.
	_, err := turn.recallFetch("wrongkind:abc123")
	if err == nil || !strings.Contains(err.Error(), "unrecognized id") {
		t.Errorf("a malformed issued id should get the format error, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "never constructed") {
		t.Error("an id the agent really holds must not be called a fabrication")
	}

	// An id the user typed is issued too.
	typed := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "open doc:xyz789 for me"},
	}}}
	if !typed.recallIDWasIssued("doc:xyz789") {
		t.Error("an id the user typed was issued")
	}
}

// forget deletes, and its miss message ("it may already be gone") reads as
// confirmation that the thing existed and is now handled — the worst possible
// answer to an id nobody ever issued.
func TestForgetRefusesAnInventedID(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "forget what you know about me"},
	}}}
	if turn.recallIDWasIssued("fact:made-up-id") {
		t.Fatal("nothing in this session issued that id")
	}
	err := turn.inventedRecallIDError("fact:made-up-id")
	if !strings.Contains(err.Error(), "query") || !strings.Contains(err.Error(), "never constructed") {
		t.Errorf("forget must give the same directive as recall: %v", err)
	}
}

// No session, nothing issued — and the guard must not panic reaching for one.
func TestRecallIDIssuedIsSafeWithoutASession(t *testing.T) {
	if (&chatTurn{}).recallIDWasIssued("doc:abc") {
		t.Error("no session, no issued ids")
	}
	if (&chatTurn{session: &ChatSession{}}).recallIDWasIssued("") {
		t.Error("an empty id was never issued")
	}
}

// The tool description has to carry the rule too: the error is the backstop,
// the description is what stops the call being made at all.
func TestRecallDescriptionForbidsConstructingIDs(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{Name: "Wren"}}
	def := turn.recallToolDef()
	if !strings.Contains(def.Tool.Description, "never construct") {
		t.Error("the recall description must forbid constructing ids")
	}
	idParam, ok := def.Tool.Parameters["id"]
	if !ok {
		t.Fatal("recall must still take an id")
	}
	if !strings.Contains(idParam.Description, "COPIED") {
		t.Errorf("the id parameter must say where an id comes from: %q", idParam.Description)
	}
}

// An id this same turn's recall printed counts as given: the thread scan only
// sees messages saved when a turn ends. And a copy slip of one, four
// characters dropped from the middle, gets the real id named instead of "no
// such id was ever given"; forget still acts only on what is re-sent.
func TestARecallIDFromThisTurnCountsAndASlipIsNamed(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{}}
	real := "fact:0795eddd-1540-4171-866e-8d6e9ddefc22"
	out := "- [pinned] User Craig remains online finalizing gohart release readiness.\n  id: " + real + "\n- [knowledge] FlashInfer\n  id: doc:autofill-f57ab20a\n"
	if turn.recallIDWasIssued(real) {
		t.Fatal("precondition: nothing issued yet")
	}
	turn.noteIssuedRecallIDs(out)
	if !turn.recallIDWasIssued(real) || !turn.recallIDWasIssued("doc:autofill-f57ab20a") {
		t.Fatal("ids this turn's recall printed should count as given")
	}
	slip := "fact:0795eddd-1540-4171-866e-9ddefc22"
	if turn.recallIDWasIssued(slip) {
		t.Fatal("a slip is not the id")
	}
	err := turn.inventedRecallIDError(slip)
	if !strings.Contains(err.Error(), real) || !strings.Contains(err.Error(), "copy one exactly") {
		t.Errorf("the slip should name the real id: %v", err)
	}
	if err := turn.inventedRecallIDError("fact:made-up-id"); strings.Contains(err.Error(), real) || !strings.Contains(err.Error(), "never constructed") {
		t.Errorf("an invented id gets the plain refusal, no suggestion: %v", err)
	}
	if err := turn.inventedRecallIDError("doc:0795eddd-1540-4171-866e-9ddefc22"); strings.Contains(err.Error(), real) {
		t.Errorf("a near id of another kind is not suggested: %v", err)
	}
}

// The recall score log names every candidate with its score and what became
// of it, so the operator floor can be set from numbers instead of a guess.
func TestRecallScoreLogNamesEachCandidate(t *testing.T) {
	l := recallScoreLog{query: "gohart", layer: "knowledge/finding"}
	l.add(SearchHit{Score: 0.3812, ReportID: "765f85f4", Title: "It's cowardly to be a billionaire with a doomsday bunker."}, "shown")
	l.add(SearchHit{Score: 0.2104, ReportID: "f5d6c192", Title: "Executive Summary"}, "below floor")
	if len(l.entries) != 2 {
		t.Fatalf("one entry per candidate: %v", l.entries)
	}
	if !strings.HasPrefix(l.entries[0], "0.381 765f85f4 ") || !strings.HasSuffix(l.entries[0], "(shown)") || !strings.Contains(l.entries[0], "...") {
		t.Errorf("score, id, a clipped title and its fate: %q", l.entries[0])
	}
	if !strings.Contains(l.entries[1], "(below floor)") {
		t.Errorf("a dropped candidate says why: %q", l.entries[1])
	}
	l.emit() // must not panic
}

func withRecencyWeight(t *testing.T, w float64) func() {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, TunableRecencyWeight, w)
	SetTunablesDB(db)
	return func() { SetTunablesDB(nil) }
}

// TestRerankFindingsByRecency: a slightly-less-relevant but FRESH finding should
// overtake a slightly-more-relevant YEAR-OLD one only when recency weighting is
// on and strong enough. With it off, semantic score alone decides.
func TestRerankFindingsByRecency(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	old := now.Add(-365 * 24 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)

	stale := SearchHit{ReportID: "stale", Score: 0.80, Date: old}
	recent := SearchHit{ReportID: "recent", Score: 0.75, Date: fresh}
	input := func() []SearchHit { return []SearchHit{stale, recent} }

	// Weight off → pure semantic order: the 0.80 hit stays first.
	restore := withRecencyWeight(t, 0)
	if got := rerankFindingsByRecency(input(), now); got[0].ReportID != "stale" {
		t.Fatalf("weight off should keep semantic order, got %q first", got[0].ReportID)
	}
	restore()

	// Strong weight → the year-old finding is down-weighted below the fresh one.
	restore = withRecencyWeight(t, 1)
	if got := rerankFindingsByRecency(input(), now); got[0].ReportID != "recent" {
		t.Fatalf("strong recency should promote the fresh finding, got %q first", got[0].ReportID)
	}
	restore()
}

// TestRecallAgeNote: findings show an absolute saved-date hint; a missing or
// unparseable date yields no note (never a bogus one).
//
// Dated RELATIVE to now. The literal date this once asserted kept its text
// only until real time moved past the caution threshold, at which point the
// test failed on correct behavior — a hardcoded date makes an age assertion
// rot by construction.
func TestRecallAgeNote(t *testing.T) {
	fresh := time.Now().AddDate(0, 0, -2)
	want := "(saved " + fresh.Format("2006-01-02") + ")"
	if got := recallAgeNote(fresh.Format(time.RFC3339)); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := recallAgeNote(""); got != "" {
		t.Fatalf("empty date should give no note, got %q", got)
	}
	if got := recallAgeNote("not-a-date"); got != "" {
		t.Fatalf("unparseable date should give no note, got %q", got)
	}
}
