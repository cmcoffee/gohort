package scribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// turnFixture is a guide with two sections and a chat turn's tool kit over
// it, wrapped the way handleChatSend wraps it.
func turnFixture(t *testing.T) (*Scribe, Database, *turnChanges, map[string]AgentToolDef) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	T := &Scribe{AppCore: AppCore{DB: root}}
	orch := &orchestrate.OrchestrateApp{AppCore: AppCore{DB: root}}
	saveGuideRev(udb, Guide{ID: "g1", Owner: "u", Title: "Guide", Sections: []Section{
		{ID: "a", Title: "Setup", Order: 1, Markdown: "Install it."},
		{ID: "b", Title: "Use", Order: 2, Markdown: "Run it."},
	}}, "new")
	sc := coauthorScope{Ctx: context.Background(), UDB: udb, Orch: orch, User: "u", CanEdit: true, Guide: "g1"}
	tc := newTurnChanges(&orchestrate.AppBlockPublisher{}, func() (Guide, Database, bool) {
		g, oudb, _, ok := T.openGuide(sc)
		return g, oudb, ok
	})
	defs := map[string]AgentToolDef{}
	for _, td := range tc.wrap(T.coauthorTools(sc)) {
		defs[td.Tool.Name] = td
	}
	return T, udb, tc, defs
}

func titles(g Guide) string {
	var out []string
	for _, s := range g.sorted() {
		out = append(out, s.Title)
	}
	return strings.Join(out, ", ")
}

// A turn's changes are listed in order on one card, and its Undo puts the
// guide back the way the turn found it, as a revision of its own.
func TestATurnListsItsChangesAndUndoesThemAtOnce(t *testing.T) {
	T, udb, tc, defs := turnFixture(t)
	run := func(name string, args map[string]any) {
		t.Helper()
		if _, err := defs[name].Handler(context.Background(), args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A read changes nothing and starts nothing.
	run("list_sections", map[string]any{})
	if tc.before != "" {
		t.Error("a read pinned the guide as if it had changed it")
	}
	run("delete_section", map[string]any{"section_title": "Setup"})
	run("add_section", map[string]any{"section_title": "Install", "markdown": "Get it."})
	run("rename_section", map[string]any{"section_title": "Use", "new_title": "Running"})
	// A failed change adds nothing to the card.
	if _, err := defs["delete_section"].Handler(context.Background(), map[string]any{"section_title": "Nope"}); err == nil {
		t.Fatal("deleting a missing section should fail")
	}

	blk := tc.block()
	want := "Removed section: Setup\nAdded section: Install\nRenamed section: Use → Running"
	if blk.Type != turnChangesBlock || blk.Data["changes"] != want || blk.Text != "3 changes" {
		t.Fatalf("card: %+v", blk)
	}
	if g, _ := loadGuide(udb, "g1"); titles(g) != "Running, Install" {
		t.Fatalf("after the turn: %s", titles(g))
	}
	// Nothing saved the guide since its last revision, so that revision is
	// the one Undo goes back to, with no extra one written.
	if revs := listRevisions(udb, "g1"); blk.Data["rev"] != revs[0].ID || len(revs) != 4 {
		t.Errorf("the turn should start from the newest revision: rev %s, %d revisions", blk.Data["rev"], len(revs))
	}

	w := httptest.NewRecorder()
	T.handleRestore(w, httptest.NewRequest(http.MethodPost, "/scribe/restore?id=g1&rev="+blk.Data["rev"], nil), udb, "u")
	if w.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if g, _ := loadGuide(udb, "g1"); titles(g) != "Setup, Use" || g.Sections[0].Markdown != "Install it." {
		t.Errorf("undo should put the guide back as the turn found it: %s", titles(g))
	}
}

// A save made outside History (an editor's) is not lost to the Undo: the
// guide as the turn found it gets a revision of its own before the first
// change, and the Undo goes back to that rather than past it.
func TestUndoGoesBackToTheGuideTheTurnFound(t *testing.T) {
	T, udb, tc, defs := turnFixture(t)
	g, _ := loadGuide(udb, "g1")
	g.Sections[1].Markdown = "Run it, edited by hand."
	saveGuide(udb, g)

	if _, err := defs["edit_section"].Handler(context.Background(), map[string]any{"section_title": "Use", "markdown": "Rewritten."}); err != nil {
		t.Fatal(err)
	}
	revs := listRevisions(udb, "g1")
	if tc.before == revs[0].ID {
		t.Fatal("the Undo would go back past the hand edit")
	}
	w := httptest.NewRecorder()
	T.handleRestore(w, httptest.NewRequest(http.MethodPost, "/scribe/restore?id=g1&rev="+tc.before, nil), udb, "u")
	if g, _ := loadGuide(udb, "g1"); g.Sections[1].Markdown != "Run it, edited by hand." {
		t.Errorf("the hand edit was lost: %q", g.Sections[1].Markdown)
	}
}

// The curator's unattended runs never deleted a section: the tool asked, and
// nobody was there. With the asking gone they are not given it.
func TestUnattendedRunsAreNotGivenDelete(t *testing.T) {
	if got := withoutDelete([]AgentToolDef{{Tool: Tool{Name: "delete_section"}}, {Tool: Tool{Name: "edit_section"}}}); len(got) != 1 || got[0].Tool.Name != "edit_section" {
		t.Errorf("withoutDelete kept %v", got)
	}
	for _, f := range []string{"curator.go", "coauthor.go"} {
		src, _ := os.ReadFile(f)
		if !strings.Contains(string(src), "tools := withoutDelete(T.coauthorTools(") {
			t.Errorf("%s builds an unattended kit with delete_section in it", f)
		}
	}
}

// A picture the author draws carries no writing, and the rule says so on the
// turns that can draw.
func TestThePictureRuleKeepsWordsOutOfPictures(t *testing.T) {
	for _, want := range []string{"no writing", "quoted phrase", "caption"} {
		if !strings.Contains(guidePictureRule, want) {
			t.Errorf("the picture rule is missing %q", want)
		}
	}
	if strings.Contains(guidePictureRule, "—") {
		t.Error("prompts take no em-dashes")
	}
	src, _ := os.ReadFile("web.go")
	if !strings.Contains(string(src), "\"generate_image\")\n\t\t\tagent.OrchestratorPrompt += guidePictureRule") {
		t.Error("the rule should ride the turns that are given generate_image")
	}
}
