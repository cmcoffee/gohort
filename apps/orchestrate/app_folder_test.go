package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func folderTestTurn(t *testing.T) (*chatTurn, string) {
	t.Helper()
	prevRoot, prevAssets, prevWS := RootDB, AppAssetsDir(), WorkspacesDir()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { RootDB = prevRoot; SetAppAssetsDir(prevAssets); SetWorkspacesDir(prevWS) })
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "builder"}}
	ws, _, _ := turn.turnWorkspace()
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	return turn, ws
}

// An app goes out as a folder and comes back from it: the page, each script,
// the settings and the notes survive the round trip, and a change made to one
// file in the folder is what the published app carries.
func TestAnAppRoundTripsThroughItsFolder(t *testing.T) {
	turn, ws := folderTestTurn(t)
	page := "<!DOCTYPE html><html><body><h1>Wx</h1><script>app.data('now').then(function(d){});</script></body></html>"
	_, err := turn.appDefCreateOrUpdate(map[string]any{
		"name":         "Wx",
		"notes":        "Plan: a forecast page.",
		"sections":     []any{map[string]any{"id": "page", "kind": "html", "html": page}},
		"data_sources": []any{map[string]any{"name": "now", "script": "print('{}')", "capabilities": []any{"fetch"}}},
		"actions":      []any{map[string]any{"name": "save", "script": "print('{}')", "label": "Save"}},
		"settings":     []any{map[string]any{"name": "units", "type": "choice", "default": "F", "options": []any{"F", "C"}}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := turn.appDefCheckout(map[string]any{"id": "wx"})
	if err != nil || !strings.Contains(out, "wx.app") {
		t.Fatalf("checkout: %q %v", out, err)
	}
	for _, f := range []string{"app.json", "page.html", "data/now.py", "actions/save.py", "NOTES.md"} {
		if _, err := os.Stat(filepath.Join(ws, "wx.app", f)); err != nil {
			t.Fatalf("checkout did not write %s", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "wx.app", "page.html")); string(b) != page {
		t.Fatalf("page.html = %q", b)
	}
	// Change one file, as workspace edit would, and publish.
	os.WriteFile(filepath.Join(ws, "wx.app", "data", "now.py"), []byte("print('{\"t\": 1}')"), 0o644)
	if _, err := turn.appDefPublish(map[string]any{"dir": "wx.app"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	spec, _ := LoadAppSpec("u", "wx")
	if len(spec.DataSources) != 1 || spec.DataSources[0].Script != "print('{\"t\": 1}')" || len(spec.DataSources[0].Capabilities) != 1 {
		t.Fatalf("data sources after publish: %+v", spec.DataSources)
	}
	if len(spec.Actions) != 1 || spec.Actions[0].Label != "Save" || len(spec.Settings) != 1 || spec.Settings[0].Default != "F" {
		t.Fatalf("actions %+v settings %+v", spec.Actions, spec.Settings)
	}
	var secs []map[string]any
	if json.Unmarshal(spec.Sections, &secs) != nil || len(secs) != 1 || secs[0]["html"] != page || secs[0]["html_file"] != nil {
		t.Fatalf("sections after publish: %s", spec.Sections)
	}
	if spec.Notes != "Plan: a forecast page." {
		t.Fatalf("notes = %q", spec.Notes)
	}
	if _, err := turn.appDefCheckout(map[string]any{"id": "wx"}); err == nil {
		t.Fatal("checkout over an existing folder without overwrite")
	}
}

// A new app starts as a folder whose page is already wired to window.app, and
// publishing it creates the app.
func TestANewAppStartsAsAFolder(t *testing.T) {
	turn, ws := folderTestTurn(t)
	if _, err := turn.appDefCheckout(map[string]any{"name": "Run Log"}); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(ws, "run-log.app", "page.html"))
	if err != nil || !strings.Contains(string(page), "app.onChange") || !strings.Contains(string(page), "<title>Run Log</title>") {
		t.Fatalf("starter page: %v\n%s", err, page)
	}
	if _, err := turn.appDefPublish(map[string]any{"dir": "run-log.app"}); err != nil {
		t.Fatalf("publish a new folder: %v", err)
	}
	if _, ok := LoadAppSpec("u", "run-log"); !ok {
		t.Fatal("publishing a new folder did not create the app")
	}
	if _, err := turn.appDefRun(map[string]any{"dir": "run-log.app", "file": "data/missing.py"}); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("running an unlisted file: %v", err)
	}
	if p := turn.saveAppScreenshot("run-log", []byte("\xff\xd8\xff")); p != "run-log.app/preview.jpg" {
		t.Fatalf("screenshot saved at %q", p)
	}
}

// run executes one backend file from the folder against a sample, with the
// script checks, and publishes nothing. Skips where the sandbox cannot run.
func TestRunExecutesOneFolderFile(t *testing.T) {
	turn, ws := folderTestTurn(t)
	if _, err := turn.appDefCheckout(map[string]any{"name": "Wx"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws, "wx.app")
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "data", "now.py"), []byte("import os, json\nrecs = json.loads(os.environ.get('records','[]'))\nprint(json.dumps({'city': recs[-1]['city'] if recs else 'none', 'temp': 71}))\n"), 0o644)
	manifest := `{"name":"Wx","slug":"wx","sections":[{"id":"page","kind":"html","html_file":"page.html"}],"data_sources":[{"name":"now","file":"data/now.py"}]}`
	os.WriteFile(filepath.Join(dir, "app.json"), []byte(manifest), 0o644)
	if root, err := EnsureWorkspaceDir("u"); err == nil {
		os.MkdirAll(root, 0o755)
	}
	out, err := turn.appDefRun(map[string]any{"dir": "wx.app", "file": "data/now.py", "sample": []any{map[string]any{"city": "Reno"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "could not run") || strings.Contains(out, "workspace:") || strings.Contains(out, "bwrap:") {
		t.Skipf("sandbox unavailable: %s", out)
	}
	if !strings.Contains(out, `"city":"Reno"`) || !strings.Contains(out, "It ran clean") || !strings.Contains(out, "not published") {
		t.Fatalf("run said:\n%s", out)
	}
	if _, ok := LoadAppSpec("u", "wx"); ok {
		t.Fatal("run published the app")
	}
}

// Code the scripts share travels as lib/: out on checkout, back on publish,
// and a module deleted from the folder is gone from the app.
func TestLibrariesTravelWithTheFolder(t *testing.T) {
	turn, ws := folderTestTurn(t)
	page := "<!DOCTYPE html><html><body><script>app.data('now').then(function(d){});</script></body></html>"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Wx", "libraries": map[string]any{"json": "x"},
		"sections": []any{map[string]any{"id": "page", "kind": "html", "html": page}}}, false); err == nil || !strings.Contains(err.Error(), "replace the module") {
		t.Fatalf("a library named json was taken: %v", err)
	}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Wx", "libraries": map[string]any{"engine.py": "def price(n):\n    return n\n"},
		"sections":     []any{map[string]any{"id": "page", "kind": "html", "html": page}},
		"data_sources": []any{map[string]any{"name": "now", "script": "from engine import price\nprint('{}')"}}}, false); err != nil {
		t.Fatal(err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); spec.Libraries["engine"] == "" {
		t.Fatalf("libraries = %+v", spec.Libraries)
	}
	if out, _ := turn.appDefGet(map[string]any{"id": "wx"}); !strings.Contains(out, `"libraries":{"engine":{`) || !strings.Contains(out, `"price"`) {
		t.Errorf("get does not show the library: %s", out)
	}
	if _, err := turn.appDefCheckout(map[string]any{"id": "wx"}); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(ws, "wx.app", "lib")
	if b, _ := os.ReadFile(filepath.Join(lib, "engine.py")); string(b) != "def price(n):\n    return n\n" {
		t.Fatalf("lib/engine.py = %q", b)
	}
	os.WriteFile(filepath.Join(lib, "rules.py"), []byte("MAX = 3\n"), 0o644)
	if _, err := turn.appDefPublish(map[string]any{"dir": "wx.app"}); err != nil {
		t.Fatal(err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); spec.Libraries["rules"] != "MAX = 3\n" || spec.Libraries["engine"] == "" {
		t.Fatalf("after adding lib/rules.py: %+v", spec.Libraries)
	}
	os.Remove(filepath.Join(lib, "rules.py"))
	if _, err := turn.appDefPublish(map[string]any{"dir": "wx.app"}); err != nil {
		t.Fatal(err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); len(spec.Libraries) != 1 {
		t.Fatalf("after deleting lib/rules.py: %+v", spec.Libraries)
	}
}

// A folder and its live app stay one: a live edit of an app the folder holds
// as it is goes to the folder, and a publish over a live change it does not
// have is refused until it says it has it.
func TestTheFolderAndTheLiveAppStayOne(t *testing.T) {
	turn, ws := folderTestTurn(t)
	page := "<!DOCTYPE html><html><body><h1>Wx</h1><script>app.data('now').then(function(d){});</script></body></html>"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Wx",
		"sections":     []any{map[string]any{"id": "page", "kind": "html", "html": page}},
		"data_sources": []any{map[string]any{"name": "now", "script": "print('{}')"}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := turn.appFolderOwnsEdit(map[string]any{"id": "wx"}); err != nil {
		t.Fatalf("a live edit with no folder was refused: %v", err)
	}
	if _, err := turn.appDefCheckout(map[string]any{"id": "wx"}); err != nil {
		t.Fatal(err)
	}
	if err := turn.appFolderOwnsEdit(map[string]any{"id": "wx"}); err == nil || !strings.Contains(err.Error(), "wx.app/") {
		t.Fatalf("a live edit of an app its folder holds went through: %v", err)
	}
	// A verify leaves the folder current.
	spec, _ := LoadAppSpec("u", "wx")
	before := appLiveFingerprint(spec)
	spec.Verify = &AppVerifyState{At: "now", Pass: true}
	spec.Sample = []map[string]any{{"x": 1}}
	if appLiveFingerprint(spec) != before {
		t.Error("a verify reads as an edit")
	}
	// A change made live anyway (another session, the app's own self-update).
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "wx", "notes": "live notes", "note": "fixed the title live"}, true); err != nil {
		t.Fatal(err)
	}
	if err := turn.appFolderOwnsEdit(map[string]any{"id": "wx"}); err != nil {
		t.Fatalf("a folder behind the live app still claims its edits: %v", err)
	}
	os.WriteFile(filepath.Join(ws, "wx.app", "NOTES.md"), []byte("folder notes"), 0o644)
	_, err := turn.appDefPublish(map[string]any{"dir": "wx.app"})
	if err == nil || !strings.Contains(err.Error(), "NOT PUBLISHED") || !strings.Contains(err.Error(), "fixed the title live") {
		t.Fatalf("a publish over a live change went through: %v", err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); spec.Notes != "live notes" {
		t.Fatalf("the refused publish changed the app: %q", spec.Notes)
	}
	if _, err := turn.appDefPublish(map[string]any{"dir": "wx.app", "overwrite_live": true}); err != nil {
		t.Fatal(err)
	}
	if spec, _ := LoadAppSpec("u", "wx"); spec.Notes != "folder notes" {
		t.Fatalf("notes = %q", spec.Notes)
	}
	if err := turn.appFolderOwnsEdit(map[string]any{"id": "wx"}); err == nil {
		t.Fatal("after a publish the folder holds the app again, and a live edit went through")
	}
	// A folder with no record of the live app, over an app of its name.
	os.Remove(filepath.Join(ws, "wx.app", appFolderBaseFile))
	if _, err := turn.appDefPublish(map[string]any{"dir": "wx.app"}); err == nil || !strings.Contains(err.Error(), "no record") {
		t.Fatalf("a folder with no record published over the live app: %v", err)
	}
}
