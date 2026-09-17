package core

import (
	"strings"
	"testing"
)

func boolRule() PlaybookRule {
	return PlaybookRule{
		Fact: "queue_draining",
		How:  "Read the consumer lag for the orders queue over the last five minutes.",
		Then: "Look at the consumer.",
		Else: "Look at the broker.",
	}
}

// A rule must name its fact and how to establish it, and carry at least one
// arm; a choice covers its values; nesting stops at two levels.
func TestPlaybookProblems(t *testing.T) {
	if p := boolRule().Problems("rule 1", 1); len(p) != 0 {
		t.Fatalf("a sound rule has no problems: %v", p)
	}
	bad := PlaybookRule{Fact: "a fact", Then: "x"}
	p := bad.Problems("rule 1", 1)
	if len(p) != 2 || !strings.Contains(p[0], "one word") || !strings.Contains(p[1], "how is required") {
		t.Fatalf("expected the fact-name and how problems, got %v", p)
	}
	if p := (PlaybookRule{Fact: "f", How: "h"}).Problems("r", 1); len(p) != 1 || !strings.Contains(p[0], "at least one arm") {
		t.Fatalf("an armless bool rule: %v", p)
	}
	choice := PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "fine", "sideways": "?"}}
	p = choice.Problems("r", 1)
	if len(p) != 1 || !strings.Contains(p[0], `"sideways"`) {
		t.Fatalf("a case outside the values: %v", p)
	}
	if p := (PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up"}}).Problems("r", 1); len(p) < 2 {
		t.Fatalf("one value and no arm are two problems: %v", p)
	}
	deep := boolRule()
	deep.Then, deep.ThenRule = "", &PlaybookRule{Fact: "l2", How: "h", ThenRule: &PlaybookRule{Fact: "l3", How: "h", Then: "x"}}
	p = deep.Problems("rule 1", 1)
	if len(p) != 1 || !strings.Contains(p[0], "rule 1.then_rule.then_rule") || !strings.Contains(p[0], "more than 2 levels") {
		t.Fatalf("three levels must be refused by path: %v", p)
	}
	if p := (PlaybookRule{Fact: "f", How: "h", Then: "x", ThenRule: &PlaybookRule{Fact: "g", How: "h", Then: "y"}}).Problems("r", 1); len(p) != 1 || !strings.Contains(p[0], "both prose and a rule") {
		t.Fatalf("an arm is prose or a rule: %v", p)
	}
	// Storage stores: a half-built rule saves (the editor's door), and the
	// record-level check still names it for the doors that refuse.
	if _, err := SaveSkill(memDB(t), "u", SkillRecord{Name: "s", Description: "d", Playbook: []PlaybookRule{bad}}); err != nil {
		t.Fatalf("save must store a half-built rule: %v", err)
	}
	if p := (SkillRecord{Playbook: []PlaybookRule{bad}}).PlaybookProblems(); len(p) == 0 || !strings.HasPrefix(p[0], "rule 1:") {
		t.Fatalf("record-level problems name the rule: %v", p)
	}
}

// The sentence reads the fields back the way the author would say them.
func TestPlaybookSentence(t *testing.T) {
	r := boolRule()
	if got := r.Sentence(); got != "Whenever this skill is consulted, establish queue_draining; if yes, Look at the consumer; if no, Look at the broker." {
		t.Fatalf("got %q", got)
	}
	r.When = []string{"stuck order", "order status"}
	r.Else, r.ElseRule = "", &PlaybookRule{Fact: "broker_up", How: "h", Then: "restart the consumer"}
	if got := r.Sentence(); !strings.HasPrefix(got, "When the message mentions stuck order or order status, establish queue_draining; if yes, Look at the consumer; if no, whenever this skill is consulted, establish broker_up; if yes, restart the consumer.") {
		t.Fatalf("got %q", got)
	}
	c := PlaybookRule{Fact: "state", How: "h", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "fine", "down": "bad"}}
	if got := c.Sentence(); got != "Whenever this skill is consulted, establish state; if up, fine; if down, bad." {
		t.Fatalf("got %q", got)
	}
}

// A rule compiles to a one-phase unattended machine that validates, with
// the fact as a required declared output of the right type and the skill's
// tools on the step.
func TestPlaybookRuleCompilesToAValidMachine(t *testing.T) {
	skill := SkillRecord{Name: "Orders", AllowedTools: []string{"run_command"}, Playbook: []PlaybookRule{boolRule()}}
	def := boolRule().Machine(skill)
	if err := def.Validate(); err != nil {
		t.Fatalf("does not validate: %v", err)
	}
	if !def.Unattended || len(def.Phases) != 1 || def.Phases[0].Name != playbookEstablishPhase {
		t.Fatalf("one unattended establishing phase expected, got %+v", def)
	}
	ph := def.Phases[0]
	if len(ph.Output) != 1 || ph.Output[0].Name != "queue_draining" || ph.Output[0].Type != FieldBool || !ph.Output[0].Required {
		t.Fatalf("the fact must be the required bool output, got %+v", ph.Output)
	}
	if len(ph.Tools) != 1 || ph.Tools[0] != "run_command" {
		t.Fatalf("the step carries the skill's tools, got %v", ph.Tools)
	}
	if !strings.Contains(ph.Prompt, "consumer lag") || !strings.Contains(ph.Prompt, "{input}") {
		t.Fatalf("the prompt carries how and the input: %q", ph.Prompt)
	}
	choice := PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "a", "down": "b"}}
	cdef := choice.Machine(skill)
	if err := cdef.Validate(); err != nil {
		t.Fatalf("choice does not validate: %v", err)
	}
	if cdef.Phases[0].Output[0].Type != FieldString || !strings.Contains(cdef.Phases[0].Output[0].Desc, "up, down") {
		t.Fatalf("a choice fact is a string over its values, got %+v", cdef.Phases[0].Output[0])
	}
}

// Decide reads a bool from a bool or from the words a model writes, a choice
// case-insensitively, and refuses what decides nothing.
func TestPlaybookDecide(t *testing.T) {
	r := boolRule()
	for _, v := range []any{true, "true", "Yes", " y "} {
		if shown, arm, _, ok := r.Decide(v); !ok || shown != "true" || arm != "Look at the consumer." {
			t.Fatalf("%v must decide true: %q %q %v", v, shown, arm, ok)
		}
	}
	if _, arm, _, ok := r.Decide("no"); !ok || arm != "Look at the broker." {
		t.Fatal("no must decide false")
	}
	if _, _, _, ok := r.Decide("maybe"); ok {
		t.Fatal("maybe decides nothing")
	}
	if _, _, _, ok := r.Decide(3); ok {
		t.Fatal("a number decides nothing")
	}
	nested := boolRule()
	nested.Else, nested.ElseRule = "", &PlaybookRule{Fact: "broker_up", How: "h", Then: "x", Else: "y"}
	if _, arm, next, ok := nested.Decide(false); !ok || arm != "" || next == nil || next.Fact != "broker_up" {
		t.Fatal("a rule arm comes back as the nested rule")
	}
	c := PlaybookRule{Fact: "state", How: "h", Type: "choice", Values: []string{"Up", "down"}, Cases: map[string]string{"Up": "fine", "down": "bad"}}
	if shown, arm, _, ok := c.Decide("UP"); !ok || shown != "Up" || arm != "fine" {
		t.Fatalf("choice must match case-insensitively and show the declared value: %q %q %v", shown, arm, ok)
	}
	if _, _, _, ok := c.Decide("left"); ok {
		t.Fatal("a value outside the set decides nothing")
	}
}

// The fallback tells the model to establish the fact itself and gives every
// arm with its condition, nested rules included.
func TestPlaybookFallbackCarriesEveryArm(t *testing.T) {
	r := boolRule()
	r.Else, r.ElseRule = "", &PlaybookRule{Fact: "broker_up", How: "ping it", Then: "restart consumer", Else: "page the on-call"}
	fb := r.Fallback()
	for _, want := range []string{"Establish queue_draining first", "if true: Look at the consumer.", "if false: then Establish broker_up first: ping it", "if true: restart consumer", "if false: page the on-call"} {
		if !strings.Contains(fb, want) {
			t.Fatalf("missing %q in:\n%s", want, fb)
		}
	}
}

// The available-skills list says a playbook skill establishes its facts on
// consult, so the model reaches for it early rather than working them out.
func TestAvailableSkillsNamesPlaybookFacts(t *testing.T) {
	out := RenderAvailableSkills([]SkillRecord{{Name: "Orders", Description: "orders help", Playbook: []PlaybookRule{boolRule()}}})
	if !strings.Contains(out, "playbook: consulting it establishes queue_draining") {
		t.Fatalf("missing playbook note:\n%s", out)
	}
}
