package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

// A Claude stream the server opens and then leaves silent ends after the
// idle timeout instead of waiting out the turn: with nothing delivered it is
// the idle-timeout error the retry layer retries, and with part of a reply
// delivered it is that part, marked interrupted, for the loop to continue.
// A stream that keeps sending, however slowly, is never cut off. The
// Anthropic API, Bedrock's invoke stream and Bedrock's Converse stream.
func TestAClaudeStreamThatGoesQuietEndsInsteadOfHanging(t *testing.T) {
	prev := claudeStreamIdle
	claudeStreamIdle = func() time.Duration { return time.Second }
	t.Cleanup(func() { claudeStreamIdle = prev })

	sse := func(lines ...string) string {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("data: " + l + "\n\n")
		}
		return b.String()
	}
	start := `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-test-quiet","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`
	textOpen := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	textDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Partial answer"}}`

	type kind struct {
		name  string
		quiet func(partial bool) []byte // what the server sends before it goes quiet
		llm   func(t *testing.T, url string) LLM
		// slow is a whole reply as pieces, each sent after a pause shorter
		// than the idle timeout, which take longer than it all together.
		slow [][]byte
	}
	stop := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`
	textMore := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`
	kinds := []kind{
		{"anthropic",
			func(partial bool) []byte {
				if partial {
					return []byte(sse(start, textOpen, textDelta))
				}
				return []byte(sse(start))
			},
			func(t *testing.T, url string) LLM {
				c := &anthropicClient{model: "claude-test-quiet", apiKey: "k", api: &apiclient.APIClient{URLScheme: "http", VerifySSL: false, Server: strings.TrimPrefix(url, "http://")}}
				return c
			},
			[][]byte{[]byte(sse(start, textOpen)), []byte(sse(textMore)), []byte(sse(textMore)), []byte(sse(textMore)), []byte(sse(textMore)), []byte(sse(textMore)), []byte(sse(textMore)), []byte(sse(stop, `{"type":"message_stop"}`))}},
		{"bedrock-runtime",
			func(partial bool) []byte {
				out := encodeBedrockChunk(start)
				if partial {
					out = append(out, encodeBedrockChunk(textOpen)...)
					out = append(out, encodeBedrockChunk(textDelta)...)
				}
				return out
			},
			func(t *testing.T, url string) LLM {
				llm, err := newBedrockRuntimeLLM(bedrockAuth{bearer: "tok"}, "us.anthropic.claude-opus-4-8", "us-west-2", strings.TrimPrefix(url, "http://"), &apiclient.APIClient{URLScheme: "http", VerifySSL: false})
				if err != nil {
					t.Fatal(err)
				}
				return llm
			},
			[][]byte{append(encodeBedrockChunk(start), encodeBedrockChunk(textOpen)...), encodeBedrockChunk(textMore), encodeBedrockChunk(textMore), encodeBedrockChunk(textMore), encodeBedrockChunk(textMore), encodeBedrockChunk(textMore), encodeBedrockChunk(textMore), encodeBedrockChunk(stop)}},
		{"bedrock-converse",
			func(partial bool) []byte {
				out := converseFrame("messageStart", `{"role":"assistant"}`)
				if partial {
					out = append(out, converseFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Partial answer"}}`)...)
				}
				return out
			},
			func(t *testing.T, url string) LLM {
				llm, err := newBedrockConverseLLM(bedrockAuth{bearer: "tok"}, "us.anthropic.claude-opus-4-8", "us-west-2", strings.TrimPrefix(url, "http://"), &apiclient.APIClient{URLScheme: "http", VerifySSL: false})
				if err != nil {
					t.Fatal(err)
				}
				return llm
			},
			[][]byte{converseFrame("messageStart", `{"role":"assistant"}`), converseDot(), converseDot(), converseDot(), converseDot(), converseDot(), converseDot(), converseFrame("messageStop", `{"stopReason":"end_turn"}`)}},
	}
	for _, k := range kinds {
		// Slow but alive: 400ms between pieces, about 3s in all, past the
		// 1s timeout, and it finishes.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(k.name, "bedrock") {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			} else {
				w.Header().Set("Content-Type", "text/event-stream")
			}
			for i, piece := range k.slow {
				if i > 0 {
					time.Sleep(400 * time.Millisecond)
				}
				w.Write(piece)
				w.(http.Flusher).Flush()
			}
		}))
		resp, err := k.llm(t, srv.URL).ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, func(string) {})
		srv.Close()
		if err != nil || resp == nil || resp.Content != "......" || resp.StopReason == stopInterrupted {
			t.Errorf("%s: a slow stream that kept sending was cut off: %v %+v", k.name, err, resp)
		}

		for _, partial := range []bool{false, true} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(k.name, "bedrock") {
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				w.Write(k.quiet(partial))
				w.(http.Flusher).Flush()
				select { // quiet until the client gives up
				case <-r.Context().Done():
				case <-time.After(20 * time.Second):
				}
			}))
			t0 := time.Now()
			resp, err := k.llm(t, srv.URL).ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, func(string) {})
			took := time.Since(t0)
			srv.Close()
			if took > 8*time.Second {
				t.Errorf("%s (partial %v): took %s to give up on a quiet stream", k.name, partial, took.Round(time.Second))
			}
			switch {
			case !partial && !IsStreamIdleTimeoutError(err):
				t.Errorf("%s: a quiet stream with nothing delivered: %v, want the idle-timeout error", k.name, err)
			case partial && (err != nil || resp == nil || resp.Content != "Partial answer" || resp.StopReason != stopInterrupted):
				t.Errorf("%s: a quiet stream mid-reply: %v %+v, want the part, interrupted", k.name, err, resp)
			}
		}
	}
}

func converseDot() []byte {
	return converseFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"."}}`)
}
