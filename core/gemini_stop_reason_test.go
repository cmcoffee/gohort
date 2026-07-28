package core

import "testing"

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
