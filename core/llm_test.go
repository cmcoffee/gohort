package core

import (
	"bytes"
	"encoding/json"
	"github.com/cmcoffee/snugforge/nfo"
	"io"
	"reflect"
	"strings"
	"testing"
)

// TestApplyOptsAutoDate: by default a system prompt gets the "Today's date is …"
// prepend; WithoutAutoDate() suppresses it (for callers that stamp the date onto
// the user turn instead).
func TestApplyOptsAutoDate(t *testing.T) {
	// Default: date is prepended, original prompt preserved after it.
	cfg := applyOpts("m", 100, []ChatOption{WithSystemPrompt("BASE PROMPT")})
	if !strings.HasPrefix(cfg.SystemPrompt, "Today's date is ") {
		t.Fatalf("expected date prepend, got %q", cfg.SystemPrompt)
	}
	if !strings.HasSuffix(cfg.SystemPrompt, "BASE PROMPT") {
		t.Fatalf("original prompt not preserved: %q", cfg.SystemPrompt)
	}

	// Suppressed: system prompt is untouched.
	cfg = applyOpts("m", 100, []ChatOption{WithSystemPrompt("BASE PROMPT"), WithoutAutoDate()})
	if cfg.SystemPrompt != "BASE PROMPT" {
		t.Fatalf("WithoutAutoDate should leave prompt untouched, got %q", cfg.SystemPrompt)
	}
	if !cfg.SuppressAutoDate {
		t.Fatalf("SuppressAutoDate flag not set")
	}

	// Empty system prompt: never gets a date (nothing to prepend to).
	cfg = applyOpts("m", 100, nil)
	if cfg.SystemPrompt != "" {
		t.Fatalf("empty prompt should stay empty, got %q", cfg.SystemPrompt)
	}
}

// TestCurrentContextStamp: the user-turn marker is well-formed and carries no
// em-dash (an AI tell scrubbed from product output).
func TestCurrentContextStamp(t *testing.T) {
	s := CurrentContextStamp()
	if !strings.HasPrefix(s, "[Current date & time:") || !strings.HasSuffix(s, "]") {
		t.Fatalf("malformed stamp: %q", s)
	}
	if strings.ContainsRune(s, '—') {
		t.Fatalf("stamp contains an em-dash: %q", s)
	}
}

// The Anthropic path's context cap.
//
// Before these clients implemented ContextSizer, LeadContextSize() silently
// fell back to the WORKER's window — lead agent loops compacted against the
// local llama.cpp num_ctx — and with no sized worker it returned 0, which
// disables compaction entirely. The cap defaults well under the API's 1M
// window on purpose: it bounds per-turn input cost, not what the API accepts.

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

// Tool descriptions are prompt text. Around 310 of them carried an em-dash,
// which put several hundred examples of the character in front of a model
// directly below a rule telling it never to produce one. A rule losing to its
// own prompt is not the model being stubborn.
func TestToolDescriptionsLoseEmDashesOnTheWayToTheModel(t *testing.T) {
	var cfg ChatConfig
	WithTools([]Tool{{
		Name:        "get_joke",
		Description: "Fetch a joke — one per call.",
		Parameters: map[string]ToolParam{
			"category": {Type: "string", Description: "Any category — or blank."},
			"opts": {Type: "object", Properties: map[string]ToolParam{
				"safe": {Type: "boolean", Description: "Family friendly — filters harshly."},
			}},
			"tags": {Type: "array", Items: &ToolParam{Type: "string", Description: "One tag — lowercase."}},
		},
	}})(&cfg)

	got := cfg.Tools[0]
	if strings.ContainsRune(got.Description, '—') {
		t.Errorf("tool description kept its em-dash: %q", got.Description)
	}
	if d := got.Parameters["category"].Description; strings.ContainsRune(d, '—') {
		t.Errorf("parameter kept its em-dash: %q", d)
	}
	// Nesting is where a partial pass would quietly miss: object properties and
	// array item schemas are descriptions too.
	if d := got.Parameters["opts"].Properties["safe"].Description; strings.ContainsRune(d, '—') {
		t.Errorf("nested object property kept its em-dash: %q", d)
	}
	if d := got.Parameters["tags"].Items.Description; strings.ContainsRune(d, '—') {
		t.Errorf("array item schema kept its em-dash: %q", d)
	}
}

// Names and enum values are identifiers the model has to reproduce EXACTLY.
// Rewriting one would break the call it is meant to make, so the pass touches
// descriptions and nothing else.
func TestToolIdentifiersAreNeverRewritten(t *testing.T) {
	var cfg ChatConfig
	WithTools([]Tool{{
		Name:        "weird—name",
		Description: "x",
		Parameters:  map[string]ToolParam{"mode": {Type: "string", Enum: []string{"a—b", "c"}}},
	}})(&cfg)

	if cfg.Tools[0].Name != "weird—name" {
		t.Errorf("tool NAME was rewritten to %q; the model could no longer call it", cfg.Tools[0].Name)
	}
	if got := cfg.Tools[0].Parameters["mode"].Enum[0]; got != "a—b" {
		t.Errorf("enum VALUE was rewritten to %q; it would no longer match", got)
	}
}

// The caller's slice is theirs. Rewriting in place would mutate a tool catalog
// that other calls share.
func TestWithToolsDoesNotMutateTheCallersTools(t *testing.T) {
	orig := []Tool{{Name: "t", Description: "keep — this"}}
	var cfg ChatConfig
	WithTools(orig)(&cfg)
	if orig[0].Description != "keep — this" {
		t.Errorf("caller's tool was mutated: %q", orig[0].Description)
	}
}

// The observed failure: llama.cpp missed an inline "</parameter>" close
// tag and terminated the question value at the options parameter's
// close instead, swallowing the whole options block into question and
// dropping the options key from the arguments.
func TestSalvageSwallowedParams(t *testing.T) {
	args := map[string]any{
		"question": "Here's the plan.\n\nSound good?</parameter>\n<parameter=options>\n[\"yes\", \"edit\", \"no\"]",
	}
	salvageSwallowedParams(args)
	if got := args["question"]; got != "Here's the plan.\n\nSound good?" {
		t.Errorf("question = %q", got)
	}
	want := []any{"yes", "edit", "no"}
	if got, ok := args["options"].([]any); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("options = %#v, want %#v", args["options"], want)
	}
}

func TestSalvageSwallowedParamsCases(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "prose mentioning the tag is untouched",
			in:   map[string]any{"question": "What does </parameter> mean in Qwen's format, and why does it matter?"},
			want: map[string]any{"question": "What does </parameter> mean in Qwen's format, and why does it matter?"},
		},
		{
			name: "trailing wrapper residue with no params is stripped",
			in:   map[string]any{"question": "Proceed?</parameter>\n</function>\n</tool_call>"},
			want: map[string]any{"question": "Proceed?"},
		},
		{
			name: "multiple swallowed params, truncated last value",
			in:   map[string]any{"question": "Pick one?</parameter>\n<parameter=multi>\ntrue\n</parameter>\n<parameter=options>\n[\"a\", \"b\"]"},
			want: map[string]any{"question": "Pick one?", "multi": true, "options": []any{"a", "b"}},
		},
		{
			name: "existing keys are never overwritten",
			in:   map[string]any{"question": "Go?</parameter>\n<parameter=multi>\ntrue\n</parameter>", "multi": false},
			want: map[string]any{"question": "Go?", "multi": false},
		},
		{
			name: "legit tag mention before a real swallow point",
			in:   map[string]any{"question": "Why did </parameter> leak last time? Anyway — proceed?</parameter>\n<parameter=options>\n[\"yes\", \"no\"]\n</parameter>"},
			want: map[string]any{"question": "Why did </parameter> leak last time? Anyway — proceed?", "options": []any{"yes", "no"}},
		},
		{
			name: "non-string values ignored",
			in:   map[string]any{"count": float64(3)},
			want: map[string]any{"count": float64(3)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			salvageSwallowedParams(tc.in)
			if !reflect.DeepEqual(tc.in, tc.want) {
				t.Errorf("got %#v, want %#v", tc.in, tc.want)
			}
		})
	}
}

// TestDropInvalidlyNamedTools — the backstop for a whole-catalog outage. A
// provider handed one malformed tool name rejects the ENTIRE request, so
// without this guard a single bad tool silently costs the agent every other
// tool it had.
func TestDropInvalidlyNamedTools(t *testing.T) {
	tools := []Tool{
		{Name: "fetch_url"},
		{Name: "atlassian.search"}, // the real one: a dot
		{Name: "read-file"},
		{Name: "has space"},
		{Name: ""},
		{Name: strings.Repeat("x", maxLLMToolNameBytes+1)},
		{Name: "OK_Mixed-123"},
	}
	got := dropInvalidlyNamedTools(tools, "test")
	want := []string{"fetch_url", "read-file", "OK_Mixed-123"}
	if len(got) != len(want) {
		t.Fatalf("kept %d tools, want %d: %v", len(got), len(want), plainToolNames(got))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

// TestDropInvalidlyNamedToolsIsFreeOnTheCommonPath — this runs over the whole
// catalog on every LLM call, so an all-valid list must not be copied.
func TestDropInvalidlyNamedToolsIsFreeOnTheCommonPath(t *testing.T) {
	tools := []Tool{{Name: "a"}, {Name: "b_c"}, {Name: "d-e"}}
	got := dropInvalidlyNamedTools(tools, "test")
	if len(got) != len(tools) || &got[0] != &tools[0] {
		t.Error("an all-valid tool list was reallocated instead of passed through")
	}
	if dropInvalidlyNamedTools(nil, "test") != nil {
		t.Error("a nil tool list should stay nil")
	}
}

// TestApplyOptsDropsInvalidToolNames pins the guard to the chokepoint every
// provider path goes through, rather than to any one provider's builder.
func TestApplyOptsDropsInvalidToolNames(t *testing.T) {
	cfg := applyOpts("m", 100, []ChatOption{
		WithTools([]Tool{{Name: "good_tool"}, {Name: "bad.tool"}}),
	})
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "good_tool" {
		t.Errorf("applyOpts left %v in the catalog", plainToolNames(cfg.Tools))
	}
}

func TestValidLLMToolName(t *testing.T) {
	ok := []string{"a", "A", "0", "a_b-C9", strings.Repeat("x", maxLLMToolNameBytes)}
	for _, s := range ok {
		if !validLLMToolName(s) {
			t.Errorf("validLLMToolName(%q) = false, want true", s)
		}
	}
	bad := []string{"", "a.b", "a b", "a/b", "a:b", "ключ", strings.Repeat("x", maxLLMToolNameBytes+1)}
	for _, s := range bad {
		if validLLMToolName(s) {
			t.Errorf("validLLMToolName(%q) = true, want false", s)
		}
	}
}

func plainToolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

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
