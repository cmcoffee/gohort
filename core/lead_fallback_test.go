package core

import "testing"

// A remote provider refusing on ITS content policy says nothing about what
// this deployment permits — the local worker has no such policy and can just
// do the work. Detected from the stop reason, which every client now
// populates (Gemini's did not until recently, which is part of why a refusal
// looked like a dead turn).
func TestProviderRefusedDetectsPolicyStops(t *testing.T) {
	for _, reason := range []string{"safety", "SAFETY", "recitation", "blocklist", "content_filter", "prohibited_content"} {
		if !providerRefused(&Response{StopReason: reason}) {
			t.Errorf("stop reason %q not recognized as a provider refusal", reason)
		}
	}
}

// A model that ANSWERED has not refused, even if a filter tripped afterward.
// Treating that as a refusal would re-run finished work on the worker and
// double the cost of every filtered-but-complete reply.
func TestProviderRefusedIgnoresAnsweredTurns(t *testing.T) {
	if providerRefused(&Response{StopReason: "safety", Content: "here is the answer"}) {
		t.Error("a response WITH content read as a refusal")
	}
	if providerRefused(&Response{StopReason: "safety", ToolCalls: []ToolCall{{Name: "x"}}}) {
		t.Error("a response WITH tool calls read as a refusal")
	}
}

// Ordinary completions must never look like refusals, or every turn would run
// twice.
func TestProviderRefusedIgnoresNormalStops(t *testing.T) {
	for _, reason := range []string{"stop", "length", "tool_calls", "", "end_turn"} {
		if providerRefused(&Response{StopReason: reason}) {
			t.Errorf("normal stop reason %q read as a refusal", reason)
		}
	}
	if providerRefused(nil) {
		t.Error("nil response read as a refusal")
	}
}
