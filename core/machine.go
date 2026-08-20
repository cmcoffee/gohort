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
	if len(ph.Tools) > 0 || PhaseReach(ph) != ReachAll {
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

// Reach values for MachinePhase.Reach — the coarse tool scope, and the
// only part of a phase's tool setting that survives being carried to
// another agent or another deployment. See the field's own comment.
const (
	ReachAll  = ""     // inherit the agent's whole catalog
	ReachRead = "read" // only what reads: nothing that writes, runs, or reaches the network
	ReachNone = "none" // nothing at all; the step decides or reshapes and hands on
)

// ReachAllowsCaps is the capability set a reach permits, or nil for "no
// restriction". One definition, so the filter and anything that explains
// the filter cannot disagree about what "read-only" means.
func ReachAllowsCaps(reach string) []Capability {
	if strings.TrimSpace(reach) == ReachRead {
		return []Capability{CapRead}
	}
	return nil
}

// PhaseReach is the phase's reach, reading the legacy marker as the
// setting it always meant. A stored ["__none__"] predates the Reach
// field and says exactly what ReachNone says.
func PhaseReach(ph MachinePhase) string {
	if r := strings.TrimSpace(ph.Reach); r != "" {
		return r
	}
	for _, n := range ph.Tools {
		if strings.TrimSpace(n) == NoToolsMarker {
			return ReachNone
		}
	}
	return ReachAll
}

// PhaseTools narrows a catalog to what a phase may reach: the reach
// first (a capability class, which travels), then the phase's own names
// on top of what is left (exact strings, which do not).
//
// Empty Tools inherits whatever the reach allowed — matching
// resolveStageTools, so an author who learned one surface has learned
// the other.
func PhaseTools(ph MachinePhase, catalog []AgentToolDef) []AgentToolDef {
	switch PhaseReach(ph) {
	case ReachNone:
		return nil
	case ReachRead:
		catalog = FilterToolsByCaps(catalog, ReachAllowsCaps(ReachRead))
	}
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

// --- child runs -------------------------------------------------------

// MaxMachineDepth caps how deep a machine may run machines.
//
// One, which means a run may have children and those children may not.
// The case this exists for is research forking a gap-filling run per gap
// it finds, and research's own guard is exactly this: a child skips gap
// filling so the tree cannot grow a third level.
//
// A NUMBER rather than a bool because the next case that wants two should
// be able to argue for two by changing this, rather than by unpicking a
// design that assumed one.
const MaxMachineDepth = 1

// machineDepthKey carries the current nesting depth on the context.
//
// On the CONTEXT rather than on a struct because the child runs through
// the host's own PhaseRunner, which is the same closure the parent uses.
// The depth has to travel with the call rather than with the caller, or a
// child's phases would look exactly like a parent's and nothing would stop
// the third level.
type machineDepthKey struct{}

// WithMachineDepth returns a context carrying d as the nesting depth.
func WithMachineDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, machineDepthKey{}, d)
}

// MachineDepth reports how many machines deep this call already is. Zero
// for a top-level run, which is what an absent value means.
func MachineDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	d, _ := ctx.Value(machineDepthKey{}).(int)
	return d
}
