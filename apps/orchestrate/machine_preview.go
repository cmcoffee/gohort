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

// phasePreviewParts is the composed block and the sentence that frames
// it, for both the page render and the refresh endpoint — one function,
// so what a save re-fetches cannot drift from what the page first drew.
func phasePreviewParts(def MachineDef, p MachinePhase) (block, note string) {
	block = strings.TrimSpace(def.PhaseInstructions(p, sampleStateFor(def, p), samplePhaseVars()))
	note = "This is what the step is actually told, composed by the framework — your instructions, plus everything the definition already knows. You do not need to repeat any of it."
	// Who RECEIVES this text depends on what the step runs, and for two kinds of
	// step the answer is "no model at all". Saying "this is what the step is
	// actually told" over a tool step's composed prompt is the exact failure
	// this panel exists to prevent, one level up: an author tunes wording that
	// changes nothing, and finds out only by running the machine.
	//
	// Branches on the same fields the runner branches on (machineHost.runPhase
	// for the delegating kinds, MachineDef.runPhase for the pin-only early
	// return), so the panel and the runner cannot disagree about which case
	// this is.
	switch {
	case strings.TrimSpace(p.Tool) != "":
		note = "This step calls the tool " + strings.TrimSpace(p.Tool) + ". NO model is asked anything — the text below is composed only to fill that tool's arguments, so wording here changes what the tool receives, not how anything is reasoned about."
	case len(p.ModelOutput()) == 0 && strings.TrimSpace(p.Prompt) == "" && len(p.StaticFields()) > 0:
		note = "This step pins values and asks nothing. NO model runs — the fields below are filled from what the framework already holds, so the composed text is shown for reference only."
	case strings.TrimSpace(p.Pipeline) != "":
		note = "This text is handed to the pipeline " + strings.TrimSpace(p.Pipeline) + " as its input. What the pipeline's own stages then tell a model is defined in the pipeline, not here."
	case strings.TrimSpace(p.Machine) != "":
		note = "This text is handed to the machine " + strings.TrimSpace(p.Machine) + " as its input. What that machine's steps tell a model is defined there, not here."
	case strings.TrimSpace(p.Agent) != "":
		note = "This text is the brief handed to the agent " + strings.TrimSpace(p.Agent) + ". When the step declares fields, what it reports back is then shaped by a second call that quotes this brief alongside the reply — so the model sees more than what is shown here."
	}
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
	return block, note
}

// phasePreview renders the composed block for one step, greyed, under
// its form.
//
// Tagged with the step's name so a save can refresh it in place
// (machinePreviewRefreshJS): this is the surface that TEACHES what the
// framework composes, and a teaching surface showing the pre-edit
// composition is the same lie as an added step that never appears. It
// refreshes rather than reloading the page, because the alternative
// would fire while somebody is still writing the prompt it describes.
func phasePreview(def MachineDef, p MachinePhase) ui.Component {
	block, note := phasePreviewParts(def, p)
	return ui.Card{HTML: `<details class="ui-card" style="opacity:0.75" data-preview-step="` + HTMLEscape(p.Name) + `">` +
		`<summary style="cursor:pointer;font-weight:600">What this step actually receives</summary>` +
		`<p data-preview-note style="font-size:0.8rem;color:var(--text-mute);margin:0.5rem 0">` + HTMLEscape(note) + `</p>` +
		`<pre data-preview-body style="white-space:pre-wrap;font-size:0.76rem;line-height:1.5;overflow-x:auto;color:var(--text-mute)">` +
		HTMLEscape(block) + `</pre></details>`}
}
