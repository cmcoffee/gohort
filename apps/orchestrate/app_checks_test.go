package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/tools/appscript"
)

const totalScript = "import os, json\nrecs = json.loads(os.environ.get('records', '[]'))\nprint(json.dumps({'total': sum(r.get('hours', 0) for r in recs)}))\n"

func totalCheck(want int) map[string]any {
	return map[string]any{"name": "totals the hours", "script": "total",
		"records": []any{map[string]any{"hours": 3}, map[string]any{"hours": 4}},
		"expect":  []any{map[string]any{"path": "total", "equals": want}}}
}

// checksTestApp makes an app whose data source totals its records' hours,
// with a check that it does, or skips where no script can run.
func checksTestApp(t *testing.T) *chatTurn {
	t.Helper()
	turn, _ := folderTestTurn(t)
	// A sandboxed script runs in its owner's workspace folder, and the
	// sandbox mounts a fresh /tmp over the host's, so the workspaces live
	// outside it, as the flows tests' do.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache folder")
	}
	ws, err := os.MkdirTemp(cache, "gohort-app-checks-")
	if err != nil {
		t.Skip("no cache folder to run scripts from")
	}
	prevWS := WorkspacesDir()
	SetWorkspacesDir(ws)
	t.Cleanup(func() { SetWorkspacesDir(prevWS); os.RemoveAll(ws) })
	if err := os.MkdirAll(filepath.Join(ws, turn.user), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := turn.appDefCreateOrUpdate(map[string]any{
		"name": "Hours",
		"sections": []any{
			map[string]any{"id": "entry", "kind": "form", "fields": []any{map[string]any{"field": "hours"}}},
			map[string]any{"id": "sum", "kind": "display", "source_script": "total", "pairs": []any{map[string]any{"label": "Total", "field": "total"}}},
		},
		"data_sources": []any{map[string]any{"name": "total", "script": totalScript}},
		"checks":       []any{totalCheck(7)},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "could not run") {
		t.Skipf("no script can run here: %s", out)
	}
	if !strings.Contains(out, `PASS "totals the hours"`) {
		t.Fatalf("the check did not pass on create:\n%s", out)
	}
	return turn
}

func storedScript(t *testing.T, owner string) string {
	spec, _ := LoadAppSpec(owner, "hours")
	if len(spec.DataSources) == 0 {
		return ""
	}
	return spec.DataSources[0].Script
}

// A change that breaks a check that passed is refused, and the app keeps
// serving what it was. Changing the check with the feature, on purpose, is
// not refused.
func TestASaveThatBreaksAPassingCheckIsRefused(t *testing.T) {
	turn := checksTestApp(t)
	doubled := strings.Replace(totalScript, "sum(", "2 * sum(", 1)
	_, err := turn.appDefCreateOrUpdate(map[string]any{"id": "hours",
		"data_sources": []any{map[string]any{"name": "total", "script": doubled}}}, true)
	if err == nil || !strings.Contains(err.Error(), "breaks a check") || !strings.Contains(err.Error(), "totals the hours") {
		t.Fatalf("a change that broke the check was not refused by name: %v", err)
	}
	if storedScript(t, "u") != totalScript {
		t.Fatal("the refused change was saved")
	}
	// The feature changed on purpose, and the check with it.
	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "hours",
		"data_sources": []any{map[string]any{"name": "total", "script": doubled}},
		"checks":       []any{totalCheck(14)}}, true)
	if err != nil || !strings.Contains(out, `PASS "totals the hours"`) {
		t.Fatalf("a feature changed with its check was refused: %v\n%s", err, out)
	}
}

// A new check that fails is reported, not refused: it describes something not
// built yet. A check naming a script the app no longer has refuses the save
// that dropped it.
func TestANewFailingCheckIsReportedAndADroppedScriptIsRefused(t *testing.T) {
	turn := checksTestApp(t)
	wrong := totalCheck(99)
	wrong["name"] = "counts to ninety-nine"
	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "hours", "checks": []any{totalCheck(7), wrong}}, true)
	if err != nil || !strings.Contains(out, `FAIL "counts to ninety-nine"`) || !strings.Contains(out, `PASS "totals the hours"`) {
		t.Fatalf("a new failing check was not reported beside the passing one: %v\n%s", err, out)
	}
	_, err = turn.appDefCreateOrUpdate(map[string]any{"id": "hours", "data_sources": []any{}, "confirm_rewrite": true}, true)
	if err == nil || !strings.Contains(err.Error(), "is not one of the app's data sources or actions") {
		t.Fatalf("dropping a script a check runs was not refused naming it: %v", err)
	}
}

// app_def test runs the checks and reports each.
func TestAppDefTestRunsTheChecks(t *testing.T) {
	turn := checksTestApp(t)
	out, err := turn.appDefTest(map[string]any{"id": "hours"})
	if err != nil || !strings.Contains(out, `PASS "totals the hours"`) || !strings.Contains(out, "1 passed, 0 failed") {
		t.Fatalf("test did not report the check: %v\n%s", err, out)
	}
}

// The checks travel in the app's folder as checks.json, out and back.
func TestChecksTravelInTheAppFolder(t *testing.T) {
	spec := AppSpec{Slug: "hours", Name: "Hours", Owner: "u", Sections: json.RawMessage(`[{"id":"s","kind":"empty"}]`)}
	if err := json.Unmarshal([]byte(`[{"name":"totals","script":"total","expect":[{"path":"total","equals":7}]}]`), &spec.Checks); err != nil {
		t.Fatal(err)
	}
	turn, ws := folderTestTurn(t)
	dir := ws + "/hours.app"
	if _, err := writeAppFolder(dir, spec, nil, "/apps/hours/"); err != nil {
		t.Fatal(err)
	}
	args, _, err := turn.appFolderRead(dir, "hours.app")
	if err != nil {
		t.Fatal(err)
	}
	got, err := appChecksArg(args["checks"])
	if err != nil || len(got) != 1 || got[0].Name != "totals" {
		t.Fatalf("checks.json did not come back: %+v %v", got, err)
	}
}

// A check this save changes and that fails is reported, not refused: the
// feature changed on purpose and the check describes where it is going.
func TestAChangedFailingCheckIsReportedNotRefused(t *testing.T) {
	turn := checksTestApp(t)
	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "hours", "checks": []any{totalCheck(15)}}, true)
	if err != nil || !strings.Contains(out, `FAIL "totals the hours"`) {
		t.Fatalf("a changed check that fails should be reported, not refused: %v\n%s", err, out)
	}
}

// A check's run is held to what a check may do: a script that runs an agent
// is told it is in a check, rather than running it.
func TestACheckRunIsHeldToTheCheckRules(t *testing.T) {
	turn := checksTestApp(t)
	agentScript := "import json\nfrom gohort import run_agent\ntry:\n    run_agent('hi')\n    print(json.dumps({'r': 'ran'}))\nexcept Exception as e:\n    print(json.dumps({'r': str(e)}))\n"
	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "hours",
		"data_sources": []any{map[string]any{"name": "total", "script": totalScript},
			map[string]any{"name": "helper", "script": agentScript, "capabilities": []any{"run_agent"}}},
		"checks": []any{totalCheck(7), map[string]any{"name": "is held to the check", "script": "helper",
			"expect": []any{map[string]any{"path": "r", "contains": "not in a check"}}}}}, true)
	if err != nil || !strings.Contains(out, `PASS "is held to the check"`) {
		t.Fatalf("a check run was not held to the check rules: %v\n%s", err, out)
	}
}

// fakeBrowser stands in for the browser: each load must carry a live page
// check's scratch token, and a see step holds when the app as stored now has
// that text in its sections.
func fakeBrowser(t *testing.T, owner, slug string) *int {
	t.Helper()
	prevB, prevAddr := BrowserCheckPage, WebListenAddr
	t.Cleanup(func() { BrowserCheckPage, WebListenAddr = prevB, prevAddr })
	WebListenAddr = "127.0.0.1:9"
	loads := 0
	BrowserCheckPage = func(target string, cookies []PageCheckCookie, probe string, _ bool) (*PageCheckReport, error) {
		loads++
		scratch := false
		for _, c := range cookies {
			if c.Name == appscript.CheckScratchCookie {
				_, scratch = appscript.CheckScratchFor(c.Value)
			}
		}
		if !scratch {
			t.Errorf("a page check loaded %s without a live scratch store", target)
		}
		steps, ok := appcheck.DecodeStepsProbe(probe)
		if !ok {
			t.Fatalf("the probe is not a page check's steps: %q", probe)
		}
		spec, _ := LoadAppSpec(owner, slug)
		res := appcheck.StepsResult{OK: true}
		for i, st := range steps {
			if st.See != "" && !strings.Contains(string(spec.Sections), st.See) {
				res = appcheck.StepsResult{Step: i + 1, Saw: "the page shows: " + string(spec.Sections)}
				break
			}
		}
		b, _ := json.Marshal(res)
		return &PageCheckReport{ProbeJSON: string(b)}, nil
	}
	return &loads
}

func seeCheck(name, text string) map[string]any {
	return map[string]any{"name": name, "steps": []any{map[string]any{"see": text}}}
}

func pageApp(t *testing.T, title string) *chatTurn {
	t.Helper()
	turn, _ := folderTestTurn(t)
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Board",
		"sections": []any{map[string]any{"id": "list", "kind": "table", "title": title, "empty_text": "None yet.", "columns": []any{map[string]any{"field": "item"}}}},
		"checks":   []any{seeCheck("shows the list", "Shopping")}}, false); err != nil {
		t.Fatal(err)
	}
	return turn
}

func retitle(t *testing.T, turn *chatTurn, title string, checks ...map[string]any) (string, error) {
	t.Helper()
	args := map[string]any{"id": "board", "sections": []any{map[string]any{"id": "list", "kind": "table", "title": title, "empty_text": "None yet.", "columns": []any{map[string]any{"field": "item"}}}}}
	if len(checks) > 0 {
		var list []any
		for _, c := range checks {
			list = append(list, c)
		}
		args["checks"] = list
	}
	return turn.appDefCreateOrUpdate(args, true)
}

// A page check runs on the saved version as served. A change that breaks
// one that passed puts the previous version back to serve and is refused; a
// check changed with its feature is not.
func TestAPageCheckThisSaveBreaksRollsItBack(t *testing.T) {
	loads := fakeBrowser(t, "u", "board")
	turn := pageApp(t, "Shopping")
	if *loads == 0 {
		t.Fatal("the page check did not run on create")
	}
	_, err := retitle(t, turn, "Groceries")
	if err == nil || !strings.Contains(err.Error(), "ROLLED BACK") || !strings.Contains(err.Error(), "shows the list") {
		t.Fatalf("a change that broke a passing page check was not rolled back: %v", err)
	}
	if spec, _ := LoadAppSpec("u", "board"); !strings.Contains(string(spec.Sections), "Shopping") {
		t.Fatalf("the previous version is not serving after the roll back: %s", spec.Sections)
	}
	out, err := retitle(t, turn, "Groceries", seeCheck("shows the list", "Groceries"))
	if err != nil || !strings.Contains(out, `PASS "shows the list"`) {
		t.Fatalf("a feature changed with its page check was refused: %v\n%s", err, out)
	}
	if spec, _ := LoadAppSpec("u", "board"); !strings.Contains(string(spec.Sections), "Groceries") {
		t.Errorf("the accepted change is not serving: %s", spec.Sections)
	}
}

// A page check already failing, or new and failing, is reported and the save
// stands; with no browser a page check does not run and blocks nothing.
func TestAFailingPageCheckThatWasFailingDoesNotBlock(t *testing.T) {
	fakeBrowser(t, "u", "board")
	turn := pageApp(t, "Shopping")
	out, err := retitle(t, turn, "Shopping", seeCheck("shows the list", "Shopping"), seeCheck("shows the total", "Total"))
	if err != nil || !strings.Contains(out, `FAIL "shows the total"`) {
		t.Fatalf("a new failing page check was not reported: %v\n%s", err, out)
	}
	out, err = retitle(t, turn, "Shopping list", seeCheck("shows the list", "Shopping"), seeCheck("shows the total", "Total"))
	if err != nil || !strings.Contains(out, `FAIL "shows the total"`) {
		t.Fatalf("a page check that was already failing blocked the save: %v\n%s", err, out)
	}
	if spec, _ := LoadAppSpec("u", "board"); !strings.Contains(string(spec.Sections), "Shopping list") {
		t.Fatalf("the save stood in name only: the previous version is serving: %s", spec.Sections)
	}
	BrowserCheckPage = nil
	out, err = retitle(t, turn, "Shopping list 2", seeCheck("shows the list", "Shopping"), seeCheck("shows the total", "Total"))
	if err != nil || !strings.Contains(out, "not run") {
		t.Fatalf("with no browser a page check blocked the save or said nothing: %v\n%s", err, out)
	}
}
