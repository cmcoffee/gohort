package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/applocal"
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
// app as saved as its first commit. An agent's later save writes the app's
// files into it and commits nothing: a finished piece of work is the commit,
// not a save. What the app no longer has leaves the folder, and deleting the app
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

	// An agent's save waits; a person's own would be committed at once
	// (TestACommitIsOnlyItsAuthorsWork).
	SaveAppSpecBy(historyApp(map[string]string{"now": "print('{\"t\": 2}')", "week": "print('[]')"}), "update", "alice", "Builder")
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

	SaveAppSpecBy(historyApp(map[string]string{"now": "print('{\"t\": 2}')"}), "update", "alice", "Builder")
	if _, err := os.Stat(filepath.Join(dir, "data", "week.py")); err == nil {
		t.Error("a data source the app no longer has is still in its history's files")
	}

	DeleteAppSpec("alice", "wx")
	if _, err := os.Stat(dir); err == nil {
		t.Error("the deleted app's history is still there")
	}
}

// An administrator's stop changes none of the app's files and is recorded
// in its history all the same, as theirs: an agent's saves waiting are
// committed first as the agent's person's, so the stop is its own entry.
// An app with no history yet records nothing.
func TestAnAdministratorsStopIsInTheAppsHistory(t *testing.T) {
	ws := historyWorld(t)
	dir := filepath.Join(ws, "alice", ".apps", "wx")
	applocal.HistoryNote("alice", "wx", "root", "Switched off by an administrator")
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a note made a history for an app that has none")
	}
	SaveAppSpec(historyApp(nil))
	SaveAppSpecBy(historyApp(map[string]string{"now": "print('{}')"}), "update", "alice", "Builder")
	applocal.HistoryNote("alice", "wx", "root", "Switched off by an administrator\n\nroot switched the app off.")
	log, _ := appRepoGit(dir, "log", "--format=%an|%s")
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if len(lines) != 3 || lines[0] != "root|Switched off by an administrator" || !strings.HasPrefix(lines[1], "alice|") {
		t.Fatalf("history = %q", log)
	}
	if st, _ := appRepoGit(dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("saves still waiting after the note: %q", st)
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

// A commit is its author's finished work. A person's own change is committed
// at once; an agent's saves wait for the work to be finished, and a save by
// someone else first commits what was waiting, under its own author, so no
// commit holds two people's work. CommitAppHistory commits only what its
// author has waiting, crediting the agent as co-author.
func TestACommitIsOnlyItsAuthorsWork(t *testing.T) {
	ws := historyWorld(t)
	dir := filepath.Join(ws, "alice", ".apps", "wx")
	commits := func() string {
		out, _ := appRepoGit(dir, "log", "--reverse", "--format=%an|%s")
		return strings.TrimSpace(out)
	}
	save := func(by, via, note, script string) {
		s := historyApp(map[string]string{"now": script})
		s.ChangeNote = note
		SaveAppSpecBy(s, "update", by, via)
	}

	save("alice", "", "", "print(1)") // starts the history
	save("alice", "Builder", "Add a forecast", "print(2)")
	save("alice", "Builder", "Show the week", "print(3)")
	if got := commits(); got != "alice|Start of Weather's history" {
		t.Errorf("an agent's saves were committed before the work was finished:\n%s", got)
	}

	// A maintainer's save: alice's waiting work is committed first, as hers.
	save("bob", "Builder", "Fix the units", "print(4)")
	if got := commits(); !strings.HasSuffix(got, "alice|Add a forecast") {
		t.Errorf("alice's waiting work was not committed as hers first:\n%s", got)
	}
	body, _ := appRepoGit(dir, "log", "-1", "--format=%B")
	if !strings.Contains(body, "before this work was marked finished") || !strings.Contains(body, "- Show the week") || !strings.Contains(body, "Co-Authored-By: Builder") {
		t.Errorf("the interrupted commit's message:\n%s", body)
	}

	// Bob finishes: his commit holds his work, with Builder credited.
	done, err := CommitAppHistory("alice", "wx", "bob", "Builder", "Use Celsius everywhere\n\nThe forecast mixed units.")
	if err != nil || !done {
		t.Fatalf("commit: %v %v", done, err)
	}
	if got := commits(); !strings.HasSuffix(got, "bob|Use Celsius everywhere") {
		t.Errorf("bob's commit:\n%s", got)
	}
	if body, _ := appRepoGit(dir, "log", "-1", "--format=%B"); !strings.Contains(body, "Co-Authored-By: Builder <builder@agents.gohort.invalid>") {
		t.Errorf("the agent is not credited:\n%s", body)
	}
	if done, _ := CommitAppHistory("alice", "wx", "bob", "Builder", "Again"); done {
		t.Error("a commit with nothing waiting was made")
	}

	// A person's own change is committed at once, with its note.
	save("alice", "", "Rename the page", "print(5)")
	if got := commits(); !strings.HasSuffix(got, "alice|Rename the page") {
		t.Errorf("a person's own change was not committed at once:\n%s", got)
	}
}

// Builder's commit is refused until the work is tested: verify has passed on
// the version serving now. Then it commits the person's work with Builder as
// co-author.
func TestBuilderCommitsOnlyVerifiedWork(t *testing.T) {
	turn, _ := folderTestTurn(t)
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	turn.agent.Name = "Builder"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{
		"name": "Tally", "notes": "Counts things.",
		"sections": []any{map[string]any{"id": "page", "kind": "html", "html": "<!DOCTYPE html><html><body><h1>Tally</h1></body></html>"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "tally", "notes": "Counts things, by day.", "note": "Count by day"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.appDefCommit(map[string]any{"id": "tally", "message": "Count by day"}); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("a commit before verify: %v", err)
	}
	spec, _ := LoadAppSpec("u", "tally")
	spec.RecordVerify(true, "ok")
	// A reply ending the turn with that work verified and uncommitted is held
	// back once, to commit it.
	turn.session = &ChatSession{App: "tally"}
	if n := turn.uncommittedWorkNotice(); !strings.Contains(n, `app_def(action="commit", id="tally"`) {
		t.Errorf("the turn's end was not held for the commit: %q", n)
	}
	if n := turn.uncommittedWorkNotice(); n != "" {
		t.Error("held twice in one turn")
	}
	out, err := turn.appDefCommit(map[string]any{"id": "tally", "message": "Count by day\n\nOne row per day."})
	if err != nil || !strings.Contains(out, "Committed") {
		t.Fatalf("a verified commit: %q %v", out, err)
	}
	dir, _ := appRepoDir("u", "tally")
	if log, _ := appRepoGit(dir, "log", "-1", "--format=%an|%s|%b"); !strings.HasPrefix(log, "u|Count by day|") || !strings.Contains(log, "Co-Authored-By: Builder") {
		t.Errorf("the commit: %q", log)
	}
}

// An app's agent is the app's: what it is changes only through Builder or
// Anvil, so a change goes through the app's checks, history and approval.
// What a person does with it as they use it (always allowing a tool, hiding
// it, sharing it) is still theirs to change, and an agent of no app is not
// held at all. Making an agent an app's is itself held.
func TestAnAppsAgentChangesOnlyThroughBuilderOrAnvil(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	app, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Helper", OrchestratorPrompt: "Help.", OwningApp: "shop", appEdit: "Builder"})
	if err != nil {
		t.Fatalf("Builder making an app's agent: %v", err)
	}
	edit := app
	edit.OrchestratorPrompt = "Help, and promise refunds."
	if _, err := saveAgent(udb, edit); err == nil || !strings.Contains(err.Error(), "change it with Builder or Anvil") {
		t.Errorf("an app's agent changed outside Builder and Anvil: %v", err)
	}
	edit.appEdit = "Anvil"
	if _, err := saveAgent(udb, edit); err != nil {
		t.Errorf("Anvil changing an app's agent: %v", err)
	}
	app, _ = loadAgent(udb, app.ID)
	use := app
	use.AutoApproveTools, use.Hidden = []string{"web_search"}, true
	if _, err := saveAgent(udb, use); err != nil {
		t.Errorf("a person's choices on an app's agent: %v", err)
	}

	plain, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Mine", OrchestratorPrompt: "Mine."})
	plain.OrchestratorPrompt = "Still mine."
	if _, err := saveAgent(udb, plain); err != nil {
		t.Errorf("an agent of no app was held: %v", err)
	}
	plain.OwningApp = "shop"
	if _, err := saveAgent(udb, plain); err == nil {
		t.Error("an agent was made an app's outside Builder and Anvil")
	}
	if _, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Sneak", OrchestratorPrompt: "p", OwningApp: "shop"}); err == nil {
		t.Error("an app's agent was made outside Builder and Anvil")
	}
}

// What an app owns is in its history: its agent is a file of its folder
// (agents/<name>.json), a change Builder makes to it is the app's change,
// waiting as Builder's work, and a shared app whose owner is not an
// administrator is taken out of sharing until one approves it, as a change
// to its page would.
func TestAnAppsOwnAgentIsInItsHistoryAndItsApproval(t *testing.T) {
	ws := historyWorld(t)
	prevHost := agenthost.Get()
	o := &OrchestrateApp{}
	o.DB = &DBase{Store: kvlite.MemStore()}
	agenthost.Register(o)
	t.Cleanup(func() { agenthost.Register(prevHost) })
	udb := UserDB(o.DB, "alice")

	app := historyApp(map[string]string{"now": "print(1)"})
	app.Shared = true
	SaveAppSpec(app)
	helper, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Helper", OrchestratorPrompt: "Help.", OwningApp: "wx", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws, "alice", ".apps", "wx")
	if b, err := os.ReadFile(filepath.Join(dir, "agents", "helper.json")); err != nil || !strings.Contains(string(b), "Help.") {
		t.Fatalf("the app's agent is not in its history: %v %s", err, b)
	}
	if spec, _ := LoadAppSpec("alice", "wx"); spec.Shared {
		t.Error("a shared app stayed shared after its agent changed")
	}

	edit, _ := loadAgent(udb, helper.ID)
	edit.OrchestratorPrompt = "Help kindly."
	edit.appEdit = "Builder"
	if _, err := saveAgent(udb, edit); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "agents", "helper.json")); !strings.Contains(string(b), "Help kindly.") {
		t.Errorf("the change is not in the history's files: %s", b)
	}
	if p := loadAppPending(dir); p.Via != "Builder" || len(p.Notes) == 0 || !strings.Contains(p.Notes[len(p.Notes)-1], "agent Helper") {
		t.Errorf("the change is not waiting as Builder's work: %+v", p)
	}
}
