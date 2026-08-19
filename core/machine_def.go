// Session-resident phase machines — the third corner of the framework's
// vocabulary for running an LLM, between a pipeline and an agent.
//
// A PipelineDef is durable control flow with no memory: a run starts
// empty, walks its stages, returns a string, and dies. An agent is the
// inverse — durable memory with no control flow, re-deciding its whole
// approach from scratch on every turn. Neither one can hold a POSITION
// between user turns, which is what a conversation that decomposes once,
// routes once, and then settles into answering actually needs.
//
// A MachineDef is that third thing: an ordered set of phases, each a
// mini-agent (its own prompt layer, tool subset, tier, declared output),
// where a session persists WHICH PHASE IT IS IN plus a blackboard of what
// earlier phases decided. Phases come in two kinds:
//
//   - Transient — runs, produces a structured result, hands off. The user
//     never takes a turn inside one. Decompose, route, plan.
//   - Resident — subsequent user turns land here directly. A turn ENDS
//     here. Answer, converse, execute.
//
// That distinction is the whole feature. Transient phases chain INSIDE ONE
// TURN (see AdvanceMachine); the turn ends when control reaches a resident
// phase and it replies. Turn 1 runs decompose → route → answer; turns 2..N
// enter at answer with the decomposition and the route already pinned.
//
// A transient phase's output is STATE, NOT TRANSCRIPT. It decodes into the
// session's MachineState and renders into the system prompt as a pinned
// block (PhaseBlock); the transcript gets one collapsed card, the way a
// pipeline stage does. Turn 8 never re-reads the router's reasoning, and
// because the block changes only when a transient phase writes, the system
// prompt stays byte-identical across a resident run — so the prompt-cache
// prefix holds and cold prefill is paid once.
//
// Design rules carried over from PipelineDef (see project_export_import_artifacts):
//   - The def is the RECIPE. Session position lives elsewhere (MachineCursor).
//   - ID / Owner / timestamps are storage metadata, stripped on export.
//   - Routing is a reference to a DECLARED FIELD, never an expression
//     language — the same discipline branchTaken already enforces.
//
// See docs/agent-machines.md for the full design and staging.

package core

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// MachineDefsTable stores per-user machine definitions.
const MachineDefsTable = "machine_defs"

// MaxPhaseTransitions caps how many phase hops ONE turn may take before
// the driver stops walking and replies from wherever it stands.
//
// Without it a machine whose router can re-enter decompose (which routes
// again, which re-enters decompose) burns the turn in a loop the user
// never sees. Four is enough for the shapes this exists to serve — the
// canonical one is decompose → route → answer, which is two hops — and
// low enough that a cycle costs three wasted calls rather than a hung
// turn. Hitting it is a breadcrumb, never a silent stop.
const MaxPhaseTransitions = 4

// MaxUnattendedTransitions caps an UNATTENDED run the same way, at a
// number chosen for a different job.
//
// Four is right for a turn somebody is waiting on. A run nobody is
// watching is the other case: deep research is twenty-two phases, and a
// ceiling that cannot hold the shapes this exists to serve would make
// the mode useless. A hundred is well clear of that and still bounds a
// cycle to a bill somebody can survive.
//
// It is a BACKSTOP, not a design. A machine whose graph can cycle
// without a guard that reads a declared field is refused at save time
// (see problems); this catches what validation cannot prove.
const MaxUnattendedTransitions = 100

// MachineDef is the serializable recipe: a named, ordered set of phases
// plus the phase a fresh session starts in.
type MachineDef struct {
	ID          string `json:"id,omitempty"`    // storage key; stripped on export
	Owner       string `json:"owner,omitempty"` // owning user; stripped on export
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // what it does / when to use it

	// Start is the phase a fresh session enters. Empty means the first
	// phase in the list, which is what an author who never thought about
	// it means anyway.
	Start string `json:"start,omitempty"`

	Phases []MachinePhase `json:"phases"`

	// Unattended marks a machine that RUNS rather than converses. It is
	// started once, walks its phases until one hands off nowhere, and
	// that phase's result is the run's result. Nobody is waiting, so
	// there is no phase for a turn to land in and no reason to stop
	// early: the conversational hop cap (MaxPhaseTransitions, four, a
	// courtesy to somebody watching a cursor blink) is replaced by
	// MaxUnattendedTransitions.
	//
	// It inverts the resident rule rather than relaxing it. A
	// conversational machine MUST have a step the conversation waits in;
	// an unattended one must have none, because a step that waits for a
	// person is a step this run can never leave.
	Unattended bool `json:"unattended,omitempty"`

	// AllowedUsers is the peer-share recipient set: which OTHER users of
	// this deployment may read and run this machine. Empty (the default,
	// and what every machine was before this) means private to the owner.
	//
	// Deliberately the same field, name and rule PipelineDef.AllowedUsers
	// and AgentRecord.AllowedUsers carry: the recipe travels, the
	// authority does not. A recipient runs the OWNER's definition against
	// THEIR own agents, tools, credentials and guardrails, so nothing of
	// the owner's travels with the share.
	//
	// Stripped on export, like the owner stamp — a recipe that arrived in
	// somebody else's deployment naming users of yours would be asserting
	// a grant across a boundary it cannot see.
	AllowedUsers []string `json:"allowed_users,omitempty"`

	Created time.Time `json:"created,omitempty"` // stripped on export
	Updated time.Time `json:"updated,omitempty"` // stripped on export

	// Previous is the definition this one replaced, kept so a wholesale
	// rewrite can be taken back.
	//
	// Exactly ONE deep, and only set by the doors that replace a machine
	// rather than edit part of it (today: describe-a-change). A form
	// that changes one field does not need it — the field is right
	// there — but a revision drafted from a paragraph can rewrite every
	// prompt in the machine, and prompts are the part somebody actually
	// wrote. Without this, "describe a change" is a control you cannot
	// safely try, which is the same as one nobody uses.
	//
	// Stripped on export: a recipe carries a machine, not its history.
	Previous *MachineDef `json:"previous,omitempty"`
}

// MachinePhase is one position a session can hold. Every field except
// Name and Prompt is optional; a phase with just those two is a prompt
// layer, which is the smallest useful thing.
type MachinePhase struct {
	// Name is unique within the machine and is also the MachineState key
	// this phase's result lands under. No dots — a dot would make
	// {state:a.b} ambiguous between a phase named "a.b" and field "b" of
	// phase "a", the same reason stage names ban them.
	Name string `json:"name"`

	// Desc is one line describing what this phase is for. Shown in the
	// editor, and (St2) handed to the guard so it can judge re-entry
	// against something written by the author rather than inferred.
	Desc string `json:"desc,omitempty"`

	// Prompt is this phase's directive, layered ON TOP of the agent's
	// persona rather than replacing it: the agent is still who it is in
	// every phase, what changes is what it's currently doing. Supports
	// {input} / {prev} / {state:PHASE} / {state:PHASE.field} templating
	// (ResolvePhaseTemplate).
	Prompt string `json:"prompt"`

	// Tools narrows the agent's catalog while in this phase. Empty
	// inherits the full catalog — the same semantics as
	// PipelineStage.Tools (see resolveStageTools), so an author who
	// learned one has learned the other.
	Tools []string `json:"tools,omitempty"`

	// Model pins the tier for turns spent here ("worker" | "lead"), and
	// maps onto AgentLoopConfig.TierOverride. Empty follows the agent's
	// own routing.
	Model string `json:"model,omitempty"`

	// Think overrides reasoning mode for this phase: "on" | "off" |
	// "" (inherit). A tri-state STRING, matching AgentRecord.Think, and
	// deliberately not the *bool PipelineStage.Think uses: kvlite
	// encodes records with gob, and gob omits zero values, so a *bool
	// pointing at FALSE decodes back as nil. "Off" would silently
	// become "inherit" on every read, which is the one setting an
	// author reaches for when they want a phase to be cheap.
	Think string `json:"think,omitempty"`

	// Output declares the structured handoff, decoded by the SAME
	// machinery pipelines use (runDeclaredOutput → decodeStageOutput →
	// coerceField, one repair retry included). Decoded fields land at
	// MachineState[Name].Fields. Empty output = a prose phase that
	// writes only text.
	Output []PipelineField `json:"output,omitempty"`

	// Resident marks the phase user turns come back to. A turn ENDS
	// here; the driver stops walking the moment it reaches one. A
	// machine with no resident phase is a pipeline with extra steps, and
	// Validate rejects it.
	Resident bool `json:"resident,omitempty"`

	// Next is the phase entered when this one finishes. Empty on a
	// resident phase means "stay", which is the ordinary case.
	Next string `json:"next,omitempty"`

	// NextFrom names one of THIS phase's own declared string output
	// fields whose VALUE is the next phase name. This is how a router
	// routes, and it is deliberately not an expression: Validate proves
	// the field is declared and of type string, and an unknown phase
	// name at run time falls back to Next with a breadcrumb rather than
	// stranding the turn.
	//
	// Prefer Choices. NextFrom is the hand-wired form, kept because
	// machines that use it exist and because a routing value that is
	// ALSO a meaningful finding ("severity", say) is worth naming
	// yourself. Setting it wins over Choices.
	NextFrom string `json:"next_from,omitempty"`

	// Choices lists the steps this one may hand the turn to, when the
	// step decides at run time which. Non-empty means the framework
	// declares the routing field itself (BuiltinNextStep) — no field to
	// invent, no field to point at, no list of allowed values to keep in
	// sync with the phase names.
	//
	// It exists because routing was three settings and a variable for
	// one idea, and the two wrong answers (a field of the wrong type, a
	// target that is not a phase) were both things the definition
	// already knew. Declaring the destinations is the whole decision;
	// everything else is derived from it — the contract the step
	// receives, the instruction naming each destination and what it is
	// for, the arrows in the diagram, and a save-time error when a name
	// stops resolving.
	//
	// TRANSIENT phases only: a resident phase leaves through change_phase
	// or a guard, not through a decoded field.
	Choices []string `json:"choices,omitempty"`

	// Guard is the re-entry condition for a resident phase, judged once
	// per user turn. Prose, because the question it asks ("is this still
	// the same job?") is not one a field reference can answer. Empty
	// means transitions happen only via the change_phase tool.
	//
	// DECLARED IN ST1, EVALUATED IN ST2 — Validate already enforces its
	// shape so the schema doesn't move when the evaluator lands.
	Guard   string `json:"guard,omitempty"`
	GuardTo string `json:"guard_to,omitempty"` // where a tripped guard goes; empty = Start

	// ExitsTo restricts where a conversation may be MOVED from this step
	// by change_phase — the model deciding mid-turn that the request has
	// moved on. Empty means anywhere, which is the right default: a
	// conversation that genuinely changed subject must not be trapped.
	//
	// It exists for the shape a list of steps cannot express: a machine
	// that BRANCHES, where the left side should stay left. Every waiting
	// step otherwise offers every other step as a destination, so a
	// conversation in one arm can cross into the other because the model
	// judged it close enough. Naming the legal exits is how an author
	// says the arms are separate.
	//
	// It bounds change_phase ONLY. A guard's target, a step's own next,
	// and the routing a step decides are the author's own wiring and are
	// not second-guessed — this restricts what the MODEL may do on its
	// own initiative, which is the only transition nobody wrote down.
	ExitsTo []string `json:"exits_to,omitempty"`

	// Keep lists the MachineState keys that survive RE-ENTRY into this
	// phase (entering it when it has already run once this session).
	// Empty keeps everything, which is the safe default: a re-route that
	// silently wipes the decomposition is the expensive mistake, not the
	// other way round.
	Keep []string `json:"keep,omitempty"`

	// Agent delegates this phase to another agent by name or id: the
	// step's work is done by something with its own persona, tools and
	// memory, and what comes back is shaped into this phase's declared
	// fields.
	//
	// This is the third way a phase can differ from the agent running the
	// conversation, alongside a narrowed catalog and a different tier —
	// and the strongest, because a delegate is not a configuration of the
	// caller. It is the shape servitor uses: something conducts, and
	// something else with different reach does the work.
	//
	// TRANSIENT phases only. A resident phase is where the conversation
	// LIVES, and handing that to a delegate would mean the person is
	// talking to something other than the agent they opened — so
	// Problems() reports it rather than quietly doing something
	// surprising.
	//
	// A name that does not resolve is a RUNTIME breadcrumb, not a save
	// refusal: a machine is portable between deployments, and the agent
	// it delegates to may simply not exist here yet. The phase falls back
	// to running inline, and says so.
	Agent string `json:"agent,omitempty"`

	// Pipeline names a stored pipeline that RUNS this phase, by name or
	// id. Mutually exclusive with Agent, because they are different kinds
	// of thing: a delegate is another AGENT (its own persona, tools and
	// memory, deciding its own approach), a pipeline is a RECIPE (fixed
	// stages, fanout, loop, declared output).
	//
	// Same portability posture as Agent: a pipeline that does not exist in
	// this deployment does not fail the turn, the phase runs inline and
	// says so.
	//
	// A pipeline whose final stage declares the fields this phase declares
	// costs ONE call: the shape it already produced is taken as the
	// phase's own. A delegate cannot do that, because an agent answers in
	// prose and something has to read the fields back out of it.
	Pipeline string `json:"pipeline,omitempty"`

	// Machine names another machine this phase runs as a CHILD: its own
	// blackboard, its own phases, run to completion, and its result comes
	// back as this phase's. Mutually exclusive with Agent and Pipeline.
	//
	// The child must be Unattended. A conversational machine has a step
	// that waits for a person, and there is nobody inside a phase to wait
	// for; a phase that named one would enter a step it could never leave.
	//
	// This is the recursive shape, and the reason it is a phase rather
	// than a general facility: research forks a child run per gap it finds
	// and merges what comes back. The merge needs no new mechanism — the
	// child's result is this phase's result, so Accumulates carries it
	// into the parent's working set exactly like any other contribution.
	//
	// Depth is capped (MaxMachineDepth): a child may not run a child.
	Machine string `json:"machine,omitempty"`

	// Accumulates declares the run-scoped lists this phase contributes to.
	// See machine_accum.go: the contribution lands under the LIST's name
	// rather than this phase's, which is what lets many phases build one
	// working set.
	Accumulates []MachineAccumulator `json:"accumulates,omitempty"`
}

// MayExitTo reports whether change_phase may move a conversation from
// this step to the named one.
//
// Empty ExitsTo means anywhere. A step that lists its exits allows
// exactly those — plus, always, the step it already hands off to
// statically and the target of its own guard: refusing a move to a
// place the author's own wiring already sends the turn would be the
// restriction contradicting the machine it is part of.
func (p MachinePhase) MayExitTo(name string) bool {
	name = strings.TrimSpace(name)
	if len(p.ExitsTo) == 0 || name == "" {
		return true
	}
	for _, allowed := range append(append([]string{}, p.ExitsTo...), p.Next, p.GuardTo) {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

// ExitOptions is where this step may be moved to, in the machine's own
// order — for the prompt block that tells a model its choices, and for
// an error that has to say what was allowed instead.
func (d MachineDef) ExitOptions(p MachinePhase) []MachinePhase {
	var out []MachinePhase
	for _, other := range d.Phases {
		if other.Name != p.Name && p.MayExitTo(other.Name) {
			out = append(out, other)
		}
	}
	return out
}

// Phase looks a phase up by name.
func (d MachineDef) Phase(name string) (MachinePhase, bool) {
	name = strings.TrimSpace(name)
	for _, p := range d.Phases {
		if p.Name == name {
			return p, true
		}
	}
	return MachinePhase{}, false
}

// PhaseNames lists every phase in declared order. For error text and
// pickers that need to say what the choices actually are.
func (d MachineDef) PhaseNames() []string {
	out := make([]string, 0, len(d.Phases))
	for _, p := range d.Phases {
		out = append(out, p.Name)
	}
	return out
}

// StartPhase resolves the entry phase: Start when set, otherwise the
// first phase in the list.
func (d MachineDef) StartPhase() string {
	if s := strings.TrimSpace(d.Start); s != "" {
		return s
	}
	if len(d.Phases) > 0 {
		return d.Phases[0].Name
	}
	return ""
}

// RenameStep renames a step and rewrites every reference to it, so a
// rename is one edit rather than one edit plus a scavenger hunt.
//
// Without this, renaming triage left next, choices, keep, guard_to, the
// routing targets and every {state:triage.…} reference pointing at a
// name that no longer exists — the checklist then reported three or four
// problems the author caused by fixing a typo. The definition knows both
// names; propagating them is its job, not the author's.
//
// The one thing it deliberately does NOT touch is live sessions: a
// conversation parked in the old name heals through resume() with a
// breadcrumb, and its blackboard entry under the old key simply ages
// out. Rewriting stored cursors from an editor save would be action at
// a distance into conversations someone may be in the middle of.
func (d *MachineDef) RenameStep(from, to string) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return
	}
	if strings.TrimSpace(d.Start) == from {
		d.Start = to
	}
	for i := range d.Phases {
		p := &d.Phases[i]
		if p.Name == from {
			p.Name = to
		}
		if strings.TrimSpace(p.Next) == from {
			p.Next = to
		}
		if strings.TrimSpace(p.GuardTo) == from {
			p.GuardTo = to
		}
		renameInList(p.Keep, from, to)
		renameInList(p.Choices, from, to)
		for fi := range p.Output {
			f := &p.Output[fi]
			renameInList(f.Enum, from, to)
			f.From = renameStateRefs(f.From, from, to)
		}
		p.Prompt = renameStateRefs(p.Prompt, from, to)
		p.Guard = renameStateRefs(p.Guard, from, to)
	}
}

// RemoveStep deletes a step and takes every reference to it with it,
// reporting the steps it had to edit.
//
// Without this, removing a step left the machine full of names that no
// longer resolve — and the checklist then reported them as work to do,
// blaming the author for a deletion they meant. A reference to
// something deleted is not a mistake to fix; it is part of the
// deletion, and the definition knows every place it appears.
//
// Two references are deliberately NOT rewritten, because only a person
// can answer them: a step whose `next` pointed here is left with
// nowhere to go (it must be told where it goes now), and a prompt that
// reads {state:gone.field} keeps its text (it asks for a value nothing
// produces, which is a real thing to fix). Both surface in the
// checklist as questions, rather than as damage.
func (d *MachineDef) RemoveStep(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	touched := map[string]bool{}
	out := d.Phases[:0:0]
	for _, p := range d.Phases {
		if p.Name == name {
			continue
		}
		if strings.TrimSpace(p.Next) == name {
			p.Next = ""
			touched[p.Name] = true
		}
		// A guard sending the turn to a step that is gone falls back to
		// the machine's start, which is what an empty guard_to means and
		// the least surprising place for a conversation to land.
		if strings.TrimSpace(p.GuardTo) == name {
			p.GuardTo = ""
			touched[p.Name] = true
		}
		if dropped := dropFromList(&p.Choices, name); dropped {
			touched[p.Name] = true
		}
		if dropped := dropFromList(&p.Keep, name); dropped {
			touched[p.Name] = true
		}
		if dropped := dropFromList(&p.ExitsTo, name); dropped {
			touched[p.Name] = true
		}
		fields := append([]PipelineField(nil), p.Output...)
		for i := range fields {
			if dropFromList(&fields[i].Enum, name) {
				touched[p.Name] = true
			}
		}
		p.Output = fields
		out = append(out, p)
	}
	d.Phases = out
	// The entry point, if that is what was removed: an empty Start
	// RESOLVES to the first step, which would make the beginning
	// positional again — so it is written down rather than left implied.
	if strings.TrimSpace(d.Start) == name {
		d.Start = ""
		if s := d.StartPhase(); s != "" {
			d.Start = s
			touched["start"] = true
		}
	}
	names := make([]string, 0, len(touched))
	for n := range touched {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// dropFromList removes a name from a slice in place, reporting whether
// it was there. The slice is COPIED before writing: phases are values
// but their slices are not, and rewriting one in place would reach the
// caller's copy of the machine.
func dropFromList(list *[]string, name string) bool {
	found := false
	out := make([]string, 0, len(*list))
	for _, v := range *list {
		if strings.TrimSpace(v) == name {
			found = true
			continue
		}
		out = append(out, v)
	}
	if found {
		*list = out
	}
	return found
}

func renameInList(list []string, from, to string) {
	for i, v := range list {
		if strings.TrimSpace(v) == from {
			list[i] = to
		}
	}
}

// renameStateRefs rewrites {state:from.…} and {state:from} without
// touching a step whose name merely starts the same way — the closing
// brace / dot is part of the match, so "triage" cannot catch
// "triage_two".
func renameStateRefs(s, from, to string) string {
	s = strings.ReplaceAll(s, "{state:"+from+".", "{state:"+to+".")
	return strings.ReplaceAll(s, "{state:"+from+"}", "{state:"+to+"}")
}

// NextPhase resolves where control goes after ph produced fields.
//
// Returns the target phase name and, when the resolution was not the
// plain static one, a note for the caller's breadcrumb — a router whose
// choice didn't resolve is exactly the kind of framework decision that
// must not vanish into a debug log.
func (d MachineDef) NextPhase(ph MachinePhase, fields map[string]any) (string, string) {
	from := ph.RoutesBy()
	if from == "" {
		return ph.Next, ""
	}
	raw, present := fields[from]
	if !present {
		return ph.Next, "step " + ph.Name + " was to choose its next step in " + from + " but the reply carried no such field; falling back to " + fallbackLabel(ph.Next)
	}
	want, _ := raw.(string)
	want = strings.TrimSpace(want)
	if want == "" {
		return ph.Next, "step " + ph.Name + " routed to an empty step name; falling back to " + fallbackLabel(ph.Next)
	}
	if _, ok := d.Phase(want); !ok {
		return ph.Next, "step " + ph.Name + " routed to unknown step " + strconv.Quote(want) + "; falling back to " + fallbackLabel(ph.Next)
	}
	return want, ""
}

// fallbackLabel names the static fallback for a breadcrumb, without
// printing an empty string when there isn't one.
func fallbackLabel(next string) string {
	if strings.TrimSpace(next) == "" {
		return "no next phase (the turn replies from here)"
	}
	return next
}

// --- validation -----------------------------------------------------

// Validate checks a machine is runnable: at least one phase, unique
// dot-free names, at least one resident phase, every transition target
// resolvable, routers routing off a declared string field, guards only
// where a guard can fire, and no transient cycle that could never reach
// a reply.
//
// Every INDEPENDENT problem is reported, not just the first — same
// reasoning as validateStageList: a machine is authored as one object,
// so its mistakes arrive as a set, and returning them one at a time
// turns one fix into three round-trips.
//
// Note the one rule that is deliberately WEAKER than a pipeline's:
// {state:X.field} may reference ANY phase, not just an earlier one.
// Pipeline stages run strictly in order so a forward reference is always
// a bug; machine phases persist their state across turns and can be
// re-entered in any order, so "later" is not a meaningful notion here.
// Existence is checked; ordering is not.
func (d MachineDef) Validate() error {
	probs := d.problems()
	switch len(probs) {
	case 0:
		return nil
	case 1:
		return Error(probs[0])
	}
	return Error("this machine has " + strconv.Itoa(len(probs)) + " problems — fix them all in one revision:\n- " + strings.Join(probs, "\n- "))
}

// Advice is what is worth fixing but does not stop a machine running —
// kept apart from Problems for a reason that matters at the call site:
// Problems REFUSES a save, and a heuristic must never do that. A rule
// that reads prompt wording is a guess about intent, and a guess with
// the power to reject somebody's work is worse than no rule.
//
// Shown beside the checklist in the editor, where it reads as "you
// probably meant something else" rather than "this is broken".
func (d MachineDef) Advice() []string {
	var out []string
	for _, p := range d.Phases {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		for _, f := range p.Output {
			// Every built-in is TEXT: a message, a name, a rendered clock.
			// Declaring a filled field as a list or a number describes
			// something that cannot happen, and DeclaredOutput quietly
			// treats it as text rather than storing a lie — so say so
			// here instead of refusing a machine that runs correctly.
			if strings.TrimSpace(f.From) != "" && f.Type != "" && f.Type != FieldString {
				out = append(out, "step "+name+": "+f.Name+" is filled from "+f.From+
					" and declared "+string(f.Type)+". Everything a variable holds is text, so it will hold text. "+
					"If you need it as a "+string(f.Type)+", let the step work it out instead of filling it.")
			}
		}
		if len(p.Tools) > 0 && strings.TrimSpace(p.Agent) != "" {
			out = append(out, "step "+name+": it names tools AND delegates. A delegate works from its own catalog, "+
				"so the list here does nothing — narrow the delegate itself, or drop the delegate and let this step do the work.")
		}
		if !p.Resident && len(p.Tools) == 0 && strings.TrimSpace(p.Agent) == "" && wantsToLook(p.Prompt) {
			out = append(out, "step "+name+": the instructions send it looking, but it names no tools and it is not delegated — "+
				"a step that passes on runs before the turn has a catalog, so it reaches exactly what it names and otherwise nothing. "+
				"Tick its tools under \"How this step runs\", or give the step to an agent that already has them.")
		}
		if len(p.Output) > 0 && asksForRawJSON(p.Prompt) {
			out = append(out, promptFormatAdvice(name))
		}
	}
	return out
}

// promptFormatAdvice is the one finding whose fix is PROSE: everything
// else the editor reports is answered by a control (tick a tool, set a
// next, turn on resident), and this one is answered by deleting
// sentences from what the author wrote.
//
// Written once and matched by value, so the panel offering to rewrite it
// and the list reporting it cannot drift into two different sentences.
func promptFormatAdvice(name string) string { return DeclaredOutputPromptAdvice("step", name) }

// DeclaredOutputPromptAdvice is that finding for anything with declared
// output fields — a machine's step, a pipeline's stage.
//
// Both mechanisms are the same one: declare fields and the framework
// asks for them, encodes them, and validates what comes back. So both
// have the same failure, where the prompt ALSO specifies a format and
// the model ends up nesting a JSON string inside a JSON field. The
// machine spec has told authors not to since it existed; the pipeline
// spec never did, and neither checked.
//
// kind is the caller's noun ("step", "stage") and appears twice, so the
// sentence reads in the vocabulary of whatever is reporting it.
func DeclaredOutputPromptAdvice(kind, name string) string {
	return kind + " " + name + ": the prompt asks for JSON, but this " + kind + " already declares " +
		"fields — the framework encodes them for you and validates what comes back. Delete the " +
		"format instructions and the example, and say what to FIND instead. Two sets of " +
		"formatting rules is how a model ends up returning a JSON string inside a JSON field."
}

// AsksForRawJSON reports whether a prompt hand-rolls a JSON contract.
// Exported for the other definitions that declare output fields.
func AsksForRawJSON(prompt string) bool { return asksForRawJSON(prompt) }

// MachineRewrite is a finding a draft-and-review can settle: the step,
// and the finding as the brief the drafter is given.
type MachineRewrite struct {
	Step string `json:"step"`
	Why  string `json:"why"`
}

// PromptRewrites lists them.
//
// Deliberately not a repair (see machine_repair.go): which sentences are
// formatting instructions and which are content is a judgement — "return
// the JSON object's key" is the subject, not the format — so this class
// gets a draft to react to rather than a silent edit.
func (d MachineDef) PromptRewrites() []MachineRewrite {
	var out []MachineRewrite
	for _, p := range d.Phases {
		name := strings.TrimSpace(p.Name)
		if name == "" || len(p.Output) == 0 || !asksForRawJSON(p.Prompt) {
			continue
		}
		out = append(out, MachineRewrite{Step: name, Why: promptFormatAdvice(name)})
	}
	return out
}

// wantsToLook spots a prompt that tells a step to go and find
// something, which it cannot do with no tools.
//
// Deliberately phrase-based and narrow, like asksForRawJSON: it fires
// on instructions to GO somewhere, not on every mention of a source. A
// step that says "explain what the log showed" is working from what it
// was handed and is fine.
func wantsToLook(prompt string) bool {
	l := strings.ToLower(prompt)
	for _, sign := range []string{
		"go and look",
		"go look",
		"search ",
		"look it up",
		"read the file",
		"read the log",
		"run the command",
		"ask the live",
		"check the system",
		"fetch ",
	} {
		if strings.Contains(l, sign) {
			return true
		}
	}
	return false
}

// asksForRawJSON spots a prompt hand-rolling the structured output the
// declared fields already provide.
//
// Deliberately narrow: it only fires on a phase that ALREADY declares
// fields, which is the only case where the instruction is redundant
// rather than merely unusual. A phase whose subject happens to be JSON —
// explain this config, read this payload — declares nothing and is left
// alone.
func asksForRawJSON(prompt string) bool {
	l := strings.ToLower(prompt)
	for _, sign := range []string{
		"json object",
		"as json",
		"valid json",
		"in json format",
		"do not include any other text",
		"respond only with",
		"reply only with",
	} {
		if strings.Contains(l, sign) {
			return true
		}
	}
	return false
}

// Problems is Validate's findings as a LIST rather than one error.
//
// The editor renders these as a running checklist while somebody builds
// a machine out, which is a different job from refusing a save: an
// unfinished machine has problems by definition, and showing them as
// work remaining beats showing them as failure. Same function behind
// both, so the checklist can never disagree with what the save will do.
func (d MachineDef) Problems() []string { return d.problems() }

func (d MachineDef) problems() []string {
	if len(d.Phases) == 0 {
		return []string{"machine has no steps"}
	}
	var probs []string

	// Pass 1: identity. Every later check reads a name, and reporting
	// "phase : output field..." for a nameless phase helps nobody.
	seen := make(map[string]bool, len(d.Phases))
	declared := make(map[string]map[string]PipelineFieldType, len(d.Phases))
	resident := 0
	for i, p := range d.Phases {
		switch {
		case strings.TrimSpace(p.Name) == "":
			probs = append(probs, "step "+strconv.Itoa(i+1)+" has no name")
			continue
		case seen[p.Name]:
			probs = append(probs, "duplicate step name: "+p.Name)
			continue
		case strings.Contains(p.Name, "."):
			probs = append(probs, "step name may not contain a dot: "+p.Name)
			continue
		}
		seen[p.Name] = true
		if p.Resident {
			resident++
		}
		fields, err := validateOutputFields(p.Name, p.Output, false)
		if err != nil {
			probs = append(probs, err.Error())
			continue
		}
		declared[p.Name] = fields
	}
	switch {
	case d.Unattended && resident > 0:
		// The inverse of the rule below, and worth its own sentence: a
		// step that waits for a person, in a run with no person, is a
		// step the walk enters and cannot leave.
		probs = append(probs, "this machine RUNS unattended, so no step may wait for the person — "+
			strings.Join(residentNames(d), ", ")+" would stop the run with nobody there to continue it. "+
			"Turn off \"the conversation waits here\", or turn off unattended.")
	case d.Unattended && !d.hasTerminalPhase():
		// Where a conversational machine ends by handing the turn back,
		// an unattended one ends by running out of steps. Without a step
		// that hands off nowhere there is no result to return, and the
		// run would walk until the backstop.
		probs = append(probs, "this machine RUNS unattended but no step finishes it — every step hands on to another, "+
			"so the run has no result and would walk until it hits the "+strconv.Itoa(MaxUnattendedTransitions)+"-step ceiling. "+
			"Leave \"then go to\" empty on the step that produces the answer.")
	case !d.Unattended && resident == 0:
		probs = append(probs, "no step waits for the person — a machine with nowhere for a turn to land is a pipeline, not a machine. Turn on \"the conversation waits here\" (resident) on the step that replies.")
	}
	// Accumulators join the same namespaces phases live in, so
	// {state:answers} and {state:answers.count} resolve like any other
	// reference. Done between the two passes: pass 1 established which
	// names are taken, pass 2 checks the references that may name these.
	probs = append(probs, d.accumulatorProblems(seen)...)
	for _, name := range d.Accumulators() {
		if seen[name] {
			continue // reported as a collision below; do not also mask the step
		}
		seen[name] = true
		declared[name] = accumulatorFields()
	}
	if s := d.StartPhase(); s != "" && !seen[s] {
		probs = append(probs, "start names unknown step "+strconv.Quote(s))
	}

	// Pass 2: per-phase wiring, now that every name is known.
	for _, p := range d.Phases {
		if !seen[p.Name] || declared[p.Name] == nil && len(p.Output) > 0 {
			continue // already reported; don't cascade
		}
		probs = append(probs, d.phaseProblems(p, seen, declared)...)
	}
	probs = append(probs, d.cycleProblems(seen)...)
	return probs
}

// accumulatorProblems checks the working set's own wiring: that a list has
// a name nothing else is using, that the field it takes exists, and that
// the mode is one the driver knows.
//
// taken is the phase-name set as of pass 1, so a collision is reported
// against the step that owns the name rather than by whichever happened to
// be written second.
func (d MachineDef) accumulatorProblems(taken map[string]bool) []string {
	var probs []string
	for _, p := range d.Phases {
		declaredHere := map[string]bool{}
		for _, f := range p.Output {
			declaredHere[strings.TrimSpace(f.Name)] = true
		}
		for _, a := range p.Accumulates {
			name := strings.TrimSpace(a.Name)
			from := strings.TrimSpace(a.From)
			switch {
			case name == "":
				probs = append(probs, "step "+p.Name+": a contribution with no list name — say which list it adds to")
			case strings.Contains(name, "."):
				probs = append(probs, "step "+p.Name+": list name "+strconv.Quote(name)+" may not contain a dot, for the same reason a step name may not: {state:a.b} would be ambiguous")
			case taken[name]:
				probs = append(probs, "step "+p.Name+": list "+strconv.Quote(name)+" has the same name as a step. They share the blackboard, so one would overwrite the other — rename the list.")
			}
			switch {
			case from == "":
				probs = append(probs, "step "+p.Name+": contribution to "+strconv.Quote(name)+" says nothing about WHAT it adds — name one of this step's own output fields in \"from\".")
			case !declaredHere[from]:
				probs = append(probs, "step "+p.Name+": contributes "+strconv.Quote(from)+" to "+strconv.Quote(name)+", but this step declares no such field. A step can only contribute what it produces.")
			}
			switch strings.ToLower(strings.TrimSpace(a.Mode)) {
			case "", AccumAppend, AccumReplace, AccumUnion:
			default:
				probs = append(probs, "step "+p.Name+": contribution mode "+strconv.Quote(a.Mode)+" is not one of append, replace, union")
			}
		}
	}
	return probs
}

// phaseRunners names the things a phase says should run it. More than one
// is the error; the list is what makes the message say WHICH.
func phaseRunners(p MachinePhase) []string {
	var out []string
	if strings.TrimSpace(p.Agent) != "" {
		out = append(out, "an agent")
	}
	if strings.TrimSpace(p.Pipeline) != "" {
		out = append(out, "a pipeline")
	}
	if strings.TrimSpace(p.Machine) != "" {
		out = append(out, "a machine")
	}
	return out
}

// hasTerminalPhase reports whether any step hands off nowhere: no static
// next, and no routing field to pick one. That step is where an
// unattended run finishes and what it returns.
func (d MachineDef) hasTerminalPhase() bool {
	for _, p := range d.Phases {
		if strings.TrimSpace(p.Next) == "" && strings.TrimSpace(p.RoutesBy()) == "" {
			return true
		}
	}
	return false
}

// residentNames lists the steps that wait, for a message that names them
// rather than leaving somebody to find them.
func residentNames(d MachineDef) []string {
	var out []string
	for _, p := range d.Phases {
		if p.Resident {
			out = append(out, strconv.Quote(p.Name))
		}
	}
	return out
}

// phaseProblems collects one phase's wiring problems.
func (d MachineDef) phaseProblems(p MachinePhase, seen map[string]bool, declared map[string]map[string]PipelineFieldType) []string {
	var probs []string
	name := p.Name

	if tier := strings.ToLower(strings.TrimSpace(p.Model)); tier != "" && tier != "worker" && tier != "lead" {
		probs = append(probs, "step "+name+": model must be \"worker\" or \"lead\", got "+strconv.Quote(p.Model))
	}
	switch strings.ToLower(strings.TrimSpace(p.Think)) {
	case "", "on", "off":
	default:
		probs = append(probs, "step "+name+": think must be \"on\", \"off\", or empty to inherit, got "+strconv.Quote(p.Think))
	}
	if runners := phaseRunners(p); len(runners) > 1 {
		probs = append(probs, "step "+name+" names "+strings.Join(runners, " and ")+" — a step is run by ONE thing. "+
			"An agent brings its own persona and tools; a pipeline is a fixed recipe; a machine is a whole run of its own. "+
			"Keep whichever the step is really for.")
	}
	if t := strings.TrimSpace(p.Next); t != "" && !seen[t] {
		probs = append(probs, "step "+name+": next names unknown step "+strconv.Quote(t))
	}
	if !d.Unattended && !p.Resident && strings.TrimSpace(p.Next) == "" && p.RoutesBy() == "" {
		// A transient phase that hands off nowhere would run and then
		// strand the turn with no reply. Catch it here rather than
		// letting the driver degrade at run time.
		//
		// Exempt in an UNATTENDED machine, where this is not a dead end
		// but the finish line: the step that hands off nowhere is where
		// the run stops and what it returns. The inverse rule (a run must
		// HAVE one) is checked once for the machine, in problems().
		probs = append(probs, "step "+name+" passes on but goes nowhere — a step the person never takes a turn in has to hand off somewhere. Set next, or list choices for it to decide between.")
	}
	if p.Resident && len(p.Output) > 0 {
		// A resident phase's reply IS the user-facing message. Wrapping
		// it in a JSON contract would hand the person a decoded envelope
		// instead of an answer, so structure belongs on the transient
		// phases that feed this one.
		probs = append(probs, "step "+name+": output is not valid on a step the conversation waits in — its reply goes to the person, not to a decoder. Declare the structure on the step that feeds it.")
	}
	if strings.TrimSpace(p.Agent) != "" && p.Resident {
		// A resident phase is where the conversation lives. Delegating it
		// would mean the person is talking to something other than the
		// agent they opened, without being told.
		probs = append(probs, "step "+name+": agent is not valid on a step the conversation waits in — this is where the conversation lives, and handing it to a delegate means the person is talking to something they did not open. Delegate the step that does the work, and let this one report what came back.")
	}
	// The steps a deciding phase may choose between must be real, and the
	// framework's own routing field must not collide with one the author
	// declared. Both are save-time questions: the alternative is a name
	// that resolves to nothing at run time and falls back silently.
	if len(p.Choices) > 0 {
		switch {
		case p.Resident:
			probs = append(probs, "step "+name+": a step the conversation waits in cannot choose its next step by deciding — its reply goes to the person, not to a decoder. It leaves through change_phase or a guard.")
		case strings.TrimSpace(p.NextFrom) != "":
			probs = append(probs, "step "+name+": it both routes on the field "+p.NextFrom+" and lists steps to choose between. Keep one — the field wins today, so the list is doing nothing.")
		}
		for _, t := range p.Choices {
			if t = strings.TrimSpace(t); t != "" && !seen[t] {
				probs = append(probs, "step "+name+": it may choose "+strconv.Quote(t)+", which is not a step in this machine. Either add that step or remove it from the choices.")
			}
		}
		for _, f := range p.Output {
			if f.Name == BuiltinNextStep {
				probs = append(probs, "step "+name+": "+BuiltinNextStep+" is the field the framework declares for a step that chooses where to go, so declaring one of your own leaves two. Remove the field, or clear the choices and point next_from at it.")
			}
		}
	}
	if from := strings.TrimSpace(p.NextFrom); from != "" && p.Resident {
		probs = append(probs, "step "+name+": next_from is not valid on a step the conversation waits in (it routes off a declared output field, and a waiting step has none). Use next for a one-turn handoff, or a guard to leave on a condition.")
	} else if from != "" {
		// A declared set of targets must name real phases. Checked at
		// SAVE time, which is the whole point of declaring them: the
		// alternative is a name that resolves to nothing at run time and
		// falls back silently to next.
		if ph, found := d.Phase(name); found {
			for _, f := range ph.Output {
				if f.Name != from || len(f.Enum) == 0 {
					continue
				}
				for _, target := range f.Enum {
					if _, real := d.Phase(strings.TrimSpace(target)); !real {
						probs = append(probs, "step "+name+": "+from+" may return "+strconv.Quote(target)+
							", which is not a step in this machine. Either add that step or remove it from the choices.")
					}
				}
			}
		}
		switch t, ok := declared[name][from]; {
		case !ok:
			probs = append(probs, "step "+name+": next_from "+strconv.Quote(from)+" is not one of this step's declared output fields")
		case t != FieldString:
			probs = append(probs, "step "+name+": next_from "+from+" must be a string field holding a step name, but it is declared "+string(t))
		}
	}
	if g := strings.TrimSpace(p.Guard); g != "" && !p.Resident {
		// A guard runs when a USER TURN arrives at a phase. A transient
		// phase never sees one, so a guard there is config that can
		// never fire — silent dead weight the author would keep editing.
		probs = append(probs, "step "+name+": guard is only valid on a step the conversation waits in (a step that passes on never receives a user turn for a guard to judge)")
	}
	if t := strings.TrimSpace(p.GuardTo); t != "" {
		if !seen[t] {
			probs = append(probs, "step "+name+": guard_to names unknown step "+strconv.Quote(t))
		}
		if strings.TrimSpace(p.Guard) == "" {
			probs = append(probs, "step "+name+": guard_to is set but there is no guard to trip it")
		}
	}
	if len(p.ExitsTo) > 0 && !p.Resident {
		// change_phase happens DURING a turn — the model deciding the
		// request has moved on — and a step that passes on never holds
		// one. So this bounds nothing, the same way a guard on a
		// transient step judges nothing.
		probs = append(probs, "step "+name+": exits_to is only valid on a step the conversation waits in (it bounds change_phase, which happens during a turn, and a step that passes on never holds one). Set next, or list choices for it to decide between.")
	}
	for _, t := range p.ExitsTo {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			probs = append(probs, "step "+name+": exits_to names unknown step "+strconv.Quote(t))
		}
	}
	for _, k := range p.Keep {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			probs = append(probs, "step "+name+": keep names unknown step "+strconv.Quote(k))
		}
	}
	if err := doubleBraceProblem(name, "prompt", p.Prompt); err != nil {
		probs = append(probs, err.Error())
	}
	if p.Resident {
		// A resident step's prompt renders into the system prefix
		// (PhaseBlock), which must stay byte-identical from one turn to
		// the next or every turn re-pays cold prefill. So the VOLATILE
		// variables are refused here — each for its own reason — while
		// the session-stable ones ({original_input}, {user}, {agent},
		// {step}, {machine}) resolve fine. PhaseBlock zeroes the
		// volatile three regardless; this check is what turns a silent
		// blank into a save-time answer.
		for _, v := range []struct{ tok, why string }{
			{"{input}", "the person's message is already in the conversation, and pinning it would rewrite the cached prompt every turn"},
			{"{prev}", "it means the step run just before, within one turn — a step the conversation waits in IS the turn"},
			{"{now}", "a clock in a pinned prompt rewrites the cached prompt every turn; the framework already stamps the time on the turn itself"},
			{"{established}", "what earlier steps established is already composed into this step's block"},
		} {
			if strings.Contains(p.Prompt, v.tok) {
				probs = append(probs, "step "+name+": "+v.tok+" is not available in a step the conversation waits in — "+v.why+". The stable variables ({original_input}, {user}, {agent}, {step}, {machine}) work here.")
			}
		}
	}
	probs = append(probs, stateRefProblems(name, p.Prompt, seen, declared)...)
	// A field filled from a variable is only as good as the variable. A
	// reference that resolves to nothing would leave the field silently
	// empty, which is the failure mode that makes a value nobody can see
	// worse than one nobody set.
	for _, f := range p.Output {
		ref := strings.TrimSpace(f.From)
		if ref == "" {
			continue
		}
		probs = append(probs, stateRefProblems(name, ref, seen, declared)...)
		if !strings.Contains(ref, "{state:") && !isBuiltinVarRef(ref) {
			probs = append(probs, "step "+name+": "+f.Name+" is filled from "+strconv.Quote(ref)+
				", which is not one of the built-in variables or a {state:PHASE.field} reference")
		}
	}
	if p.Guard != "" {
		probs = append(probs, stateRefProblems(name, p.Guard, seen, declared)...)
	}
	return probs
}

// stateRefProblems checks every {state:PHASE} / {state:PHASE.field}
// reference in a template resolves. Existence only — see Validate on why
// ordering isn't checked.
func stateRefProblems(where, tmpl string, seen map[string]bool, declared map[string]map[string]PipelineFieldType) []string {
	var probs []string
	for _, ref := range stateRefs(tmpl) {
		name, field := SplitStageRef(ref)
		if !seen[name] {
			probs = append(probs, "step "+where+": {state:"+ref+"} references unknown step "+strconv.Quote(name))
			continue
		}
		if field == "" {
			continue
		}
		if _, ok := declared[name][field]; !ok {
			probs = append(probs, "step "+where+": {state:"+ref+"} references a field step "+name+" does not declare")
		}
	}
	return probs
}

// cycleProblems catches a transient cycle reachable from Start using
// only STATIC next edges — a machine that could never reach a reply.
//
// The walk stops at any phase carrying next_from, because a router's
// target is a run-time value: it may well be the resident phase, and
// calling that a cycle would reject the most useful machine there is.
// The run-time transition cap is what covers the dynamic case.
func (d MachineDef) cycleProblems(seen map[string]bool) []string {
	start := d.StartPhase()
	if start == "" || !seen[start] {
		return nil // already reported
	}
	visited := map[string]bool{}
	name := start
	for {
		p, ok := d.Phase(name)
		if !ok || p.Resident || p.RoutesBy() != "" {
			return nil
		}
		if visited[name] {
			return []string{"steps starting at " + start + " loop back on themselves without ever reaching a step the conversation can wait in, so a turn could never produce a reply"}
		}
		visited[name] = true
		next := strings.TrimSpace(p.Next)
		if next == "" || !seen[next] {
			return nil // already reported as a transient dead end / unknown target
		}
		name = next
	}
}

// stateRefs extracts the inner text of every {state:...} occurrence in a
// template — "route" from {state:route}, "route.target" from
// {state:route.target}. Unterminated occurrences are ignored: they can't
// resolve at run time either, and they stay in the prompt verbatim.
func stateRefs(tmpl string) []string {
	const open = "{state:"
	var out []string
	rest := tmpl
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, "}")
		if j < 0 {
			return out
		}
		if ref := strings.TrimSpace(rest[:j]); ref != "" {
			out = append(out, ref)
		}
		rest = rest[j+1:]
	}
}

// --- storage (per-user) ---------------------------------------------

// SaveMachineDef writes a machine def to the user's store, minting an ID
// on first save and stamping Updated. Returns the saved record.
func SaveMachineDef(udb Database, d MachineDef) MachineDef {
	if udb == nil {
		return d
	}
	if d.ID == "" {
		d.ID = UUIDv4()
	}
	if d.Created.IsZero() {
		d.Created = time.Now()
	}
	d.Updated = time.Now()
	udb.Set(MachineDefsTable, d.ID, d)
	// Every save path funnels through here — the editor, the machine tool,
	// revise, undo, import, duplicate, repair — which is why the share
	// index is synced from a hook rather than at each of those call sites.
	// A grant that silently fails to register on one path is a grant the
	// owner believes they made.
	if MachineSavedHook != nil {
		MachineSavedHook(d)
	}
	return d
}

// MachineSavedHook, when set by an app that keeps an index over machines,
// is called after every save with the stored record. Mirrors
// PipelineSavedHook, for the same reason and with the same contract.
var MachineSavedHook func(def MachineDef)

// LoadMachineDef reads a machine def by ID. ok=false when absent or when
// the record's owner doesn't match (defensive — a guessed ID from
// another user's space doesn't resolve).
func LoadMachineDef(udb Database, owner, id string) (MachineDef, bool) {
	if udb == nil || id == "" {
		return MachineDef{}, false
	}
	var d MachineDef
	if !udb.Get(MachineDefsTable, id, &d) {
		return MachineDef{}, false
	}
	if owner != "" && d.Owner != "" && d.Owner != owner {
		return MachineDef{}, false
	}
	return d, true
}

// ListMachineDefs returns the user's machine defs, most-recently-updated
// first.
func ListMachineDefs(udb Database, owner string) []MachineDef {
	if udb == nil {
		return nil
	}
	var out []MachineDef
	for _, k := range udb.Keys(MachineDefsTable) {
		var d MachineDef
		if !udb.Get(MachineDefsTable, k, &d) {
			continue
		}
		if owner != "" && d.Owner != "" && d.Owner != owner {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// DeleteMachineDef removes a machine def by ID.
func DeleteMachineDef(udb Database, id string) {
	if udb == nil || id == "" {
		return
	}
	def, existed := LoadMachineDef(udb, "", id)
	udb.Unset(MachineDefsTable, id)
	// Tell whoever depends on it, exactly as deleting a pipeline does. A
	// schedule can target a machine and so can an agent's dispatch list,
	// and without this their only notice is the next fire.
	if existed && MachineDeletedHook != nil {
		MachineDeletedHook(def.Owner, def.ID, def.Name)
	}
}

// MachineDeletedHook, when set by an app that owns dependents, is called
// after a machine is removed: owner, id, and the name it had (the name is
// gone from storage by then, and a reason reading "runs deleted machine
// \"\"" helps nobody).
var MachineDeletedHook func(owner, id, name string)

// --- export / import (portable recipe) ------------------------------

// ExportMachine returns a portable copy with storage/identity metadata
// stripped. ID travels for the same reason a pipeline's does: an agent
// references its machine by ID, so preserving it is what lets an
// agent+machine bundle land with its wiring intact.
func ExportMachine(d MachineDef) MachineDef {
	d.Owner = ""
	// Who it was shared WITH is this deployment's fact about this record,
	// not part of the recipe. Carrying it would either name strangers or
	// assert a grant in a deployment that never made it.
	d.AllowedUsers = nil
	d.Created = time.Time{}
	d.Updated = time.Time{}
	// A recipe carries a machine, not its history — and an undo snapshot
	// would double the size of every bundle for something the importer
	// can never take back anyway.
	d.Previous = nil
	return d
}

// ImportMachine assigns a recipe to owner and saves it. The traveled ID
// is kept when free and reminted when it would collide, so importing the
// same recipe twice makes a copy instead of clobbering.
//
// It does NOT demand the machine be runnable. Every storage door —
// the editor, the draft endpoint — deliberately keeps machines with
// outstanding problems, because the editor's checklist is where
// problems belong; an import door that refused them meant a legally
// stored draft could export and then never come back, and an agent
// bundled with it landed pointing at a machine the refusal threw away.
// Only structural emptiness is refused: a nameless recipe or one with
// no steps is not a draft, it is a decode accident.
func ImportMachine(udb Database, owner string, recipe MachineDef) (MachineDef, error) {
	if strings.TrimSpace(recipe.Name) == "" {
		return MachineDef{}, Error("machine recipe has no name")
	}
	if len(recipe.Phases) == 0 {
		return MachineDef{}, Error("machine recipe has no steps")
	}
	if recipe.ID != "" {
		if _, exists := LoadMachineDef(udb, "", recipe.ID); exists {
			recipe.ID = ""
		}
	}
	recipe.Owner = owner
	// A recipe arriving with a recipient list on it would assert a grant the
	// importer never made — and one naming users of a deployment it came from.
	// Export strips it; this strips it again, because an import door has to
	// hold on its own against a hand-written file.
	recipe.AllowedUsers = nil
	recipe.Created = time.Time{}
	recipe.Updated = time.Time{}
	return SaveMachineDef(udb, recipe), nil
}

// StarterMachine is a blank machine that VALIDATES: the shape someone
// gets when they ask for a new one.
//
// A working example rather than an empty object, because the rules a
// machine has to satisfy — at least one resident step, a step that
// passes on must hand off, a step the conversation waits in declares no
// output — are easier to read off something correct than out of a
// rejection. It models the current idiom: the prompt never places
// {input}, because the framework hands the message over anyway. An editor that opens on a definition the server would
// refuse teaches the wrong lesson about the feature in the first ten
// seconds.
//
// Lives here rather than in the editor's JavaScript so there is one
// copy and a test can prove the claim in this comment.
func StarterMachine() MachineDef {
	return MachineDef{
		Name:        "New machine",
		Description: "What this machine is for, and when to reach for it.",
		Start:       "look",
		Phases: []MachinePhase{
			{
				Name:   "look",
				Desc:   "Work out what is actually being asked.",
				Think:  "on",
				Prompt: "Work out what is being asked, and hand it forward.",
				Next:   "reply",
				Output: []PipelineField{
					{Name: "summary", Type: FieldString, Required: true,
						Desc: "what they are actually asking, in one sentence"},
				},
			},
			{
				Name:     "reply",
				Desc:     "Answer, and keep answering.",
				Resident: true,
				Prompt:   "Answer plainly, working from what is settled below.",
			},
		},
	}
}
