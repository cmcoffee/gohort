package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

// serveTimedOpenAIStream is a backend that sits silent for prefill before its
// first chunk, then writes the rest gap apart: the shape of a real stream.
func serveTimedOpenAIStream(t *testing.T, prefill, gap time.Duration, lines ...string) LLM {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(prefill)
		for i, l := range lines {
			if i > 0 {
				time.Sleep(gap)
			}
			w.Write([]byte(l + "\n\n"))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	return newOpenAILLM("", "any-model", srv.URL, api)
}

// A backend that reports no timings of its own gets a rate timed from its
// first token to its last. The wait before the first token is prefill, and
// counting it is what made the rate read as a fraction of the real speed.
func TestStreamRateLeavesOutTheWaitBeforeTheFirstToken(t *testing.T) {
	llm := serveTimedOpenAIStream(t, 600*time.Millisecond, 150*time.Millisecond,
		`data: {"choices":[{"delta":{"reasoning_content":"Thinking it over."}}]}`,
		`data: {"choices":[{"delta":{"content":"The answer"}}]}`,
		`data: {"choices":[{"delta":{"content":" is here."},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":900,"completion_tokens":31}}`,
		`data: [DONE]`,
	)
	start := time.Now()
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "q"}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	whole := 31 / time.Since(start).Seconds()
	// 30 tokens after the first, over the ~300ms between first and last.
	if resp.PredictedPerSecond < 60 || resp.PredictedPerSecond > 110 {
		t.Errorf("rate = %.1f tok/s, want about 100 (30 tokens over 0.3s)", resp.PredictedPerSecond)
	}
	if resp.PredictedPerSecond < 2*whole {
		t.Errorf("rate %.1f is near the whole-call figure %.1f: the wait before the first token was counted", resp.PredictedPerSecond, whole)
	}
	if resp.PromptPerSecond != 0 {
		t.Errorf("prefill rate = %.1f, want none: only the server can know it", resp.PromptPerSecond)
	}
}

// llama.cpp's own figure wins over the stream's: it is the number its UI
// shows, measured where the tokens are made.
func TestServerReportedRateIsKept(t *testing.T) {
	llm := serveTimedOpenAIStream(t, 0, 150*time.Millisecond,
		`data: {"choices":[{"delta":{"content":"The answer"}}]}`,
		`data: {"choices":[{"delta":{"content":" is here."},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":900,"completion_tokens":31},"timings":{"predicted_per_second":42.5,"prompt_per_second":1200}}`,
		`data: [DONE]`,
	)
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "q"}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.PredictedPerSecond != 42.5 || resp.PromptPerSecond != 1200 {
		t.Errorf("rates = %.1f / %.1f, want the server's 42.5 / 1200", resp.PredictedPerSecond, resp.PromptPerSecond)
	}
}

// A short reply that lands in one or two network bursts has no span to read a
// rate from; dividing by a few microseconds reports thousands a second.
func TestDecodeClockNeedsASpan(t *testing.T) {
	t0 := time.Now()
	cases := []struct {
		span   time.Duration
		tokens int
		want   float64
	}{
		{2 * time.Second, 101, 50},
		{10 * time.Millisecond, 40, 0},
		{2 * time.Second, 1, 0},
		{0, 0, 0},
	}
	for _, c := range cases {
		d := decodeClock{first: t0, last: t0.Add(c.span)}
		if c.span == 0 {
			d = decodeClock{}
		}
		if got := d.rate(c.tokens); got != c.want {
			t.Errorf("%d tokens over %v: rate = %.1f, want %.1f", c.tokens, c.span, got, c.want)
		}
	}
}

// The Anthropic parser (Bedrock's too) starts the clock at the first delta of
// any kind: thinking is billed as output, so it is part of the span. The
// message_start that precedes it is the request being accepted, not output.
func TestAnthropicStreamClockStartsAtTheFirstDelta(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":10}}}`))
	if !st.clock.first.IsZero() {
		t.Fatal("message_start started the clock; it carries no output")
	}
	st.feed([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`))
	st.feed([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`))
	if st.clock.first.IsZero() {
		t.Fatal("a thinking delta did not start the clock")
	}
}
