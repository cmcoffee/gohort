package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// clearObservedCeilings keeps cases from leaking into one another — the store
// is process-wide by design.
func clearObservedCeilings(t *testing.T) {
	t.Helper()
	reset := func() {
		observedCeilings.Range(func(k, _ any) bool { observedCeilings.Delete(k); return true })
	}
	reset()
	t.Cleanup(reset)
}

// The exact regression, in the numbers the log recorded: a worker advertising
// 262144 refused a prompt estimated at ~130k. Recovery used to take the
// configured number, ask stillTooBig whether the history fit inside it, hear
// "yes", and skip every rung of the ladder below — so the retry re-sent what
// had just been refused.
func TestRecoveryWindowLandsBelowTheRefusedPrompt(t *testing.T) {
	const configured, refused = 262144, 130333

	window := recoveryWindow(configured, fmt.Errorf("llama.cpp api error (500): Context size has been exceeded."), refused)
	if window >= refused {
		t.Fatalf("recovery window %d is not below the refused prompt %d — the ladder will no-op", window, refused)
	}

	// The behavioural consequence, which is the thing that actually matters:
	// a history that size must now read as too big, so summarize and elide run.
	history := historyOfAboutTokens(refused)
	if !stillTooBig(history, "", 0, window) {
		t.Error("history that was just refused does not read as too big in the recovery window")
	}
	// And the other half of the regression, pinned so it can't come back: the
	// SAME history reads as fitting against the configured window. That is
	// precisely why recovering into cfg.ContextSize did nothing at all.
	if stillTooBig(history, "", 0, configured) {
		t.Error("test no longer reproduces the bug: the refused history reads as too big even against the configured window")
	}
}

// A refusal with nothing to do with size must not drive recovery into a window
// too small to hold a turn.
func TestRecoveryWindowIsFloored(t *testing.T) {
	if got := recoveryWindow(262144, nil, 1000); got < observedCeilingFloor {
		t.Errorf("recovery window %d fell below the floor %d", got, observedCeilingFloor)
	}
}

// With nothing configured, the provider's own wording is still the better
// source than the blind fallback.
func TestRecoveryWindowFallsBackWhenNothingIsConfigured(t *testing.T) {
	err := fmt.Errorf("maximum context length is 32768 tokens, however you requested 40000 tokens")
	if got := recoveryWindow(0, err, 0); got != 32768 {
		t.Errorf("window = %d, want the 32768 the refusal named", got)
	}
	if got := recoveryWindow(0, fmt.Errorf("context exceeded"), 0); got != fallbackRecoveryWindow {
		t.Errorf("window = %d, want the %d fallback", got, fallbackRecoveryWindow)
	}
}

// The point of remembering: the NEXT turn has to build smaller, or it spends a
// round rediscovering the same wall.
func TestARefusalShrinksTheWindowTheNextTurnBuildsAgainst(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144

	if got := effectiveContextSize(configured); got != configured {
		t.Fatalf("with no refusal seen, effective size = %d, want the configured %d", got, configured)
	}
	noteContextRefusal(configured, 130333)
	got := effectiveContextSize(configured)
	if got >= 130333 {
		t.Errorf("effective size %d is not below the refused %d", got, 130333)
	}
	if got <= 0 {
		t.Errorf("effective size collapsed to %d", got)
	}
}

// Two refusals mean the ceiling is under both.
func TestSmallestRefusalWins(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144
	noteContextRefusal(configured, 130000)
	noteContextRefusal(configured, 90000)
	first := effectiveContextSize(configured)
	noteContextRefusal(configured, 200000) // a later, larger refusal teaches nothing new
	if second := effectiveContextSize(configured); second != first {
		t.Errorf("a larger refusal loosened the ceiling: %d → %d", first, second)
	}
}

// The real ceiling moves — on the deployment that prompted this, a 149,267-token
// prompt was accepted and a ~130k one refused later the same day. So the clamp
// has to lift on its own: no restart, no setting.
func TestTheCeilingExpiresSoTheWindowComesBack(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144
	noteContextRefusal(configured, 130333)
	if effectiveContextSize(configured) == configured {
		t.Fatal("the refusal was not recorded at all")
	}
	// Age the observation past its TTL.
	v, _ := observedCeilings.Load(configured)
	obs := v.(observedCeiling)
	observedCeilings.Store(configured, observedCeiling{tokens: obs.tokens, at: time.Now().Add(-observedCeilingTTL - time.Minute)})

	if got := effectiveContextSize(configured); got != configured {
		t.Errorf("stale ceiling still clamping: %d, want the full %d back", got, configured)
	}
	if _, still := observedCeilings.Load(configured); still {
		t.Error("expired observation was not dropped, so it keeps being re-checked")
	}
}

// A refusal at or above the configured window teaches nothing, and recording it
// would raise a ceiling a smaller refusal had correctly lowered.
func TestARefusalAboveTheClaimIsNotRecorded(t *testing.T) {
	clearObservedCeilings(t)
	const configured = 262144
	noteContextRefusal(configured, 400000)
	if got := effectiveContextSize(configured); got != configured {
		t.Errorf("effective size = %d, want the configured %d left alone", got, configured)
	}
}

// historyOfAboutTokens builds a history whose estimate is near n tokens, so a
// case can talk in the units the code does.
func historyOfAboutTokens(n int) []Message {
	return []Message{{Role: "user", Content: strings.Repeat("x", n*4)}}
}

// One tool returning a body larger than the window BRICKS a conversation: the
// newest tool result is spared unconditionally by compaction, so every
// following turn assembles a prompt that cannot fit and no message the user
// types can get through again. Reported live as a cortex thread that answered
// nothing, with a request of 2,135,474 tokens against a 262,144 window.
func TestAnOversizedResultDoesNotBrickTheSession(t *testing.T) {
	const window = 262144
	huge := strings.Repeat("x", 8<<20) // ~8 MiB, ~2M tokens
	msgs := []Message{
		{Role: "user", Content: "change the scheduler"},
		{Role: "assistant", ToolResults: []ToolResult{{Content: huge}}},
	}

	compactHistory(msgs, "system", 0, window, true)

	got := msgs[1].ToolResults[0].Content
	if len(got) >= len(huge) {
		t.Fatalf("the oversized result survived compaction (%d bytes) — the session stays unusable", len(got))
	}
	if len(got) > oversizedResultBytes(window)+512 {
		t.Fatalf("truncated to %d bytes, still too large for a round", len(got))
	}
	// It has to SAY it was truncated, and how to get the rest — a silently
	// shortened result is one the model will reason over as if complete.
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "Re-run the tool") {
		t.Errorf("the truncation is silent:\n%s", got[:200])
	}
}

// The single-result case is the one that returns EARLY today: with only one
// tool-result message, force compaction has nothing "old" to elide and bails
// out having done nothing at all.
func TestSingleOversizedResultIsStillCut(t *testing.T) {
	const window = 262144
	msgs := []Message{
		{Role: "assistant", ToolResults: []ToolResult{{Content: strings.Repeat("y", 4<<20)}}},
	}
	compactHistory(msgs, "system", 0, window, true)
	if len(msgs[0].ToolResults[0].Content) >= 4<<20 {
		t.Fatal("a lone oversized result was left untouched, so the session cannot recover")
	}
}

// The tail is kept, not the head: a command's error is at the END of its
// output, and that is overwhelmingly what an oversized result is.
func TestTruncationKeepsTheEndWhereTheErrorIs(t *testing.T) {
	body := strings.Repeat("noise\n", 200000) + "FATAL: the actual error\n"
	msgs := []Message{{Role: "assistant", ToolResults: []ToolResult{{Content: body}}}}
	compactHistory(msgs, "system", 0, 262144, true)
	if !strings.Contains(msgs[0].ToolResults[0].Content, "FATAL: the actual error") {
		t.Fatal("truncation discarded the end of the output, which is where the error lives")
	}
}

// Ordinary results must be left completely alone — a large file read has to
// keep working, and this only exists for bodies that could not be used anyway.
func TestOrdinaryResultsAreUntouched(t *testing.T) {
	body := strings.Repeat("z", 64<<10) // 64 KiB — big, but usable
	msgs := []Message{{Role: "assistant", ToolResults: []ToolResult{{Content: body}}}}
	compactHistory(msgs, "system", 0, 262144, true)
	if msgs[0].ToolResults[0].Content != body {
		t.Fatalf("an ordinary %d-byte result was altered", len(body))
	}
}

// An unknown window (a provider that never reported one) must still bound the
// result, or the one check that could have caught this is skipped exactly when
// nothing else knows the limit either.
func TestUnknownWindowStillBoundsAResult(t *testing.T) {
	if got := oversizedResultBytes(0); got <= 0 || got > 1<<20 {
		t.Fatalf("unknown window yields a cap of %d bytes", got)
	}
	// A big-context model is not held to a small model's limit.
	if oversizedResultBytes(1<<20) <= oversizedResultBytes(262144) {
		t.Error("the cap does not scale with the window")
	}
}

// --- saying where the tokens are ---------------------------------------------

// "The prompt is too large" is not a diagnosis. A live failure at two million
// tokens was investigated twice from the code and misdiagnosed twice, because
// nothing in the failure said WHICH part was big.
func TestTheFailureNamesWhereTheBulkIs(t *testing.T) {
	big := strings.Repeat("s", 400000)
	cfg := AgentLoopConfig{Tools: []AgentToolDef{
		{Tool: Tool{Name: "t", Description: "d"}},
	}}
	hist := []Message{{Role: "user", Content: "hi"}}

	// System-prompt-dominated: the case compaction can do nothing about, and
	// the one a "start a new session" suggestion actively misleads on.
	h := promptSizeHeadline(cfg, big, hist)
	if !strings.Contains(h, "system prompt") {
		t.Errorf("a system-dominated prompt is described as %q", h)
	}

	// Tool-schema-dominated.
	fat := AgentLoopConfig{Tools: []AgentToolDef{
		{Tool: Tool{Name: "t", Description: strings.Repeat("d", 400000)}},
	}}
	if h = promptSizeHeadline(fat, "", hist); !strings.Contains(h, "tool definitions") {
		t.Errorf("a tool-dominated prompt is described as %q", h)
	}

	// History-dominated, INCLUDING tool results — the original floor log
	// counted only Message.Content, so a history made of tool output reported
	// as small and pointed the investigation away from itself.
	heavy := []Message{{Role: "assistant", ToolResults: []ToolResult{{Content: big}}}}
	if h = promptSizeHeadline(cfg, "", heavy); !strings.Contains(h, "history") {
		t.Errorf("a history of tool results is described as %q", h)
	}
}

// The full report has to name the single biggest message, which is what turns
// "history is large" into something findable.
func TestTheReportNamesTheBiggestMessage(t *testing.T) {
	hist := []Message{
		{Role: "user", Content: "small"},
		{Role: "assistant", ToolResults: []ToolResult{{Content: strings.Repeat("x", 100000)}}},
		{Role: "user", Content: "also small"},
	}
	r := promptSizeReport(AgentLoopConfig{}, "sys", hist)
	if !strings.Contains(r, "#1") || !strings.Contains(r, "assistant") {
		t.Errorf("the report does not identify the offending message: %s", r)
	}
	if !strings.Contains(r, "tool results") {
		t.Errorf("the report does not separate tool results from message text: %s", r)
	}
}

// --- the recovery ladder -----------------------------------------------------

// summarizeOldHistory has to CHUNK, because the thing that does not fit cannot
// be handed to a summarizer in one piece either. 1.4M tokens of history is not
// summarizable by a model with a 262k window, so a single fold call would fail
// exactly where recovery is needed most.
func TestSummarizationChunksOversizedHistory(t *testing.T) {
	app, stub := withFoldStub(t)

	// Twelve messages of ~40k tokens each, well past a 262k window.
	var msgs []Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, Message{Role: "user", Content: strings.Repeat("word ", 32000)})
	}
	msgs = append(msgs, Message{Role: "user", Content: "the actual question"})

	out, ok := app.summarizeOldHistory(t.Context(), msgs, 262144, contextRecoveryKeepWhole)
	if !ok {
		t.Fatal("summarization declined to run on history far past the window")
	}
	if stub.Calls() < 2 {
		t.Errorf("history %dx larger than a fold budget was summarized in %d call(s) — it was not chunked",
			len(msgs), stub.Calls())
	}
	// Every fold input must itself fit, or chunking achieved nothing.
	for _, n := range foldInputTokens(stub) {
		if n > 262144 {
			t.Errorf("a fold call was handed %d tokens, more than the window", n)
		}
	}
	// The newest messages survive verbatim: the round is about those.
	if out[len(out)-1].Content != "the actual question" {
		t.Fatal("the message the turn is about was folded away")
	}
	if len(out) >= len(msgs) {
		t.Errorf("history did not shrink: %d → %d", len(msgs), len(out))
	}
	// And the fold is labelled, so the model does not read it as something the
	// user just said.
	if !strings.Contains(out[0].Content, "summarized") {
		t.Errorf("the summary is not marked as one: %q", out[0].Content[:80])
	}
}

// Summarization is tried BEFORE anything is thrown away, and elision is the
// floor for when it cannot run at all — no LLM, or a failing one.
func TestElisionIsTheFallbackNotTheFirstResort(t *testing.T) {
	// No LLM configured: summarization declines, elision still saves the turn.
	var noLLM AppCore
	msgs := []Message{
		{Role: "user", Content: strings.Repeat("a", 200000)},
		{Role: "user", Content: strings.Repeat("b", 200000)},
		{Role: "user", Content: "recent"},
	}
	if _, ok := noLLM.summarizeOldHistory(t.Context(), msgs, 262144, 1); ok {
		t.Fatal("summarization claimed to run with no model configured")
	}
	if n := elideOldMessageText(msgs, 1000, 1); n == 0 {
		t.Fatal("elision reclaimed nothing, so a turn with no summarizer stays dead")
	}
	if msgs[2].Content != "recent" {
		t.Error("elision touched the newest message")
	}
	if !strings.Contains(msgs[0].Content, "elided") {
		t.Error("an elided message does not say it was elided")
	}
}

// Structure has to survive: only Content is replaced, so tool calls and their
// results stay paired and the transcript keeps its shape.
func TestElisionPreservesToolPairing(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Content: strings.Repeat("x", 100000),
			ToolResults: []ToolResult{{ID: "call-1", Content: "result body"}}},
		{Role: "user", Content: "now"},
	}
	elideOldMessageText(msgs, 10, 1)
	if len(msgs[0].ToolResults) != 1 || msgs[0].ToolResults[0].ID != "call-1" {
		t.Fatal("elision broke the tool-call pairing")
	}
}

// --- stub -------------------------------------------------------------------

func withFoldStub(t *testing.T) (*AppCore, *FakeLLM) {
	t.Helper()
	stub := &FakeLLM{Turns: []FakeTurn{{Content: "notes about that span", Repeat: true}}}
	return &AppCore{LLM: stub}, stub
}

// foldInputTokens is what each fold call was handed, estimated the way the
// budget does. It used to be computed inside the stub, where the test could
// not see the rule it was being judged by; the fake records the messages, so
// the sum belongs here with the assertion that reads it.
func foldInputTokens(stub *FakeLLM) []int {
	var out []int
	for i := 0; i < stub.Calls(); i++ {
		n := 0
		for _, m := range stub.Sent(i) {
			n += EstimateTokens(m.Content)
		}
		out = append(out, n)
	}
	return out
}

// --- recovering without a configured window ----------------------------------

// The recovery ladder used to be gated on cfg.ContextSize, which is routinely
// ZERO: it comes from an optional ContextSizer a provider may not implement,
// and every size-dependent path documents itself as disabling when it is. That
// meant recovery switched itself off precisely when the provider had just said
// the prompt was too large — the reason three attempts at this bug changed
// nothing on a live deployment.
func TestTheWindowIsReadFromTheProvidersRefusal(t *testing.T) {
	// The exact message from the live failure.
	err := errors.New("llama.cpp api error (400): request (2135903 tokens) exceeds the available context size (262144 tokens), try increasing it")
	if got := contextWindowFromError(err); got != 262144 {
		t.Fatalf("read %d from the refusal, want 262144 (the window, not the prompt)", got)
	}
	// The SMALLER of the two figures is the window; the larger is the prompt
	// that did not fit. Getting that backwards would recover into a window
	// bigger than the real one and achieve nothing.
	err = errors.New("prompt is 500000 tokens, limit is 128000 tokens")
	if got := contextWindowFromError(err); got != 128000 {
		t.Fatalf("read %d, want the smaller figure 128000", got)
	}
	// Nothing recognizable is not a crash and not a wrong guess.
	if got := contextWindowFromError(errors.New("something went wrong")); got != 0 {
		t.Fatalf("invented a window of %d from a message with no numbers", got)
	}
	if got := contextWindowFromError(nil); got != 0 {
		t.Fatalf("invented a window of %d from no error", got)
	}
	// Small incidental numbers are not windows.
	if got := contextWindowFromError(errors.New("error 400 after 12 tokens")); got != 0 {
		t.Fatalf("mistook %d for a context window", got)
	}
}

// With no window from anywhere, recovery must still act — assuming a small one
// rather than declining. Folding more history than strictly necessary costs
// some context; declining costs the turn.
func TestUnknownWindowStillRecovers(t *testing.T) {
	if fallbackRecoveryWindow <= 0 {
		t.Fatal("the fallback window disables recovery")
	}
	// stillTooBig must report TRUE for a large history against the fallback,
	// or the ladder is skipped anyway.
	msgs := []Message{{Role: "user", Content: strings.Repeat("x", 4<<20)}}
	if !stillTooBig(msgs, "", 0, fallbackRecoveryWindow) {
		t.Fatal("a 4 MiB history is not considered too big for the fallback window")
	}
}

// --- not breaking the transcript while saving it -----------------------------

// A message carrying ToolResults renders as role:"tool", and a tool message is
// only valid immediately after the assistant holding the matching tool_calls.
// Folding between the two makes the model's own chat template reject the whole
// request — llama.cpp answers "A tool message must follow an assistant or tool
// message" with a 500, turning context recovery into a worse failure than the
// one it was recovering from.
func TestFoldingNeverOrphansAToolMessage(t *testing.T) {
	app, _ := withFoldStub(t)

	// A conversation whose tool pair straddles the natural fold boundary.
	msgs := []Message{
		{Role: "user", Content: strings.Repeat("old ", 50000)},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "search"}}},
		{Role: "assistant", ToolResults: []ToolResult{{ID: "c1", Content: "results"}}},
		{Role: "assistant", Content: "here is what I found"},
		{Role: "user", Content: "thanks"},
	}
	// keepWhole chosen so the naive boundary lands ON the tool-result message.
	out, ok := app.summarizeOldHistory(t.Context(), msgs, 262144, 3)
	if !ok {
		t.Fatal("summarization declined")
	}
	assertNoOrphanedToolMessages(t, out)
}

// The boundary rule on its own, across every position it could land.
func TestSafeCutPointNeverSplitsAPair(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1"}}},
		{Role: "assistant", ToolResults: []ToolResult{{ID: "c1"}}},
		{Role: "assistant", ToolResults: []ToolResult{{ID: "c2"}}},
		{Role: "user", Content: "b"},
	}
	for idx := 0; idx <= len(msgs); idx++ {
		cut := safeCutPoint(msgs, idx)
		if cut < len(msgs) && len(msgs[cut].ToolResults) > 0 {
			t.Errorf("cut at %d landed on a tool message (index %d)", idx, cut)
		}
		if cut < idx {
			t.Errorf("cut at %d moved backwards to %d, keeping a call whose result was folded", idx, cut)
		}
	}
}

// Elision must leave the transcript's shape alone: it replaces Content only,
// so calls and results stay paired.
func TestElisionNeverOrphansAToolMessage(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Content: strings.Repeat("x", 50000), ToolCalls: []ToolCall{{ID: "c1"}}},
		{Role: "assistant", ToolResults: []ToolResult{{ID: "c1", Content: "r"}}},
		{Role: "user", Content: "now"},
	}
	elideOldMessageText(msgs, 1, 1)
	assertNoOrphanedToolMessages(t, msgs)
	if len(msgs[0].ToolCalls) != 1 {
		t.Error("elision dropped the assistant's tool calls, orphaning its result")
	}
}

// assertNoOrphanedToolMessages checks the invariant the chat template enforces.
func assertNoOrphanedToolMessages(t *testing.T, msgs []Message) {
	t.Helper()
	for i := range msgs {
		if len(msgs[i].ToolResults) == 0 {
			continue
		}
		if i == 0 {
			t.Fatalf("message %d carries tool results with nothing before it", i)
		}
		prev := msgs[i-1]
		if len(prev.ToolCalls) == 0 && len(prev.ToolResults) == 0 {
			t.Fatalf("message %d carries tool results but follows a %q message with no tool calls — the template rejects this",
				i, prev.Role)
		}
	}
}

// --- keeping the salvage small -----------------------------------------------

// A fold runs on a server that is by definition under pressure — the turn got
// here because something did not fit. Asking for quarter-window bites meant
// the recovery competed for KV cache with the conversation it was rescuing,
// seen on llama.cpp as slots being purged mid-flight to place the request.
func TestFoldRequestsStaySmall(t *testing.T) {
	app, stub := withFoldStub(t)

	var msgs []Message
	for i := 0; i < 30; i++ {
		msgs = append(msgs, Message{Role: "user", Content: strings.Repeat("word ", 20000)})
	}
	msgs = append(msgs, Message{Role: "user", Content: "the question"})

	if _, ok := app.summarizeOldHistory(t.Context(), msgs, 262144, contextRecoveryKeepWhole); !ok {
		t.Fatal("summarization declined")
	}
	for i, n := range foldInputTokens(stub) {
		// A generous ceiling over the chunk budget: one oversized message is
		// folded alone and may exceed it, but nothing should approach the
		// quarter-window bite this replaced.
		if n > foldChunkTokens*3 {
			t.Errorf("fold call %d asked for %d tokens; the budget is %d and a loaded server cannot place requests that size",
				i, n, foldChunkTokens)
		}
	}
}

// An unbounded fold count turns one failed turn into an arbitrarily long queue
// of LLM calls — a poor trade against simply dropping old text, and on a
// loaded server a queue nobody asked for.
func TestFoldCallsAreBounded(t *testing.T) {
	app, stub := withFoldStub(t)

	// Far more history than the cap can fold.
	var msgs []Message
	for i := 0; i < 400; i++ {
		msgs = append(msgs, Message{Role: "user", Content: strings.Repeat("word ", 4000)})
	}
	msgs = append(msgs, Message{Role: "user", Content: "the question"})

	app.summarizeOldHistory(t.Context(), msgs, 262144, contextRecoveryKeepWhole)

	// maxFoldCalls chunk folds, plus at most one fold-of-folds.
	if stub.Calls() > maxFoldCalls+1 {
		t.Fatalf("made %d fold calls; the cap is %d", stub.Calls(), maxFoldCalls)
	}
	if stub.Calls() == 0 {
		t.Fatal("made no fold calls at all")
	}
}

// A result a later call made stale leaves the history: a file read before it
// was edited, the same read made twice. Fresh reads, other files, and results
// too small to matter stay, and the caller's history is never changed.
func TestStaleToolResultsLeaveTheHistory(t *testing.T) {
	big := strings.Repeat("x", 20000)
	touches := map[string]func(map[string]any) (string, bool){
		"ws": func(a map[string]any) (string, bool) {
			p, _ := a["path"].(string)
			if a["action"] == "edit" {
				return "file|" + p, true
			}
			return "file|" + p + "|cat", false
		},
	}
	call := func(id, action, path string) Message {
		return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "ws", Args: map[string]any{"action": action, "path": path}}}}
	}
	result := func(id, body string) Message {
		return Message{Role: "user", ToolResults: []ToolResult{{ID: id, Content: body}}}
	}
	orig := []Message{
		call("c1", "cat", "page.html"), result("c1", big), // edited later: stale
		call("c2", "cat", "notes.md"), result("c2", big), // read again later: superseded
		call("c3", "cat", "other.py"), result("c3", big), // untouched since: stays
		call("c4", "edit", "page.html"), result("c4", "edited"),
		call("c5", "cat", "notes.md"), result("c5", big), // the newest read: stays
		call("c6", "cat", "page.html"), result("c6", big), // read after the edit: stays
	}
	msgs := append([]Message(nil), orig...)
	if n := dropSupersededResults(msgs, touches); n < 8000 {
		t.Fatalf("reclaimed %d tokens, want two 20k results' worth", n)
	}
	for i, want := range map[int]string{1: "changed after", 3: "made again later"} {
		if !strings.Contains(msgs[i].ToolResults[0].Content, want) {
			t.Errorf("message %d: %.80q, want it dropped (%s)", i, msgs[i].ToolResults[0].Content, want)
		}
	}
	for _, i := range []int{5, 9, 11} {
		if msgs[i].ToolResults[0].Content != big {
			t.Errorf("message %d was dropped but nothing made it stale", i)
		}
	}
	if orig[1].ToolResults[0].Content != big {
		t.Error("the caller's history was changed")
	}

	// Too little to be worth a cold prefill: nothing changes.
	small := []Message{call("a", "cat", "x"), result("a", strings.Repeat("y", 2000)), call("b", "edit", "x"), result("b", "ok")}
	if n := dropSupersededResults(small, touches); n != 0 || small[1].ToolResults[0].Content == "" || strings.HasPrefix(small[1].ToolResults[0].Content, "[earlier") {
		t.Errorf("a %d-token saving acted", n)
	}
}

// The estimate counts four bytes a token, and the provider's count ran a
// quarter higher on code and logs: a prompt put at 165k was over 208k.
// Every reply's real count corrects it, per configured window, rising fast,
// falling slowly, never below 1 (so a count that leaves cached tokens out
// cannot loosen compaction), never above 2; compaction's window shrinks by
// it. A refusal's stated size counts too.
func TestTheEstimateIsCorrectedByWhatTheProviderCounts(t *testing.T) {
	const w = 204801 // a window no other test uses: the ratios are shared
	if tokenRatio(w) != 1 || calibratedWindow(100000, w) != 100000 {
		t.Fatal("nothing measured is no correction")
	}
	noteTokenRatio(w, 165510, 208401)
	r := tokenRatio(w)
	if r < 1.1 || r > 1.26 {
		t.Fatalf("one measurement moves it most of the way up: %.3f", r)
	}
	if got := calibratedWindow(204800, w); got >= 204800*10/11 {
		t.Errorf("the window shrinks by it: %d", got)
	}
	noteTokenRatio(w, 165510, 208401)
	up := tokenRatio(w)
	noteTokenRatio(w, 100000, 100000)
	if down := tokenRatio(w); down >= up || down < up-0.05 {
		t.Errorf("it falls slowly: %.3f -> %.3f", up, down)
	}
	noteTokenRatio(w, 100000, 10) // a count that leaves cache out
	if tokenRatio(w) < 1 {
		t.Error("never below 1")
	}
	const big = 204802
	for i := 0; i < 20; i++ {
		noteTokenRatio(big, 10000, 90000)
	}
	if tokenRatio(big) > 2 {
		t.Error("never above 2")
	}
	const small = 204803
	noteTokenRatio(small, 500, 2000)
	if tokenRatio(small) != 1 {
		t.Error("a small prompt says too little to learn from")
	}
	err := errors.New("vllm api error (400): This model's maximum context length is 208400 tokens. However, you requested 0 output tokens and your prompt contains at least 208401 input tokens, for a total of at least 208401 tokens.")
	if n := promptTokensInRefusal(err); n != 208401 {
		t.Errorf("the refusal's prompt size: %d", n)
	}
}

// One round that read three whole files put the next past the window before
// it ran. Each result is held to a share of the window as it arrives:
// kept whole in the paged store and read on with read_output when the loop
// has it, its end kept with a note when it does not. The same cap holds on
// every round, because a later turn rebuilds its history with each result
// whole, and it holds still: a capped result is not cut again.
func TestAResultIsHeldToItsShareOfTheWindow(t *testing.T) {
	file := strings.Repeat("line of a large file\n", 12000) // ~250 KB
	capBytes := ingestCapBytes(160000)                      // ~96 KB
	if capBytes < 90000 || capBytes > 100000 {
		t.Fatalf("15%% of the window: %d", capBytes)
	}
	if ingestCapBytes(10000) != ingestFloorBytes || ingestCapBytes(0) != 0 {
		t.Error("floored, and off when the window is unknown")
	}

	paged := []ToolResult{{ID: "a", Content: file}, {ID: "b", Content: "small"}}
	if n := capIngestedResults(paged, capBytes, true); n != 1 {
		t.Fatalf("capped %d", n)
	}
	if !strings.Contains(paged[0].Content, "output_id=") || len(paged[0].Content) > capBytes+capNoteSlack || paged[1].Content != "small" {
		t.Errorf("a large result arrives as a window with an id to read on: %d bytes", len(paged[0].Content))
	}
	plain := []ToolResult{{ID: "a", Content: file}}
	capIngestedResults(plain, capBytes, false)
	if !strings.Contains(plain[0].Content, "truncated as it arrived") || !strings.HasSuffix(plain[0].Content, "line of a large file\n") {
		t.Errorf("without read_output its end is kept with a note: %.100s", plain[0].Content)
	}

	stored := []Message{{Role: "user", ToolResults: []ToolResult{{ID: "a", Content: file}}}, {Role: "user", ToolResults: paged}}
	if n := capStoredResults(stored, capBytes); n != 1 {
		t.Fatalf("a whole stored result is cut, a capped one left: cut %d", n)
	}
	once := stored[0].ToolResults[0].Content
	if capStoredResults(stored, capBytes) != 0 || stored[0].ToolResults[0].Content != once {
		t.Error("cutting is stable: a cut result is not cut again")
	}

	// compactHistory holds it on every round, even under budget.
	msgs := []Message{{Role: "user", Content: "read these"}, {Role: "user", ToolResults: []ToolResult{{ID: "x", Content: file}}}}
	compactHistory(msgs, "", 1000, 160000, false)
	if len(msgs[1].ToolResults[0].Content) > capBytes+capNoteSlack {
		t.Errorf("a round starts with every result within its share: %d bytes", len(msgs[1].ToolResults[0].Content))
	}
}

// argCall is an assistant message whose one tool call sent body as an
// argument, and the tool's short reply.
func argCall(id, body string) []Message {
	return []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "app_def", Args: map[string]any{"action": "update", "sections": []any{map[string]any{"html": body}}}}}},
		{Role: "user", ToolResults: []ToolResult{{ID: id, Content: "Updated."}}},
	}
}

// Every earlier call goes back to the model with its arguments whole, and on
// an authoring thread that is where the weight is: a page or a file sent as
// one argument. The estimate counted none of it, so a thread far past the
// provider's window read as fitting, and nothing could shrink it. It is
// counted now, and compaction cuts the bulk of older calls' arguments, the
// newest kept whole and the stored turn untouched.
func TestOldToolCallArgumentsAreCountedAndCut(t *testing.T) {
	page := strings.Repeat("<div>row</div>", 4000) // ~56k bytes, ~14k tokens
	var msgs []Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, argCall(string(rune('a'+i)), page)...)
	}
	before := EstimateMessagesTokens(msgs)
	if before < 6*14000 || before > 6*15000 {
		t.Fatalf("the arguments are counted, as sent (no \\u003c escapes): estimate %d", before)
	}
	if est := estimatePromptTokens(msgs, ""); est < 6*14000 {
		t.Fatalf("the prompt estimate counts them too: %d", est)
	}
	stored := msgs[0].ToolCalls[0].Args // the map the stored turn holds

	compactHistory(msgs, "", 1000, 120000, false)
	// The newest four calls are kept whole; the older two are cut.
	if after := EstimateMessagesTokens(msgs); after > before-2*13000 {
		t.Fatalf("compaction cut the older arguments: %d -> %d", before, after)
	}
	first, _ := msgs[0].ToolCalls[0].Args["sections"].([]any)
	if html, _ := first[0].(map[string]any)["html"].(string); !strings.Contains(html, "elided to fit context") {
		t.Errorf("an old call's page is replaced by a marker: %.80s", html)
	}
	if msgs[0].ToolCalls[0].Name != "app_def" || msgs[0].ToolCalls[0].Args["action"] != "update" {
		t.Error("the call keeps its name and short arguments")
	}
	last := msgs[len(msgs)-2].ToolCalls[0].Args["sections"].([]any)[0].(map[string]any)["html"].(string)
	if last != page {
		t.Error("the newest call is kept whole")
	}
	if s := stored["sections"].([]any)[0].(map[string]any)["html"].(string); s != page {
		t.Error("the stored turn's arguments were changed")
	}
}

// The recovery's last rung cuts old arguments before what was said.
func TestRecoveryCutsArgumentsBeforeText(t *testing.T) {
	page := strings.Repeat("x", 40000)
	msgs := []Message{{Role: "user", Content: strings.Repeat("words ", 200)}}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, argCall(string(rune('a'+i)), page)...)
	}
	elideOldMessageText(msgs, 25000, 2)
	if !strings.Contains(msgs[1].ToolCalls[0].Args["sections"].([]any)[0].(map[string]any)["html"].(string), "elided") {
		t.Error("an old call's argument is cut")
	}
	if strings.Contains(msgs[0].Content, "elided") {
		t.Error("what was said is kept while cutting arguments is enough")
	}
}

// A call's arguments go to the model as written: an HTML page sent as an
// argument is not turned into \u003c escapes, which made it two to three
// times its size on every later round.
func TestToolArgumentsAreSentWithoutHTMLEscapes(t *testing.T) {
	got := toolArgsJSON(map[string]any{"html": "<div class=\"a\">x & y</div>"})
	if strings.Contains(got, "\\u003c") || !strings.Contains(got, "<div") || !strings.Contains(got, "x & y") {
		t.Errorf("got %s", got)
	}
	if toolArgsJSON(nil) != "null" && toolArgsJSON(nil) != "{}" {
		t.Errorf("no arguments: %q", toolArgsJSON(nil))
	}
}

// The newest messages can be the bulk: a few whole files read the round
// before. Every recovery rung spares them, so a retry re-sent most of what
// was refused (25 of 31 messages folded, still 770 KB). The last rung cuts
// the largest bodies anywhere, tool results first, the newest included,
// keeping each one's end with a note, until it fits; what was said is cut
// only when nothing else is left.
func TestRecoveryShrinksTheNewestWhenTheyAreTheBulk(t *testing.T) {
	file := strings.Repeat("line of a large file\n", 12000) // ~250 KB
	ask := "Please look at these and tell me what is wrong. " + strings.Repeat("context ", 1500)
	msgs := []Message{
		{Role: "user", Content: ask},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "read_file"}, {ID: "b", Name: "read_file"}, {ID: "c", Name: "read_file"}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "a", Content: file}, {ID: "b", Content: file}, {ID: "c", Content: file}}},
	}
	budget := 40000
	if EstimateMessagesTokens(msgs) <= budget {
		t.Fatal("the fixture must start over budget")
	}
	if n := shrinkLargestBodies(msgs, budget); n <= 0 {
		t.Fatal("nothing was cut")
	}
	if got := EstimateMessagesTokens(msgs); got > budget {
		t.Fatalf("still over: %d > %d", got, budget)
	}
	for _, tr := range msgs[2].ToolResults {
		if !strings.Contains(tr.Content, "cut to fit context") || !strings.HasSuffix(tr.Content, "line of a large file\n") {
			t.Errorf("each result keeps its end with a note: %.120s", tr.Content)
		}
	}
	if msgs[0].Content != ask {
		t.Error("what was said is kept while cutting results is enough")
	}

	// Nothing left above the floor: it stops rather than loop.
	small := []Message{{Role: "user", Content: "hi"}}
	if n := shrinkLargestBodies(small, 0); n != 0 {
		t.Errorf("nothing to cut, cut %d", n)
	}
}
