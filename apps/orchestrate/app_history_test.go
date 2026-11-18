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

// historyWorld is an app store and a workspaces folder, for the history the
// saves write.
func historyWorld(t *testing.T) string {
	t.Helper()
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	prevRoot, prevAssets, prevWS := RootDB, AppAssetsDir(), WorkspacesDir()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	ws := t.TempDir()
	SetWorkspacesDir(ws)
	t.Cleanup(func() { RootDB = prevRoot; SetAppAssetsDir(prevAssets); SetWorkspacesDir(prevWS) })
	return ws
}

func historyApp(scripts map[string]string) AppSpec {
	s := AppSpec{Owner: "alice", Slug: "wx", Name: "Weather", RecordKey: "id",
		Sections: json.RawMessage(`[{"id":"page","kind":"html","html":"<!DOCTYPE html><html><body>Wx</body></html>"}]`),
		Page:     json.RawMessage(`{"title":"Weather","sections":[]}`)}
	for name, src := range scripts {
		s.DataSources = append(s.DataSources, AppDataSource{Name: name, Script: src})
	}
	return s
}

// Every app has a repository of its folder, made by its first save with the
// app as saved as its first commit. A later save writes the app's files into
// it and commits nothing: a finished piece of work is the commit, not a
// save. What the app no longer has leaves the folder, and deleting the app
// takes its history with it.
func TestEverySaveWritesTheAppIntoItsHistory(t *testing.T) {
	ws := historyWorld(t)
	dir := filepath.Join(ws, "alice", ".apps", "wx")

	SaveAppSpec(historyApp(map[string]string{"now": "print('{}')"}))
	if log, _ := appRepoGit(dir, "log", "--format=%an|%s"); strings.TrimSpace(log) != "alice|Start of Weather's history" {
		t.Fatalf("the first save's history: %q", log)
	}
	for _, f := range []string{"app.json", "page.html", "data/now.py", "NOTES.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("the history has no %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, appFolderBaseFile)); err == nil {
		t.Error("the history holds an install's live marker")
	}

	SaveAppSpec(historyApp(map[string]string{"now": "print('{\"t\": 2}')", "week": "print('[]')"}))
	if b, _ := os.ReadFile(filepath.Join(dir, "data", "now.py")); string(b) != `print('{"t": 2}')` {
		t.Errorf("the save was not written: %q", b)
	}
	st, _ := appRepoGit(dir, "status", "--porcelain")
	if !strings.Contains(st, "data/now.py") || !strings.Contains(st, "data/week.py") {
		t.Errorf("a save's changes are not waiting to be committed: %q", st)
	}
	if n, _ := appRepoGit(dir, "rev-list", "--count", "HEAD"); strings.TrimSpace(n) != "1" {
		t.Errorf("a save made a commit: %s commits", strings.TrimSpace(n))
	}

	SaveAppSpec(historyApp(map[string]string{"now": "print('{\"t\": 2}')"}))
	if _, err := os.Stat(filepath.Join(dir, "data", "week.py")); err == nil {
		t.Error("a data source the app no longer has is still in its history's files")
	}

	DeleteAppSpec("alice", "wx")
	if _, err := os.Stat(dir); err == nil {
		t.Error("the deleted app's history is still there")
	}
}

// Without git nothing is written: apps save and keep revisions as always,
// and the admin page can say why there is no history.
func TestWithoutGitAnAppSavesAsAlways(t *testing.T) {
	ws := historyWorld(t)
	prev := appGit
	appGit = func() string { return "" }
	t.Cleanup(func() { appGit = prev })
	saved := SaveAppSpec(historyApp(map[string]string{"now": "print('{}')"}))
	if saved.Updated == "" {
		t.Fatal("the app was not saved")
	}
	if _, err := os.Stat(filepath.Join(ws, "alice", ".apps")); err == nil {
		t.Error("a history was written without git")
	}
	if !strings.Contains(AppHistoryUnavailable(), "git is not installed") {
		t.Errorf("the reason: %q", AppHistoryUnavailable())
	}
}
