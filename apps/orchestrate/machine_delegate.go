// The two hosts a machine run can have.
//
// A conversation has a person in it: a live catalog, an approval gate to ask,
// an activity surface to narrate to, and a session the delegate's own thread
// hangs off. A run has none of that, and used to get none of the STEP KINDS
// either — the schedule and the Run button ran a delegating step as an ordinary
// prompt, so the machine returned something that read right and had done none of
// the work its author arranged.
//
// Both now build a machineHost (machine_host.go) and differ only in what they
// put in it. The step dispatch itself is written once.

package orchestrate

import (
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// machineHost is the conversational host: everything comes from the turn.
func (t *chatTurn) machineHost() *machineHost {
	h := &machineHost{
		app:       t.app,
		user:      t.user,
		udb:       t.udb,
		catalog:   t.machineCatalog,
		confirm:   t.machineConfirm(),
		status:    t.emitStatus,
		note:      t.turnDiag,
		priorWork: t.notePriorWork,
		state:     func() MachineState { return t.machine.currentState() },
		turn:      t.machineTurn,
	}
	h.agentID, h.agentName = t.agent.ID, chFirst(t.agent.Name, t.agent.ID)
	if t.session != nil {
		h.thread = t.session.ID
	}
	// Progress out of a sub-run rides the turn's activity surface: these calls
	// run at the head of the turn with somebody waiting, and a pipeline of six
	// stages is a long silence otherwise.
	h.activity = func(source, text string) {
		t.sse.Send(map[string]any{
			"kind": "activity", "type": "cmd", "id": activityCheapID(),
			"text": source + ": " + text,
		})
	}
	return h
}

// phaseRunner is the turn's runner, unchanged for every caller that had one.
func (t *chatTurn) phaseRunner() PhaseRunner { return t.machineHost().phaseRunner() }

// unattendedRun is what a turn-free door knows about the run it is starting.
// Struct-first because the doors differ in three small ways and would otherwise
// pass five positional arguments each.
type unattendedRun struct {
	// User is whose store the run resolves references in and whose authority it
	// carries. For a dispatched machine that is the REQUESTER, not the owner.
	User string
	// Agent, when the run belongs to one. Empty is normal: the Run button and a
	// schedule aimed at a machine have no agent behind them, and the only thing
	// that costs is the self-delegation check, since there is no self.
	Agent AgentRecord
	// ID distinguishes this run from the next one. It is what a delegate's
	// sub-thread hangs off, so it decides whether two runs of the same machine
	// share a delegate's context: they must not, which is why the doors pass
	// something unique per run rather than the machine's id.
	ID string
	// Tools is the pool the run holds, already wrapped in whatever the door
	// wraps it in (the run cache). Steps narrow it; nothing widens it.
	Tools []AgentToolDef
	// Note takes the framework's breadcrumbs. Status narrates the step about to
	// run; Activity narrates progress coming OUT of a sub-run (a delegate's
	// thinking, a pipeline's stages). All optional, and separate because a
	// surface that already draws a block per step wants the second without the
	// first — the headline would just repeat the block it sits above.
	Note     func(kind, detail string)
	Status   func(text string)
	Activity func(source, text string)
	// Cursor is the run's blackboard, read when a tool step templates its
	// arguments. Optional, but a tool step in a machine without one can only
	// template {input} and {prev}.
	Cursor *MachineCursor
}

// unattendedHost builds the host for a run with nobody watching.
//
// The differences from a conversation are all absences, and each one is a
// decision rather than a gap:
//
//   - No approval gate. There is nobody to ask, so a step reaching a tool that
//     needs confirmation is governed by the autonomous pre-authorization the run
//     door applies, not by a prompt nobody would see. PhaseWorkerConfirm reads a
//     nil gate the same way.
//   - No prior-work ledger. That feeds the end-of-turn judge, which is a
//     property of a reply to a person.
//   - Narration is the run's, not the turn's: a streaming run panel gets status
//     lines, a schedule gets nothing.
func (T *OrchestrateApp) unattendedHost(run unattendedRun) *machineHost {
	pool := run.Tools
	h := &machineHost{
		app:     T,
		user:    run.User,
		udb:     UserDB(T.DB, run.User),
		agentID: strings.TrimSpace(run.Agent.ID),
		thread:  firstNonEmptyStr(strings.TrimSpace(run.ID), "run:"+strconv.FormatInt(time.Now().UnixNano(), 36)),
		// The run's whole pool, narrowed per step the same way the turn's is.
		// PhaseWorker narrows again by ph.Tools; ReachNone is the one way to say
		// "no tools at all", and it has to be honored here or a step that asked
		// for nothing would still be handed everything.
		catalog: func(ph MachinePhase) []AgentToolDef {
			if PhaseReach(ph) == ReachNone {
				return nil
			}
			return pool
		},
		note:   run.Note,
		status: run.Status,
	}
	h.agentName = chFirst(run.Agent.Name, run.Agent.ID)
	if h.agentName == "" {
		h.agentName = "this run"
	}
	if cur := run.Cursor; cur != nil {
		h.state = func() MachineState { return cur.State }
	}
	// A sub-run's progress is narrated as its own line: "researcher: searching…".
	// Falls back to the step narration when the door named no separate sink, and
	// to nothing at all when it named neither — which is the schedule.
	if run.Activity != nil {
		h.activity = run.Activity
	} else {
		h.activity = func(source, text string) { h.emitStatus(source + ": " + text) }
	}
	return h
}
