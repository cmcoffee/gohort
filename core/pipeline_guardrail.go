// Guardrails INSIDE a pipeline run.
//
// The dispatch boundary is judged by the app (see the orchestrate app's
// pipeline guard): what goes in, and what comes back. That covers everything a
// pipeline SAYS. It covers nothing a pipeline DOES.
//
// A worker stage with tools runs a real agent loop, and that loop's pre_action
// gate is the check standing between a governed agent and a send, a post, a
// delete, or a spend. The interpreter built its AgentLoopConfig with no
// Guardrail fields at all, so an agent barred from mailing a customer could
// route the request through a pipeline whose worker stage holds send_message
// and the mail went out — and unlike a disclosure, no output check can take it
// back afterwards. That is the whole argument for this file: an output gate is
// a redaction, an action gate is the only one that prevents.
//
// Carried on the CONTEXT rather than threaded as a parameter, matching how this
// package already carries run identity (withParentRun, withDispatchedPipeline).
// A pipeline's work reaches down through stage lists, loop bodies, and fanout
// branches, each of which would otherwise need the same argument added to it,
// and a nested run started from any of them would silently lose it. The context
// reaches all of them and cannot be forgotten at one call site.
//
// It is deliberately DROPPED at an agent stage. That stage dispatches to an
// agent with a record of its own, and that agent's rules govern its work; the
// caller's set travelling into it would judge one agent's actions by another
// agent's rules, which is not what either owner authored.
package core

import "context"

// StageGuardrails is one agent's enforcement set, handed to a pipeline so its
// worker stages act under it. Mirrors the three AgentLoopConfig hooks plus the
// owner's decline voice; the zero value is inert and costs nothing.
//
// Whose rules these are is the caller's decision, not this package's — the app
// resolves them. For a dispatched pipeline they are the DISPATCHING agent's: a
// pipeline has no record and no rules of its own, and the alternative to
// borrowing the caller's is being ungoverned.
type StageGuardrails struct {
	Check    func(hookPoint, candidate string) GuardrailDecision
	Halted   func() bool
	Reject   func(reason, request string) string
	Declines []string
}

type stageGuardrailsKey struct{}

// WithStageGuardrails puts an enforcement set on the context for the pipeline
// run started with it. Passing an inert set is the same as passing none.
func WithStageGuardrails(ctx context.Context, g StageGuardrails) context.Context {
	if g.Check == nil {
		return ctx
	}
	return context.WithValue(ctx, stageGuardrailsKey{}, g)
}

// WithoutStageGuardrails strips the set, for the hand-off to an agent that
// carries rules of its own.
func WithoutStageGuardrails(ctx context.Context) context.Context {
	if ctx.Value(stageGuardrailsKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, stageGuardrailsKey{}, StageGuardrails{})
}

// stageGuardrails reads the set off the context. Zero value when absent.
func stageGuardrails(ctx context.Context) StageGuardrails {
	if g, ok := ctx.Value(stageGuardrailsKey{}).(StageGuardrails); ok {
		return g
	}
	return StageGuardrails{}
}

// stageCheck narrows the set to pre_action before it reaches a stage's agent
// loop. Every other hook point the loop offers answers "no objection".
//
// Narrowed HERE, in one place, rather than by asking each caller to hand over a
// pre-filtered set — a caller that forgot would silently buy a warden call per
// stage, and the silence is the problem: nothing would look wrong.
//
// pre_output is the one being turned down, and deliberately. A stage's text is
// an intermediate that no reader ever sees; it reaches a person only through
// the pipeline's final output, which the dispatch boundary already judges. So
// per-stage pre_output would buy one model call per stage to re-judge, in
// pieces, what is judged once as a whole. pre_action has no such backstop: the
// call either happens or it doesn't, and by the time there is an output to
// judge the mail has been sent.
func (g StageGuardrails) stageCheck() func(hookPoint, candidate string) GuardrailDecision {
	if g.Check == nil {
		return nil
	}
	return func(hookPoint, candidate string) GuardrailDecision {
		if hookPoint != GuardHookPreAction {
			return GuardrailDecision{}
		}
		return g.Check(hookPoint, candidate)
	}
}
