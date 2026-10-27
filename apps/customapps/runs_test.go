package customapps

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// recordRun files one live run of an app's script, the way the host's run
// sites do.
func recordRun(owner, slug, kind, name, caller string, interactive bool, fail error, lines ...string) {
	j := appscript.Job{Owner: owner, Slug: slug, Kind: kind, Name: name, Caller: caller, Interactive: interactive}
	trace := appscript.Track(&j)
	for _, l := range lines {
		j.OnLog("info", l)
	}
	trace.Done(fail)
}

// The owner reads their app's runs on a page of its own: what ran, how it
// started, how it ended, and behind Details the error and what it logged.
// Somebody the app is shared with reaches neither the page nor its list.
func TestTheOwnerReadsTheirAppsRuns(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "wx", Name: "Weather", Owner: "alice", RecordKey: "id",
		Page:        json.RawMessage(`{"title":"Weather","sections":[]}`),
		DataSources: []AppDataSource{{Name: "forecast", Script: "x"}},
		Actions:     []AppAction{{Name: "refresh", Script: "x"}}}
	recordRun("alice", "wx", "data", "forecast", "alice", true, errors.New("KeyError: 'temp'"), "got 3 cities")
	recordRun("alice", "wx", "action", "refresh", "alice", false, nil)
	recordRun("alice", "wx", "action", "refresh", "bob", true, nil, "bob's private note")

	w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/wx/_runs/list", nil))
	if w.Code != 200 {
		t.Fatalf("the owner's list: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 3 {
		t.Fatalf("three runs, newest first: %v", rows)
	}
	bob, sched, forecast := rows[0], rows[1], rows[2]
	if forecast["script"] != "data source forecast" || forecast["outcome"] != "failed" || forecast["trigger"] != "opened the page" ||
		forecast["error"] != "KeyError: 'temp'" || forecast["log"] != "got 3 cities" || forecast["has_detail"] != "1" {
		t.Errorf("a failed data source: %v", forecast)
	}
	if sched["script"] != "action refresh" || sched["outcome"] != "ok" || sched["trigger"] != "on its schedule" || sched["has_detail"] != nil {
		t.Errorf("a quiet scheduled action has nothing behind Details: %v", sched)
	}
	if bob["trigger"] != "pressed, by someone you shared it with" || bob["log"] != nil {
		t.Errorf("another person's run shows how it ended, not what it logged: %v", bob)
	}

	page := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/wx/_runs", nil))
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Recent runs") {
		t.Errorf("the owner's page: %d", page.Code)
	}
	if w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/wx/_runs/list", nil)); w.Code != http.StatusNotFound {
		t.Errorf("somebody it is shared with must not read its runs: %d %s", w.Code, w.Body.String())
	}
	if w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/wx/_runs", nil)); w.Code != http.StatusNotFound {
		t.Errorf("nor open the page: %d", w.Code)
	}

	// The app page's header points the owner at the failure; not anyone else.
	if w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/wx/", nil)); !strings.Contains(w.Body.String(), "Data source forecast failed: see its runs") {
		t.Errorf("the owner's app page should flag the failed data source: %.300s", w.Body.String())
	}
	if w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/wx/", nil)); w.Code != 200 || strings.Contains(w.Body.String(), "see its runs") {
		t.Errorf("somebody it is shared with gets the page without the link: %d", w.Code)
	}

	// My Apps offers Runs on an app with scripts.
	w = as(t, T, "alice", httptest.NewRequest(http.MethodGet, "/apps/_apps", nil))
	if !strings.Contains(w.Body.String(), `"has_runs":"1"`) {
		t.Errorf("My Apps should offer Runs for an app with scripts: %s", w.Body.String())
	}
}

// The app page's header says so when a script's latest run failed, and stops
// saying so once it runs clean.
func TestTheAppPageFlagsAFailedLatestRun(t *testing.T) {
	link := func(runs []appscript.AppRun) string {
		out := withFailedRunLink(json.RawMessage(`{"nav":[]}`), "/apps/wx", runs)
		var m struct {
			Nav []struct{ Label, URL string } `json:"nav"`
		}
		json.Unmarshal(out, &m)
		if len(m.Nav) == 0 {
			return ""
		}
		return m.Nav[0].Label + " -> " + m.Nav[0].URL
	}
	failed := appscript.AppRun{Kind: "data", Name: "forecast", OK: false}
	ok := appscript.AppRun{Kind: "data", Name: "forecast", OK: true}
	if got := link([]appscript.AppRun{failed}); got != "⚠ Data source forecast failed: see its runs -> /apps/wx/_runs" {
		t.Errorf("one failing script: %q", got)
	}
	if got := link([]appscript.AppRun{ok, failed}); got != "" {
		t.Errorf("a script that ran clean since it failed is not flagged: %q", got)
	}
	other := appscript.AppRun{Kind: "action", Name: "refresh", OK: false}
	if got := link([]appscript.AppRun{failed, other}); got != "⚠ 2 scripts failed: see their runs -> /apps/wx/_runs" {
		t.Errorf("two failing scripts: %q", got)
	}
	if got := link(nil); got != "" {
		t.Errorf("no runs, no link: %q", got)
	}
}
