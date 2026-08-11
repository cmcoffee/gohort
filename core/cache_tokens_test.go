package core

import "testing"

// Prompt caching splits the prompt three ways, and input_tokens names only the
// UNCACHED remainder. On a conversation whose system prompt and history are a
// cache hit that remainder is a handful of tokens — an "input_tokens=2" against
// a prompt of thousands is a faithful report of a near-total cache hit, not a
// broken counter.
//
// The accumulator dropped the other two, so every cached turn reported a
// two-token prompt and looked free. This is the fixture that pins it: a large
// cached prompt, a tiny uncached remainder.
func TestStreamingCapturesTheCachedHalvesOfThePrompt(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"message_start","message":{"model":"claude-opus-4-8","usage":{` +
		`"input_tokens":2,"cache_read_input_tokens":41318,"cache_creation_input_tokens":1204}}}`))
	st.feed([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":867}}`))

	r := st.response("test")
	if r.InputTokens != 2 {
		t.Errorf("uncached input = %d, want the 2 the API reported", r.InputTokens)
	}
	if r.CacheReadTokens != 41318 {
		t.Errorf("cache read = %d, want 41318 — the bulk of the prompt was being discarded", r.CacheReadTokens)
	}
	if r.CacheWriteTokens != 1204 {
		t.Errorf("cache write = %d, want 1204", r.CacheWriteTokens)
	}
	if got := r.InputTokens + r.CacheReadTokens + r.CacheWriteTokens; got != 42524 {
		t.Errorf("prompt size = %d, want 42524", got)
	}
}

// A turn with no caching must be unaffected — the sum has to equal the plain
// input count, or every non-caching provider starts over-reporting.
func TestUncachedTurnReportsPlainInput(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":1500}}}`))
	st.feed([]byte(`{"type":"message_delta","usage":{"output_tokens":40}}`))

	r := st.response("test")
	if r.CacheReadTokens != 0 || r.CacheWriteTokens != 0 {
		t.Errorf("no caching in play but cache tokens reported: read=%d write=%d",
			r.CacheReadTokens, r.CacheWriteTokens)
	}
	if got := r.InputTokens + r.CacheReadTokens + r.CacheWriteTokens; got != 1500 {
		t.Errorf("prompt size = %d, want the plain 1500", got)
	}
}

// Some providers restate usage on message_delta. Taking the larger keeps a
// message_start value from being erased by a zero arriving later.
func TestCacheTokensSurviveAZeroInMessageDelta(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"message_start","message":{"model":"m","usage":{` +
		`"input_tokens":2,"cache_read_input_tokens":9000}}}`))
	st.feed([]byte(`{"type":"message_delta","usage":{"output_tokens":10}}`))

	if r := st.response("test"); r.CacheReadTokens != 9000 {
		t.Errorf("cache read = %d after a delta that omitted it, want 9000", r.CacheReadTokens)
	}
}

// Bedrock's invocationMetrics reports the BILLED TOTAL for the prompt. Once the
// stream has given a cache breakdown of that same prompt, overwriting the
// uncached part with the total and then summing all three double-counts
// everything cached — which is most of a long conversation.
func TestBedrockMetricsDoNotDoubleCountCachedTokens(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"message_start","message":{"model":"m","usage":{` +
		`"input_tokens":2,"cache_read_input_tokens":41318,"cache_creation_input_tokens":1204}}}`))
	applyBedrockMetrics(st, []byte(`{"type":"message_stop","amazon-bedrock-invocationMetrics":`+
		`{"inputTokenCount":42524,"outputTokenCount":867}}`))

	r := st.response("test")
	if r.InputTokens != 2 {
		t.Errorf("uncached input = %d — the billed total overwrote the breakdown, so the sum "+
			"now counts the cached prompt twice", r.InputTokens)
	}
	if got := r.InputTokens + r.CacheReadTokens + r.CacheWriteTokens; got != 42524 {
		t.Errorf("prompt size = %d, want 42524 (it would be %d if double-counted)", got, 42524+41318+1204)
	}
}

// With no cache breakdown the billed total is the only real number there is,
// and it still wins — that is the placeholder case the metrics reading exists
// for, and it must keep working.
func TestBedrockMetricsStillWinWithNoCacheBreakdown(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":2}}}`))
	applyBedrockMetrics(st, []byte(`{"type":"message_stop","amazon-bedrock-invocationMetrics":`+
		`{"inputTokenCount":2447,"outputTokenCount":153}}`))

	if r := st.response("test"); r.InputTokens != 2447 {
		t.Errorf("input = %d, want the billed 2447", r.InputTokens)
	}
}
