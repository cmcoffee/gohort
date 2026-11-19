package orchestrate

import (
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
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
