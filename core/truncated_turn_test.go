package core

import "testing"

// The reported turn: a lead spent its whole output allowance thinking, emitted
// 133 characters and no tool call, and the loop read that as a finished turn —
// exit=respond_directly at rounds_used=1/30, twice in a row, on a promise.
//
// stop_reason said so both times. Nothing was listening: providerRefused covers
// safety reasons only, and llm_openai's finish_reason==length check requires
// content AND tool calls to both be empty, which is exactly what a truncated
// turn is not.
func TestATruncatedReplyIsNotAFinishedOne(t *testing.T) {
	for _, stop := range []string{"max_tokens", "length", "MAX_TOKENS", " length "} {
		resp := &Response{Content: "No — I said I was writing it. Doing it now.", StopReason: stop}
		if !responseWasTruncated(resp) {
			t.Errorf("stop_reason=%q did not read as truncated", stop)
		}
	}
}

// The distinction that makes it worth having: a truncated turn normally HAS
// content. A guard that fires only on an empty response misses the case that
// actually strands the user.
func TestTruncationIsDetectedWithContentPresent(t *testing.T) {
	withContent := &Response{Content: "Doing it now.", StopReason: "max_tokens"}
	if !responseWasTruncated(withContent) {
		t.Error("a truncated reply that carries a preamble was not detected")
	}
}

// A clean finish must not be re-prompted — that would spend a round and an LLM
// call on every normal turn in the system.
func TestACleanFinishIsLeftAlone(t *testing.T) {
	for _, stop := range []string{"stop", "end_turn", "tool_use", "", "pause_turn"} {
		if responseWasTruncated(&Response{Content: "Here you go.", StopReason: stop}) {
			t.Errorf("stop_reason=%q was misread as truncated", stop)
		}
	}
	if responseWasTruncated(nil) {
		t.Error("a nil response read as truncated")
	}
}

// Truncation and refusal are different terminal conditions and must not both
// claim the same response.
func TestTruncationAndRefusalDoNotOverlap(t *testing.T) {
	refusal := &Response{StopReason: "safety"}
	if responseWasTruncated(refusal) {
		t.Error("a safety stop read as truncated")
	}
	if !providerRefused(refusal) {
		t.Error("the refusal guard stopped recognizing its own case")
	}
	truncated := &Response{Content: "partial", StopReason: "max_tokens"}
	if providerRefused(truncated) {
		t.Error("a truncated reply read as a provider refusal")
	}
}
