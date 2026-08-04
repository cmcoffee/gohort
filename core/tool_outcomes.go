// Whether a tool action has ever actually worked.
//
// The verify gate proves a tool works ONCE, at authoring time, and then stops
// watching. Nothing looks at it again. So a definition that is wrong in a way
// the author's single test didn't reach goes on failing forever, and the only
// record is a Debug line nobody reads.
//
// Observed: one toolbox action was called 987 times over two days and failed
// 371 of them — every failure the same shape, a required param the caller could
// not possibly supply on a first call. 260 of those were a single action that
// had never once returned a result. Nothing anywhere said so. Not the model,
// which kept trying; not the session trail; not the toolbox page. The framework
// had every fact it needed and never put two of them together.
//
// This is the seam that does. Hosts wire a recorder; the grouped-tool dispatcher
// reports every outcome through it; what to do about a run of failures is the
// app's to decide.

package core

import (
	"strconv"
	"strings"
)

// ToolOutcomeRecorder receives the result of one grouped-tool action call.
// err is nil on success. Called for EVERY dispatch, including the ones the
// framework's own argument validation rejects before the handler runs — a call
// bounced for a missing param is a call that failed, and those were the whole
// of the observed failure.
//
// The returned string, when non-empty, is APPENDED to the error the model is
// about to read. That return is the point of the whole seam: the host is the
// only thing that knows this action has failed twenty times running, and the
// model is the only thing that can stop calling it. See ToolNeverWorkedHint.
//
// Nil (the default) means no bookkeeping, which is what every host had before.
type ToolOutcomeRecorder func(sess *ToolSession, tool, action string, err error) string

// RecordToolOutcome is the wired recorder. Set by the host at init.
var RecordToolOutcome ToolOutcomeRecorder

// noteToolOutcome reports one call and returns any advisory to append to the
// model's error. Nil-safe at both ends.
func noteToolOutcome(sess *ToolSession, tool, action string, err error) string {
	if RecordToolOutcome == nil || sess == nil {
		return ""
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return "" // a bare probe or a help dump is not an attempt at anything
	}
	return RecordToolOutcome(sess, tool, action, err)
}

// ToolNeverWorkedHint is what a host appends to the error of an action that has
// failed repeatedly and never once succeeded.
//
// Addressed to the model, and its whole job is to stop the retry loop: the
// definition is wrong, so trying again with different arguments cannot help,
// and the person who can fix it is the user. Without this the model has no way
// to distinguish "I called it wrong" — worth another go — from "this has never
// worked for anyone" — worth reporting. It had no way to tell, so it kept
// guessing, hundreds of times.
func ToolNeverWorkedHint(tool, action string, failures int) string {
	return "\n\nSTOP RETRYING THIS. " + tool + "(action=\"" + action + "\") has now failed " +
		strconv.Itoa(failures) + " times and has never once succeeded — for anyone, on any arguments. " +
		"That is a broken tool DEFINITION, not a mistake in your call, so re-sending it with different " +
		"params cannot work. Do not try another route to the same thing either. Tell the user plainly " +
		"that this action is broken and needs fixing (its params or its URL), then carry on with whatever " +
		"else they asked for."
}
