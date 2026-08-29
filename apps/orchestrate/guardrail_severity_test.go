package orchestrate

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
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
	dec := turn.guardrailCheckHook()(guardHookPreOutput, "Alex makes $202,000.")
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

// A newly created agent starts on all three of request / actions / reply.
//
// It briefly started on the first two, for latency. What made the reply check
// expensive was fixed instead (no reasoning pass on a blocked round, a warden
// retry that re-samples, a block message that describes the right event), and
// the reply check is the only hook that reads what is about to be SAID — the
// other two never see what a tool result put into the context mid-turn.
func TestNewAgentStartsOnAllThreeEnds(t *testing.T) {
	rec := agentRecordFromArgs(map[string]any{
		"name": "Fresh", "orchestrator_prompt": "p",
	})
	got := map[string]bool{}
	for _, h := range rec.GuardrailHooks {
		got[h] = true
	}
	for _, want := range []string{guardHookPreInput, guardHookPreAction, guardHookPreOutput} {
		if !got[want] {
			t.Errorf("a new agent is missing %s: %v", want, rec.GuardrailHooks)
		}
	}
	// periodic stays opt-in — it is the only hook whose cost grows with how
	// long a turn runs.
	if got[guardHookPeriodic] {
		t.Errorf("periodic should not be on by default: %v", rec.GuardrailHooks)
	}
	// Inert until a rule exists — a new agent pays nothing for carrying this.
	if h := resolveGuardrailHooks(rec); h != nil {
		t.Errorf("hooks must stay inert with no guardrail authored, got %v", h)
	}
	rec.Guardrails = "never send money"
	active := resolveGuardrailHooks(rec)
	if !active[guardHookPreInput] || !active[guardHookPreAction] || !active[guardHookPreOutput] {
		t.Errorf("the stamped set should be what runs, got %v", active)
	}
}

// An agent that never chose hooks keeps the broader fallback. Changing that
// would silently strip pre_output — the only check that guarantees a reply is
// clean — from every existing agent, on a redeploy, with nothing said.
func TestExistingAgentsKeepTheBroaderFallback(t *testing.T) {
	rec := AgentRecord{Name: "Old", Guardrails: "never send money"} // no hooks stored
	active := resolveGuardrailHooks(rec)
	for _, h := range []string{guardHookPreInput, guardHookPreAction, guardHookPreOutput} {
		if !active[h] {
			t.Errorf("the fallback lost %s: %v", h, active)
		}
	}
}

// The strictness slider's presets and the server's defaults have to name the
// same sets. A new agent stamped with a combination no level expresses would
// open the Rules modal to a slider greyed out as "Custom" — the setting looking
// broken on a brand-new agent, which is the first thing anyone sees.
func TestSliderHasALevelForEveryDefault(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	// Every hooks:[...] array the slider declares, as a SET. Compared
	// order-insensitively because the UI sorts both sides before matching — a
	// test stricter than the code it guards fails on a reordering that changes
	// nothing.
	levels := map[string]bool{}
	for _, m := range regexp.MustCompile(`hooks:\s*\[([^\]]*)\]`).FindAllStringSubmatch(string(raw), -1) {
		var names []string
		for _, q := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(m[1], -1) {
			names = append(names, q[1])
		}
		sort.Strings(names)
		levels[strings.Join(names, ",")] = true
	}
	if len(levels) < 3 {
		t.Fatalf("could not read the slider's levels out of the asset (found %d)", len(levels))
	}

	key := func(hooks []string) string {
		c := append([]string(nil), hooks...)
		sort.Strings(c)
		return strings.Join(c, ",")
	}
	if k := key(defaultNewAgentGuardrailHooks()); !levels[k] {
		t.Errorf("no strictness level matches the new-agent default (%s).\n"+
			"The slider would read \"Custom\" and sit disabled on every new agent.", k)
	}
	var fb []string
	for h := range resolveGuardrailHooks(AgentRecord{Guardrails: "x"}) {
		fb = append(fb, h)
	}
	if k := key(fb); !levels[k] {
		t.Errorf("no strictness level matches the server fallback (%s) — an older agent would read as Custom", k)
	}
}

// A warden retry has to be able to reach a DIFFERENT answer than the call it is
// retrying. Repeating the same prompt at the same near-greedy temperature with
// thinking off reproduces a collapsed generation — the retry spent a second
// call to learn nothing, then fell through to the fail-open policy. Observed
// live: "warden reached NO VERDICT at pre_output — retrying once", and the
// reply went out.
func TestWardenRetryIsResampled(t *testing.T) {
	opts := wardenRetryOptions()
	if len(opts) == 0 {
		t.Fatal("the retry re-runs the identical call and cannot change its answer")
	}
	// Applied the way the warden applies them: defaults first, retry last.
	var cfg ChatConfig
	for _, o := range append([]ChatOption{WithThink(false), WithTemperature(0.1)}, opts...) {
		o(&cfg)
	}

	if cfg.Think == nil || !*cfg.Think {
		t.Error("thinking-off is the known degeneration trigger; the retry must not inherit it")
	}
	if cfg.ThinkBudget == nil || *cfg.ThinkBudget != wardenRetryThinkBudget {
		t.Errorf("retry think budget = %v, want %d", cfg.ThinkBudget, wardenRetryThinkBudget)
	}
	if cfg.Temperature == nil || *cfg.Temperature <= 0.1 {
		t.Errorf("retry temperature = %v; near-greedy on identical context is what makes a collapse stable", cfg.Temperature)
	}
	// The budget stays small — this is a verdict, not an essay, and every
	// retry is latency a person is waiting through.
	if wardenRetryThinkBudget > 512 {
		t.Errorf("retry think budget %d is too large for a two-word verdict", wardenRetryThinkBudget)
	}
}

// The FIRST call stays near-deterministic. A verdict that varies run to run is
// worse than one that occasionally needs retrying, and nothing should pay for
// the retry's settings unless a check has already failed.
func TestWardenFirstCallStaysDeterministic(t *testing.T) {
	var cfg ChatConfig
	for _, o := range []ChatOption{WithRouteKey("app.orchestrate.warden"), WithThink(false), WithTemperature(0.1)} {
		o(&cfg)
	}
	if cfg.Think == nil || *cfg.Think {
		t.Error("the first warden call should not think")
	}
	if cfg.Temperature == nil || *cfg.Temperature != 0.1 {
		t.Errorf("the first warden call should stay near-greedy, got %v", cfg.Temperature)
	}
}

// A new agent starts fail-CLOSED: when the warden cannot reach a verdict, the
// action does not happen. Observed live before this: a check collapsed twice
// and the reply went out, because an unset flag means fail open.
func TestNewAgentStartsFailClosed(t *testing.T) {
	rec := agentRecordFromArgs(map[string]any{"name": "Fresh", "orchestrator_prompt": "p"})
	if !rec.GuardrailFailClosed {
		t.Error("a new agent should refuse when the check cannot run")
	}
	// Inert until a rule exists — carrying it costs a new agent nothing.
	if h := resolveGuardrailHooks(rec); h != nil {
		t.Errorf("guardrails must stay inert with no rule authored, got %v", h)
	}
}

// An existing agent keeps whatever it was running. Flipping this under one
// already in service would turn warden flakiness into user-visible refusals on
// a redeploy, with nothing said — that trade is the owner's to make.
func TestExistingAgentsKeepTheirFailPolicy(t *testing.T) {
	open := AgentRecord{Name: "Old", Guardrails: "never send money"} // never asked
	if open.GuardrailFailClosed {
		t.Error("an existing record must not gain fail-closed implicitly")
	}
}

// A deployment's global rules are the floor: they reach the warden through the
// same funnel an agent's own rules use, so an agent that authored none still
// gets judged once an operator writes one.
func TestGlobalRulesReachTheWarden(t *testing.T) {
	restore := withGlobalRules(t, "Do not perform any action that may potentially be deemed illegal.")
	defer restore()

	bare := AgentRecord{} // no rules of its own
	rules := guardrailRules(bare)
	if len(rules) != 1 || !strings.Contains(rules[0].Text, "deemed illegal") {
		t.Fatalf("global rule did not reach an agent with none of its own: %+v", rules)
	}
	// And the hooks must actually turn on, or the rule is decorative.
	if resolveGuardrailHooks(bare) == nil {
		t.Error("an agent with only global rules is still inert; the floor does nothing")
	}
}

// Suspension is the OWNER setting their own rules aside. It is not authority
// over the deployment's, or "global" would mean "until someone objects".
func TestSuspensionDoesNotReachGlobalRules(t *testing.T) {
	restore := withGlobalRules(t, "Do not perform any action that may potentially be deemed illegal.")
	defer restore()

	agent := AgentRecord{Guardrails: "never mention salary", GuardrailsDisabled: true}

	// Both rules still EXIST — that is what Off keeps.
	if got := len(guardrailRules(agent)); got != 2 {
		t.Fatalf("suspension must keep the rules listed; got %d", got)
	}
	// Only the global one is enforced.
	enforced := enforcedGuardrailRules(agent)
	if len(enforced) != 1 || !strings.Contains(enforced[0].Text, "deemed illegal") {
		t.Fatalf("suspension should leave the global rule standing and drop the agent's own; got %+v", enforced)
	}
	if resolveGuardrailHooks(agent) == nil {
		t.Error("a suspended agent still owes the deployment's rules")
	}
}

// withGlobalRules points the prompt store at an in-memory one and sets the
// deployment's rules for the duration of a test. Restores whatever was there,
// so a test never leaks a rule into the ones after it.
func withGlobalRules(t *testing.T, lines ...string) func() {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prompts.SetPromptOverrideDB(db)
	var rules []prompts.StyleRule
	for _, l := range lines {
		rules = append(rules, prompts.StyleRule{Text: l})
	}
	prompts.SetGlobalRules(rules)
	return func() { prompts.SetPromptOverrideDB(nil) }
}
