package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func playbookSkill() SkillRecord {
	return SkillRecord{ID: "s1", Name: "Orders", Playbook: []PlaybookRule{{
		Fact: "queue_draining",
		How:  "Read the consumer lag.",
		Then: "Look at the consumer.",
		Else: "Look at the broker.",
	}}}
}

// The model receives the fact that was established and ONLY the arm that
// applies; the other arm never appears.
func TestPlaybookHandsBackOnlyTheArmThatApplies(t *testing.T) {
	var ran []string
	pr := playbookRunner{msg: "is order 12 stuck?", establish: func(_ context.Context, def MachineDef, input string) (map[string]any, string, error) {
		ran = append(ran, def.Phases[0].Output[0].Name)
		if input != "is order 12 stuck?" {
			t.Fatalf("the establishing step is given the user message, got %q", input)
		}
		return map[string]any{"queue_draining": false}, "lag is 40k and climbing", nil
	}}
	out := pr.resolve(context.Background(), playbookSkill())
	for _, want := range []string{"**Playbook** (Orders", "**Established:** queue_draining = false", "lag is 40k and climbing", "**So:** Look at the broker."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Look at the consumer") {
		t.Fatalf("the arm that does not apply must not be shown:\n%s", out)
	}
	if len(ran) != 1 || ran[0] != "queue_draining" {
		t.Fatalf("one establishing run, got %v", ran)
	}
}

// A nested rule establishes its own fact after the outer one, and a rule
// whose When does not match the turn is skipped without running.
func TestPlaybookNestsAndHonoursWhen(t *testing.T) {
	skill := playbookSkill()
	skill.Playbook[0].Else = ""
	skill.Playbook[0].ElseRule = &PlaybookRule{Fact: "broker_up", How: "ping it", Then: "restart the consumer", Else: "page the on-call"}
	skill.Playbook = append(skill.Playbook, PlaybookRule{When: []string{"refund"}, Fact: "refund_pending", How: "check", Then: "x", Else: "y"})
	var ran []string
	pr := playbookRunner{msg: "order 12 is stuck", establish: func(_ context.Context, def MachineDef, _ string) (map[string]any, string, error) {
		fact := def.Phases[0].Output[0].Name
		ran = append(ran, fact)
		switch fact {
		case "queue_draining":
			return map[string]any{"queue_draining": "no"}, "", nil
		case "broker_up":
			return map[string]any{"broker_up": true}, "broker answers", nil
		}
		return nil, "", errors.New("unexpected fact " + fact)
	}}
	out := pr.resolve(context.Background(), skill)
	if strings.Join(ran, ",") != "queue_draining,broker_up" {
		t.Fatalf("outer then nested, refund rule skipped; ran %v", ran)
	}
	for _, want := range []string{"queue_draining = false", "broker_up = true", "**So:** restart the consumer"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "page the on-call") || strings.Contains(out, "refund") {
		t.Fatalf("unearned arms and unmatched rules must not appear:\n%s", out)
	}
}

// When the fact cannot be established the model is told so and given the
// rule as prose, both arms with their conditions, rather than nothing.
func TestPlaybookFallsBackToProseOnFailure(t *testing.T) {
	pr := playbookRunner{establish: func(context.Context, MachineDef, string) (map[string]any, string, error) {
		return nil, "", errors.New("embedder down")
	}}
	out := pr.resolve(context.Background(), playbookSkill())
	for _, want := range []string{"Could not establish queue_draining (embedder down)", "Establish queue_draining first", "if true: Look at the consumer.", "if false: Look at the broker."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	pr.establish = func(context.Context, MachineDef, string) (map[string]any, string, error) {
		return map[string]any{"queue_draining": "unclear"}, "", nil
	}
	if out := pr.resolve(context.Background(), playbookSkill()); !strings.Contains(out, `reported "unclear", which decides nothing`) {
		t.Fatalf("an undecidable value falls back:\n%s", out)
	}
}

// The Builder tool's playbook argument is parsed and validated; an empty
// string clears.
func TestPlaybookFromArgs(t *testing.T) {
	rules, has, err := playbookFromArgs(map[string]any{"playbook": `[{"fact":"q","how":"h","then":"a"}]`})
	if err != nil || !has || len(rules) != 1 || rules[0].Fact != "q" {
		t.Fatalf("parse: %v %v %+v", err, has, rules)
	}
	if _, has, err := playbookFromArgs(map[string]any{"playbook": ""}); err != nil || !has {
		t.Fatalf("empty clears: %v %v", err, has)
	}
	if _, has, _ := playbookFromArgs(map[string]any{}); has {
		t.Fatal("absent is not a change")
	}
	if _, _, err := playbookFromArgs(map[string]any{"playbook": `[{"fact":"q"}]`}); err == nil || !strings.Contains(err.Error(), "how is required") {
		t.Fatalf("validation names the problem: %v", err)
	}
	if _, _, err := playbookFromArgs(map[string]any{"playbook": `{not json`}); err == nil {
		t.Fatal("bad JSON is an error")
	}
}
