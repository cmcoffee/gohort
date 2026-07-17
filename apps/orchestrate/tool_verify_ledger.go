// Tool-verification ledger — the per-session record of which authored tools
// currently stand verified, and why the rest don't.
//
// Verification outcomes used to be prose and nothing else: add_tool said
// "Verification call with test_args FAILED: …", tool_def's test printed a
// PASS/FAIL table, and both scrolled away. Nothing downstream could see them,
// so report_build_gaps graded on the only signal it had — self-reported step
// status. A model that marked its own step done after a failed verify was told
// "All steps completed successfully — no gaps to report" and duly reported
// success to the user, shipping a tool that had never worked.
//
// The ledger makes that outcome durable for the length of the session, so the
// done-gate can grade on what was VERIFIED rather than on what was CLAIMED.
// Keyed by chat session id in a flat table for the same reason as
// authoring.go: the write side (add_tool, tool_def) only ever sees a
// ToolSession, while the read side (report_build_gaps) is chatTurn-bound.
//
// One row per session holding the whole slice — fewer keys, atomic updates,
// and the row count stays bounded by sessions rather than tools.

package orchestrate

import (
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const toolVerifyTable = "orchestrate_tool_verify"

// toolVerifyRecord is the standing of ONE authored tool in a session.
type toolVerifyRecord struct {
	Tool string `json:"tool"`
	// Passed is true only after a verification actually succeeded. It is not
	// "no error seen" — a tool nobody tested is not verified.
	Passed bool `json:"passed"`
	// Reason explains a false Passed in the words the model needs to act on
	// ("never tested", "edited after passing", the failure text). Empty when
	// Passed.
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// Wire the core hook to this ledger, so every authoring surface — including
// tool_def, which lives in another package and only ever sees a ToolSession —
// records through one seam.
func init() {
	ToolVerifyRecorder = func(sess *ToolSession, toolName string, passed bool, reason string) {
		if sess == nil || sess.DB == nil || sess.ChatSessionID == "" {
			return
		}
		recordToolVerify(sess.DB, sess.ChatSessionID, toolName, passed, reason)
	}
}

// recordToolVerify upserts one tool's standing. Replace-by-name, so the LATEST
// outcome always wins: a passing re-test clears an earlier failure, and an edit
// after a pass demotes it right back.
func recordToolVerify(db Database, sessionID, tool string, passed bool, reason string) {
	if db == nil || sessionID == "" {
		return
	}
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return
	}
	existing := loadToolVerifications(db, sessionID)
	out := existing[:0]
	for _, e := range existing {
		if e.Tool != tool {
			out = append(out, e)
		}
	}
	out = append(out, toolVerifyRecord{Tool: tool, Passed: passed, Reason: reason, At: time.Now()})
	db.Set(toolVerifyTable, sessionID, out)
}

// loadToolVerifications returns every recorded standing for a session, name-
// ordered so the gap report is stable across calls.
func loadToolVerifications(db Database, sessionID string) []toolVerifyRecord {
	if db == nil || sessionID == "" {
		return nil
	}
	var out []toolVerifyRecord
	if !db.Get(toolVerifyTable, sessionID, &out) {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// unverifiedTools returns the tools that do NOT currently stand verified —
// exactly what a done-gate must refuse to sign off on.
func unverifiedTools(db Database, sessionID string) []toolVerifyRecord {
	var out []toolVerifyRecord
	for _, r := range loadToolVerifications(db, sessionID) {
		if !r.Passed {
			out = append(out, r)
		}
	}
	return out
}

// clearToolVerifications drops the slot when a session is deleted, so the table
// doesn't accumulate rows for threads that no longer exist.
func clearToolVerifications(db Database, sessionID string) {
	if db == nil || sessionID == "" {
		return
	}
	db.Unset(toolVerifyTable, sessionID)
}
