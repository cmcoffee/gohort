package orchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
)

// shelfVersions is a live app Shelf saved twice by its owner, "Books" then
// "Novels", each a version in its history; it returns the versions newest
// first.
func shelfVersions(t *testing.T) (*chatTurn, []AppCommit) {
	t.Helper()
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	turn, _ := copyTestTurn(t)
	page := func(word string) json.RawMessage {
		b, _ := json.Marshal([]map[string]any{{"id": "page", "kind": "html", "html": "<!DOCTYPE html><html><body><h1>" + word + "</h1></body></html>"}})
		return b
	}
	spec := AppSpec{Owner: "u", Slug: "shelf", Name: "Shelf", RecordKey: "id", Sections: page("Books"), Page: json.RawMessage(`{"title":"Shelf","sections":[]}`)}
	SaveAppSpec(spec)
	spec, _ = LoadAppSpec("u", "shelf")
	spec.Sections, spec.ChangeNote = page("Novels"), "Say novels"
	SaveAppSpec(spec)
	list, err := AppHistoryLog("u", "shelf", 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("the history: %v %+v", err, list)
	}
	return turn, list
}

// The history lists every version the app went live as, newest first, who
// made it and which is live; one version's diff is what it changed; and a
// name that is not a version of the app is refused before git reads it.
func TestAnAppsHistoryListsItsVersions(t *testing.T) {
	_, list := shelfVersions(t)
	if !list[0].Live || list[1].Live || list[0].Subject != "Say novels" || list[0].Author != "u" || list[1].Subject != "Start of Shelf's history" {
		t.Errorf("the versions: %+v", list)
	}
	diff, err := AppHistoryDiff("u", "shelf", list[0].Short)
	if err != nil || !strings.Contains(diff, "Novels") || !strings.Contains(diff, "Books") {
		t.Errorf("the newest version's diff: %v\n%s", err, diff)
	}
	// A commit in the history's store that the app never went live as.
	dir, _ := appRepoDir("u", "shelf")
	tree, _ := appRepoGit(dir, "rev-parse", "HEAD^{tree}")
	stray, err := appRepoGit(dir, "-c", "user.name=x", "-c", "user.email=x@x", "commit-tree", strings.TrimSpace(tree), "-m", "stray")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"--all", "live", "deadbeef", "", strings.TrimSpace(stray)} {
		if _, err := AppHistoryDiff("u", "shelf", bad); err == nil {
			t.Errorf("%q was read as a version of the app", bad)
		}
	}
}

// Going back to an earlier version makes the app as it was then live again,
// as the person's change made from Anvil's History: its files and what it
// owned then (its own agent as that version had it), a new version saying
// which it went back to, the versions before it kept, so going back is
// undone the same way.
func TestThePersonGoesBackToAnEarlierVersion(t *testing.T) {
	turn, _ := shelfVersions(t)
	udb := UserDB(orchestrateBaseDB, "u")
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Old prompt.", OwningApp: "shelf", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	CommitAppHistory("u", "shelf", "u", "Builder", "Give the shelf a clerk")
	clerk.OrchestratorPrompt, clerk.appEdit = "New prompt.", "Builder"
	saveAgent(udb, clerk)
	CommitAppHistory("u", "shelf", "u", "Builder", "Change the clerk's prompt")
	list, _ := AppHistoryLog("u", "shelf", 0)
	if len(list) != 4 {
		t.Fatalf("the fixture's history: %+v", list)
	}
	// list: the clerk's new prompt, the clerk, "Say novels", the start.
	out, err := turn.app.RestoreAppVersion(context.Background(), "u", "shelf", list[1].Hash)
	if err != nil || !strings.Contains(out, "back to the version") {
		t.Fatalf("going back to the clerk's first prompt: %v %s", err, out)
	}
	if now, _ := loadAgent(udb, clerk.ID); now.OrchestratorPrompt != "Old prompt." {
		t.Fatalf("the app's own agent is not as that version had it: %q", now.OrchestratorPrompt)
	}
	// And to its first version, the page as it began.
	list, _ = AppHistoryLog("u", "shelf", 0)
	out, err = turn.app.RestoreAppVersion(context.Background(), "u", "shelf", list[len(list)-1].Hash)
	if err != nil || !strings.Contains(out, "back to the version") {
		t.Fatalf("going back: %v %s", err, out)
	}
	live, _ := LoadAppSpec("u", "shelf")
	if !strings.Contains(string(live.Sections), "Books") || strings.Contains(string(live.Sections), "Novels") {
		t.Fatalf("the app is not as the earlier version had it: %s", live.Sections)
	}
	now, _ := AppHistoryLog("u", "shelf", 0)
	if len(now) != 6 || !strings.HasPrefix(now[0].Subject, "Go back to the version of") || now[0].Author != "u" || now[0].Agent != "Anvil" || !now[0].Live {
		t.Fatalf("the history after going back: %+v", now)
	}
	// And going back again, to the version it replaced.
	if _, err := turn.app.RestoreAppVersion(context.Background(), "u", "shelf", now[1].Hash); err != nil {
		t.Fatal(err)
	}
	if live, _ := LoadAppSpec("u", "shelf"); !strings.Contains(string(live.Sections), "Novels") {
		t.Errorf("going back was not undone the same way: %s", live.Sections)
	}
}

// Builder reads the same history: revisions lists its versions by hash, and
// revert to one puts the app as that version had it in Builder's draft,
// never straight into the live app.
func TestBuilderGoesBackToAVersionInItsDraft(t *testing.T) {
	turn, list := shelfVersions(t)
	turn.agent = AgentRecord{ID: "seed-builder", Name: "Builder"}
	out, err := turn.appDefRevisions(map[string]any{"id": "shelf"})
	if err != nil || !strings.Contains(out, list[1].Short) || !strings.Contains(out, "(live)") {
		t.Fatalf("revisions: %v\n%s", err, out)
	}
	if _, err := turn.appDefRevert(map[string]any{"id": "shelf", "to": list[1].Short}); err != nil {
		t.Fatal(err)
	}
	d, ok := appdraft.Load("u", "shelf", "u")
	if !ok || !strings.Contains(string(d.Spec.Sections), "Books") {
		t.Fatalf("the draft is not the earlier version: %v", ok)
	}
	if live, _ := LoadAppSpec("u", "shelf"); !strings.Contains(string(live.Sections), "Novels") {
		t.Error("Builder's going back reached the live app before its commit")
	}
}

// Work live and not in the history yet (an agent's saves not committed as
// finished) leaves no version live: the history says so, and Builder's
// versions list names it above them.
func TestWorkLiveAndNotInTheHistoryLeavesNoVersionLive(t *testing.T) {
	turn, _ := shelfVersions(t)
	if _, ok := AppHistoryWaiting("u", "shelf"); ok {
		t.Fatal("a history with nothing waiting says something is")
	}
	// A change saved and put back, as an install refused after it saved:
	// its record stays, and nothing is waiting.
	spec, _ := LoadAppSpec("u", "shelf")
	kept := spec.Sections
	spec.Sections = json.RawMessage(`[{"id":"page","kind":"html","html":"<!DOCTYPE html><html><body><h1>Broken</h1></body></html>"}]`)
	spec.ChangeNote = "Installed from Anvil"
	SaveAppSpecBy(spec, "update", "u", "Anvil")
	spec.Sections = kept
	SaveAppSpecBy(spec, "update", "u", "Anvil")
	if _, ok := AppHistoryWaiting("u", "shelf"); ok {
		t.Error("a change put back reads as waiting")
	}
	if list, _ := AppHistoryLog("u", "shelf", 0); !list[0].Live {
		t.Errorf("a change put back leaves the newest version not live: %+v", list[0])
	}
	spec, _ = LoadAppSpec("u", "shelf")
	spec.Sections = json.RawMessage(`[{"id":"page","kind":"html","html":"<!DOCTYPE html><html><body><h1>Poems</h1></body></html>"}]`)
	spec.ChangeNote = "Say poems"
	SaveAppSpecBy(spec, "update", "u", "Anvil")
	w, ok := AppHistoryWaiting("u", "shelf")
	if !ok || w.By != "u" || w.Via != "Anvil" || len(w.Notes) == 0 || w.Notes[len(w.Notes)-1] != "Say poems" {
		t.Errorf("the work waiting: %v %+v", ok, w)
	}
	list, _ := AppHistoryLog("u", "shelf", 0)
	for _, c := range list {
		if c.Live {
			t.Errorf("%q reads as live, with newer work serving", c.Subject)
		}
	}
	out, err := turn.appDefRevisions(map[string]any{"id": "shelf"})
	if err != nil || !strings.Contains(out, `LIVE NOW, not in the history yet: "Installed from Anvil; Say poems", by u with Anvil`) || strings.Contains(out, "(live)") {
		t.Errorf("the versions Builder reads:\n%v %s", err, out)
	}
	CommitAppHistory("u", "shelf", "u", "Anvil", "Say poems")
	if list, _ := AppHistoryLog("u", "shelf", 0); !list[0].Live {
		t.Errorf("once in, the newest version is not live: %+v", list[0])
	}
}
