package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func conditionalSkill() SkillRecord {
	return SkillRecord{ID: "sv4", Name: "Sync settings", Description: "v4 sync help", Instructions: "Walk them through the v4 sync settings.",
		Triggers:   []string{"sync"},
		Conditions: []SkillCase{{Kind: "if", Question: "Is the customer on version 4 or later?", How: "Look up the account and read its app version."}}}
}

// The conditions are settled once per skill per turn, the check given the
// person's message, and every later door gets the same answer: the same case,
// its instructions.
func TestAConditionIsCheckedOncePerTurn(t *testing.T) {
	turn := &chatTurn{conditionMsg: "my app won't sync"}
	runs := 0
	check := func(_ context.Context, def MachineDef, input string) (map[string]any, string, error) {
		runs++
		if input != "my app won't sync" || !strings.Contains(def.Phases[0].Prompt, "version 4 or later") {
			t.Fatalf("the check was given %q / %q", input, def.Phases[0].Prompt)
		}
		return map[string]any{"case_1": false, "case_1_evidence": "app_version: 3.2", "case_2": true, "case_2_evidence": "app_version: 3.2"}, "", nil
	}
	s := conditionalSkill()
	s.Conditions = append(s.Conditions, SkillCase{Kind: "else_if", Question: "Is it version 3?", How: "Same lookup.", Instructions: "Clear the sync cache."})
	for i := 0; i < 3; i++ {
		chosen, applies, note := turn.skillGateWith(s, check)
		if !applies || chosen.Instructions != "Clear the sync cache." || !strings.Contains(note, "3.2") {
			t.Fatalf("call %d: %v %q %q", i, applies, chosen.Instructions, note)
		}
	}
	if runs != 1 {
		t.Errorf("the check ran %d times in one turn", runs)
	}
	// A plain skill is never checked.
	if _, applies, note := turn.skillGateWith(SkillRecord{ID: "p", Name: "Plain"}, check); !applies || note != "" || runs != 1 {
		t.Errorf("a plain skill was checked: %v %q", applies, note)
	}
	// A check that fails does not apply, and says why.
	fresh := &chatTurn{}
	_, applies, note := fresh.skillGateWith(s, func(context.Context, MachineDef, string) (map[string]any, string, error) {
		return nil, "", errors.New("the CRM is down")
	})
	if applies || !strings.Contains(note, "the CRM is down") {
		t.Errorf("a failed check: %v %q", applies, note)
	}
}

// Before the first round: a conditional skill whose triggers match is
// checked, and delivered only if it applies. A plain skill, or a message that
// matches nothing, is left to the model as before.
func TestAMatchingConditionalSkillIsCheckedBeforeTheFirstRound(t *testing.T) {
	yes := conditionalSkill()
	no := conditionalSkill()
	no.ID, no.Name = "sold", "Upgrade first"
	plain := SkillRecord{ID: "pl", Name: "Plain", Triggers: []string{"sync"}}
	verdicts := map[string]bool{"sv4": true, "sold": false}
	var asked []string
	gate := SkillGate(func(s SkillRecord) (SkillRecord, bool, string) {
		asked = append(asked, s.Name)
		return s, verdicts[s.ID], "note"
	})
	turn := &chatTurn{deliveredSkills: map[string]bool{}}
	got := checkConditionalSkillsIn(turn, []SkillRecord{yes, no, plain}, "my app won't sync", gate)
	if strings.Join(got, ",") != "Sync settings,Upgrade first" || strings.Join(asked, ",") != "Sync settings,Upgrade first" {
		t.Errorf("checked %v (asked %v)", got, asked)
	}
	if !turn.deliveredSkills["sv4"] || turn.deliveredSkills["sold"] || turn.deliveredSkills["pl"] {
		t.Errorf("delivered %v", turn.deliveredSkills)
	}
	asked = nil
	if got := checkConditionalSkillsIn(&chatTurn{}, []SkillRecord{yes}, "how are you", gate); len(got) != 0 || len(asked) != 0 {
		t.Errorf("a message matching no trigger was checked: %v", got)
	}
}

// What the model sees: a conditional skill that applies comes with what the
// check found; one that does not is said, and its instructions are not there;
// a skill found not to apply is not hinted at either.
func TestTheTurnShowsWhatTheCheckDecided(t *testing.T) {
	root := pinRootDB(t)
	yes := conditionalSkill()
	no := conditionalSkill()
	no.ID, no.Name, no.Instructions = "sold", "Old sync", "Old sync steps."
	for _, s := range []SkillRecord{yes, no} {
		if _, err := SaveSkill(root, "user1", s); err != nil {
			t.Fatal(err)
		}
	}
	turn := &chatTurn{udb: root, user: "user1",
		agent:           AgentRecord{AllowedSkills: []string{"sv4", "sold"}},
		deliveredSkills: map[string]bool{"sv4": true},
		skillVerdicts: map[string]skillVerdict{
			"sv4":  {applies: true, instructions: "Walk them through the v4 sync settings.", note: "Checked: version 4.1."},
			"sold": {applies: false, note: "The \"Old sync\" skill does not apply this turn."},
		}}
	out := turn.renderTriggeredSkills()
	for _, want := range []string{"v4 sync settings", "Checked: version 4.1.", "\"Old sync\" skill does not apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Old sync steps.") {
		t.Errorf("the instructions of a skill that does not apply reached the prompt:\n%s", out)
	}
	if hints := turn.renderSkillTriggerHints("my app won't sync"); strings.Contains(hints, "Old sync") {
		t.Errorf("a skill found not to apply was hinted at:\n%s", hints)
	}

	// Delivered by a door that did not check, it is checked here and held
	// back if it does not apply.
	turn.deliveredSkills["sold"] = true
	if out := turn.renderTriggeredSkills(); strings.Contains(out, "Old sync steps.") || turn.deliveredSkills["sold"] {
		t.Errorf("an unchecked delivery of a skill that does not apply got through:\n%s", out)
	}
}

// Builder's conditions: a JSON array (as a string or decoded), validated so
// the error names the condition; an empty array clears.
func TestBuilderSetsASkillsConditions(t *testing.T) {
	cases, has, err := conditionsFromArgs(map[string]any{"conditions": `[{"kind":"if","question":"Is it v4?","how":"Read the version."},{"kind":"else","instructions":"Upgrade first."}]`}, "Sync steps.")
	if err != nil || !has || len(cases) != 2 || cases[1].Instructions != "Upgrade first." {
		t.Fatalf("a chain: %+v %v %v", cases, has, err)
	}
	if cases, _, err := conditionsFromArgs(map[string]any{"conditions": []any{map[string]any{"kind": "if", "check": "mentions", "mentions": []any{"refund"}}}}, "Refund steps."); err != nil || len(cases) != 1 || cases[0].Mentions[0] != "refund" {
		t.Errorf("a decoded array: %+v %v", cases, err)
	}
	if cases, has, err := conditionsFromArgs(map[string]any{"conditions": "[]"}, "x"); err != nil || !has || cases != nil {
		t.Errorf("an empty array should clear: %+v %v %v", cases, has, err)
	}
	if _, _, err := conditionsFromArgs(map[string]any{"conditions": `[{"kind":"if","question":"Is it v4?"}]`}, "x"); err == nil || !strings.Contains(err.Error(), "how to check") {
		t.Errorf("a question with no way to check it: %v", err)
	}
	if _, _, err := conditionsFromArgs(map[string]any{"conditions": "not json"}, "x"); err == nil {
		t.Error("not an array was accepted")
	}
	if _, _, err := conditionsFromArgs(map[string]any{"conditions": `[{"kind":"if","question":"q","how":"h"}]`}, ""); err == nil || !strings.Contains(err.Error(), "Instructions, which are empty") {
		t.Errorf("conditions on a skill with no instructions: %v", err)
	}
	if _, has, _ := conditionsFromArgs(map[string]any{"name": "x"}, "x"); has {
		t.Error("no conditions argument should change nothing")
	}
}
