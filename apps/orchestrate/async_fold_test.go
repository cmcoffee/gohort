package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// stubLLM satisfies core.LLM with a canned (optionally gated) response.
type stubLLM struct {
	reply string
	gate  chan struct{} // when non-nil, Chat blocks until the gate closes
}

func (s *stubLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	if s.gate != nil {
		<-s.gate
	}
	return &Response{Content: s.reply}, nil
}

func (s *stubLLM) ChatStream(ctx context.Context, msgs []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	return s.Chat(ctx, msgs, opts...)
}

func seedFoldSession(t *testing.T, db Database, agentID, sessID string, n int) {
	t.Helper()
	sess := ChatSession{ID: sessID, AgentID: agentID, Created: time.Now()}
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sess.Messages = append(sess.Messages, ChatMessage{Role: role, Content: "message about the atlas project number " + strings.Repeat("x", i%3)})
	}
	if _, err := saveChatSession(db, sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// TestFoldOperatorHistoryCycle drives one background-fold cycle synchronously:
// the reloaded thread folds, the cursor + fold counter persist, the span is
// archived under the fold-seq key, and the surfaced fact lands in Explicit
// memory with Source=observed.
func TestFoldOperatorHistoryCycle(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &stubLLM{reply: "Running summary of the thread.\nFACTS: user prefers metric units"}}}
	agent := AgentRecord{ID: "ag", Owner: "u", ContextDepth: 4}
	seedFoldSession(t, db, "ag", "s1", 30)

	T.foldOperatorHistory(db, agent, "s1", CompactionConfig{KeepRecent: 4, Trigger: 6})

	st := loadCompactState(db, "ag", "s1")
	if st.SummarizedThrough != 26 || st.FoldSeq != 1 {
		t.Fatalf("fold state not persisted: through=%d seq=%d", st.SummarizedThrough, st.FoldSeq)
	}
	if !strings.Contains(st.Summary, "Running summary") {
		t.Fatalf("summary not persisted: %q", st.Summary)
	}
	source := operatorLCMSource("ag", "s1")
	if chunks := FetchRecallSpanChunks(db, source, source+"#f0"); len(chunks) == 0 {
		t.Fatal("folded span was not archived under the fold-seq key")
	}
	facts := ListMemoryFacts(db, factsNamespace("ag"))
	if len(facts) != 1 || facts[0].Source != MemSourceObserved {
		t.Fatalf("fold fact should be stored once as observed, got %+v", facts)
	}
}

// TestFoldRespectsMemoryToggles: a memory-off agent still folds (bounded
// history) but seeds no facts.
func TestFoldRespectsMemoryToggles(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &stubLLM{reply: "Summary.\nFACTS: something durable"}}}
	agent := AgentRecord{ID: "ag2", Owner: "u", DisableExplicit: true}
	seedFoldSession(t, db, "ag2", "s1", 30)

	T.foldOperatorHistory(db, agent, "s1", CompactionConfig{KeepRecent: 4, Trigger: 6})

	if st := loadCompactState(db, "ag2", "s1"); st.SummarizedThrough == 0 {
		t.Fatal("memory-off agent should still get its history folded")
	}
	if facts := ListMemoryFacts(db, factsNamespace("ag2")); len(facts) != 0 {
		t.Fatalf("DisableExplicit agent must not receive fold facts, got %+v", facts)
	}
}

// TestCompactOperatorHistoryDoesNotBlockOnFold: the turn path renders the
// pre-fold view immediately while the fold (a blocked worker call here) runs
// in the background; the state lands once the worker returns. trimStoredHistory
// defers while the fold is in flight.
func TestCompactOperatorHistoryDoesNotBlockOnFold(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	gate := make(chan struct{})
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &stubLLM{reply: "Summary.\nFACTS: none", gate: gate}}}
	agent := AgentRecord{ID: "ag3", Owner: "u", ContextDepth: 4}
	seedFoldSession(t, db, "ag3", "s1", 30)
	sess, _ := loadChatSession(db, "ag3", "s1")

	done := make(chan []ChatMessage, 1)
	go func() { done <- T.compactOperatorHistory(db, "u", agent, "s1", sess.Messages) }()
	var view []ChatMessage
	select {
	case view = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("compactOperatorHistory blocked on the worker fold")
	}
	// Pre-fold view: nothing summarized yet, so the whole thread renders.
	if len(view) != len(sess.Messages) {
		t.Fatalf("expected the pre-fold view (%d msgs), got %d", len(sess.Messages), len(view))
	}
	if !operatorFoldBusy("ag3", "s1") {
		t.Fatal("a background fold should be in flight")
	}
	// Trim must defer while the fold computes its cursor.
	long := make([]ChatMessage, storedHistoryCap+storedHistorySlack+10)
	for i := range long {
		long[i] = ChatMessage{Role: "user", Content: "m"}
	}
	if got := T.trimStoredHistory(db, agent, "s1", long); len(got) != len(long) {
		t.Fatal("trimStoredHistory must defer while a fold is in flight")
	}

	close(gate) // let the worker "answer"
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st := loadCompactState(db, "ag3", "s1"); st.SummarizedThrough == 26 && st.FoldSeq == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background fold never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFindingMatchesPinned: the read-time cross-layer dedup is verbatim-only —
// a finding that merely overlaps a pinned note still shows.
func TestFindingMatchesPinned(t *testing.T) {
	pinned := []string{"User prefers metric units.", "Deploy header is X-Auth."}
	if !findingMatchesPinned("user prefers  METRIC units.", pinned) {
		t.Fatal("verbatim duplicate (case/whitespace drift) should dedup")
	}
	if findingMatchesPinned("User prefers metric units for engineering docs, imperial for recipes.", pinned) {
		t.Fatal("overlapping-but-richer finding must not dedup")
	}
	if findingMatchesPinned("anything", nil) {
		t.Fatal("no pinned notes → no dedup")
	}
}
