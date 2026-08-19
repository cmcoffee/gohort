// A phase that delegates its work to another agent.
//
// The other two ways a phase can differ from the agent running the
// conversation — a narrowed tool catalog, a different model tier — are
// configurations of the SAME agent. This one is not: a delegate has its
// own persona, its own tools, its own memory. It is the shape servitor
// uses, where something conducts and something else with different reach
// does the work, and until now a machine could not express it.
//
// The seam is PhaseRunner, which already abstracts "run this phase's
// prompt, hand me its declared fields". Delegation is a different runner,
// not different machinery.
//
// TWO CALLS, when the phase declares fields. The delegate is a whole
// agent and answers in prose — it plans, uses tools, and reports. Asking
// it for JSON as well would put a decoder's constraints on something
// whose value is that it is not a decoder. So the delegate reports, and
// the phase's own worker shapes that report into the declared fields
// using the exact path a non-delegating phase uses. A phase declaring
// nothing costs one call.

package orchestrate

import (
	"context"
	"encoding/json"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// phaseRunner returns the runner the machine drives this turn: the
// ordinary inline worker, wrapped so a phase naming an agent is handed
// to that agent instead.
func (t *chatTurn) phaseRunner() PhaseRunner {
	return func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		// Per step, because the catalog is: a step that names no tools
		// pays nothing, and one that names some gets exactly those
		// (PhaseWorker narrows by ph.Tools). A delegate brings its own,
		// so it is handed none of this.
		base := t.app.PhaseWorkerConfirm(t.machineCatalog(ph), t.machineConfirm())
		// Say what is happening BEFORE it happens. These calls run at the
		// head of the turn, before the persona is even assembled, so
		// until the first one returns the person is looking at nothing —
		// and a machine that decomposes and routes spends two model calls
		// there. Silence during work somebody is waiting on reads as
		// hung, whatever the reason for it.
		t.emitStatus(phaseStatusLine(ph))
		// A pipeline first: Validate refuses a phase that names both, so
		// the order only decides which one wins on an imported phase that
		// carries the pair anyway.
		if pipe := strings.TrimSpace(ph.Pipeline); pipe != "" {
			return t.runPipelinePhase(ctx, ph, pipe, prompt, base)
		}
		ref := strings.TrimSpace(ph.Agent)
		if ref == "" {
			return base(ctx, ph, prompt)
		}
		return t.runDelegatedPhase(ctx, ph, ref, prompt, base)
	}
}

// phaseStatusLine narrates one step in the author's own words.
//
// The step's description is written for a person (it is what the rail
// and the routing instruction show), so it is the right sentence here
// too — no second copy to keep in sync. A guard arrives as a synthetic
// phase named "guard:<step>", and saying so matters: it is a call the
// person pays for on EVERY turn spent in that step, and "checking
// whether this is still the same job" is the only honest description of
// where that second went.
func phaseStatusLine(ph MachinePhase) string {
	if guarded := strings.TrimPrefix(ph.Name, "guard:"); guarded != ph.Name {
		return "Checking whether this is still the same job…"
	}
	if d := strings.TrimSpace(ph.Desc); d != "" {
		return ph.Name + ": " + strings.TrimRight(d, ".") + "…"
	}
	return "Working through " + ph.Name + "…"
}

// runDelegatedPhase dispatches one phase to another agent.
func (t *chatTurn) runDelegatedPhase(ctx context.Context, ph MachinePhase, ref, prompt string, base PhaseRunner) (string, error) {
	target, found := findAgentByNameOrID(t.udb, t.user, ref)
	if !found {
		// Broken-dependency posture: a machine is portable and the agent
		// it names may simply not exist in this deployment. Run the phase
		// inline rather than failing the turn, and say so — a phase that
		// quietly stops delegating is a machine that quietly stopped
		// being what its author built.
		t.turnDiag("phase_delegate_missing", "phase "+ph.Name+" delegates to agent "+ref+
			", which does not exist here; this turn ran the phase inline instead")
		return base(ctx, ph, prompt)
	}
	if target.ID == t.agent.ID {
		// Delegating to yourself is a second turn of the same agent with
		// none of the benefit and all of the cost.
		t.turnDiag("phase_delegate_self", "phase "+ph.Name+" delegates to the agent already running it; ran inline")
		return base(ctx, ph, prompt)
	}

	// One continuing thread per (conversation, phase). Continuing, so a
	// re-entered phase builds on what the delegate already established in
	// THIS conversation; per-conversation, so two investigations never
	// share a delegate's context — the contamination the whole
	// investigation workflow is arranged to prevent.
	sub := "machine:" + t.session.ID + ":" + ph.Name
	t.turnDiag("phase_delegate", "phase "+ph.Name+" delegated to "+chFirst(target.Name, target.ID))

	res, err := t.app.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
		AgentOwner:   t.user,
		RuntimeUser:  t.user,
		AgentKey:     target.ID,
		SubSessionID: sub,
		Message:      prompt,
		// The caller is mid-turn with a person waiting, so the delegate's
		// progress rides the same activity surface the rest of the turn
		// uses rather than disappearing into a silence.
		StatusCallback: func(s string) {
			if strings.TrimSpace(s) == "" {
				return
			}
			t.sse.Send(map[string]any{
				"kind": "activity", "type": "cmd", "id": activityCheapID(),
				"text": chFirst(target.Name, target.ID) + ": " + s,
			})
		},
	})
	if err != nil {
		// The delegate failed. Falling back to inline would answer the
		// question with the wrong thing wearing the right name, so this
		// fails the phase and lets the machine's own error path report it.
		return "", err
	}
	report := strings.TrimSpace(res.Text)
	if report == "" {
		return "", Error("phase " + ph.Name + " delegated to " + chFirst(target.Name, target.ID) + " and got nothing back")
	}
	if len(ph.Output) == 0 {
		// Nothing declared: the report IS the phase's product, and there
		// is nothing to decode.
		return report, nil
	}
	// Shape the report into the declared fields, through the same worker
	// the phase would have used. The delegate is asked for its work, not
	// for a schema.
	return base(ctx, ph, "A delegate was given this task:\n\n"+prompt+
		"\n\nIt reported back:\n\n"+report+
		"\n\nRecord what it found in the fields below. Take the delegate's findings as given — "+
		"do not re-do its work, second-guess it, or fill a field it did not address. "+
		"A field it left unanswered is better left empty than invented.")
}

// runPipelinePhase runs one phase THROUGH a stored pipeline.
//
// The delegate above hands a phase to another agent; this hands it to a
// recipe. Same seam, same broken-dependency posture, and one difference
// that is the whole reason to prefer it where it fits: a pipeline can hand
// back a SHAPE. An agent answers in prose, so a phase that declares fields
// has to pay a second call to read them out of the answer. A pipeline whose
// final stage declared those same fields has already produced them, and
// they are taken as the phase's own.
func (t *chatTurn) runPipelinePhase(ctx context.Context, ph MachinePhase, ref, prompt string, base PhaseRunner) (string, error) {
	def, found := findPipelineByNameOrID(t.udb, t.user, ref)
	if !found {
		// A machine is portable and the pipeline it names may not exist
		// here. Run inline rather than failing the turn, and say so: a
		// phase that quietly stops using its pipeline is a machine that
		// quietly stopped being what its author built.
		t.turnDiag("phase_pipeline_missing", "phase "+ph.Name+" runs through pipeline "+ref+
			", which does not exist here; this turn ran the phase inline instead")
		return base(ctx, ph, prompt)
	}
	t.turnDiag("phase_pipeline", "phase "+ph.Name+" ran through pipeline "+chFirst(def.Name, def.ID))

	// An agent stage inside the pipeline dispatches as the caller, the same
	// way a pipeline run from its own page does.
	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		return t.app.RunAgentSync(ctx, t.user, t.user, agentID, stageInput)
	}
	// Stage progress rides the turn's activity surface: these calls run at
	// the head of the turn with somebody waiting, and a pipeline of six
	// stages is a long silence otherwise.
	status := func(s string) {
		if strings.TrimSpace(s) == "" {
			return
		}
		t.sse.Send(map[string]any{
			"kind": "activity", "type": "cmd", "id": activityCheapID(),
			"text": chFirst(def.Name, def.ID) + ": " + s,
		})
	}
	// The phase's own catalog is the pipeline's inherited pool, so a stage
	// that names no tools reaches exactly what the phase may reach, and one
	// that names some gets that subset. Nothing widens here.
	out, fields, err := t.app.RunPipelineDefSyncFields(ctx, def, prompt, dispatch, status, t.machineCatalog(ph))
	if err != nil {
		// Falling back to inline would answer with the wrong thing wearing
		// the right name. Fail the phase and let the machine report it,
		// exactly as a failed delegate does.
		return "", err
	}
	report := strings.TrimSpace(out)
	if report == "" {
		return "", Error("phase " + ph.Name + " ran pipeline " + chFirst(def.Name, def.ID) + " and got nothing back")
	}
	if len(ph.Output) == 0 {
		return report, nil
	}
	// The one-call path: the pipeline already produced every field this
	// phase declares, so hand those straight to the decoder rather than
	// paying a model to copy them across.
	if js, ok := declaredFieldsJSON(ph.Output, fields); ok {
		t.turnDiag("phase_pipeline_shape", "phase "+ph.Name+" took its fields from the pipeline's own declared output; no second call")
		return js, nil
	}
	// Shapes did not line up. Same recovery as a delegate: the phase's own
	// worker reads the report into the declared fields.
	return base(ctx, ph, "A pipeline was run for this task:\n\n"+prompt+
		"\n\nIt produced:\n\n"+report+
		"\n\nRecord what it produced in the fields below. Take its output as given — "+
		"do not re-do the work, second-guess it, or fill a field it did not address. "+
		"A field it left unanswered is better left empty than invented.")
}

// declaredFieldsJSON re-encodes the pipeline's values for the fields this
// phase declares, and reports whether it could.
//
// Every declared name must be present. A partial match is not a shortcut
// worth taking: the missing field would decode as empty and read as "the
// pipeline had nothing to say about it", when the truth is that nobody
// asked. That case goes to the shaping call, which can.
//
// Re-encoding the SUBSET rather than passing the pipeline's own JSON
// through is deliberate. runDeclaredOutput retries once on a decode
// mismatch by calling the runner again, and for this runner that means
// running the whole pipeline a second time. Handing the decoder exactly
// the names it asked for keeps that retry from ever being reached.
func declaredFieldsJSON(declared []PipelineField, fields map[string]any) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	out := make(map[string]any, len(declared))
	for _, f := range declared {
		v, ok := fields[f.Name]
		if !ok {
			return "", false
		}
		out[f.Name] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// findPipelineByNameOrID resolves a phase's pipeline reference.
//
// By NAME as well as id, and name first among the user's own, because a
// machine is portable: an exported recipe carries the name somebody wrote,
// while the id belongs to the deployment it was authored in.
func findPipelineByNameOrID(udb Database, user, ref string) (PipelineDef, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PipelineDef{}, false
	}
	for _, d := range ListPipelineDefs(udb, user) {
		if strings.EqualFold(strings.TrimSpace(d.Name), ref) {
			return d, true
		}
	}
	if def, _, _, found := resolvePipelineFor(user, udb, ref); found {
		return def, true
	}
	return PipelineDef{}, false
}
