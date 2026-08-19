// Dispatching a MACHINE, beside an agent and a pipeline.
//
// The third target, and the last one missing. A schedule has been able to fire
// a machine since v0.6.283 and the page has had a Run button since v0.6.274,
// but no agent could ask for a run — so the only way to reach a procedure from
// a conversation was to be the person who clicked the button. `machine=` closes
// that, and the shape is the one `pipeline=` already established: exactly one
// target is named, naming two is REFUSED rather than resolved by precedence,
// and everything checkable is checked before anything is spent.
//
// WHY IT IS A DIFFERENT TARGET FROM A PIPELINE, since they arrive through the
// same parameter list and run through the same guards: a pipeline is dataflow
// that forgets between stages, and a machine carries a working set from step to
// step and decides where to go next. "Gather what changed, keep what is new,
// and report on what it means" is a machine; "summarise these four documents in
// parallel" is a pipeline. An agent that can reach only one of them has to
// simulate the other in its own head, which is the thing these primitives exist
// to stop.
//
// ONLY AN UNATTENDED MACHINE. A conversational machine has a step the run would
// have to wait in, and a dispatch has nobody to wait for it — the caller is a
// model in the middle of a turn. That refusal is the same one the schedule
// gives, worded the same way, because it is the same fact about the machine and
// somebody who hits it in one place will hit it in the other.
//
// POSTURE. The schedule's and the page's: the run happens as the REQUESTER,
// with the requester's tool pool narrowed per step by each step's own Tools
// list. A machine that reached further when an agent asked for it than when a
// person pressed Run would be a surprise nobody asked for, and it is the same
// argument that decided a dispatched pipeline inherits no catalog.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxAdvertisedMachines caps how many machine names the agents tool lists in
// its own description, for the reason maxAdvertisedPipelines states: the list
// is prompt weight on every turn of every agent, and the model can still name
// any machine — the description is a hint, not the allowlist.
const maxAdvertisedMachines = 12

// dispatchedMachinesKey carries the machines already running above this point.
// It rides the CONTEXT rather than the turn for the reason
// dispatchedPipelinesKey states: a machine's delegating step dispatches through
// RunAgentSync, which starts each sub-turn with an empty chain, and a
// turn-local list would not survive the one hop that matters.
type dispatchedMachinesKey struct{}

// withDispatchedMachine records a machine as in-flight for everything run
// beneath it.
func withDispatchedMachine(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	prior := dispatchedMachines(ctx)
	next := make([]string, len(prior), len(prior)+1)
	copy(next, prior)
	return context.WithValue(ctx, dispatchedMachinesKey{}, append(next, id))
}

// dispatchedMachines returns the machine ids already in flight above ctx.
func dispatchedMachines(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	ids, _ := ctx.Value(dispatchedMachinesKey{}).([]string)
	return ids
}

// dispatchMachineCapKey namespaces a machine in the per-turn dispatch counters,
// which are keyed by agent id — a machine id colliding with an agent id would
// spend one budget on two different targets.
func dispatchMachineCapKey(id string) string { return "machine:" + id }

// machineUniverse is every machine this user can see, before policy: their own,
// then the ones shared with them.
//
// Unfiltered, like pipelineUniverse and for the same reason — the resolver has
// to tell "exists but not permitted" and "exists but converses" apart from "no
// such machine", and a pre-filtered list cannot.
func (t *chatTurn) machineUniverse() []MachineDef {
	own := ListMachineDefs(t.udb, t.user)
	out := make([]MachineDef, 0, len(own))
	names := make(map[string]bool, len(own))
	for _, d := range own {
		names[strings.ToLower(strings.TrimSpace(d.Name))] = true
		out = append(out, d)
	}
	// Own first, so a name that exists in both resolves to the user's own. A
	// shared one whose name collides is dropped rather than listed: the model
	// has names and not ids, so listing it would advertise a target that can
	// never be reached by the only handle the model has.
	for _, sm := range sharedMachinesFor(t.user) {
		if names[strings.ToLower(strings.TrimSpace(sm.Def.Name))] {
			Log("[orchestrate.machines] %q shared by %q is not dispatchable for %q: the name collides with one of their own",
				sm.Def.Name, sm.Owner, t.user)
			continue
		}
		out = append(out, sm.Def)
	}
	return out
}

// foreignMachine reports whether a dispatch target belongs to somebody else.
func (t *chatTurn) foreignMachine(d MachineDef) bool {
	owner := strings.TrimSpace(d.Owner)
	return owner != "" && owner != t.user
}

// machineDispatchAllowed applies this agent's dispatch policy to one machine.
// The single place the question is answered, so the advertised list and the
// gate cannot disagree.
//
// There is no DisabledMachines list to consult, and the absence is deliberate:
// DisabledPipelines exists because attaching a pipeline mints a run_<name> tool
// whose grant somebody may want to take back one at a time. A machine is never
// attached as a tool, so the only grant it has is the dispatch policy, and a
// second denial list with nothing to deny would be a control that reads as
// meaningful and does nothing.
func (t *chatTurn) machineDispatchAllowed(mode string, d MachineDef) bool {
	switch mode {
	case dispatchNone:
		return false
	case dispatchOnly:
		return dispatchListNames(t.agent.AllowedDispatchTargets, d.ID, d.Name)
	case dispatchExcept:
		return !dispatchListNames(t.agent.AllowedDispatchTargets, d.ID, d.Name)
	}
	return true // dispatchAll
}

// dispatchableMachines lists the machines this caller may dispatch to: policy
// permits it, and it RUNS rather than converses.
//
// The unattended filter is here rather than only at the gate because this list
// is what the tool description advertises, and advertising a target that always
// refuses teaches the model that dispatch is unreliable. A machine that
// converses still resolves at the gate — where it gets a refusal that says what
// to change — so nothing is hidden, only un-advertised.
func (t *chatTurn) dispatchableMachines() []MachineDef {
	mode := effectiveDispatchMode(t.agent)
	if mode == dispatchNone {
		return nil
	}
	all := t.machineUniverse()
	out := make([]MachineDef, 0, len(all))
	for _, d := range all {
		if d.Unattended && t.machineDispatchAllowed(mode, d) {
			out = append(out, d)
		}
	}
	return out
}

// dispatchableMachineNames is the reachable set as display names, for the tool
// description. Bounded, for the same reason the pipeline list is.
func (t *chatTurn) dispatchableMachineNames(max int) []string {
	defs := t.dispatchableMachines()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		if n := strings.TrimSpace(d.Name); n != "" {
			names = append(names, n)
		}
		if len(names) == max {
			break
		}
	}
	return names
}

// dispatchableMachine resolves a machine the caller may run, by name or id.
//
// Four different answers, because they need four different actions from the
// caller: it does not exist (stop, or pick from the list), it exists but the
// policy forbids it (stop, and tell the user what to change), it exists but
// converses (stop — this is not a target, ever), and here it is.
func (t *chatTurn) dispatchableMachine(ref string) (MachineDef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return MachineDef{}, errors.New("machine is required for action=run")
	}
	mode := effectiveDispatchMode(t.agent)
	if mode == dispatchNone {
		return MachineDef{}, fmt.Errorf("agents(run, machine=%q) refused — your dispatch policy is Allow NONE, which covers machines as well as agents and pipelines. Do the work with the tools you have", ref)
	}
	var names []string
	var denied, converses string
	for _, def := range t.machineUniverse() {
		// Matched by NAME as well as id: a name is what the model has, since
		// it reads machine names and never storage keys.
		match := strings.EqualFold(def.ID, ref) || strings.EqualFold(def.Name, ref)
		if !t.machineDispatchAllowed(mode, def) {
			if match {
				denied = def.Name
			}
			continue
		}
		if !def.Unattended {
			if match {
				converses = def.Name
			}
			continue
		}
		if match {
			return def, nil
		}
		if t.foreignMachine(def) {
			names = append(names, def.Name+" (shared by "+def.Owner+")")
			continue
		}
		names = append(names, def.Name)
	}
	switch {
	case denied != "":
		return MachineDef{}, fmt.Errorf("agents(run, machine=%q) refused — that machine exists but is not on this agent's dispatch target list. Ask the user to add it (Security & Access → Dispatch target list) or to change the dispatch policy; do not retry, and do not look for another route to the same work", denied)
	case converses != "":
		// The same fact the schedule reports, worded the same way. A machine
		// that converses is not a target that is temporarily unavailable — it
		// is a different kind of thing, and the caller should stop asking.
		return MachineDef{}, fmt.Errorf("agents(run, machine=%q) refused — that machine converses rather than runs: it has a step that waits for a person, and a dispatched run has nobody there. Ask the user to turn on \"this RUNS instead of converses\" on it, or do the work another way", converses)
	case len(names) == 0:
		return MachineDef{}, fmt.Errorf("no machine %q is available to you — the user has no machines that RUN, or none this agent's dispatch policy permits", ref)
	}
	return MachineDef{}, fmt.Errorf("no machine %q — you can run: %s", ref, strings.Join(names, ", "))
}

// machineDispatchGate resolves and refuses a machine dispatch: everything
// checkable before anything runs.
//
// Separate from the run itself because the detach policy needs exactly this and
// none of the rest — a refusal has to reach the model while it still has a
// round to fix it in.
func (t *chatTurn) machineDispatchGate(args map[string]any) (MachineDef, string, error) {
	if t.dispatchDepth >= maxDispatchDepth {
		return MachineDef{}, "", fmt.Errorf("agents(run): depth limit %d exceeded", maxDispatchDepth)
	}
	msg := strings.TrimSpace(stringArg(args, "message"))
	if msg == "" {
		return MachineDef{}, "", errors.New("message is required for action=run")
	}
	def, err := t.dispatchableMachine(stringArg(args, "machine"))
	if err != nil {
		return MachineDef{}, "", err
	}
	// A machine can be SAVED with problems — the editor's whole posture is that
	// a half-built machine is the normal state — so unlike a pipeline's
	// Validate() this is not a defensive check against an old record. It is the
	// common case, and it is the same list the Run button and the schedule
	// refuse on, so the three cannot disagree about whether a machine can run.
	if probs := def.Problems(); len(probs) > 0 {
		return MachineDef{}, "", fmt.Errorf("machine %q will not run yet — %s (%d outstanding). Its page lists them; do not retry until they are fixed",
			def.Name, probs[0], len(probs))
	}
	// Steps that hand off to something else only run inside a CONVERSATION
	// today. The delegate / pipeline / child-machine seam lives on chatTurn's
	// PhaseRunner (machine_delegate.go); a turn-free host runs steps through
	// AppCore.PhaseWorker, which knows nothing about those fields and would run
	// such a step as an ordinary prompt — the machine would produce an answer
	// that looks right and did none of the work its author arranged.
	//
	// So it is refused rather than degraded. A wrong answer with the right
	// shape is the single worst thing a dispatched procedure can return, and
	// the caller has no way to tell. (The schedule and the page's Run button
	// take the degraded path today; this is the new door, and it is not going
	// to add a third place that quietly does the wrong thing.)
	if steps := machineStepsNeedingAConversation(def); len(steps) > 0 {
		return MachineDef{}, "", fmt.Errorf("machine %q cannot be dispatched — its step(s) %s hand off to another agent, pipeline or machine, and that only happens in a conversation. Attach it to an agent and talk to it instead, or ask the user for a version whose steps do their own work",
			def.Name, strings.Join(steps, ", "))
	}
	// Transitive authority, the same fence the other two targets carry:
	// everything above judges the IMMEDIATE caller, which makes an allowlist a
	// one-hop gate. Authority must never GROW along a chain.
	if origin := t.dispatchOrigin; origin != nil && !origin.allowsMachine(def) {
		Log("[orchestrate.agents.run] blocked transitive machine dispatch %s → %s: not permitted by originator %s",
			t.agent.ID, def.ID, origin.AgentID)
		return MachineDef{}, "", fmt.Errorf("agents(run, machine=%q) refused — you are running on behalf of %q, whose dispatch policy does not permit that machine. A delegated agent cannot reach further than the agent that delegated to it. Do what you can with your own tools, or report back that %q was needed and not permitted", def.Name, origin.AgentName, def.Name)
	}
	// Cycle guard. A machine whose delegating step dispatches back into the
	// same machine is a loop no depth counter catches quickly: each hop resets
	// the per-turn depth, so it would iterate the cap's worth at every level.
	for _, prior := range dispatchedMachines(t.ctx) {
		if prior == def.ID {
			return MachineDef{}, "", fmt.Errorf("agents(run, machine=%q) refused — that machine is already running above this call; a step of it cannot re-enter it. Answer with what you have, or dispatch something else", def.Name)
		}
	}
	return def, msg, nil
}

// machineStepsNeedingAConversation names the steps a turn-free run cannot
// honour: the ones that delegate to an agent, run a pipeline, or run a child
// machine. Named rather than counted, because the answer to this refusal is to
// go and look at those steps.
func machineStepsNeedingAConversation(def MachineDef) []string {
	var out []string
	for _, p := range def.Phases {
		if strings.TrimSpace(p.Agent) != "" || strings.TrimSpace(p.Pipeline) != "" || strings.TrimSpace(p.Machine) != "" {
			out = append(out, strconv.Quote(p.Name))
		}
	}
	return out
}

// agentsRunMachineAction dispatches to a saved machine and returns the result
// of the step that finished it.
func (t *chatTurn) agentsRunMachineAction(args map[string]any) (string, error) {
	def, msg, err := t.machineDispatchGate(args)
	if err != nil {
		return "", err
	}
	if t.agentDispatchCounts == nil {
		t.agentDispatchCounts = map[string]int{}
	}
	if block := dispatchCapDecision(t.agentDispatchCounts, dispatchMachineCapKey(def.ID), def.Name, msg, isBuilderAgent(t.agent.ID)); block != "" {
		Log("[orchestrate.agents.run] per-turn dispatch cap hit: %s → machine %s — blocking further dispatch", t.agent.ID, def.ID)
		// An ERROR, never a normal result: a normal result rides through
		// fenceAgentsOutput, and a framework STOP verdict delivered inside a
		// fence that says to ignore embedded directions is a guard the model
		// is licensed to walk past.
		return "", errors.New(block)
	}
	// The warden, before anything is spent. A refused request must not leave a
	// run record, an activity pill, or a half-finished machine behind it.
	if err := t.guardMachineInput(t.ctx, def, msg); err != nil {
		return "", err
	}
	t.dispatchDepth++
	defer func() { t.dispatchDepth-- }()

	liveRun := t.app.runsRegistry().Create(t.user, "", "", nil).
		Describe("machine", machineRunLabel(t, def), truncateObs(msg, 100)).
		Parent(parentRunFromCtx(t.ctx))
	defer liveRun.Complete(RunStatusFailed) // safety net; the explicit calls below win

	ctx := withDispatchedMachine(t.ctx, def.ID)
	ctx = withParentRun(ctx, liveRun.ID)
	// The caller's rules travel INTO the run, so a step's tool calls are judged
	// the way the caller's own would be. A machine's steps reach them through
	// the same core.runWorkerStageConfirm a pipeline's worker stages do.
	ctx = t.guardedRunContext(ctx)

	// The framework's decisions about the run reach the person watching as
	// status, and the turn's own diagnostics as breadcrumbs. Only the inline
	// path does either: the detached one runs after the turn that made it has
	// ended, so its stream is closed and its diagnostics belong to nobody.
	note := func(kind, detail string) {
		t.emitStatus("[" + def.Name + "] " + detail)
		t.turnDiag(kind, detail)
		Log("[orchestrate.machine %q] %s", def.Name, detail)
	}

	Log("[orchestrate.agents.run] %s dispatching machine %q%s (%d steps)", t.agent.ID, def.Name, machineOwnerNote(t, def), len(def.Phases))
	out, err := t.runDispatchedMachine(ctx, def, msg, note)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return "", err
	}
	liveRun.Complete(RunStatusCompleted)
	out, err = t.guardMachineOutput(t.ctx, def, out)
	if err != nil {
		return "", err
	}
	return machineDispatchResult(def, out)
}

// runDetachedMachine is the handed-off variant: the caller said it was not
// waiting, so this runs after that turn has ended.
//
// Deliberately NOT the inline path with a different context, for the reasons
// runDetachedPipeline states: the turn's SSE stream is closed, so status
// emission has nowhere to go, and its per-turn counters would be mutated from a
// goroutine racing the turn that owns them.
func (t *chatTurn) runDetachedMachine(d *ToolSession, def MachineDef, msg string) (string, error) {
	// The detached session's context, NOT the turn's: t.ctx was cancelled when
	// the turn that handed this off ended, and a warden call on a dead context
	// fails the check instead of running it.
	ctx := d.Context()
	if err := t.guardMachineInput(ctx, def, msg); err != nil {
		return "", err
	}
	liveRun := t.app.runsRegistry().Create(t.user, "", "", nil).
		Describe("machine", machineRunLabel(t, def), truncateObs(msg, 100))
	defer liveRun.Complete(RunStatusFailed)

	ctx = withDispatchedMachine(ctx, def.ID)
	ctx = withParentRun(ctx, liveRun.ID)
	ctx = t.guardedRunContext(ctx) // as inline; see agentsRunMachineAction

	Log("[orchestrate.agents.run] %s handed off machine %q%s (%d steps)", t.agent.ID, def.Name, machineOwnerNote(t, def), len(def.Phases))
	// nil note: the live run above is what carries progress here, and the
	// turn's status channel is closed.
	out, err := t.runDispatchedMachine(ctx, def, msg, nil)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return "", err
	}
	liveRun.Complete(RunStatusCompleted)
	out, err = t.guardMachineOutput(ctx, def, out)
	if err != nil {
		return "", err
	}
	return machineDispatchResult(def, out)
}

// runDispatchedMachine walks an unattended machine to the step that hands off
// nowhere, and returns that step's result. Shared by the inline and detached
// paths so the two cannot come to reach different things.
//
// The REQUESTER's tool pool, not the owner's — the recipe travels, the
// authority does not, and a shared machine that ran against its owner's
// credentials would be exactly the thing peer-sharing promises it is not.
func (t *chatTurn) runDispatchedMachine(ctx context.Context, def MachineDef, msg string, note func(kind, detail string)) (string, error) {
	sess := &ToolSession{Username: t.user, DB: AuthDB()}
	catalog, err := GetAgentToolsWithSession(sess, availableWorkerToolNames()...)
	if err != nil {
		Log("[orchestrate.machines] dispatch of %q: tool catalog partly unresolved for %q: %v", def.Name, t.user, err)
	}
	cache := NewRunToolCache()
	runner := t.app.PhaseWorker(WrapToolsWithRunCache(cache, catalog))

	cur := &MachineCursor{}
	final, out, rerr := t.app.RunUnattended(ctx, def, cur, MachineTurn{
		Input: msg,
		User:  t.user,
		Now:   time.Now().In(UserLocation(t.user)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}, runner, func(kind, detail string) {
		if note != nil {
			note(kind, detail)
		}
	})
	if rerr != nil {
		// The partial result rides along in the message: a run that stopped at
		// step nine did nine steps of work, and a caller told only that
		// something went wrong will start again from the beginning.
		msg := fmt.Sprintf("machine %q stopped: %v", def.Name, rerr)
		if partial := strings.TrimSpace(out); partial != "" {
			msg += "\n\nWhat it had produced before it stopped:\n" + partial
		}
		return "", errors.New(msg)
	}
	Log("[orchestrate.machines] user=%q dispatched %q → finished at %s after %d step(s), %d bytes out, %d cached tool call(s)",
		t.user, def.Name, final.Name, len(cur.Log)+1, len(out), cache.Hits())
	return out, nil
}

// machineRunLabel names a run in the activity surface. A machine somebody else
// wrote carries their name, because "why is my agent running Outage Review" has
// a different answer depending on whose Outage Review it is, and the pill is
// where somebody looks first.
func machineRunLabel(t *chatTurn, def MachineDef) string {
	if t.foreignMachine(def) {
		return def.Name + " (shared by " + def.Owner + ")"
	}
	return def.Name
}

// machineOwnerNote is the same fact for the log line: empty for the ordinary
// case, so an owned dispatch logs exactly what it always did.
func machineOwnerNote(t *chatTurn, def MachineDef) string {
	if t.foreignMachine(def) {
		return " (shared by " + def.Owner + ")"
	}
	return ""
}

// machineDispatchResult is the shared ending for both paths.
func machineDispatchResult(def MachineDef, out string) (string, error) {
	if strings.TrimSpace(out) == "" {
		// An empty result is one the caller cannot act on and cannot
		// distinguish from a silent failure. Name it.
		return "", fmt.Errorf("machine %q ran to completion but produced no output — check the step that finishes it, the one that hands off nowhere", def.Name)
	}
	return out, nil
}
