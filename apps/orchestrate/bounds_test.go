package orchestrate

import (
	"fmt"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// seedFindingChunk writes one derived-finding chunk directly into the vector
// store with a controlled date.
func seedFindingChunk(vdb Database, id, source, reportID, text string, date time.Time) {
	vdb.Set(EmbeddedChunks, id, EmbeddedChunk{
		ID: id, Source: source, ReportID: reportID, Section: "## Note",
		Text: text, Date: date.Format(time.RFC3339),
	})
}

// TestEnforceFindingCap: past the cap the OLDEST findings are deleted;
// chat-pasted attachments (same reportID shape, :attachments bucket) and
// uploaded documents (orch-upload-*) are never touched.
func TestEnforceFindingCap(t *testing.T) {
	oldVDB := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	defer func() { VectorDB = oldVDB }()
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, tuneFindingHardCap, float64(2))
	SetTunablesDB(tdb)
	defer SetTunablesDB(nil)

	base := knowledgeSource("u", "ag", "")
	now := time.Now()
	for i := 0; i < 4; i++ { // f0 oldest ... f3 newest
		seedFindingChunk(VectorDB, fmt.Sprintf("c%d", i), base+":apis",
			fmt.Sprintf("orch-know-f%d", i), fmt.Sprintf("finding %d", i), now.Add(time.Duration(i-4)*time.Hour))
	}
	seedFindingChunk(VectorDB, "att", base+":attachments", "orch-know-att", "pasted doc", now.Add(-100*time.Hour))
	seedFindingChunk(VectorDB, "upl", base+":apis", "orch-upload-1", "uploaded doc", now.Add(-100*time.Hour))
	seedFindingChunk(VectorDB, "other", knowledgeSource("u", "other-agent", ""), "orch-know-x", "someone else's", now.Add(-100*time.Hour))

	enforceFindingCap("u", "ag")

	remaining := map[string]bool{}
	for _, k := range VectorDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if VectorDB.Get(EmbeddedChunks, k, &c) {
			remaining[c.ReportID] = true
		}
	}
	for _, gone := range []string{"orch-know-f0", "orch-know-f1"} {
		if remaining[gone] {
			t.Fatalf("oldest finding %s should have been evicted", gone)
		}
	}
	for _, kept := range []string{"orch-know-f2", "orch-know-f3", "orch-know-att", "orch-upload-1", "orch-know-x"} {
		if !remaining[kept] {
			t.Fatalf("%s must survive the cap (findings keep newest; attachments/uploads/other agents exempt)", kept)
		}
	}
}

// TestFindingExactDuplicate: the embeddings-off dedup tier catches a verbatim
// re-save (whitespace/case-insensitive), scoped to the agent's own findings.
func TestFindingExactDuplicate(t *testing.T) {
	oldVDB := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	defer func() { VectorDB = oldVDB }()

	base := knowledgeSource("u", "ag", "")
	seedFindingChunk(VectorDB, "c1", base+":apis", "orch-know-1",
		"The Acme API rotates tokens every 24h.", time.Now())

	ct := &chatTurn{user: "u", agent: AgentRecord{ID: "ag"}}
	if !ct.findingExactDuplicate("the acme  api rotates tokens every 24h.") {
		t.Fatal("verbatim re-save (case/whitespace drift) should dedup")
	}
	if ct.findingExactDuplicate("The Acme API uses OAuth device flow.") {
		t.Fatal("different content must not dedup")
	}
	other := &chatTurn{user: "u", agent: AgentRecord{ID: "other-agent"}}
	if other.findingExactDuplicate("The Acme API rotates tokens every 24h.") {
		t.Fatal("another agent's finding must not dedup this agent's save")
	}
}

// TestAppendCortexObsCap: an observation-ONLY cortex thread (no fold cursor)
// is length-capped with forget-old semantics; once compaction state exists,
// the append leaves bounding to the turn pipeline (cursor consistency).
func TestAppendCortexObsCap(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(agentsTable, "ag", AgentRecord{ID: "ag", Owner: "u", Name: "Agent", Cortex: true})

	for i := 0; i < storedHistoryCap+storedHistorySlack+10; i++ {
		appendCortexObs(db, "ag", "monitor", "event", fmt.Sprintf("observation %d", i))
	}
	sess, ok := loadChatSession(db, "ag", cortexSessionID("ag"))
	if !ok {
		t.Fatal("cortex session missing")
	}
	if len(sess.Messages) > storedHistoryCap+storedHistorySlack {
		t.Fatalf("observation-only cortex not capped: %d messages", len(sess.Messages))
	}

	// With a fold cursor present, the append must NOT drop leading messages.
	saveCompactState(db, "ag", cortexSessionID("ag"), CompactState{Summary: "s", SummarizedThrough: 3})
	before := len(sess.Messages)
	for i := 0; i < storedHistorySlack+40; i++ {
		appendCortexObs(db, "ag", "monitor", "event", fmt.Sprintf("late observation %d", i))
	}
	sess, _ = loadChatSession(db, "ag", cortexSessionID("ag"))
	if len(sess.Messages) != before+storedHistorySlack+40 {
		t.Fatalf("append trimmed a thread that has compaction state: %d messages", len(sess.Messages))
	}
}

// TestFindingCapCountsOthersApart: on a pooled agent the owner's copy holds
// what other people added too. Those are capped apart from the owner's own,
// each to the limit, so a busy pool never evicts the owner's findings and the
// owner's saving never evicts what others added.
func TestFindingCapCountsOthersApart(t *testing.T) {
	oldVDB := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	defer func() { VectorDB = oldVDB; InvalidateChunkCache() }()
	InvalidateChunkCache()
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, tuneFindingHardCap, float64(2))
	SetTunablesDB(tdb)
	defer SetTunablesDB(nil)

	base := knowledgeSource("u", "ag", "")
	now := time.Now()
	// The owner's two are the oldest of all, so a single count would evict
	// them first.
	for i := 0; i < 2; i++ {
		seedFindingChunk(VectorDB, fmt.Sprintf("own%d", i), base+":general",
			fmt.Sprintf("orch-know-own%d", i), "owner's", now.Add(time.Duration(i-100)*time.Hour))
	}
	for i := 0; i < 4; i++ { // p0 oldest ... p3 newest
		id := fmt.Sprintf("p%d", i)
		VectorDB.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: base + ":general", ReportID: "orch-know-" + id,
			Section: "## Note", Text: "pooled", By: "bob", Date: now.Add(time.Duration(i-4) * time.Hour).Format(time.RFC3339)})
	}
	InvalidateChunkCache()

	enforceFindingCap("u", "ag")

	remaining := map[string]bool{}
	for _, c := range ChunksWhere(VectorDB, func(EmbeddedChunk) bool { return true }) {
		remaining[c.ReportID] = true
	}
	for _, kept := range []string{"orch-know-own0", "orch-know-own1", "orch-know-p2", "orch-know-p3"} {
		if !remaining[kept] {
			t.Errorf("%s was evicted", kept)
		}
	}
	for _, gone := range []string{"orch-know-p0", "orch-know-p1"} {
		if remaining[gone] {
			t.Errorf("%s survived past the limit for what others added", gone)
		}
	}
}
