package customapps

// An app's recent runs, for its owner: the record each live run of a data
// source or action leaves (tools/appscript/runlog.go), on a page of its own
// beside Settings. Builder reads the same record through app_def get; this is
// the owner reading it without asking anyone. When a script's latest run
// failed, the app page says so in its header, where the owner already looks.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// hasScripts is whether an app has anything that leaves a run record.
func hasScripts(spec AppSpec) bool {
	return len(spec.DataSources) > 0 || len(spec.Actions) > 0
}

// runScriptLabel names a run's script the way the app's author does.
func runScriptLabel(r appscript.AppRun) string {
	if r.Kind == "data" {
		return "data source " + r.Name
	}
	return r.Kind + " " + r.Name
}

// runsList is the runs table: one row per kept run, newest first. The
// owner's only: a run's error and log lines are the app's internals.
func (T *CustomApps) runsList(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.NotFound("no such page")
	}
	rows := []map[string]any{}
	for i, r := range appscript.RecentRuns(a.owner, a.spec.Slug) {
		row := map[string]any{
			"id":      strconv.Itoa(i),
			"at":      r.At,
			"script":  runScriptLabel(r),
			"trigger": "opened the page",
			"outcome": "ok",
			"took":    strconv.FormatInt(r.Ms, 10) + " ms",
		}
		if r.Trigger == "schedule" {
			row["trigger"] = "on its schedule"
		}
		if r.Kind == "action" && r.Trigger == "page" {
			row["trigger"] = "pressed"
		}
		if r.Other {
			row["trigger"] = row["trigger"].(string) + ", by someone you shared it with"
		}
		if !r.OK {
			row["outcome"] = "failed"
		}
		if r.Times > 1 {
			row["took"] = strconv.Itoa(r.Times) + " runs, last " + row["took"].(string)
		}
		if r.Error != "" {
			row["error"] = r.Error
		}
		if len(r.Log) > 0 {
			row["log"] = strings.Join(r.Log, "\n")
		}
		if r.Error != "" || len(r.Log) > 0 {
			row["has_detail"] = "1"
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleRunsPage is the app's Recent runs page, its owner's only.
func (T *CustomApps) handleRunsPage(w http.ResponseWriter, r *http.Request, spec AppSpec, owner bool) {
	if !owner {
		http.NotFound(w, r)
		return
	}
	base := T.WebPath() + "/" + spec.Slug
	ui.Page{
		Title:     spec.Name + " runs",
		ShowTitle: true,
		BackURL:   base + "/",
		MaxWidth:  "900px",
		Sections: []ui.Section{{
			Title: "Recent runs",
			Subtitle: "Each time a data source or action of this app ran: when, how it started, and how it ended, newest first. " +
				"Open a run for its error and what its script logged.",
			Detail: "A data source runs when someone opens the page; an action runs when someone presses it or on its schedule. " +
				"Runs that went the same quiet way in a row are counted together. The last 20 runs are kept, plus up to 10 older failures. " +
				"A run started by someone you shared the app with shows how it ended, never what it logged.",
			Body: ui.Table{
				Source:        base + "/_runs/list",
				RowKey:        "id",
				AutoRefreshMS: 10000,
				Columns: []ui.Col{
					{Field: "script", Flex: 2},
					{Field: "outcome", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "ok", Label: "OK", Color: "success"},
						{Value: "failed", Label: "Failed", Color: "danger"},
					}},
					{Field: "at", Format: "reltime", Mute: true},
					{Field: "trigger", Mute: true, Line: 2},
					{Field: "took", Mute: true, Line: 2},
				},
				RowActions: []ui.RowAction{
					ui.ExpandIf("Details", "has_detail", "", ui.RecordView{Pairs: []ui.DisplayPair{
						{Label: "Error", Field: "error", Mono: true, Block: true},
						{Label: "Logged", Field: "log", Mono: true, Block: true},
					}}),
				},
				EmptyText: "Nothing has run yet. A data source runs when someone opens the app; an action when someone presses it or its schedule comes round.",
			},
		}},
	}.ServeHTTP(w, r)
}

// withFailedRunLink adds a header link to the Recent runs page when the
// latest run of any of the app's scripts failed: the owner finds out where
// they already are, without a failure on the page itself to go on.
func withFailedRunLink(page json.RawMessage, base string, runs []appscript.AppRun) json.RawMessage {
	latest := map[string]bool{}
	var failed []string
	for _, r := range runs { // newest first: the first of each script is its latest
		key := r.Kind + "/" + r.Name
		if latest[key] {
			continue
		}
		latest[key] = true
		if !r.OK {
			failed = append(failed, runScriptLabel(r))
		}
	}
	switch len(failed) {
	case 0:
		return page
	case 1:
		return withNavLink(page, "⚠ "+capitalize(failed[0])+" failed: see its runs", base+"/_runs")
	default:
		return withNavLink(page, "⚠ "+strconv.Itoa(len(failed))+" scripts failed: see their runs", base+"/_runs")
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
