package orchestrate

// A rule can say which tool it is about. That does two things, and neither is
// cosmetic: the warden stops being asked about the rule everywhere, and the
// framework stops having to guess the tool from a block that already cost a
// turn.

import (
	"context"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestTheToolMarkerParsesAlongsideTheOthers(t *testing.T) {
	cases := []struct {
		line string
		text string
		tool string
	}{
		{"#send_email never email anyone outside the company", "never email anyone outside the company", "send_email"},
		{"never delegate", "never delegate", ""},
		// Order between markers must not matter — the same rule authored two
		// ways has to be judged the same way.
		{"? #post_message keep it short", "keep it short", "post_message"},
		{"#post_message ?keep it short", "keep it short", "post_message"},
		{"~#agents never dispatch at night", "never dispatch at night", "agents"},
		{"@night-shift #send_email never email out of hours", "never email out of hours", "send_email"},
		// A bare "#" names nothing, so it binds nothing. The rule stays
		// general, which is the SAFE reading: the opposite would let a stray
		// A hyphen IS part of a tool name: validLLMToolName is
		// ^[a-zA-Z0-9_-]{1,128}$, so "send-email" is an ordinary name and the
		// picker offers it. This asserted the opposite, and the truncation it
		// pinned is what made a hyphenated binding reopen as "any action".
		{"#send-email never email", "never email", "send-email"},
	}
	for _, c := range cases {
		r := parseGuardrailRule(c.line)
		if r.Text != c.text || r.Tool != c.tool {
			t.Errorf("parse(%q) = {Text:%q Tool:%q}, want {Text:%q Tool:%q}", c.line, r.Text, r.Tool, c.text, c.tool)
		}
	}
	// Case folded on the way in, so "#Send_Email" and "#send_email" are one
	// binding rather than two that each half-work.
	if got := parseGuardrailRule("#Send_Email never").Tool; got != "send_email" {
		t.Errorf("tool name not folded: %q", got)
	}
}

// The narrowing. A bound rule is judged where it applies and nowhere else.
func TestABoundRuleIsJudgedOnlyForItsTool(t *testing.T) {
	rules := []guardrailRule{
		{Text: "never email outside the company", Tool: "send_email"},
		{Text: "never delegate", Tool: "agents"},
		{Text: "always be polite"},
	}
	names := func(rs []guardrailRule) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Text)
		}
		return strings.Join(out, " | ")
	}
	if got := names(rulesForTool(rules, "send_email")); got != "never email outside the company | always be polite" {
		t.Errorf("a call to send_email must be judged against its own rule and the general ones: %s", got)
	}
	if got := names(rulesForTool(rules, "calculate")); got != "always be polite" {
		t.Errorf("an unrelated tool must not be judged against either binding: %s", got)
	}
	// No tool in play at all. A rule about USING email must not be turned on a
	// reply that merely discusses it — that is how a rule about conduct becomes
	// a rule against a topic.
	if got := names(rulesForTool(rules, "")); got != "always be polite" {
		t.Errorf("pre_input/pre_output/periodic judge no tool call: %s", got)
	}
}

// Which tool a check is about is read off the hook, not guessed.
func TestOnlyPreActionHasAToolInPlay(t *testing.T) {
	if got := wardenToolInPlay(guardHookPreAction, "send_email to=x subject=y"); got != "send_email" {
		t.Errorf("pre_action tool = %q", got)
	}
	for _, hook := range []string{guardHookPreInput, guardHookPreOutput, guardHookPeriodic} {
		if got := wardenToolInPlay(hook, "send_email to=x"); got != "" {
			t.Errorf("%s judges prose, not a call; got tool %q", hook, got)
		}
	}
}

// Every rule bound to some other tool dropping out means there is nothing left
// to ask — and then the warden call itself is skipped, which is the whole
// efficiency argument.
func TestACheckWithNothingToJudgeCostsNoWardenCall(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"r","status":"violate","reason":"x"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "Wren", Guardrails: "#send_email never email outside the company",
		GuardrailHooks: []string{"pre_action"},
	})
	hook := turn.guardrailCheckHook()
	if hook == nil {
		t.Fatal("an agent with a rule must still have a hook")
	}
	if dec := hook(guardHookPreAction, "calculate expr=2+2"); dec.Blocked {
		t.Fatal("a rule bound to send_email must not block a calculate call")
	}
	if stub.seen() != "" {
		t.Errorf("the warden was asked about a call no rule applies to:\n%s", stub.seen())
	}
	// ...and the bound tool is still judged.
	if dec := hook(guardHookPreAction, "send_email to=outside@example.com"); !dec.Blocked {
		t.Error("the rule must still be enforced for its own tool")
	}
}

// The agent is told where a bound rule applies, or it reads a rule about USING
// a tool as a rule about the subject and declines to discuss it.
func TestTheAgentIsToldWhereABoundRuleApplies(t *testing.T) {
	got := renderGuardrailsPromptSection(AgentRecord{
		Name: "Wren", Guardrails: "#send_email never email anyone outside the company\nalways be polite",
	})
	if !strings.Contains(got, "never email anyone outside the company (applies when you use the send_email tool)") {
		t.Errorf("a bound rule must say where it applies:\n%s", got)
	}
	if strings.Contains(got, "always be polite (applies when") {
		t.Errorf("an unbound rule must not claim a binding:\n%s", got)
	}
	// The marker is plumbing; the agent reads the owner's words.
	if strings.Contains(got, "#send_email") {
		t.Errorf("the marker reached the agent:\n%s", got)
	}
}

// A binding only ever NARROWS where a rule is judged, so a name matching
// nothing narrows it to nowhere. Refused at the boundary rather than stored.
func TestABindingToAToolTheAgentCannotCallIsRefused(t *testing.T) {
	agent := AgentRecord{ID: "a1", Name: "Wren", AllowedTools: []string{"calculate"}}
	if err := validateGuardrailToolBindings(agent, "#calculate never do arithmetic for payroll"); err != nil {
		t.Fatalf("a tool the agent has must be accepted: %v", err)
	}
	if err := validateGuardrailToolBindings(agent, "#agents never delegate"); err != nil {
		t.Fatalf("a framework tool the agent carries must be accepted: %v", err)
	}
	err := validateGuardrailToolBindings(agent, "#send_emial never email")
	if err == nil {
		t.Fatal("a rule bound to nothing is enforced nowhere; it must not save")
	}
	if !strings.Contains(err.Error(), "send_emial") {
		t.Errorf("the refusal must name the tool that is wrong: %v", err)
	}
	// An unbound rule is never the validator's business.
	if err := validateGuardrailToolBindings(agent, "never do anything rash"); err != nil {
		t.Fatalf("unbound rules must pass: %v", err)
	}
}

// The picker's list and the validator's allowlist are the same function, so
// the picker cannot offer a name the save will refuse.
func TestTheChoicesAreWhatTheAgentCanActuallyCall(t *testing.T) {
	choices := guardrailToolChoices(AgentRecord{ID: "a1", AllowedTools: []string{"calculate"}})
	has := func(n string) bool {
		for _, c := range choices {
			if c == n {
				return true
			}
		}
		return false
	}
	if !has("calculate") {
		t.Error("an allowlisted tool must be offered")
	}
	// The binding people reach for first, and it is in no registry — built per
	// turn, so a registry-only vocabulary would omit exactly the useful case.
	if !has("agents") {
		t.Error("the framework's per-turn tools must be offerable")
	}
	if has("*") || has(noToolsSentinel) {
		t.Errorf("markers are not tools: %v", choices)
	}
	// An admin who set zero tools gets no bindings to make, but the framework
	// names must not leak back in.
	if got := guardrailToolChoices(AgentRecord{ID: "a1", AllowedTools: []string{noToolsSentinel}}); len(got) != 0 {
		t.Errorf("the no-tools sentinel must offer nothing: %v", got)
	}
}

// The binding names the pair, so the "does this forbid the tool outright"
// question is settled at SAVE — not after a block, a wasted turn, and a
// classification.
func TestABoundRuleIsScopedWhenItIsSavedNotWhenItIsHit(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"the tool only dispatches to other agents"}`, Repeat: true}}}
	app.DB = root
	udb := UserDB(root, "u")
	agent := AgentRecord{ID: "a1", Name: "Wren", Guardrails: "#agents never delegate to other agents",
		GuardrailHooks: []string{"pre_action"}}

	// Synchronous stand-in for what the save fires off the request.
	for _, r := range enforcedGuardrailRules(agent) {
		if r.Tool == "" {
			continue
		}
		scopeAndRecordGuardrailTool(context.Background(), app, udb, "a1", "a1", "", r.Text, r.Tool, r.Tool)
	}

	turn := &chatTurn{app: app, agent: agent, user: "u", udb: udb, ctx: context.Background()}
	withheld := turn.guardrailWithheldTools()
	if withheld["agents"] != "never delegate to other agents" {
		t.Fatalf("the tool should already be gone before the agent ever reaches for it: %v", withheld)
	}
	// And a rule the classifier reads as conditional leaves the tool alone,
	// exactly as it does on the discovered path.
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"some","why":"only for recipients outside the company"}`, Repeat: true}}}
	agent2 := AgentRecord{ID: "a2", Guardrails: "#send_email never email anyone outside the company"}
	scopeAndRecordGuardrailTool(context.Background(), app, udb, "a2", "a2", "",
		"never email anyone outside the company", "send_email", "send_email")
	turn2 := &chatTurn{app: app, agent: agent2, user: "u", udb: udb, ctx: context.Background()}
	if got := turn2.guardrailWithheldTools(); len(got) != 0 {
		t.Errorf("a conditional rule must leave its tool in the catalog: %v", got)
	}
}

// Nothing is asked twice: a save that changes something else must not re-read
// every binding it already has.
func TestSavingAgainDoesNotReAskAboutKnownBindings(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	stub := &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"x"}`, Repeat: true}}}
	app := &OrchestrateApp{}
	app.LLM = stub
	app.DB = root
	udb := UserDB(root, "u")
	agent := AgentRecord{ID: "a1", Guardrails: "#agents never delegate", GuardrailHooks: []string{"pre_action"}}
	saveGuardrailToolScope(udb, "a1", GuardrailToolScope{Rule: "never delegate", Tool: "agents", Scope: guardrailScopeAll})

	app.scopeBoundGuardrailRules(context.Background(), udb, agent)
	if stub.Calls() != 0 {
		t.Errorf("a binding already read must not be asked about again; calls=%d", stub.Calls())
	}
}

// The picker has to be a select, because of which way a typo fails.
func TestTheRulePickerIsASelectNotAField(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, want := range []string{
		"function gTool(",                  // read the binding off the line
		"createElement('select')",          // picked, never typed
		"d.tool_choices",                   // from the live catalog
		"if (tool) { pre += '#' + tool; }", // written back with the other markers
		"(not available)",                  // a binding whose tool is gone is flagged, not dropped
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the rules editor is missing %q", want)
		}
	}
	// Every gCompose CALL has to pass a binding through, or editing a rule's
	// text or severity would silently unbind it — the failure that would look
	// like "the picker doesn't stick".
	calls := 0
	for _, line := range strings.Split(html, "\n") {
		if !strings.Contains(line, "gCompose(") || strings.Contains(line, "function gCompose(") {
			continue
		}
		calls++
		if !strings.Contains(line, "gTool(") && !strings.Contains(line, "toolSel.value") {
			t.Errorf("a gCompose call drops the tool binding:\n%s", strings.TrimSpace(line))
		}
	}
	if calls == 0 {
		t.Error("found no gCompose call sites — this assertion is checking nothing")
	}
}
