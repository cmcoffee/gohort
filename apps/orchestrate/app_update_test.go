package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/core/appsource"
)

// updateTestPage is a page whose heading and footer are lines apart, so a
// change to one and a change to the other merge.
func updateTestPage(heading, footer string) json.RawMessage {
	html := "<!DOCTYPE html>\n<html>\n<body>\n<h1>" + heading + "</h1>\n<p>The desk.</p>\n<p>Open all day.</p>\n<footer>" + footer + "</footer>\n</body>\n</html>\n"
	b, _ := json.Marshal([]map[string]any{{"id": "page", "kind": "html", "html": html}})
	return b
}

// updateTestApp makes u's app desk, with an agent of its own, and returns a
// function exporting it as a bundle.
func updateTestApp(t *testing.T) func() []byte {
	t.Helper()
	turn, udb := copyTestTurn(t)
	copyTestApp(t, turn, nil)
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Help at the desk.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadAppSpec("u", "desk")
	spec.AgentID, spec.Sections = clerk.ID, updateTestPage("Desk", "Made by u")
	SaveAppSpec(spec)
	return func() []byte {
		t.Helper()
		bundle, err := ExportArtifactBundleAsUser(RootDB, "u", []ArtifactSel{{Type: "custom_app", Name: "desk", Owner: "u"}}, UserExportOptions{IncludeDeps: true})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(bundle)
		return data
	}
}

// changeUApp changes u's desk: its page's heading, and its agent's prompt.
func changeUApp(t *testing.T, heading, prompt string) {
	t.Helper()
	spec, _ := LoadAppSpec("u", "desk")
	spec.Sections = updateTestPage(heading, "Made by u")
	SaveAppSpec(spec)
	if prompt == "" {
		return
	}
	udb := UserDB(orchestrateBaseDB, "u")
	a, ok := loadAgent(udb, spec.AgentID)
	if !ok {
		t.Fatal("u's app has no agent")
	}
	a.OrchestratorPrompt, a.appEdit = prompt, "Builder"
	if _, err := saveAgent(udb, a); err != nil {
		t.Fatal(err)
	}
}

func appPageHTML(s AppSpec) string {
	var secs []map[string]any
	json.Unmarshal(s.Sections, &secs)
	if len(secs) == 0 {
		return ""
	}
	h, _ := secs[0]["html"].(string)
	return h
}

func outcomeOf(res ArtifactImportResult, typ string) ArtifactImportOutcome {
	for _, o := range res.Outcomes {
		if o.Type == typ {
			return o
		}
	}
	return ArtifactImportOutcome{}
}

// An imported app takes a later export of the copy it was taken from as an
// update: merged with the changes its importer made since, kept as their
// draft, nothing live changed until they make it live, and then the next
// update merges against it. The same file twice, an older one, and a file
// from another copy are refused, and so is an update while one waits.
func TestAnImportedAppTakesItsAuthorsUpdates(t *testing.T) {
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	export := updateTestApp(t)
	first := export()
	if res, err := ImportArtifactBundleAsUser(RootDB, first, "v"); err != nil || outcomeOf(res, "custom_app").Status != "imported" {
		t.Fatalf("v's import: %v %+v", err, res)
	}
	src, ok := appsource.Load("v", "desk")
	if !ok || src.Copy == "" || src.Exported == "" || src.Mark == "" {
		t.Fatalf("where v's app came from: %v %+v", ok, src)
	}

	// v changes their copy's footer; u changes the heading and the agent.
	vSpec, _ := LoadAppSpec("v", "desk")
	vSpec.Sections = updateTestPage("Desk", "Kept by v")
	SaveAppSpec(vSpec)
	changeUApp(t, "Front desk", "Help at the front desk.")
	second := export()

	prev, err := PreviewArtifactBundleAsUser(RootDB, second, "v")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range prev.Items {
		if it.Type == "custom_app" && (it.Action != "update" || !strings.Contains(it.Detail, "update")) {
			t.Errorf("the preview of the update: %+v", it)
		}
	}
	res, err := ImportArtifactBundleAsUser(RootDB, second, "v")
	if o := outcomeOf(res, "custom_app"); err != nil || o.Status != "updated" || res.Updated != 1 || !strings.Contains(o.Detail, "merged with your changes") {
		t.Fatalf("the update's import: %v %+v", err, res)
	}
	live, _ := LoadAppSpec("v", "desk")
	if h := appPageHTML(live); !strings.Contains(h, "<h1>Desk</h1>") || !strings.Contains(h, "Kept by v") {
		t.Errorf("the update changed the live app before it was made live:\n%s", h)
	}
	d, ok := appdraft.Load("v", "desk", "v")
	if !ok || d.Update == nil || d.Update.Commit == "" || len(d.Update.Clashed) != 0 {
		t.Fatalf("the update's draft: %v %+v", ok, d.Update)
	}
	if h := appPageHTML(d.Spec); !strings.Contains(h, "Front desk") || !strings.Contains(h, "Kept by v") {
		t.Errorf("the draft is not the update merged with v's change:\n%s", h)
	}
	if !strings.Contains(string(d.Owned["agents/clerk.json"]), "front desk") {
		t.Errorf("the draft does not hold the agent as updated: %s", d.Owned["agents/clerk.json"])
	}
	// Only what the author changed: the app's agent is the app's own here
	// by id, as its history has it, not a change to app.json.
	if got := strings.Join(d.Update.Changed, " "); got != "agents/clerk.json page.html" {
		t.Errorf("what the update changes: %q", got)
	}
	// A history imported while an update waits does not take the place of
	// the one it is merged with.
	hist, err := appHistoryArtifact{}.ExportArtifact(nil, "desk", "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, why, _ := (appHistoryArtifact{}).ImportArtifact(nil, hist, "v"); !strings.Contains(why, "keeps the history") {
		t.Errorf("a history imported while an update waits: %q", why)
	}

	// The same file again, while it waits.
	if res, _ := ImportArtifactBundleAsUser(RootDB, second, "v"); !strings.Contains(outcomeOf(res, "custom_app").Detail, "already waiting") {
		t.Errorf("a second update while one waits: %+v", res)
	}
	// An older one.
	if res, _ := ImportArtifactBundleAsUser(RootDB, first, "v"); !strings.Contains(outcomeOf(res, "custom_app").Detail, "already have this version") {
		t.Errorf("the version taken, again: %+v", res)
	}

	msg, err := makeDraftLive(d, "v")
	if err != nil || !strings.Contains(msg, "update") {
		t.Fatalf("making the update live: %q %v", msg, err)
	}
	live, _ = LoadAppSpec("v", "desk")
	if h := appPageHTML(live); !strings.Contains(h, "Front desk") || !strings.Contains(h, "Kept by v") {
		t.Errorf("the live app after the update:\n%s", h)
	}
	vdb := UserDB(orchestrateBaseDB, "v")
	if a, ok := loadAgent(vdb, live.AgentID); !ok || a.OrchestratorPrompt != "Help at the front desk." || a.OwningApp != "desk" {
		t.Errorf("the app's agent after the update: %v %+v", ok, a)
	}
	src2, _ := appsource.Load("v", "desk")
	if src2.Exported != d.Update.Exported || src2.Exported == src.Exported {
		t.Errorf("the version taken did not move: %+v (was %+v)", src2, src)
	}
	dir, _ := appRepoDir("v", "desk")
	if up, _ := appRepoGit(dir, "rev-parse", appUpstreamRef); strings.TrimSpace(up) != d.Update.Commit {
		t.Errorf("the history's version taken is %q, not the update's %q", strings.TrimSpace(up), d.Update.Commit)
	}
	// Its owner's change, committed at once: the app's, then what it owns.
	log, _ := AppHistoryLog("v", "desk", 0)
	took := false
	for _, c := range log[:2] {
		if c.Author != "v" || c.Agent != "" {
			t.Errorf("the update is not its owner's own change: %+v", c)
		}
		took = took || c.Subject == "Take the update from the author"
	}
	if !took {
		t.Errorf("the history does not say the update was taken: %+v", log[:2])
	}
	if st, _ := appRepoGit(dir, "status", "--porcelain"); strings.TrimSpace(st) != "" || appHistoryHasWaiting("v", "desk") {
		t.Errorf("the update left work waiting in the history: %q", st)
	}
	if res, _ := ImportArtifactBundleAsUser(RootDB, first, "v"); !strings.Contains(outcomeOf(res, "custom_app").Detail, "before the version of it you have") {
		t.Errorf("an older export: %+v", res)
	}

	// Both change the heading: the draft has the author's, and says so.
	live.Sections = updateTestPage("V's desk", "Kept by v")
	SaveAppSpec(live)
	changeUApp(t, "Desk, third", "")
	res, _ = ImportArtifactBundleAsUser(RootDB, export(), "v")
	if o := outcomeOf(res, "custom_app"); o.Status != "updated" || !strings.Contains(o.Detail, "page.html") || !strings.Contains(o.Detail, "Anvil") {
		t.Fatalf("an update that clashes: %+v", res)
	}
	d, _ = appdraft.Load("v", "desk", "v")
	if d.Update == nil || strings.Join(d.Update.Clashed, ",") != "page.html" {
		t.Fatalf("what clashed: %+v", d.Update)
	}
	if d.Owned != nil {
		t.Errorf("an update that leaves what the app owns as it is changes it in the draft: %v", d.Owned)
	}
	if h := appPageHTML(d.Spec); !strings.Contains(h, "Desk, third") || !strings.Contains(h, "Kept by v") {
		t.Errorf("the clashing draft is not the author's lines with v's others:\n%s", h)
	}
	if live, _ := LoadAppSpec("v", "desk"); !strings.Contains(appPageHTML(live), "V's desk") {
		t.Error("a clashing update changed the live app")
	}
	if b, err := appRepoGit(dir, "rev-parse", "refs/heads/"+appUpdateBranch); err != nil || strings.TrimSpace(b) != d.Update.Commit {
		t.Errorf("the update's version is not waiting on its branch for Anvil: %q %v", b, err)
	}

	// Imported with its history, the version taken is the app as imported
	// on top of it.
	if _, err := ImportArtifactBundleAsUser(RootDB, first, "x"); err != nil {
		t.Fatal(err)
	}
	if _, why, _ := (appHistoryArtifact{}).ImportArtifact(nil, hist, "x"); why != "" {
		t.Fatalf("x's history: %q", why)
	}
	xdir, _ := appRepoDir("x", "desk")
	head, _ := appRepoGit(xdir, "rev-parse", "HEAD")
	if up, _ := appRepoGit(xdir, "rev-parse", appUpstreamRef); strings.TrimSpace(up) != strings.TrimSpace(head) || strings.TrimSpace(head) == "" {
		t.Errorf("x's version taken is %q, not the app as imported on its history %q", up, head)
	}

	// An app of the same slug taken from another copy, or made here.
	SaveAppSpec(AppSpec{Owner: "w", Slug: "desk", Name: "Desk", RecordKey: "id", Sections: updateTestPage("W", "w"), Page: json.RawMessage(`{"title":"Desk","sections":[]}`)})
	if res, _ := ImportArtifactBundleAsUser(RootDB, export(), "w"); !strings.Contains(outcomeOf(res, "custom_app").Detail, "already exists") {
		t.Errorf("an app made here, of the same slug: %+v", res)
	}
	appsource.Save("w", "desk", appsource.Source{Copy: "another", Exported: src.Exported})
	if res, _ := ImportArtifactBundleAsUser(RootDB, export(), "w"); !strings.Contains(outcomeOf(res, "custom_app").Detail, "another copy") {
		t.Errorf("an app taken from another copy: %+v", res)
	}
}

// Without a history to merge from, an update is taken whole, and only into
// an app unchanged since it was taken.
func TestAnUpdateWithoutHistoryNeedsTheAppUnchanged(t *testing.T) {
	export := updateTestApp(t)
	prev := appGit
	appGit = func() string { return "" }
	t.Cleanup(func() { appGit = prev })
	if _, err := ImportArtifactBundleAsUser(RootDB, export(), "v"); err != nil {
		t.Fatal(err)
	}
	changeUApp(t, "Front desk", "")
	second := export()
	res, _ := ImportArtifactBundleAsUser(RootDB, second, "v")
	if o := outcomeOf(res, "custom_app"); o.Status != "updated" {
		t.Fatalf("an update to an app unchanged since: %+v", res)
	}
	d, ok := appdraft.Load("v", "desk", "v")
	if !ok || !strings.Contains(appPageHTML(d.Spec), "Front desk") || d.BaseCommit != "" {
		t.Fatalf("the update's draft: %v %+v", ok, d)
	}
	appdraft.Delete("v", "desk", "v")

	vSpec, _ := LoadAppSpec("v", "desk")
	vSpec.Sections = updateTestPage("Desk", "Kept by v")
	SaveAppSpec(vSpec)
	res, _ = ImportArtifactBundleAsUser(RootDB, second, "v")
	if o := outcomeOf(res, "custom_app"); o.Status != "skipped" || !strings.Contains(o.Detail, "changed here since you took it") {
		t.Errorf("an update to an app changed since, with nothing to merge with: %+v", res)
	}
	if _, ok := appdraft.Load("v", "desk", "v"); ok {
		t.Error("a refused update left a draft")
	}
}

func appHistoryHasWaiting(owner, slug string) bool {
	_, waiting := AppHistoryWaiting(owner, slug)
	return waiting
}
