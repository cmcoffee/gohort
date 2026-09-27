package core

import (
	"encoding/json"
	"testing"
)

// Gemini left Response.StopReason EMPTY on every response while OpenAI and
// Anthropic both populated it. Empty is not neutral — the agent loop reads it:
//
//	if resp.StopReason == "stop" && len(resp.Content) >= cleanFinishProseFloor
//
// so the prose tool-call scan that is meant to be SKIPPED after a clean finish
// always ran on Gemini output and could cut a turn short. The local
// OpenAI-compatible model got the skip and kept going; Gemini "just stopped."
func TestGeminiStopReasonMapsCleanFinish(t *testing.T) {
	if got := geminiStopReason("STOP"); got != "stop" {
		t.Errorf("STOP mapped to %q, want \"stop\" — the clean-finish gate compares against exactly this", got)
	}
	if got := geminiStopReason("stop"); got != "stop" {
		t.Errorf("lowercase stop mapped to %q", got)
	}
	if got := geminiStopReason("  STOP  "); got != "stop" {
		t.Errorf("padded STOP mapped to %q", got)
	}
}

// An absent finishReason must not fall back to "" — that is the exact value
// that silently disabled the gate.
func TestGeminiStopReasonNeverReturnsEmpty(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if got := geminiStopReason(in); got == "" {
			t.Errorf("geminiStopReason(%q) returned empty — the value that broke the gate", in)
		}
	}
}

func TestGeminiStopReasonMapsLength(t *testing.T) {
	if got := geminiStopReason("MAX_TOKENS"); got != "length" {
		t.Errorf("MAX_TOKENS mapped to %q, want \"length\"", got)
	}
}

// A filtered response must remain distinguishable from a finished one, so the
// caller can tell "blocked" from "done".
func TestGeminiStopReasonPreservesFilterReasons(t *testing.T) {
	for _, in := range []string{"SAFETY", "RECITATION", "BLOCKLIST", "OTHER"} {
		got := geminiStopReason(in)
		if got == "stop" || got == "length" {
			t.Errorf("%s was flattened to %q — a blocked response would read as a clean finish", in, got)
		}
		if got == "" {
			t.Errorf("%s mapped to empty", in)
		}
	}
}

// Gemini's cached share of a prompt is reported apart from the rest, the way
// Anthropic reports it, so it is priced as a cache read and a turn's lead
// budget counts only what was actually processed.
func TestGeminiReportsItsCachedPromptApart(t *testing.T) {
	var r gemResponse
	if err := json.Unmarshal([]byte(`{"usageMetadata":{"promptTokenCount":63000,"cachedContentTokenCount":58000,"candidatesTokenCount":90}}`), &r); err != nil {
		t.Fatal(err)
	}
	if got := geminiCached(r.UsageMetadata.PromptTokenCount, r.UsageMetadata.CachedContentTokenCount); got != 58000 {
		t.Errorf("cached share: got %d, want 58000", got)
	}
	if geminiCached(100, 500) != 100 || geminiCached(0, 5) != 0 || geminiCached(100, -1) != 0 {
		t.Error("the cached share never exceeds the prompt or goes negative")
	}
}
