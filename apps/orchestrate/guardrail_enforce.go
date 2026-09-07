package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// guardrailCheckHook builds the AgentLoopConfig.GuardrailCheck for this turn,
// or nil when the agent has no active guardrail hooks (so the loop pays zero
// overhead). The returned closure holds a per-turn block counter: after
// guardBlockEscalateAt blocks it halts the turn and notifies the owner,
// because a context that keeps rephrasing to slip past the guard is no longer
// a drifting agent to be corrected but a compromised one to be stopped.
// guardrailEnforcement is the set of hooks an agent loop needs to enforce this
// agent's guardrails: the check itself, whether the turn must now END, and who
// writes the reply when it does. The zero value is inert — every field nil, so
// core takes its no-guardrails fast path and pays nothing.
//
// They travel together because they share the block counter. The escalation
// threshold is a property of the whole turn, not of one check, and the halt
// decision is meaningless without it.
type guardrailEnforcement struct {
	Check  func(hookPoint, candidate string) GuardrailDecision
	Halted func() bool
	Reject func(reason, request string) string
	// ActionGate widens WHICH calls reach Check at pre_action, and only while
	// this turn is tainted. It travels with the rest because it is meaningless
	// without them: a gate with no check behind it judges nothing.
	ActionGate func(toolName string) bool
}

// guardrailEnforcer returns the enforcement set for this turn. Inert (zero
// value) when the agent has no rules, so core takes its no-guardrails path.
//
// Built once and cached: a config literal names all three fields, and rebuilding
// per field would allocate three closures to do one job. (They would still be
// correct — the block count lives on the turn, not in the closure — but the
// read is worse and the waste is real.)
func (t *chatTurn) guardrailEnforcer() guardrailEnforcement {
	if t.guardrails != nil {
		return *t.guardrails
	}
	e := t.guardrailEnforcerCtx(t.ctx)
	t.guardrails = &e
	return e
}

// guardrailEnforcerCtx is guardrailEnforcer bound to an explicit context rather
// than the turn's, and deliberately NOT cached — the cache belongs to the turn,
// and a set built against another context is not the turn's set.
//
// It exists for work that outlives the turn which authorized it. A handed-off
// dispatch runs on the detached task's context, and by then t.ctx is cancelled:
// a warden call made on it does not return a lenient verdict, it returns an
// error, and an error is handled as "the check could not run". A guard that
// reports itself unable to run for structural reasons is not a guard, so the
// context has to be the live one.
func (t *chatTurn) guardrailEnforcerCtx(ctx context.Context) guardrailEnforcement {
	check := t.guardrailCheckHookCtx(ctx)
	if check == nil {
		return guardrailEnforcement{} // inert — core takes its no-guardrails path
	}
	return guardrailEnforcement{
		Check:      check,
		ActionGate: t.guardrailActionGate(),
		Halted:     func() bool { return t.guardrailBlocks >= guardBlockEscalateAt },
		// Bound to the same context as the check, for the same reason: the
		// rejection writer is itself a model call, and one made on a dead
		// context falls through to the canned decline every time.
		Reject: func(reason, request string) string { return t.guardrailRejectionCtx(ctx, reason, request) },
	}
}

func (t *chatTurn) guardrailCheckHook() func(hookPoint, candidate string) GuardrailDecision {
	return t.guardrailCheckHookCtx(t.ctx)
}

func (t *chatTurn) guardrailCheckHookCtx(ctx context.Context) func(hookPoint, candidate string) GuardrailDecision {
	rulesActive := resolveGuardrailHooks(t.agent) != nil
	// Two checks share this one hook, because the loop has one interception
	// point and they fire at the same moment. They are NOT the same check:
	//
	//   rules  — does this action break something the owner wrote (the warden)
	//   taint  — is this action serving text that just tried to steer the agent
	//
	// The second needs no authored rule, which is exactly why it cannot be
	// folded into the first: the agent most exposed to a feed is usually the
	// one with no rules at all, and for that agent resolveGuardrailHooks
	// returns nil and the hook used to be inert.
	tightens := scanTightens(t.agent)
	if !rulesActive && !tightens {
		return nil // no rules, no scanning → inert, and core pays nothing
	}
	// Resolved once per turn, not per check: the requester cannot change
	// mid-turn, and the checks fire on every governed tool call.
	who := t.requester()
	// pass blocks nothing — the zero GuardrailDecision. Named so the many
	// early returns below read as a decision rather than a bare pair.
	pass := GuardrailDecision{}
	return func(hookPoint, candidate string) GuardrailDecision {
		// The tainted-action check runs FIRST and independently of the hook
		// selection. An owner who turned pre_action off chose not to have their
		// RULES judged there; they did not choose to let a steered agent act
		// unchecked, because at the time they chose it there was nothing to
		// steer it. It also runs for an agent with no rules at all.
		if tightens && hookPoint == guardHookPreAction && t.turnTainted() {
			if dec := t.checkTaintedAction(ctx, candidate); dec.Blocked {
				t.guardrailBlocks++ // shares the turn's escalation counter
				return dec
			}
		}
		if !rulesActive || !guardrailHookActive(t.agent, hookPoint) {
			return pass
		}
		verdicts, err := t.app.runWarden(ctx, t.agent, hookPoint, candidate, who)
		if err != nil {
			// The warden is itself an LLM call, so an infra hiccup has to have
			// a policy. The owner picks it per agent (GuardrailFailClosed);
			// either way the gap is recorded, never silent.
			if t.agent.GuardrailFailClosed {
				t.turnDiag("guardrail-blocked", fmt.Sprintf("Guardrail check could not run (%v) — BLOCKED (this agent fails closed).", err))
				Log("[orchestrate.guardrail] agent=%s fail-closed block at %s: warden error: %v", t.agent.ID, hookPoint, err)
				return GuardrailDecision{Blocked: true, Message: guardrailNoVerdictMessage()}
			}
			t.turnDiag("guardrail-error", fmt.Sprintf("Guardrail check could not run (%v) — the action proceeded unchecked.", err))
			return pass
		}
		// UNSURE is not compliance. parseWardenVerdicts deliberately returns
		// "unsure" for a reply it cannot read, on the stated grounds that "an
		// unreadable warden must not read as compliance" — and then this
		// caller treated unsure exactly like comply, silently. So a warden
		// whose generation collapsed (the worker runs no-think, which this
		// deployment's model is known to degenerate under) waved the action
		// through leaving no trace at all.
		//
		// Retry once: a collapsed generation is usually transient, and a
		// second warden call is far cheaper than an unchecked consequential
		// action. If it is still unreadable, fail open — the deliberate
		// policy for warden infrastructure trouble — but leave a breadcrumb,
		// which is the house rule for every guard that drops something.
		if worstVerdict(verdicts) == guardNoVerdict {
			Log("[orchestrate.guardrail] agent=%s warden reached NO VERDICT at %s — retrying once", t.agent.ID, hookPoint)
			retried, rerr := t.app.runWarden(ctx, t.agent, hookPoint, candidate, who, wardenRetryOptions()...)
			if rerr == nil && worstVerdict(retried) != guardNoVerdict {
				verdicts = retried
			} else {
				_, reason := firstViolation(verdicts)
				if strings.TrimSpace(reason) == "" {
					reason = "warden verdict unreadable"
				}
				if t.agent.GuardrailFailClosed {
					t.turnDiag("guardrail-blocked", fmt.Sprintf(
						"Guardrail check at %s could not reach a verdict (%s) — BLOCKED (this agent fails closed). Retried once.", hookPoint, reason))
					Log("[orchestrate.guardrail] agent=%s fail-closed block at %s after retry (%s)", t.agent.ID, hookPoint, reason)
					return GuardrailDecision{Blocked: true, Message: guardrailNoVerdictMessage()}
				}
				t.turnDiag("guardrail-no-verdict", fmt.Sprintf(
					"Guardrail check at %s could not reach a verdict (%s) — the action proceeded UNCHECKED. Retried once.", hookPoint, reason))
				Log("[orchestrate.guardrail] agent=%s UNCHECKED at %s after retry (%s)", t.agent.ID, hookPoint, reason)
				return pass
			}
		}
		if worstVerdict(verdicts) != guardViolate {
			return pass
		}
		rule, reason := firstViolation(verdicts)
		// An appeal already settled this rule for this turn. The warden is
		// stateless and fresh every call, so without this the next round asks
		// the same blind question, gets the same verdict, and the win the agent
		// evidenced a moment ago evaporates — which would make the whole appeal
		// path decorative. Scoped to the turn and to the ONE rule that was
		// appealed; nothing else relaxes.
		if t.appealCleared(rule) {
			t.turnDiag("guardrail-appeal-honored", fmt.Sprintf(
				"Guardrail %q flagged a %s check again; an appeal already established its condition was met this turn, so it was not blocked.", rule, hookPoint))
			return pass
		}
		// Blocks unless the owner marked this rule correctable. A revise pass only
		// earns its cost when a compliant answer to the same question exists; where
		// the rule forbids what was asked for, each attempt regenerates the
		// violation from the same context and each one is another draft holding the
		// protected thing to retract and scrub. So core skips the correction budget
		// by default and hands the reply to the fresh-context rejection writer.
		correctable := ruleIsCorrectable(t.agent, rule)
		modeNote := " (a blocking rule — answered by a separate check, no rewrite attempted)"
		if correctable {
			modeNote = " (one rewrite will be attempted)"
		}
		// Counted on the TURN, not in this closure: the halt predicate and the
		// check are separate hooks that must read one number, and a turn's
		// escalation state belongs to the turn.
		t.guardrailBlocks++
		t.noteGuardrailRule(rule)
		t.turnDiag("guardrail-blocked", fmt.Sprintf("Guardrail %q blocked a %s check%s: %s", rule, hookPoint, modeNote, reason))
		Log("[orchestrate.guardrail] agent=%s blocked %s (rule=%q correctable=%v) block#%d", t.agent.ID, hookPoint, rule, correctable, t.guardrailBlocks)
		// File it for review. Every block, including repeats — a rule tripping
		// repeatedly is the shape most worth seeing, and the per-thread trail
		// above can only be found by someone who already knows which thread.
		t.recordGuardrailBlock(rule, hookPoint, reason)
		if t.guardrailBlocks >= guardBlockEscalateAt {
			t.notifyOwnerGuardrail(rule, t.guardrailBlocks)
			// The returned text still goes back as the blocked result, but it is
			// no longer what stops the turn — GuardrailHalted does, and core ends
			// the turn without asking this model for anything further. It used to
			// be the entire mechanism: a string reading "STOP" handed to the very
			// agent whose judgment the warden had just overruled.
			return GuardrailDecision{
				Blocked:     true,
				Correctable: correctable,
				Message:     fmt.Sprintf("STOP — you have hit enforced guardrails %d times this turn. This turn is being terminated; the user's reply is being written by a separate check. Do NOT keep rephrasing or re-routing to slip the guardrail; the owner has been notified.", t.guardrailBlocks),
			}
		}
		// A contestable rule adds one sentence inviting an appeal, and arms the
		// tool. Appended to the block message rather than replacing it: the
		// block still stands unless and until evidence overturns it, so the
		// instructions for complying have to survive the invitation.
		msg := guardrailBlockMessageAt(hookPoint, rule, reason)
		if invite := t.offerGuardrailAppeal(rule, hookPoint, candidate); invite != "" {
			msg += invite
			// Correctable regardless of the rule's own severity: an appeal is
			// worthless if the turn ends before the agent can make it. This is
			// the ONE place a contestable rule bends, and it buys a round to
			// present evidence, not a softer verdict.
			correctable = true
		}
		return GuardrailDecision{Blocked: true, Correctable: correctable, Message: msg}
	}
}

// guardrailBlockMessage is the trusted, unfenced message handed back on a
// block: it names the rule, states the action didn't happen, and explicitly
// forbids re-routing (the "denied by user → hand-rolled fetch_url" lesson).
// It is deliberately SHORT, and that is a latency property rather than a style
// choice. The first version stated eight imperatives, several of them negative
// and mutually constraining — don't re-route, don't cite a rule, don't say
// "not allowed", don't reveal the check, do change course, do deflect naturally,
// plus a parenthetical about who sees what. A reasoning model handed that spends
// thousands of tokens reconciling it before writing one sentence (visible as the
// loop's COLLAPSE-DIAG: a huge reasoning block, almost no output, no tool call),
// and every one of those tokens is turn latency the user waits through.
//
// Deliberation scales with the number of constraints there are to satisfy, so
// each clause here has to earn its place. What survived: what the rule was, that
// the call did not run, don't reach the same end another way, carry on or wrap up,
// don't name the mechanism. What went: the list of specific banned phrasings (the
// rejection writer owns wording, and this message is not user-facing), the
// restatement that the guardrail will keep blocking (already implied by "don't
// retry"), and the note about the owner's view (nothing for the agent to act on).
func guardrailBlockMessage(rule, reason string) string {
	return guardrailBlockMessageAt("", rule, reason)
}

// guardrailBlockMessageAt is guardrailBlockMessage told WHICH hook fired, so it
// can describe what actually happened.
//
// The single message said "That call did not run" at every hook. At pre_output
// the model made no call — it wrote a reply — so on exactly the turns where a
// reply is withheld it was told about a tool call it never attempted. Being
// corrected for something you did not do is not a useful correction, and it
// arrives on the round that most needs to land: the one deciding what to say
// instead.
func guardrailBlockMessageAt(hookPoint, rule, reason string) string {
	msg := "Not permitted here: \"" + strings.TrimSpace(rule) + "\"."
	if r := strings.TrimSpace(reason); r != "" {
		msg += " " + r + "."
	}
	switch strings.TrimSpace(hookPoint) {
	case guardHookPreOutput, guardHookPeriodic:
		// No call was made. What was withheld is the text, and the useful
		// instruction is about the NEXT text — including not circling back to
		// the thing that was just refused, which is how a refused request gets
		// answered a turn later.
		msg += " That reply was WITHHELD and the user did not see it. Answer what was asked without touching what the rule protects, or say briefly that you can't help with that part — and do not return to it afterwards. Do not mention a rule or a restriction."
	default:
		msg += " That call did not run. Do not reach the same result another way. Carry on with something that fits, or finish up and tell the user briefly that you couldn't do that part — without mentioning a rule or a restriction."
	}
	return msg
}
