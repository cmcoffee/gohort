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

	Global  bool      `json:"global,omitempty"`
	Created time.Time `json:"created,omitempty"` // stripped on export
	Updated time.Time `json:"updated,omitempty"` // stripped on export
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
	NextFrom string `json:"next_from,omitempty"`

	// Guard is the re-entry condition for a resident phase, judged once
	// per user turn. Prose, because the question it asks ("is this still
	// the same job?") is not one a field reference can answer. Empty
	// means transitions happen only via the change_phase tool.
	//
	// DECLARED IN ST1, EVALUATED IN ST2 — Validate already enforces its
	// shape so the schema doesn't move when the evaluator lands.
	Guard   string `json:"guard,omitempty"`
	GuardTo string `json:"guard_to,omitempty"` // where a tripped guard goes; empty = Start

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

// NextPhase resolves where control goes after ph produced fields.
//
// Returns the target phase name and, when the resolution was not the
// plain static one, a note for the caller's breadcrumb — a router whose
// choice didn't resolve is exactly the kind of framework decision that
// must not vanish into a debug log.
func (d MachineDef) NextPhase(ph MachinePhase, fields map[string]any) (string, string) {
	if ph.NextFrom == "" {
		return ph.Next, ""
	}
	raw, present := fields[ph.NextFrom]
	if !present {
		return ph.Next, "phase " + ph.Name + " declared next_from " + ph.NextFrom + " but the reply carried no such field; falling back to " + fallbackLabel(ph.Next)
	}
	want, _ := raw.(string)
	want = strings.TrimSpace(want)
	if want == "" {
		return ph.Next, "phase " + ph.Name + " routed to an empty phase name; falling back to " + fallbackLabel(ph.Next)
	}
	if _, ok := d.Phase(want); !ok {
		return ph.Next, "phase " + ph.Name + " routed to unknown phase " + strconv.Quote(want) + "; falling back to " + fallbackLabel(ph.Next)
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
		if len(p.Output) > 0 && asksForRawJSON(p.Prompt) {
			out = append(out, "phase "+name+": the prompt asks for JSON, but this phase already declares "+
				"fields — the framework encodes them for you and validates what comes back. Delete the "+
				"format instructions and the example, and say what to FIND instead. Two sets of "+
				"formatting rules is how a model ends up returning a JSON string inside a JSON field.")
		}
	}
	return out
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
		return []string{"machine has no phases"}
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
			probs = append(probs, "phase "+strconv.Itoa(i+1)+" has no name")
			continue
		case seen[p.Name]:
			probs = append(probs, "duplicate phase name: "+p.Name)
			continue
		case strings.Contains(p.Name, "."):
			probs = append(probs, "phase name may not contain a dot: "+p.Name)
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
	if resident == 0 {
		probs = append(probs, "no phase is resident — a machine with nowhere for a user turn to land is a pipeline, not a machine (set resident on the phase that replies)")
	}
	if s := d.StartPhase(); s != "" && !seen[s] {
		probs = append(probs, "start names unknown phase "+strconv.Quote(s))
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

// phaseProblems collects one phase's wiring problems.
func (d MachineDef) phaseProblems(p MachinePhase, seen map[string]bool, declared map[string]map[string]PipelineFieldType) []string {
	var probs []string
	name := p.Name

	if tier := strings.ToLower(strings.TrimSpace(p.Model)); tier != "" && tier != "worker" && tier != "lead" {
		probs = append(probs, "phase "+name+": model must be \"worker\" or \"lead\", got "+strconv.Quote(p.Model))
	}
	switch strings.ToLower(strings.TrimSpace(p.Think)) {
	case "", "on", "off":
	default:
		probs = append(probs, "phase "+name+": think must be \"on\", \"off\", or empty to inherit, got "+strconv.Quote(p.Think))
	}
	if t := strings.TrimSpace(p.Next); t != "" && !seen[t] {
		probs = append(probs, "phase "+name+": next names unknown phase "+strconv.Quote(t))
	}
	if !p.Resident && strings.TrimSpace(p.Next) == "" && strings.TrimSpace(p.NextFrom) == "" {
		// A transient phase that hands off nowhere would run and then
		// strand the turn with no reply. Catch it here rather than
		// letting the driver degrade at run time.
		probs = append(probs, "phase "+name+" is transient but names neither next nor next_from — a phase the user never takes a turn in has to hand off somewhere")
	}
	if p.Resident && len(p.Output) > 0 {
		// A resident phase's reply IS the user-facing message. Wrapping
		// it in a JSON contract would hand the person a decoded envelope
		// instead of an answer, so structure belongs on the transient
		// phases that feed this one.
		probs = append(probs, "phase "+name+": output is not valid on a resident phase — its reply goes to the user, not to a decoder. Declare the structure on the transient phase that feeds it.")
	}
	if strings.TrimSpace(p.Agent) != "" && p.Resident {
		// A resident phase is where the conversation lives. Delegating it
		// would mean the person is talking to something other than the
		// agent they opened, without being told.
		probs = append(probs, "phase "+name+": agent is not valid on a resident phase — this is where the conversation lives, and handing it to a delegate means the person is talking to something they did not open. Delegate the transient step that does the work, and let this phase report what came back.")
	}
	if from := strings.TrimSpace(p.NextFrom); from != "" && p.Resident {
		probs = append(probs, "phase "+name+": next_from is not valid on a resident phase (it routes off a declared output field, and a resident phase has none). Use next for a one-turn handoff, or a guard to leave on a condition.")
	} else if from != "" {
		switch t, ok := declared[name][from]; {
		case !ok:
			probs = append(probs, "phase "+name+": next_from "+strconv.Quote(from)+" is not one of this phase's declared output fields")
		case t != FieldString:
			probs = append(probs, "phase "+name+": next_from "+from+" must be a string field holding a phase name, but it is declared "+string(t))
		}
	}
	if g := strings.TrimSpace(p.Guard); g != "" && !p.Resident {
		// A guard runs when a USER TURN arrives at a phase. A transient
		// phase never sees one, so a guard there is config that can
		// never fire — silent dead weight the author would keep editing.
		probs = append(probs, "phase "+name+": guard is only valid on a resident phase (a transient phase never receives a user turn for a guard to judge)")
	}
	if t := strings.TrimSpace(p.GuardTo); t != "" {
		if !seen[t] {
			probs = append(probs, "phase "+name+": guard_to names unknown phase "+strconv.Quote(t))
		}
		if strings.TrimSpace(p.Guard) == "" {
			probs = append(probs, "phase "+name+": guard_to is set but there is no guard to trip it")
		}
	}
	for _, k := range p.Keep {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			probs = append(probs, "phase "+name+": keep names unknown phase "+strconv.Quote(k))
		}
	}
	if err := doubleBraceProblem(name, "prompt", p.Prompt); err != nil {
		probs = append(probs, err.Error())
	}
	if p.Resident {
		// {input} / {prev} are TURN-LOCAL, and a resident phase's prompt
		// renders into the system prefix (PhaseBlock) where it is
		// supposed to stay byte-identical from one turn to the next.
		// Interpolating the current message there would re-pay cold
		// prefill every turn to tell the model something the
		// conversation already carries.
		for _, tok := range []string{"{input}", "{prev}"} {
			if strings.Contains(p.Prompt, tok) {
				probs = append(probs, "phase "+name+": "+tok+" is not available in a resident phase — its prompt is pinned in the system prompt across turns, and the user's message is already in the conversation. Use {state:...} for what earlier phases established.")
			}
		}
	}
	probs = append(probs, stateRefProblems(name, p.Prompt, seen, declared)...)
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
			probs = append(probs, "phase "+where+": {state:"+ref+"} references unknown phase "+strconv.Quote(name))
			continue
		}
		if field == "" {
			continue
		}
		if _, ok := declared[name][field]; !ok {
			probs = append(probs, "phase "+where+": {state:"+ref+"} references a field phase "+name+" does not declare")
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
		if !ok || p.Resident || strings.TrimSpace(p.NextFrom) != "" {
			return nil
		}
		if visited[name] {
			return []string{"phases starting at " + start + " loop back on themselves without ever reaching a resident phase, so a turn could never produce a reply"}
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
	return d
}

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
	udb.Unset(MachineDefsTable, id)
}

// --- export / import (portable recipe) ------------------------------

// ExportMachine returns a portable copy with storage/identity metadata
// stripped. ID travels for the same reason a pipeline's does: an agent
// references its machine by ID, so preserving it is what lets an
// agent+machine bundle land with its wiring intact.
func ExportMachine(d MachineDef) MachineDef {
	d.Owner = ""
	d.Created = time.Time{}
	d.Updated = time.Time{}
	d.Global = false // scope is a local decision
	return d
}

// ImportMachine assigns a recipe to owner, validates it, and saves it.
// The traveled ID is kept when free and reminted when it would collide,
// so importing the same recipe twice makes a copy instead of clobbering.
func ImportMachine(udb Database, owner string, recipe MachineDef) (MachineDef, error) {
	if recipe.ID != "" {
		if _, exists := LoadMachineDef(udb, "", recipe.ID); exists {
			recipe.ID = ""
		}
	}
	recipe.Owner = owner
	recipe.Created = time.Time{}
	recipe.Updated = time.Time{}
	if err := recipe.Validate(); err != nil {
		return MachineDef{}, err
	}
	return SaveMachineDef(udb, recipe), nil
}

// StarterMachine is a blank machine that VALIDATES: the shape someone
// gets when they ask for a new one.
//
// A working example rather than an empty object, because the rules a
// machine has to satisfy — at least one resident phase, a transient
// phase must hand off, a resident phase declares no output and cannot
// template {input} — are easier to read off something correct than out
// of a rejection. An editor that opens on a definition the server would
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
				Prompt: "Work out what is being asked, and hand it forward.\n\n{input}",
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
