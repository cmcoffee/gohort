package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/snugforge/nfo"
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

// ListModels is what the admin Model field's picker shows. Ollama keeps its
// list at the server root whatever path the chat endpoint was given, and a
// refusal has to come back as the provider's own reason.
func TestListModelsOllamaAndRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[{"name":"llama3:8b"},{"name":"gemma:2b"},{"name":"llama3:8b"}]}`))
	}))
	defer srv.Close()
	ids, err := ListModels(context.Background(), LLMProviderConfig{Provider: "ollama", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "gemma:2b,llama3:8b" {
		t.Errorf("ids = %v, want sorted and de-duplicated", ids)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"not authorized to list"}`))
	}))
	defer bad.Close()
	_, err = ListModels(context.Background(), LLMProviderConfig{Provider: "vllm", Endpoint: bad.URL})
	if err == nil || !strings.Contains(err.Error(), "not authorized to list") {
		t.Errorf("err = %v, want the provider's reason", err)
	}
	if _, err := ListModels(context.Background(), LLMProviderConfig{}); err == nil {
		t.Error("no provider should be an error")
	}
}

func TestModelListReasonShapes(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":{"message":"API key not valid"}}`: "API key not valid",
		`{"error":"bad key"}`:                       "bad key",
		`{"message":"denied"}`:                      "denied",
		`{"Message":"User is not authorized"}`:      "User is not authorized",
		`plain text failure`:                        "plain text failure",
	} {
		if got := modelListReason([]byte(body)); got != want {
			t.Errorf("modelListReason(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestOpenAIChatModelFilter(t *testing.T) {
	for id, want := range map[string]bool{
		"gpt-5":                  true,
		"o4-mini":                true,
		"text-embedding-3-large": false,
		"whisper-1":              false,
		"tts-1-hd":               false,
		"dall-e-3":               false,
		"gpt-image-1":            false,
		"omni-moderation-latest": false,
	} {
		if got := openAIChatModel(id); got != want {
			t.Errorf("openAIChatModel(%q) = %v, want %v", id, got, want)
		}
	}
}

// Thinking is switched OFF for one round after a guardrail block.
//
// A block is the round where deliberation reliably goes wrong: the model is
// handed a refusal it did not expect, about a mechanism it is told not to name,
// and asked to carry on. It reasons at length about the system it is inside
// instead of answering — the failure COLLAPSE-DIAG exists to detect. Trimming
// the block messages helped and did not fix it, because the deliberation is
// caused by the situation rather than only by the wording.
//
// These pin the mechanism at the level it can be tested without a live model:
// the option is produced, it wins over the static options, and it lasts exactly
// one round.

// thinkStateOf resolves a built option slice the way the LLM layer does and
// reports the thinking decision it lands on.
func thinkStateOf(opts []ChatOption) (enabled, set bool) {
	cfg := applyOpts("m", 1024, opts)
	if cfg.Think == nil {
		return false, false
	}
	return *cfg.Think, true
}

// TestQuietRoundOptionWins — the option is appended LAST, so it has to beat a
// per-agent think budget and any static ChatOptions the caller set. An agent
// configured to think would otherwise keep thinking through exactly the round
// this exists for.
func TestQuietRoundOptionWins(t *testing.T) {
	// What the loop builds: static options first, the one-shot last.
	static := []ChatOption{WithThink(true), WithThinkBudget(4096)}
	on, set := thinkStateOf(static)
	if !set || !on {
		t.Fatalf("baseline should have thinking on: on=%v set=%v", on, set)
	}
	quiet := append(append([]ChatOption{}, static...), WithThink(false))
	on, set = thinkStateOf(quiet)
	if !set {
		t.Fatal("the one-shot option did not reach the config")
	}
	if on {
		t.Error("a static WithThink(true) beat the one-shot; it must be appended last and win")
	}
}

// TestQuietRoundIsOneShot — the flag is consumed when applied. A block that
// silenced thinking for the rest of the turn would degrade every later round,
// including ones doing real work after the agent changed course.
func TestQuietRoundIsOneShot(t *testing.T) {
	// Mirrors the loop's consume-on-apply shape.
	quiet := true
	build := func() []ChatOption {
		opts := []ChatOption{WithThink(true)}
		if quiet {
			quiet = false
			opts = append(opts, WithThink(false))
		}
		return opts
	}
	if on, _ := thinkStateOf(build()); on {
		t.Error("the round after a block should not think")
	}
	if quiet {
		t.Error("the flag was not consumed")
	}
	if on, _ := thinkStateOf(build()); !on {
		t.Error("thinking must come back the round after — the agent still has work to do")
	}
}

// TestUnblockedRoundsAreUntouched — an agent that never trips a guardrail must
// build byte-identical options to before this existed. The whole feature is
// meant to cost nothing in the steady state.
func TestUnblockedRoundsAreUntouched(t *testing.T) {
	quiet := false
	opts := []ChatOption{WithThink(true), WithThinkBudget(2048)}
	if quiet {
		opts = append(opts, WithThink(false))
	}
	on, set := thinkStateOf(opts)
	if !set || !on {
		t.Errorf("an unblocked round changed shape: on=%v set=%v", on, set)
	}
	cfg := applyOpts("m", 1024, opts)
	if cfg.ThinkBudget == nil || *cfg.ThinkBudget != 2048 {
		t.Errorf("the agent's think budget was disturbed: %v", cfg.ThinkBudget)
	}
}

// TestClosedNoteIsForTheModelOnly — the note exists to stop the next turn
// answering a request that was refused, and it must never reach the reader.
func TestClosedNoteIsForTheModelOnly(t *testing.T) {
	substituted := "I can't help with that one." + guardrailClosedNote

	// The persisted copy carries it: cleanReply is stored BEFORE stripping, so
	// the next turn's history contains it and the model sees the outcome.
	if !strings.Contains(substituted, "declined and is closed") {
		t.Fatal("the persisted reply lost the note the next turn depends on")
	}
	// Every delivery boundary strips it.
	delivered := textutil.StripMetaTags(substituted)
	if strings.Contains(delivered, "gohort-meta") || strings.Contains(delivered, "declined and is closed") {
		t.Errorf("the note reached the reader: %q", delivered)
	}
	if strings.TrimSpace(delivered) != "I can't help with that one." {
		t.Errorf("stripping disturbed the decline itself: %q", delivered)
	}
	// It must not name the MECHANISM. Doing so invites the model to reason
	// about the system it is inside — the deliberation the one-shot
	// thinking-off exists to stop, and the reason guardrailInputMessage
	// dropped "ENFORCED GUARDRAIL". Nor may it restate the subject, which
	// would re-seed the topic the block just removed.
	low := strings.ToLower(guardrailClosedNote)
	for _, leak := range []string{"guardrail", "rule", "enforced", "check", "policy", "blocked"} {
		if strings.Contains(low, leak) {
			t.Errorf("the note names the mechanism (%q): %s", leak, guardrailClosedNote)
		}
	}
	// And it still reads as CLOSED rather than deferred — the distinction the
	// model got wrong live.
	for _, want := range []string{"closed", "do not answer it later"} {
		if !strings.Contains(low, want) {
			t.Errorf("the note does not say the request is settled (missing %q)", want)
		}
	}
}

func gateFixture(t *testing.T, workerSees bool) (lead *retryLLM, leadInner, worker *FakeLLM) {
	t.Helper()
	worker = &FakeLLM{Turns: []FakeTurn{{Content: "A red bicycle against a brick wall; a sign reads OPEN 9-5.", Repeat: true}}}
	leadInner = &FakeLLM{Turns: []FakeTurn{{Content: "It is a bicycle.", Repeat: true}}}
	w := &retryLLM{inner: worker, maxRetries: 1, noImages: !workerSees}
	lead = &retryLLM{inner: leadInner, maxRetries: 1, noImages: true}
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(w, lead)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	describeMu.Lock()
	describeCache, describeOrder = map[[32]byte]string{}, nil
	describeMu.Unlock()
	return lead, leadInner, worker
}

// A model set not to read images never receives one, on either call path: the
// picture goes to the worker, which reads them, and the lead gets the
// worker's description in its place. The caller's messages keep the image,
// and the same picture is described once however often it is re-sent.
func TestAModelThatDoesNotReadImagesGetsADescription(t *testing.T) {
	lead, leadInner, worker := gateFixture(t, true)
	photo := []byte("\x89PNG fake bytes of a photo")
	msgs := []Message{{Role: "user", Content: "what is in this picture?", Images: [][]byte{photo}}}

	if _, err := lead.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := lead.ChatStream(context.Background(), msgs, func(string) {}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < leadInner.Calls(); i++ {
		for _, m := range leadInner.Sent(i) {
			if len(m.Images) > 0 || len(m.Videos) > 0 {
				t.Fatalf("call %d: the lead was sent an image", i)
			}
		}
		got := leadInner.Sent(i)[0].Content
		if !strings.Contains(got, "what is in this picture?") || !strings.Contains(got, "sign reads OPEN 9-5") ||
			!strings.Contains(got, "described by another model") {
			t.Errorf("call %d: the lead got %q", i, got)
		}
	}
	if worker.Calls() != 1 {
		t.Errorf("the picture was described %d times; once is enough", worker.Calls())
	}
	if len(worker.Sent(0)[0].Images) != 1 {
		t.Error("the describing model was not shown the picture")
	}
	if len(msgs[0].Images) != 1 || msgs[0].Content != "what is in this picture?" {
		t.Error("the caller's messages were changed")
	}
}

// With nothing that reads images, the model is told an image was there and
// that its contents are unknown, rather than nothing at all; a model that
// reads images is sent them as before; and a video with no still to take is
// said to have been there.
func TestWithNoDescriberTheModelIsToldAnImageWasThere(t *testing.T) {
	lead, leadInner, worker := gateFixture(t, false)
	lead.Chat(context.Background(), []Message{{Role: "user", Content: "look", Images: [][]byte{[]byte("img")}, Videos: [][]byte{[]byte("not a video")}}})
	sent := leadInner.Sent(0)[0]
	if len(sent.Images) > 0 || len(sent.Videos) > 0 || !strings.Contains(sent.Content, "contents are unknown") {
		t.Errorf("the lead got %+v", sent)
	}
	if worker.Calls() != 0 {
		t.Error("a model that does not read images was asked to describe one")
	}

	sees := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	open := &retryLLM{inner: sees, maxRetries: 1}
	open.Chat(context.Background(), []Message{{Role: "user", Content: "look", Images: [][]byte{[]byte("img")}}})
	if len(sees.Sent(0)[0].Images) != 1 {
		t.Error("a model that reads images was not sent the image")
	}
}

// The setting reaches the client built from a configuration.
func TestNoImagesReachesTheClient(t *testing.T) {
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "ollama", Model: "m", NoImages: true})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := llm.(*retryLLM); !ok || !r.noImages {
		t.Errorf("the client does not hold back images: %#v", llm)
	}
}

// capLLM reads pictures but refuses more than max in one request, the way
// vLLM does over its --limit-mm-per-prompt, and records how many each call
// carried.
type capLLM struct {
	max  int
	sent [][]Message
}

func (c *capLLM) answer(messages []Message) (*Response, error) {
	c.sent = append(c.sent, messages)
	if n := countImages(messages); n > c.max {
		return nil, &APIError{Provider: "vllm", StatusCode: 400, Message: fmt.Sprintf("At most %d image(s) may be provided in one prompt. (parameter=image)", c.max)}
	}
	return &Response{Content: "a dog on a sofa"}, nil
}

func (c *capLLM) Chat(_ context.Context, messages []Message, _ ...ChatOption) (*Response, error) {
	return c.answer(messages)
}

func (c *capLLM) ChatStream(_ context.Context, messages []Message, _ StreamHandler, _ ...ChatOption) (*Response, error) {
	return c.answer(messages)
}

// A model that takes two pictures a request, sent four: the refusal is not
// the end of the turn. The request goes again with the newest two, each older
// picture described by the same model on its own, and the next request is
// trimmed before it is sent. Observed 2026-10-23: four parallel finds queued
// four pictures, vLLM refused the round, and the turn failed outright.
func TestAModelsPictureLimitIsLearnedAndKept(t *testing.T) {
	describeMu.Lock()
	describeCache, describeOrder = map[[32]byte]string{}, nil
	describeMu.Unlock()
	stub := &capLLM{max: 2}
	r := &retryLLM{inner: stub, maxRetries: 1}
	pic := func(s string) []byte { return []byte("\x89PNG " + s) }
	msgs := []Message{
		{Role: "user", Content: "earlier", Images: [][]byte{pic("old")}},
		{Role: "user", Content: "the four you found", Images: [][]byte{pic("a"), pic("b"), pic("c")}},
	}
	if _, err := r.Chat(context.Background(), msgs); err != nil {
		t.Fatalf("the refusal should have been retried within the limit: %v", err)
	}
	counts := func(from int) (n []int) {
		for _, m := range stub.sent[from:] {
			n = append(n, countImages(m))
		}
		return n
	}
	// Refused with four; each of the two held back described alone; then two.
	if got := fmt.Sprint(counts(0)); got != "[4 1 1 2]" {
		t.Fatalf("pictures per call = %s, want [4 1 1 2]", got)
	}
	last := stub.sent[len(stub.sent)-1]
	if imgs := last[1].Images; len(imgs) != 2 || string(imgs[0]) != string(pic("b")) || string(imgs[1]) != string(pic("c")) {
		t.Errorf("the newest two should be kept: %q", imgs)
	}
	if len(last[0].Images) != 0 || !strings.Contains(last[0].Content, "earlier") ||
		!strings.Contains(last[0].Content, "reads at most 2 in one request. What it shows: a dog on a sofa") {
		t.Errorf("an older picture should go as its description: %q", last[0].Content)
	}
	if len(msgs[1].Images) != 3 || msgs[0].Content != "earlier" {
		t.Error("the caller's messages were changed")
	}

	// Kept: the next request, streamed this time, is trimmed before it goes,
	// and the descriptions come from the cache.
	from := len(stub.sent)
	if _, err := r.ChatStream(context.Background(), msgs, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(counts(from)); got != "[2]" {
		t.Errorf("after learning the limit, pictures per call = %s, want [2]", got)
	}
}

func TestAPictureLimitIsReadFromTheRefusal(t *testing.T) {
	for err, want := range map[error]int{
		&APIError{Provider: "vllm", StatusCode: 400, Message: "At most 2 image(s) may be provided in one prompt. (parameter=image)"}: 2,
		errors.New("at most 5 images may be provided in one prompt"):                                                                 5,
		errors.New("vllm api error (400): maximum context length exceeded"):                                                          0,
	} {
		if got := imageLimitFrom(err); got != want {
			t.Errorf("%v: limit %d, want %d", err, got, want)
		}
	}
	if imageLimitFrom(nil) != 0 {
		t.Error("no error, no limit")
	}
}

// A limit lowered while gohort runs is learned again from the next refusal.
func TestALoweredPictureLimitIsLearnedAgain(t *testing.T) {
	describeMu.Lock()
	describeCache, describeOrder = map[[32]byte]string{}, nil
	describeMu.Unlock()
	stub := &capLLM{max: 2}
	r := &retryLLM{inner: stub, maxRetries: 1}
	msgs := []Message{{Role: "user", Content: "four", Images: [][]byte{[]byte("1"), []byte("2"), []byte("3"), []byte("4")}}}
	if _, err := r.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	stub.max = 1
	if _, err := r.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if got := r.imageLimit.Load(); got != 1 {
		t.Errorf("limit %d, want the lowered 1", got)
	}
}

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

// recordingChatServer answers every chat call with a short reply and keeps
// each request body it was sent.
func recordingChatServer(t *testing.T) (url string, bodies func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"qwen","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), got...)
	}
}

// vLLM is the llama.cpp client minus thinking_budget_tokens. vLLM ignores that
// field (a 128-token budget still thought for thousands), so sending it only
// padded max_tokens for a cap nothing enforced. Effort still reaches the model,
// through the chat template, and the no-think switch still goes out.
func TestVLLMSendsEffortButNoBudget(t *testing.T) {
	for _, c := range []struct {
		provider   string
		wantBudget bool
	}{
		{"vllm", false},
		{"llama.cpp", true},
	} {
		url, bodies := recordingChatServer(t)
		llm, err := NewLLMFromConfig(LLMProviderConfig{
			Provider: c.provider, Model: "qwen", Endpoint: url,
			ThinkingBudget: 4096, NoThinkUseKwarg: true, NoThinkSendBudget: true,
		})
		if err != nil {
			t.Fatalf("%s: build: %v", c.provider, err)
		}
		msgs := []Message{{Role: "user", Content: "hi"}}
		if _, err := llm.Chat(t.Context(), msgs, WithEffort("low")); err != nil {
			t.Fatalf("%s: effort call: %v", c.provider, err)
		}
		if _, err := llm.Chat(t.Context(), msgs, WithThink(false)); err != nil {
			t.Fatalf("%s: no-think call: %v", c.provider, err)
		}
		got := bodies()
		if len(got) != 2 {
			t.Fatalf("%s: %d calls reached the server, want 2", c.provider, len(got))
		}
		for i, b := range got {
			if _, has := b["thinking_budget_tokens"]; has != c.wantBudget {
				t.Errorf("%s call %d: thinking_budget_tokens present=%v, want %v (body %v)", c.provider, i, has, c.wantBudget, b)
			}
		}
		kw, _ := got[0]["chat_template_kwargs"].(map[string]any)
		if kw["reasoning_effort"] != "low" || kw["enable_thinking"] != true {
			t.Errorf("%s: effort call kwargs %v, want reasoning_effort=low with thinking on", c.provider, kw)
		}
		kw, _ = got[1]["chat_template_kwargs"].(map[string]any)
		if kw["enable_thinking"] != false {
			t.Errorf("%s: no-think call kwargs %v, want enable_thinking=false", c.provider, kw)
		}
	}
}

// The rest of the framework treats vLLM as the local server it is: native
// tool calls, a model this instance may lend to a peer, and its own name in
// logs and errors.
func TestVLLMIsALocalServer(t *testing.T) {
	if !ProviderHasNativeTools("vllm") {
		t.Error("vLLM calls tools natively; without this its tools would be parsed out of prose")
	}
	if !peerLendableProviders["vllm"] {
		t.Error("a local vLLM model should be lendable to peers, as llama.cpp is")
	}
	if ok, why := ProviderLooksPrivate("vllm", "http://localhost:8000/v1"); !ok {
		t.Errorf("a loopback vLLM endpoint read as not private: %s", why)
	}
	if ok, _ := ProviderLooksPrivate("vllm", "https://api.example.com/v1"); ok {
		t.Error("a public vLLM endpoint read as private: the endpoint, not the name, decides")
	}
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	oc, ok := unwrapOpenAIClient(llm)
	if !ok {
		t.Fatal("vllm did not build the OpenAI-compatible client")
	}
	if oc.endpoint != "http://localhost:8000/v1" || oc.provider() != "vllm" {
		t.Errorf("endpoint %q tag %q, want vLLM's default port and its own name", oc.endpoint, oc.provider())
	}
}

// unwrapOpenAIClient digs the OpenAI-compatible client out of the wrappers
// NewLLMFromConfig puts around it.
func unwrapOpenAIClient(l LLM) (*openAIClient, bool) {
	for i := 0; i < 8 && l != nil; i++ {
		switch v := l.(type) {
		case *openAIClient:
			return v, true
		case *retryLLM:
			l = v.inner
		default:
			return nil, false
		}
	}
	return nil, false
}

// A system message past the first one goes to a local server as a user turn,
// in the same place: Qwen's chat template refuses a system message anywhere
// but first, and the guardrail's steer sits just before the request. OpenAI's
// own API keeps it as written.
func TestALaterSystemMessageReachesALocalServerAsAUserTurn(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "ok"},
		{Role: "system", Content: "Constraint on this reply"}, {Role: "user", Content: "now"}}
	roles := func(c *openAIClient) string {
		var r []string
		for _, m := range c.buildMessages(ChatConfig{SystemPrompt: "persona"}, msgs) {
			r = append(r, m.Role)
		}
		return strings.Join(r, ",")
	}
	for name, c := range map[string]*openAIClient{"vllm": {vllm: true}, "llama.cpp": {llamacpp: true}} {
		if got := roles(c); got != "system,user,assistant,user,user" {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := roles(&openAIClient{}); got != "system,user,assistant,system,user" {
		t.Errorf("openai: %s", got)
	}
}

// A tool that arrived mid-turn (RenderLate) leaves the tools block on vLLM,
// whose template ignores lazy_tool_names and would put it at the top, where
// adding it invalidates the whole cached prompt. It is described in a note at
// the end instead, so the tools block, and the prompt up to the conversation's
// tail, is the same as the round before it was loaded. llama.cpp's split
// template keeps it in the block, named in lazy_tool_names.
func TestALateToolLeavesTheToolsBlockOnVLLM(t *testing.T) {
	early := Tool{Name: "web_search", Description: "Search.", Parameters: map[string]ToolParam{"q": {Type: "string"}}}
	late := Tool{Name: "recall_about", Description: "Recall.", Parameters: map[string]ToolParam{"name": {Type: "string", Description: "Who"}}, RenderLate: true}
	msgs := []Message{{Role: "user", Content: "hi"}}
	for _, c := range []struct {
		provider  string
		stream    bool
		wantBlock []string
	}{
		{"vllm", false, []string{"web_search"}},
		{"vllm", true, []string{"web_search"}},
		{"llama.cpp", false, []string{"web_search", "recall_about"}},
	} {
		url, bodies := recordingChatServer(t)
		llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: c.provider, Model: "qwen", Endpoint: url})
		if err != nil {
			t.Fatal(err)
		}
		if c.stream {
			llm.ChatStream(t.Context(), msgs, func(string) {}, WithTools([]Tool{early, late}))
		} else {
			llm.Chat(t.Context(), msgs, WithTools([]Tool{early, late}))
		}
		got := bodies()
		if len(got) == 0 {
			t.Fatalf("%s: no request", c.provider)
		}
		body := got[len(got)-1]
		var block []string
		tools, _ := body["tools"].([]any)
		for _, tl := range tools {
			fn, _ := tl.(map[string]any)["function"].(map[string]any)
			block = append(block, fmt.Sprint(fn["name"]))
		}
		if strings.Join(block, ",") != strings.Join(c.wantBlock, ",") {
			t.Errorf("%s stream=%v: tools block %v, want %v", c.provider, c.stream, block, c.wantBlock)
		}
		sent, _ := body["messages"].([]any)
		last, _ := sent[len(sent)-1].(map[string]any)
		lastText := fmt.Sprint(last["content"])
		if c.provider == "vllm" {
			if !strings.Contains(lastText, `"name":"recall_about"`) || !strings.Contains(lastText, "Who") {
				t.Errorf("%s: the late tool is not described at the end: %d messages, last %q", c.provider, len(sent), lastText)
			}
		} else if strings.Contains(lastText, "recall_about") {
			t.Errorf("%s: a note was added where the template already places late tools", c.provider)
		}
	}
}
