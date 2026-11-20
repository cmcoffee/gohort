package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/core/appdraft"
	"github.com/cmcoffee/gohort/core/apptools"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// makeLive puts turn's draft of its app slug live, as its commit does
// (app_draft.go), for a test whose subject is the live app. Nothing when the
// turn has no draft of it.
func makeLive(t *testing.T, turn *chatTurn, slug string) {
	t.Helper()
	spec, _, found := turn.appForTurn(slug)
	if !found {
		return
	}
	d, ok := turn.appDraftOf(spec.Owner, slug)
	if !ok {
		return
	}
	if _, err := turn.goLive(d, "test fixture"); err != nil {
		t.Fatalf("making %s live: %v", slug, err)
	}
}

// turnApp is the app slug as turn sees it: its draft when it has one, else
// the live app (appForTurn), for a test of what a save of the turn's did.
func turnApp(turn *chatTurn, slug string) (AppSpec, bool) {
	s, _, ok := turn.appForTurn(slug)
	return s, ok
}

func draftTestPage(word string) []any {
	return []any{map[string]any{"id": "page", "kind": "html", "html": "<!DOCTYPE html><html><body><h1>" + word + "</h1></body></html>"}}
}

// Builder's change to a live app is its draft: the live app keeps serving
// what it had, the result says the change is not live and how it goes live,
// and a commit before the draft is verified is refused. Verified and
// committed, the change is live, the draft is gone, and the history holds
// the work as the person's, with Builder credited, on its live branch.
func TestBuildersChangeIsADraftUntilCommitted(t *testing.T) {
	turn, _ := folderTestTurn(t)
	turn.agent.Name = "Builder"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	makeLive(t, turn, "shelf")

	out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "shelf", "sections": draftTestPage("Novels"), "note": "Say novels"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NOT LIVE YET") || !strings.Contains(out, `app_def(action="commit", id="shelf"`) {
		t.Errorf("the result does not say the change is a draft and how it goes live:\n%s", out)
	}
	if live, _ := LoadAppSpec("u", "shelf"); !strings.Contains(string(live.Sections), "Books") {
		t.Fatalf("Builder's change reached the live app before its commit: %s", live.Sections)
	}
	if d, ok := appdraft.Load("u", "shelf", "u"); !ok || !strings.Contains(string(d.Spec.Sections), "Novels") {
		t.Fatalf("the change is not in the draft: %v", ok)
	}
	if got, _ := turnApp(turn, "shelf"); !strings.Contains(string(got.Sections), "Novels") {
		t.Error("Builder reads the live app, not its draft")
	}

	if _, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Say novels"}); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("an unverified draft was committed: %v", err)
	}
	draft, _ := turnApp(turn, "shelf")
	turn.recordVerdict(draft, true, "ok")
	// Sharing it meanwhile is not a change the draft would undo: the commit
	// goes ahead, and the app stays shared as it was set.
	shared, _ := LoadAppSpec("u", "shelf")
	shared.SharedWith = []string{"v"}
	SaveAppSpec(shared)
	out, err = turn.appDefCommit(map[string]any{"id": "shelf", "message": "Say novels\n\nThe shelf holds novels now."})
	if err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("the verified commit: %v %s", err, out)
	}
	live, _ := LoadAppSpec("u", "shelf")
	if !strings.Contains(string(live.Sections), "Novels") {
		t.Fatalf("the commit did not make the change live: %s", live.Sections)
	}
	if live.Verify == nil || !live.Verify.Current(live) || !live.Verify.Pass {
		t.Errorf("the draft's verdict did not go live with it: %+v", live.Verify)
	}
	if len(live.SharedWith) != 1 || live.SharedWith[0] != "v" {
		t.Errorf("the commit undid the sharing set on the live app: %v", live.SharedWith)
	}
	if _, ok := appdraft.Load("u", "shelf", "u"); ok {
		t.Error("the draft is still there after its commit")
	}
	if appGit() != "" {
		dir, _ := appRepoDir("u", "shelf")
		if log, _ := appRepoGit(dir, "log", "-1", "--format=%an|%s|%b"); !strings.HasPrefix(log, "u|Say novels|") || !strings.Contains(log, "Co-Authored-By: Builder") {
			t.Errorf("the history's last commit: %q", log)
		}
		if b, _ := appRepoGit(dir, "symbolic-ref", "--short", "HEAD"); strings.TrimSpace(b) != AppHistoryBranch {
			t.Errorf("the history is on %q", strings.TrimSpace(b))
		}
	}
}

// A new app is only a draft until its first commit: nowhere live, listed as
// not live yet, deleted as a draft. Committed, it is live, and its history
// starts with that work under Builder's message.
func TestANewAppIsLiveAtItsFirstCommit(t *testing.T) {
	turn, _ := folderTestTurn(t)
	turn.agent.Name = "Builder"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadAppSpec("u", "shelf"); ok {
		t.Fatal("a new app was live before its first commit")
	}
	if out, _ := turn.appDefList(); !strings.Contains(out, `"slug":"shelf"`) || !strings.Contains(out, "not live yet") {
		t.Errorf("the list does not show the new app as not live yet: %s", out)
	}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err == nil {
		t.Error("a second create of the same app was not refused")
	}
	draft, _ := turnApp(turn, "shelf")
	turn.recordVerdict(draft, true, "ok")
	out, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Make a shelf of books\n\nA list to keep."})
	if err != nil || !strings.Contains(out, "live now") || !strings.Contains(out, "first version") {
		t.Fatalf("the first commit: %v %s", err, out)
	}
	if _, ok := LoadAppSpec("u", "shelf"); !ok {
		t.Fatal("the first commit did not make the app live")
	}
	if appGit() != "" {
		dir, _ := appRepoDir("u", "shelf")
		if log, _ := appRepoGit(dir, "log", "--format=%an|%s|%b"); !strings.HasPrefix(log, "u|Make a shelf of books|") || !strings.Contains(log, "Co-Authored-By: Builder") || strings.Contains(log, "Start of") {
			t.Errorf("the new app's history does not start with its work:\n%s", log)
		}
	}

	// A draft of an app never live is all a delete takes.
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Scratch", "sections": draftTestPage("Tmp")}, false); err != nil {
		t.Fatal(err)
	}
	if out, err := turn.appDefDelete(map[string]any{"id": "scratch"}); err != nil || !strings.Contains(out, "never live") {
		t.Errorf("deleting a draft-only app: %v %s", err, out)
	}
	if _, ok := appdraft.Load("u", "scratch", "u"); ok {
		t.Error("the deleted draft is still there")
	}
}

// mergeFixture is a live app Shelf with two data sources and a page, and
// Builder's verified draft of it changing what its first data source prints.
func mergeFixture(t *testing.T) *chatTurn {
	t.Helper()
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	// With the host registered: a merge reads the merged folder back as the
	// app the way an Install does.
	turn, _ := copyTestTurn(t)
	turn.agent.Name = "Builder"
	page := "<!DOCTYPE html><html><body>\n<h1>Books</h1>\n<p>Shelf</p>\n</body></html>"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf",
		"sections":     []any{map[string]any{"id": "page", "kind": "html", "html": page}},
		"data_sources": []any{map[string]any{"name": "count", "script": "print('{\"n\": 1}')"}, map[string]any{"name": "top", "script": "print('{\"t\": 1}')"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	makeLive(t, turn, "shelf")
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "shelf",
		"data_sources": []any{map[string]any{"name": "count", "script": "print('{\"n\": 2}')"}, map[string]any{"name": "top", "script": "print('{\"t\": 1}')"}}}, true); err != nil {
		t.Fatal(err)
	}
	draft, _ := turnApp(turn, "shelf")
	turn.recordVerdict(draft, true, "ok")
	return turn
}

// liveChange is the person changing the live app themselves while Builder's
// draft is open: committed at once, as theirs.
func liveChange(t *testing.T, change func(*AppSpec)) {
	t.Helper()
	live, _ := LoadAppSpec("u", "shelf")
	change(&live)
	SaveAppSpec(live)
}

func scriptOf(s AppSpec, name string) string {
	for _, ds := range s.DataSources {
		if ds.Name == name {
			return ds.Script
		}
	}
	return ""
}

// A live app changed since the draft started is merged into the draft at
// its commit, as git merges. A change to its words only goes live with the
// draft, the draft's verdict standing; a change to how it works is merged
// into the draft, and the commit waits for the two to be verified together.
func TestADraftTakesInWhatChangedLiveSinceItStarted(t *testing.T) {
	turn := mergeFixture(t)
	liveChange(t, func(s *AppSpec) { s.Desc = "Changed by hand." })
	out, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Count two"})
	if err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("a draft with only words changed live since: %v %s", err, out)
	}
	live, _ := LoadAppSpec("u", "shelf")
	if live.Desc != "Changed by hand." || scriptOf(live, "count") != "print('{\"n\": 2}')" {
		t.Errorf("the merge lost one side: desc %q, count %q", live.Desc, scriptOf(live, "count"))
	}

	// Builder's next draft, and the person changes another script live.
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "shelf",
		"data_sources": []any{map[string]any{"name": "count", "script": "print('{\"n\": 3}')"}, map[string]any{"name": "top", "script": "print('{\"t\": 1}')"}}}, true); err != nil {
		t.Fatal(err)
	}
	draft, _ := turnApp(turn, "shelf")
	turn.recordVerdict(draft, true, "ok")
	liveChange(t, func(s *AppSpec) {
		for i := range s.DataSources {
			if s.DataSources[i].Name == "top" {
				s.DataSources[i].Script = "print('{\"t\": 9}')"
			}
		}
	})
	if _, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Count three"}); err == nil || !strings.Contains(err.Error(), "Verify your draft again") {
		t.Fatalf("a merge that changed how the app works went live untested: %v", err)
	}
	merged, _ := turnApp(turn, "shelf")
	if scriptOf(merged, "count") != "print('{\"n\": 3}')" || scriptOf(merged, "top") != "print('{\"t\": 9}')" {
		t.Fatalf("the draft does not hold both: count %q top %q", scriptOf(merged, "count"), scriptOf(merged, "top"))
	}
	if now, _ := LoadAppSpec("u", "shelf"); scriptOf(now, "count") != "print('{\"n\": 2}')" {
		t.Error("the stopped commit changed the live app")
	}
	turn.recordVerdict(merged, true, "ok")
	if out, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Count three"}); err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("the verified merge: %v %s", err, out)
	}
	if now, _ := LoadAppSpec("u", "shelf"); scriptOf(now, "count") != "print('{\"n\": 3}')" || scriptOf(now, "top") != "print('{\"t\": 9}')" {
		t.Errorf("the merge went live without one side: count %q top %q", scriptOf(now, "count"), scriptOf(now, "top"))
	}
}

// The draft and the live app changing the same lines is a conflict: the
// commit names what overlaps, both stay as they were, and the person can
// have Builder drop its draft to start again from the live app.
func TestADraftThatOverlapsALiveChangeIsAConflict(t *testing.T) {
	turn := mergeFixture(t)
	liveChange(t, func(s *AppSpec) {
		for i := range s.DataSources {
			if s.DataSources[i].Name == "count" {
				s.DataSources[i].Script = "print('{\"n\": 7}')"
			}
		}
	})
	_, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Count two"})
	if err == nil || !strings.Contains(err.Error(), "change the same lines in data/count.py") || !strings.Contains(err.Error(), `action="discard"`) {
		t.Fatalf("an overlap: %v", err)
	}
	if now, _ := LoadAppSpec("u", "shelf"); scriptOf(now, "count") != "print('{\"n\": 7}')" {
		t.Errorf("the conflict changed the live app: %q", scriptOf(now, "count"))
	}
	if d, _ := turnApp(turn, "shelf"); scriptOf(d, "count") != "print('{\"n\": 2}')" {
		t.Errorf("the conflict changed the draft: %q", scriptOf(d, "count"))
	}
	if out, err := turn.appDefDiscard(map[string]any{"id": "shelf"}); err != nil || !strings.Contains(out, "as it is live") {
		t.Fatalf("discard: %v %s", err, out)
	}
	if d, _ := turnApp(turn, "shelf"); scriptOf(d, "count") != "print('{\"n\": 7}')" {
		t.Error("after the discard Builder still reads its draft")
	}
}

// With no history to merge from (no git, or a draft started before the app
// had one), a draft older than the live app is refused at its commit, and
// both stay as they were.
func TestADraftWithNoHistoryToMergeIsRefused(t *testing.T) {
	turn := mergeFixture(t)
	d, _ := appdraft.Load("u", "shelf", "u")
	d.BaseCommit = ""
	appdraft.Save(d, true)
	liveChange(t, func(s *AppSpec) { s.Desc = "Changed by hand." })
	if _, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Count two"}); err == nil || !strings.Contains(err.Error(), "cannot be merged") {
		t.Fatalf("a draft older than the live app, with nothing to merge from: %v", err)
	}
	if now, _ := LoadAppSpec("u", "shelf"); scriptOf(now, "count") != "print('{\"n\": 1}')" {
		t.Error("the refused commit changed the live app")
	}
}

// A person's own change (an Install from Anvil, on a turn of no agent's)
// is live at once and makes no draft.
func TestAPersonsOwnChangeIsLiveAtOnce(t *testing.T) {
	turn, _ := folderTestTurn(t)
	person := &chatTurn{user: "u", changeAsk: approveEveryChange}
	if _, err := person.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadAppSpec("u", "shelf"); !ok {
		t.Fatal("a person's own new app is not live")
	}
	if _, ok := appdraft.Load("u", "shelf", "u"); ok {
		t.Error("a person's own change made a draft")
	}
	_ = turn
}

// A browser check of Builder's change loads the draft, through its preview,
// as the person changing it: never the live app.
func TestABrowserCheckOfAChangeLoadsTheDraft(t *testing.T) {
	turn, _ := folderTestTurn(t)
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	makeLive(t, turn, "shelf")
	prevB, prevAddr, prevPC := BrowserCheckPage, WebListenAddr, appdraft.PreviewCookie
	t.Cleanup(func() { BrowserCheckPage, WebListenAddr, appdraft.PreviewCookie = prevB, prevAddr, prevPC })
	WebListenAddr = "127.0.0.1:9"
	var previewed []AppSpec
	appdraft.PreviewCookie = func(editor string, spec AppSpec) (PageCheckCookie, bool) {
		previewed = append(previewed, spec)
		return PageCheckCookie{Name: "test_preview", Value: editor}, true
	}
	var carried []string
	BrowserCheckPage = func(target string, cookies []PageCheckCookie, probe string, _ bool) (*PageCheckReport, error) {
		for _, c := range cookies {
			carried = append(carried, c.Name)
		}
		return &PageCheckReport{}, nil
	}
	if _, err := turn.appDefPatchHTML(map[string]any{"id": "shelf", "find": "Books", "replace": "Novels"}); err != nil {
		t.Fatal(err)
	}
	if len(previewed) == 0 || !strings.Contains(string(previewed[0].Sections), "Novels") {
		t.Fatalf("the check did not load the draft through its preview: %d previews", len(previewed))
	}
	if !slices.Contains(carried, "test_preview") {
		t.Errorf("the browser carried %v, not the preview", carried)
	}
}

// A draft's verdict holds while what it judged stands: a change of its words
// keeps it, and a change of what the app does drops it, so a commit cannot
// make an untested change live on an earlier pass.
func TestADraftsVerdictHoldsOnlyWhileWhatItJudgedStands(t *testing.T) {
	turn, _ := folderTestTurn(t)
	prevB := BrowserCheckPage
	BrowserCheckPage = nil
	t.Cleanup(func() { BrowserCheckPage = prevB })
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	draft, _ := turnApp(turn, "shelf")
	turn.recordVerdict(draft, true, "ok")
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "shelf", "description": "A shelf of books."}, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := turnApp(turn, "shelf"); d.Verify == nil || !d.Verify.Current(d) {
		t.Error("a change of its words dropped the draft's verdict")
	}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "shelf", "sections": draftTestPage("Novels")}, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := turnApp(turn, "shelf"); d.Verify != nil && d.Verify.Current(d) {
		t.Error("a change of what the app does kept the draft's verdict")
	}
	if _, err := turn.appDefCommit(map[string]any{"id": "shelf", "message": "Say novels"}); err == nil {
		t.Error("an untested change was committed on an earlier pass")
	}
}

// The person makes Builder's draft live themselves, from My Apps, before it
// was verified if they choose: live through the same save, the draft gone,
// and the history saying who made it live and that it was not verified, with
// Builder credited for the work.
func TestThePersonMakesADraftLive(t *testing.T) {
	turn, _ := folderTestTurn(t)
	turn.agent.Name = "Builder"
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	makeLive(t, turn, "shelf")
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "shelf", "sections": draftTestPage("Novels"), "note": "Say novels"}, true); err != nil {
		t.Fatal(err)
	}
	d, _ := appdraft.Load("u", "shelf", "u")
	if d.Spec.Verify != nil && d.Spec.Verify.Current(d.Spec) {
		t.Fatal("the fixture's draft is verified; the case is one that is not")
	}
	out, err := appdraft.MakeLive(d, "u")
	if err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("make live: %v %s", err, out)
	}
	if live, _ := LoadAppSpec("u", "shelf"); !strings.Contains(string(live.Sections), "Novels") {
		t.Fatalf("the draft is not live: %s", live.Sections)
	}
	if _, ok := appdraft.Load("u", "shelf", "u"); ok {
		t.Error("the draft is still there")
	}
	if appGit() != "" {
		dir, _ := appRepoDir("u", "shelf")
		log, _ := appRepoGit(dir, "log", "-1", "--format=%an|%s|%b")
		if !strings.HasPrefix(log, "u|Make Builder's changes live|") || !strings.Contains(log, "before Builder had verified") || !strings.Contains(log, "Say novels") || !strings.Contains(log, "Co-Authored-By: Builder") {
			t.Errorf("the history's record of it: %q", log)
		}
	}
}

// The person making a draft live takes the merge with what changed live
// since it started as it is: their choice, through the same save.
func TestThePersonMakesAMergedDraftLive(t *testing.T) {
	mergeFixture(t)
	liveChange(t, func(s *AppSpec) {
		for i := range s.DataSources {
			if s.DataSources[i].Name == "top" {
				s.DataSources[i].Script = "print('{\"t\": 9}')"
			}
		}
	})
	d, _ := appdraft.Load("u", "shelf", "u")
	if out, err := appdraft.MakeLive(d, "u"); err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("make live over a live change: %v %s", err, out)
	}
	if now, _ := LoadAppSpec("u", "shelf"); scriptOf(now, "count") != "print('{\"n\": 2}')" || scriptOf(now, "top") != "print('{\"t\": 9}')" {
		t.Errorf("the merge went live without one side: count %q top %q", scriptOf(now, "count"), scriptOf(now, "top"))
	}
}

// Builder changes what a live app owns in its draft, through the app's
// folder: its direct tools refuse, a publish keeps the folder's files in
// the draft and the live agent as it was, the draft's Ask reads the drafted
// agent, and the commit makes it live with the app.
func TestALiveAppsOwnPartsChangeInItsDraft(t *testing.T) {
	turn, udb := copyTestTurn(t)
	turn.agent = AgentRecord{ID: "seed-builder", Name: "Builder"}
	copyTestApp(t, turn, nil)
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Old prompt.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadAppSpec("u", "desk")
	spec.AgentID = clerk.ID
	SaveAppSpec(spec)

	sess := &ToolSession{}
	wireAppToolSpace(sess, "u")
	if err := sess.SaveAppTool("desk", TempTool{Name: "lookup", Description: "Look it up.", CommandTemplate: "echo hi"}); err == nil || !strings.Contains(err.Error(), "NOT CHANGED") || !strings.Contains(err.Error(), "checkout") {
		t.Errorf("a direct change to a live app's tool was not sent to its folder: %v", err)
	}
	if appPartDraftRefusal("u", "desk", "", "agent") != nil {
		t.Error("someone other than Builder was refused here")
	}

	if _, err := turn.appDefCheckout(map[string]any{"id": "desk"}); err != nil {
		t.Fatal(err)
	}
	// A folder published as it was changes nothing the app owns.
	if out, err := turn.appDefPublish(map[string]any{"dir": "desk.app"}); err != nil || strings.Contains(out, "changed in your draft") {
		t.Fatalf("publishing the folder as it was: %v %s", err, out)
	}
	if d, _ := appdraft.Load("u", "desk", "u"); d.Owned != nil {
		t.Error("a folder as it was left what the app owns changed in the draft")
	}
	ws, _ := turn.turnWorkspace()
	files, _ := filepath.Glob(filepath.Join(ws, "desk.app", "agents", "*.json"))
	if len(files) != 1 {
		t.Fatalf("the checkout's agents: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	os.WriteFile(files[0], []byte(strings.Replace(string(b), "Old prompt.", "New prompt.", 1)), 0o644)
	out, err := turn.appDefPublish(map[string]any{"dir": "desk.app"})
	if err != nil || !strings.Contains(out, "changed in your draft") {
		t.Fatalf("publish: %v %s", err, out)
	}
	if live, _ := loadAgent(udb, clerk.ID); live.OrchestratorPrompt != "Old prompt." {
		t.Fatalf("the publish changed the live app's agent: %q", live.OrchestratorPrompt)
	}
	d, ok := appdraft.Load("u", "desk", "u")
	if !ok || d.Owned == nil {
		t.Fatal("the draft does not hold what the app owns")
	}
	live, _ := loadAgent(udb, clerk.ID)
	if got := draftAgentFor(appdraft.WithOwned(context.Background(), d.Owned), live); got.OrchestratorPrompt != "New prompt." || got.ID != clerk.ID {
		t.Errorf("the draft's Ask reads %q (id %s)", got.OrchestratorPrompt, got.ID)
	}
	if got := draftAgentFor(context.Background(), live); got.OrchestratorPrompt != "Old prompt." {
		t.Error("an Ask with no draft read the drafted agent")
	}
	// A drafted file that leaves the app out is still the app's agent.
	loose := map[string][]byte{}
	for k, v := range d.Owned {
		loose[k] = []byte(strings.Replace(string(v), `"owning_app": "desk"`, `"owning_app": ""`, 1))
	}
	if got := draftAgentFor(appdraft.WithOwned(context.Background(), loose), live); got.OwningApp != "desk" || got.OrchestratorPrompt != "New prompt." {
		t.Errorf("a drafted agent file without its app: app %q, prompt %q", got.OwningApp, got.OrchestratorPrompt)
	}

	draft, _ := turnApp(turn, "desk")
	turn.recordVerdict(draft, true, "ok")
	if out, err := turn.appDefCommit(map[string]any{"id": "desk", "message": "Give the clerk a new prompt"}); err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("commit: %v %s", err, out)
	}
	if now, _ := loadAgent(udb, clerk.ID); now.OrchestratorPrompt != "New prompt." {
		t.Errorf("the commit did not make the drafted agent live: %q", now.OrchestratorPrompt)
	}
	if _, ok := appdraft.Load("u", "desk", "u"); ok {
		t.Error("the draft is still there after its commit")
	}
}

// A new app not live yet takes its own tools straight away, as Builder
// builds it; a draft's tool space is its files', in place of the live one.
func TestADraftsToolsAreItsOwn(t *testing.T) {
	turn, _ := copyTestTurn(t)
	turn.agent = AgentRecord{ID: "seed-builder", Name: "Builder"}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Shelf", "sections": draftTestPage("Books")}, false); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{}
	wireAppToolSpace(sess, "u")
	if err := sess.SaveAppTool("shelf", TempTool{Name: "lookup", Description: "Look it up.", CommandTemplate: "echo hi"}); err != nil {
		t.Fatalf("a new app's own tool, while it is a draft: %v", err)
	}
	files := appOwnedFiles("u", "shelf")
	var toolFile string
	for f := range files {
		if strings.HasPrefix(f, "tools/") && strings.HasSuffix(f, ".json") {
			toolFile = f
		}
	}
	if toolFile == "" {
		t.Fatalf("no tool file among %v", files)
	}
	files[toolFile] = []byte(strings.Replace(string(files[toolFile]), "Look it up.", "Look it up, drafted.", 1))
	tools, ok := draftAppTools(appdraft.WithOwned(context.Background(), files))
	if !ok || len(tools) != 1 || tools[0].Tool.Description != "Look it up, drafted." {
		t.Errorf("the draft's tool space: %v %+v", ok, tools)
	}
	if _, ok := draftAppTools(context.Background()); ok {
		t.Error("a tool space with no draft read one")
	}
}

// ownedDeskFixture is a live app Desk whose own agent Clerk answers with
// "Old prompt.", and a tool of its own, lookup.
func ownedDeskFixture(t *testing.T) (*chatTurn, Database, AgentRecord) {
	t.Helper()
	turn, udb := copyTestTurn(t)
	turn.agent = AgentRecord{ID: "seed-builder", Name: "Builder"}
	copyTestApp(t, turn, nil)
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", Description: "Old desc.", OrchestratorPrompt: "Old prompt.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	if err := apptools.SaveBy("u", "desk", TempTool{Name: "lookup", Description: "Look it up.", CommandTemplate: "echo hi"}, "Builder"); err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadAppSpec("u", "desk")
	spec.AgentID = clerk.ID
	SaveAppSpec(spec)
	return turn, udb, clerk
}

// draftedFiles is what Desk owns as files, with old turned into new.
func draftedFiles(old, new string) map[string][]byte {
	files := appOwnedFiles("u", "desk")
	for k, v := range files {
		files[k] = []byte(strings.ReplaceAll(string(v), old, new))
	}
	return files
}

// Where a draft is tried, what the app owns is read as the draft has it:
// the app's Ask asks the drafted agent, an agent check of the draft carries
// the draft's files to the Ask, and the app's agents load the drafted tool
// space. Without the draft, each reads the live app.
func TestADraftsAskChecksAndToolsReadItsOwnParts(t *testing.T) {
	turn, udb, clerk := ownedDeskFixture(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Answered.", Repeat: true}}}
	turn.app.LLM, turn.app.LeadLLM = fake, fake
	files := draftedFiles("Old prompt.", "New prompt.")

	if _, _, err := turn.app.AppAgentAsk(appdraft.WithOwned(context.Background(), files), "u", "", clerk.ID, "hello", false); err != nil {
		t.Fatal(err)
	}
	if p := fake.Prompt(0); !strings.Contains(p, "New prompt.") {
		t.Errorf("the draft's Ask did not ask the drafted agent:\n%s", p)
	}
	if _, _, err := turn.app.AppAgentAsk(context.Background(), "u", "", clerk.ID, "hello", false); err != nil {
		t.Fatal(err)
	}
	if p := fake.Prompt(1); !strings.Contains(p, "Old prompt.") {
		t.Errorf("an Ask outside the draft did not ask the live agent:\n%s", p)
	}

	// An agent check of the draft.
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"id": "desk", "description": "A desk."}, true); err != nil {
		t.Fatal(err)
	}
	d, _ := appdraft.Load("u", "desk", "u")
	d.Owned = files
	d = appdraft.Save(d, false)
	prev := appscript.AppAsk
	t.Cleanup(func() { appscript.AppAsk = prev })
	carried := false
	appscript.AppAsk = func(ctx context.Context, spec AppSpec, caller, prompt string, _ bool) (string, error) {
		_, carried = appdraft.OwnedFrom(ctx)
		return "ok", nil
	}
	turn.runAgentCheck(d.Spec, appcheck.Check{Name: "answers", Agent: clerk.ID, Ask: "hello", Expect: []appcheck.Expect{{Contains: "ok"}}})
	if !carried {
		t.Error("an agent check of the draft did not carry the draft's files")
	}

	// The app's agent, run on the draft, loads its drafted tools.
	toolFiles := draftedFiles("Look it up.", "Look it up, drafted.")
	load := func(ctx context.Context) string {
		tt := &chatTurn{user: "u", udb: udb, agent: clerk, ctx: ctx, agentOwnTools: map[string]bool{}}
		sess := &ToolSession{}
		tt.loadAppSpaceTools(sess, "u")
		for _, x := range sess.TempTools {
			if x.Name == "lookup" {
				return x.Description
			}
		}
		return ""
	}
	if got := load(appdraft.WithOwned(context.Background(), toolFiles)); got != "Look it up, drafted." {
		t.Errorf("the app's agent on the draft loaded lookup as %q", got)
	}
	if got := load(context.Background()); got != "Look it up." {
		t.Errorf("the app's agent outside the draft loaded lookup as %q", got)
	}
}

// What the app owns, changed live (by Anvil) while Builder's draft changes
// another part of it, is merged: the commit keeps both, and never undoes the
// live change with the draft's older copy of the agent. (The same lines, or
// lines next to each other, changed on both sides are a conflict, as git
// has it.)
func TestADraftsOwnPartsMergeWithALiveChange(t *testing.T) {
	if appGit() == "" {
		t.Skip("git is not installed")
	}
	turn, udb, clerk := ownedDeskFixture(t)
	if _, err := turn.appDefCheckout(map[string]any{"id": "desk"}); err != nil {
		t.Fatal(err)
	}
	ws, _ := turn.turnWorkspace()
	files, _ := filepath.Glob(filepath.Join(ws, "desk.app", "tools", "*.json"))
	if len(files) != 1 {
		t.Fatalf("the checkout's tools: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	os.WriteFile(files[0], []byte(strings.Replace(string(b), "Look it up.", "Look it up, drafted.", 1)), 0o644)
	if _, err := turn.appDefPublish(map[string]any{"dir": "desk.app"}); err != nil {
		t.Fatal(err)
	}
	// Anvil changes the agent's description live meanwhile.
	live, _ := loadAgent(udb, clerk.ID)
	live.Description, live.appEdit = "Live desc.", "Anvil"
	if _, err := saveAgent(udb, live); err != nil {
		t.Fatal(err)
	}
	draft, _ := turnApp(turn, "desk")
	turn.recordVerdict(draft, true, "ok")
	out, err := turn.appDefCommit(map[string]any{"id": "desk", "message": "Give the clerk a new prompt"})
	if err != nil && strings.Contains(err.Error(), "Verify your draft again") {
		draft, _ = turnApp(turn, "desk")
		turn.recordVerdict(draft, true, "ok")
		out, err = turn.appDefCommit(map[string]any{"id": "desk", "message": "Give the clerk a new prompt"})
	}
	if err != nil || !strings.Contains(out, "live now") {
		t.Fatalf("commit: %v %s", err, out)
	}
	if now, _ := loadAgent(udb, clerk.ID); now.Description != "Live desc." {
		t.Errorf("the commit undid the live change to the agent: %q", now.Description)
	}
	if at, ok := apptools.ByName("u", "desk", "lookup"); !ok || at.Tool.Description != "Look it up, drafted." {
		t.Errorf("the draft's tool is not live: %+v", at.Tool.Description)
	}
}

// Builder's direct change to a live app's own agent is refused, and before
// the owner is asked to approve it: a card for a change that would be
// refused anyway waits on the owner for nothing.
func TestARefusedPartChangeAsksNobody(t *testing.T) {
	_, udb, clerk := ownedDeskFixture(t)
	asked := 0
	sess := &ToolSession{Username: "u", DB: udb, AgentID: "seed-builder",
		ApproveChange: func(kind, name, detail string, op ChangeOp) string { asked++; return "" }}
	_, err := updateAgentTool{}.RunWithSession(map[string]any{"id": clerk.ID, "orchestrator_prompt": "New prompt."}, sess)
	if err == nil || !strings.Contains(err.Error(), "NOT CHANGED") {
		t.Fatalf("a direct change to a live app's agent: %v", err)
	}
	if asked != 0 {
		t.Errorf("the owner was asked %d time(s) to approve a change refused anyway", asked)
	}
	if now, _ := loadAgent(udb, clerk.ID); now.OrchestratorPrompt != "Old prompt." {
		t.Errorf("the refused change was saved: %q", now.OrchestratorPrompt)
	}
}

// In a draft's preview and checks, a script's run_pipeline runs the app's
// own pipeline as the draft has it, a renamed one still found as the same
// pipeline and keeping who it is, and its agent steps ask the app's agent
// as the draft has it; a script's run_agent asks that agent too. With no
// draft on the request, both are as they are live.
func TestADraftsScriptsRunItsPipelineAndAgent(t *testing.T) {
	turn, udb, clerk := ownedDeskFixture(t)
	pinRootDB(t)
	pipe, err := SavePipelineDefBy(udb, PipelineDef{Owner: "u", Name: "Brief", OwningApp: "desk",
		Stages: []PipelineStage{{Name: "a", Kind: StageAgent, Agent: "Clerk", Prompt: "Brief: {input}"}}}, "create", "Builder")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := SavePipelineDefBy(udb, PipelineDef{Owner: "u", Name: "Digest", OwningApp: "desk",
		Stages: []PipelineStage{{Name: "a", Kind: StageWorker, Prompt: "Digest: {input}"}}}, "create", "Builder")
	if err != nil {
		t.Fatal(err)
	}
	files := appOwnedFiles("u", "desk")
	var pipeFile, agentFile string
	for f := range files {
		switch {
		case strings.HasPrefix(f, "pipelines/") && strings.Contains(string(files[f]), "Brief: {input}"):
			pipeFile = f
		case strings.HasPrefix(f, "agents/"):
			agentFile = f
		}
	}
	if pipeFile == "" || agentFile == "" {
		t.Fatalf("the app's files: %v", files)
	}
	files[pipeFile] = []byte(strings.NewReplacer(`"Brief"`, `"Morning brief"`, "Brief: {input}", "Drafted brief: {input}").Replace(string(files[pipeFile])))
	files[agentFile] = []byte(strings.Replace(string(files[agentFile]), "Old prompt.", "New prompt.", 1))
	ctx := appdraft.WithOwned(context.Background(), files)

	got := draftPipelineFor(ctx, pipe)
	if got.Name != "Morning brief" || got.ID != pipe.ID || got.OwningApp != "desk" || got.Owner != "u" {
		t.Errorf("the pipeline as the draft has it: %+v", got)
	}
	if d := draftPipelineFor(ctx, digest); d.Name != "Digest" || d.ID != digest.ID {
		t.Errorf("another of the app's pipelines took the drafted one's file: %+v", d)
	}
	if other := draftPipelineFor(ctx, PipelineDef{ID: "x", Owner: "u", Name: "Brief"}); other.ID != "x" || other.OwningApp != "" {
		t.Errorf("a pipeline that is not the app's took the draft's: %+v", other)
	}

	app := turn.app
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Done.", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	sawCall := func(from int, prompt, stage string) bool {
		for i := from; i < fake.Calls(); i++ {
			if strings.Contains(fake.Config(i).SystemPrompt, prompt) && (stage == "" || strings.Contains(fake.Prompt(i), stage)) {
				return true
			}
		}
		return false
	}
	n := fake.Calls()
	if _, _, err := app.AppPipelineRun(ctx, "u", "u", "desk", "", pipe.ID, "today"); err != nil {
		t.Fatal(err)
	}
	if !sawCall(n, "New prompt.", "Drafted brief: today") || sawCall(n, "Old prompt.", "") {
		t.Error("a draft's run_pipeline did not run the draft's pipeline and agent")
	}
	n = fake.Calls()
	if _, _, err := app.AppPipelineRun(context.Background(), "u", "u", "desk", "", pipe.ID, "today"); err != nil {
		t.Fatal(err)
	}
	if !sawCall(n, "Old prompt.", "Brief: today") || sawCall(n, "", "Drafted brief") {
		t.Error("a live run_pipeline ran the draft's")
	}
	n = fake.Calls()
	if _, _, _, err := app.AppAgentRun(ctx, "u", "u", "desk", clerk.ID, "", "Hello.", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !sawCall(n, "New prompt.", "") || sawCall(n, "Old prompt.", "") {
		t.Error("a draft's run_agent did not ask the draft's agent")
	}
}
