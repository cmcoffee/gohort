// A skill's playbook, resolved for one turn.
//
// A rule says "establish Y; if it holds, Z, else U". The resolver runs the
// establishing step through the turn's machine host — the same host a
// machine's phases run through, so the step has the skill's tools, the
// approval gate, and a line of activity the person sees — reads the decoded
// fact off the run's blackboard, and renders ONLY the arm that applies. The
// model receives "Y is true, so: Z" and never the branch it did not earn,
// which is the difference between a rule it follows and a rule it reads.
//
// Resolved once per skill per turn and cached, whichever door delivered the
// skill: read_skill, a knowledge search that attached its instructions, or
// the re-injection of consulted skills into a later round's prompt.

package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// playbookRunner is the resolver with its one dependency — establishing a
// fact — injectable, so the decision and rendering can be tested without a
// model. fields is the establishing phase's decoded output; text is what the
// step said, kept as the evidence line.
type playbookRunner struct {
	establish func(ctx context.Context, def MachineDef, input string) (fields map[string]any, text string, err error)
	msg       string   // the turn's newest user message, for When and {input}
	docNames  []string // attachment names, for glob triggers in When
}

// playbookEstablishPhaseName mirrors the phase name PlaybookRule.Machine
// compiles to; the fact is read from the blackboard under it.
const playbookEstablishPhaseName = "establish"

// playbookRunner builds the resolver over this turn's machine host.
func (t *chatTurn) playbookRunner() playbookRunner {
	return playbookRunner{
		msg:      t.playbookMsg,
		docNames: t.docNames,
		establish: func(ctx context.Context, def MachineDef, input string) (map[string]any, string, error) {
			h := t.machineHost()
			// A fresh cursor and a host that reads it: the establishing run
			// is a piece of this turn's work, not the session's machine, and
			// its blackboard must not be the conversation's.
			cur := &MachineCursor{}
			child := *h
			child.state = func() MachineState { return cur.State }
			_, text, err := t.app.RunUnattended(ctx, def, cur, h.machineTurn(input), child.phaseRunner(), h.note)
			if err != nil {
				return nil, "", err
			}
			return cur.State[playbookEstablishPhaseName].Fields, text, nil
		},
	}
}

// playbookBlock resolves a skill's playbook for this turn, once.
func (t *chatTurn) playbookBlock(ctx context.Context, skill SkillRecord) string {
	if t == nil || len(skill.Playbook) == 0 {
		return ""
	}
	if block, done := t.playbookBlocks[skill.ID]; done {
		return block
	}
	block := t.playbookRunner().resolve(ctx, skill)
	if t.playbookBlocks == nil {
		t.playbookBlocks = map[string]string{}
	}
	t.playbookBlocks[skill.ID] = block
	if block != "" {
		t.turnDiag("skill_playbook", "resolved the "+skill.Name+" playbook: "+excerptLine(block, 200))
	}
	return block
}

// resolve runs every rule whose When matches this turn (or names no turn)
// and renders the outcomes as one block.
func (pr playbookRunner) resolve(ctx context.Context, skill SkillRecord) string {
	var b strings.Builder
	for _, rule := range skill.Playbook {
		if len(rule.When) > 0 && !TriggersMatch(rule.When, pr.msg, pr.docNames) {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(pr.run(ctx, skill, rule))
	}
	if b.Len() == 0 {
		return ""
	}
	return "**Playbook** (" + skill.Name + " — established for this turn; follow what applies):\n\n" + b.String()
}

// run establishes one rule's fact and renders the arm that applies, recursing
// into a nested rule when the arm is one.
func (pr playbookRunner) run(ctx context.Context, skill SkillRecord, rule PlaybookRule) string {
	fact := strings.TrimSpace(rule.Fact)
	fields, text, err := pr.establish(ctx, rule.Machine(skill), pr.msg)
	if err != nil {
		return "Could not establish " + fact + " (" + err.Error() + "). " + rule.Fallback()
	}
	raw, present := fields[fact]
	if !present {
		return "Could not establish " + fact + " (the check reported no value). " + rule.Fallback()
	}
	shown, arm, next, ok := rule.Decide(raw)
	if !ok {
		return "Could not establish " + fact + " (the check reported " + fmt.Sprintf("%q", shown) + ", which decides nothing). " + rule.Fallback()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Established:** %s = %s", fact, shown)
	if ev := strings.TrimSpace(text); ev != "" {
		fmt.Fprintf(&b, " — %s", excerptLine(ev, 240))
	}
	b.WriteString("\n")
	switch {
	case next != nil:
		b.WriteString(pr.run(ctx, skill, *next))
	case strings.TrimSpace(arm) != "":
		fmt.Fprintf(&b, "**So:** %s", strings.TrimSpace(arm))
	default:
		b.WriteString("**So:** nothing further is prescribed for this case; proceed on the instructions.")
	}
	return b.String()
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
