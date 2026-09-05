package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

// serveOpenAIStream runs a fake OpenAI-compatible backend that writes the
// given SSE lines and closes the body.
func serveOpenAIStream(t *testing.T, lines ...string) LLM {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			w.Write([]byte(l + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	return newOpenAILLM("", "qwen3.6", srv.URL, api)
}

// A body that ends with neither finish_reason nor [DONE] never said the reply
// was over. A peer proxy that gave up on a silent upstream ends the body
// exactly like this, and what had arrived came back as a finished answer —
// a report that stopped short with nothing to say why.
func TestOpenAIStreamWithoutFinishIsInterrupted(t *testing.T) {
	llm := serveOpenAIStream(t,
		`data: {"choices":[{"delta":{"content":"The first half of the report, and"}}]}`,
		`data: {"choices":[{"delta":{"content":" then the proxy"}}]}`,
	)
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "report"}}, func(string) {})
	if err != nil {
		t.Fatalf("partial content must still be returned: %v", err)
	}
	if resp.Content != "The first half of the report, and then the proxy" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.StopReason != stopInterrupted || !responseWasTruncated(resp) {
		t.Errorf("stop_reason = %q, want %q and the loop to read it as unfinished", resp.StopReason, stopInterrupted)
	}
}

// The ordinary end — finish_reason on the last chunk, then [DONE] — is a
// finish, and so is finish_reason without [DONE].
func TestOpenAIStreamFinishReasonIsAFinish(t *testing.T) {
	for _, lines := range [][]string{
		{`data: {"choices":[{"delta":{"content":"Done."}}]}`, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`},
		{`data: {"choices":[{"delta":{"content":"Done."}}]}`, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`},
	} {
		llm := serveOpenAIStream(t, lines...)
		resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "report"}}, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StopReason != "stop" || responseWasTruncated(resp) {
			t.Errorf("stop_reason = %q, want a clean finish", resp.StopReason)
		}
	}
	// [DONE] with no finish_reason at all is what some backends send for a
	// complete reply; not a cut.
	llm := serveOpenAIStream(t, `data: {"choices":[{"delta":{"content":"Done."}}]}`, `data: [DONE]`)
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "report"}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(resp.StopReason, stopInterrupted) {
		t.Error("[DONE] closes the reply; it must not read as interrupted")
	}
}
