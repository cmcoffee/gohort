package core

import (
	"context"
	"strings"
	"testing"
)

// flakyStream fails once after emitting a partial chunk, then succeeds — the
// shape of a stream that dies on a deadline mid-generation. The partial is
// delivered BEFORE the failure, which is the whole point: whether anything
// already reached the caller is what retrying has to decide on.
func flakyStream(partial, full string) *FakeLLM {
	return &FakeLLM{Turns: []FakeTurn{
		{Chunks: []string{partial}, Err: context.DeadlineExceeded},
		{Chunks: []string{full}, Repeat: true},
	}}
}

// Without an opt-in, a stream that delivered anything is never retried. This is
// the existing contract and the reason a visible reply is never duplicated.
func TestStreamNotRetriedAfterPartialByDefault(t *testing.T) {
	inner := flakyStream("half a sen", "a whole sentence")
	r := &retryLLM{inner: inner, maxRetries: 5}

	var got strings.Builder
	_, err := r.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}},
		func(chunk string) { got.WriteString(chunk) })

	if err == nil {
		t.Fatal("a stream that died after partial output reported success")
	}
	if inner.Calls() != 1 {
		t.Fatalf("attempted %d times; a visible stream must not be retried", inner.Calls())
	}
	if got.String() != "half a sen" {
		t.Fatalf("caller received %q", got.String())
	}
}

// With the reset supplied, the same failure retries — and the caller's buffer
// holds ONLY the successful attempt. That last part is the whole reason the
// option carries a callback instead of being a bool: a caller that kept its
// partial would end up with "half a sen" + "a whole sentence".
func TestStreamRetriesWhenTheCallerCanDiscardItsPartial(t *testing.T) {
	inner := flakyStream("half a sen", "a whole sentence")
	r := &retryLLM{inner: inner, maxRetries: 5}

	var buf strings.Builder
	resp, err := r.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}},
		func(chunk string) { buf.WriteString(chunk) },
		WithStreamRestart(func() { buf.Reset() }),
	)
	if err != nil {
		t.Fatalf("a retryable stream still failed: %v", err)
	}
	if inner.Calls() != 2 {
		t.Fatalf("attempted %d times, want 2", inner.Calls())
	}
	if buf.String() != "a whole sentence" {
		t.Fatalf("the caller's buffer holds %q — the partial was not discarded", buf.String())
	}
	if resp == nil || resp.Content != "a whole sentence" {
		t.Fatalf("response = %+v", resp)
	}
}

// The reset does not make every failure retryable. Transience still decides,
// so a stream that died on something deterministic fails once and stops.
func TestStreamRestartDoesNotRetryNonTransientFailures(t *testing.T) {
	inner := permanentFailStream()
	r := &retryLLM{inner: inner, maxRetries: 5}

	var buf strings.Builder
	_, err := r.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}},
		func(chunk string) { buf.WriteString(chunk) },
		WithStreamRestart(func() { buf.Reset() }),
	)
	if err == nil {
		t.Fatal("a permanent failure reported success")
	}
	if inner.Calls() != 1 {
		t.Fatalf("attempted %d times; a 400 is not worth repeating", inner.Calls())
	}
}

// permanentFailStream emits and then fails with a 400 every time — an error
// that repeating cannot fix.
func permanentFailStream() *FakeLLM {
	return &FakeLLM{Turns: []FakeTurn{{
		Chunks: []string{"some output"},
		Err:    &APIError{StatusCode: 400, Message: "malformed request"},
		Repeat: true,
	}}}
}
