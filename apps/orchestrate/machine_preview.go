// Showing the author what the step actually receives.
//
// The prompt box is only PART of what a step is told. The framework
// already composes the rest mechanically — every earlier step's findings
// pinned under "Established earlier in this conversation", the other
// steps named so change_phase has real names to use, and (through the
// structured-output path) the declared fields the step must return.
//
// None of that was visible anywhere, so authors wrote it themselves:
// the investigation recipe hand-copies {state:triage.observation} into
// its prompt when the same value is already pinned two paragraphs above,
// and hand-writes "exactly one of: hunch, answer" for a choice the
// machine's own shape defines. That is tokens paid twice and a copy that
// drifts from the field names.
//
// So the editor renders the REAL composed block, from the REAL function
// a live turn calls (MachineDef.PhaseBlock). Not a description of it, not
// a re-implementation — the same bytes, so this can never quietly stop
// being true. The author writes the job; everything greyed out below is
// added for them.

package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// sampleStateFor builds a stand-in blackboard: every step that could have
// run before this one, with its declared fields carrying a placeholder.
//
// Placeholders rather than real values because there is no conversation
// yet — the point is to show the SHAPE and the labels, which is what an
// author needs to know they do not have to repeat. A step that declares
// nothing contributes its reply text slot, the same as at runtime.
func sampleStateFor(def MachineDef, self MachinePhase) MachineState {
	st := MachineState{}
	for _, p := range def.Phases {
		if p.Name == self.Name {
			continue
		}
		res := PhaseResult{Fields: map[string]any{}}
		for _, f := range p.Output {
			if f.Name == "" {
				continue
			}
			hint := strings.TrimSpace(f.Desc)
			if hint == "" {
				hint = "what " + p.Name + " found"
			}
			res.Fields[f.Name] = "‹" + hint + "›"
		}
		if len(res.Fields) == 0 {
			res.Text = "‹what " + p.Name + " replied›"
		}
		st[p.Name] = res
	}
	return st
}

// samplePhaseVars stands in for the turn: placeholders in the shape of
// the real values, so the preview shows WHERE each one lands without
// pretending to know what somebody will type.
func samplePhaseVars() PhaseVars {
	return PhaseVars{
		MachineTurn: MachineTurn{
			Input: "‹what the person just said›",
			User:  "‹the person›",
			Agent: "‹the agent they opened›",
			Now:   "‹the date and time where they are›",
		},
		Opening: "‹the message that opened the conversation›",
	}
}

// phasePreview renders the composed block for one step, greyed, under
// its form.
func phasePreview(def MachineDef, p MachinePhase) ui.Component {
	block := strings.TrimSpace(def.PhaseInstructions(p, sampleStateFor(def, p), samplePhaseVars()))
	note := "This is what the step is actually told, composed by the framework — your instructions, plus everything the definition already knows. You do not need to repeat any of it."
	// A filled field is deliberately absent below, and absent without a
	// word reads as a bug rather than as the saving it is.
	if static := p.StaticFields(); len(static) > 0 {
		var names []string
		for _, f := range static {
			names = append(names, f.Name+" ← "+f.From)
		}
		note += " Not shown, because the model is never asked for them: " + strings.Join(names, ", ") +
			" are filled from what the framework already holds, after the step runs."
	}
	if p.Resident {
		note += " A step the conversation waits in receives what earlier steps established, and where else it can go."
	} else {
		note += " Notice that the fields you declare arrive as instructions in their own right, each with the description you gave it — that is why the prompt should say HOW to go about the work rather than restating what to produce."
	}
	return ui.Card{HTML: `<details class="ui-card" style="opacity:0.75">` +
		`<summary style="cursor:pointer;font-weight:600">What this step actually receives</summary>` +
		`<p style="font-size:0.8rem;color:var(--text-mute);margin:0.5rem 0">` + HTMLEscape(note) + `</p>` +
		`<pre style="white-space:pre-wrap;font-size:0.76rem;line-height:1.5;overflow-x:auto;color:var(--text-mute)">` +
		HTMLEscape(block) + `</pre></details>`}
}
