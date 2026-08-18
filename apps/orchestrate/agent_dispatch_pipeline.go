// Dispatching a PIPELINE, beside dispatching an agent.
//
// `agents(action=run)` has always taken one kind of target: an agent, resolved
// by name, run as a conversation. A pipeline was reachable only if it had been
// ATTACHED to the caller in advance, as a run_<name> tool. That is a fine way
// to give an agent a workflow it uses constantly, and a poor way to reach one
// it needs once — the attachment has to be arranged before the need is known.
//
// So `pipeline=` sits beside `agent=`, and exactly one of them is named. The
// shape is StandingAgent's (core/standing_agent.go): AgentID is "a
// conversation lives here", PipelineID is "a multi-stage RUN lives here", and
// naming both is REFUSED rather than resolved by precedence, because whichever
// the handler happened to check first would be the one that ran forever and
// which one depends on the order of an if.
//
// Posture is the SCHEDULE's, not the attached tool's: the pipeline runs as the
// owner, with no inherited tool catalog, so a worker stage reaches exactly what
// it declares. The attached run_<name> tool inherits the caller's resolved
// catalog on the argument that tools flow down to a workflow you deliberately
// bolted on. Dispatch has no such prior act — its whole purpose is reaching
// something you did NOT attach — so there is no baseline for a caller to reason
// from, and inheriting would mean the same pipeline reaches further when
// dispatched than when run from its own page.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxAdvertisedPipelines caps how many pipeline names the agents tool lists in
// its own description. The list is prompt weight on every turn of every agent;
// past a handful it stops being a menu and becomes noise, and the model can
// still name any of them — the description is a hint, not the allowlist.
const maxAdvertisedPipelines = 12

// validateDispatchTarget checks that exactly one target is named.
//
// Ported from StandingAgent.ValidateTarget deliberately, reasoning included:
// this is the same question about the same pair of targets, and answering it
// differently in two places is how the two surfaces drift.
func validateDispatchTarget(agent, pipeline string) error {
	hasAgent := strings.TrimSpace(agent) != ""
	hasPipeline := strings.TrimSpace(pipeline) != ""
	switch {
	case hasAgent && hasPipeline:
		return errors.New("agents(run) takes either agent= or pipeline=, not both — " +
			"drop one, because whichever this checked first would be the one that ran")
	case !hasAgent && !hasPipeline:
		return errors.New("agents(run) needs something to run: agent= for a conversation with a fleet agent, or pipeline= for a saved multi-stage workflow")
	}
	return nil
}

// dispatchedPipelinesKey carries the pipelines already running above this
// point. It rides the CONTEXT rather than chatTurn.dispatchChain because a
// pipeline's agent stages are dispatched through RunAgentSync, which starts
// each sub-turn with an empty chain — so a turn-local list would not survive
// the one hop that matters. The context does survive it: a sub-turn is built
// with the ctx it was called on (agent_dispatch.go), so a stage agent that
// dispatches back into the pipeline that spawned it sees the entry.
type dispatchedPipelinesKey struct{}

// withDispatchedPipeline records a pipeline as in-flight for everything run
// beneath it.
func withDispatchedPipeline(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	prior := dispatchedPipelines(ctx)
	next := make([]string, len(prior), len(prior)+1)
	copy(next, prior)
	return context.WithValue(ctx, dispatchedPipelinesKey{}, append(next, id))
}

// dispatchedPipelines returns the pipeline ids already in flight above ctx.
func dispatchedPipelines(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	ids, _ := ctx.Value(dispatchedPipelinesKey{}).([]string)
	return ids
}

// dispatchPipelineCapKey namespaces a pipeline in the per-turn dispatch
// counters. Those are keyed by agent id, and a pipeline id colliding with an
// agent id would spend one budget on two different targets.
func dispatchPipelineCapKey(id string) string { return "pipeline:" + id }

// dispatchablePipelines lists the pipelines this caller may dispatch to:
// every pipeline the OWNER has, not just the ones attached to this agent.
//
// Attachment stays what it always was — the decision to put a workflow in an
// agent's catalog as a run_<name> tool, advertised in its prompt, reached for
// by habit. Dispatch answers the other question: this agent needs a workflow
// once, and arranging an attachment first means knowing the need before it
// arises. Gating dispatch on attachment made the two the same question and
// left dispatch adding nothing an attached tool did not already do.
//
// The one absolute is Allow none. An agent whose dispatch policy is off does
// not acquire a second delegation channel because the target is a pipeline —
// that setting means this agent does not hand work to anything, and a rule
// with an exception in it is not the rule the operator selected.
//
// The allowlist modes govern this too. They deliberately did not, on the
// argument that AllowedDispatchTargets holds AGENT ids curated through an agent
// picker, so reading it as a pipeline restriction would invent a policy nobody
// set. What that argument missed is the shape of the hole it leaves: an agent
// restricted to two named targets still reached every pipeline its owner had,
// and a pipeline is a delegation channel with a tool catalog of its own. "Only
// these two" that actually means "only these two, plus anything that is not an
// agent" is not the policy the operator selected either.
//
// So the list is a list of TARGETS, not a list of agents: the picker offers
// pipelines beside agents (handleAgents, role=dispatch-target), and a pipeline
// answers to its id or its name. This IS a behaviour change for an existing
// agent in Only mode — its list names no pipelines, so it reaches none until
// the owner ticks one. The refusal says exactly that, because a tightening
// nobody can act on is a tightening that gets switched off.
//
// DisabledPipelines still bites on top of all of it: an absent grant and an
// expressed denial are different statements, and Except mode is where the
// second one belongs.
//
// SHARED pipelines are in the set too (v0.6.267). They were held out when
// peer-sharing landed, on the argument that an agent choosing by itself to run
// another user's recipe is a different question from a person clicking Run.
// What settles it is that the difference is one of degree and the guards do not
// care which: the run happens in the REQUESTER's namespace either way — their
// agents answer its agent stages, their catalog resolves its tools, their
// credentials back them, and their warden judges its actions (v0.6.263) — and
// the dispatch policy applies to a shared pipeline exactly as it does to an
// owned one. A recipe somebody else wrote is untrusted text that this user
// already accepted; leaving it reachable by hand and not by agent protected
// nothing, it just meant the useful half needed a human in the loop.
//
// It IS worth knowing that under the default Allow-all policy this makes every
// pipeline shared with a user reachable by all of their agents. Except mode
// denies one; Only mode requires it ticked.
func (t *chatTurn) dispatchablePipelines() []PipelineDef {
	mode := effectiveDispatchMode(t.agent)
	if mode == dispatchNone {
		return nil
	}
	all := t.pipelineUniverse()
	out := make([]PipelineDef, 0, len(all))
	for _, d := range all {
		if t.pipelineDispatchAllowed(mode, d) {
			out = append(out, d)
		}
	}
	return out
}

// pipelineUniverse is every pipeline this user can see, before policy: their
// own, then the ones shared with them. Policy is applied by the callers,
// separately, because one of them has to tell a target this agent may not reach
// apart from a name that does not exist — and a pre-filtered list cannot.
func (t *chatTurn) pipelineUniverse() []PipelineDef {
	own := ListPipelineDefs(t.udb, t.user)
	out := make([]PipelineDef, 0, len(own))
	names := make(map[string]bool, len(own))
	for _, d := range own {
		names[strings.ToLower(strings.TrimSpace(d.Name))] = true
		out = append(out, d)
	}
	// Own first, so a name that exists in both resolves to the user's own — the
	// same shadowing rule resolvePipelineFor applies on the page. A shared one
	// whose name collides is dropped rather than listed: the model has names and
	// not ids, so listing it would advertise a target that can never be reached
	// by the only handle the model has. Logged, because "my colleague shared it
	// and my agent says there is no such pipeline" is otherwise unanswerable.
	for _, sp := range sharedPipelinesFor(t.user) {
		if names[strings.ToLower(strings.TrimSpace(sp.Def.Name))] {
			Log("[orchestrate.pipelines] %q shared by %q is not dispatchable for %q: the name collides with one of their own",
				sp.Def.Name, sp.Owner, t.user)
			continue
		}
		out = append(out, sp.Def)
	}
	return out
}

// foreignPipeline reports whether a dispatch target belongs to somebody else.
// Owner is stamped on every stored record, so this needs no second lookup —
// and every place that treats a shared pipeline differently asks it here.
func (t *chatTurn) foreignPipeline(d PipelineDef) bool {
	owner := strings.TrimSpace(d.Owner)
	return owner != "" && owner != t.user
}

// pipelineDispatchAllowed applies this agent's dispatch policy to one pipeline.
// The single place the question is answered, so the advertised list and the
// gate cannot disagree — a name the tool description offers and the gate then
// refuses is worse than never offering it.
func (t *chatTurn) pipelineDispatchAllowed(mode string, d PipelineDef) bool {
	if containsString(t.agent.DisabledPipelines, d.ID) {
		return false
	}
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

// dispatchablePipelineNames is the reachable set as display names, for the
// tool description. Bounded: a name list is prompt weight on every turn, and
// past the first several it stops being a menu and becomes noise.
func (t *chatTurn) dispatchablePipelineNames(max int) []string {
	defs := t.dispatchablePipelines()
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

// dispatchablePipeline resolves a pipeline the caller may run, by name or id.
func (t *chatTurn) dispatchablePipeline(ref string) (PipelineDef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PipelineDef{}, errors.New("pipeline is required for action=run")
	}
	if effectiveDispatchMode(t.agent) == dispatchNone {
		return PipelineDef{}, fmt.Errorf("agents(run, pipeline=%q) refused — your dispatch policy is Allow NONE, which covers pipelines as well as agents. Do the work with the tools you have", ref)
	}
	mode := effectiveDispatchMode(t.agent)
	var names []string
	var denied string
	for _, def := range t.pipelineUniverse() {
		// Matched by NAME as well as id: a name is what the model has, since
		// it reads pipeline names and never storage keys.
		match := strings.EqualFold(def.ID, ref) || strings.EqualFold(def.Name, ref)
		if !t.pipelineDispatchAllowed(mode, def) {
			// Named and refused is a DIFFERENT answer from never existed, and
			// conflating them taught the model to go looking for a spelling
			// that works. It is told the policy instead, and told to stop.
			if match {
				denied = def.Name
			}
			continue
		}
		if match {
			return def, nil
		}
		if t.foreignPipeline(def) {
			names = append(names, def.Name+" (shared by "+def.Owner+")")
			continue
		}
		names = append(names, def.Name)
	}
	if denied != "" {
		return PipelineDef{}, fmt.Errorf("agents(run, pipeline=%q) refused — that pipeline exists but is not on this agent's dispatch target list. Ask the user to add it (Security & Access → Dispatch target list) or to change the dispatch policy; do not retry, and do not look for another route to the same work", denied)
	}
	if len(names) == 0 {
		return PipelineDef{}, fmt.Errorf("no pipeline %q is available to you — the user has no saved pipelines, or none this agent's dispatch policy permits", ref)
	}
	return PipelineDef{}, fmt.Errorf("no pipeline %q — you can run: %s", ref, strings.Join(names, ", "))
}

// pipelineDispatchGate resolves and refuses a pipeline dispatch: everything
// checkable before anything runs.
//
// Separate from the run itself because the detach policy needs exactly this
// and none of the rest — a refusal has to reach the model while it still has a
// round to fix it in. Detached, "that pipeline is not attached to you" arrives
// as a wake with no turn left to correct it, and the agent invents a reason.
func (t *chatTurn) pipelineDispatchGate(args map[string]any) (PipelineDef, string, error) {
	if t.dispatchDepth >= maxDispatchDepth {
		return PipelineDef{}, "", fmt.Errorf("agents(run): depth limit %d exceeded", maxDispatchDepth)
	}
	msg := strings.TrimSpace(stringArg(args, "message"))
	if msg == "" {
		return PipelineDef{}, "", errors.New("message is required for action=run")
	}
	def, err := t.dispatchablePipeline(stringArg(args, "pipeline"))
	if err != nil {
		return PipelineDef{}, "", err
	}
	// A pipeline refuses to be STORED unrunnable, but a record written by an
	// older build or restored from a bundle can still be invalid. Saying so
	// beats firing it and reporting whatever the first stage made of a broken
	// reference.
	if verr := def.Validate(); verr != nil {
		return PipelineDef{}, "", fmt.Errorf("pipeline %q would not run — %v", def.Name, verr)
	}
	// Transitive authority, the same fence the agent path carries: everything
	// above judges the IMMEDIATE caller, which makes an allowlist a one-hop
	// gate — A(allow:[B]) → B → any pipeline would reach what A could not.
	// Authority must never GROW along a chain. There is no ownership exemption
	// here, because a pipeline has no owning agent to be internal to.
	if origin := t.dispatchOrigin; origin != nil && !origin.allowsPipeline(def) {
		Log("[orchestrate.agents.run] blocked transitive pipeline dispatch %s → %s: not permitted by originator %s",
			t.agent.ID, def.ID, origin.AgentID)
		return PipelineDef{}, "", fmt.Errorf("agents(run, pipeline=%q) refused — you are running on behalf of %q, whose dispatch policy does not permit that pipeline. A delegated agent cannot reach further than the agent that delegated to it. Do what you can with your own tools, or report back that %q was needed and not permitted", def.Name, origin.AgentName, def.Name)
	}
	// Cycle guard. A pipeline whose agent stage dispatches back into the same
	// pipeline is a loop no depth counter catches quickly: each hop resets the
	// per-turn depth, so it would iterate the cap's worth at every level.
	for _, prior := range dispatchedPipelines(t.ctx) {
		if prior == def.ID {
			return PipelineDef{}, "", fmt.Errorf("agents(run, pipeline=%q) refused — that pipeline is already running above this call; a stage of it cannot re-enter it. Answer with what you have, or dispatch something else", def.Name)
		}
	}
	return def, msg, nil
}

// pipelineStageDispatch is the agent-stage runner for a dispatched pipeline.
// Carries the caller's chain so stages reach the channels their dispatcher
// reaches — the documented meaning of RunAgentSync's via. A schedule omits it
// because it has no dispatching parent; this has one.
//
// Built from VALUES off the turn (ids, not streams), so the detached path can
// use the same closure after the turn that made it has ended.
func (t *chatTurn) pipelineStageDispatch() func(context.Context, string, string) (string, error) {
	via := append(append([]string(nil), t.dispatchChain...), t.agent.ID)
	user := t.user
	app := t.app
	return func(c context.Context, agentID, stageInput string) (string, error) {
		return app.RunAgentSync(c, user, user, agentID, stageInput, via...)
	}
}

// agentsRunPipelineAction dispatches to a saved pipeline and returns its final
// output.
func (t *chatTurn) agentsRunPipelineAction(args map[string]any) (string, error) {
	def, msg, err := t.pipelineDispatchGate(args)
	if err != nil {
		return "", err
	}
	if t.agentDispatchCounts == nil {
		t.agentDispatchCounts = map[string]int{}
	}
	if block := dispatchCapDecision(t.agentDispatchCounts, dispatchPipelineCapKey(def.ID), def.Name, msg, isBuilderAgent(t.agent.ID)); block != "" {
		Log("[orchestrate.agents.run] per-turn dispatch cap hit: %s → pipeline %s — blocking further dispatch", t.agent.ID, def.ID)
		// An ERROR, never a normal result: a normal result rides through
		// fenceAgentsOutput, and a framework STOP verdict delivered inside a
		// fence that says to ignore embedded directions is a guard the model
		// is licensed to walk past.
		return "", errors.New(block)
	}
	// The warden, before anything is spent. A refused request must not leave a
	// run record, an activity pill, or a half-finished pipeline behind it.
	if err := t.guardPipelineInput(t.ctx, def, msg); err != nil {
		return "", err
	}
	t.dispatchDepth++
	defer func() { t.dispatchDepth-- }()

	// Live activity, same as the agent path and the schedule path: without it
	// a multi-minute run is invisible until it finishes.
	liveRun := t.app.runsRegistry().Create(t.user, "", "", nil).
		Describe("pipeline", pipelineRunLabel(t, def), truncateObs(msg, 100)).
		Parent(parentRunFromCtx(t.ctx))
	defer liveRun.Complete(RunStatusFailed) // safety net; the explicit calls below win

	ctx := withDispatchedPipeline(t.ctx, def.ID)
	ctx = withParentRun(ctx, liveRun.ID)
	// The caller's rules travel INTO the run, so a worker stage's tool calls
	// are judged the way the caller's own would be. Withheld output is a
	// redaction; a blocked action is the only guard that prevents.
	ctx = t.guardedPipelineContext(ctx)

	status := func(s string) {
		t.emitStatus("[" + def.Name + "] " + s)
		Log("[orchestrate.pipeline %q] %s", def.Name, s)
	}

	Log("[orchestrate.agents.run] %s dispatching pipeline %q%s (%d stages)", t.agent.ID, def.Name, pipelineOwnerNote(t, def), len(def.Stages))
	out, err := t.app.RunPipelineDefSync(ctx, def, msg, t.pipelineStageDispatch(), status)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return "", fmt.Errorf("pipeline %q failed: %w", def.Name, err)
	}
	liveRun.Complete(RunStatusCompleted)
	out, err = t.guardPipelineOutput(t.ctx, def, out)
	if err != nil {
		return "", err
	}
	return pipelineDispatchResult(def, out)
}

// runDetachedPipeline is the handed-off variant: the caller said it was not
// waiting, so this runs after that turn has ended.
//
// Deliberately NOT the inline path with a different context. Everything the
// inline path touches belongs to a turn that is over — its SSE stream is
// closed, so status emission has nowhere to go, and its per-turn dispatch
// counters would be mutated from a goroutine racing the turn that owns them.
// This mirrors the agent path's split, and the turn-free posture the schedule
// runner already uses.
func (t *chatTurn) runDetachedPipeline(d *ToolSession, def PipelineDef, msg string) (string, error) {
	// The detached session's context, NOT the turn's: t.ctx was cancelled when
	// the turn that handed this off ended, and a warden call on a dead context
	// fails the check instead of running it. Everything here that touches a
	// model gets d.Context() for the same reason.
	ctx := d.Context()
	if err := t.guardPipelineInput(ctx, def, msg); err != nil {
		return "", err
	}
	liveRun := t.app.runsRegistry().Create(t.user, "", "", nil).
		Describe("pipeline", pipelineRunLabel(t, def), truncateObs(msg, 100))
	defer liveRun.Complete(RunStatusFailed)

	ctx = withDispatchedPipeline(ctx, def.ID)
	ctx = withParentRun(ctx, liveRun.ID)
	ctx = t.guardedPipelineContext(ctx) // as inline; see agentsRunPipelineAction

	Log("[orchestrate.agents.run] %s handed off pipeline %q%s (%d stages)", t.agent.ID, def.Name, pipelineOwnerNote(t, def), len(def.Stages))
	// nil status: the live run above is what carries progress here, and the
	// turn's status channel is closed.
	out, err := t.app.RunPipelineDefSync(ctx, def, msg, t.pipelineStageDispatch(), nil)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return "", fmt.Errorf("pipeline %q failed: %w", def.Name, err)
	}
	liveRun.Complete(RunStatusCompleted)
	out, err = t.guardPipelineOutput(ctx, def, out)
	if err != nil {
		return "", err
	}
	return pipelineDispatchResult(def, out)
}

// pipelineRunLabel names a run in the activity surface. A pipeline somebody
// else wrote carries their name, because "why is my agent running Payroll
// Report" has a different answer depending on whose Payroll Report it is, and
// the pill is where somebody looks first.
func pipelineRunLabel(t *chatTurn, def PipelineDef) string {
	if t.foreignPipeline(def) {
		return def.Name + " (shared by " + def.Owner + ")"
	}
	return def.Name
}

// pipelineOwnerNote is the same fact for the log line: empty for the ordinary
// case, so an owned dispatch logs exactly what it always did.
func pipelineOwnerNote(t *chatTurn, def PipelineDef) string {
	if t.foreignPipeline(def) {
		return " (shared by " + def.Owner + ")"
	}
	return ""
}

// pipelineDispatchResult is the shared ending for both paths.
func pipelineDispatchResult(def PipelineDef, out string) (string, error) {
	if strings.TrimSpace(out) == "" {
		// An empty synthesis is a result the caller cannot act on and cannot
		// distinguish from a silent failure. Name it.
		return "", fmt.Errorf("pipeline %q ran to completion but produced no output — check its final stage", def.Name)
	}
	return out, nil
}
