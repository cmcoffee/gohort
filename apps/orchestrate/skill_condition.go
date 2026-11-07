// A conditional skill, settled for one turn.
//
// A skill with conditions (if the customer is on version 4, as written; else
// if on 3, clear the cache; else, upgrade first) is settled before it is
// handed over. Its lookups run as one check through the turn's machine host,
// the same host a machine's phases run through, so the check has the skill's
// tools, the approval gate, and a line of activity the person sees. The
// answers are read off the run's blackboard as decoded fields, and the agent
// gets the skill with the instructions of the case that applied, or is told
// it does not apply and why (SkillRecord.DecideConditions).
//
// Checked once per skill per turn and cached, whichever door asked first:
// read_skill, a knowledge search or fetch, the skill's triggers before the
// first round, or the re-injection of consulted skills into a later round.
// Not across turns: the answer can change between messages.

package orchestrate

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// skillVerdict is one conditional skill's answer for this turn: whether it
// applies, the instructions of the case that did, and what the agent is told.
type skillVerdict struct {
	applies      bool
	instructions string
	note         string
}

// conditionCheck runs a skill's condition check; injectable so the gate can be
// tested without a model. fields is the check phase's decoded output; text is
// what the step said, kept as evidence when the declared field is empty.
type conditionCheck func(ctx context.Context, def MachineDef, input string) (fields map[string]any, text string, err error)

// runConditionCheck runs the check over this turn's machine host.
func (t *chatTurn) runConditionCheck(ctx context.Context, def MachineDef, input string) (map[string]any, string, error) {
	h := t.machineHost()
	// A fresh cursor and a host that reads it: the check is a piece of this
	// turn's work, not the session's machine, and its blackboard must not be
	// the conversation's.
	cur := &MachineCursor{}
	child := *h
	child.state = func() MachineState { return cur.State }
	_, text, err := t.app.RunUnattended(ctx, def, cur, h.machineTurn(input), child.phaseRunner(), h.note)
	if err != nil {
		return nil, "", err
	}
	return cur.State[def.Start].Fields, text, nil
}

// skillGate is the turn's SkillGate: it settles a conditional skill once and
// answers from the cache after.
func (t *chatTurn) skillGate(s SkillRecord) (SkillRecord, bool, string) {
	return t.skillGateWith(s, t.runConditionCheck)
}

func (t *chatTurn) skillGateWith(s SkillRecord, check conditionCheck) (SkillRecord, bool, string) {
	if t == nil || !s.Conditional() {
		return s, true, ""
	}
	if v, done := t.skillVerdicts[s.ID]; done {
		s.Instructions = v.instructions
		return s, v.applies, v.note
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	chosen, applies, note := s.DecideConditions(t.conditionMsg, t.docNames, func(def MachineDef) (map[string]any, string, error) {
		return check(ctx, def, t.conditionMsg)
	})
	if t.skillVerdicts == nil {
		t.skillVerdicts = map[string]skillVerdict{}
	}
	t.skillVerdicts[s.ID] = skillVerdict{applies: applies, instructions: chosen.Instructions, note: note}
	verb := "applies"
	if !applies {
		verb = "does not apply"
	}
	t.turnDiag("skill_condition_checked", "checked the conditions of "+s.Name+": it "+verb+". "+excerptLine(note, 200))
	return chosen, applies, note
}

// checkConditionalSkills checks, before the first model round, every allowed
// conditional skill whose triggers match this turn, and delivers the ones that
// apply, so their instructions are in the round's prompt whether or not the
// model would have consulted them. The ones that do not apply are told to the
// model instead (renderTriggeredSkills). Returns the names checked.
//
// Consultation is the model's call for an ordinary skill, and a hint is the
// most a trigger match does for one. A condition is different: "on sync
// problems, check the version first" is a rule about the turn, and a rule the
// model may decline to look up is a suggestion.
func (t *chatTurn) checkConditionalSkills(userMsg string) []string {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(t.agent.AllowedSkills))
	for _, id := range t.agent.AllowedSkills {
		allowed[id] = true
	}
	var mine []SkillRecord
	for _, s := range t.agentSkills() {
		if allowed[s.ID] {
			mine = append(mine, s)
		}
	}
	return checkConditionalSkillsIn(t, mine, userMsg, t.skillGate)
}

// checkConditionalSkillsIn is the rule itself, over a given set of skills and
// gate, so the decision is testable without a store, an agent or a model.
func checkConditionalSkillsIn(t *chatTurn, skills []SkillRecord, userMsg string, gate SkillGate) []string {
	if t == nil || strings.TrimSpace(userMsg) == "" {
		return nil
	}
	var checked []string
	for _, s := range skills {
		if t.deliveredSkills[s.ID] || !s.ConditionFires(userMsg, t.docNames) {
			continue
		}
		checked = append(checked, s.Name)
		if _, applies, _ := gate(s); !applies {
			continue
		}
		if t.deliveredSkills == nil {
			t.deliveredSkills = map[string]bool{}
		}
		t.deliveredSkills[s.ID] = true
	}
	return checked
}

// excerptLine flattens text to one line and caps it.
func excerptLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		cut := s[:max]
		if i := strings.LastIndex(cut, " "); i > max/2 {
			cut = cut[:i]
		}
		return cut + "…"
	}
	return s
}
