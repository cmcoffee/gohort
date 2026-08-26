package orchestrate

import (
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// The bounded view was gated to persistent "channel:" threads, on the stated
// reasoning that a normal chat session "gets a fresh id per conversation and is
// short-lived". That is how they are usually used, and nothing enforces it —
// one thread somebody keeps returning to grows without limit while the gate
// excludes it from the only thing that would stop it. Observed live at ~1.4M
// tokens of conversation against a 262k window, failing every turn.
func TestALongOrdinarySessionIsBounded(t *testing.T) {
	const window = 262144

	// A short session is left alone: verbatim is the point for a disposable
	// thread, and summarizing one would spend an LLM call to lose detail.
	short := []ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	if historyOutgrewItsSession(short, window) {
		t.Error("a two-message session was treated as overgrown")
	}

	// A session that has genuinely grown past the framework's own steady-state
	// budget is bounded whatever its id.
	var long []ChatMessage
	for i := 0; i < 40; i++ {
		long = append(long, ChatMessage{Role: "user", Content: strings.Repeat("word ", 4000)})
	}
	if !historyOutgrewItsSession(long, window) {
		t.Fatalf("a session of ~%dk tokens is still treated as short-lived", len(long)*4000*5/4000)
	}
}

// contextSize is routinely ZERO — it comes from an optional interface a
// provider may not implement. A check that disables itself when it cannot
// measure disables itself exactly where the measurement mattered, which is the
// mistake that cost three rounds of this investigation.
func TestBoundingStillWorksWithNoReportedWindow(t *testing.T) {
	var huge []ChatMessage
	for i := 0; i < 200; i++ {
		huge = append(huge, ChatMessage{Role: "user", Content: strings.Repeat("x", 4000)})
	}
	if !historyOutgrewItsSession(huge, 0) {
		t.Fatal("with no reported window, an enormous session is treated as fine")
	}
	// And a small one is still small.
	if historyOutgrewItsSession([]ChatMessage{{Content: "short"}}, 0) {
		t.Error("with no reported window, a short session is needlessly compacted")
	}
}

// The scan stops as soon as the answer is known — this runs on every turn of
// every session, including ones with thousands of messages.
func TestTheCheckStopsEarly(t *testing.T) {
	msgs := []ChatMessage{{Content: strings.Repeat("x", 4_000_000)}}
	for i := 0; i < 10000; i++ {
		msgs = append(msgs, ChatMessage{Content: "tail"})
	}
	if !historyOutgrewItsSession(msgs, 262144) {
		t.Fatal("did not detect an overgrown session")
	}
}

// --- the cleanup has to be able to run ---------------------------------------

// The background fold built ONE prompt from the entire aging span. Fine while
// a thread was folded regularly; fatal once one had not been. A backlog past
// the window failed the call, CompactConversation returned unchanged, the
// cursor never advanced, and the fold failed identically every turn — so the
// thread paid the agent loop's emergency recovery forever, because the cleanup
// that would have ended it was itself too big to run. Reported as a cortex
// thread where saying "hello" cost a full recovery cycle every time.
func TestTheBacklogIsFoldedInPiecesThatFit(t *testing.T) {
	// ~1.4M tokens of history, the size seen live.
	var aging []Message
	for i := 0; i < 350; i++ {
		aging = append(aging, Message{Role: "user", Content: strings.Repeat("word ", 4000)})
	}
	chunks := chunkMessages(aging, operatorFoldChunkTokens)
	if len(chunks) < 2 {
		t.Fatalf("a backlog of %d messages folded in %d chunk(s) — it is still one impossible call", len(aging), len(chunks))
	}
	// Every chunk must fit a request comfortably.
	for i, c := range chunks {
		n := 0
		for _, m := range c {
			n += EstimateTokens(m.Content)
		}
		if n > operatorFoldChunkTokens*3 {
			t.Errorf("chunk %d is %d tokens against a %d budget", i, n, operatorFoldChunkTokens)
		}
	}
	// And nothing may be dropped: the caller advances its cursor past the
	// WHOLE span on success, so a chunker that stopped early would mark
	// content summarized that was never read.
	seen := 0
	for _, c := range chunks {
		seen += len(c)
	}
	if seen != len(aging) {
		t.Fatalf("chunking covered %d of %d messages — the rest would be marked folded without being read", seen, len(aging))
	}
}

// A short backlog stays a single call: chunking must not add round trips to
// the ordinary case it was never about.
func TestASmallBacklogFoldsInOneCall(t *testing.T) {
	aging := []Message{
		{Role: "user", Content: "what is the scheduler doing"},
		{Role: "assistant", Content: "it runs nightly"},
	}
	if got := len(chunkMessages(aging, operatorFoldChunkTokens)); got != 1 {
		t.Fatalf("a two-message backlog folded in %d calls, want 1", got)
	}
	if got := len(chunkMessages(nil, operatorFoldChunkTokens)); got != 0 {
		t.Fatalf("an empty backlog produced %d chunks", got)
	}
}

// --- one fold at a time ------------------------------------------------------

// The in-flight guard is keyed per (agent, session), which sufficed while only
// persistent channel threads folded. Widening the trigger to any overgrown
// session made a stampede possible: several threads crossing at once, each
// running a series of LLM calls AND ingesting its folded span into the vector
// store.
//
// The store is bolt, so writes are serialized deployment-wide and a bulk
// ingest is a long one. Concurrent folds queue every OTHER write behind them,
// felt as a page that is usually quick and occasionally takes seconds.
func TestOnlyOneFoldRunsAtATime(t *testing.T) {
	var mu sync.Mutex
	concurrent, peak := 0, 0

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			operatorFoldSlot <- struct{}{}
			defer func() { <-operatorFoldSlot }()

			mu.Lock()
			concurrent++
			if concurrent > peak {
				peak = concurrent
			}
			mu.Unlock()

			time.Sleep(2 * time.Millisecond) // stand in for the LLM calls + ingest

			mu.Lock()
			concurrent--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if peak != 1 {
		t.Fatalf("%d folds ran at once; each one holds the write path while it ingests", peak)
	}
}

// The slot must be released even when a fold panics, or one crash stops every
// thread in the deployment from ever folding again — the failure that turns a
// latency fix into a permanent stall.
func TestAPanickingFoldReleasesTheSlot(t *testing.T) {
	func() {
		defer func() { _ = recover() }()
		operatorFoldSlot <- struct{}{}
		defer func() { <-operatorFoldSlot }()
		panic("fold blew up")
	}()

	select {
	case operatorFoldSlot <- struct{}{}:
		<-operatorFoldSlot
	case <-time.After(time.Second):
		t.Fatal("the slot was never released; no thread can fold again")
	}
}
