package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &FakeLLM{Turns: []FakeTurn{{Content: "Running summary of the thread.\nFACTS: user prefers metric units", Repeat: true}}}}}
	agent := AgentRecord{ID: "ag", Owner: "u", ContextDepth: 4}
	seedFoldSession(t, db, "ag", "s1", 30)

	T.foldOperatorHistory(db, "u", agent, "s1", CompactionConfig{KeepRecent: 4, Trigger: 6})

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
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &FakeLLM{Turns: []FakeTurn{{Content: "Summary.\nFACTS: something durable", Repeat: true}}}}}
	agent := AgentRecord{ID: "ag2", Owner: "u", DisableExplicit: true}
	seedFoldSession(t, db, "ag2", "s1", 30)

	T.foldOperatorHistory(db, "u", agent, "s1", CompactionConfig{KeepRecent: 4, Trigger: 6})

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
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &FakeLLM{Turns: []FakeTurn{{Content: "Summary.\nFACTS: none", Wait: gate, Repeat: true}}}}}
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

// A thread shows where a history fold stopped the agent reading word for
// word. The page kept every message on screen while the agent worked from a
// summary of most of them, so it looked as if the agent had forgotten what
// was plainly there. The fold said so only in the trail.

func TestTheFoldIsMarkedWhereTheAgentStopsReading(t *testing.T) {
	thread := func(n int) []ChatMessage {
		out := make([]ChatMessage, n)
		for i := range out {
			out[i] = ChatMessage{Role: "user", Content: "m"}
		}
		return out
	}
	marked := func(msgs []ChatMessage) int {
		for i, m := range msgs {
			if m.FoldBefore != nil {
				return i
			}
		}
		return -1
	}
	// Served from the start: marked at the fold point.
	tail := thread(30)
	markFoldPoint(CompactState{SummarizedThrough: 26}, tail, 0)
	if i := marked(tail); i != 26 || tail[26].FoldBefore.Summarized != 26 {
		t.Errorf("marked at %d, want 26", i)
	}
	// Served from index 20: the same message, at its place in the tail.
	tail = thread(10)
	markFoldPoint(CompactState{SummarizedThrough: 26}, tail, 20)
	if i := marked(tail); i != 6 {
		t.Errorf("in a tail served from 20, marked at %d, want 6", i)
	}
	// A hidden message at the fold point passes the mark on to the next one shown.
	tail = thread(30)
	tail[26].Hidden = true
	markFoldPoint(CompactState{SummarizedThrough: 26}, tail, 0)
	if i := marked(tail); i != 27 {
		t.Errorf("past a hidden message, marked at %d, want 27", i)
	}
	// Not in what was served, or nothing folded: no mark.
	for _, c := range []struct{ through, n, off int }{{26, 10, 30}, {0, 30, 0}, {10, 10, 20}} {
		tail = thread(c.n)
		markFoldPoint(CompactState{SummarizedThrough: c.through}, tail, c.off)
		if i := marked(tail); i != -1 {
			t.Errorf("through %d, served %d from %d: marked at %d, want none", c.through, c.n, c.off, i)
		}
	}
}

// End to end: the session a page loads carries the mark, and only there.
func TestALoadedThreadCarriesTheFoldMark(t *testing.T) {
	app, req, udb := authedApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "ag", Owner: "alice", Name: "Wiwee", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	seedFoldSession(t, udb, "ag", "s1", 30)
	saveCompactState(udb, "ag", "s1", CompactState{SummarizedThrough: 26, FoldSeq: 1})
	w := httptest.NewRecorder()
	routed(app)(w, req(http.MethodGet, "/api/sessions/s1?agent_id=ag", nil))
	if w.Code != 200 {
		t.Fatalf("GET %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Messages      []map[string]any `json:"messages"`
		MessageOffset int              `json:"message_offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var at []int
	for i, m := range got.Messages {
		if m["fold_before"] != nil {
			at = append(at, i+got.MessageOffset)
		}
	}
	if len(at) != 1 || at[0] != 26 {
		t.Errorf("the mark is on message(s) %v, want just 26", at)
	}
	sess, _ := loadChatSession(udb, "ag", "s1")
	for _, m := range sess.Messages {
		if m.FoldBefore != nil {
			t.Fatal("the mark was stored on the thread")
		}
	}
}

// The page draws it: a line above the first message the agent still reads
// word for word, once however often the thread is redrawn.
func TestThePageDrawsTheFoldMark(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	js := ReportCardHead[strings.Index(ReportCardHead, "<script>")+len("<script>") : strings.LastIndex(ReportCardHead, "</script>")]
	script := `
var hooks = [];
global.window = {uiRegisterMessageReplayHook: function(f) { hooks.push(f); }};
global.document = {createElement: function(tag) { return {tagName: tag, classList: {list: [], add: function(c) { this.list.push(c); }, contains: function(c) { return this.list.indexOf(c) >= 0; }}, set className(v) { this.classList.list = v.split(' '); }}; }};
` + js + `
var inserted = [];
var list = {insertBefore: function(n, ref) { inserted.push(n); ref.previousElementSibling = n; }};
var bubble = {parentNode: list, previousElementSibling: null};
hooks.forEach(function(h) { h(bubble, {role: 'user', content: 'x', fold_before: {summarized: 26}}); });
hooks.forEach(function(h) { h(bubble, {role: 'user', content: 'x', fold_before: {summarized: 26}}); });
var plain = {parentNode: list, previousElementSibling: null};
hooks.forEach(function(h) { h(plain, {role: 'user', content: 'y'}); });
if (inserted.length !== 1) { console.log('FAIL drew ' + inserted.length + ' marks'); process.exit(1); }
if (inserted[0].textContent.indexOf('26 messages above are summarized') < 0 || !inserted[0].classList.contains('orch-fold-mark')) {
  console.log('FAIL the mark reads ' + JSON.stringify(inserted[0].textContent)); process.exit(1);
}
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "fold.js")
	if err := os.WriteFile(tmp, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

func msgsOf(sizes ...int) []ChatMessage {
	out := make([]ChatMessage, 0, len(sizes))
	for i, n := range sizes {
		out = append(out, ChatMessage{Role: "user", Content: strings.Repeat("x ", n) + "#" + string(rune('a'+i))})
	}
	return out
}

// The gap this closes: every other bound here counts MESSAGES, and on a
// persistent thread a message is a standing report or a daily brief, not a chat
// turn. A thread sitting exactly at its configured depth of 12, with the fold
// cursor advancing and nothing broken, still arrived having eaten most of the
// window — while every message-shaped diagnostic said it was fine.
func TestTailIsBoundedByTokensNotJustMessages(t *testing.T) {
	// Twelve messages — a healthy count — that are enormous individually.
	// Roughly 4000 tokens each: one standing report, not one chat turn.
	big := make([]int, 12)
	for i := range big {
		big[i] = 8000
	}
	tail := msgsOf(big...)

	got := capTailTokens(tail, 1_000_000, "ag", "channel:ag")
	if len(got) >= len(tail) {
		t.Fatalf("a message-shaped bound let all %d through; the window is measured in tokens", len(tail))
	}
	total := 0
	for _, m := range got {
		total += EstimateTokens(m.Content)
	}
	if budget := TuneInt("tune_persistent_tail_tokens"); total > budget {
		t.Errorf("kept ~%d tokens against a budget of %d", total, budget)
	}
	// Trimmed from the FRONT: the newest messages are what the turn is about.
	if got[len(got)-1].Content != tail[len(tail)-1].Content {
		t.Error("the newest message must survive — it is the turn being answered")
	}
}

// A bound that can drop the user's just-typed message produces a model
// answering from a summary about a question it was never shown.
func TestTheNewestMessageIsAlwaysKept(t *testing.T) {
	// One message that blows the entire budget on its own.
	tail := msgsOf(100, 200000)
	got := capTailTokens(tail, 8000, "ag", "channel:ag")
	if len(got) != 1 {
		t.Fatalf("expected only the newest, got %d", len(got))
	}
	if got[0].Content != tail[1].Content {
		t.Error("the kept message must be the newest one, however expensive")
	}
}

// The cases where doing nothing is right.
func TestTailBudgetLeavesWellEnoughAlone(t *testing.T) {
	tail := msgsOf(10, 10, 10)

	// Comfortably inside the budget.
	if got := capTailTokens(tail, 32000, "ag", "s"); len(got) != 3 {
		t.Errorf("a small tail must pass through untouched, got %d", len(got))
	}
	// An undeclared window is not a reason to skip the budget: the budget is
	// an absolute number of tokens and means the same thing without one. (The
	// first version took a percent of the window and did nothing here, which
	// is exactly the silence this test now forbids.)
	if got := capTailTokens(msgsOf(90000, 90000, 90000), 0, "ag", "s"); len(got) >= 3 {
		t.Errorf("with no declared window the token budget still governs, got %d", len(got))
	}
	// A single message is already the minimum.
	if got := capTailTokens(msgsOf(500000), 8000, "ag", "s"); len(got) != 1 {
		t.Errorf("one message stays one message, got %d", len(got))
	}
	if got := capTailTokens(nil, 8000, "ag", "s"); got != nil {
		t.Error("an empty tail stays empty")
	}
}

// The budget is an absolute number of tokens, because that is what a turn
// COSTS. A share of the window meant 70k on a 200k model and 350k on a 1M one —
// so on the big-window deployment the bound was never reached, the thread
// stayed enormous, and the setting looked applied. (Reported live: a cortex turn
// still arriving at 205k with the percent budget in force.)
func TestTheBudgetDoesNotGrowWithTheWindow(t *testing.T) {
	tail := msgsOf(9000, 9000, 9000, 9000, 9000, 9000)
	small := capTailTokens(tail, 200_000, "ag", "s")
	huge := capTailTokens(tail, 1_000_000, "ag", "s")
	if len(small) != len(huge) {
		t.Errorf("a bigger window bought a bigger tail: %d vs %d — the budget is tokens, not a share",
			len(small), len(huge))
	}
}

// The share survives as a CEILING only: a deployment that raised the budget for
// a 200k model must not hand the same tail to a 32k local one and lose the
// system prompt to it.
func TestASmallWindowCapsTheBudget(t *testing.T) {
	tail := msgsOf(2000, 2000, 2000, 2000, 2000, 2000)
	got := capTailTokens(tail, 16000, "ag", "s")
	total := 0
	for _, m := range got {
		total += EstimateTokens(m.Content)
	}
	if ceiling := 16000 * tailWindowShareCapPct / 100; total > ceiling {
		t.Errorf("kept ~%d tokens of a 16000 window; the cap is %d", total, ceiling)
	}
	if len(got) == 0 {
		t.Error("the cap must still leave the turn itself")
	}
}

// The weight of a standing thread is not in what anybody said. A monitor wake
// or a standing report is a paragraph of content over pages of tool results,
// and those results are replayed into the prompt — so a budget that weighs
// Content alone never binds on the exact threads it exists for.
//
// Live: a cortex measured "4,635 tokens over 22 messages" by that reading and
// reached the model at 151k by the loop's own, which counts the bodies.
func TestTheBudgetWeighsToolResultsToo(t *testing.T) {
	report := func(body string) ChatMessage {
		return ChatMessage{Role: "assistant", Content: "Standing report filed.",
			ToolCalls: []PersistedToolCall{{Name: "fetch_url", Result: body}}}
	}
	big := strings.Repeat("x", 80_000) // ~20k tokens of result apiece
	tail := []ChatMessage{report(big), report(big), report(big), report(big)}

	// By content alone these are ~6 tokens each and nothing would ever trim.
	if content := EstimateTokens(tail[0].Content); content > 50 {
		t.Fatalf("fixture is wrong: the CONTENT should be trivial, got %d tokens", content)
	}
	got := capTailTokens(tail, 262_144, "ag", "channel:ag")
	if len(got) >= len(tail) {
		t.Fatalf("all %d kept — the budget is still weighing what was said, not what it costs", len(tail))
	}
	total := 0
	for _, m := range got {
		total += chatMessageTokens(m)
	}
	if budget := TuneInt("tune_persistent_tail_tokens"); total > budget {
		t.Errorf("kept ~%d tokens against a budget of %d", total, budget)
	}
}

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
