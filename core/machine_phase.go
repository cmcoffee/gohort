package core

import (
	"context"
	"strings"
)

// PhaseWorker returns the default PhaseRunner: a transient phase runs as
// a focused worker call over its slice of catalog, on its own tier, with
// its own think setting.
//
// It exists so a host gets transient phases without writing any LLM
// plumbing of its own — the same reason pipelines ship an interpreter
// rather than an interface. A host with a reason to run phases
// differently (its own persona, its own guardrails, a streaming surface)
// passes its own PhaseRunner instead; nothing here is privileged.
//
// Note what it deliberately does NOT carry: no persona, no session
// history. A transient phase does one bounded job against the prompt the
// driver resolved for it, and its product is state, not conversation.
func (T *AppCore) PhaseWorker(catalog []AgentToolDef) PhaseRunner {
	return T.PhaseWorkerConfirm(catalog, nil)
}

// PhaseWorkerConfirm is PhaseWorker with the host's approval hook, for a
// host running steps where somebody is watching.
//
// The hook is the turn's own: a step must not reach a tool the turn
// itself would have stopped to ask about. nil means allow (the dry run
// and any unattended host), which is safe only because those have no
// person to ask and no live catalog to reach.
func (T *AppCore) PhaseWorkerConfirm(catalog []AgentToolDef, confirm func(name, args string) bool) PhaseRunner {
	return func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		// A transient phase defaults to NOT reasoning: it is a bounded
		// transform (split this up, pick a lane) sitting in front of the
		// user's actual turn, and the latency it adds is paid before
		// anyone sees a word. Authors opt in per phase.
		think := PhaseThink(ph, false)
		return T.runWorkerStageConfirm(ctx, prompt, PhaseTools(ph, catalog), think, len(ph.ModelOutput()) > 0, PhaseTier(ph), confirm)
	}
}

// CompleteTurn closes a turn the host has finished running, advancing
// the cursor when the resident phase that replied names a Next.
//
// This is what makes a one-beat resident phase possible: an intake that
// greets, asks its questions, and then hands off. AdvanceMachine cannot
// do it, because the handoff is only correct AFTER the phase has had its
// turn, and the driver returns before that. Empty Next (the ordinary
// case) stays put, which is the shape most machines want: a resident
// phase the conversation lives in.
//
// It deliberately writes NOTHING to the state blackboard. A resident
// phase's product is the conversation, and the conversation is already
// in history; pinning its reply into MachineState would paste it into
// the system prompt of every later phase, forever, growing the one part
// of the prompt this design keeps small and stable.
func (d MachineDef) CompleteTurn(cur *MachineCursor, ph MachinePhase, note func(kind, detail string)) {
	if cur == nil || !ph.Resident {
		return
	}
	next := strings.TrimSpace(ph.Next)
	if next == "" {
		return
	}
	if note == nil {
		note = func(string, string) {}
	}
	nph, ok := d.Phase(next)
	if !ok {
		note("machine_dead_end", "step "+ph.Name+" hands off to unknown step "+next+"; staying put")
		return
	}
	cur.moveTo(ph.Name, nph, "handed off after one turn", note, d.accumulatorNames())
	note("machine_phase_advance", "step "+ph.Name+" has had its turn; moving to "+nph.Name)
}

// PhaseInstructions returns EXACTLY what one step is sent, for a caller
// that needs to show an author the real thing rather than a description
// of it.
//
// The two kinds are composed differently and that difference is easy to
// get wrong — the editor's first preview called PhaseBlock for both,
// showing transient steps an "Established earlier" block they never
// receive and hiding the output contract they do:
//
//   - A TRANSIENT step gets its own prompt with {input} / {prev} /
//     {state:…} resolved, plus the declared-output contract appended by
//     runDeclaredOutput.
//   - A RESIDENT step gets PhaseBlock, layered into the agent's system
//     prompt: its directive, what earlier steps established, where else
//     it can go, and the routing block.
//
// Built from the same functions the run path uses, so it cannot drift
// into describing something the model never sees.
func (d MachineDef) PhaseInstructions(ph MachinePhase, st MachineState, v PhaseVars) string {
	if ph.Resident {
		return d.PhaseBlock(ph, st, v)
	}
	out := d.phasePrompt(ph, st, v)
	if fields := ph.ModelOutput(); len(fields) > 0 {
		out += renderOutputContract(fields)
	}
	return out
}

// phasePrompt composes a transient step's directive: its own prompt with
// the vocabulary resolved, preceded by the person's message when the
// prompt never placed it itself.
//
// Split from PhaseInstructions because the run path adds the output
// contract itself (runDeclaredOutput renders it, and again on the repair
// retry), so this is the part both share and neither duplicates.
func (d MachineDef) phasePrompt(ph MachinePhase, st MachineState, v PhaseVars) string {
	// The two that depend on WHICH step is asking, filled in here so no
	// caller has to remember to.
	v.Step = ph.Name
	v.Machine = chooseStr(v.Machine, d.Name)
	v.Established = d.establishedBlock(ph, st)

	out := ResolvePhaseTemplate(ph.Prompt, v, st)
	// A step that runs against no message at all is the most expensive
	// thing an author can forget, and it fails silently: the model
	// answers confidently about nothing.
	if !mentionsInput(ph.Prompt) {
		out = v.inputBlock() + out
	}
	// And what earlier steps worked out, unless the prompt reaches for it
	// itself. A resident step has always been handed this; a transient
	// one was left to hand-copy {state:…} references for values the
	// definition already knows.
	if !mentionsEstablished(ph.Prompt) {
		out = v.establishedBlock() + out
	}
	return out
}

// runPhase resolves one transient phase's prompt and calls it, decoding
// a declared Output through the same contract → decode → one repair path
// pipeline stages use.
func (T *AppCore) runPhase(ctx context.Context, def MachineDef, ph MachinePhase, v PhaseVars, st MachineState, run PhaseRunner, note func(kind, detail string)) (string, map[string]any, error) {
	// One composition, shared with the editor's preview, so what an
	// author is shown is what the model is sent.
	prompt := def.phasePrompt(ph, st, v)
	out := ph.ModelOutput()
	call := func(p string) (string, error) { return run(ctx, ph, p) }

	// A step that asks the model for nothing and says nothing is a step
	// that pins values. Calling anyway would buy a paragraph nobody
	// reads and a bill nobody expected.
	if len(out) == 0 && strings.TrimSpace(ph.Prompt) == "" && len(ph.StaticFields()) > 0 {
		return "", def.fillStatic(ph, nil, v, st, note), nil
	}
	if len(out) == 0 {
		text, err := call(prompt)
		if err != nil {
			return "", nil, Error("machine " + def.Name + ", phase " + ph.Name + ": " + err.Error())
		}
		return text, def.fillStatic(ph, nil, v, st, note), nil
	}
	status := func(s string) { note("machine_output_repair", s) }
	text, fields, err := T.runDeclaredOutput(ctx, "phase "+ph.Name, out, prompt, call, status)
	if err != nil {
		return "", nil, Error("machine " + def.Name + ", phase " + ph.Name + ": " + err.Error())
	}
	return text, def.fillStatic(ph, fields, v, st, note), nil
}

// fillStatic merges the fields taken from variables into a step's
// result, so the blackboard carries them exactly like the answered ones.
//
// Filled AFTER the model, and it overwrites: a model that answered a
// field it was never shown has guessed, and the known value wins.
func (d MachineDef) fillStatic(ph MachinePhase, fields map[string]any, v PhaseVars, st MachineState, note func(kind, detail string)) map[string]any {
	static := ph.StaticFields()
	if len(static) == 0 {
		return fields
	}
	if fields == nil {
		fields = map[string]any{}
	}
	v.Step = ph.Name
	v.Machine = chooseStr(v.Machine, d.Name)
	v.Established = d.establishedBlock(ph, st)
	for _, f := range static {
		val := strings.TrimSpace(ResolvePhaseTemplate(f.From, v, st))
		if val == "" && note != nil {
			// The field the author expected to be free is empty, and
			// nothing downstream will say why. {original_input} on a turn
			// that arrived as an image and no words is the real case.
			note("machine_static_empty", "step "+ph.Name+": "+f.Name+" is filled from "+f.From+
				", which resolved to nothing this turn; the field is empty")
		}
		fields[f.Name] = val
	}
	return fields
}
