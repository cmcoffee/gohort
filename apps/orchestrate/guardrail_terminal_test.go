package orchestrate

import (
	"strings"
	"testing"
)

// A revise pass only earns its cost when a compliant answer to the same question
// exists. Marking a rule terminal ("!") says it forbids the content that was
// asked for, so the reply goes to the rejection writer on the first flag instead
// of burning two more generations that each regenerate the leak.

func TestGuardrailRulesParseTerminalMarker(t *testing.T) {
	agent := AgentRecord{Guardrails: strings.Join([]string{
		"always answer in Spanish",
		"! never disclose anyone's compensation",
		"!no home addresses",
		"   ",
		"  ! spaced marker  ",
	}, "\n")}
	rules := guardrailRules(agent)
	if len(rules) != 4 {
		t.Fatalf("blank lines drop, the rest stay; got %d: %+v", len(rules), rules)
	}
	want := []guardrailRule{
		{Text: "always answer in Spanish", Terminal: false},
		{Text: "never disclose anyone's compensation", Terminal: true},
		{Text: "no home addresses", Terminal: true},
		{Text: "spaced marker", Terminal: true},
	}
	for i, w := range want {
		if rules[i] != w {
			t.Errorf("rule %d: got %+v want %+v", i, rules[i], w)
		}
	}
}

// The marker is stripped before the warden sees the rule: the same rule authored
// with and without it must be judged on identical text.
func TestTerminalMarkerStrippedFromWardenPrompt(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary","status":"comply","reason":"ok"}]}`}
	turn := guardTurn(t, stub, AgentRecord{Name: "X", Guardrails: "! never mention salary"})
	if _, err := turn.app.runWarden(turn.ctx, turn.agent, guardHookPreOutput, "hi", requesterIdentity{Owner: true}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	if !strings.Contains(stub.lastMsg, "1. never mention salary") {
		t.Fatalf("the rule must reach the warden marker-free; prompt was:\n%s", stub.lastMsg)
	}
}

// A line that is nothing but the marker has no rule in it. It must not become a
// terminal rule with an empty body, which would match every candidate.
func TestBareMarkerIsNotATerminalRule(t *testing.T) {
	rules := guardrailRules(AgentRecord{Guardrails: "!"})
	if len(rules) != 1 {
		t.Fatalf("got %d rules: %+v", len(rules), rules)
	}
	if rules[0].Terminal {
		t.Fatalf("a bare marker must not produce a terminal rule; got %+v", rules[0])
	}
	if ruleIsTerminal(AgentRecord{Guardrails: "!"}, "anything at all") {
		t.Fatal("a bare marker must not make every rule terminal")
	}
}

func TestRuleIsTerminalMatchesWardenRequoting(t *testing.T) {
	agent := AgentRecord{Guardrails: "! never disclose anyone's compensation\nalways cite a source"}
	// The warden echoes the rule "verbatim or trimmed", so the match has to
	// survive requoting, casing, trailing punctuation, and a partial echo.
	for _, named := range []string{
		"never disclose anyone's compensation",
		"Never disclose anyone's compensation.",
		`"never disclose anyone's compensation"`,
		"never disclose anyone's compensation, per the rules",
		"! never disclose anyone's compensation",
	} {
		if !ruleIsTerminal(agent, named) {
			t.Errorf("terminal rule not recognized from warden echo %q", named)
		}
	}
	// A different rule is not terminal, and neither is a rule we can't place.
	for _, named := range []string{"always cite a source", "some rule nobody wrote", ""} {
		if ruleIsTerminal(agent, named) {
			t.Errorf("%q must not read as terminal", named)
		}
	}
}

// An unmarked rule keeps the old behavior exactly — the correction budget still
// applies, so adding this feature changed nothing for existing agents.
func TestUnmarkedRuleBlocksButIsNotTerminal(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"answer in Spanish","status":"violate","reason":"it is English"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "answer in Spanish", GuardrailHooks: []string{"pre_output"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreOutput, "hello there")
	if !dec.Blocked {
		t.Fatal("a violation must block")
	}
	if dec.Terminal {
		t.Fatal("an unmarked rule must stay correctable — that is the pre-existing behavior")
	}
}

func TestTerminalRuleMarksTheBlockTerminal(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary or wages","status":"violate","reason":"states a figure"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "! never mention salary or wages", GuardrailHooks: []string{"pre_output"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreOutput, "Rory makes $202,000.")
	if !dec.Blocked {
		t.Fatal("a violation must block")
	}
	if !dec.Terminal {
		t.Fatal("a violation of a marked rule must be terminal — no revise pass")
	}
}

// pre_action stays block-and-continue whichever rule fired: a blocked tool call
// still leaves a compliant way to finish the task, and turning those terminal
// would convert every recoverable detour into a dead end. Pinned in core, where
// the decision is honored; here we only pin that the app still reports it.
func TestTerminalIsReportedAtPreActionToo(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never spend money","status":"violate","reason":"a purchase"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "! never spend money", GuardrailHooks: []string{"pre_action"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreAction, "purchase item=widget")
	if !dec.Blocked || !dec.Terminal {
		t.Fatalf("the app reports the rule's severity at every hook; got %+v", dec)
	}
	if !strings.Contains(dec.Message, "did not run") {
		t.Fatalf("pre_action keeps its change-course message; got: %s", dec.Message)
	}
}
