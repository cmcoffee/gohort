// The machine runtime: a session's position, the blackboard it carries,
// and the driver that walks transient phases at the head of a turn.
//
// The split with the host is deliberate and narrow. Core owns the WALK —
// which phase is current, what its prompt resolves to, decoding its
// declared output, where control goes next, what state survives. The host
// owns the CALL: how a phase actually reaches an LLM, with which tools,
// on which tier, and what it does with the reply. Core never learns what
// a session, a persona, or a tool catalog is, which is what keeps this
// usable by any app rather than only by orchestrate.
//
// See machine_def.go for the recipe layer and docs/agent-machines.md for
// the design.

package core

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PhaseResult is one phase's product: the raw reply text plus, when the
// phase declared an Output contract, its decoded fields.
//
// Both are kept for the same reason stageOutput keeps both — adding
// structure shouldn't take anything away, so {state:NAME} can still read
// the whole reply as text while {state:NAME.field} addresses one field.
type PhaseResult struct {
	Text   string         `json:"text,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// MachineState is the blackboard: one entry per phase that has run,
// keyed by phase name. Persisted on the session (host side), which is
// what makes a machine's control flow durable across user turns.
type MachineState map[string]PhaseResult

// PhaseHop is one recorded transition.
//
// The breadcrumb trail already narrates these as sentences, and St1
// deliberately did not add a second home for them. The graph overlay is
// what changed the calculus: "which edges has this conversation actually
// taken" is a structural question, and answering it by parsing
// framework-authored prose would break silently the first time someone
// reworded a message.
type PhaseHop struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	Why  string    `json:"why,omitempty"`
	At   time.Time `json:"at,omitempty"`
}

// maxPhaseLog bounds the trail. A machine conversation that has taken
// more than this many transitions has a design problem the last fifty
// hops will show just as well as all of them.
const maxPhaseLog = 50

// MachineCursor is a session's position in a machine: where it is, what
// got decided on the way there, and how it got here. The host persists
// this and hands it back on the next turn.
type MachineCursor struct {
	Phase string       `json:"phase,omitempty"`
	State MachineState `json:"state,omitempty"`
	Log   []PhaseHop   `json:"log,omitempty"`
}

// PhaseRunner executes ONE phase's LLM call on the host's behalf and
// returns the raw reply. The driver supplies the fully-resolved prompt;
// the host decides how the call is made (a worker completion, a full
// agent loop, whatever that host does) and applies the phase's Tools /
// Model / Think itself, since core has no idea what those mean.
//
// Decoding a declared Output is the DRIVER's job, not the runner's:
// that path includes a repair retry, which calls the runner a second
// time with a different prompt.
type PhaseRunner func(ctx context.Context, ph MachinePhase, prompt string) (string, error)

// AdvanceMachine walks the transient phases at the head of a turn and
// returns the phase that owns the REPLY.
//
// It runs every transient phase it passes through (writing each result
// into cur.State) and stops the moment it reaches a resident one, which
// it returns WITHOUT running: that phase is the actual user-facing turn
// and the host runs it with its own streaming, tools, and guardrails.
// So turn 1 of a decompose → route → answer machine makes two calls here
// and returns "answer"; turns 2..N make none and return "answer"
// immediately, because the cursor is already sitting there.
//
// cur is updated in place and is what the host persists. note receives a
// breadcrumb for every framework decision taken on the user's behalf —
// a reset, a routing fallback, a state trim, the transition cap. A guard
// that alters a turn and leaves no trace is the failure the diagnostics
// trail exists to prevent, and every degradation path below is one.
//
// Errors are returned only when the machine cannot produce a reply at
// all. Everything else degrades toward "answer the user from somewhere
// sensible", because the user is waiting on a turn.
func (T *AppCore) AdvanceMachine(ctx context.Context, def MachineDef, cur *MachineCursor, input string, run PhaseRunner, note func(kind, detail string)) (MachinePhase, error) {
	if cur == nil {
		return MachinePhase{}, Error("machine " + def.Name + ": nil cursor")
	}
	if run == nil {
		return MachinePhase{}, Error("machine " + def.Name + ": no phase runner")
	}
	if note == nil {
		note = func(string, string) {}
	}
	if cur.State == nil {
		cur.State = MachineState{}
	}

	ph, resumed, err := def.resume(cur, note)
	if err != nil {
		return MachinePhase{}, err
	}
	// The guard judges a NEW user turn arriving at a phase the session
	// was already parked in. It deliberately does not run on a phase the
	// walk just entered: there is nothing to re-decide about a phase the
	// machine reached one line ago, and guarding it would ask a model
	// whether to undo the routing decision made moments earlier.
	if resumed && ph.Resident {
		if moved, tripped := T.checkGuard(ctx, def, ph, cur, input, run, note); tripped {
			ph = moved
		}
	}

	return T.walk(ctx, def, cur, ph, input, run, note)
}

// ChangePhase moves a session to a named phase MID-TURN and returns the
// phase that owns the rest of it, running any transient phases the move
// passes through on the way.
//
// This is what the host's change_phase tool calls: the model, partway
// through a turn, recognising that the conversation has moved on. It is
// the second of the two transition mechanisms (see machine_guard.go) and
// it lands in the same place the first one does — moveTo, the same
// breadcrumbs, the same walk — so a phase reached this way is
// indistinguishable from one the guard reached.
//
// What it CANNOT change is the turn's already-assembled system prompt,
// tool catalog, and tier. Those were fixed before the first round. The
// host is expected to hand the returned phase's block back to the model
// as the tool result, so the new directive arrives as the most recent
// thing in the context; the rest catches up on the next turn.
func (T *AppCore) ChangePhase(ctx context.Context, def MachineDef, cur *MachineCursor, to, input string, run PhaseRunner, note func(kind, detail string)) (MachinePhase, error) {
	if cur == nil {
		return MachinePhase{}, Error("machine " + def.Name + ": nil cursor")
	}
	if cur.State == nil {
		cur.State = MachineState{}
	}
	if note == nil {
		note = func(string, string) {}
	}
	target, ok := def.Phase(to)
	if !ok {
		return MachinePhase{}, Error("machine " + def.Name + " has no phase named " + strconv.Quote(strings.TrimSpace(to)))
	}
	from := cur.Phase
	if from == target.Name {
		return target, nil
	}
	cur.moveTo(from, target, chooseStr(strings.TrimSpace(input), "changed mid-turn"), note)
	note("machine_phase_changed", "moved from phase "+from+" to "+target.Name+" mid-turn")
	return T.walk(ctx, def, cur, target, input, run, note)
}

// walk runs transient phases until control reaches one that can reply.
// Shared by the head-of-turn entry (AdvanceMachine) and a mid-turn move
// (ChangePhase) so there is exactly one implementation of what a
// transition costs and where it stops.
func (T *AppCore) walk(ctx context.Context, def MachineDef, cur *MachineCursor, ph MachinePhase, input string, run PhaseRunner, note func(kind, detail string)) (MachinePhase, error) {
	var prev string // the text of the phase run just before this one, THIS turn
	for hops := 0; ; hops++ {
		if ph.Resident {
			return ph, nil
		}
		if hops >= MaxPhaseTransitions {
			// Reply from where we stand rather than keep walking. The
			// check sits BEFORE the call deliberately: the phase we
			// return has not been run this iteration, so the host
			// running it as the reply is the first time it fires, not a
			// second.
			note("machine_transition_cap", "machine "+def.Name+" made "+strconv.Itoa(hops)+" phase transitions without reaching a resident phase; replying from "+ph.Name)
			return ph, nil
		}

		text, fields, err := T.runPhase(ctx, def, ph, input, prev, cur.State, run, note)
		if err != nil {
			return MachinePhase{}, err
		}
		cur.State[ph.Name] = PhaseResult{Text: text, Fields: fields}
		prev = text

		next, why := def.NextPhase(ph, fields)
		if why != "" {
			note("machine_route_fallback", why)
		}
		nph, ok := def.Phase(next)
		if !ok {
			// A router whose choice didn't resolve and that declared no
			// static fallback. Validate can't reach this (next_from is
			// checked, Next is optional), so it is a live path: send the
			// turn to the phase that exists to reply.
			nph, ok = def.firstResident()
			if !ok {
				return MachinePhase{}, Error("machine " + def.Name + ": phase " + ph.Name + " handed off nowhere and the machine has no resident phase")
			}
			note("machine_dead_end", "phase "+ph.Name+" handed off nowhere; replying from "+nph.Name)
		}
		cur.moveTo(ph.Name, nph, chooseStr(why, "routed by "+ph.Name), note)
		ph = nph
	}
}

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
	return func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		// A transient phase defaults to NOT reasoning: it is a bounded
		// transform (split this up, pick a lane) sitting in front of the
		// user's actual turn, and the latency it adds is paid before
		// anyone sees a word. Authors opt in per phase.
		think := PhaseThink(ph, false)
		return T.runWorkerStage(ctx, prompt, PhaseTools(ph, catalog), think, len(ph.Output) > 0, PhaseTier(ph))
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
		note("machine_dead_end", "phase "+ph.Name+" hands off to unknown phase "+next+"; staying put")
		return
	}
	cur.moveTo(ph.Name, nph, "handed off after one turn", note)
	note("machine_phase_advance", "phase "+ph.Name+" has had its turn; moving to "+nph.Name)
}

// runPhase resolves one transient phase's prompt and calls it, decoding
// a declared Output through the same contract → decode → one repair path
// pipeline stages use.
func (T *AppCore) runPhase(ctx context.Context, def MachineDef, ph MachinePhase, input, prev string, st MachineState, run PhaseRunner, note func(kind, detail string)) (string, map[string]any, error) {
	prompt := ResolvePhaseTemplate(ph.Prompt, input, prev, st)
	call := func(p string) (string, error) { return run(ctx, ph, p) }

	if len(ph.Output) == 0 {
		text, err := call(prompt)
		if err != nil {
			return "", nil, Error("machine " + def.Name + ", phase " + ph.Name + ": " + err.Error())
		}
		return text, nil, nil
	}
	status := func(s string) { note("machine_output_repair", s) }
	text, fields, err := T.runDeclaredOutput(ctx, "phase "+ph.Name, ph.Output, prompt, call, status)
	if err != nil {
		return "", nil, Error("machine " + def.Name + ", phase " + ph.Name + ": " + err.Error())
	}
	return text, fields, nil
}

// resume resolves the phase a turn opens on, healing a cursor that no
// longer matches the machine.
//
// A machine edited underneath a live session is the ordinary case, not
// an exotic one: the phase a session is parked in can simply stop
// existing. Falling back to Start while KEEPING the state is the same
// posture as broken-dependency safety — keep the thing, surface the
// break, never silently discard what the session already established.
// The bool reports whether this was a genuine RESUME — the cursor named
// a phase that still exists — as opposed to a first entry or a healed
// fallback. Only a resume can be guarded.
func (d MachineDef) resume(cur *MachineCursor, note func(kind, detail string)) (MachinePhase, bool, error) {
	if ph, ok := d.Phase(cur.Phase); ok {
		return ph, true, nil
	}
	start := d.StartPhase()
	ph, ok := d.Phase(start)
	if !ok {
		return MachinePhase{}, false, Error("machine " + d.Name + " has no phase " + start)
	}
	if strings.TrimSpace(cur.Phase) != "" {
		note("machine_phase_reset", "phase "+cur.Phase+" is no longer part of machine "+d.Name+"; resuming at "+ph.Name+" with state kept")
	}
	cur.Phase = ph.Name
	return ph, false, nil
}

// moveTo records a transition into a phase, applying that phase's Keep
// list when this is a RE-ENTRY (it has already run in this session).
//
// Trimming only on re-entry, and only when Keep is non-empty, is what
// makes the default safe: a re-route that silently wipes the
// decomposition is the expensive mistake. Resuming a phase the cursor is
// already parked in is not a transition and never trims, or a machine
// with a Keep list would shed state on every ordinary turn.
func (cur *MachineCursor) moveTo(from string, to MachinePhase, why string, note func(kind, detail string)) {
	cur.Phase = to.Name
	if from != "" && from != to.Name {
		cur.Log = append(cur.Log, PhaseHop{From: from, To: to.Name, Why: why, At: time.Now()})
		if len(cur.Log) > maxPhaseLog {
			cur.Log = cur.Log[len(cur.Log)-maxPhaseLog:]
		}
	}
	if from == to.Name || len(to.Keep) == 0 {
		return
	}
	if _, again := cur.State[to.Name]; !again {
		return
	}
	keep := make(map[string]bool, len(to.Keep))
	for _, k := range to.Keep {
		keep[strings.TrimSpace(k)] = true
	}
	var dropped []string
	for name := range cur.State {
		if !keep[name] {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) == 0 {
		return
	}
	sort.Strings(dropped)
	for _, name := range dropped {
		delete(cur.State, name)
	}
	note("machine_state_trim", "re-entering phase "+to.Name+" dropped state from "+strings.Join(dropped, ", "))
}

// chooseStr returns a when it has content, else b. Lives here rather
// than with the graph renderer that also uses it: the engine must not
// depend on the drawing layer.
func chooseStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// firstResident returns the first phase a reply can come from, in
// declared order.
func (d MachineDef) firstResident() (MachinePhase, bool) {
	for _, p := range d.Phases {
		if p.Resident {
			return p, true
		}
	}
	return MachinePhase{}, false
}

// ResolvePhaseTemplate substitutes the machine templating vocabulary
// into a phase prompt:
//
//	{input}             — the user message that opened this turn
//	{prev}              — the phase run immediately before, THIS turn
//	{state:NAME}        — a phase's reply text, from any earlier turn
//	{state:NAME.field}  — one declared field of a phase's result
//
// {input} and {prev} are turn-local and belong to transient phases; a
// resident phase's prompt lands in the cacheable system prefix and
// Validate rejects both there (see phaseProblems).
//
// Plain literal replacement is enough even with the field form:
// {state:route} can't match inside {state:route.target} because the
// closing brace is part of the literal. Unknown placeholders are left
// untouched rather than blanked, so a mistake degrades to a visible
// prompt artifact instead of silently dropping context.
func ResolvePhaseTemplate(tmpl, input, prev string, st MachineState) string {
	s := strings.ReplaceAll(tmpl, "{input}", input)
	s = strings.ReplaceAll(s, "{prev}", prev)
	for name, res := range st {
		s = strings.ReplaceAll(s, "{state:"+name+"}", res.Text)
		for field, v := range res.Fields {
			s = strings.ReplaceAll(s, "{state:"+name+"."+field+"}", renderFieldValue(v))
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
// order (never map order) and resolves only {state:...}, which changes
// solely when a transient phase writes. Across a resident run of N turns
// the prefix is identical and the prompt cache holds; render one map
// iteration in here and every turn re-pays cold prefill.
//
// The current phase's own prior result is left out: it is the phase
// talking, and handing it its own last answer invites it to repeat it.
func (d MachineDef) PhaseBlock(ph MachinePhase, st MachineState) string {
	var b strings.Builder
	b.WriteString("\n\n## Current phase: ")
	b.WriteString(ph.Name)
	b.WriteString("\n")
	if desc := strings.TrimSpace(ph.Desc); desc != "" {
		b.WriteString(desc)
		b.WriteString("\n")
	}
	if p := strings.TrimSpace(ResolvePhaseTemplate(ph.Prompt, "", "", st)); p != "" {
		b.WriteString("\n")
		b.WriteString(p)
		b.WriteString("\n")
	}

	if est := d.establishedBlock(ph, st); est != "" {
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
	if len(d.Phases) > 1 {
		b.WriteString("\n## Other phases in this workflow\n")
		b.WriteString("Reachable with change_phase, and only when the request has genuinely moved on. A follow-up or a clarification is the same job: stay here.\n")
		for _, p := range d.Phases {
			if p.Name == ph.Name {
				continue
			}
			b.WriteString("- " + p.Name)
			if desc := strings.TrimSpace(p.Desc); desc != "" {
				b.WriteString(": " + desc)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderPhaseFindings renders one phase's result for the pinned block:
// its declared fields in declared order, or its reply text when it
// declared none.
func renderPhaseFindings(p MachinePhase, res PhaseResult) string {
	if len(p.Output) == 0 {
		return strings.TrimSpace(res.Text)
	}
	var b strings.Builder
	single := len(p.Output) == 1
	for _, f := range p.Output {
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

// PhaseTools narrows a catalog to what a phase may reach. Empty Tools
// inherits the whole catalog, matching resolveStageTools so the two
// authoring surfaces behave the same.
func PhaseTools(ph MachinePhase, catalog []AgentToolDef) []AgentToolDef {
	return resolveStageTools(ph.Tools, catalog)
}

// PhaseThink applies a phase's reasoning override on top of whatever
// the host already resolved (route default, then per-agent). The phase
// is the most specific setting in that chain, so it goes last; an empty
// Think inherits and returns base untouched.
func PhaseThink(ph MachinePhase, base bool) bool {
	switch strings.ToLower(strings.TrimSpace(ph.Think)) {
	case "on":
		return true
	case "off":
		return false
	}
	return base
}

// PhaseTier maps a phase's Model onto the loop's tier override.
// TierUnset (the zero value) follows the agent's own routing.
func PhaseTier(ph MachinePhase) LLMTier {
	switch strings.ToLower(strings.TrimSpace(ph.Model)) {
	case "worker":
		return WORKER
	case "lead":
		return LEAD
	}
	return TierUnset
}
