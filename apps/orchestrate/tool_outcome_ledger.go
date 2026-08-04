// The running record of whether each tool action has ever actually worked.
//
// Distinct from the verify ledger next door, and the distinction is the point.
// tool_verify_ledger records that a tool passed a test ONCE, at authoring time,
// scoped to the session that authored it. This records what happens every time
// it is called afterwards, for as long as it exists — because the failure it
// exists for is a definition that was signed off and then never worked again.
//
// Observed: one toolbox action was called 987 times over two days and failed
// 371 of them. 260 were a single action that had never once returned a result.
// The model kept trying because nothing told it not to; the user never found
// out because nothing told them either. Every fact needed to say "this is
// broken" was already in the process, unjoined.
//
// Keyed by user rather than by session: "has this ever worked" is a question
// about the tool, not about a conversation, and the answer has to survive both.

package orchestrate

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const toolOutcomeTable = "orchestrate_tool_outcomes"

// neverWorkedThreshold is how many failures with ZERO successes it takes before
// the framework calls an action broken.
//
// Low, because the claim is a strong one and the evidence for it is strong too:
// an action that has failed this many times without ever succeeding is not
// having a bad run. Five is comfortably past a model fumbling its arguments a
// couple of times and nowhere near the 260 it took to notice by hand.
const neverWorkedThreshold = 5

// toolOutcomeRecord is one action's lifetime standing.
type toolOutcomeRecord struct {
	Tool   string `json:"tool"`
	Action string `json:"action"`
	OK     int    `json:"ok"`
	Fail   int    `json:"fail"`
	// LastError is the most recent failure text, truncated. The whole value of
	// the record to a human is knowing WHAT keeps going wrong without going to
	// the log for it.
	LastError string    `json:"last_error,omitempty"`
	FirstAt   time.Time `json:"first_at"`
	LastAt    time.Time `json:"last_at"`
	// Flagged records that the threshold has already been reported, so a broken
	// action warns once rather than on every one of its next two hundred calls.
	Flagged bool `json:"flagged,omitempty"`
}

// broken reports the state this whole file exists to detect: repeated failures
// and not one success, ever.
func (r toolOutcomeRecord) broken() bool {
	return r.OK == 0 && r.Fail >= neverWorkedThreshold
}

func (r toolOutcomeRecord) key() string { return r.Tool + "." + r.Action }

// outcomeMu serializes the read-modify-write. Tool calls in one round dispatch
// in parallel goroutines, so two failures of the same action can land at once
// and the second would otherwise overwrite the first's count with a stale one —
// which, for a counter whose entire job is to reach a threshold, means never
// reaching it.
var outcomeMu sync.Mutex

func init() {
	RecordToolOutcome = func(sess *ToolSession, tool, action string, err error) string {
		if sess == nil || sess.DB == nil || strings.TrimSpace(sess.Username) == "" {
			return ""
		}
		return recordToolOutcome(sess, tool, action, err)
	}
}

// recordToolOutcome updates one action's tally, reports a newly-broken action
// exactly once — to the log, and to the session trail where the user is
// actually standing when it happens — and returns the advisory to hand the
// model on EVERY failure of an action already known to be broken.
//
// Once for the human, every time for the model, and the asymmetry is
// deliberate. A person needs telling once; a model reads only the error in
// front of it and has no memory of the nineteen before.
func recordToolOutcome(sess *ToolSession, tool, action string, err error) string {
	outcomeMu.Lock()
	defer outcomeMu.Unlock()

	all := loadToolOutcomes(sess.DB, sess.Username)
	now := time.Now()
	rec := toolOutcomeRecord{Tool: tool, Action: action, FirstAt: now}
	idx := -1
	for i, e := range all {
		if e.Tool == tool && e.Action == action {
			rec, idx = e, i
			break
		}
	}
	if err == nil {
		rec.OK++
		// A success retires the flag AND the failure count. The definition was
		// fixed, or the call finally hit the shape it wanted; either way the
		// action is not broken any more and must not keep saying it is.
		rec.Fail, rec.Flagged, rec.LastError = 0, false, ""
	} else {
		rec.Fail++
		rec.LastError = oneLineError(err.Error(), 200)
	}
	rec.LastAt = now

	newlyBroken := rec.broken() && !rec.Flagged
	if newlyBroken {
		rec.Flagged = true
	}
	if idx >= 0 {
		all[idx] = rec
	} else {
		all = append(all, rec)
	}
	sess.DB.Set(toolOutcomeTable, sess.Username, all)

	advice := ""
	if err != nil && rec.broken() {
		advice = ToolNeverWorkedHint(tool, action, rec.Fail)
	}
	if !newlyBroken {
		return advice
	}
	Log("[tool-health] %s(action=%q) has failed %d times and never succeeded — the definition is likely wrong. Last error: %s",
		tool, action, rec.Fail, rec.LastError)
	// Into the session trail too. A log line is for whoever goes looking; the ⚠
	// reaches the person who is, right now, watching it not work.
	appendSessionDiag(sess.DB, sess.AgentID, sess.ChatSessionID, "tool-never-worked",
		fmt.Sprintf("%s(action=%q) has now failed %d times and has never once succeeded. That points at the tool's definition — its required params or its URL — rather than at how it is being called. Last error: %s",
			tool, action, rec.Fail, rec.LastError))
	return advice
}

// brokenToolActions lists everything currently failing with no successes,
// worst first. For a surface that wants to show a user what needs fixing.
func brokenToolActions(db Database, user string) []toolOutcomeRecord {
	if db == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	var out []toolOutcomeRecord
	for _, e := range loadToolOutcomes(db, user) {
		if e.broken() {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Fail != out[j].Fail {
			return out[i].Fail > out[j].Fail
		}
		return out[i].key() < out[j].key()
	})
	return out
}

// loadToolOutcomes reads a user's whole tally. One row holding the slice, same
// shape and for the same reasons as the verify ledger.
func loadToolOutcomes(db Database, user string) []toolOutcomeRecord {
	var out []toolOutcomeRecord
	if db == nil || strings.TrimSpace(user) == "" {
		return out
	}
	db.Get(toolOutcomeTable, user, &out)
	return out
}

// oneLineError flattens an error for storage: errors here carry the framework's
// own multi-line guidance, and a record meant to be read at a glance should not
// contain a paragraph.
func oneLineError(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
