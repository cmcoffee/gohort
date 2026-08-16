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
	on    bool
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
	cur := &MachineCursor{Phase: t.session.Phase, State: t.session.MachineState, Log: t.session.MachineLog}
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
	t.machine = turnMachine{def: def, phase: ph, state: cur.State, on: true}
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
	if t.session.Phase == cur.Phase && len(t.session.MachineState) == len(cur.State) && len(t.session.MachineLog) == len(cur.Log) {
		return
	}
	t.session.Phase = cur.Phase
	t.session.MachineState = cur.State
	t.session.MachineLog = cur.Log
	t.saveSession()
}

// machineCatalog is the tool pool a transient phase draws from.
//
// ST1 GIVES IT NOTHING, deliberately. A transient phase runs during
// system-prompt assembly, before the turn has built its ToolSession, and
// the live catalog is wired to that session (GetAgentToolsWithSession) —
// tools that stage files, attach images, and hold per-turn state. Handing
// a phase a half-wired copy would be worse than handing it none.
//
// It costs nothing today: the phases this exists to serve (decompose,
// route, classify) are prompt-in-JSON-out work, and a phase that names
// tools it can't reach takes the tool-less path, the same degradation a
// pipeline stage takes (see resolveStageTools). The seam is here, and
// filling it is what moves transient phases onto the real catalog when a
// machine turns up that needs one.
func (t *chatTurn) machineCatalog() []AgentToolDef {
	return nil
}

// completeMachine closes the turn: a resident phase that names a Next
// hands off now that it has had its turn.
func (t *chatTurn) completeMachine(m turnMachine) {
	if !m.on || t.session == nil {
		return
	}
	cur := &MachineCursor{Phase: t.session.Phase, State: t.session.MachineState, Log: t.session.MachineLog}
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
			if t.phaseChanges >= maxPhaseChangesPerTurn {
				t.turnDiag("machine_change_capped", "refused a further phase change to "+to+" this turn (limit "+strconv.Itoa(maxPhaseChangesPerTurn)+")")
				return "", Error("you have already changed phase " + strconv.Itoa(t.phaseChanges) + " times this turn. Answer from where you are; you can move again on the next turn.")
			}
			t.phaseChanges++

			cur := &MachineCursor{Phase: t.session.Phase, State: t.session.MachineState, Log: t.session.MachineLog}
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
			t.machine = turnMachine{def: m.def, phase: ph, state: cur.State, on: true}

			return "Phase changed to " + ph.Name + ". The current-phase block in your system prompt is now out of date — these instructions replace it for the rest of this turn:\n" +
				m.def.PhaseBlock(ph, cur.State), nil
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
	return m.def.PhaseBlock(m.phase, m.currentState())
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
