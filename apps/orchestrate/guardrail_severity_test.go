package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Guardrails are TERMINAL by default: a violation ends the turn and the rejection
// writer answers, with no attempt to talk the agent into a compliant version. The
// marker names the exception — "?" for the rare rule that shapes an answer rather
// than forbidding it, where a retry can genuinely satisfy it.
//
// Which way round the marker goes is a safety property, not a preference. See
// TestUnmatchedRuleStaysBlocking.

func TestGuardrailRulesParseSeverityMarker(t *testing.T) {
	agent := AgentRecord{Guardrails: strings.Join([]string{
		"never disclose anyone's compensation",
		"? always answer in Spanish",
		"?no bullet lists",
		"   ",
		"  ? spaced marker  ",
		// Legacy "!" meant non-negotiable back when correctable was the default.
		// Blocking is the default now, so it means the same thing and must keep
		// working — stripped so the warden judges the rule, not the punctuation.
		"! no home addresses",
	}, "\n")}
	rules := guardrailRules(agent)
	if len(rules) != 5 {
		t.Fatalf("blank lines drop, the rest stay; got %d: %+v", len(rules), rules)
	}
	want := []guardrailRule{
		{Text: "never disclose anyone's compensation", Correctable: false},
		{Text: "always answer in Spanish", Correctable: true},
		{Text: "no bullet lists", Correctable: true},
		{Text: "spaced marker", Correctable: true},
		{Text: "no home addresses", Correctable: false},
	}
	for i, w := range want {
		// Compared field-by-field rather than with ==: guardrailRule carries the
		// linked-exception names, and a struct holding a slice is not comparable.
		got := rules[i]
		if got.Text != w.Text || got.Correctable != w.Correctable || got.Contestable != w.Contestable ||
			got.ExceptAuthorized != w.ExceptAuthorized || len(got.Links) != len(w.Links) {
			t.Errorf("rule %d: got %+v want %+v", i, got, w)
		}
	}
}

// The marker is stripped before the warden sees the rule: the same rule authored
// with and without it must be judged on identical text.
func TestSeverityMarkerStrippedFromWardenPrompt(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary","status":"comply","reason":"ok"}]}`}
	turn := guardTurn(t, stub, AgentRecord{Name: "X", Guardrails: "? never mention salary"})
	if _, err := turn.app.runWarden(turn.ctx, turn.agent, guardHookPreOutput, "hi", requesterIdentity{Owner: true}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	if !strings.Contains(stub.lastMsg, "1. never mention salary") {
		t.Fatalf("the rule must reach the warden marker-free; prompt was:\n%s", stub.lastMsg)
	}
}

// A line that is nothing but the marker has no rule in it. It must not become a
// blocking rule with an empty body, which would match every candidate.
func TestBareMarkerIsNotARule(t *testing.T) {
	for _, marker := range []string{"?", "!"} {
		rules := guardrailRules(AgentRecord{Guardrails: marker})
		if len(rules) != 1 {
			t.Fatalf("%q: got %d rules: %+v", marker, len(rules), rules)
		}
		if rules[0].Correctable {
			t.Fatalf("%q: a bare marker must not produce a correctable rule with an empty body that matches everything; got %+v", marker, rules[0])
		}
		if ruleIsCorrectable(AgentRecord{Guardrails: marker}, "anything at all") {
			t.Fatalf("%q: a bare marker must not make every rule correctable", marker)
		}
	}
}

func TestRuleIsCorrectableMatchesWardenRequoting(t *testing.T) {
	agent := AgentRecord{Guardrails: "? never disclose anyone's compensation\nalways cite a source"}
	// The warden echoes the rule "verbatim or trimmed", so the match has to
	// survive requoting, casing, trailing punctuation, and a partial echo.
	for _, named := range []string{
		"never disclose anyone's compensation",
		"Never disclose anyone's compensation.",
		`"never disclose anyone's compensation"`,
		"never disclose anyone's compensation, per the rules",
		"? never disclose anyone's compensation",
	} {
		if !ruleIsCorrectable(agent, named) {
			t.Errorf("correctable rule not recognized from warden echo %q", named)
		}
	}
	// A different rule is not correctable, and neither is a rule we can't place.
	for _, named := range []string{"always cite a source", "some rule nobody wrote", ""} {
		if ruleIsCorrectable(agent, named) {
			t.Errorf("%q must not read as correctable", named)
		}
	}
}

// A rule marked correctable gets the revise pass.
func TestCorrectableRuleGetsItsRevisePass(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"answer in Spanish","status":"violate","reason":"it is English"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "? answer in Spanish", GuardrailHooks: []string{"pre_output"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreOutput, "hello there")
	if !dec.Blocked {
		t.Fatal("a violation must block")
	}
	if !dec.Correctable {
		t.Fatal("a rule marked correctable must get its revise pass")
	}
}

// THE reason the marker names the exception rather than the rule. The warden hands
// back a rule as TEXT ("verbatim or trimmed"), so mapping it to an authored line is
// a fuzzy match — and a fuzzy match can fail. When it does, the rule must stay
// blocking. Marking the blocking case instead would have made a match failure silently
// downgrade a hard limit to a suggestion, in a mechanism whose whole job is to not
// fail open.
func TestUnmatchedRuleStaysBlocking(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"some paraphrase the matcher will not place","status":"violate","reason":"x"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "? answer in Spanish", GuardrailHooks: []string{"pre_output"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreOutput, "anything")
	if !dec.Blocked {
		t.Fatal("a violation must block")
	}
	if dec.Correctable {
		t.Fatal("an unmatchable rule name must fail CLOSED (blocking), never open")
	}
}

func TestUnmarkedRuleBlocks(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary or wages","status":"violate","reason":"states a figure"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "never mention salary or wages", GuardrailHooks: []string{"pre_output"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreOutput, "Rory makes $202,000.")
	if !dec.Blocked {
		t.Fatal("a violation must block")
	}
	if dec.Correctable {
		t.Fatal("an unmarked guardrail must block — that is what the band is for")
	}
}

// pre_action stays block-and-continue whichever rule fired: a blocked tool call
// still leaves a compliant way to finish the task, and ending the turn there
// would convert every recoverable detour into a dead end. Pinned in core, where
// the decision is honored; here we only pin that the app still reports it.
func TestSeverityIsReportedAtPreActionToo(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never spend money","status":"violate","reason":"a purchase"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "never spend money", GuardrailHooks: []string{"pre_action"},
	})
	dec := turn.guardrailCheckHook()(guardHookPreAction, "purchase item=widget")
	if !dec.Blocked || dec.Correctable {
		t.Fatalf("the app reports the rule's severity at every hook; got %+v", dec)
	}
	if !strings.Contains(dec.Message, "did not run") {
		t.Fatalf("pre_action keeps its change-course message; got: %s", dec.Message)
	}
}

// A blocking rule flagged at pre_input refuses the request OUTRIGHT: no model
// runs. Asking the agent to decline is what the COLLAPSE-DIAG was measuring —
// thousands of reasoning tokens spent working out how to refuse without saying
// why — and when the answer is already "no", none of that buys anything.
func TestBlockingRuleAtPreInputRefusesOutright(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary or wages","status":"violate","reason":"asks for pay"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "never mention salary or wages", GuardrailHooks: []string{"pre_input"},
	})
	in := []Message{{Role: "user", Content: "What does the manager earn?"}}
	out, decline := turn.applyInputGuardrail(in)
	if decline == "" {
		t.Fatal("a blocking rule at pre_input must refuse outright, not steer the model")
	}
	// No directive was prepended: the messages are irrelevant now, nothing runs.
	if len(out) != len(in) {
		t.Errorf("a hard block must not also inject a directive; got %d msgs for %d", len(out), len(in))
	}
	// The decline must not give the rule away.
	low := strings.ToLower(decline)
	for _, banned := range []string{"salary", "wage", "guardrail", "rule", "not allowed", "policy"} {
		if strings.Contains(low, banned) {
			t.Errorf("the decline leaks %q: %s", banned, decline)
		}
	}
}

// A CORRECTABLE rule still steers rather than refusing — the agent may well be
// able to answer within the constraint, and killing the turn would turn a
// shaping rule into a hard refusal.
func TestCorrectableRuleAtPreInputStillSteers(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"answer in Spanish","status":"violate","reason":"asked in English"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "? answer in Spanish", GuardrailHooks: []string{"pre_input"},
	})
	in := []Message{{Role: "user", Content: "hello"}}
	out, decline := turn.applyInputGuardrail(in)
	if decline != "" {
		t.Fatalf("a correctable rule must steer, not refuse; got decline %q", decline)
	}
	if len(out) != len(in)+1 || out[len(out)-2].Role != "system" {
		t.Fatal("a correctable rule must inject its directive next to the request")
	}
}

// The rejection writer's output goes straight to whoever asked, and the prompt
// telling it not to name a rule is a request rather than a guarantee. It gets the
// same deterministic leak filter the authored decline lines do, with the canned
// line as the fallback — which is what a stub returning something unusable
// exercises here.
func TestRejectionOutputIsLeakFiltered(t *testing.T) {
	for _, bad := range []string{
		"I can't discuss that because of your rules.",
		"A policy check blocked this one.",
		"That's restricted; try rewording it.",
	} {
		if !declineLeaks(bad) {
			t.Errorf("a decline naming the mechanism must be caught: %q", bad)
		}
	}
	// And an ordinary refusal must survive the filter, or every decline would
	// collapse to the canned pool and the writer would be pointless.
	for _, ok := range []string{
		"That one's a no from me.",
		"Not going to get into that.",
		"I'll skip that one. What else is on your mind?",
	} {
		if declineLeaks(ok) {
			t.Errorf("a clean refusal must pass the filter: %q", ok)
		}
	}
}

// Enforcement had no off switch. Rules are inert only when the FIELD is empty, and
// clearing every hook doesn't help — resolveGuardrailHooks reads an empty set as
// "use the default" and turns three hooks back on. So the only way to stop the
// checks was to delete the rules, i.e. destroy the work to find out whether it was
// causing a wrong refusal.
func TestGuardrailsDisabledIsFullyInert(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Guardrails: "never mention salary\n? answer in Spanish",
		GuardrailHooks:     []string{"pre_input", "pre_action", "pre_output", "periodic"},
		GuardrailsDisabled: true,
	}
	if hooks := resolveGuardrailHooks(agent); hooks != nil {
		t.Fatalf("disabled must resolve to no hooks at all; got %v", hooks)
	}
	for _, h := range []string{guardHookPreInput, guardHookPreAction, guardHookPreOutput, guardHookPeriodic} {
		if guardrailHookActive(agent, h) {
			t.Errorf("hook %s must be inactive while enforcement is off", h)
		}
	}
	// Output guardrails are what force the runner to buffer instead of streaming
	// tokens live, so switching off must hand streaming back.
	if agentHasOutputGuardrail(agent) {
		t.Error("a disabled agent must not be treated as having an output guardrail — live streaming should return")
	}
	// And core must take its no-guardrails fast path: a nil check hook.
	turn := guardTurn(t, &wardenStubLLM{reply: `{"verdicts":[{"rule":"r","status":"violate","reason":"x"}]}`}, agent)
	if turn.guardrailCheckHook() != nil {
		t.Error("a disabled agent must yield a nil check hook (zero overhead)")
	}
	// The pre_input pre-pass must not run either — it is an app-layer call, so a
	// nil check hook alone would not have stopped it.
	in := []Message{{Role: "user", Content: "How much does the manager earn?"}}
	out, decline := turn.applyInputGuardrail(in)
	if decline != "" || len(out) != len(in) {
		t.Errorf("pre_input must be inert while enforcement is off; got decline=%q msgs=%d", decline, len(out))
	}
}

// Off keeps the rules. That is the whole point — the alternative was already
// available by deleting them.
func TestDisabledKeepsTheRules(t *testing.T) {
	agent := AgentRecord{Guardrails: "never mention salary\n? answer in Spanish", GuardrailsDisabled: true}
	rules := guardrailRules(agent)
	if len(rules) != 2 {
		t.Fatalf("the rules must survive being suspended; got %d", len(rules))
	}
	if rules[0].Correctable || !rules[1].Correctable {
		t.Error("severity must survive too")
	}
}

// The zero value enforces. A field that defaulted to off would silently unprotect
// every agent that already has rules.
func TestGuardrailsEnabledByDefault(t *testing.T) {
	agent := AgentRecord{Name: "X", Guardrails: "never mention salary"}
	if agent.GuardrailsDisabled {
		t.Fatal("the zero value must be ENFORCING")
	}
	if resolveGuardrailHooks(agent) == nil {
		t.Fatal("an agent with rules and no explicit hooks must still enforce the default set")
	}
}

// Owner-only, like the rules: a whole-record save must not be able to switch
// enforcement off, or the agent's own edit paths could disable the check they are
// about to be judged by.
func TestDisabledSurvivesWholeRecordSave(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	rec, err := saveAgent(udb, AgentRecord{
		Name: "X", Owner: "u", OrchestratorPrompt: "p",
		Guardrails: "never mention salary", GuardrailsDisabled: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadAgent(udb, rec.ID)
	if !ok {
		t.Fatal("load failed")
	}
	if !got.GuardrailsDisabled {
		t.Error("the suspended state must round-trip through storage")
	}
}
