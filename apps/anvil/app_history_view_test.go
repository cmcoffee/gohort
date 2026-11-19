package anvil

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// An app project's History lists every version the app went live as, opens
// one to what it changed, and goes back to one: the app as that version had
// it live again, as the owner's own change, and the project's files brought
// up to it with the work in progress kept on top. Nobody else reaches it.
func TestAnAppProjectsHistoryGoesBackToAVersion(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	base, _ := p.baseDir()

	// A second version, made with Builder.
	live, _ := LoadAppSpec("alice", "wx")
	live.Sections = withPageHTML(live, strings.Replace(pageHTML(live), "<h1>Wx</h1>", "<h1>Forecast</h1>", 1))
	live.ChangeNote = "Call it Forecast"
	SaveAppSpecBy(live, "update", "alice", "Builder")
	orchestrate.CommitAppHistory("alice", "wx", "alice", "Builder", "Call it Forecast")
	// The project's files take it, as before any install.
	if _, err := takeAppChanges(context.Background(), "alice", "wx", base); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "page.html")); !strings.Contains(string(b), "Forecast") {
		t.Fatalf("the fixture's project does not hold the newest version: %s", b)
	}

	w := serve("alice", "GET", "/api/project/"+id+"/app_history", "")
	var res struct {
		Commits []orchestrate.AppCommit `json:"commits"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || len(res.Commits) != 2 {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	newest, first := res.Commits[0], res.Commits[1]
	if newest.Subject != "Call it Forecast" || newest.Agent != "Builder" || !newest.Live || first.Live {
		t.Errorf("the versions: %+v", res.Commits)
	}
	if w := serve("alice", "GET", "/api/project/"+id+"/app_history/diff?commit="+newest.Short, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Forecast") {
		t.Errorf("a version's diff: %d %s", w.Code, w.Body.String())
	}
	if w := serve("bob", "GET", "/api/project/"+id+"/app_history", ""); w.Code == 200 {
		t.Errorf("someone else read alice's app's history: %s", w.Body.String())
	}

	// Work in progress in the project, on another file.
	notes := filepath.Join(base, "NOTES.md")
	os.WriteFile(notes, []byte("Plan: a forecast page, kept while going back.\n"), 0o644)

	if w := serve("bob", "POST", "/api/project/"+id+"/app_history/restore", `{"commit":"`+first.Hash+`"}`); w.Code == 200 {
		t.Errorf("someone else took alice's app back: %s", w.Body.String())
	}
	if w := serve("alice", "POST", "/api/project/"+id+"/app_history/restore", `{"commit":"`+first.Hash+`"}`); w.Code != 200 {
		t.Fatalf("go back: %d %s", w.Code, w.Body.String())
	}
	now, _ := LoadAppSpec("alice", "wx")
	if !strings.Contains(pageHTML(now), "<h1>Wx</h1>") {
		t.Errorf("the app is not as the first version had it: %s", pageHTML(now))
	}
	if b, _ := os.ReadFile(filepath.Join(base, "page.html")); !strings.Contains(string(b), "<h1>Wx</h1>") {
		t.Errorf("the project's files were not brought up to the version gone back to: %s", b)
	}
	if b, _ := os.ReadFile(notes); !strings.Contains(string(b), "kept while going back") {
		t.Error("the project's work in progress was lost going back")
	}
	if list, _ := orchestrate.AppHistoryLog("alice", "wx", 0); len(list) != 3 || !strings.HasPrefix(list[0].Subject, "Go back to the version of") {
		t.Errorf("the history after going back: %+v", list)
	}
}

// An install that leaves the app not working is live but not in its history
// yet: History says so above the versions and marks none of them live, so
// the newest, the last that worked, is one to go back to. Going back to it
// puts the app as it last worked, and the newest version is live again.
func TestHistoryGoesBackPastAnInstallThatDoesNotWork(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	base, _ := p.baseDir()
	before, _ := LoadAppSpec("alice", "wx")
	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("raise SystemExit('the weather service is down')\n"), 0o644)
	if w := serve("alice", "POST", "/api/project/"+id+"/install", "{}"); w.Code != 200 || !strings.Contains(w.Body.String(), `"broken"`) {
		t.Fatalf("the install that does not work: %d %s", w.Code, w.Body.String())
	}

	w := serve("alice", "GET", "/api/project/"+id+"/app_history", "")
	var res struct {
		Commits []orchestrate.AppCommit `json:"commits"`
		Waiting *orchestrate.AppWaiting `json:"waiting"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || len(res.Commits) == 0 {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	if res.Waiting == nil || res.Waiting.By != "alice" || res.Waiting.Via != "Anvil" {
		t.Errorf("the install live and not in the history is not said: %+v", res.Waiting)
	}
	for _, c := range res.Commits {
		if c.Live {
			t.Errorf("%q reads as live, with the install serving", c.Subject)
		}
	}
	if w := serve("alice", "POST", "/api/project/"+id+"/app_history/restore", `{"commit":"`+res.Commits[0].Hash+`"}`); w.Code != 200 || strings.Contains(w.Body.String(), `"broken"`) {
		t.Fatalf("go back to the version that worked: %d %s", w.Code, w.Body.String())
	}
	if now, _ := LoadAppSpec("alice", "wx"); now.DataSources[0].Script != before.DataSources[0].Script {
		t.Errorf("the app is not as it last worked: %q", now.DataSources[0].Script)
	}
	if _, waiting := orchestrate.AppHistoryWaiting("alice", "wx"); waiting {
		t.Error("the install is still waiting to go in after going back past it")
	}
	if list, _ := orchestrate.AppHistoryLog("alice", "wx", 0); len(list) != len(res.Commits) || !list[0].Live {
		t.Errorf("the history after going back: %+v", list)
	}
}

// History is on an app project's bar, and on no other project's.
func TestOnlyAnAppProjectHasAHistoryButton(t *testing.T) {
	var T Anvil
	has := func(p Project) bool {
		for _, a := range T.toolbarActions(p, "alice", false) {
			if a.Label == "History" && a.URL == "anvil_app_history" {
				return true
			}
		}
		return false
	}
	if !has(Project{Kind: KindClone, Source: sourceApp, AppSlug: "wx", Owner: "alice"}) {
		t.Error("an app project has no History")
	}
	if has(Project{Kind: KindClone, Owner: "alice"}) {
		t.Error("a repository's project has the app History")
	}
}
