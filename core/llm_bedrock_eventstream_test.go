package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

func TestEventStreamRoundTrip(t *testing.T) {
	payload := []byte(`{"hello":"world"}`)
	raw := encodeEventStreamFrame(map[string]string{
		":message-type": "event",
		":event-type":   "chunk",
	}, payload)

	f, err := newEventStreamReader(bytes.NewReader(raw)).next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if f.messageType() != "event" || f.Headers[":event-type"] != "chunk" {
		t.Errorf("headers = %v", f.Headers)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Errorf("payload = %q, want %q", f.Payload, payload)
	}
}

// Every header value type has to advance the cursor by the right width. Get
// one wrong and the rest of the block decodes as garbage, so walk a block that
// contains all of them.
func TestEventStreamHeaderValueTypes(t *testing.T) {
	var b []byte
	put := func(name string, vtype byte, val []byte) {
		b = append(b, byte(len(name)))
		b = append(b, name...)
		b = append(b, vtype)
		b = append(b, val...)
	}
	put("t", 0, nil)                                     // bool true
	put("f", 1, nil)                                     // bool false
	put("by", 2, []byte{1})                              // byte
	put("sh", 3, []byte{0, 2})                           // int16
	put("in", 4, []byte{0, 0, 0, 4})                     // int32
	put("lo", 5, []byte{0, 0, 0, 0, 0, 0, 0, 8})         // int64
	put("ba", 6, append([]byte{0, 3}, []byte("abc")...)) // byte array
	put("ts", 8, make([]byte, 8))                        // timestamp
	put("id", 9, make([]byte, 16))                       // uuid
	// The string that must survive all of the above.
	put(":event-type", 7, append([]byte{0, 5}, []byte("chunk")...))

	got, err := parseEventStreamHeaders(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got[":event-type"] != "chunk" {
		t.Errorf("cursor desynchronized: %v", got)
	}
}

func TestEventStreamRejectsMalformedFrames(t *testing.T) {
	for name, raw := range map[string][]byte{
		"implausible length": binary.BigEndian.AppendUint32(nil, 3),
		"headers exceed frame": func() []byte {
			b := binary.BigEndian.AppendUint32(nil, 32)
			b = binary.BigEndian.AppendUint32(b, 999)
			return binary.BigEndian.AppendUint32(b, 0)
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			// Pad so the prelude read itself succeeds where relevant.
			padded := append(raw, make([]byte, 16)...)
			if _, err := newEventStreamReader(bytes.NewReader(padded)).next(); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// A truncated stream must not look like a clean end.
func TestEventStreamTruncationIsAnError(t *testing.T) {
	raw := encodeBedrockChunk(`{"type":"ping"}`)
	if _, err := newEventStreamReader(bytes.NewReader(raw[:len(raw)-3])).next(); err == nil {
		t.Error("truncated frame body should error")
	}
	if _, err := newEventStreamReader(bytes.NewReader(raw[:5])).next(); err == nil {
		t.Error("truncated prelude should error")
	}
}

func TestDecodeBedrockEvent(t *testing.T) {
	// A chunk yields the inner Anthropic event verbatim.
	inner := `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`
	f, _ := newEventStreamReader(bytes.NewReader(encodeBedrockChunk(inner))).next()
	got, err := decodeBedrockEvent(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != inner {
		t.Errorf("event = %s", got)
	}

	// An exception frame surfaces its type, which is the actionable part.
	body, _ := json.Marshal(map[string]string{"message": "rate exceeded"})
	raw := encodeEventStreamFrame(map[string]string{
		":message-type":   "exception",
		":exception-type": "throttlingException",
	}, body)
	f, _ = newEventStreamReader(bytes.NewReader(raw)).next()
	if _, err = decodeBedrockEvent(f); err == nil {
		t.Fatal("exception frame should error")
	} else if !strings.Contains(err.Error(), "throttlingException") ||
		!strings.Contains(err.Error(), "rate exceeded") {
		t.Errorf("error should name type and message, got: %v", err)
	}

	// Unknown non-chunk frames are skipped, not fatal.
	raw = encodeEventStreamFrame(map[string]string{
		":message-type": "event", ":event-type": "keepalive"}, nil)
	f, _ = newEventStreamReader(bytes.NewReader(raw)).next()
	got, err = decodeBedrockEvent(f)
	if err != nil || got != nil {
		t.Errorf("keepalive should be skipped, got %q / %v", got, err)
	}
}

// End to end: a real streaming exchange over the real framing, asserting the
// handler sees incremental tokens and the tool call assembles.
func TestBedrockRuntimeStreamingEndToEnd(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	events := []string{
		`{"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":8}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"get_time"}}`,
		// partial_json, the REAL wire field. This test previously used "text"
		// here, which is what the buggy accumulator read — so it passed while
		// every live tool call arrived with empty arguments. A fixture copied
		// from the code it tests proves only that the code agrees with itself.
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"utc\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":14}}`,
	}

	var gotPath, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept = r.URL.EscapedPath(), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, e := range events {
			w.Write(encodeBedrockChunk(e))
		}
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-4-8", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatal(err)
	}

	var chunks []string
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}},
		func(c string) { chunks = append(chunks, c) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if want := "/model/us.anthropic.claude-opus-4-8/invoke-with-response-stream"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotAccept != "application/vnd.amazon.eventstream" {
		t.Errorf("Accept = %q", gotAccept)
	}
	// The whole point: tokens arrive incrementally, not in one lump.
	if len(chunks) != 2 || chunks[0] != "Hi " || chunks[1] != "there" {
		t.Errorf("handler chunks = %q, want incremental delivery", chunks)
	}
	if resp.Content != "Hi there" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Model != "claude-opus-4-8" || resp.InputTokens != 8 || resp.OutputTokens != 14 {
		t.Errorf("usage: model=%q in=%d out=%d", resp.Model, resp.InputTokens, resp.OutputTokens)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "get_time" ||
		resp.ToolCalls[0].Args["tz"] != "utc" {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
}

// runBedrockStream plays a fixed set of events through a real ChatStream.
func runBedrockStream(t *testing.T, events []string) *Response {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, e := range events {
			w.Write(encodeBedrockChunk(e))
		}
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-4-8", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return resp
}

// Bedrock's message_start reports a placeholder input count on this endpoint —
// 2, observed live against a prompt of thousands of tokens — and nothing later
// in the Anthropic events corrects it. The billed counts arrive once, on the
// last chunk, under a Bedrock-specific key. Reading only the Anthropic usage
// blocks recorded expensive turns as costing nothing.
//
// The fixture is the AWS wire shape (inputTokenCount, camelCase, alongside
// message_stop), not this package's field names.
func TestBedrockBilledTokensOverrideThePlaceholder(t *testing.T) {
	resp := runBedrockStream(t, []string{
		`{"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":2}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":2447,` +
			`"outputTokenCount":153,"invocationLatency":2100,"firstByteLatency":480}}`,
	})
	if resp.InputTokens != 2447 {
		t.Errorf("input tokens = %d, want the billed 2447 (the stream's placeholder was 2)", resp.InputTokens)
	}
	if resp.OutputTokens != 153 {
		t.Errorf("output tokens = %d, want the billed 153", resp.OutputTokens)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q — reading metrics must not disturb the response", resp.Content)
	}
}

// No metrics chunk (a truncated stream, or an endpoint that stops sending
// them): keep what the Anthropic events reported rather than reporting zero.
func TestBedrockUsageSurvivesMissingMetrics(t *testing.T) {
	resp := runBedrockStream(t, []string{
		`{"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":11}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
	})
	if resp.InputTokens != 11 || resp.OutputTokens != 9 {
		t.Errorf("usage in=%d out=%d, want the stream's 11/9", resp.InputTokens, resp.OutputTokens)
	}
}

// A metrics block that omits a count must not zero out one the stream did
// report. Overriding unconditionally would trade a wrong number for a wronger
// one on any future shape change.
func TestBedrockPartialMetricsDoNotZeroCounts(t *testing.T) {
	resp := runBedrockStream(t, []string{
		`{"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":11}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"invocationLatency":2100}}`,
	})
	if resp.InputTokens != 11 || resp.OutputTokens != 9 {
		t.Errorf("usage in=%d out=%d, want the stream's 11/9 preserved", resp.InputTokens, resp.OutputTokens)
	}
}

// A stream that dies mid-answer should return what arrived, not nothing: the
// caller has already rendered those tokens through the handler.
func TestBedrockRuntimeStreamTruncationKeepsPartial(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(encodeBedrockChunk(`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`))
		w.Write(encodeBedrockChunk(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`))
		half := encodeBedrockChunk(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lost"}}`)
		w.Write(half[:len(half)/2]) // cut mid-frame
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, _ := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-4-8", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)

	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("a truncated stream with partial content should not error: %v", err)
	}
	if resp.Content != "partial" {
		t.Errorf("content = %q, want the partial text", resp.Content)
	}
}

// Guard the envelope assumption: Bedrock base64s the inner event under "bytes",
// and encoding/json decodes []byte from base64 automatically.
func TestBedrockChunkEnvelopeIsBase64(t *testing.T) {
	raw := encodeBedrockChunk(`{"type":"ping"}`)
	f, _ := newEventStreamReader(bytes.NewReader(raw)).next()
	var env map[string]string
	if err := json.Unmarshal(f.Payload, &env); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(env["bytes"])
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if string(decoded) != `{"type":"ping"}` {
		t.Errorf("decoded = %s", decoded)
	}
	_ = io.EOF
}
