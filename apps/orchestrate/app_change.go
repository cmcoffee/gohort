package orchestrate

// What one save changed in a live app, and holding a changed action to a run.
//
// "Published" read as "you will see it", and it did not say what changed. A
// change to an action's script (how a button unpacks a downloaded case) left
// the page exactly as it was, so the person opened it, saw nothing new, and
// read the change as not taken. And the save's own check runs the data
// sources but never an action, since an action can write or reach an outside
// system, so the app stood ready on the finish check with the changed action
// never run: the Builder said the change was done, and only later ran it and
// found it broken.
//
// Every save now says what it changed and, when the page did not, that it
// will look the same and where the change shows. A changed or new action
// leaves the app not ready on the finish check until it has run: verify runs
// it, and so does test, which clears it when that was all that stood open.

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appChange is what one save changed, by part.
type appChange struct {
	page        bool
	actions     []string // changed or new, by name
	dataSources []string // changed or new, by name
}

// appChangeOf compares the app before a save (nil for a new one) with the
// app as saved. A part counts as changed when what it runs or renders
// differs: an action or data source by its script, language or capabilities,
// the page by its stored form.
func appChangeOf(prior *AppSpec, saved AppSpec) appChange {
	var c appChange
	if prior == nil {
		c.page = len(saved.Page) > 0
		for _, a := range saved.Actions {
			c.actions = append(c.actions, a.Name)
		}
		for _, d := range saved.DataSources {
			c.dataSources = append(c.dataSources, d.Name)
		}
		return c
	}
	c.page = !bytes.Equal(prior.Page, saved.Page) || !bytes.Equal(prior.Sections, saved.Sections)
	before := map[string]AppAction{}
	for _, a := range prior.Actions {
		before[a.Name] = a
	}
	for _, a := range saved.Actions {
		if b, ok := before[a.Name]; !ok || b.Script != a.Script || b.Language != a.Language || !slices.Equal(b.Capabilities, a.Capabilities) {
			c.actions = append(c.actions, a.Name)
		}
	}
	beforeDS := map[string]AppDataSource{}
	for _, d := range prior.DataSources {
		beforeDS[d.Name] = d
	}
	for _, d := range saved.DataSources {
		if b, ok := beforeDS[d.Name]; !ok || b.Script != d.Script || b.Language != d.Language || !slices.Equal(b.Capabilities, d.Capabilities) {
			c.dataSources = append(c.dataSources, d.Name)
		}
	}
	return c
}

// summary is the save's last word on what it changed, in terms of what the
// person will and will not see.
func (c appChange) summary(slug string) string {
	if !c.page && len(c.actions) == 0 && len(c.dataSources) == 0 {
		return "\n\nWhat this save changed: nothing the app shows or runs (its page, actions and data sources are as they were)."
	}
	var parts []string
	if c.page {
		parts = append(parts, "the page")
	}
	if len(c.actions) > 0 {
		parts = append(parts, "action"+plural(len(c.actions))+" "+strings.Join(c.actions, ", "))
	}
	if len(c.dataSources) > 0 {
		parts = append(parts, "data source"+plural(len(c.dataSources))+" "+strings.Join(c.dataSources, ", "))
	}
	var b strings.Builder
	b.WriteString("\n\nWhat this save changed: " + strings.Join(parts, "; ") + ".")
	if !c.page {
		b.WriteString(" The page itself is unchanged, so it looks the same as before: tell the user where the change shows")
		switch {
		case len(c.actions) > 0:
			b.WriteString(" (when the changed action runs, from its button), not that the page will look different.")
		default:
			b.WriteString(" (in what the page loads), not that the page will look different.")
		}
	}
	if len(c.actions) > 0 {
		fmt.Fprintf(&b, " The changed action has NOT run since this change: run app_def(action=\"test\", id=%q) (or verify) before telling the user it works. If one cannot be run safely here (it sends something to an outside system), say plainly in your reply that it was not run.", slug)
	}
	return b.String()
}

// appActionsUnrunPrefix opens the finish-check reason for an app whose
// changed actions have not run. test clears a reason that is only this.
const appActionsUnrunPrefix = "an action changed and has not run since: "

// noteActionsUnrun marks app slug not ready until its changed actions run,
// keeping any reason it already stands not ready for.
func (t *chatTurn) noteActionsUnrun(slug string, names []string) {
	sid := t.chatSessionID()
	if t.udb == nil || sid == "" || len(names) == 0 {
		return
	}
	reason := appActionsUnrunPrefix + strings.Join(names, ", ") + " (app_def(action=\"test\") or verify runs it)"
	for _, r := range loadToolVerifications(t.udb, sid) {
		if r.Tool == appLedgerPrefix+slug && !r.Passed && strings.TrimSpace(r.Reason) != "" && !strings.HasPrefix(r.Reason, appActionsUnrunPrefix) {
			reason = r.Reason + "; and " + reason
		}
	}
	t.noteAppStanding(slug, false, reason)
}

// clearActionsUnrun marks app slug ready after a test ran its actions clean,
// when unrun actions were all that held it: a test runs the scripts, not the
// page, so it cannot clear anything else.
func (t *chatTurn) clearActionsUnrun(slug string) {
	sid := t.chatSessionID()
	if t.udb == nil || sid == "" {
		return
	}
	for _, r := range loadToolVerifications(t.udb, sid) {
		if r.Tool == appLedgerPrefix+slug && !r.Passed && strings.HasPrefix(r.Reason, appActionsUnrunPrefix) {
			t.noteAppStanding(slug, true, "")
		}
	}
}
