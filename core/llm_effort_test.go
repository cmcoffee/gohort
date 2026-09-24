package core

// Effort is the everyday reasoning control, provider-neutral: "off", "low",
// "medium", "high". Each client turns it into its own dial. The raw token
// budget stays as the advanced override, and a caller that turned thinking off
// meant it. These pin the precedence and every provider's mapping, because a
// mapping that goes wrong does not fail - it just spends the wrong amount.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

func effortCfg(opts ...ChatOption) ChatConfig {
	var cfg ChatConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

func TestResolveEffortPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		opts      []ChatOption
		def, max  string
		wantLevel string
		wantThink string // "", "on", "off"
	}{
		{"nothing set stays untouched", nil, "", "", "", ""},
		{"tier default applies", nil, "medium", "", "medium", "on"},
		{"per-call beats tier default", []ChatOption{WithEffort("high")}, "low", "", "high", "on"},
		{"explicit budget wins over per-call", []ChatOption{WithEffort("high"), WithThinkBudget(900)}, "", "", "", ""},
		{"explicit budget wins over tier default", []ChatOption{WithThinkBudget(900)}, "high", "", "", ""},
		{"think=false wins over per-call", []ChatOption{WithThink(false), WithEffort("high")}, "", "", "", "off"},
		{"think=false wins over tier default", []ChatOption{WithThink(false)}, "high", "", "", "off"},
		{"max caps a per-call level", []ChatOption{WithEffort("high")}, "", "low", "low", "on"},
		{"max caps a tier default", nil, "high", "medium", "medium", "on"},
		{"max leaves a lower level alone", []ChatOption{WithEffort("low")}, "", "high", "low", "on"},
		{"off disables thinking", []ChatOption{WithEffort("off")}, "", "", "off", "off"},
		{"off disables even an asked-for think", []ChatOption{WithThink(true), WithEffort("off")}, "", "", "off", "off"},
		{"a tier default of off does not veto an explicit think", []ChatOption{WithThink(true)}, "off", "", "", "on"},
		{"a tier default of off applies to a call that did not ask", nil, "off", "", "off", "off"},
		{"not a level reads as unset", []ChatOption{WithEffort("maximum")}, "", "", "", ""},
		{"levels are normalized", []ChatOption{WithEffort(" High ")}, "", "", "high", "on"},
	}
	for _, c := range cases {
		cfg := effortCfg(c.opts...)
		resolveEffort(&cfg, c.def, c.max)
		if cfg.Effort != c.wantLevel {
			t.Errorf("%s: effort = %q, want %q", c.name, cfg.Effort, c.wantLevel)
		}
		got := ""
		if cfg.Think != nil {
			got = map[bool]string{true: "on", false: "off"}[*cfg.Think]
		}
		if got != c.wantThink {
			t.Errorf("%s: think = %q, want %q", c.name, got, c.wantThink)
		}
	}
}

// --- Claude: direct, Bedrock Messages API, Bedrock InvokeModel ---------------

const anthOKBody = `{"content":[{"type":"text","text":"ok"}],"model":"m","usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"end_turn"}`

// captureServer answers every request with body and records what was sent.
func captureServer(t *testing.T, status int, reply func(sent []byte) (int, string)) (*httptest.Server, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		code, out := status, anthOKBody
		if reply != nil {
			code, out = reply(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), bodies...)
	}
}

// claudeBodyFor sends one call through the named Claude path with the given
// tier default and returns the request body it sent.
func claudeBodyFor(t *testing.T, path, model, tierDefault string, opts ...ChatOption) map[string]any {
	t.Helper()
	srv, sent := captureServer(t, http.StatusOK, nil)
	host := strings.TrimPrefix(srv.URL, "http://")
	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	tier := effortTier{def: tierDefault}
	var llm LLM
	switch path {
	case "direct":
		c := newAnthropicLLM("test-key", model, api).(*anthropicClient)
		c.api.Server = host
		c.effort = tier
		llm = c
	case "bedrock-messages":
		l, err := newBedrockLLM("test-token", model, "us-west-2", "", host, api)
		if err != nil {
			t.Fatal(err)
		}
		l.(*anthropicClient).effort = tier
		llm = l
	case "bedrock-invoke":
		l, err := newBedrockRuntimeLLM("test-token", model, "us-west-2", "", host, api)
		if err != nil {
			t.Fatal(err)
		}
		l.(*bedrockRuntimeClient).effort = tier
		llm = l
	}
	if _, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, opts...); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	bodies := sent()
	if len(bodies) != 1 {
		t.Fatalf("%s: %d requests, want 1", path, len(bodies))
	}
	var out map[string]any
	if err := json.Unmarshal(bodies[0], &out); err != nil {
		t.Fatalf("%s: body is not JSON: %v", path, err)
	}
	return out
}

func thinkingOf(body map[string]any) (typ string, budget float64, effort string) {
	if th, ok := body["thinking"].(map[string]any); ok {
		typ, _ = th["type"].(string)
		budget, _ = th["budget_tokens"].(float64)
	}
	if oc, ok := body["output_config"].(map[string]any); ok {
		effort, _ = oc["effort"].(string)
	}
	return
}

// The deployment runs Claude through Bedrock InvokeModel, so all three paths
// are checked on the wire rather than through the shared builder alone.
func TestClaudeAdaptiveModelsGetTheEffortItself(t *testing.T) {
	for _, path := range []string{"direct", "bedrock-messages", "bedrock-invoke"} {
		clearAdaptive(t)
		model := "us.anthropic.claude-test-adaptive"
		noteAdaptiveThinking(model)
		noteAdaptiveThinking(bedrockModelID(model))

		body := claudeBodyFor(t, path, model, "", WithEffort("high"))
		typ, budget, effort := thinkingOf(body)
		if typ != anthThinkAdaptive || effort != "high" || budget != 0 {
			t.Errorf("%s: thinking=%q budget=%v effort=%q, want adaptive with output_config.effort=high", path, typ, budget, effort)
		}
		// The tier default reaches the wire the same way.
		body = claudeBodyFor(t, path, model, "low")
		if typ, _, effort := thinkingOf(body); typ != anthThinkAdaptive || effort != "low" {
			t.Errorf("%s: tier default low sent thinking=%q effort=%q", path, typ, effort)
		}
		// A bare budget still translates through budgetAsEffort.
		body = claudeBodyFor(t, path, model, "high", WithThink(true), WithThinkBudget(1024))
		if _, _, effort := thinkingOf(body); effort != "low" {
			t.Errorf("%s: an explicit budget should win over the tier default: effort=%q", path, effort)
		}
	}
	clearAdaptive(t)
}

func TestClaudeBudgetedModelsGetTheBudgetTable(t *testing.T) {
	clearAdaptive(t)
	want := map[string]float64{"low": 1024, "medium": 4096, "high": 12288}
	for _, path := range []string{"direct", "bedrock-messages", "bedrock-invoke"} {
		for level, budget := range want {
			body := claudeBodyFor(t, path, "us.anthropic.claude-test-budgeted", "", WithEffort(level))
			typ, got, effort := thinkingOf(body)
			if typ != anthThinkBudgeted || got != budget || effort != "" {
				t.Errorf("%s %s: thinking=%q budget=%v effort=%q, want enabled/%v and no output_config", path, level, typ, got, effort, budget)
			}
			if max, _ := body["max_tokens"].(float64); max <= got {
				t.Errorf("%s %s: max_tokens %v does not exceed the budget %v", path, level, max, got)
			}
		}
		body := claudeBodyFor(t, path, "us.anthropic.claude-test-budgeted", "high", WithEffort("off"))
		if _, ok := body["thinking"]; ok {
			t.Errorf("%s: effort off still sent a thinking block: %v", path, body["thinking"])
		}
		if _, ok := body["output_config"]; ok {
			t.Errorf("%s: effort off still sent output_config", path)
		}
	}
}

// --- OpenAI reasoning_effort -------------------------------------------------

const oaiOKBody = `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

func TestOpenAISendsReasoningEffortAndBelievesARefusal(t *testing.T) {
	noReasoningEffortMu.Lock()
	noReasoningEffortModels = map[string]bool{}
	noReasoningEffortMu.Unlock()

	srv, sent := captureServer(t, http.StatusOK, func(b []byte) (int, string) {
		if strings.Contains(string(b), `"model":"plain-model"`) && strings.Contains(string(b), "reasoning_effort") {
			return http.StatusBadRequest, `{"error":{"message":"Unsupported parameter: 'reasoning_effort' is not supported with this model."}}`
		}
		return http.StatusOK, oaiOKBody
	})
	api := func() *apiclient.APIClient { return &apiclient.APIClient{URLScheme: "http", VerifySSL: false} }
	msgs := []Message{{Role: "user", Content: "hi"}}

	// A reasoning model takes the level as reasoning_effort, and no `think`.
	reasoner := newOpenAILLM("test-key", "reasoning-model", srv.URL, api()).(*openAIClient)
	if _, err := (&retryLLM{inner: reasoner}).Chat(context.Background(), msgs, WithEffort("medium")); err != nil {
		t.Fatal(err)
	}
	var first map[string]any
	json.Unmarshal(sent()[0], &first)
	if first["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", first["reasoning_effort"])
	}
	if _, ok := first["think"]; ok {
		t.Error("the gohort-local think flag went to a hosted OpenAI endpoint alongside reasoning_effort")
	}

	// A model that refuses it: one 400, one retry without it, then never again.
	plain := &retryLLM{inner: newOpenAILLM("test-key", "plain-model", srv.URL, api()).(*openAIClient)}
	before := len(sent())
	resp, err := plain.Chat(context.Background(), msgs, WithEffort("high"))
	if err != nil {
		t.Fatalf("the refusal was not recovered from: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
	bodies := sent()[before:]
	if len(bodies) != 2 {
		t.Fatalf("%d requests for the refusing model, want the refused one and one retry", len(bodies))
	}
	if strings.Contains(string(bodies[1]), "reasoning_effort") {
		t.Errorf("the retry still carried reasoning_effort: %s", bodies[1])
	}
	before = len(sent())
	if _, err := plain.Chat(context.Background(), msgs, WithEffort("high")); err != nil {
		t.Fatal(err)
	}
	if bodies := sent()[before:]; len(bodies) != 1 || strings.Contains(string(bodies[0]), "reasoning_effort") {
		t.Errorf("the model that refused was sent reasoning_effort again (%d requests)", len(bodies))
	}

	// off never asks for reasoning.
	before = len(sent())
	if _, err := (&retryLLM{inner: reasoner}).Chat(context.Background(), msgs, WithEffort("off")); err != nil {
		t.Fatal(err)
	}
	if b := sent()[before]; strings.Contains(string(b), "reasoning_effort") {
		t.Errorf("effort off still sent reasoning_effort: %s", b)
	}
}

func TestReasoningEffortRefusalIsRecognizedNarrowly(t *testing.T) {
	if !isUnsupportedReasoningEffortErr(&APIError{StatusCode: 400, Message: "Unrecognized request argument supplied: reasoning_effort"}) {
		t.Error("the refusal is not recognized")
	}
	for _, other := range []error{
		nil,
		&APIError{StatusCode: 400, Message: "max_tokens is too large"},
		&APIError{StatusCode: 500, Message: "reasoning_effort backend crashed"},
	} {
		if isUnsupportedReasoningEffortErr(other) {
			t.Errorf("an unrelated failure was read as a reasoning_effort refusal: %v", other)
		}
	}
}

// --- llama.cpp, Gemini, Ollama -----------------------------------------------

func TestLlamacppEffortTableIsClampedByTheCeiling(t *testing.T) {
	for _, c := range []struct {
		ceiling int
		level   string
		want    int
	}{
		{0, "low", 512}, {0, "medium", 2048}, {0, "high", 4096},
		{1024, "low", 512}, {1024, "medium", 1024}, {1024, "high", 1024},
	} {
		oc := &openAIClient{llamacpp: true, llamacppBudget: c.ceiling}
		cfg := effortCfg(WithEffort(c.level))
		resolveEffort(&cfg, "", "")
		got := oc.llamacppThinkBudget(cfg)
		if got == nil || *got != c.want {
			t.Errorf("ceiling %d, %s: budget %v, want %d", c.ceiling, c.level, fmtThinkBudget(got), c.want)
		}
	}
	// off takes the existing no-think path.
	oc := &openAIClient{llamacpp: true, llamacppBudget: 4096, noThinkSendBudget: true}
	cfg := effortCfg(WithEffort("off"))
	resolveEffort(&cfg, "", "")
	if got := oc.llamacppThinkBudget(cfg); got == nil || *got != llamacppNoThinkDefaultBudget {
		t.Errorf("effort off: budget %v, want the no-think cap %d", fmtThinkBudget(got), llamacppNoThinkDefaultBudget)
	}
	// An explicit budget still wins, clamped as before.
	cfg = effortCfg(WithEffort("high"), WithThinkBudget(300))
	resolveEffort(&cfg, "", "")
	if got := oc.llamacppThinkBudget(cfg); got == nil || *got != 300 {
		t.Errorf("explicit budget: %v, want 300", fmtThinkBudget(got))
	}
}

func TestGeminiEffortTable(t *testing.T) {
	c := &geminiClient{thinkingBudget: 2000}
	for level, want := range map[string]int{"low": 1024, "medium": 8192, "high": 24576} {
		cfg := effortCfg(WithEffort(level))
		resolveEffort(&cfg, "", "")
		if got := c.thinkBudgetFor(cfg); got != want {
			t.Errorf("%s: thinkingBudget %d, want %d", level, got, want)
		}
	}
	if got := c.thinkBudgetFor(effortCfg(WithThink(true))); got != 2000 {
		t.Errorf("no effort: %d, want the configured 2000", got)
	}
	cfg := effortCfg(WithEffort("high"), WithThinkBudget(512))
	resolveEffort(&cfg, "", "")
	if got := c.thinkBudgetFor(cfg); got != 512 {
		t.Errorf("explicit budget: %d, want 512", got)
	}
	// off is Think=false, which the request builder turns into thinkingBudget 0
	// on Flash, exactly as DisableThinking does.
	cfg = effortCfg(WithEffort("off"))
	resolveEffort(&cfg, "", "")
	if cfg.Think == nil || *cfg.Think {
		t.Error("effort off did not turn thinking off for Gemini")
	}
}

func TestOllamaEffortIsOnOrOff(t *testing.T) {
	for level, want := range map[string]bool{"off": false, "low": true, "high": true} {
		cfg := effortCfg(WithEffort(level))
		resolveEffort(&cfg, "", "")
		if cfg.Think == nil || *cfg.Think != want {
			t.Errorf("%s: think = %v, want %v", level, cfg.Think, want)
		}
	}
}

// --- the agent loop ----------------------------------------------------------

func TestAgentLoopEffortReachesTheCall(t *testing.T) {
	run := func(cfg AgentLoopConfig) ChatConfig {
		t.Helper()
		fake := &FakeLLM{Turns: []FakeTurn{{Content: "done", Repeat: true}}}
		app := &AppCore{LLM: fake}
		if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, cfg); err != nil {
			t.Fatalf("loop: %v", err)
		}
		return fake.Config(0)
	}
	if got := run(AgentLoopConfig{Effort: "high", MaxRounds: 2}); got.Effort != "high" {
		t.Errorf("the agent's effort did not reach the call: %q", got.Effort)
	}
	// A per-loop budget is the advanced override: effort is not sent at all.
	got := run(AgentLoopConfig{Effort: "high", ThinkBudget: 2048, MaxRounds: 2})
	if got.Effort != "" || got.ThinkBudget == nil || *got.ThinkBudget != 2048 {
		t.Errorf("with a budget: effort=%q budget=%v, want no effort and 2048", got.Effort, got.ThinkBudget)
	}
	if got := run(AgentLoopConfig{MaxRounds: 2}); got.Effort != "" {
		t.Errorf("no effort configured, but %q was sent", got.Effort)
	}
}
