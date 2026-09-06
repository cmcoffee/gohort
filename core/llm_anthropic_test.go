package core

import (
	"encoding/json"
	"errors"
	"github.com/cmcoffee/snugforge/kvlite"
	"strings"
	"testing"
)

// Thinking was honoured by llama.cpp, ollama and Gemini and silently dropped on
// every Claude path — direct Anthropic and both Bedrock modes. A route stage set
// to "lead (thinking)", a per-route budget and a per-agent budget all applied
// perfectly and produced a request with no thinking in it. The control said one
// thing and the model did another, on the tier where reasoning was most wanted.

func thinkOn(budget int) ChatConfig {
	on := true
	cfg := ChatConfig{Think: &on}
	if budget > 0 {
		cfg.ThinkBudget = &budget
	}
	return cfg
}

// TestNoThinkingUnlessAsked — the whole point of shipping this quietly. Every
// existing call must be byte-identical, so no bill moves by surprise.
func TestNoThinkingUnlessAsked(t *testing.T) {
	for name, cfg := range map[string]ChatConfig{
		"unset":    {},
		"disabled": func() ChatConfig { off := false; return ChatConfig{Think: &off} }(),
	} {
		blk, _, max := anthThinkingFor(cfg, 8192)
		if blk != nil {
			t.Errorf("%s: a thinking block was sent anyway: %+v", name, blk)
		}
		if max != 8192 {
			t.Errorf("%s: max_tokens moved from 8192 to %d on a call that asked for nothing", name, max)
		}
	}
}

// TestAskingForThinkingSendsIt — with the framework's own default budget when
// none is named, so a Claude call and a llama.cpp call asked for the same thing
// get the same thing.
func TestAskingForThinkingSendsIt(t *testing.T) {
	blk, _, _ := anthThinkingFor(thinkOn(0), 32000)
	if blk == nil {
		t.Fatal("asking for thinking produced no thinking block")
	}
	if blk.Type != "enabled" {
		t.Errorf("type = %q, want enabled", blk.Type)
	}
	if blk.BudgetTokens != anthDefaultThinkBudget {
		t.Errorf("budget = %d, want the framework default %d", blk.BudgetTokens, anthDefaultThinkBudget)
	}
	if blk, _, _ := anthThinkingFor(thinkOn(12000), 32000); blk.BudgetTokens != 12000 {
		t.Errorf("an explicit budget was not honoured: %d", blk.BudgetTokens)
	}
}

// TestMaxTokensMakesRoomForAnAnswer — max_tokens and the thinking budget share
// one output allowance. A ceiling at or below the budget is rejected outright,
// and one only slightly above it spends the whole turn thinking and returns
// nothing, which reads as the model having failed.
func TestMaxTokensMakesRoomForAnAnswer(t *testing.T) {
	// The default ceiling is BELOW a large budget — the case that would 400.
	blk, _, max := anthThinkingFor(thinkOn(16000), anthDefaultMaxTokens)
	if max <= blk.BudgetTokens {
		t.Fatalf("max_tokens %d does not exceed the budget %d — the request is rejected outright",
			max, blk.BudgetTokens)
	}
	if max != blk.BudgetTokens+anthThinkAnswerHeadroom {
		t.Errorf("max_tokens = %d, want budget + headroom", max)
	}
	// A caller whose ceiling is already generous keeps it.
	if _, _, max := anthThinkingFor(thinkOn(4096), 64000); max != 64000 {
		t.Errorf("a generous ceiling was lowered to %d", max)
	}
}

// TestTheBlockSerializesAsTheAPIExpects — the field names are the contract, and
// a typo here is a silently-ignored parameter rather than an error.
func TestTheBlockSerializesAsTheAPIExpects(t *testing.T) {
	b, err := json.Marshal(anthRequest{Model: "m", MaxTokens: 8192,
		Thinking: &anthThinking{Type: "enabled", BudgetTokens: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"thinking":{"type":"enabled","budget_tokens":4096}`) {
		t.Errorf("thinking did not serialize as the API expects: %s", got)
	}
	// Absent, not null, when off — an explicit null is a different request.
	b, _ = json.Marshal(anthRequest{Model: "m", MaxTokens: 8192})
	if strings.Contains(string(b), "thinking") {
		t.Errorf("a non-thinking request still mentions thinking: %s", b)
	}
}

// TestInvokeModelCarriesItToo — Bedrock's legacy endpoint takes the Messages
// body verbatim, and it is the mode this deployment uses. Leaving it out would
// have fixed the two paths nobody here is on.
func TestInvokeModelCarriesItToo(t *testing.T) {
	b, err := json.Marshal(bedrockInvokeRequest{
		AnthropicVersion: "x", MaxTokens: 8192,
		Thinking: &anthThinking{Type: "enabled", BudgetTokens: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"budget_tokens":4096`) {
		t.Errorf("InvokeModel does not carry the thinking budget: %s", b)
	}
	b, _ = json.Marshal(bedrockInvokeRequest{AnthropicVersion: "x", MaxTokens: 8192})
	if strings.Contains(string(b), "thinking") {
		t.Errorf("a non-thinking InvokeModel request mentions thinking: %s", b)
	}
}

// --- the two shapes ----------------------------------------------------------

// Claude has two thinking APIs. Older models take a token budget; newer ones
// reject that outright — "thinking.type.enabled is not supported for this
// model. Use thinking.type.adaptive and output_config.effort" — and decide
// their own depth from an effort level.

func clearAdaptive(t *testing.T) {
	t.Helper()
	adaptiveThinkMu.Lock()
	adaptiveThinkModels = map[string]bool{}
	adaptiveThinkMu.Unlock()
}

// TestTheProviderIsBelievedAboutItsOwnShape — which shape a model wants is not
// derivable from its id in a way that keeps working: Bedrock ids move
// constantly and a hardcoded list fails closed on everything released after it
// was written. So it is asked, once.
func TestTheProviderIsBelievedAboutItsOwnShape(t *testing.T) {
	clearAdaptive(t)
	cfg := thinkOn(4096)
	cfg.Model = "us.anthropic.claude-opus-4-8"

	blk, out, _ := anthThinkingFor(cfg, 32000)
	if blk.Type != anthThinkBudgeted || out != nil {
		t.Fatalf("the first attempt should use the budgeted shape, got %+v / %+v", blk, out)
	}
	noteAdaptiveThinking(cfg.Model)
	blk, out, _ = anthThinkingFor(cfg, 32000)
	if blk.Type != anthThinkAdaptive {
		t.Errorf("after the model said adaptive, the shape is still %q", blk.Type)
	}
	if out == nil || out.Effort == "" {
		t.Error("adaptive thinking carries no effort — the model has no dial at all then")
	}
	// A budget is meaningless in the adaptive shape and must not be sent.
	if blk.BudgetTokens != 0 {
		t.Errorf("a token budget rode along with adaptive thinking: %d", blk.BudgetTokens)
	}
	// One model's answer must not speak for another.
	other := thinkOn(4096)
	other.Model = "anthropic.claude-3-5-sonnet"
	if b, _, _ := anthThinkingFor(other, 32000); b.Type != anthThinkBudgeted {
		t.Error("one model's rejection changed the shape used for a different model")
	}
}

// TestTheRefusalIsRecognized — matched on the message because the status is a
// plain 400 shared with every other malformed request, and reacting to all of
// those by changing the thinking shape would turn one clear error into two
// confusing ones.
func TestTheRefusalIsRecognized(t *testing.T) {
	real := errors.New(`bedrock-runtime api error (400): "thinking.type.enabled" is not supported ` +
		`for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`)
	if !isUnsupportedThinkingTypeErr(real) {
		t.Error("the reported refusal is not recognized")
	}
	for _, other := range []error{
		nil,
		errors.New("bedrock-runtime api error (400): max_tokens must be greater than thinking.budget_tokens"),
		errors.New("api error (429): too many requests"),
		errors.New("context deadline exceeded"),
	} {
		if isUnsupportedThinkingTypeErr(other) {
			t.Errorf("an unrelated failure was read as a thinking-shape refusal: %v", other)
		}
	}
}

// TestEffortCarriesTheIntent — a budget and an effort are not convertible, and
// pretending otherwise would be worse. What crosses over is what the operator
// meant: less than standard, standard, or noticeably more.
func TestEffortCarriesTheIntent(t *testing.T) {
	for budget, want := range map[int]string{
		1024:  "low",
		4096:  "medium",
		8192:  "medium",
		12288: "high",
		32000: "high",
		0:     "",
	} {
		if got := budgetAsEffort(budget); got != want {
			t.Errorf("budget %d → effort %q, want %q", budget, got, want)
		}
	}
}

// TestAdaptiveDoesNotStealMaxTokens — there is no budget competing for the
// allowance in that shape, so carving headroom out of the caller's ceiling would
// shrink the answer for no reason.
func TestAdaptiveDoesNotStealMaxTokens(t *testing.T) {
	clearAdaptive(t)
	cfg := thinkOn(16000)
	cfg.Model = "m-adaptive"
	noteAdaptiveThinking(cfg.Model)
	if _, _, max := anthThinkingFor(cfg, 8192); max != 8192 {
		t.Errorf("adaptive thinking moved max_tokens to %d", max)
	}
}

// TestTheClientRecordsItsOwnModelName — the fallback was defeated TWICE by
// keying on the wrong copy of the model name: first on an empty per-call
// config, then on the configured string, which is not always what was sent.
// bedrockModelID prefixes a bare name and substitutes a default for an empty
// one, so a cache keyed from the caller's side misses on exactly the
// deployments that need it and the retry rebuilds an identical request forever.
func TestTheClientRecordsItsOwnModelName(t *testing.T) {
	clearAdaptive(t)
	refusal := errors.New(`api error (400): "thinking.type.enabled" is not supported for this model. ` +
		`Use "thinking.type.adaptive" and "output_config.effort"`)

	// The id the CLIENT sends, which may differ from the configured string.
	sent := bedrockModelID("claude-opus-4-8")
	if sent == "claude-opus-4-8" {
		t.Fatalf("the fixture no longer exercises a transformed id: %q", sent)
	}
	if err := noteIfAdaptiveThinking(sent, refusal); err != refusal {
		t.Error("the error was altered on its way out — callers match on it")
	}
	cfg := thinkOn(4096)
	cfg.Model = sent
	if blk, _, _ := anthThinkingFor(cfg, 32000); blk.Type != anthThinkAdaptive {
		t.Error("the model's own answer was recorded under a name its own request builder " +
			"does not use, so every retry rebuilds the request that just failed")
	}
	// An unrelated error records nothing.
	clearAdaptive(t)
	noteIfAdaptiveThinking(sent, errors.New("api error (429): slow down"))
	cfg2 := thinkOn(4096)
	cfg2.Model = sent
	if blk, _, _ := anthThinkingFor(cfg2, 32000); blk.Type != anthThinkBudgeted {
		t.Error("an unrelated failure switched the thinking shape")
	}
}

// A streamed tool call carries its arguments in input_json_delta events, whose
// fragment field is partial_json — NOT text. Reading the wrong field yields a
// correctly-named call with empty arguments, which surfaces as every tool
// answering "required parameter missing" and reads like the model failing to
// send arguments rather than the client failing to read them.
func TestStreamedToolArgumentsComeFromPartialJSON(t *testing.T) {
	st := &anthStreamState{}
	for _, e := range []string{
		`{"type":"message_start","message":{"model":"claude-opus-5","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"web_search"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"world news\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
	} {
		st.feed([]byte(e))
	}
	resp := st.response("test")
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Args["query"]; got != "world news" {
		t.Errorf("args = %+v — the fragments were dropped", resp.ToolCalls[0].Args)
	}
}

// Fragments must concatenate in order: each delta is a slice of one JSON
// document, so a single dropped or reordered piece makes the whole thing
// unparseable and the call arrives empty.
func TestStreamedToolArgumentsConcatenateAcrossManyFragments(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"fetch_url"}}`))
	for _, frag := range []string{`{"url"`, `:"https://`, `example.com`, `/news"}`} {
		st.feed([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` +
			mustJSONString(frag) + `}}`))
	}
	st.feed([]byte(`{"type":"content_block_stop","index":0}`))

	resp := st.response("test")
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Args["url"] != "https://example.com/news" {
		t.Errorf("reassembled args = %+v", resp.ToolCalls)
	}
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The two cache lifetimes are an economic choice, not a preference, so
// the tests are about the things that would silently cost money: the
// default not moving, the beta being declared only when used, and the
// cost model following the setting.

// withLongCache points the tunable store at a scratch DB and sets the
// knob, restoring both afterwards.
func withLongCache(t *testing.T, on bool) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	if on {
		db.Set(WebTable, "tune_prompt_cache_1h", float64(1))
	}
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })
}

func TestDefaultCacheLifetimeIsUnchanged(t *testing.T) {
	withLongCache(t, false)
	cc := ephemeralCache()
	if cc.TTL != "" {
		t.Errorf("the default must stay the 5-minute cache — changing what a deployment is billed on upgrade is not a silent act. Got ttl=%q", cc.TTL)
	}
	// And nothing declares a beta it is not using.
	if betas := promptCacheBetas(); len(betas) != 0 {
		t.Errorf("beta declared while the feature is off: %v", betas)
	}
	// The write multiplier is the 5-minute figure.
	if got := (CostRates{}).EffectiveCacheWriteMultiplier(); got != 1.25 {
		t.Errorf("write multiplier = %v, want 1.25 for the 5-minute cache", got)
	}
}

func TestLongCacheStampsTTLAndDeclaresTheBeta(t *testing.T) {
	withLongCache(t, true)
	cc := ephemeralCache()
	if cc.TTL != "1h" {
		t.Fatalf("ttl = %q, want 1h", cc.TTL)
	}
	if cc.Type != "ephemeral" {
		t.Errorf("type should stay ephemeral: %q", cc.Type)
	}
	// Serialized shape matters — the API reads this, not the struct.
	raw, _ := json.Marshal(cc)
	if !strings.Contains(string(raw), `"ttl":"1h"`) {
		t.Errorf("ttl missing from the wire form: %s", raw)
	}
	if betas := promptCacheBetas(); len(betas) != 1 || betas[0] != extendedCacheTTL {
		t.Errorf("beta not declared for the body-carried path: %v", betas)
	}
	// The cost model must follow, or a deployment that just started
	// writing 2x caches keeps reporting them at 1.25x.
	if got := (CostRates{}).EffectiveCacheWriteMultiplier(); got != 2.0 {
		t.Errorf("write multiplier = %v, want 2.0 under the 1-hour cache", got)
	}
	// An explicit operator setting still wins over the automatic one.
	if got := (CostRates{CacheWriteMultiplier: 1.4}).EffectiveCacheWriteMultiplier(); got != 1.4 {
		t.Errorf("an explicit multiplier must not be overridden: %v", got)
	}
}

// Every breakpoint gets the same lifetime — a mixed set writes part of
// the prefix short-lived and re-primes it while the rest is still warm.
func TestAllBreakpointsShareTheLifetime(t *testing.T) {
	withLongCache(t, true)
	tools := buildAnthTools([]Tool{{Name: "a"}, {Name: "b"}})
	if len(tools) == 0 || tools[len(tools)-1].CacheControl == nil {
		t.Fatal("no breakpoint on the tools block")
	}
	if tools[len(tools)-1].CacheControl.TTL != "1h" {
		t.Error("the tools breakpoint kept the short lifetime")
	}
	sys := buildSystemBlocks("SYSTEM")
	if len(sys) == 0 || sys[0].CacheControl == nil || sys[0].CacheControl.TTL != "1h" {
		t.Error("the system breakpoint kept the short lifetime")
	}
}
