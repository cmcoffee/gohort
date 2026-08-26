package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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

	compactHistory(msgs, "system", window, true)

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
	compactHistory(msgs, "system", window, true)
	if len(msgs[0].ToolResults[0].Content) >= 4<<20 {
		t.Fatal("a lone oversized result was left untouched, so the session cannot recover")
	}
}

// The tail is kept, not the head: a command's error is at the END of its
// output, and that is overwhelmingly what an oversized result is.
func TestTruncationKeepsTheEndWhereTheErrorIs(t *testing.T) {
	body := strings.Repeat("noise\n", 200000) + "FATAL: the actual error\n"
	msgs := []Message{{Role: "assistant", ToolResults: []ToolResult{{Content: body}}}}
	compactHistory(msgs, "system", 262144, true)
	if !strings.Contains(msgs[0].ToolResults[0].Content, "FATAL: the actual error") {
		t.Fatal("truncation discarded the end of the output, which is where the error lives")
	}
}

// Ordinary results must be left completely alone — a large file read has to
// keep working, and this only exists for bodies that could not be used anyway.
func TestOrdinaryResultsAreUntouched(t *testing.T) {
	body := strings.Repeat("z", 64<<10) // 64 KiB — big, but usable
	msgs := []Message{{Role: "assistant", ToolResults: []ToolResult{{Content: body}}}}
	compactHistory(msgs, "system", 262144, true)
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
	if stub.calls < 2 {
		t.Errorf("history %dx larger than a fold budget was summarized in %d call(s) — it was not chunked",
			len(msgs), stub.calls)
	}
	// Every fold input must itself fit, or chunking achieved nothing.
	for _, n := range stub.inputTokens {
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

type foldStub struct {
	calls       int
	inputTokens []int
}

func (f *foldStub) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	f.calls++
	n := 0
	for _, msg := range m {
		n += EstimateTokens(msg.Content)
	}
	f.inputTokens = append(f.inputTokens, n)
	return &Response{Content: "notes about that span"}, nil
}
func (f *foldStub) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return f.Chat(ctx, m, o...)
}

func withFoldStub(t *testing.T) (*AppCore, *foldStub) {
	t.Helper()
	stub := &foldStub{}
	return &AppCore{LLM: stub}, stub
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
	if !stillTooBig(msgs, "", fallbackRecoveryWindow) {
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
	for i, n := range stub.inputTokens {
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
	if stub.calls > maxFoldCalls+1 {
		t.Fatalf("made %d fold calls; the cap is %d", stub.calls, maxFoldCalls)
	}
	if stub.calls == 0 {
		t.Fatal("made no fold calls at all")
	}
}
