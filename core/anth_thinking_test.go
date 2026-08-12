package core

import (
	"encoding/json"
	"errors"
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
