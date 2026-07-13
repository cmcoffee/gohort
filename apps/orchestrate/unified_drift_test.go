package orchestrate

import (
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

	_, err := ct.rememberToolDef().Handler(map[string]any{"content": "long reference finding about the Acme API"})
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
	if _, err := ct.forgetToolDef().Handler(map[string]any{}); err == nil {
		t.Fatal("forget with neither id nor query must error")
	}
	ct.agent.DisableInferred = true
	if _, err := ct.forgetToolDef().Handler(map[string]any{"query": "acme api"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("query-mode with Inferred off must refuse: %v", err)
	}
}
