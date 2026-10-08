package orchestrate

// Whether each app this session touched stands ready, in the same per-session
// ledger the authored tools are graded on, so the finish check holds a reply
// that calls an app done while its last check failed.
//
// Observed twice: the Builder's last app_def result said FAIL and "Do NOT tell
// the user the app is ready", and its reply told the user where to open it.
// An app_def failure comes back as a result, not an error, so the guard for
// a turn that gives up on errors never saw it, and the prompt's instruction
// was all there was. Recording the standing makes it something the turn's
// end can check.

import "strings"

// appLedgerPrefix marks an app's row in the tool ledger: a tool name cannot
// contain a colon, so the two never collide.
const appLedgerPrefix = "app:"

// noteAppStanding records whether app slug stands ready after this call, and
// if not, why, in the words the model needs to act on.
func (t *chatTurn) noteAppStanding(slug string, ready bool, reason string) {
	if t == nil || t.udb == nil || strings.TrimSpace(slug) == "" {
		return
	}
	if sid := t.chatSessionID(); sid != "" {
		recordToolVerify(t.udb, sid, appLedgerPrefix+slug, ready, reason)
	}
}

// forgetAppStanding drops a deleted app's row: there is nothing left to verify.
func (t *chatTurn) forgetAppStanding(slug string) {
	sid := t.chatSessionID()
	if t == nil || t.udb == nil || sid == "" {
		return
	}
	existing := loadToolVerifications(t.udb, sid)
	out := existing[:0]
	for _, e := range existing {
		if e.Tool != appLedgerPrefix+slug {
			out = append(out, e)
		}
	}
	t.udb.Set(toolVerifyTable, sid, out)
}

// ledgerApp reports whether a ledger row is an app's, and its slug.
func ledgerApp(name string) (string, bool) {
	return strings.CutPrefix(name, appLedgerPrefix)
}
