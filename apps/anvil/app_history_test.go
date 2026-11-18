package anvil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// An app project works in the app's own history: opening the app clones it;
// a change Builder saved and committed since is taken into the work before
// an install, the two merged when they touch different lines, and the
// install's commit is the one the checkout then stands on. Where both change
// the same line, the install stops on the conflict instead of being refused
// as a folder gone stale.
func TestAnAppProjectWorksInTheAppsHistory(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	base, _ := p.baseDir()
	git := func(args ...string) string {
		out, _ := runGit(base, args...)
		return strings.TrimSpace(out)
	}
	if !strings.Contains(git("log", "--format=%s"), "history") {
		t.Fatalf("the checkout is not the app's history: %q", git("log", "--format=%s"))
	}
	if st := git("status", "--porcelain"); st != "" {
		t.Errorf("a fresh checkout has changes: %q", st)
	}

	// Builder changes the data source and commits it, while Anvil changes
	// the page.
	live, _ := LoadAppSpec("alice", "wx")
	live.DataSources[0].Script = "print('{\"t\": 7}')"
	live.ChangeNote = "Report seven"
	SaveAppSpecBy(live, "update", "alice", "Builder")
	if done, err := orchestrate.CommitAppHistory("alice", "wx", "alice", "Builder", "Report seven"); err != nil || !done {
		t.Fatalf("Builder's commit: %v %v", done, err)
	}
	page := filepath.Join(base, "page.html")
	orig, _ := os.ReadFile(page)
	os.WriteFile(page, []byte(strings.Replace(string(orig), "<h1>Wx</h1>", "<h1>Weather</h1>", 1)), 0o644)

	if w := serve("alice", "POST", "/api/project/"+id+"/install", "{}"); w.Code != 200 {
		t.Fatalf("install over Builder's change: %d %s", w.Code, w.Body.String())
	}
	now, _ := LoadAppSpec("alice", "wx")
	if now.DataSources[0].Script != "print('{\"t\": 7}')" || !strings.Contains(pageHTML(now), "Weather</h1>") {
		t.Errorf("the live app does not hold both changes: %q, %s", now.DataSources[0].Script, pageHTML(now))
	}
	if git("log", "--format=%s") == "" || !strings.Contains(git("log", "--format=%an %s"), "alice Report seven") {
		t.Errorf("Builder's commit is not in the checkout's history:\n%s", git("log", "--format=%an %s"))
	}
	if st := git("status", "--porcelain"); st != "" {
		t.Errorf("the checkout is not on the install's commit: %q", st)
	}

	// Both change the same line: the install stops on the conflict.
	live, _ = LoadAppSpec("alice", "wx")
	live.Sections = withPageHTML(live, strings.Replace(pageHTML(live), "Weather</h1>", "Forecast</h1>", 1))
	SaveAppSpecBy(live, "update", "alice", "Builder")
	orchestrate.CommitAppHistory("alice", "wx", "alice", "Builder", "Call it Forecast")
	cur, _ := os.ReadFile(page)
	os.WriteFile(page, []byte(strings.Replace(string(cur), "Weather</h1>", "Outlook</h1>", 1)), 0o644)
	w := serve("alice", "POST", "/api/project/"+id+"/install", "{}")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "overlap in page.html") {
		t.Fatalf("an overlapping install: %d %s", w.Code, w.Body.String())
	}
	if after, _ := LoadAppSpec("alice", "wx"); !strings.Contains(pageHTML(after), "Forecast</h1>") {
		t.Error("a conflicting install changed the live app")
	}
	if b, _ := os.ReadFile(page); !strings.Contains(string(b), "<<<<<<<") {
		t.Error("the conflict is not marked in the file")
	}
}

// pageHTML is the app's first html section's markup, and withPageHTML its
// sections with that markup replaced.
func pageHTML(s AppSpec) string {
	var secs []map[string]any
	json.Unmarshal(s.Sections, &secs)
	if len(secs) == 0 {
		return ""
	}
	h, _ := secs[0]["html"].(string)
	return h
}

func withPageHTML(s AppSpec, html string) json.RawMessage {
	var secs []map[string]any
	json.Unmarshal(s.Sections, &secs)
	secs[0]["html"] = html
	b, _ := json.Marshal(secs)
	return b
}
