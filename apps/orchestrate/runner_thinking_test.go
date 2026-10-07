package orchestrate

import (
	"bytes"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// The model's reasoning becomes progress a person can watch: one event the
// moment thinking starts, then one per tick rather than one per chunk, since
// the run buffer replays every event to a page that rejoins.
func TestThinkingReportsOnATick(t *testing.T) {
	buf := &bytes.Buffer{}
	turn := &chatTurn{sse: &sseWriter{live: buf}}

	turn.thinkChunk(strings.Repeat("x", 400))
	if n := strings.Count(buf.String(), `"kind":"thinking"`); n != 1 {
		t.Fatalf("first chunk sent %d progress events, want 1 at once", n)
	}
	if !strings.Contains(buf.String(), `"tokens":100`) {
		t.Errorf("progress %q, want ~100 tokens for 400 chars", buf.String())
	}
	for i := 0; i < 50; i++ {
		turn.thinkChunk("more reasoning ")
	}
	if n := strings.Count(buf.String(), `"kind":"thinking"`); n != 1 {
		t.Errorf("a burst of chunks inside one tick sent %d events, want still 1", n)
	}
	// The reasoning itself never goes out, only that it is happening.
	if strings.Contains(buf.String(), "more reasoning") {
		t.Error("the reasoning text reached the client")
	}

	// A tick later the next chunk reports again.
	turn.think.mu.Lock()
	turn.think.lastSent = time.Now().Add(-thinkTick)
	turn.think.mu.Unlock()
	turn.thinkChunk("x")
	if n := strings.Count(buf.String(), `"kind":"thinking"`); n != 2 {
		t.Errorf("after a tick: %d events, want 2", n)
	}
}

// The footer's thinking time adds up each call's own span, so the tools that
// run between two calls are not counted as thinking.
func TestThinkingTimeSumsEachCall(t *testing.T) {
	turn := &chatTurn{}
	if turn.thinkTotalMS() != 0 {
		t.Fatal("no reasoning yet, but thinking time is not zero")
	}
	base := time.Now()
	turn.think.spanStart, turn.think.last = base, base.Add(3*time.Second)
	turn.thinkNewCall() // a tool ran here, then round two
	turn.think.spanStart, turn.think.last = base.Add(60*time.Second), base.Add(62*time.Second)
	if got := turn.thinkTotalMS(); got != 5000 {
		t.Errorf("thinking time %dms, want 5000 (3s + 2s, not the minute between)", got)
	}
	// Closing a span with none open changes nothing.
	turn.thinkNewCall()
	turn.thinkNewCall()
	if got := turn.thinkTotalMS(); got != 5000 {
		t.Errorf("after idle round starts: %dms, want 5000", got)
	}
}

// The footer's thinking and output tokens are the whole turn's. A turn that
// thinks, calls a tool and thinks again used to show only its last call's.
func TestStatsFooterTotalsEveryCall(t *testing.T) {
	buf := &bytes.Buffer{}
	turn := &chatTurn{sse: &sseWriter{live: buf}}
	turn.thinkResponse(&Response{OutputTokens: 900, ReasoningTokens: 800}) // thinks, calls a tool
	turn.thinkResponse(&Response{OutputTokens: 300, ReasoningTokens: 250}) // thinks again, calls another
	final := &Response{OutputTokens: 120, ReasoningTokens: 40, InputTokens: 5000}
	turn.thinkResponse(final)
	turn.emitStats("m1", final, time.Now())

	u := turn.drainLastUsage()
	if u == nil {
		t.Fatal("no usage recorded")
	}
	if u.ReasoningTokens != 1090 || u.OutputTokens != 1320 {
		t.Errorf("recorded think=%d out=%d, want 1090 and 1320 (every call, not the last)", u.ReasoningTokens, u.OutputTokens)
	}
	if u.InputTokens != 5000 {
		t.Errorf("prompt %d, want the last call's 5000", u.InputTokens)
	}
	if !strings.Contains(buf.String(), `"reasoning_tokens":1090`) {
		t.Errorf("live footer %q, want the turn's 1090 thinking tokens", buf.String())
	}
}

// With no call counted, the footer falls back to the response it was handed.
func TestStatsFooterFallsBackToResponse(t *testing.T) {
	turn := &chatTurn{sse: &sseWriter{live: &bytes.Buffer{}}}
	turn.emitStats("m1", &Response{OutputTokens: 70, ReasoningTokens: 30}, time.Now())
	if u := turn.drainLastUsage(); u == nil || u.ReasoningTokens != 30 || u.OutputTokens != 70 {
		t.Errorf("usage %+v, want the response's own 70 out / 30 think", u)
	}
}
