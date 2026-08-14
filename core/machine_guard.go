// The re-entry guard: the check that decides whether a user turn
// arriving at a resident phase still belongs to the job that phase is
// doing.
//
// It is the hard half of the whole design. A machine buys you an agent
// that stops re-deciding its approach every turn — but a conversation
// that has genuinely moved on then needs a way OUT of the phase it is
// parked in, and if the only way out is the model's own judgment, you
// have given back exactly what you bought. So there are two mechanisms
// and they converge on one function:
//
//   - The change_phase tool (host side). Free, LLM-judged, always
//     available when a machine has somewhere else to go.
//   - The Guard below. A separate cheap call, made BEFORE the phase gets
//     the turn, judged in fresh context against a condition the author
//     wrote. Deterministic, and a latency tax on every turn.
//
// The tool is the default and the guard is opt-in per phase, because the
// cost is per-turn and the author is the one who knows whether this
// particular phase needs the stronger thing.
//
// Structurally this is the premise gate from speaker grounding pointed
// at a different question: does the incoming turn still sit inside the
// domain the last routing decision claimed.

package core

import (
	"context"
	"strings"
)

// guardOutput is the declared shape of a guard verdict. Three fields,
// one of them required: a check that can only fail in one direction is
// easier for a small model to get right than a free-text judgment the
// caller has to interpret.
var guardOutput = []PipelineField{
	{Name: "stay", Type: FieldBool, Required: true,
		Desc: "true if the new message still belongs to the current phase's job; false if the conversation has moved on and needs a different phase"},
	{Name: "to", Type: FieldString,
		Desc: "when stay is false, the name of the phase to move to, chosen from the list above"},
	{Name: "why", Type: FieldString,
		Desc: "one short sentence explaining the call, in plain language"},
}

// checkGuard judges whether a resumed resident phase keeps this turn.
//
// It returns the phase that should own the turn plus whether the guard
// tripped. It FAILS OPEN in every uncertain case — a call that errors, a
// verdict that won't parse, a target that doesn't resolve — because the
// cost of a wrong stay is one turn answered from a slightly stale phase,
// while the cost of a wrong move is throwing away the state the
// conversation is built on. Every one of those paths leaves a
// breadcrumb; a guard that silently declined to guard is the thing this
// must never be.
func (T *AppCore) checkGuard(ctx context.Context, def MachineDef, ph MachinePhase, cur *MachineCursor, input string, run PhaseRunner, note func(kind, detail string)) (MachinePhase, bool) {
	if strings.TrimSpace(ph.Guard) == "" || strings.TrimSpace(input) == "" {
		return ph, false
	}
	probe := MachinePhase{
		// Named so a status line or a log reads as what it is. The
		// colon keeps it out of the phase namespace, where names are
		// dot-free but otherwise unconstrained.
		Name: "guard:" + ph.Name,
		// A guard is a cheap check standing in front of the user's
		// actual turn: worker tier, no reasoning budget, no tools. An
		// author who wants their guard to deliberate is really asking
		// for a transient phase.
		Model:  "worker",
		Think:  "off",
		Output: guardOutput,
	}
	raw, fields, err := T.runDeclaredOutput(ctx, "guard on phase "+ph.Name, guardOutput,
		def.guardPrompt(ph, cur.State, input),
		func(p string) (string, error) { return run(ctx, probe, p) }, nil)
	if err != nil {
		note("machine_guard_failed", "the guard on phase "+ph.Name+" could not be evaluated ("+err.Error()+"); staying put")
		return ph, false
	}
	if stay, _ := fields["stay"].(bool); stay {
		return ph, false
	}
	why := strings.TrimSpace(readableFieldValue(fields["why"]))
	if why == "" {
		why = "the guard judged this turn to be outside the phase's job"
	}
	target, ok := def.guardTarget(ph, fields)
	if !ok {
		note("machine_guard_unresolved", "the guard on phase "+ph.Name+" wanted to move ("+why+") but named no phase that exists; staying put. Raw verdict: "+previewForRepair(raw))
		return ph, false
	}
	if target.Name == ph.Name {
		// The guard fired and the fallback chain led straight back here —
		// a verdict that named nothing, on a phase that is also the
		// machine's start. Nothing to do, but the guard DID decide
		// something and a decision with no trace is the failure this
		// rule exists to prevent.
		note("machine_guard_unresolved", "the guard on phase "+ph.Name+" wanted to move ("+why+") but resolved back to the phase it is already in; staying put")
		return ph, false
	}
	note("machine_guard_tripped", "moving from phase "+ph.Name+" to "+target.Name+": "+why)
	cur.moveTo(ph.Name, target, "guard: "+why, note)
	return target, true
}

// guardTarget resolves where a tripped guard sends the turn: the phase
// the verdict named, else the author's declared GuardTo, else the
// machine's start.
//
// The fallback chain matters more than it looks. A guard that fires
// correctly and then can't say where to go is the common small-model
// failure, and "back to the start" is almost always the author's intent
// anyway — that is where a machine re-decomposes.
func (d MachineDef) guardTarget(ph MachinePhase, fields map[string]any) (MachinePhase, bool) {
	want, _ := fields["to"].(string)
	for _, name := range []string{want, ph.GuardTo, d.StartPhase()} {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if target, ok := d.Phase(name); ok {
			return target, true
		}
	}
	return MachinePhase{}, false
}

// guardPrompt builds the question the guard answers.
//
// It deliberately does NOT include the conversation. The guard is a
// fresh-context check on ONE thing — whether this new message belongs to
// the job the phase is doing — and handing it the history it is meant to
// judge from outside is how a check ends up agreeing with whatever the
// conversation was already doing. It gets the author's condition, what
// earlier phases settled, the phases it may name, and the message.
func (d MachineDef) guardPrompt(ph MachinePhase, st MachineState, input string) string {
	var b strings.Builder
	b.WriteString("A conversation is currently in the phase named \"" + ph.Name + "\".")
	if desc := strings.TrimSpace(ph.Desc); desc != "" {
		b.WriteString(" That phase's job: " + desc)
	}
	b.WriteString("\n\nThe condition to check:\n" + strings.TrimSpace(ph.Guard) + "\n")

	if est := d.establishedBlock(ph, st); est != "" {
		b.WriteString("\nWhat earlier phases of this conversation settled:\n")
		b.WriteString(est)
	}
	b.WriteString("\nThe phases you may move to:\n")
	for _, p := range d.Phases {
		if p.Name == ph.Name {
			continue
		}
		line := "- " + p.Name
		if desc := strings.TrimSpace(p.Desc); desc != "" {
			line += ": " + desc
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nThe new message from the user:\n" + strings.TrimSpace(input) + "\n")
	b.WriteString("\nDecide whether the conversation stays in \"" + ph.Name + "\" or moves. Staying is the default: move only when the condition above clearly applies. A follow-up, a clarification, or a related question is the SAME job and stays.")
	return b.String()
}

// establishedBlock renders the blackboard for a phase's audience, in
// declared order, skipping the phase itself. Shared by the guard prompt
// and PhaseBlock so the two never disagree about what "established"
// means.
func (d MachineDef) establishedBlock(ph MachinePhase, st MachineState) string {
	var b strings.Builder
	for _, p := range d.Phases {
		if p.Name == ph.Name {
			continue
		}
		res, ran := st[p.Name]
		if !ran {
			continue
		}
		body := renderPhaseFindings(p, res)
		if body == "" {
			continue
		}
		b.WriteString("\n### ")
		b.WriteString(p.Name)
		if desc := strings.TrimSpace(p.Desc); desc != "" {
			b.WriteString(" — ")
			b.WriteString(desc)
		}
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}
