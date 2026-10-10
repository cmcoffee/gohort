package scribe

// Scribe keeps one kind of document: a guide. A stored article becomes one as
// it is read, the whole guide edits as one markdown page that splits back into
// sections at its ## headings, and a short guide reads as one flow (no
// contents block, no numbers) while a long one gets both.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestSectionsFromMarkdown(t *testing.T) {
	md := "Some intro.\n\n## Install\n\nRun it.\n\n```sh\n## not a heading\n```\n\n## Verify ##\n\nCheck it."
	secs := sectionsFromMarkdown(md, nil)
	if len(secs) != 3 {
		t.Fatalf("got %d sections, want 3: %+v", len(secs), secs)
	}
	if secs[0].Title != "Overview" || secs[0].Markdown != "Some intro." {
		t.Errorf("text before the first heading should be the Overview: %+v", secs[0])
	}
	if secs[1].Title != "Install" || !strings.Contains(secs[1].Markdown, "## not a heading") {
		t.Errorf("a ## inside a code fence is content, not a section: %+v", secs[1])
	}
	if secs[2].Title != "Verify" || secs[2].Order != 3 {
		t.Errorf("closing #s are trimmed and order runs 1..n: %+v", secs[2])
	}
	// A document that already has an Overview names its lead text differently.
	if s := sectionsFromMarkdown("lead\n\n## Overview\n\nx", nil); s[0].Title != "Introduction" {
		t.Errorf("lead text beside an Overview = %q", s[0].Title)
	}
	if s := sectionsFromMarkdown("   \n", nil); len(s) != 0 {
		t.Errorf("empty markdown should give no sections, got %+v", s)
	}
}

// Edited as one page and saved back, a guide keeps its sections' IDs where the
// titles survive, so History and the tools still know them.
func TestWholeGuideEditRoundTrips(t *testing.T) {
	g := Guide{Sections: []Section{
		{ID: "a", Title: "Install", Markdown: "Run it.", Order: 1},
		{ID: "b", Title: "Verify", Markdown: "Check it.", Order: 2},
	}}
	md := guideMarkdown(g)
	if md != "## Install\n\nRun it.\n\n## Verify\n\nCheck it." {
		t.Fatalf("guide as one page = %q", md)
	}
	edited := strings.Replace(md, "Check it.", "Check it twice.", 1) + "\n\n## Troubleshoot\n\nRestart."
	secs := sectionsFromMarkdown(edited, g.Sections)
	if len(secs) != 3 || secs[0].ID != "a" || secs[1].ID != "b" || secs[1].Markdown != "Check it twice." {
		t.Errorf("surviving sections should keep their IDs: %+v", secs)
	}
	if secs[2].ID == "a" || secs[2].ID == "b" || secs[2].ID == "" {
		t.Errorf("a new section needs a new ID: %+v", secs[2])
	}
}

// A stored article (and its History) reads as a guide of its ## parts.
func TestStoredArticleReadsAsGuide(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	art := Guide{ID: "x", Kind: KindArticle, Title: "Certs", ImageURL: "https://img/x.png",
		Sections: []Section{{ID: "s", Markdown: "Why.\n\n## Steps\n\nRenew.", Order: 1}}}
	udb.Set(guidesTable, "x", art)
	udb.Set(revisionsTable, "x", guideRevisions{Revisions: []GuideRevision{{ID: "r", Guide: art}}})
	g, ok := loadGuide(udb, "x")
	if !ok || g.Kind != KindGuide || len(g.Sections) != 2 || g.Sections[1].Title != "Steps" || g.ImageURL == "" {
		t.Errorf("article read as guide = %+v", g)
	}
	if revs := listRevisions(udb, "x"); len(revs) != 1 || revs[0].Guide.Kind != KindGuide || len(revs[0].Guide.Sections) != 2 {
		t.Errorf("an article's History should read as a guide too: %+v", revs)
	}
}

func TestContentsOnlyForALongGuide(t *testing.T) {
	short := newDocument("u", "Short", "## One\n\na\n\n## Two\n\nb", false)
	short.ImageURL = "https://img.example/banner.png"
	html := renderGuideHTML(short, true)
	for _, reject := range []string{"guide-toc", "guide-section-num"} {
		if strings.Contains(html, reject) {
			t.Errorf("a two-section guide should read as one flow, found %q", reject)
		}
	}
	if !strings.Contains(html, `class="guide-doc-image"`) {
		t.Error("any guide can carry a header image")
	}
	md := renderGuideMarkdown(short)
	if strings.Contains(md, "Contents") || !strings.Contains(md, "![Short](https://img.example/banner.png)") || !strings.Contains(md, "## One\n") {
		t.Errorf("short guide markdown:\n%s", md)
	}
	long := newDocument("u", "Long", "## A\n\na\n\n## B\n\nb\n\n## C\n\nc", false)
	html = renderGuideHTML(long, true)
	if !strings.Contains(html, "guide-toc") || !strings.Contains(html, "guide-section-num") {
		t.Error("a guide of three or more sections gets contents and numbers")
	}
	if md := renderGuideMarkdown(long); !strings.Contains(md, "## Contents") || !strings.Contains(md, "## 1. A") {
		t.Errorf("long guide markdown:\n%s", md)
	}
}

// What arrives from elsewhere starts Private; what the user starts does not.
func TestNewDocumentPrivacy(t *testing.T) {
	if !newDocument("u", "", "x", true).Private || newDocument("u", "", "x", false).Private {
		t.Error("newDocument should take its privacy from the caller")
	}
	if g := newDocument("u", " ", "", false); g.Title != "Untitled guide" || len(g.Sections) != 0 {
		t.Errorf("an untitled empty document = %+v", g)
	}
}

// A page exported from here (and TechWriter's older shape) comes back as a
// title and a markdown body with the chrome stripped.
func TestDocFromHTML(t *testing.T) {
	page := renderGuideStandaloneHTML(newDocument("u", "Backups & restores", "## Plan\n\nNightly.", false), "Acme", "Acme Wiki")
	title, body := docFromHTML(page)
	if title != "Backups & restores" {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(body, "Plan") || !strings.Contains(body, "Nightly") {
		t.Errorf("body lost content: %q", body)
	}
	for _, reject := range []string{"Acme Wiki", "guide-brand", "<h1"} {
		if strings.Contains(body, reject) {
			t.Errorf("body should not carry page chrome %q: %q", reject, body)
		}
	}
	tw := `<!DOCTYPE html><html><head><title>Old article</title></head><body><h1>Old article</h1><div class="meta"><span>June 1</span></div><h2>Setup</h2><p>Install it.</p></body></html>`
	title, body = docFromHTML(tw)
	if title != "Old article" || strings.Contains(body, "June 1") || !strings.Contains(body, "Install it") {
		t.Errorf("techwriter page: title=%q body=%q", title, body)
	}
}

// TechWriter's library migrates once per user: each article lands as a
// private guide keeping its id and image, revisions become History, rules
// carry over, and a second run copies nothing.
func TestMigrateTechWriterUser(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	src := UserDB(root.Bucket(techWriterBucket), "u")
	dst := UserDB(root.Bucket("guides"), "u")
	src.Set(techWriterHistoryTable, "a1", twArticle{ID: "a1", Subject: "Disk report", Body: "# Disk report\n\nfull", Date: "2026-01-02T00:00:00Z", ImageURL: "https://x/y.png"})
	src.Set(techWriterRevisionTable, "r1", twRevision{ID: "r1", ArticleID: "a1", Subject: "Disk report", Body: "draft", Date: "2026-01-01T00:00:00Z"})
	src.Set(techWriterRevisionTable, "r2", twRevision{ID: "r2", ArticleID: "a1", Subject: "Disk report", Body: "# Disk report\n\nfull", Date: "2026-01-02T00:00:00Z"})
	docs.SaveDocRules(src, techWriterRulesNS, "Never name customers.")

	if n := migrateTechWriterUser(src, dst, "u"); n != 1 {
		t.Fatalf("migrated %d, want 1", n)
	}
	g, ok := loadGuide(dst, "a1")
	if !ok {
		t.Fatal("article a1 did not land")
	}
	if g.Kind != KindGuide || !g.Private || g.Owner != "u" || g.ImageURL != "https://x/y.png" || g.Title != "Disk report" {
		t.Errorf("migrated guide = %+v", g)
	}
	if guideMarkdown(g) != "## Overview\n\n# Disk report\n\nfull" {
		t.Errorf("body = %q", guideMarkdown(g))
	}
	revs := listRevisions(dst, "a1")
	if len(revs) != 2 || revs[0].ID != "r1" || revs[1].ID != "r2" || guideMarkdown(revs[0].Guide) != "## Overview\n\ndraft" {
		t.Errorf("revisions = %+v", revs)
	}
	if got := LoadDocRules(dst, rulesNamespace); got != "Never name customers." {
		t.Errorf("rules = %q", got)
	}
	if n := migrateTechWriterUser(src, dst, "u"); n != 0 {
		t.Errorf("second run migrated %d, want 0", n)
	}
	if got := len(listGuides(dst)); got != 1 {
		t.Errorf("documents after second run = %d, want 1", got)
	}
}

func TestListLabel(t *testing.T) {
	if got := listLabel(Guide{Title: "G"}, " - shared"); got != "G - shared" {
		t.Errorf("label = %q", got)
	}
	if got := listLabel(Guide{}, ""); got != "Untitled guide" {
		t.Errorf("untitled label = %q", got)
	}
}
