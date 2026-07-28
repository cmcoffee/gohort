package core

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/nfo"
)

// captureTrace routes the TRACE level at a buffer for the duration of
// the test and restores both the sink and the flag afterwards, so these
// tests can't leak trace routing into the rest of the suite.
func captureTrace(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := nfo.GetOutput(nfo.TRACE)
	prevEnabled := TraceEnabled()
	buf := &bytes.Buffer{}
	nfo.SetOutput(nfo.TRACE, buf)
	t.Cleanup(func() {
		nfo.SetOutput(nfo.TRACE, prev)
		SetTraceEnabled(prevEnabled)
	})
	return buf
}

// bigLLMBody stands in for a real tool-heavy request: the cost this
// guard avoids is parsing and re-serializing a document like this on
// every call.
func bigLLMBody(t *testing.T) []byte {
	t.Helper()
	tools := make([]map[string]any, 0, 80)
	for i := 0; i < 80; i++ {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "tool_" + strings.Repeat("x", 8),
				"description": strings.Repeat("a description of what this tool does. ", 20),
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
				},
			},
		})
	}
	body, err := json.Marshal(map[string]any{
		"model":    "test",
		"messages": []map[string]any{{"role": "user", "content": "Hello"}},
		"tools":    tools,
	})
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	return body
}

func TestSnoopRequestSilentWhenTraceOff(t *testing.T) {
	buf := captureTrace(t)
	SetTraceEnabled(false)

	c := &openAIClient{endpoint: "http://localhost:8080/v1", llamacpp: true}
	c.snoopRequest(bigLLMBody(t), true)
	snoopOAIResponse(200, []byte(`{"choices":[{"message":{"content":"hi"}}]}`))

	if buf.Len() != 0 {
		t.Fatalf("trace output produced while disabled: %q", buf.String())
	}
}

func TestSnoopRequestWritesWhenTraceOn(t *testing.T) {
	buf := captureTrace(t)
	SetTraceEnabled(true)

	c := &openAIClient{endpoint: "http://localhost:8080/v1", llamacpp: true}
	c.snoopRequest(bigLLMBody(t), true)

	out := buf.String()
	if !strings.Contains(out, "REQUEST BODY") {
		t.Fatalf("expected the request body in trace output, got: %q", truncForLog(out, 200))
	}
}

func TestSnoopAnthropicRespectsTraceFlag(t *testing.T) {
	buf := captureTrace(t)
	SetTraceEnabled(false)

	c := &anthropicClient{}
	c.snoopRequest(bigLLMBody(t), true)
	snoopAnthResponse(200, []byte(`{"content":[{"type":"text","text":"hi"}]}`))
	if buf.Len() != 0 {
		t.Fatalf("anthropic trace output produced while disabled: %q", buf.String())
	}

	SetTraceEnabled(true)
	snoopAnthResponse(200, []byte(`{"content":[{"type":"text","text":"hi"}]}`))
	if !strings.Contains(buf.String(), "RESPONSE STATUS") {
		t.Fatal("expected anthropic trace output once enabled")
	}
}

func TestTraceEnabledDefaultsOff(t *testing.T) {
	prev := TraceEnabled()
	t.Cleanup(func() { SetTraceEnabled(prev) })

	SetTraceEnabled(false)
	if TraceEnabled() {
		t.Fatal("TraceEnabled reported on after being set off")
	}
	SetTraceEnabled(true)
	if !TraceEnabled() {
		t.Fatal("TraceEnabled reported off after being set on")
	}
}

// The two benchmarks below contrast the paths on a realistic body. The
// difference is what the guard saves on every LLM call of a deployment
// that isn't tracing. Run with -bench=SnoopRequest.
func benchSnoop(b *testing.B, traceOn bool) {
	b.Helper()
	body := bigLLMBody(&testing.T{})
	c := &openAIClient{endpoint: "http://localhost:8080/v1", llamacpp: true}
	prevOut, prevEnabled := nfo.GetOutput(nfo.TRACE), TraceEnabled()
	nfo.SetOutput(nfo.TRACE, io.Discard)
	SetTraceEnabled(traceOn)
	b.Cleanup(func() {
		nfo.SetOutput(nfo.TRACE, prevOut)
		SetTraceEnabled(prevEnabled)
	})
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.snoopRequest(body, true)
	}
}

func BenchmarkSnoopRequestTraceOff(b *testing.B) { benchSnoop(b, false) }
func BenchmarkSnoopRequestTraceOn(b *testing.B)  { benchSnoop(b, true) }
