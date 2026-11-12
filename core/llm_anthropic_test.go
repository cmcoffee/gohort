package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cmcoffee/snugforge/kvlite"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestNoThinkingUnlessAsked — a call that does not ask about thinking sends
// nothing, so it is byte-identical and no bill moves by surprise. A call that
// asks for thinking OFF sends the off-switch: leaving it out runs adaptive
// thinking on the newer models, which spent a short check's whole allowance
// and returned no text.
func TestNoThinkingUnlessAsked(t *testing.T) {
	blk, out, max := anthThinkingFor(ChatConfig{}, 8192)
	if blk != nil || out != nil || max != 8192 {
		t.Errorf("unset: sent %+v %+v, max %d; want nothing", blk, out, max)
	}
	off := false
	blk, out, max = anthThinkingFor(ChatConfig{Model: "claude-test-off-default", Think: &off}, 8192)
	if blk == nil || blk.Type != "disabled" || out != nil || max != 8192 {
		t.Errorf("off: sent %+v %+v, max %d; want thinking disabled", blk, out, max)
	}
}

// A model that refuses thinking "disabled" is believed about the off-switch
// it names, however the refusal is worded: the API's words and Bedrock's both
// name between_tools for Sonnet 5.5; Opus 5.5 cannot turn thinking off and
// gets no setting, low effort and room to answer. A refusal that names no
// switch, or of the next step down, ends at sending nothing, the model's own
// default. Only a 400 naming what was sent counts, and none of it marks the
// model as wanting adaptive thinking when asked to think.
func TestTheThinkingOffSwitchIsLearned(t *testing.T) {
	off := false
	refuse := func(model, msg string) error {
		return noteIfAdaptiveThinking(model, &APIError{Provider: "bedrock-runtime", StatusCode: 400, Message: msg})
	}
	sent := func(model string, max int) (*anthThinking, *anthOutputConfig, int) {
		return anthThinkingFor(ChatConfig{Model: model, Think: &off}, max)
	}
	for name, msg := range map[string]string{
		"api":     `"thinking.type.disabled" is not supported for this model. Use "thinking.type.between_tools" for the lowest thinking setting, or "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`,
		"bedrock": `To turn thinking off on this model, send "thinking": {"type": "between_tools"} instead of {"type": "disabled"}. The model does not think before responding. The short updates it writes between tool calls come back as thinking blocks.`,
	} {
		model := "claude-test-sonnet-off-" + name
		if err := refuse(model, msg); !refusedThinkingOff(err) {
			t.Errorf("%s: the refusal is not marked for a retry: %v", name, err)
		}
		if blk, out, max := sent(model, 300); blk == nil || blk.Type != "between_tools" || out != nil || max != 300 {
			t.Errorf("%s: sent %+v %+v, max %d; want between_tools", name, blk, out, max)
		}
		if thinkingStyleFor(model) == anthThinkAdaptive {
			t.Errorf("%s: an off-switch refusal marked it adaptive", name)
		}
	}
	opus := "claude-test-opus-off"
	refuse(opus, `"thinking.type.disabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`)
	if blk, out, max := sent(opus, 300); blk != nil || out == nil || out.Effort != "low" || max != anthThinkOffFloor {
		t.Errorf("opus: sent %+v %+v, max %d; want no setting, low effort, max %d", blk, out, max, anthThinkOffFloor)
	}
	// Low effort refused too: nothing, as before any of this.
	refuse(opus, `output_config.effort is not supported for this model`)
	if blk, out, max := sent(opus, 300); blk != nil || out != nil || max != 300 {
		t.Errorf("opus after low effort was refused: sent %+v %+v, max %d; want nothing", blk, out, max)
	}
	// A refusal of disabled that names no switch: nothing.
	odd := "claude-test-odd-off"
	refuse(odd, `thinking type disabled is unavailable here`)
	if blk, out, _ := sent(odd, 300); blk != nil || out != nil {
		t.Errorf("odd: sent %+v %+v; want nothing", blk, out)
	}
	// Not a refusal of what was sent: a thinking-ON refusal, another status.
	plain := "claude-test-plain"
	if err := refuse(plain, `"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive"`); refusedThinkingOff(err) || thinkingOffFor(plain) != anthOffDisabled {
		t.Error("a thinking-on refusal moved the off-switch")
	}
	if err := noteIfAdaptiveThinking(plain, &APIError{StatusCode: 500, Message: "thinking disabled internal error"}); refusedThinkingOff(err) {
		t.Error("a 500 moved the off-switch")
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
	forgetAdaptations(fieldThinkingShape)
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

// Claude bills thinking as output and reports no breakdown, so the thinking
// share is estimated from the thinking text. Before this every Claude call read
// as zero thinking. The text is counted and streamed live, but kept off
// Response.Reasoning, which the agent loop would act on.
func TestStreamedThinkingIsCountedNotReturned(t *testing.T) {
	var live strings.Builder
	st := &anthStreamState{reasoning: func(s string) { live.WriteString(s) }}
	for _, e := range []string{
		`{"type":"message_start","message":{"model":"claude-opus-5","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"` + strings.Repeat("t", 300) + `"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"` + strings.Repeat("a", 100) + `"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":400}}`,
	} {
		st.feed([]byte(e))
	}
	resp := st.response("test")
	if resp.ReasoningTokens != 300 {
		t.Errorf("thinking tokens = %d, want 300 (3/4 of 400 by text share)", resp.ReasoningTokens)
	}
	if resp.Reasoning != "" {
		t.Error("thinking text reached Response.Reasoning, where the loop would act on it")
	}
	if strings.Contains(resp.Content, "ttt") {
		t.Error("thinking text leaked into the reply")
	}
	if live.Len() != 300 {
		t.Errorf("live reasoning stream got %d chars, want 300", live.Len())
	}
}

// The same estimate on a non-streamed reply, and zero where no thinking text
// came back: counted only where it is known.
func TestThinkingEstimateOnAWholeReply(t *testing.T) {
	var result anthResponse
	if err := json.Unmarshal([]byte(`{"model":"claude-opus-5","content":[`+
		`{"type":"thinking","thinking":"`+strings.Repeat("t", 100)+`","signature":"sig"},`+
		`{"type":"text","text":"`+strings.Repeat("a", 100)+`"}],`+
		`"usage":{"output_tokens":200},"stop_reason":"end_turn"}`), &result); err != nil {
		t.Fatal(err)
	}
	if got := parseAnthResponse(result).ReasoningTokens; got != 100 {
		t.Errorf("thinking tokens = %d, want 100", got)
	}
	result.Content = result.Content[1:]
	if got := parseAnthResponse(result).ReasoningTokens; got != 0 {
		t.Errorf("no thinking text, but thinking tokens = %d", got)
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

// The API refuses a system message in first place ("messages.0: use the
// top-level 'system' parameter for the initial system prompt"), and every lead
// call that opened with one ran on the worker. The opening system turns go to
// the top-level system prompt; a later one stays where it was said, which the
// API still takes.
func TestTheOpeningSystemTurnsGoToTheSystemPrompt(t *testing.T) {
	msgs, err := buildAnthMessages([]Message{
		{Role: "user", Content: "  "}, // skipped by the builder, leaving a system turn first
		{Role: "system", Content: "You are terse."},
		{Role: "system", Content: "Answer in English."},
		{Role: "user", Content: "hi"},
		{Role: "system", Content: "The user is on a phone."},
		{Role: "assistant", Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lead, rest := leadingSystemTurns(msgs)
	if lead != "You are terse.\n\nAnswer in English." {
		t.Errorf("lead = %q", lead)
	}
	if len(rest) != 3 || rest[0].Role != "user" || rest[1].Role != "system" {
		t.Fatalf("rest: %+v", rest)
	}
	if lead, rest := leadingSystemTurns(rest); lead != "" || len(rest) != 3 {
		t.Errorf("a conversation that opens with the person lost a turn: %q %d", lead, len(rest))
	}
	// Both ways a request goes out (Chat and Stream) take them out.
	if n := strings.Count(readRepoSource(t, "llm_anthropic.go"), "leadingSystemTurns(msgs)"); n != 2 {
		t.Errorf("leadingSystemTurns is applied on %d send paths, want 2", n)
	}
}

// A user turn's pictures reach Anthropic, and Bedrock's Messages and
// InvokeModel APIs, as image blocks ahead of its text. They were dropped,
// and a turn that was only a picture (view_image's) was skipped whole, so the
// model was never shown it. A picture in a format the API does not take is
// converted; an assistant turn carries none.
func TestAnthropicMessagesCarryImages(t *testing.T) {
	png := smallPNG(t)
	msgs, err := buildAnthMessages([]Message{
		{Role: "user", Content: "What is in this?", Images: [][]byte{png}},
		{Role: "user", Images: [][]byte{png}}, // view_image: a picture and no text
		{Role: "assistant", Content: "A square.", Images: [][]byte{png}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want all 3 (an image-only turn must not be skipped)", len(msgs))
	}
	var first, only []anthContentBlock
	if err := json.Unmarshal(msgs[0].Content, &first); err != nil {
		t.Fatalf("a turn with a picture is not content blocks: %s", msgs[0].Content)
	}
	if len(first) != 2 || first[0].Type != "image" || first[1].Type != "text" || first[1].Text != "What is in this?" {
		t.Fatalf("blocks = %+v, want the image then the text", first)
	}
	if s := first[0].Source; s == nil || s.Type != "base64" || s.MediaType != "image/png" || s.Data == "" {
		t.Errorf("image source = %+v", first[0].Source)
	}
	if err := json.Unmarshal(msgs[1].Content, &only); err != nil || len(only) != 1 || only[0].Type != "image" {
		t.Errorf("the image-only turn: %s", msgs[1].Content)
	}
	if !strings.Contains(string(msgs[2].Content), `"A square."`) || strings.Contains(string(msgs[2].Content), "image") {
		t.Errorf("an assistant turn carried a picture: %s", msgs[2].Content)
	}
}

// End to end: a model that refuses thinking "disabled" the way Sonnet 5.5
// does is asked again with the switch it named, and the call returns its
// answer; the next thinking-off call goes right the first time.
func TestAThinkingOffRefusalIsRetriedWithTheNamedSwitch(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		n := len(sent)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.disabled\" is not supported for this model. Use \"thinking.type.between_tools\" for the lowest thinking setting, or \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior."}}`))
			return
		}
		w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"{\"status\": \"on_task\"}"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":5}}`))
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "anthropic", Model: "claude-test-e2e-off", APIKey: "k", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "judge"}}, WithThink(false), WithMaxTokens(300))
	if err != nil || !strings.Contains(resp.Content, "on_task") {
		t.Fatalf("the call: %v, %+v", err, resp)
	}
	if _, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "again"}}, WithThink(false), WithMaxTokens(300)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 3 || !strings.Contains(sent[0], `"type":"disabled"`) || !strings.Contains(sent[1], `"type":"between_tools"`) || !strings.Contains(sent[2], `"type":"between_tools"`) {
		t.Errorf("sent %d requests:\n%s", len(sent), strings.Join(sent, "\n"))
	}
}

// End to end, the fallback: a model that refuses every off-switch in words
// that name none ends with the setting left out, and the call answers rather
// than failing on its off-switch.
func TestAThinkingOffThatIsNeverAcceptedFallsBackToNothing(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), `"thinking"`) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking: disabled is not a value this deployment takes"}}`))
			return
		}
		w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "anthropic", Model: "claude-test-e2e-nothing", APIKey: "k", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "judge"}}, WithThink(false), WithMaxTokens(300))
	if err != nil || strings.TrimSpace(resp.Content) != "ok" {
		t.Fatalf("the call: %v, %+v", err, resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || strings.Contains(sent[1], `"thinking"`) {
		t.Errorf("sent %d requests:\n%s", len(sent), strings.Join(sent, "\n"))
	}
}
