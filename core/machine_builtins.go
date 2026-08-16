// What the framework supplies, so the author doesn't have to.
//
// Machines started out with everything declared: if a step needed the
// person's message it typed {input}, and if a step chose where to go it
// declared a text field, named that field in next_from, and listed the
// allowed values on it. Three settings and a variable for one idea
// ("this step decides"), and a step that simply forgot {input} ran
// against no message at all and produced a confident answer about
// nothing.
//
// That is guesswork the definition already has the answer to. This file
// holds the parts the framework knows and therefore provides: the fixed
// variable vocabulary (MachineVars, below) and next_step, the routing
// field a deciding step returns.
//
// A built-in is a PRIMITIVE: it always means the same thing, in every
// machine, with no name to choose and nothing to wire. If a value has to
// be configured before it means anything, it is a declared field and
// belongs on a step. If it means the same thing everywhere, it belongs
// here and the author should never have been asked about it.
//
// The rule for what belongs here: a value the framework can compute from
// the machine and the turn is not the author's to wire. Their job is to
// say WHAT the step should work out. Everything mechanical around that
// is generated, shown greyed-out in the editor's preview, and cannot
// drift from the definition because it is derived from it.

package core

import "strings"

// BuiltinNextStep is the routing field the framework declares for a step
// that chooses where to go. Reserved: a phase declaring an output field
// of this name is routing by hand, and takes precedence (Problems()
// reports the collision rather than one silently winning).
const BuiltinNextStep = "next_step"

// MachineTurn is what the HOST knows about one turn and the driver
// cannot work out for itself: who is talking, which agent they opened,
// and what time it is where they are.
//
// A struct rather than more positional strings, so adding the next fact
// does not mean editing every call site to pass another "" — and so a
// host that supplies none of them still reads correctly at the call
// site: MachineTurn{Input: msg}.
type MachineTurn struct {
	// Input is what the person said THIS turn.
	Input string
	// User is the person, by the name they are known as here.
	User string
	// Agent is the agent running the conversation.
	Agent string
	// Now is the date and time where the PERSON is, already rendered.
	// A string rather than a time.Time because the framework has no
	// business deciding the format a prompt reads, and the host already
	// has the user's zone.
	Now string
}

// PhaseVars are every value a step's prompt can reference: the host's
// facts about the turn, plus what the driver works out as it walks.
//
// The whole set is FIXED. That is the point of a built-in — it always
// means the same thing, in every machine, with no name to choose and
// nothing to wire. An author asking "what did they originally want?"
// writes {original_input} and is done; the alternative was declaring a
// field, filling it in one step, and referencing it from another.
type PhaseVars struct {
	MachineTurn

	// Opening is the message that started the conversation — the thing
	// the whole machine is about. It survives every later turn, which is
	// the point: a step five turns in can ask "does this answer what
	// they originally wanted?" without the author having to pin the
	// question into state by hand.
	Opening string
	// Prev is the text the step immediately before produced, this turn.
	// Empty on the first step of a turn.
	Prev string
	// Established is everything earlier steps worked out, rendered the
	// same way a resident step receives it. Set by the driver per step,
	// since it depends on which step is asking.
	Established string
	// Step and Machine are where the prompt is, by name. Small, but they
	// are the two things an author would otherwise hard-code and then
	// forget to change after a rename.
	Step    string
	Machine string
}

// MachineVar is one entry in the built-in vocabulary: how it is written,
// what it always means, and what the framework does when a prompt does
// not place it.
//
// Declared as data because three surfaces have to agree about it — the
// editor's help, the machine tool's spec, and the resolver below. Two of
// them being prose is how a variable gets documented and never
// implemented, or implemented and never mentioned.
type MachineVar struct {
	Ref   string
	Means string
	Auto  string
}

// MachineVars is the vocabulary. Everything here is a primitive: no
// configuration, no declaration, the same meaning in every machine.
//
// All of them are TEXT. That is not an accident of the current set — a
// variable holds what the framework can hand a prompt, and a prompt
// takes words. It is why a field filled from one is normalized to a
// string, and it is the answer to "can I fill a list from a built-in?":
// no, let the step work that out.
//
// {input} and {original_input} are the WORDS of a message. Images and
// files attached to a turn are not in them, so a turn that arrived as a
// photo and nothing else resolves to nothing, and the driver leaves a
// breadcrumb rather than a silently empty field.
func MachineVars() []MachineVar {
	return []MachineVar{
		{"{input}", "the words the person said this turn",
			"handed to the step anyway when the prompt never places it"},
		{"{original_input}", "the words that opened the conversation, unchanged on turn nine", ""},
		{"{established}", "everything earlier steps worked out, with their names",
			"handed to the step anyway when the prompt places no {state:…} reference of its own"},
		{"{prev}", "what the step just before this one produced, this turn", ""},
		{"{now}", "the date and time where the person is", ""},
		{"{user}", "the person, by name", ""},
		{"{agent}", "the agent running the conversation", ""},
		{"{step}", "the name of this step", ""},
		{"{machine}", "the name of this machine", ""},
	}
}

// BuiltinFieldName is the name a field takes when it holds a built-in:
// "{original_input}" → "original_input". The braces are the templating
// syntax, not part of the name, and a field called "{now}" would need
// escaping everywhere it was referenced.
func BuiltinFieldName(ref string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(ref), "{"), "}")
}

// BuiltinRefForFieldName is the reverse: a field named after a built-in
// is filled from it. This is what makes the editor's wheel work — the
// author picks a name, and the name IS the choice of where the value
// comes from.
//
// Exact matches only, and only against the fixed vocabulary. A field
// called "input_summary" is somebody's own field and stays their step's
// work.
func BuiltinRefForFieldName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, v := range MachineVars() {
		if BuiltinFieldName(v.Ref) == name {
			return v.Ref, true
		}
	}
	return "", false
}

// isBuiltinVarRef reports whether a reference is exactly one of the
// built-ins, which is what a field filled from a variable is allowed to
// name (that, or a {state:…} reference).
func isBuiltinVarRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	for _, v := range MachineVars() {
		if v.Ref == ref {
			return true
		}
	}
	return false
}

// resolve substitutes the built-in vocabulary. State refs are resolved
// separately (ResolvePhaseTemplate) since they come from the blackboard.
func (v PhaseVars) resolve(s string) string {
	// Longest first: {input} is a substring of {original_input}, so
	// replacing it first would leave "{original_" + the message + "}".
	s = strings.ReplaceAll(s, "{original_input}", v.Opening)
	s = strings.ReplaceAll(s, "{input}", v.Input)
	s = strings.ReplaceAll(s, "{established}", strings.TrimSpace(v.Established))
	s = strings.ReplaceAll(s, "{prev}", v.Prev)
	s = strings.ReplaceAll(s, "{now}", v.Now)
	s = strings.ReplaceAll(s, "{user}", v.User)
	s = strings.ReplaceAll(s, "{agent}", v.Agent)
	s = strings.ReplaceAll(s, "{step}", v.Step)
	s = strings.ReplaceAll(s, "{machine}", v.Machine)
	return s
}

// mentionsEstablished reports whether a prompt reaches for what earlier
// steps worked out — either the whole block or one field of it.
func mentionsEstablished(tmpl string) bool {
	return strings.Contains(tmpl, "{established}") || strings.Contains(tmpl, "{state:")
}

// establishedBlockFor wraps the blackboard for a transient step that
// never asked for it.
//
// The asymmetry this closes: a RESIDENT step has always been handed
// everything earlier steps established, composed into its block. A
// transient step was handed nothing, so its author hand-copied
// {state:triage.observation}, {state:triage.source}, {state:triage.asked}
// one reference at a time — three chances to typo a name the definition
// already knows, and three lines to fix after a rename.
func (v PhaseVars) establishedBlock() string {
	body := strings.TrimSpace(v.Established)
	if body == "" {
		return ""
	}
	return "## Established earlier in this conversation\n" +
		"Settled. Work from it rather than re-deriving it, and do not re-ask what it already answers.\n" +
		body + "\n\n"
}

// mentionsInput reports whether a prompt places the person's message
// itself. Used to decide whether the framework has to.
func mentionsInput(tmpl string) bool {
	return strings.Contains(tmpl, "{input}") || strings.Contains(tmpl, "{original_input}")
}

// inputBlock is what a transient step is given when its prompt never
// mentions the person's message.
//
// This closes the trap that made machines feel unpredictable: a step's
// prompt is a TEMPLATE, so a prompt that doesn't say {input} is sent
// without the message, and the step answers from nothing. It looks like
// the model ignoring instructions and is actually the author never being
// told there was a variable to place. Now placing it is optional and
// getting it is not.
//
// Only when the prompt is silent about it, so an author who writes
// "Rewrite this: {input}" keeps their exact phrasing and gets no second
// copy.
func (v PhaseVars) inputBlock() string {
	opening := strings.TrimSpace(v.Opening)
	input := strings.TrimSpace(v.Input)
	if opening == "" && input == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## What the person said\n")
	switch {
	case opening == "" || opening == input:
		b.WriteString(chooseStr(input, opening))
	default:
		b.WriteString("Opening the conversation:\n")
		b.WriteString(opening)
		b.WriteString("\n\nJust now:\n")
		b.WriteString(input)
	}
	b.WriteString("\n\n")
	return b.String()
}

// RoutesBy names the output field carrying this step's routing decision,
// or "" when the step goes the same place every time.
//
// Two ways to get here, and the older one wins so no existing machine
// changes meaning: a hand-declared field named in NextFrom, or Choices,
// where the author picked the steps it may go to and the framework
// declares the field.
func (p MachinePhase) RoutesBy() string {
	if from := strings.TrimSpace(p.NextFrom); from != "" {
		return from
	}
	if len(p.Choices) > 0 && !p.Resident {
		return BuiltinNextStep
	}
	return ""
}

// RoutingChoices lists the steps this one may send the turn to, whether
// they were declared on a field or picked in Choices.
func (p MachinePhase) RoutingChoices() []string {
	if from := strings.TrimSpace(p.NextFrom); from != "" {
		for _, f := range p.Output {
			if f.Name == from {
				return f.Enum
			}
		}
		return nil
	}
	if p.Resident {
		return nil
	}
	return p.Choices
}

// normalized is p.Output with the two corrections the framework makes on
// the author's behalf.
//
// ONE: a field NAMED after a built-in is filled from it. The name is the
// whole choice — "original_input" cannot mean anything else, and a
// machine where it did would be one where the same field name means the
// opening message here and whatever a model wrote there. This lives in
// core rather than in the editor that offers the wheel, because the
// editor is one of four doors: the machine tool writes these, extras/
// ships them, imports carry them, and a rule enforced at one door is a
// rule that is not true.
//
// TWO: a filled field is text. Every built-in holds text — a message, a
// name, a rendered clock — so declaring one a list describes something
// that cannot happen. Storing it as declared would put a lie on the
// blackboard; refusing to run over it would be pedantry about a machine
// that works. Advice() explains it where the author can read it.
//
// An explicit From always wins: a field called "asked" pointed at
// {original_input} keeps its own name, which is the case the name rule
// cannot express.
func (p MachinePhase) normalized() []PipelineField {
	touch := false
	for _, f := range p.Output {
		if fieldNeedsNormalizing(f) {
			touch = true
			break
		}
	}
	if !touch {
		return p.Output
	}
	out := make([]PipelineField, len(p.Output))
	copy(out, p.Output)
	for i := range out {
		if strings.TrimSpace(out[i].From) == "" {
			if ref, ok := BuiltinRefForFieldName(out[i].Name); ok {
				out[i].From = ref
			}
		}
		if strings.TrimSpace(out[i].From) != "" {
			out[i].Type = FieldString
		}
	}
	return out
}

// fieldNeedsNormalizing keeps the common case allocation-free: most
// fields are somebody's own name, worked out by the step.
func fieldNeedsNormalizing(f PipelineField) bool {
	if strings.TrimSpace(f.From) == "" {
		_, isBuiltin := BuiltinRefForFieldName(f.Name)
		return isBuiltin
	}
	return f.Type != "" && f.Type != FieldString
}

// ModelOutput is what the model is actually asked for: everything the
// step establishes, minus the fields already filled from a variable.
//
// The split matters in both directions. Show the model a field it is not
// being asked for and it answers it anyway, usually with a paraphrase of
// the value that was already correct. Leave a filled field out of the
// RESULT and everything downstream loses it.
func (p MachinePhase) ModelOutput() []PipelineField {
	all := p.DeclaredOutput()
	out := make([]PipelineField, 0, len(all))
	for _, f := range all {
		if strings.TrimSpace(f.From) == "" {
			out = append(out, f)
		}
	}
	return out
}

// StaticFields are the fields filled from a variable rather than asked
// for. Same order as declared, so a rendered result reads the way the
// author wrote it.
func (p MachinePhase) StaticFields() []PipelineField {
	var out []PipelineField
	for _, f := range p.DeclaredOutput() {
		if strings.TrimSpace(f.From) != "" {
			out = append(out, f)
		}
	}
	return out
}

// DeclaredOutput is the full set of fields the step establishes: the
// ones the author declared, plus the routing field when the framework
// owns it. Some of them may be filled rather than asked for — see
// ModelOutput.
//
// Every path that renders a contract or decodes a reply works from THIS
// (or ModelOutput, derived from it) rather than .Output, so the built-in
// field cannot be half-present — asked for but not decoded, or decoded
// but never asked for.
func (p MachinePhase) DeclaredOutput() []PipelineField {
	if p.RoutesBy() != BuiltinNextStep {
		return p.normalized()
	}
	for _, f := range p.Output {
		if f.Name == BuiltinNextStep {
			return p.normalized() // hand-declared; Problems() has already said so
		}
	}
	out := make([]PipelineField, 0, len(p.Output)+1)
	out = append(out, p.normalized()...)
	out = append(out, PipelineField{
		Name:     BuiltinNextStep,
		Type:     FieldString,
		Required: true,
		Enum:     p.Choices,
		Desc:     "Which step this hands the conversation to. Pick by what the work needs, not by order.",
	})
	return out
}
