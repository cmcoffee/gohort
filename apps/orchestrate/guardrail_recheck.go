// When a guardrail check cannot reach a verdict.
//
// The warden is a model call on the worker, so it can time out behind other
// work, error, or come back unreadable. An agent that fails closed then
// refused the action outright, on what was usually a passing load spike, and
// the person watching saw only that something was blocked.
//
// Now a check that fails is retried once, automatically, wherever it runs, as
// long as the run itself still has time (a check cut off because the run's
// own deadline passed would only fail again). When it still has no verdict,
// the agent fails closed, and its owner is watching the conversation, they are
// asked: Check again, or Skip it, with what was being checked, against which
// rules, and why the check could not run. Check again runs the same check at
// the same depth, and the action goes ahead only if it passes: there is no
// "do it anyway", because that would let one click past the rule the setting
// exists to hold. Skip it, or no answer, blocks as before.
//
// Only the owner is asked. Someone else talking to the agent (a contact, a
// channel) would be shown the owner's rules and handed a decision that is not
// theirs, so their turns, like unattended ones, get the retry and then the
// block.

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// wardenOutcome is how a guardrail check went once its automatic retry and
// any re-checks the owner asked for are spent.
type wardenOutcome struct {
	verdicts []guardrailVerdict
	err      error
	tries    int  // warden calls made
	skipped  bool // the owner chose Skip it
}

// failed reports whether the check has no verdict: its call failed, or its
// answer could not be read.
func (o wardenOutcome) failed() bool {
	return o.err != nil || worstVerdict(o.verdicts) == guardNoVerdict
}

// note says, for the turn's trail, how hard the check was tried.
func (o wardenOutcome) note() string {
	switch {
	case o.skipped:
		return fmt.Sprintf(" Tried %d times; you chose Skip it.", o.tries)
	case o.tries > 1:
		return fmt.Sprintf(" Tried %d times.", o.tries)
	}
	return ""
}

// judgeWithRecheck runs a guardrail check, retries a failed one once, and,
// for an agent that fails closed with its owner watching, asks whether to
// check again until it reaches a verdict or they skip it.
func (t *chatTurn) judgeWithRecheck(ctx context.Context, hookPoint, candidate string, who requesterIdentity, failClosed bool) wardenOutcome {
	var o wardenOutcome
	call := func(opts ...ChatOption) {
		o.tries++
		o.verdicts, o.err = t.app.runWarden(ctx, t.agent, hookPoint, candidate, who, opts...)
	}
	call()
	if !o.failed() {
		return o
	}
	if ctx.Err() == nil {
		Log("[orchestrate.guardrail] agent=%s check at %s reached no verdict (%s): retrying once", t.agent.ID, hookPoint, o.reason())
		if o.err == nil {
			// An unreadable answer: re-sample rather than ask the same way.
			call(wardenRetryOptions()...)
		} else {
			call()
		}
		if !o.failed() {
			return o
		}
	}
	for failClosed && ctx.Err() == nil && t.canAskOwnerToRecheck(who) {
		if !t.askGuardrailRecheck(hookPoint, candidate, who, o) {
			o.skipped = true
			return o
		}
		call()
		if !o.failed() {
			return o
		}
	}
	return o
}

// reason says why the check has no verdict, in words for a person.
func (o wardenOutcome) reason() string {
	switch {
	case o.err == nil:
		_, r := firstViolation(o.verdicts)
		if strings.TrimSpace(r) == "" {
			r = "the checker's answer could not be read"
		}
		return r
	case errors.Is(o.err, context.DeadlineExceeded):
		return "the check timed out"
	}
	return o.err.Error()
}

// canAskOwnerToRecheck reports whether this turn may put a re-check card in
// front of anyone: its requester is the agent's owner, on the web rather than
// a channel, with a conversation to show the card in.
func (t *chatTurn) canAskOwnerToRecheck(who requesterIdentity) bool {
	return t != nil && who.Owner && who.Channel == "" && (t.sse != nil || t.askVia != nil)
}

// askGuardrailRecheck asks the owner whether to check again. True for Check
// again; false for Skip it, or no answer in time.
func (t *chatTurn) askGuardrailRecheck(hookPoint, candidate string, who requesterIdentity, o wardenOutcome) bool {
	actions := []map[string]any{
		{"label": "Check again", "value": "recheck"},
		{"label": "Skip it", "value": "skip", "variant": "danger"},
	}
	req := toolConfirmRequest{
		tool:    "guardrail re-check",
		prompt:  fmt.Sprintf("Your guardrail check couldn't run %s (%s). Check it again, or skip it?", guardHookWords(hookPoint), o.reason()),
		detail:  recheckDetail(t.agent, hookPoint, candidate, who),
		because: "the guardrail check could not run: " + o.reason(),
	}
	picked, ok := t.parkConfirm(req, actions, map[string]bool{"recheck": true})
	return ok && picked == "recheck"
}

// guardHookWords names a check point the way the card says it.
func guardHookWords(hookPoint string) string {
	switch hookPoint {
	case guardHookPreAction:
		return "before an action"
	case guardHookPreOutput:
		return "before a reply"
	case guardHookPeriodic:
		return "partway through a reply"
	case guardHookPreInput:
		return "on your message"
	}
	return "at " + hookPoint
}

// recheckDetail is what the card shows: what was about to happen, and the
// rules it was being checked against. The deployment's rules are counted, not
// quoted: they are an administrator's, and their wording is not the owner's to
// read here.
func recheckDetail(agent AgentRecord, hookPoint, candidate string, who requesterIdentity) string {
	var b strings.Builder
	what := strings.TrimSpace(candidate)
	if r := []rune(what); len(r) > 300 {
		what = string(r[:300]) + "..."
	}
	if hookPoint == guardHookPreAction {
		b.WriteString("About to run: " + what + "\n")
	} else {
		b.WriteString("About to say: " + what + "\n")
	}
	var own []string
	deployment := 0
	for _, r := range wardenRules(agent, hookPoint, candidate, who) {
		if r.Global {
			deployment++
			continue
		}
		own = append(own, "- "+strings.TrimSpace(r.Text))
	}
	if len(own) > 0 {
		b.WriteString("Checked against:\n" + strings.Join(own, "\n") + "\n")
	}
	if deployment > 0 {
		fmt.Fprintf(&b, "Also %d deployment rule(s) set by an administrator.\n", deployment)
	}
	return strings.TrimSpace(b.String())
}
