// An objective is a schedule that knows when it is finished.
//
// Two surfaces schedule work that repeats: a recurring task (an agent's own
// `recurring` tool) and a standing agent (the Fleet path, `create_standing_agent`).
// Both ran until a cap or until somebody stopped them, and neither ever asked
// whether the thing had been achieved. Giving either an `until` turns it from a
// cadence into a goal: every fire is judged against that sentence from what the
// attempt actually DID, the fire that reaches it is the last, and one that runs
// out of attempts stops and says so instead of going quiet.
//
// This file holds only what BOTH surfaces store, which is the attempt history.
// The judging and the outcome rules live with the runner in
// apps/orchestrate/objective_judge.go — core owns the record, not the policy.
//
// See docs/loop-objectives.md.
package core

// ObjectiveAttempt is one earlier fire's verdict, as the next fire is told it.
//
// Kept on the schedule's own record rather than recovered from the run ledger:
// the ledger holds the fire as prose for a person to read in Activity, while
// the next attempt needs the verdict structured. Parsing reasons back out of a
// display string would make a wire format out of a sentence written to be read.
type ObjectiveAttempt struct {
	At     string `json:"at"`               // RFC3339 UTC
	Met    bool   `json:"met,omitempty"`    // recorded for completeness; a met objective stops
	Reason string `json:"reason,omitempty"` // the checker's one line
}
