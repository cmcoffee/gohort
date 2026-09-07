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
	"time"

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
	return T.judgeWithPrompt(ctx, objectiveJudgeSysPrompt, objectiveEvidenceMessage(ev), ev.Attempt, len(ev.ToolCalls), ev.ToolErrors)
}

// judgeWithPrompt is the model call and the answer-reading, shared by every
// kind of evidence. Only the prompt and the message differ between them; the
// rule that an unreadable verdict is NO opinion rather than a pass is the same
// everywhere, and is the part worth having in one place.
func (T *OrchestrateApp) judgeWithPrompt(ctx context.Context, sysPrompt, message string, attempt, tools, toolErrors int) (objectiveVerdict, bool) {
	if T == nil || T.LLM == nil {
		return objectiveVerdict{}, false
	}
	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: message}},
		WithSystemPrompt(sysPrompt), WithJSONMode(),
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
		word, attempt, truncateObs(reason, 140), tools, toolErrors)
	return objectiveVerdict{Met: met, Reason: reason}, true
}

// objectiveObservationSysPrompt judges a goal from what a monitor OBSERVED.
//
// It is a separate prompt rather than a reuse of the attempt checker because
// the two are judging opposite things. The attempt checker treats the actions
// as the evidence and is told that an attempt which ran no actions has almost
// certainly not reached its goal — the rule that stops an agent from declaring
// success it did not earn. A monitor fire runs no actions BY DESIGN: it
// watched something and reports what changed. Sent through the attempt
// checker, every monitor fire would look like an attempt that did nothing, and
// a goal that was plainly reached would read NOT_YET forever.
const objectiveObservationSysPrompt = `You are an OBJECTIVE CHECKER. A monitor is watching something on the owner's behalf, and has just observed a change. Decide whether the owner's stopping condition is satisfied NOW.

You are given the condition and what the monitor observed.

Rules:
- The OBSERVATION is the evidence. Judge only what it shows.
- Answer MET only when the observation shows the condition is satisfied. A change in the right direction is not the condition being reached.
- The monitor takes no actions and is not supposed to. Never answer NOT_YET on the grounds that nothing was done.
- An observation that does not mention the condition either way is NOT_YET.
- reason: ONE short sentence naming what in the observation decided it. No preamble, no advice.

Answer with JSON only: {"verdict":"MET"|"NOT_YET","reason":"..."}`

// monitorObservationMessage renders a fire for the observation checker. Split
// out from the call for the same reason as the attempt version: the verdict
// turns entirely on what this says, so it is assertable without a model.
func monitorObservationMessage(until, observed string, fire int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "STOPPING CONDITION:\n%s\n\n", truncateObs(strings.TrimSpace(until), 600))
	fmt.Fprintf(&b, "FIRE: %d\n\n", fire)
	fmt.Fprintf(&b, "WHAT THE MONITOR OBSERVED:\n%s\n", truncateObs(strings.TrimSpace(observed), 2000))
	return b.String()
}

// judgeMonitorObjective asks whether a monitor's stopping condition is met by
// the change it just saw.
func (T *OrchestrateApp) judgeMonitorObjective(ctx context.Context, until, observed string, fire int) (objectiveVerdict, bool) {
	return T.judgeWithPrompt(ctx, objectiveObservationSysPrompt, monitorObservationMessage(until, observed, fire), fire, 0, 0)
}

// monitorObjective is the shared by-value view of a monitor's goal, so the
// state label and the attempts block read a monitor the same way they read the
// other two scheduling surfaces.
func monitorObjective(m EventMonitor) objectiveRun {
	return objectiveRun{Until: m.Until, Attempts: m.Attempts, Username: m.Owner}
}

// settleMonitorObjective judges a monitor's stopping condition against the
// change it just reported, records the verdict on the monitor's own record,
// and stops the monitor when the condition is met.
//
// Judged whether or not the alert reached anybody: the condition is about the
// world, not about delivery, and the ledger already carries a row for each.
func (T *OrchestrateApp) settleMonitorObjective(ctx context.Context, m EventMonitor, observed string) {
	until := strings.TrimSpace(m.Until)
	if until == "" {
		return
	}
	// This fire is counted after the wake returns, so it is one past the
	// allowance the record currently shows.
	fire := MonitorFiresUsed(m) + 1
	v, judged := T.judgeMonitorObjective(ctx, until, observed, fire)
	reason := objectiveReason(v, judged)
	cur, ok := GetEventMonitor(RootDB, m.Owner, m.Name)
	if !ok {
		return
	}
	cur.Attempts = appendObjectiveAttempt(cur.Attempts, judged && v.Met, reason)
	SaveEventMonitor(RootDB, cur)
	if judged && v.Met {
		StopEventMonitor(RootDB, m.Owner, m.Name,
			"Stopped: the condition it was watching for is met — "+reason+" Nothing is broken; resume it to watch again.")
	}
}

// objectiveReason is the one line the card, the ledger and the NEXT attempt are
// all told. An attempt nobody could judge says so in those words: the next fire
// reading "the check could not be judged" knows it learned nothing, where a
// blank reason would read as a clean attempt that simply found nothing to say.
func objectiveReason(v objectiveVerdict, judged bool) string {
	if !judged {
		return "the check could not be judged this cycle"
	}
	return v.Reason
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
	// Not "met, probably". An attempt nobody could judge is an attempt that
	// showed nothing, and it costs a fire like any other.
	reason := objectiveReason(v, judged)
	if maxAttempts > 0 && attempt >= maxAttempts {
		return fmt.Sprintf("objective STALLED after %d attempt(s) — %s", attempt, reason), true, true
	}
	return "objective not yet — " + reason, false, false
}

// objectiveAttemptNumber is which attempt of the CURRENT allowance this fire
// is. FireCount only ever climbs, so comparing it to MaxAttempts directly would
// mean a resumed objective stalls again on its very first fire — the owner
// fixed what the stall named and got one more refusal for it. Resume moves
// AttemptsBase forward instead, which restarts the allowance while leaving the
// fire count, the history and the ledger untouched.
func objectiveAttemptNumber(p orchUpdatePayload) int {
	n := p.FireCount + 1 - p.AttemptsBase
	if n < 1 {
		// A base ahead of the count means the payload was edited or restored;
		// treat it as a fresh allowance rather than a negative attempt.
		return 1
	}
	return n
}

// objectiveStateLabel says where an objective stands, for the console row and
// the recurring tool's listing. Empty for an ordinary recurring task, which has
// no goal to stand in relation to.
//
// A PARKED objective is not described here: it renders through the broken-row
// label, which already carries the stall reason the park was given.
func objectiveStateLabel(o objectiveRun) string {
	if strings.TrimSpace(o.Until) == "" {
		return ""
	}
	if len(o.Attempts) == 0 {
		return "objective — no attempts yet"
	}
	last := o.Attempts[len(o.Attempts)-1]
	if last.Met {
		return "objective — met: " + truncateObs(last.Reason, 160)
	}
	return fmt.Sprintf("objective — not yet (%d attempt(s)): %s", len(o.Attempts), truncateObs(last.Reason, 160))
}

// brokenListReason surfaces a parked task's reason in the recurring tool's
// listing, so the model that scheduled an objective can see it stopped without
// being told to go read a console.
func brokenListReason(p orchUpdatePayload) string {
	if !p.Broken {
		return ""
	}
	if r := strings.TrimSpace(p.BrokenReason); r != "" {
		return r
	}
	return "parked"
}

// objectiveRun is the objective half of whatever is running, by value. Two
// records carry one: a recurring task's payload and a standing agent. They keep
// the fields FLAT rather than sharing an embedded struct, because kvlite stores
// both with gob and gob nests an embedded struct — which would silently change
// the shape of every payload already written.
type objectiveRun struct {
	Until    string
	Attempts []ObjectiveAttempt
	Username string // whose local clock the attempt timestamps are shown in
}

func (p orchUpdatePayload) objective() objectiveRun {
	return objectiveRun{Until: p.Until, Attempts: p.Attempts, Username: p.Username}
}

func standingObjective(sa StandingAgent) objectiveRun {
	return objectiveRun{Until: sa.Until, Attempts: sa.Attempts, Username: sa.Owner}
}

// objectiveAttemptsKept bounds the history carried on the payload. Twelve is
// enough for the block to show a pattern and small enough that it stays a note
// rather than a transcript — it rides the volatile tail of every fire's prompt.
const objectiveAttemptsKept = 12

// appendObjectiveAttempt records what an attempt came to, returning the new
// history for the caller to store on its own record.
//
// The run ledger already holds a row per fire and is what a PERSON reads in
// Activity. This is the other job: the structured state the next attempt is
// told, kept where the next attempt will actually find it. Parsing the reasons
// back out of the ledger's display prose would make a wire format out of a
// sentence written to be read.
func appendObjectiveAttempt(attempts []ObjectiveAttempt, met bool, reason string) []ObjectiveAttempt {
	// Fresh slice rather than append-in-place: the caller's record may have
	// been copied from another and share its backing array.
	kept := append([]ObjectiveAttempt(nil), attempts...)
	kept = append(kept, ObjectiveAttempt{
		At:     time.Now().UTC().Format(time.RFC3339),
		Met:    met,
		Reason: strings.TrimSpace(reason),
	})
	if n := len(kept); n > objectiveAttemptsKept {
		kept = kept[n-objectiveAttemptsKept:]
	}
	return kept
}

// objectiveAttemptsBlock is what makes a fifth attempt different from a first:
// what was already tried, and why each one fell short, in the checker's own
// words. Empty on the first attempt, and for any task that is not an objective.
//
// Rendered into the fire's prompt beside the time context — the volatile tail
// that never caches anyway, so it costs no prefix reuse.
func objectiveAttemptsBlock(o objectiveRun) string {
	objective := strings.TrimSpace(o.Until)
	if objective == "" || len(o.Attempts) == 0 {
		return ""
	}
	loc := UserLocation(o.Username)
	var b strings.Builder
	fmt.Fprintf(&b, "[Objective: %s\n", truncateObs(objective, 600))
	// The COUNT, never the bound. Two reasons: after a Resume the allowance
	// restarts while the history keeps its length, so "3 of 5" would be a lie;
	// and telling the model it is nearly out of tries is pressure to declare
	// success, which is the one thing the checker exists to catch.
	fmt.Fprintf(&b, "Attempts so far: %d.\n", len(o.Attempts))
	for i, a := range o.Attempts {
		when := a.At
		if ts, err := time.Parse(time.RFC3339, a.At); err == nil {
			when = ts.In(loc).Format("2006-01-02 15:04")
		}
		verdict := "not yet"
		if a.Met {
			verdict = "met"
		}
		fmt.Fprintf(&b, " %d. %s — %s: %s\n", i+1, when, verdict, truncateObs(a.Reason, 200))
	}
	b.WriteString("Do not repeat an attempt that already failed for the same reason.]")
	return b.String()
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
