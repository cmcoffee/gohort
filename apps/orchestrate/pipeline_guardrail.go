// The warden at a PIPELINE boundary.
//
// Guardrails belong to an AGENT: the rules live on AgentRecord, the enabled
// hooks are resolved from it, and every enforcement point in the agent loop
// reads them off the turn. A pipeline has no rules of its own — it is a recipe,
// not an identity — and for most of its life that asymmetry costs nothing. The
// owner pressing Run on the pipeline page is governing themselves, and a
// scheduled fire runs as the owner with no agent anywhere in the picture.
//
// Dispatch is the exception, and it is the one that matters. `agents(run,
// pipeline=…)` lets a GOVERNED agent hand a request to a multi-stage run and
// take the result back into its own context, and nothing in the interpreter
// touches a guardrail — core.runWorkerStage builds an AgentLoopConfig with no
// Guardrail* fields at all. Without this file, an agent whose warden forbids a
// topic can reach that topic by routing the request through a pipeline and
// reading the answer back: an agent-shaped surface that skips governance, which
// is precisely what the hooks exist to prevent. What binds an agent has to bind
// what it delegates.
//
// WHOSE rules. The CALLER's. A pipeline has no persona to be judged as, and the
// caller is who reads the output, so both ends are judged with the rules of the
// agent standing on either side of the boundary. This is the same answer the
// legacy pipeline-mode tool already gives for its stages (pipeline_tools.go:
// "a stage runs under the CALLING agent's rules"), for the same reason. Where a
// distinct agent with its own record does the work — an `agent` stage, which
// dispatches through RunAgentSync — that agent's rules apply instead, and this
// boundary is not the place that decides it.
//
// WHICH hooks. pre_input on the message going in, pre_output on the synthesis
// coming back. Not per stage: a stage's text is an intermediate nobody reads,
// judging each one multiplies a model call by the stage count, and the only
// thing that escapes into the caller's context is the final output — which is
// exactly what pre_output reads. (pre_action inside a worker stage's own tool
// loop is a real and still-open hole, and a separate change: the enforcement
// set has to reach core's interpreter to close it.)
//
// What a block does NOT do here is re-prompt. The loop's pre_output offers a
// correctable rule one rewrite, because a model can be asked to say the same
// thing differently. A pipeline is fixed data: the only rewrite available is
// running every stage again, at full cost, against prompts that cannot have
// changed. So a blocked pipeline output is withheld, never revised.
package orchestrate

import (
	"context"
	"errors"
	"strconv"

	. "github.com/cmcoffee/gohort/core"
)

// stageGuardrails is the enforcement set to hand DOWN into a pipeline run, so
// its worker and tool stages act under the same rules the caller does. The
// boundary guards below judge what a pipeline says; this is what stops it
// acting — see core's StageGuardrails for why the two are separate questions
// and why only pre_action reaches a stage.
func (t *chatTurn) stageGuardrails(ctx context.Context) StageGuardrails {
	e := t.guardrailEnforcerCtx(ctx)
	if e.Check == nil {
		return StageGuardrails{} // inert; core keeps its no-guardrails path
	}
	return StageGuardrails{
		Check:    e.Check,
		Halted:   e.Halted,
		Reject:   e.Reject,
		Declines: t.agent.GuardrailDeclines,
	}
}

// guardedPipelineContext is the one call a site needs to make: it puts the
// caller's rules on the context a pipeline run will use.
func (t *chatTurn) guardedPipelineContext(ctx context.Context) context.Context {
	return WithStageGuardrails(ctx, t.stageGuardrails(ctx))
}

// guardPipelineInput judges the message about to be handed to a pipeline.
// A non-nil return is the refusal; the pipeline must not run.
//
// An ERROR rather than a returned string, for the reason the per-turn dispatch
// cap in agent_dispatch_pipeline.go already states: a normal result rides
// through fenceAgentsOutput, and a framework refusal delivered inside a fence
// that tells the model to ignore embedded directions is a guard the model is
// licensed to walk past. The agent path can afford to deliver its decline as a
// reply because there it IS a reply — written in the target agent's voice. A
// pipeline has no voice; this refusal is the caller's own warden declining to
// hand the request over, which is framework authority and belongs on the error
// channel.
//
// The gate lives at each RUN site rather than in pipelineDispatchGate, which
// resolves the target: the gate is called twice for a handed-off dispatch (once
// in Preflight, once in Detached), and the warden is a model call that must not
// be paid for twice to answer one question.
func (t *chatTurn) guardPipelineInput(ctx context.Context, def PipelineDef, msg string) error {
	e := t.guardrailEnforcerCtx(ctx)
	if e.Check == nil {
		return nil // no rules, or no hooks enabled — inert, and free
	}
	if dec := e.Check(guardHookPreInput, msg); dec.Blocked {
		// e.Check has already filed the block and written its own breadcrumb;
		// this one adds WHERE, which is the part a reader needs to understand
		// why a pipeline they expected to see run never appears in the trail.
		t.turnDiag("guardrail-input-blocked", "A guardrail refused to hand this request to pipeline "+strconv.Quote(def.Name)+"; it was not run.")
		Log("[orchestrate.pipeline.guardrail] agent=%s pre_input BLOCKED dispatch of pipeline %q", t.agent.ID, def.Name)
		// Names no rule, quotes no reason, and names no mechanism. This text
		// goes back to the model that asked, and every block message in this
		// package is held to the same line: telling an agent it is inside a
		// checking system invites it to reason about the system, which is both
		// slow and the last thing that should surface in a reply.
		return errors.New("agents(run, pipeline=" + strconv.Quote(def.Name) +
			") did not run — a constraint on you covers this request, and handing it to a pipeline does not put it outside that constraint. " +
			"Do not route it through another target. Answer within it, or say plainly that you can't.")
	}
	return nil
}

// guardPipelineOutput judges the synthesis before it re-enters the caller's
// context. Returns the output to use, or an error when it must be withheld.
//
// This is the half that is a real guarantee. An input check can be talked
// around by asking innocuously; the output check reads what the pipeline
// actually produced, where "contains the protected thing" is unambiguous no
// matter how it was asked for. The withheld text is never returned, never
// summarised, and never described — a caller told what it nearly received has
// received it.
func (t *chatTurn) guardPipelineOutput(ctx context.Context, def PipelineDef, out string) (string, error) {
	e := t.guardrailEnforcerCtx(ctx)
	if e.Check == nil || out == "" {
		return out, nil
	}
	if dec := e.Check(guardHookPreOutput, out); dec.Blocked {
		t.turnDiag("guardrail-output-withheld", "Pipeline "+strconv.Quote(def.Name)+" ran to completion, but a guardrail withheld its output — the pipeline could not be asked to revise it, so nothing was delivered.")
		Log("[orchestrate.pipeline.guardrail] agent=%s pre_output WITHHELD %d bytes from pipeline %q", t.agent.ID, len(out), def.Name)
		return "", errors.New("pipeline " + strconv.Quote(def.Name) +
			" finished, but its output cannot be given to you — a constraint on you covers what it produced. " +
			"You have not seen it. Do not run it again to try, and do not describe or guess at what it said.")
	}
	return out, nil
}
