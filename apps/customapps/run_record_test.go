package customapps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// The host's live runs leave their record on the app: a data source as the
// page asked for it, an action with its whole ending (a reply the host refuses
// is the action failing), and whatever each one logged.
func TestLiveRunsLeaveARecord(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", RecordKey: "id"}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		if j.OnLog == nil {
			t.Fatalf("%s %q ran with nothing hearing its log()", j.Kind, j.Name)
		}
		j.OnLog("info", "ran "+j.Name)
		switch j.Name {
		case "counts":
			return "", errors.New("Traceback: ZeroDivisionError")
		case "bad_reply":
			return "not json at all {", nil
		}
		return `{"message":"ok"}`, nil
	}

	if _, err := runDataSource("alice", T.recordBase(spec, "alice"), spec, AppDataSource{Name: "counts"}, map[string]any{}, "alice"); err == nil {
		t.Fatal("the stand-in data source should fail")
	}
	if _, err := runActionAndPersist("alice", T.recordBase(spec, "alice"), T.recordBase(spec, "alice"), spec, AppAction{Name: "bad_reply"}, map[string]any{}, "alice", scriptRun{Interactive: true}); err == nil {
		t.Fatal("an action whose reply is not an object should fail")
	}
	if _, err := runActionAndPersist("alice", T.recordBase(spec, "alice"), T.recordBase(spec, "alice"), spec, AppAction{Name: "nightly"}, map[string]any{}, "alice", scriptRun{}); err != nil {
		t.Fatal(err)
	}

	runs := appscript.RecentRuns("alice", "tally")
	if len(runs) != 3 {
		t.Fatalf("three runs should be recorded, got %d: %+v", len(runs), runs)
	}
	nightly, reply, counts := runs[0], runs[1], runs[2]
	if counts.Kind != "data" || counts.OK || !strings.Contains(counts.Error, "ZeroDivisionError") || counts.Trigger != "page" {
		t.Errorf("a failed data source: %+v", counts)
	}
	if reply.Kind != "action" || reply.OK || !strings.Contains(reply.Error, "must print a JSON object") {
		t.Errorf("a refused reply is recorded as the action failing: %+v", reply)
	}
	if !nightly.OK || nightly.Trigger != "schedule" || len(nightly.Log) != 1 || nightly.Log[0] != "ran nightly" {
		t.Errorf("an unattended run, with its log line: %+v", nightly)
	}
}

// What failed around an app's scripts in real use is on its record too: a
// data source that ran but printed no JSON value, an error its page hit in a
// browser (a shared user's marked as someone else's, a preview's and a
// check's not kept, a flood held to an hour's allowance), and an ask its
// agent could not answer. None of them raises the page's failed-script link,
// which only a script's latest run decides.
func TestWhatFailedAroundTheScriptsIsRecorded(t *testing.T) {
	T := sharingTestApp(t)
	spec := SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", RecordKey: "id", AgentID: "a1",
		DataSources: []AppDataSource{{Name: "counts"}}})
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) { return "Done, no JSON here.", nil }
	if _, err := runDataSource("alice", T.recordBase(spec, "alice"), spec, AppDataSource{Name: "counts"}, map[string]any{}, "alice"); err != nil {
		t.Fatal(err)
	}
	if runs := appscript.RecentRuns("alice", "tally"); len(runs) != 1 || runs[0].OK || !strings.Contains(runs[0].Error, "JSON") {
		t.Fatalf("a data source that printed no JSON: %+v", runs)
	}
	appscript.ForgetRuns("alice", "tally")

	// The page tells: window.app hears what the page does not catch and posts
	// it to a path the frame may reach.
	if !strings.Contains(appPageHelper, `m==="Script error."`) {
		t.Error("window.app tells the browser's masked \"Script error.\", which says nothing")
	}
	if !strings.Contains(appPageHelper, `addEventListener("error"`) || !strings.Contains(appPageHelper, `addEventListener("unhandledrejection"`) || !strings.Contains(appPageHelper, `fetch("problem"`) {
		t.Error("window.app does not tell the page's errors")
	}
	if !slices.Contains(appOwnPaths, "problem") {
		t.Error("the frame may not reach the path its errors are told to")
	}
	post := func(user, body string) int {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodPost, "/apps/tally/problem", strings.NewReader(body))).Code
	}
	if code := post("alice", `{"error":"TypeError: rows is undefined","where":"line 12"}`); code != http.StatusOK {
		t.Fatalf("a page error: %d", code)
	}
	post("bob", `{"error":"TypeError: rows is undefined","where":"line 12"}`)
	runs := appscript.RecentRuns("alice", "tally")
	if len(runs) != 2 || runs[1].Kind != "page" || runs[1].Other || runs[1].Error != "TypeError: rows is undefined (line 12)" || !runs[0].Other {
		t.Fatalf("page errors, the owner's and a shared user's: %+v", runs)
	}
	if w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/tally/problem", nil)); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET filed a problem: %d", w.Code)
	}
	for i := 0; i < pageProblemsPerHour+5; i++ {
		post("carol", fmt.Sprintf(`{"error":"flood %d"}`, i))
	}
	post("carol", `{"error":"one past the hour's allowance"}`)
	for _, r := range appscript.RecentRuns("alice", "tally") {
		if strings.Contains(r.Error, "past the hour's allowance") {
			t.Error("a page error past the hour's allowance was kept")
		}
	}
	if !pageProblemAllowed("alice/tally/dave") {
		t.Error("one person's flood used up another's allowance")
	}
	appscript.ForgetRuns("alice", "tally")

	// In a preview, even one on the real records, the page's errors are the
	// preview's own.
	working := spec
	working.Name = "Tally v2"
	prevHost := hostApp
	hostApp = T
	t.Cleanup(func() { hostApp = prevHost })
	tok, err := startAppPreview("alice", working, true)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/apps/tally/problem", strings.NewReader(`{"error":"from the preview"}`))
	r.AddCookie(&http.Cookie{Name: previewCookie, Value: tok})
	as(t, T, "alice", r)
	if runs := appscript.RecentRuns("alice", "tally"); len(runs) != 0 {
		t.Errorf("a preview's page error was filed on the live app: %+v", runs)
	}

	// An ask its agent could not answer.
	prevAsk := appAgentAsk
	t.Cleanup(func() { appAgentAsk = prevAsk })
	appAgentAsk = func(context.Context, string, string, string, string, bool) (string, float64, error) {
		return "", 0, errors.New("the model is not reachable")
	}
	appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodPost, "/apps/tally/ask", strings.NewReader(`{"prompt":"hi"}`)))
	runs = appscript.RecentRuns("alice", "tally")
	if len(runs) != 1 || runs[0].Kind != "ask" || !strings.Contains(runs[0].Error, "not reachable") {
		t.Fatalf("a failed ask: %+v", runs)
	}
	if got := withFailedRunLink(json.RawMessage(`{"nav":[]}`), "/apps/tally", runs); strings.Contains(string(got), "failed") {
		t.Errorf("a failed ask raised the failed-script link: %s", got)
	}
}
