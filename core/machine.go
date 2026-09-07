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

	// Opening is the message that started the conversation, kept so
	// {original_input} means something on turn nine. Written once, on
	// the first walk, and never again — a step judging its work against
	// "what they originally asked" needs the ORIGINAL, and a value that
	// quietly became the latest message would answer a different
	// question with the same name.
	Opening string `json:"opening,omitempty"`
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
func (T *AppCore) AdvanceMachine(ctx context.Context, def MachineDef, cur *MachineCursor, turn MachineTurn, run PhaseRunner, note func(kind, detail string)) (MachinePhase, error) {
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
		if moved, tripped := T.checkGuard(ctx, def, ph, cur, turn.Input, run, note); tripped {
			ph = moved
		}
	}

	ph, _, err = T.walk(ctx, def, cur, ph, turn, run, note)
	return ph, err
}

// RunUnattended drives a machine that RUNS rather than converses: start
// it once, walk until a step hands off nowhere, hand back that step and
// what it produced.
//
// It is the same walk, the same blackboard, and the same breadcrumbs as a
// conversational turn. What differs is where it stops and what the stop
// MEANS. AdvanceMachine returns the phase that owes somebody a reply and
// leaves running it to the host, because the host owns streaming, tools
// and guardrails for the turn. Nobody is waiting on this one, so the walk
// runs every phase itself and the last one's text is the answer.
//
// cur is the caller's to keep. It carries the blackboard, which is the
// point of running a machine rather than a pipeline: twenty phases of
// accumulated findings live there, and a caller that throws the cursor
// away has thrown away the run's working memory along with it.
//
// The error and the text are BOTH returned on a run that stopped early.
// A scheduled run that quietly hands back half an answer is worse than
// one that fails, so the ceiling is an error; the partial text comes with
// it for a caller that would rather show something than nothing.
func (T *AppCore) RunUnattended(ctx context.Context, def MachineDef, cur *MachineCursor, turn MachineTurn, run PhaseRunner, note func(kind, detail string)) (MachinePhase, string, error) {
	if !def.Unattended {
		return MachinePhase{}, "", Error("machine " + def.Name + " is not marked unattended; it expects a conversation")
	}
	if cur == nil {
		return MachinePhase{}, "", Error("machine " + def.Name + ": nil cursor")
	}
	if run == nil {
		return MachinePhase{}, "", Error("machine " + def.Name + ": no phase runner")
	}
	if note == nil {
		note = func(string, string) {}
	}
	if cur.State == nil {
		cur.State = MachineState{}
	}
	ph, _, err := def.resume(cur, note)
	if err != nil {
		return MachinePhase{}, "", err
	}
	final, stop, err := T.walk(ctx, def, cur, ph, turn, run, note)
	if err != nil {
		return MachinePhase{}, "", err
	}
	text := cur.State[final.Name].Text
	switch stop {
	case stopTerminal:
		return final, text, nil
	case stopResident:
		// Validate reports this at save time; reaching it live means the
		// machine was marked unattended after the fact, or came from an
		// import. Say which step, because "the run stopped" is not
		// something anybody can act on.
		return final, text, Error("machine " + def.Name + ": step " + final.Name +
			" waits for a person, and an unattended run has nobody to wait for")
	default:
		return final, text, Error("machine " + def.Name + ": stopped after " + strconv.Itoa(MaxUnattendedTransitions) +
			" steps without finishing (last step " + final.Name + ")")
	}
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
func (T *AppCore) ChangePhase(ctx context.Context, def MachineDef, cur *MachineCursor, to string, turn MachineTurn, run PhaseRunner, note func(kind, detail string)) (MachinePhase, error) {
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
	// The step's own restriction, enforced in the DRIVER rather than in
	// the tool: a host with its own change_phase, a future surface, and
	// the tool all move a turn through here, and a rule that lives in
	// one caller is a rule the others do not have.
	if here, found := def.Phase(cur.Phase); found && !here.MayExitTo(target.Name) {
		note("machine_exit_refused", "step "+cur.Phase+" may not move to "+target.Name+"; it allows "+strings.Join(exitNames(def, here), ", "))
		return MachinePhase{}, Error("step " + cur.Phase + " cannot move to " + target.Name +
			". From here the conversation may go to: " + strings.Join(exitNames(def, here), ", "))
	}
	from := cur.Phase
	if from == target.Name {
		return target, nil
	}
	cur.moveTo(from, target, chooseStr(strings.TrimSpace(turn.Input), "changed mid-turn"), note, def.accumulatorNames())
	note("machine_phase_changed", "moved from step "+from+" to "+target.Name+" mid-turn")
	ph, _, err := T.walk(ctx, def, cur, target, turn, run, note)
	return ph, err
}

// exitNames is where a step may be moved, for a message that has to say
// what was allowed instead of what was refused.
func exitNames(def MachineDef, from MachinePhase) []string {
	opts := def.ExitOptions(from)
	out := make([]string, 0, len(opts))
	for _, p := range opts {
		out = append(out, p.Name)
	}
	if len(out) == 0 {
		return []string{"nowhere — this step is where the conversation stays"}
	}
	return out
}

// walk runs transient phases until control reaches one that can reply.
// Shared by the head-of-turn entry (AdvanceMachine) and a mid-turn move
// (ChangePhase) so there is exactly one implementation of what a
// transition costs and where it stops.
// walkStop says WHY the walk stopped, because the two modes hand back
// phases in opposite states and a caller that confuses them either runs a
// phase twice or never runs it at all.
//
//	stopResident — conversational. The returned phase has NOT run; the
//	               host runs it as the turn's reply.
//	stopTerminal — unattended. The returned phase HAS run and its result
//	               is on the blackboard; it is the run's answer.
//	stopBudget   — the hop ceiling. Same state as stopResident: the
//	               phase we stand on has not run this iteration.
type walkStop int

const (
	stopResident walkStop = iota
	stopTerminal
	stopBudget
)

func (T *AppCore) walk(ctx context.Context, def MachineDef, cur *MachineCursor, ph MachinePhase, turn MachineTurn, run PhaseRunner, note func(kind, detail string)) (MachinePhase, walkStop, error) {
	// The opening message is remembered once and then never changes, so
	// a step five turns in can still ask what the person originally
	// wanted. Recorded here rather than at the call site because every
	// entry into the machine passes through this walk.
	if strings.TrimSpace(cur.Opening) == "" {
		cur.Opening = turn.Input
	}
	vars := PhaseVars{MachineTurn: turn, Opening: cur.Opening, Machine: def.Name}
	hopCap := MaxPhaseTransitions
	if def.Unattended {
		hopCap = MaxUnattendedTransitions
	}
	for hops := 0; ; hops++ {
		if ph.Resident {
			// In an unattended run this is an authoring mistake Validate
			// already reports, and the walk cannot do anything useful with
			// it: there is no person to hand the turn to. Stop here and let
			// the caller say so rather than running a phase whose whole
			// contract is that somebody replies into it.
			return ph, stopResident, nil
		}
		if hops >= hopCap {
			// Reply from where we stand rather than keep walking. The
			// check sits BEFORE the call deliberately: the phase we
			// return has not been run this iteration, so the host
			// running it as the reply is the first time it fires, not a
			// second.
			if def.Unattended {
				note("machine_run_cap", "machine "+def.Name+" ran "+strconv.Itoa(hops)+" steps without finishing; stopping at "+ph.Name+
					". A run that reaches this ceiling is looping — check the guard or routing field that should have ended it.")
			} else {
				note("machine_transition_cap", "machine "+def.Name+" made "+strconv.Itoa(hops)+" step transitions without reaching a step the conversation waits in; replying from "+ph.Name)
			}
			return ph, stopBudget, nil
		}

		text, fields, err := T.runPhase(ctx, def, ph, vars, cur.State, run, note)
		if err != nil {
			return MachinePhase{}, stopBudget, err
		}
		cur.State[ph.Name] = PhaseResult{Text: text, Fields: fields}
		// The working set, immediately after this phase's own entry, so a
		// later phase reading {state:answers} sees this contribution too.
		def.accumulate(ph, fields, cur.State, note)
		vars.Prev = text

		next, why := def.NextPhase(ph, fields)
		if why != "" {
			note("machine_route_fallback", why)
		}
		// A step that hands off nowhere ENDS an unattended run, and the
		// step we just ran is the result. The conversational path treats
		// the same situation as a dead end to be rescued (below), because
		// there a turn still owes somebody a reply.
		if def.Unattended && strings.TrimSpace(next) == "" {
			return ph, stopTerminal, nil
		}
		nph, ok := def.Phase(next)
		if !ok {
			// A router whose choice didn't resolve and that declared no
			// static fallback. Validate can't reach this (next_from is
			// checked, Next is optional), so it is a live path: send the
			// turn to the phase that exists to reply.
			nph, ok = def.firstResident()
			if !ok {
				return MachinePhase{}, stopBudget, Error("machine " + def.Name + ": step " + ph.Name + " handed off nowhere and no step waits for the person")
			}
			note("machine_dead_end", "step "+ph.Name+" handed off nowhere; replying from "+nph.Name)
		}
		cur.moveTo(ph.Name, nph, chooseStr(why, "routed by "+ph.Name), note, def.accumulatorNames())
		ph = nph
	}
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
		note("machine_phase_reset", "step "+cur.Phase+" is no longer part of machine "+d.Name+"; resuming at "+ph.Name+" with state kept")
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
func (cur *MachineCursor) moveTo(from string, to MachinePhase, why string, note func(kind, detail string), protected map[string]bool) {
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
	// Keep prunes PHASE findings on re-entry so a re-route cannot leave a
	// step reading a stale decomposition. An accumulator is the opposite
	// kind of thing: it exists BECAUSE the run keeps coming back, and a
	// loop that wiped the answers it just spent twenty phases collecting
	// would be a data loss nobody could see. Named in Keep, it is kept
	// like anything else; unnamed, it survives rather than being dropped.
	if _, again := cur.State[to.Name]; !again {
		return
	}
	keep := make(map[string]bool, len(to.Keep))
	for _, k := range to.Keep {
		keep[strings.TrimSpace(k)] = true
	}
	var dropped []string
	for name := range cur.State {
		if !keep[name] && !protected[name] {
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
