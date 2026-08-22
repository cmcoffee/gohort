package orchestrate

// Turn-side wiring for session-resident phase machines (core/machine.go,
// docs/agent-machines.md).
//
// The core driver owns the walk: which phase is current, what its prompt
// resolves to, decoding its declared output, where control goes next.
// This file owns the four things that are orchestrate's business — where
// the cursor is persisted, how a phase reaches the agent's real tool
// catalog, how it lands in the system prompt, and where its breadcrumbs
// go.
//
// The whole path is inert for an agent with no machine, which is every
// agent today: turnMachine.on stays false and each accessor returns its
// caller's input unchanged.

import (
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// turnMachine is what a machine contributes to ONE turn: the phase that
// owns the reply, plus the accessors runPlan folds into its assembly.
//
// It is a value, not a pointer, and its zero value is the no-machine
// case. That matters at the call sites: an agent without a machine takes
// the same four lines and they all no-op, so there is no second code
// path through the turn to keep in sync.
type turnMachine struct {
	def   MachineDef
	phase MachinePhase
	state MachineState
	// vars is the session-stable subset a resident block may resolve —
	// who is talking, which agent, what opened the conversation.
	// PhaseBlock zeroes the volatile fields itself, so carrying a full
	// MachineTurn here is safe.
	vars PhaseVars
	on   bool
}

// machineTurn is the facts about THIS turn that only the host has: who
// is talking, which agent they opened, and what time it is where they
// are. A step's prompt reaches them as {user}, {agent} and {now}.
//
// The time is stamped in the PERSON's zone, not the server's. A machine
// triaging "this started about an hour ago" against a timestamp in
// another timezone is worse than one with no clock at all.
func (t *chatTurn) machineTurn(msg string) MachineTurn {
	return MachineTurn{
		Input: msg,
		User:  t.user,
		Agent: chFirst(t.agent.Name, t.agent.ID),
		Now:   time.Now().In(UserLocation(t.user)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}
}

// enterMachine resolves the session's machine, walks any transient
// phases at the head of this turn, and persists the cursor it lands on.
//
// Called during system-prompt assembly, which is the only moment that
// works: the transient phases have to run BEFORE the persona is built,
// because what they establish is part of it.
func (t *chatTurn) enterMachine(userMsg string) turnMachine {
	if t.agent.Machine == "" || t.session == nil {
		return turnMachine{}
	}
	def, ok := t.sessionMachine()
	if !ok {
		return turnMachine{}
	}
	cur := &MachineCursor{Phase: t.session.Phase, State: t.session.MachineState, Log: t.session.MachineLog, Opening: t.session.MachineOpening}
	ph, err := t.app.AdvanceMachine(t.ctx, def, cur, t.machineTurn(userMsg), t.phaseRunner(), t.turnDiag)
	if err != nil {
		// A machine that cannot produce a phase must not cost the user
		// the turn. Fall back to an ordinary agent turn, loudly: the
		// breadcrumb is the only thing standing between this and an
		// agent that quietly stopped being what its author configured.
		t.turnDiag("machine_failed", "machine "+def.Name+" could not resolve a phase ("+err.Error()+"); this turn ran without it")
		return turnMachine{}
	}
	t.persistCursor(cur)
	t.machine = turnMachine{def: def, phase: ph, state: cur.State, on: true,
		vars: PhaseVars{MachineTurn: t.machineTurn(""), Opening: cur.Opening}}
	return t.machine
}

// hasMachineExit reports whether change_phase is worth offering: a
// running machine with somewhere else to go. A one-phase machine has no
// exit, and a tool whose only honest answer is "there is nowhere to go"
// is a tool that teaches the model to ignore its catalog.
func (t *chatTurn) hasMachineExit() bool {
	return t.machine.on && len(t.machine.def.Phases) > 1
}

// sessionMachine resolves the def this SESSION runs, pinning it on first
// use.
//
// The pin is the point. Re-pointing an agent at a different machine
// reshapes new conversations; a session already parked in a phase keeps
// the machine that phase belongs to, because the alternative is a
// cursor pointing into a machine that no longer contains it — every
// turn, silently, for every open session.
func (t *chatTurn) sessionMachine() (MachineDef, bool) {
	id := strings.TrimSpace(t.session.MachineID)
	if id == "" {
		id = strings.TrimSpace(t.agent.Machine)
	}
	def, ok := LoadMachineDef(t.udb, t.user, id)
	if !ok {
		// A machine deleted out from under a live session, or an agent
		// pointing at one that never saved. Broken-dependency posture:
		// say so, run the turn without it, change nothing on disk.
		t.turnDiag("machine_missing", "machine "+id+" is no longer available; this turn ran without it")
		return MachineDef{}, false
	}
	if t.session.MachineID != def.ID {
		t.session.MachineID = def.ID
		t.saveSession()
	}
	return def, true
}

// persistCursor writes the walked cursor back to the session. Skips the
// save when nothing moved, so an ordinary resident turn (the common
// case, and the one that runs no phases at all) doesn't touch the store.
func (t *chatTurn) persistCursor(cur *MachineCursor) {
	if !cursorDiffers(t.session, cur) {
		return
	}
	t.session.Phase = cur.Phase
	t.session.MachineState = cur.State
	t.session.MachineLog = cur.Log
	t.session.MachineOpening = cur.Opening
	t.saveSession()
}

// cursorDiffers is the save gate. Length comparison alone was a trap
// with the log at its cap (maxPhaseLog): a guard trip that circles back
// to the same phase appends hops, the trim holds the length at the cap,
// and phase + lengths all compare equal — the exact turn that most
// needs recording skipped its save. The last hop is the tell: any
// transition this turn changes it.
func cursorDiffers(sess *ChatSession, cur *MachineCursor) bool {
	if sess.Phase != cur.Phase || len(sess.MachineState) != len(cur.State) ||
		len(sess.MachineLog) != len(cur.Log) || sess.MachineOpening != cur.Opening {
		return true
	}
	if n := len(cur.Log); n > 0 {
		a, b := sess.MachineLog[n-1], cur.Log[n-1]
		if a.From != b.From || a.To != b.To || !a.At.Equal(b.At) {
			return true
		}
	}
	return false
}

// machineCatalog is the tool pool a transient phase draws from: the
// agent's whole catalog, which PhaseWorker then narrows to whatever the
// step named (see PhaseTools).
//
// UNCHECKED MEANS EVERYTHING, the same as it does while a conversation
// waits in a resident phase. The two used to disagree — an empty list
// inherited the catalog in one and revoked it in the other — and the
// disagreement was invisible, because it is the same control in the same
// editor and the only thing that flips its meaning is a toggle three
// fields up. What that cost is a step written to go and look running with
// nothing to look with, then reporting, accurately, that it had not been
// given what it was sent to fetch. A step that genuinely wants no catalog
// says so with the marker (phaseWantsNoTools) rather than by leaving a
// control empty.
//
// The pool is built at most once per turn and only when a step actually
// runs, which is what keeps the flip affordable: a machine whose steps
// decompose and route pays one catalog build for the turn, not one per
// step. It is a real cost where there used to be none, and it buys a
// control that means the same thing in both places it appears.
//
// A transient phase runs during system-prompt assembly, before the turn
// has built its ToolSession, and the live catalog is wired to that
// session (GetAgentToolsWithSession) — tools that stage files, attach
// images, and hold per-turn state. That is why this builds a session of
// the step's OWN rather than waiting for the turn's, and why the tools it
// hands out are wired rather than half-wired.
func (t *chatTurn) machineCatalog(ph MachinePhase) []AgentToolDef {
	// The one way to say "no tools" now that unchecked means everything.
	// Kept as a real setting rather than inferred from an empty list,
	// because both ends of the control have to be sayable: a step that
	// only decides or reshapes what it was given wants no catalog at all,
	// and it should not have to tick every box to express the opposite.
	if PhaseReach(ph) == ReachNone {
		return nil
	}
	if t.machineTools == nil {
		// A session of its own, built the way a pipeline's sub-run builds
		// one mid-turn: it shares the turn's caches and dispatch counts,
		// and what it stages is folded back into the turn below. The
		// turn's OWN session does not exist yet — a step runs during
		// system-prompt assembly, hundreds of lines before newToolSession
		// is called for the round — which is why this was empty for so
		// long, and why the fix is a session rather than a wait.
		sess := t.newToolSession()
		defer t.captureActiveWorkspace(sess)
		pool, _, err := t.resolveWorkerTools(sess, false)
		if err != nil {
			t.turnDiag("machine_tools_unavailable", "step "+ph.Name+" names tools but the catalog could not be resolved ("+err.Error()+"); it ran without them")
			return nil
		}
		// The agent's ATTACHED reach, on the same footing as the turn's
		// own catalog has it.
		//
		// resolveWorkerTools assembles the registered pool; attached
		// sources and attached pipelines are appended by the turn's
		// catalog build (runPlan) and by nothing else, so a step naming
		// search_<store> or run_<pipeline> narrowed to nothing and ran
		// tool-less — reporting, truthfully, that it had not been given
		// the thing it was sent to fetch. The step that goes and looks is
		// the whole reason a step names tools at all, and the things it
		// looks IN are attachments.
		pool = append(pool, t.buildAttachedSourceToolDefs(sess)...)
		pool = append(pool, t.buildAttachedPipelineToolDefs()...)
		// Prefixed so the activity pane reads as what it is: work done
		// inside a step, before the turn's own answer began.
		t.machineTools = t.wrapToolsForActivity(sess, pool, t.agent, "↳ [step] ")
		t.noteStepToolCalls(t.machineTools)
		// Kept because the approval hook reads it: which credential a
		// call rides on is session state, so the gate has to be built
		// against the SAME session the tools were.
		t.machineSess = sess
	}
	t.reportUnreachableStepTools(ph)
	return t.machineTools
}

// reportUnreachableStepTools says which of a step's named tools the pool
// does not carry.
//
// The resident path reports this (narrowCatalog's unmatched list, and its
// refusal to resolve a total miss to nothing); a transient step had
// neither. It gets exactly what it named and no more, so a name that
// misses is subtracted in silence — and a step whose whole list missed
// runs with no tools at all, then answers from the prompt alone. That is
// how "the logs were not provided" comes back from a step written to go
// and read them.
//
// Reported, not repaired: a step is a bounded transform with nobody
// waiting on its control plane, so running it tool-less is survivable in
// a way the resident case is not. What was missing is the part nothing
// said.
func (t *chatTurn) reportUnreachableStepTools(ph MachinePhase) {
	if len(ph.Tools) == 0 {
		return
	}
	// Judged against what the step will ACTUALLY be handed — the pool
	// after its own reach — so a name the reach dropped is reported as
	// the step's two controls disagreeing rather than as a tool nobody
	// has.
	reachable := PhaseTools(ph, t.machineTools)
	have := make(map[string]bool, len(reachable))
	for _, td := range reachable {
		have[td.Tool.Name] = true
	}
	inPool := make(map[string]bool, len(t.machineTools))
	for _, td := range t.machineTools {
		inPool[td.Tool.Name] = true
	}
	var missing []string
	for _, n := range ph.Tools {
		n = strings.TrimSpace(n)
		if n == "" || n == NoToolsMarker || have[n] {
			continue
		}
		if inPool[n] {
			missing = append(missing, n+" (dropped by this step's reach)")
			continue
		}
		missing = append(missing, n)
	}
	if len(missing) == 0 {
		return
	}
	Log("[orchestrate.orch] step %q names %d tool(s) this turn's catalog does not carry: %v",
		ph.Name, len(missing), missing)
	detail := "step " + ph.Name + " names " + strings.Join(missing, ", ") +
		", which this agent's catalog does not carry under those names, so the step ran without them. " +
		"Names must match the catalog exactly — an attached source mints its own (search_<store>), " +
		"and a remote MCP tool is published as \"<server>_<tool>\" in lowercase."
	if len(missing) == len(ph.Tools) {
		detail = "step " + ph.Name + " reached NONE of the tools it names (" + strings.Join(missing, ", ") +
			"), so it ran with no tools at all and could only answer from its prompt. " +
			"Check the names against the agent's catalog, and check the agent is attached to the source they come from."
	}
	t.turnDiag("machine_step_tools_missing", detail)
}

// machineConfirm is the approval gate a step's tools go through: the
// turn's own. A step that could reach a tool the turn itself would have
// stopped to ask about would be a hole under the approval card.
func (t *chatTurn) machineConfirm() func(name, args string) bool {
	if t.machineSess == nil {
		return nil
	}
	return t.confirmFuncFor(t.machineSess)
}

// completeMachine closes the turn: a resident phase that names a Next
// hands off now that it has had its turn.
func (t *chatTurn) completeMachine(m turnMachine) {
	if !m.on || t.session == nil {
		return
	}
	cur := &MachineCursor{Phase: t.session.Phase, State: t.session.MachineState, Log: t.session.MachineLog, Opening: t.session.MachineOpening}
	before := cur.Phase
	m.def.CompleteTurn(cur, m.phase, t.turnDiag)
	if cur.Phase == before {
		return
	}
	t.session.Phase = cur.Phase
	t.session.MachineState = cur.State
	t.session.MachineLog = cur.Log
	t.saveSession()
}

// saveSession writes the turn's session record back, keeping the
// in-memory copy in step with what landed. Same shape as every other
// mid-turn session write in runner.go.
func (t *chatTurn) saveSession() {
	if t.session == nil {
		return
	}
	if saved, err := saveChatSession(t.udb, *t.session); err == nil {
		*t.session = saved
	}
}

// maxPhaseChangesPerTurn caps how many times the model may move itself
// in one turn.
//
// Two, because the shape it exists to allow is "this isn't what I
// thought — go re-decompose, then answer", which is one move, and a
// second one covers a correction. Past that it is thrashing: each change
// runs transient phases, so an uncapped tool is a way for a confused
// model to spend the turn re-routing instead of replying.
const maxPhaseChangesPerTurn = 2

// changePhaseToolDef gives a resident phase a way out.
//
// Granted only when a machine is running and has somewhere else to go.
// The alternative to offering it is a conversation that can only leave a
// phase when the author wrote a guard, and most authors won't write one
// for every phase — so without this, "the agent is stuck in intake" is
// the default failure of every machine anyone builds.
func (t *chatTurn) changePhaseToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "change_phase",
			Description: "Move this conversation to a different PHASE of your current workflow. Use it when the user's request no longer belongs to the phase you are in — a new subject that needs re-planning, or work that belongs to a later step. Do NOT use it for a follow-up, a clarification, or a related question: those are the same job. The phases available to you are listed in your current-phase block. Takes effect immediately: the result tells you what the new phase expects.",
			Parameters: map[string]ToolParam{
				"phase": {Type: "string", Description: "Name of the phase to move to, exactly as listed in your current-phase block."},
				"why":   {Type: "string", Description: "One short sentence: what about this request put it outside the current phase."},
			},
			Required: []string{"phase", "why"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			m := t.machine
			if !m.on {
				return "", Error("this conversation is not running a phase machine, so there is no phase to change to")
			}
			to := strings.TrimSpace(stringArg(args, "phase"))
			why := strings.TrimSpace(stringArg(args, "why"))
			if to == "" {
				return "", Error("name the phase to move to")
			}
			if to == m.phase.Name {
				return "You are already in phase " + to + ". Carry on with the turn.", nil
			}
			if _, ok := m.def.Phase(to); !ok {
				return "", Error("there is no phase named " + to + " in this workflow. Available: " + strings.Join(m.def.PhaseNames(), ", "))
			}
			// Refused here as well as in the driver, so the model gets the
			// answer as a tool result it can act on rather than a failed
			// turn — and the message names where it MAY go, because "no"
			// without an alternative just gets tried again.
			if !m.phase.MayExitTo(to) {
				var allowed []string
				for _, p := range m.def.ExitOptions(m.phase) {
					allowed = append(allowed, p.Name)
				}
				t.turnDiag("machine_exit_refused", "the model tried to move from "+m.phase.Name+" to "+to+", which this step does not allow")
				if len(allowed) == 0 {
					return "", Error("this step is where the conversation stays; it has no other phase to move to. Answer from here.")
				}
				return "", Error(m.phase.Name + " cannot move to " + to + ". From here you may go to: " + strings.Join(allowed, ", "))
			}
			if t.phaseChanges >= maxPhaseChangesPerTurn {
				t.turnDiag("machine_change_capped", "refused a further phase change to "+to+" this turn (limit "+strconv.Itoa(maxPhaseChangesPerTurn)+")")
				return "", Error("you have already changed phase " + strconv.Itoa(t.phaseChanges) + " times this turn. Answer from where you are; you can move again on the next turn.")
			}
			t.phaseChanges++

			cur := &MachineCursor{Phase: t.session.Phase, State: t.session.MachineState, Log: t.session.MachineLog, Opening: t.session.MachineOpening}
			ph, err := t.app.ChangePhase(t.ctx, m.def, cur, to, t.machineTurn(why), t.phaseRunner(), t.turnDiag)
			if err != nil {
				return "", err
			}
			t.session.Phase = cur.Phase
			t.session.MachineState = cur.State
			t.session.MachineLog = cur.Log
			t.saveSession()
			// Keep the rest of the turn (and the end-of-turn handoff)
			// pointed at where we actually are.
			t.machine = turnMachine{def: m.def, phase: ph, state: cur.State, on: true,
				vars: PhaseVars{MachineTurn: t.machineTurn(""), Opening: cur.Opening}}

			return "Phase changed to " + ph.Name + ". The current-phase block in your system prompt is now out of date — these instructions replace it for the rest of this turn:\n" +
				m.def.PhaseBlock(ph, cur.State, t.machine.vars), nil
		},
	}
}

// --- what the turn folds in -------------------------------------------

// Block is the phase layer for the system prompt. Appended AFTER the
// persona for the same reason the round-shape preamble goes before it:
// recency weights, and the phase is the most authoritative instruction
// in the turn.
func (m turnMachine) Block() string {
	if !m.on {
		return ""
	}
	return m.def.PhaseBlock(m.phase, m.currentState(), m.vars)
}

// currentState is the blackboard as of this turn. Held on the turn
// rather than re-read, so the block and the templating see the same
// snapshot.
func (m turnMachine) currentState() MachineState { return m.state }

// Tools narrows the assembled catalog to what this phase may reach.
// Empty Tools inherits everything, so a machine that never mentions
// tools changes nothing about what the agent can do.
func (m turnMachine) Tools(catalog []AgentToolDef) []AgentToolDef {
	if !m.on {
		return catalog
	}
	return PhaseTools(m.phase, catalog)
}

// machineControlTools are the framework's own control plane: how a turn
// ends, and how the machine moves. A phase's Tools list is a statement
// about the agent's REACH — which systems it may touch while it is here —
// and never about whether the loop still functions, so these survive the
// narrowing no matter what the phase names.
//
// change_phase is the load-bearing one. A phase that dropped it could not
// be LEFT: the model had no way out of the step it was standing in, for
// the rest of the session, and nothing it could say would change that.
// The others are how a turn reaches an end at all — without them the model
// can neither answer, plan, decline, nor continue.
//
// resolveWorkerTools already force-includes this class past the agent's own
// allowlist for exactly this reason; the phase filter runs downstream of
// that decision and must not quietly undo it.
// noteStepToolCalls makes a step's tool calls visible to anything that
// asks what this TURN did.
//
// A step runs before the turn's loop exists, on a session of its own, so
// the loop's accounting never sees it — and the end-of-turn judge, which
// asks whether the reply is true about the turn's work, read a turn whose
// step went and searched as a turn that sat still. It then convicted the
// reply for opening "based on the Confluence research", which was exactly
// what had happened. Reported live as a false positive, and it is the
// whole class: any turn whose work was done by a step rather than by the
// model answering.
//
// Wrapped here rather than counted in the runner because this is the one
// place that knows a call is a STEP's: the same catalog is handed to
// every step of the turn.
func (t *chatTurn) noteStepToolCalls(tools []AgentToolDef) {
	for i := range tools {
		name, inner := tools[i].Tool.Name, tools[i].Handler
		if inner == nil {
			continue
		}
		tools[i].Handler = func(args map[string]any) (string, error) {
			out, err := inner(args)
			// Only what SUCCEEDED. A failed call is not work the reply
			// may claim, and handing the judge a name that errored would
			// excuse the one reply it exists to catch.
			if err == nil {
				t.notePriorWork("a step ran " + name)
			}
			return out, err
		}
	}
}

// notePriorWork records one piece of work done for this turn before the
// turn's own loop began. Shares the tool ledger's lock: same turn, same
// contention, and a step's tools run on their own goroutines.
func (t *chatTurn) notePriorWork(what string) {
	if strings.TrimSpace(what) == "" {
		return
	}
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	t.priorWork = append(t.priorWork, what)
}

// priorWorkForJudge is the AgentLoopConfig.PriorWork hook: what ran for
// this turn outside the loop, as the judge is given it.
func (t *chatTurn) priorWorkForJudge() []string {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	return append([]string(nil), t.priorWork...)
}

// noteAttachedTools records what an attachment minted this turn, so the
// phase filter can tell a GRANT apart from a selection. Additive: the
// sources build and the pipelines build both call it.
func (t *chatTurn) noteAttachedTools(defs []AgentToolDef) {
	if len(defs) == 0 {
		return
	}
	if t.attachedToolNames == nil {
		t.attachedToolNames = make(map[string]bool, len(defs))
	}
	for _, td := range defs {
		if n := td.Tool.Name; n != "" {
			t.attachedToolNames[n] = true
		}
	}
}

// phaseGovernsAttachments reports whether this phase's Tools list is a
// statement about the agent's ATTACHMENTS, which is true only when it
// names one of their tools.
//
// The rule the whole exemption turns on. A list that names none of them
// is a selection out of the worker pool — the pool the picker offers, and
// the only one the author was choosing from — so it says nothing either
// way about a source somebody attached in a different picker for a
// different reason. A list that names even one has clearly been written
// with attachments in mind, and from there the author gets exactly what
// they wrote: naming one source and not the other has to be able to mean
// "that one, not this one", or the control is no control at all.
func (m turnMachine) phaseGovernsAttachments(attached map[string]bool) bool {
	for _, n := range m.phase.Tools {
		if attached[strings.TrimSpace(n)] {
			return true
		}
	}
	return false
}

var machineControlTools = map[string]bool{
	"change_phase":     true,
	"plan_set":         true,
	"respond_directly": true,
	"stay_silent":      true,
	"keep_going":       true,
}

// narrowCatalog is Tools plus what it took away: the names this phase
// removed, the names it ASKED for that the catalog never had, and whether
// the ask missed so completely that the narrowing was abandoned.
//
// Both lists are needed because a silent narrowing is indistinguishable,
// from inside the turn, from a tool that never existed. The model has its
// own successful calls from an earlier phase in front of it, so when the
// name stops resolving it concludes it mistyped and retries spellings —
// and nothing in the log said otherwise, because the full-surface catalog
// line is printed upstream of this filter and reported the wider set.
//
// The unmatched list catches the other half: a phase naming a tool by a
// name the catalog doesn't use (an MCP tool is exposed as
// "<server>_<tool>", LOWERCASED by sanitizeToolName, never the raw remote
// name — Atlassian's camelCase getConfluencePage is atlassian_getconfluencepage
// here) narrows toward nothing instead of reporting that its allow-list missed.
//
// fellBack reports the end of that road: a list where NOTHING matched.
// Read literally that means "this phase may use no tools", but no author
// writes a list of names to express emptiness — they write it to express a
// selection, and a selection that resolves to zero is a typo every time.
// Taken literally it also costs the model its control plane, so the turn
// ends up unable to answer or advance. We keep the catalog whole and say so
// instead; a phase that genuinely wants no tools says that by naming none.
func (m turnMachine) narrowCatalog(catalog []AgentToolDef, attached map[string]bool) (out []AgentToolDef, dropped, unmatched []string, fellBack bool) {
	narrowed := m.Tools(catalog)
	if !m.on || (len(m.phase.Tools) == 0 && PhaseReach(m.phase) == ReachAll) {
		return narrowed, nil, nil, false
	}
	// The explicit "nothing": the control plane and the agent's
	// attachments, and not one tool more. Handled before the name match so
	// the marker is never reported as a name the catalog is missing, and
	// never triggers the total-miss rescue below — this IS the case where
	// emptiness was meant.
	if PhaseReach(m.phase) == ReachNone {
		for _, td := range catalog {
			if machineControlTools[td.Tool.Name] || attached[td.Tool.Name] {
				out = append(out, td)
				continue
			}
			dropped = append(dropped, td.Tool.Name)
		}
		return out, dropped, nil, false
	}
	// An attachment is a grant, not a selection — see the field comment on
	// chatTurn.attachedToolNames. Unless this phase names one of their
	// tools, they pass through the filter the way the control plane does.
	if m.phaseGovernsAttachments(attached) {
		attached = nil
	}
	kept := make(map[string]bool, len(narrowed))
	for _, td := range narrowed {
		kept[td.Tool.Name] = true
	}
	inCatalog := make(map[string]bool, len(catalog))
	for _, td := range catalog {
		inCatalog[td.Tool.Name] = true
	}
	for _, n := range m.phase.Tools {
		n = strings.TrimSpace(n)
		if n == "" || n == NoToolsMarker || kept[n] {
			continue
		}
		// Two different mistakes wearing one word. A name the catalog
		// HAS, dropped by the reach, is a step whose two controls argue
		// with each other — say which one won, or the author reads it as
		// the tool having gone missing and goes looking for it.
		if inCatalog[n] {
			unmatched = append(unmatched, n+" (dropped by this step's reach)")
			continue
		}
		unmatched = append(unmatched, n)
	}
	// A total miss is still a total miss even when attachments would have
	// carried the turn: the list was written to express a selection, and one
	// that resolves to nothing is a typo whatever else survives it.
	if len(narrowed) == 0 {
		return catalog, nil, unmatched, true
	}
	// Rebuilt in CATALOG order rather than appending the exempt tools to the
	// end: the payload the model sees stays byte-stable across turns, which
	// is what keeps the prompt cache warm.
	out = make([]AgentToolDef, 0, len(narrowed)+len(machineControlTools))
	for _, td := range catalog {
		switch {
		case kept[td.Tool.Name], machineControlTools[td.Tool.Name], attached[td.Tool.Name]:
			out = append(out, td)
		default:
			dropped = append(dropped, td.Tool.Name)
		}
	}
	return out, dropped, unmatched, false
}

// Tier is the phase's model override, feeding AgentLoopConfig.
// TierOverride. TierUnset (no machine, or a phase that names no model)
// follows the agent's own routing exactly as before.
func (m turnMachine) Tier() LLMTier {
	if !m.on {
		return TierUnset
	}
	return PhaseTier(m.phase)
}

// Think applies the phase's reasoning override on top of the value the
// turn already resolved (route default, then per-agent). The phase is
// the most specific setting in that chain, so it goes last.
func (m turnMachine) Think(base bool) bool {
	if !m.on {
		return base
	}
	return PhaseThink(m.phase, base)
}

// Name identifies the current phase for logs and (St3) the phase pill.
// Empty when no machine is running, which is what every existing log
// line will keep printing.
func (m turnMachine) Name() string {
	if !m.on {
		return ""
	}
	return m.phase.Name
}
