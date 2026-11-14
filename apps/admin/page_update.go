package admin

// Software Update (docs/design/software-update.md): what is running, where
// releases come from, and the check, update and rollback an administrator
// presses. Nothing checks on its own: the section asks the dist site only
// when Check is pressed.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/selfupdate"
	"github.com/cmcoffee/gohort/core/ui"
)

// updater is the program's updater; a variable so tests can give the page
// one of their own.
var updater = func() *selfupdate.Updater { return selfupdate.Default }

func (a *AdminApp) updateSections() []ui.Section {
	return []ui.Section{{
		Title:    "Software Update",
		Subtitle: "Check for a newer release, then install it here. gohort restarts onto it, down for a few seconds.",
		Detail: "Check asks the dist site for its newest release and checks the release list's signature against the keys built into gohort. Nothing checks on its own.\n\n" +
			"Update downloads this platform's binary, checks it against the signed list, runs it once to see it starts, then swaps it in and restarts. Anything that fails before the swap leaves gohort as it was. " +
			"The version it replaces is kept beside it as gohort.prev, and Roll back puts it back. A rollback restores the program, not the data: whatever the newer version wrote stays.\n\n" +
			"The dist site is set by [update] url in gohort.ini, and [update] key replaces the built-in keys, for a deployment that publishes its own builds.",
		Body: ui.Stack{Children: []ui.Component{
			ui.DisplayPanel{
				Source: "api/update/status",
				// After an update the server restarts; refreshing shows the
				// new version once it is back.
				AutoRefreshMS: 5000,
				Pairs: []ui.DisplayPair{
					{Label: "Running", Field: "running"},
					{Label: "Updates from", Field: "source", Mono: true},
					{Label: "Release keys", Field: "keys"},
					{Label: "Last check", Field: "last_check"},
					{Label: "Kept for rollback", Field: "previous"},
					{Label: "Updates here", Field: "updates_here"},
				},
			},
			ui.ActionList{
				Source:         "api/update/actions",
				PostTo:         "api/update?do={Key}",
				HistoryField:   "History",
				ProgressSource: "api/update/progress?key={Key}",
				EmptyText:      "This install is not offered updates: see Updates here.",
				ReloadSelf:     true,
				Invalidate:     []string{"api/update/status"},
			},
		}},
	}}
}

func (a *AdminApp) registerUpdateRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/update/status", jsonapi.Get(a.db, a.handleUpdateStatus, a.gate()))
	sub.HandleFunc("/api/update/actions", jsonapi.Get(a.db, a.handleUpdateActions, a.gate()))
	sub.HandleFunc("/api/update/progress", jsonapi.Get(a.db, a.handleUpdateProgress, a.gate()))
	sub.HandleFunc("/api/update", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleUpdateRun, a.gate()),
	}.Serve)
}

func (a *AdminApp) handleUpdateStatus(c *jsonapi.Call) (any, error) {
	u := updater()
	if u == nil {
		return map[string]string{"running": AppVersion, "updates_here": "No updater in this program."}, nil
	}
	st := u.Status()
	out := map[string]string{
		"running":      st.Version + " (" + st.Platform + ")",
		"source":       st.Source,
		"keys":         keysLine(st.Keys),
		"last_check":   checkLine(st),
		"previous":     "Nothing yet: the version an update replaces is kept here.",
		"updates_here": "Offered.",
	}
	if st.HasPrevious {
		out["previous"] = orUnknown(st.Previous)
	}
	switch {
	case st.Blocked != "":
		out["updates_here"] = "Not offered: " + st.Blocked
	case st.Installed != "":
		out["updates_here"] = st.Installed + " is installed and waiting for gohort to restart."
	}
	return out, nil
}

func keysLine(n int) string {
	switch n {
	case 0:
		return "None: no release can be checked, so none is offered."
	case 1:
		return "1 trusted key."
	}
	return fmt.Sprintf("%d trusted keys.", n)
}

func checkLine(st selfupdate.Status) string {
	if st.Checked.IsZero() {
		return "Not checked since gohort started."
	}
	ago := time.Since(st.Checked).Round(time.Minute)
	when := "just now"
	if ago >= time.Minute {
		when = humanAgo(ago) + " ago"
	}
	switch {
	case st.CheckError != "":
		return "Checked " + when + ": " + st.CheckError
	case st.Available:
		return fmt.Sprintf("Checked %s: %s is available.", when, st.Latest)
	}
	return fmt.Sprintf("Checked %s: up to date (newest release %s).", when, st.Latest)
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minute(s)", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hour(s)", int(d.Hours()))
	}
	return fmt.Sprintf("%d day(s)", int(d.Hours()/24))
}

func orUnknown(v string) string {
	if v == "" {
		return "the previous version (its number was not recorded)"
	}
	return v
}

// handleUpdateActions is the buttons this install has now: Check always, and
// Update and Roll back when there is something for them to do.
func (a *AdminApp) handleUpdateActions(c *jsonapi.Call) (any, error) {
	u := updater()
	if u == nil {
		return []map[string]string{}, nil
	}
	st := u.Status()
	if st.Blocked != "" {
		return []map[string]string{}, nil
	}
	rows := []map[string]string{{
		"Key": selfupdate.ActionCheck, "Label": "Check for update", "button": "Check",
		"Desc":    "Asks " + st.Source + " for its newest release.",
		"History": checkLine(st),
	}}
	if st.Available {
		rows = append(rows, map[string]string{
			"Key": selfupdate.ActionUpdate, "Label": "Update to " + st.Latest, "button": "Update",
			"Desc":    "Downloads it, checks it, runs it once, then swaps it in and restarts.",
			"confirm": fmt.Sprintf("Update to %s? gohort restarts onto it and is down for a few seconds.", st.Latest),
		})
	}
	if st.HasPrevious && st.Installed == "" {
		was := orUnknown(st.Previous)
		rows = append(rows, map[string]string{
			"Key": selfupdate.ActionRollback, "Label": "Roll back to " + was, "button": "Roll back",
			"Desc":    "Puts the version before this one back and restarts. The data stays as the newer version left it.",
			"confirm": fmt.Sprintf("Roll back to %s? gohort restarts onto it and is down for a few seconds.", was),
		})
	}
	return rows, nil
}

func (a *AdminApp) handleUpdateProgress(c *jsonapi.Call) (any, error) {
	u := updater()
	if u == nil {
		return map[string]string{}, nil
	}
	p, out := u.Progress(c.R.URL.Query().Get("key"))
	return map[string]string{"progress": p, "outcome": out}, nil
}

// handleUpdateRun runs one action to its end. It does not run on the
// request's context: closing the page must not stop a download halfway.
func (a *AdminApp) handleUpdateRun(c *jsonapi.Call, _ struct{}) (any, error) {
	u := updater()
	if u == nil {
		return nil, jsonapi.NotFound("this program has no updater")
	}
	ctx := context.WithoutCancel(c.R.Context())
	from := u.Version
	var (
		msg string
		err error
	)
	switch do := c.R.URL.Query().Get("do"); do {
	case selfupdate.ActionCheck:
		msg, err = u.Check(ctx)
	case selfupdate.ActionUpdate:
		msg, err = u.Update(ctx)
		if err == nil {
			adminChange(c.R, activity.Update, "updated gohort from %s to %s", from, u.Status().Installed)
		}
	case selfupdate.ActionRollback:
		msg, err = u.Rollback(ctx)
		if err == nil {
			adminChange(c.R, activity.Update, "rolled gohort back from %s to %s", from, u.Status().Installed)
		}
	default:
		return nil, jsonapi.BadRequest("unknown update action")
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]string{"message": msg}, nil
}
