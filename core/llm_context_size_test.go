package core

// The Anthropic path's context cap.
//
// Before these clients implemented ContextSizer, LeadContextSize() silently
// fell back to the WORKER's window — lead agent loops compacted against the
// local llama.cpp num_ctx — and with no sized worker it returned 0, which
// disables compaction entirely. The cap defaults well under the API's 1M
// window on purpose: it bounds per-turn input cost, not what the API accepts.

import "testing"

func TestAnthropicContextSizeDefaultsToWorkingCap(t *testing.T) {
	c := &anthropicClient{}
	if got := c.ContextSize(); got != anthropicDefaultContextSize {
		t.Errorf("default = %d, want %d", got, anthropicDefaultContextSize)
	}
	c.contextSize = 500_000
	if got := c.ContextSize(); got != 500_000 {
		t.Errorf("configured = %d, want the operator's 500000", got)
	}
}

func TestBedrockRuntimeContextSizeMatchesAnthropic(t *testing.T) {
	c := &bedrockRuntimeClient{}
	if got := c.ContextSize(); got != anthropicDefaultContextSize {
		t.Errorf("default = %d, want %d — same Claude models, same cap", got, anthropicDefaultContextSize)
	}
}

func TestRetryWrapperForwardsAnthropicContextSize(t *testing.T) {
	r := &retryLLM{inner: &anthropicClient{contextSize: 300_000}}
	if got := r.ContextSize(); got != 300_000 {
		t.Errorf("through retryLLM = %d, want 300000 — the wrapper must forward ContextSizer", got)
	}
}
