package core

import (
	"sort"
	"strings"
)

// ResolvePhaseTemplate substitutes the machine templating vocabulary
// into a phase prompt:
//
//	{input}             — the user message that opened this turn
//	{original_input}    — the message that opened the CONVERSATION
//	{prev}              — the phase run immediately before, THIS turn
//	{state:NAME}        — a phase's reply text, from any earlier turn
//	{state:NAME.field}  — one declared field of a phase's result
//
// The PhaseVars three are turn-local and belong to transient phases; a
// resident phase's prompt lands in the cacheable system prefix and
// Validate rejects both there (see phaseProblems).
//
// Plain literal replacement is enough even with the field form:
// {state:route} can't match inside {state:route.target} because the
// closing brace is part of the literal. Unknown placeholders are left
// untouched rather than blanked, so a mistake degrades to a visible
// prompt artifact instead of silently dropping context.
func ResolvePhaseTemplate(tmpl string, v PhaseVars, st MachineState) string {
	s := v.resolve(tmpl)
	// Sorted, never map order. Substitution is sequential ReplaceAll, so
	// if one value happens to CONTAIN template syntax (a model echoing
	// "{state:x}" back), whether a later pass re-expands it depends on
	// iteration order — and map order changes per call, which would make
	// the same state render different bytes on different turns. Sorted
	// keys make the outcome fixed either way.
	names := make([]string, 0, len(st))
	for name := range st {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		res := st[name]
		s = strings.ReplaceAll(s, "{state:"+name+"}", res.Text)
		fields := make([]string, 0, len(res.Fields))
		for field := range res.Fields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			s = strings.ReplaceAll(s, "{state:"+name+"."+field+"}", renderFieldValue(res.Fields[field]))
		}
	}
	return s
}

// PhaseBlock renders the system-prompt layer for the phase a turn is
// running: the phase's own directive, then what earlier phases
// established, as a pinned block.
//
// This is the piece that makes transient output STATE rather than
// TRANSCRIPT. The router's reasoning never enters history; its decision
// arrives here, once, in a fixed place.
//
// Byte-stability is a requirement, not a nicety. The block sits in the
// cacheable system prefix, so it renders phases and fields in DECLARED
// order (never map order) and resolves only values that hold still:
// {state:...}, which changes solely when a transient phase writes, and
// the SESSION-STABLE variables — {original_input} (written once, on the
// first walk), {user} and {agent} (fixed for the session), {step} and
// {machine} (fixed by the definition). The volatile three — {input},
// {prev}, {now} — are zeroed HERE, not trusted to the caller: one call
// site passing a clock in would silently re-pay cold prefill on every
// turn, which is the kind of regression nobody sees in a diff. Validate
// rejects them in a resident prompt so an author finds out at save time
// rather than by a blank.
//
// The current phase's own prior result is left out: it is the phase
// talking, and handing it its own last answer invites it to repeat it.
func (d MachineDef) PhaseBlock(ph MachinePhase, st MachineState, v PhaseVars) string {
	v.Input, v.Prev, v.Now = "", "", ""
	v.Step = ph.Name
	v.Machine = chooseStr(v.Machine, d.Name)
	v.Established = d.establishedBlock(ph, st)

	var b strings.Builder
	b.WriteString("\n\n## Current phase: ")
	b.WriteString(ph.Name)
	b.WriteString("\n")
	if desc := strings.TrimSpace(ph.Desc); desc != "" {
		b.WriteString(desc)
		b.WriteString("\n")
	}
	if p := strings.TrimSpace(ResolvePhaseTemplate(ph.Prompt, v, st)); p != "" {
		b.WriteString("\n")
		b.WriteString(p)
		b.WriteString("\n")
	}

	// The routing instruction, generated from the declaration. Without
	// this the allowed phases lived in the field's description as prose
	// somebody maintained by hand — drifting from the phase names, and
	// invisible to the validator and the diagram alike.
	if r := d.routingBlock(ph); r != "" {
		b.WriteString(r)
	}

	// Name the tool scope, for the same reason the exits are named below.
	// A phase with a Tools list narrows the catalog, and the narrowing is
	// invisible from inside the turn: an earlier phase's successful calls
	// are still in the history, so a name that stops resolving reads as a
	// name the model got wrong. It then retries spellings — a refused call
	// per round — instead of working with what this phase actually has.
	//
	// The rule it states is "judge by your catalog", NOT "judge by this
	// list". A host may keep things past the narrowing that the list does
	// not name — the workflow controls always, and whatever the agent's
	// attachments granted, which are somebody's separate deliberate grant
	// rather than a selection out of the pool this list picks from. A block
	// that said "anything else is out of scope" talked the model out of
	// tools it could see and was entitled to use.
	//
	// Static per phase, so it costs the cache nothing.
	if len(ph.Tools) > 0 || len(ph.Deny) > 0 || PhaseReach(ph) != ReachAll {
		b.WriteString("\n## Tools in this phase\n")
		if PhaseReach(ph) == ReachRead {
			b.WriteString("This phase may only READ. Nothing that writes, runs a command, or reaches the network is available here — that is the step's design, not a fault.\n")
		}
		if PhaseReach(ph) == ReachNone {
			// The author's explicit "nothing". Saying it plainly beats
			// printing the marker, which reads as a tool called __none__.
			b.WriteString("This phase reaches no tools. Answer from what you were given and what is already in this conversation.\n")
			b.WriteString("If the job genuinely needs one, change_phase to a step that carries it rather than describing a call you cannot make.\n")
		} else if len(ph.Tools) > 0 {
			b.WriteString("This phase narrows what you may reach to: " + strings.Join(ph.Tools, ", ") + " — alongside your workflow controls and anything your attachments grant.\n")
			b.WriteString("Go by what is IN your catalog. A tool you used earlier in this conversation, under a phase that allowed it, and can no longer see is out of scope HERE — not misnamed. Don't retry those names. Work with what you have, or change_phase if the job has genuinely moved to a phase that carries what you need.\n")
		}
		// Named, not merely absent. A tool the model has used all conversation
		// and now cannot see reads as a fault it should work around — by
		// retrying the name, by reaching for a neighbour that does the same
		// thing, or by authoring one. Saying which tools this step withholds,
		// and that it is deliberate, is what stops the workaround.
		if len(ph.Deny) > 0 {
			b.WriteString("Withheld in this phase, by the step's design: " + strings.Join(ph.Deny, ", ") + ". Do not reach for these, do not substitute a neighbouring tool that does the same job, and do not author a replacement. If the work genuinely needs one, say so plainly or change_phase to a step that carries it.\n")
		}
	}

	// Unless the prompt placed {established} itself — then the author
	// chose where it goes, and a second copy would argue with them. The
	// same rule a transient step follows.
	if est := v.Established; est != "" && !mentionsEstablished(ph.Prompt) {
		b.WriteString("\n## Established earlier in this conversation\n")
		b.WriteString("Settled. Work from it rather than re-deriving it, and do not re-ask what it already answers.\n")
		b.WriteString(est)
	}
	// Name the exits. The change_phase tool tells the model its choices
	// are "listed in your current-phase block", so they had better be —
	// a tool that asks for a name the prompt never supplies gets guessed
	// names, and a guessed name is a refused call.
	//
	// Static per machine, so it costs the cache nothing.
	// Only the exits this step actually allows. Listing a phase the tool
	// would refuse teaches the model to call it and be told no, which is
	// a worse failure than not offering it: it spends a round and reads
	// as the framework contradicting itself.
	if exits := d.ExitOptions(ph); len(exits) > 0 {
		b.WriteString("\n## Other phases in this workflow\n")
		b.WriteString("Reachable with change_phase, and only when the request has genuinely moved on. A follow-up or a clarification is the same job: stay here.\n")
		for _, p := range exits {
			b.WriteString("- " + p.Name)
			if desc := strings.TrimSpace(p.Desc); desc != "" {
				b.WriteString(": " + desc)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// routingBlock states where this phase may send the conversation, in
// the phases' own words: each target's name and what that phase is for.
//
// Generated rather than written, so it cannot disagree with the machine
// it describes. Empty when the phase routes statically or declares no
// targets — an undeclared routing field keeps its old behaviour, where
// anything the model returns is tried and an unknown name falls back.
func (d MachineDef) routingBlock(ph MachinePhase) string {
	from := ph.RoutesBy()
	if from == "" {
		return ""
	}
	targets := ph.RoutingChoices()
	if len(targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Where this goes next\n")
	b.WriteString("Put exactly one of these in \"" + from + "\". Choose by what the work needs, not by order.\n")
	for _, t := range targets {
		b.WriteString("- " + t)
		if p, ok := d.Phase(strings.TrimSpace(t)); ok && strings.TrimSpace(p.Desc) != "" {
			b.WriteString(": " + strings.TrimSpace(p.Desc))
		}
		b.WriteString("\n")
	}
	if fb := strings.TrimSpace(ph.Next); fb != "" {
		b.WriteString("If none of them fits, " + fb + " is used.\n")
	}
	return b.String()
}

// renderPhaseFindings renders one phase's result for the pinned block:
// its declared fields in declared order, or its reply text when it
// declared none.
func renderPhaseFindings(p MachinePhase, res PhaseResult) string {
	decl := p.DeclaredOutput()
	if len(decl) == 0 {
		return strings.TrimSpace(res.Text)
	}
	var b strings.Builder
	single := len(decl) == 1
	for _, f := range decl {
		v, ok := res.Fields[f.Name]
		if !ok {
			continue
		}
		body := readableFieldValue(v)
		if body == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if f.Name == BuiltinNextStep {
			// Always labelled, even when it is the only field: bare
			// "verify" under a heading reads as a finding rather than as
			// where the conversation went.
			b.WriteString("went to: " + body)
			continue
		}
		// One field carries the whole phase, and the phase is already
		// named on the heading above, so a label would say it twice.
		if single {
			b.WriteString(body)
			continue
		}
		b.WriteString(displayFromSnake(f.Name))
		b.WriteString(": ")
		if strings.Contains(body, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(body)
	}
	return b.String()
}
