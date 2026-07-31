package orchestrate

// Named exceptions: authored once, linked to rules with "@name", and rendered
// to the warden on an "Except:" line under the rule they belong to. The point
// of the indirection is that fifteen rules sharing a carve-out get ONE wording,
// so these pin that the wording travels intact and that a broken link fails
// toward the rule applying rather than away from it.

import (
	"strings"
	"testing"
)

func exceptionAgent(rules string) AgentRecord {
	return AgentRecord{
		Name: "X", Owner: "u", Guardrails: rules,
		GuardrailExceptions: []GuardrailException{
			{Name: "confirmed", Text: "the user has already confirmed this in the conversation"},
			{Name: "night-shift", Text: "the request arrived outside business hours"},
		},
	}
}

// TestExceptionLinksParse — "@name" links a named exception; a bare "@" is the
// built-in authorized-people carve-out. The two must not be confused, since one
// is settled by the framework and the other by the warden.
func TestExceptionLinksParse(t *testing.T) {
	cases := []struct {
		line       string
		text       string
		links      []string
		exceptAuth bool
	}{
		{"never send money", "never send money", nil, false},
		{"@confirmed never send money", "never send money", []string{"confirmed"}, false},
		{"@confirmed@night-shift never send money", "never send money", []string{"confirmed", "night-shift"}, false},
		{"@ never send money", "never send money", nil, true},
		{"@ @confirmed never send money", "never send money", []string{"confirmed"}, true},
		{"?@confirmed keep it short", "keep it short", []string{"confirmed"}, false},
	}
	for _, c := range cases {
		got := parseGuardrailRule(c.line)
		if got.Text != c.text {
			t.Errorf("%q → text %q, want %q", c.line, got.Text, c.text)
		}
		if got.ExceptAuthorized != c.exceptAuth {
			t.Errorf("%q → exceptAuthorized=%v, want %v", c.line, got.ExceptAuthorized, c.exceptAuth)
		}
		var names []string
		for _, l := range got.Links {
			names = append(names, l.Name)
		}
		if strings.Join(names, ",") != strings.Join(c.links, ",") {
			t.Errorf("%q → links %v, want %v", c.line, names, c.links)
		}
	}
}

// TestExceptionNameStopsAtTheRule — the name charset has to be narrow or a link
// eats the first word of the rule, which then reads as authored text and is not.
func TestExceptionNameStopsAtTheRule(t *testing.T) {
	got := parseGuardrailRule("@night-shift never page me")
	if got.Text != "never page me" {
		t.Errorf("rule text = %q, want %q", got.Text, "never page me")
	}
	if len(got.Links) != 1 || got.Links[0].Name != "night-shift" {
		t.Errorf("links = %+v, want [night-shift]", got.Links)
	}
}

// TestExceptionTextReachesTheWarden — the linked condition arrives under its
// rule, and the instruction explaining "Except:" arrives with it.
func TestExceptionTextReachesTheWarden(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never send money","status":"comply","reason":"ok"}]}`}
	agent := exceptionAgent("@confirmed never send money\nnever share addresses")
	turn := guardTurn(t, stub, agent)
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreOutput, "hi", requesterIdentity{Owner: true}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	prompt := stub.lastMsg
	if !strings.Contains(prompt, "1. never send money") {
		t.Errorf("rule missing from prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Except: the user has already confirmed this in the conversation") {
		t.Errorf("linked exception text missing from prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "COMPLIED WITH") {
		t.Errorf("the prompt must say what an Except line means:\n%s", prompt)
	}
	// The unlinked rule carries no carve-out. Counted on the INDENTED form:
	// the sentence explaining what an Except line means says the word too.
	if n := strings.Count(prompt, "\n   Except: "); n != 1 {
		t.Errorf("expected exactly one Except line, got %d:\n%s", n, prompt)
	}
	// The name is a handle for linking, never a thing the warden judges.
	if strings.Contains(prompt, "@confirmed") {
		t.Errorf("the link marker leaked into the prompt:\n%s", prompt)
	}
}

// TestNoExceptionsLeavesThePromptAlone — an agent not using the feature must
// pay nothing for it, in prompt bytes or in a rule shape it never sees.
func TestNoExceptionsLeavesThePromptAlone(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never send money","status":"comply","reason":"ok"}]}`}
	agent := AgentRecord{Name: "X", Owner: "u", Guardrails: "never send money"}
	turn := guardTurn(t, stub, agent)
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreOutput, "hi", requesterIdentity{Owner: true}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	if strings.Contains(stub.lastMsg, "Except") {
		t.Errorf("an agent with no exceptions should never see the word:\n%s", stub.lastMsg)
	}
}

// TestDanglingLinkTightensTheRule — deleting an exception must strengthen the
// rules that referenced it, not leave them carrying a condition nobody wrote.
func TestDanglingLinkTightensTheRule(t *testing.T) {
	agent := exceptionAgent("@deleted-one never send money")
	rule := parseGuardrailRule("@deleted-one never send money")
	if got := ruleConditionTexts(agent, rule); len(got) != 0 {
		t.Errorf("a link to a missing exception resolved to %v, want nothing", got)
	}
	// And the warden sees a bare rule rather than a dangling name.
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never send money","status":"comply","reason":"ok"}]}`}
	turn := guardTurn(t, stub, agent)
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreOutput, "hi", requesterIdentity{Owner: true}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	if strings.Contains(stub.lastMsg, "Except") || strings.Contains(stub.lastMsg, "deleted-one") {
		t.Errorf("dangling link reached the warden:\n%s", stub.lastMsg)
	}
}

// TestExceptionTextIsSharedNotCopied — one wording, many rules. This is the
// whole reason the indirection exists.
func TestExceptionTextIsSharedNotCopied(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[]}`}
	agent := exceptionAgent("@confirmed never send money\n@confirmed never delete records")
	turn := guardTurn(t, stub, agent)
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreOutput, "hi", requesterIdentity{Owner: true}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	if n := strings.Count(stub.lastMsg, "the user has already confirmed this in the conversation"); n != 2 {
		t.Errorf("expected the one authored wording under both rules, saw it %d times:\n%s", n, stub.lastMsg)
	}
}

// TestSanitizeGuardrailExceptions — anything carrying a real CONDITION must
// survive the save. The first version dropped an entry whose name was blank,
// which is the worst outcome available: the owner writes a condition, saves,
// gets no error, reopens, and finds nothing there. A name is a handle this code
// can invent, so it invents one; only a missing condition is unrecoverable.
func TestSanitizeGuardrailExceptions(t *testing.T) {
	got := sanitizeGuardrailExceptions([]GuardrailException{
		{Name: "Night Shift", Text: " outside business hours "},
		{Name: "", Text: "the user has already confirmed"}, // no name — derived
		{Name: "no-text", Text: "   "},                     // no condition — dropped
		{Name: "night shift", Text: "collides after slugging"},
		{Name: "  Confirmed!  ", Text: "already confirmed"},
	})
	// Kind defaults to condition: an unspecified kind must never promote an
	// item to "person", which the framework treats as proof of identity.
	want := []GuardrailException{
		{Name: "night-shift", Text: "outside business hours", Kind: guardrailKindCondition},
		{Name: "the-user-has-already", Text: "the user has already confirmed", Kind: guardrailKindCondition},
		{Name: "night-shift-2", Text: "collides after slugging", Kind: guardrailKindCondition},
		{Name: "confirmed", Text: "already confirmed", Kind: guardrailKindCondition},
	}
	if len(got) != len(want) {
		t.Fatalf("kept %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Every stored name must be linkable: whatever slugging or deriving did,
	// "@name" has to parse it back out whole, or the link silently points at
	// nothing and the rule quietly loses its carve-out.
	for _, e := range got {
		rule := parseGuardrailRule("@" + e.Name + " never page me")
		if len(rule.Links) != 1 || rule.Links[0].Name != e.Name || rule.Text != "never page me" {
			t.Errorf("name %q does not round-trip through a link: links=%+v text=%q", e.Name, rule.Links, rule.Text)
		}
	}
	// A collision must not merge two different conditions under one handle.
	if got[0].Text == got[2].Text {
		t.Error("two conditions collapsed onto one name")
	}
}
