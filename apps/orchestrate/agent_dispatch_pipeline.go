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

// dispatchablePipeline resolves a pipeline the caller may run, by name or id.
//
// Reachability is the attachment set (effectivePipelineIDs), NOT every
// pipeline the owner has. That is the conservative reading of a surface which
// previously required attachment for any access at all: dispatch removes the
// need to arrange the attachment BEFORE the need is known, not the need for
// the owner to have granted the pipeline to this agent.
func (t *chatTurn) dispatchablePipeline(ref string) (PipelineDef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PipelineDef{}, errors.New("pipeline is required for action=run")
	}
	ids := t.effectivePipelineIDs()
	var names []string
	for _, id := range ids {
		def, ok := LoadPipelineDef(t.udb, t.user, id)
		if !ok {
			continue // the attachment outlived the def
		}
		if strings.EqualFold(def.ID, ref) || strings.EqualFold(def.Name, ref) {
			return def, nil
		}
		names = append(names, def.Name)
	}
	// Distinguish "not yours to run" from "does not exist". An agent told a
	// pipeline is missing will try to author a replacement; one told it is not
	// attached asks the person to attach it, which is the actionable ask.
	//
	// Matched by NAME as well as id, because a name is what the model has: it
	// reads pipeline names, not storage keys, so an id-only check reported
	// every unattached pipeline as missing — the exact wrong answer.
	for _, d := range ListPipelineDefs(t.udb, t.user) {
		if strings.EqualFold(d.ID, ref) || strings.EqualFold(d.Name, ref) {
			return PipelineDef{}, fmt.Errorf("pipeline %q exists but is not attached to you — ask the user to attach it on the pipeline's page (\"Who can call it\"), or mark it global", d.Name)
		}
	}
	if len(names) == 0 {
		return PipelineDef{}, fmt.Errorf("no pipeline %q — you have none attached", ref)
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
	t.dispatchDepth++
	defer func() { t.dispatchDepth-- }()

	// Live activity, same as the agent path and the schedule path: without it
	// a multi-minute run is invisible until it finishes.
	liveRun := t.app.runsRegistry().Create(t.user, "", "", nil).
		Describe("pipeline", def.Name, truncateObs(msg, 100)).
		Parent(parentRunFromCtx(t.ctx))
	defer liveRun.Complete(RunStatusFailed) // safety net; the explicit calls below win

	ctx := withDispatchedPipeline(t.ctx, def.ID)
	ctx = withParentRun(ctx, liveRun.ID)

	status := func(s string) {
		t.emitStatus("[" + def.Name + "] " + s)
		Log("[orchestrate.pipeline %q] %s", def.Name, s)
	}

	Log("[orchestrate.agents.run] %s dispatching pipeline %q (%d stages)", t.agent.ID, def.Name, len(def.Stages))
	out, err := t.app.RunPipelineDefSync(ctx, def, msg, t.pipelineStageDispatch(), status)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return "", fmt.Errorf("pipeline %q failed: %w", def.Name, err)
	}
	liveRun.Complete(RunStatusCompleted)
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
	liveRun := t.app.runsRegistry().Create(t.user, "", "", nil).
		Describe("pipeline", def.Name, truncateObs(msg, 100))
	defer liveRun.Complete(RunStatusFailed)

	ctx := withDispatchedPipeline(d.Context(), def.ID)
	ctx = withParentRun(ctx, liveRun.ID)

	Log("[orchestrate.agents.run] %s handed off pipeline %q (%d stages)", t.agent.ID, def.Name, len(def.Stages))
	// nil status: the live run above is what carries progress here, and the
	// turn's status channel is closed.
	out, err := t.app.RunPipelineDefSync(ctx, def, msg, t.pipelineStageDispatch(), nil)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return "", fmt.Errorf("pipeline %q failed: %w", def.Name, err)
	}
	liveRun.Complete(RunStatusCompleted)
	return pipelineDispatchResult(def, out)
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
