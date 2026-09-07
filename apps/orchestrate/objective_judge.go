// Reading whether a recurring task's goal is reached, instead of counting its
// fires.
//
// A task carrying an `until` is asking for something to be TRUE, not for a
// command to be run on a cadence. Deciding that from the attempt's own words is
// the mistake the turn judge exists to prevent: a fire that reports "published
// and linked to the thread" is consistent with a fire that published nothing —
// live, on this very path, nine reads were reported as three posts. So the
// check reads the same evidence the claim judge does. The actions the attempt
// ran are the evidence; what it said about them is a claim.
//
// See docs/loop-objectives.md.
package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// objectiveEvidence is what the check reasons over: the goal, the attempt's
// actions, and its report, in that order of authority.
type objectiveEvidence struct {
	// Objective is the completion check in the owner's own words.
	Objective string
	// Reply is what this attempt reported when it finished.
	Reply string
	// ToolCalls names every action this attempt ran, in order, duplicates
	// kept. Each is a LABEL ("name" or "name/action") for the same reason the
	// claim judge takes labels: a grouped tool hides reading and writing behind
	// one name, and a goal about posting is not met by nine reads.
	ToolCalls []string
	// ToolErrors counts the actions that failed, so "it tried and the API
	// refused" is distinguishable from "it never tried".
	ToolErrors int
	// Attempt is this fire's number, and MaxAttempts the bound when one is set
	// (0 = no attempt bound). Shown to the checker as context, never as
	// pressure: running out of attempts does not make a goal met.
	Attempt     int
	MaxAttempts int
}

// objectiveVerdict is the answer: met or not, and one line saying why. The
// reason is what the card shows the owner and what the next attempt is told,
// so it has to name a thing, not a feeling.
type objectiveVerdict struct {
	Met    bool
	Reason string
}

const objectiveJudgeSysPrompt = `You are an OBJECTIVE CHECKER. A recurring task was given a goal and has just made one attempt at it. Decide whether the goal is satisfied NOW.

You are given the goal, the actions the attempt actually ran, how many of them failed, and what the attempt reported afterwards.

Rules:
- The ACTIONS are the evidence. The report is a CLAIM about them.
- Answer MET only when the evidence shows the goal is achieved.
- A report that says the goal was reached while the actions do not show it is NOT_YET. An attempt that ran no actions has almost certainly not reached a goal that requires doing something.
- A goal that only asks for text, a judgement or an answer CAN be met by the report alone.
- Judge the state of the world, not the effort. A thorough attempt that fell short is NOT_YET.
- reason: ONE short sentence. For NOT_YET name what is still missing; for MET name what shows it is done. No preamble, no advice.

Answer with JSON only: {"verdict":"MET"|"NOT_YET","reason":"..."}`

// objectiveEvidenceMessage renders the evidence. Split out from the call so the
// prompt can be asserted on without a model — the checker's answer turns
// entirely on what this message does and does not say.
func objectiveEvidenceMessage(ev objectiveEvidence) string {
	ran := "none"
	if len(ev.ToolCalls) > 0 {
		ran = strings.Join(ev.ToolCalls, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL:\n%s\n\n", truncateObs(strings.TrimSpace(ev.Objective), 600))
	if ev.MaxAttempts > 0 {
		fmt.Fprintf(&b, "ATTEMPT: %d of %d\n", ev.Attempt, ev.MaxAttempts)
	} else {
		fmt.Fprintf(&b, "ATTEMPT: %d\n", ev.Attempt)
	}
	fmt.Fprintf(&b, "ACTIONS THIS ATTEMPT RAN, COMPLETE AND IN ORDER: %s\n", ran)
	fmt.Fprintf(&b, "ACTIONS THAT FAILED: %d\n\n", ev.ToolErrors)
	fmt.Fprintf(&b, "WHAT THE ATTEMPT REPORTED:\n%s\n", truncateObs(strings.TrimSpace(ev.Reply), 2000))
	return b.String()
}

// judgeObjective asks the worker tier whether the goal is met. The bool is
// "there is an opinion" — false on any error, unparseable answer, or a verdict
// that is neither word. The caller treats no-opinion as an unmet attempt that
// still counts, never as a pass: failing open here would retire a task whose
// goal was never reached, which is the one outcome nobody would notice.
func (T *OrchestrateApp) judgeObjective(ctx context.Context, ev objectiveEvidence) (objectiveVerdict, bool) {
	if T == nil || T.LLM == nil {
		return objectiveVerdict{}, false
	}
	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: objectiveEvidenceMessage(ev)}},
		WithSystemPrompt(objectiveJudgeSysPrompt), WithJSONMode(),
		WithRouteKey("app.orchestrate.worker"), WithThink(false))
	if err != nil {
		Debug("[objective] LLM error: %v — no opinion", err)
		return objectiveVerdict{}, false
	}
	var out struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if derr := DecodeJSON(resp.Content, &out); derr != nil {
		// A reason quoting the goal usually carries unescaped quotation marks;
		// salvageJudgeJSON recovers the object by its keys.
		fields, ok := salvageJudgeJSON(resp.Content, []string{"verdict", "reason"})
		if !ok {
			Debug("[objective] unparseable verdict %q — no opinion", truncateObs(resp.Content, 120))
			return objectiveVerdict{}, false
		}
		out.Verdict, out.Reason = fields["verdict"], fields["reason"]
	}
	met := false
	switch strings.ToUpper(strings.TrimSpace(out.Verdict)) {
	case "MET":
		met = true
	case "NOT_YET", "NOT YET", "NOTYET":
		met = false
	default:
		// Neither word. Retiring a task on a verdict nobody can read is worse
		// than one more attempt.
		Debug("[objective] unusable verdict %q — no opinion", truncateObs(out.Verdict, 60))
		return objectiveVerdict{}, false
	}
	reason := strings.TrimSpace(out.Reason)
	if reason == "" {
		reason = "no reason given"
	}
	word := "NOT_YET"
	if met {
		word = "MET"
	}
	Log("[objective] %s (attempt %d) — %q (tools=%d errors=%d)",
		word, ev.Attempt, truncateObs(reason, 140), len(ev.ToolCalls), ev.ToolErrors)
	return objectiveVerdict{Met: met, Reason: reason}, true
}

// objectiveOutcome turns a verdict into what the task does about it: the line
// the card and the ledger carry, whether the chain stands down, and whether it
// stands down UNMET (which is a thing to escalate, not a quiet retirement).
//
// A pure function because it is the rule, not the plumbing: met retires,
// unjudged counts as an attempt, and the attempt bound stalls rather than
// cancelling silently.
func objectiveOutcome(v objectiveVerdict, judged bool, attempt, maxAttempts int) (line string, stop, stalled bool) {
	if judged && v.Met {
		return "objective met — " + v.Reason, true, false
	}
	reason := v.Reason
	if !judged {
		// Not "met, probably". An attempt nobody could judge is an attempt that
		// showed nothing, and it costs a fire like any other.
		reason = "the check could not be judged this cycle"
	}
	if maxAttempts > 0 && attempt >= maxAttempts {
		return fmt.Sprintf("objective STALLED after %d attempt(s) — %s", attempt, reason), true, true
	}
	return "objective not yet — " + reason, false, false
}

// objectiveToolLabels renders a fire's tool trace the way the checker reads it:
// "name/action" where a grouped tool declares one, plus the count that failed.
func objectiveToolLabels(trace []PersistedToolCall) ([]string, int) {
	labels := make([]string, 0, len(trace))
	failed := 0
	for _, tc := range trace {
		label := tc.Name
		if act, ok := tc.Args["action"].(string); ok && strings.TrimSpace(act) != "" {
			label += "/" + strings.TrimSpace(act)
		}
		labels = append(labels, label)
		if strings.TrimSpace(tc.Err) != "" {
			failed++
		}
	}
	return labels, failed
}
