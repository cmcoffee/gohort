package core

import (
	"encoding/json"
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
		blk, max := anthThinkingFor(cfg, 8192)
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
	blk, _ := anthThinkingFor(thinkOn(0), 32000)
	if blk == nil {
		t.Fatal("asking for thinking produced no thinking block")
	}
	if blk.Type != "enabled" {
		t.Errorf("type = %q, want enabled", blk.Type)
	}
	if blk.BudgetTokens != anthDefaultThinkBudget {
		t.Errorf("budget = %d, want the framework default %d", blk.BudgetTokens, anthDefaultThinkBudget)
	}
	if blk, _ := anthThinkingFor(thinkOn(12000), 32000); blk.BudgetTokens != 12000 {
		t.Errorf("an explicit budget was not honoured: %d", blk.BudgetTokens)
	}
}

// TestMaxTokensMakesRoomForAnAnswer — max_tokens and the thinking budget share
// one output allowance. A ceiling at or below the budget is rejected outright,
// and one only slightly above it spends the whole turn thinking and returns
// nothing, which reads as the model having failed.
func TestMaxTokensMakesRoomForAnAnswer(t *testing.T) {
	// The default ceiling is BELOW a large budget — the case that would 400.
	blk, max := anthThinkingFor(thinkOn(16000), anthDefaultMaxTokens)
	if max <= blk.BudgetTokens {
		t.Fatalf("max_tokens %d does not exceed the budget %d — the request is rejected outright",
			max, blk.BudgetTokens)
	}
	if max != blk.BudgetTokens+anthThinkAnswerHeadroom {
		t.Errorf("max_tokens = %d, want budget + headroom", max)
	}
	// A caller whose ceiling is already generous keeps it.
	if _, max := anthThinkingFor(thinkOn(4096), 64000); max != 64000 {
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
