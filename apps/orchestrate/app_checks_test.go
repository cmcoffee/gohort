package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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

// Page checks are refused until they can run, rather than kept and skipped.
func TestPageChecksAreRefusedUntilTheyRun(t *testing.T) {
	turn := checksTestApp(t)
	page := map[string]any{"name": "adds a row", "steps": []any{map[string]any{"see": "Total"}}}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "hours", "checks": []any{page}}, true); err == nil || !strings.Contains(err.Error(), "page checks do not run yet") {
		t.Fatalf("a page check was kept: %v", err)
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
