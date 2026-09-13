package scribe

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An article renders as title, image and one body: no contents block, no
// numbered section heading, no per-section controls even for an editor.
func TestRenderArticleHTML(t *testing.T) {
	g := newArticle("u", "Rotate the certs", "## Steps\n\nRun `renew`.")
	g.ImageURL = "https://img.example/banner.png"
	html := renderGuideHTML(g, true)
	for _, want := range []string{"Rotate the certs", `class="guide-doc-image"`, "https://img.example/banner.png", "<code>renew</code>", "guide-doc-article"} {
		if !strings.Contains(html, want) {
			t.Errorf("article html missing %q:\n%s", want, html)
		}
	}
	for _, reject := range []string{"guide-toc", "guide-section-num", "data-guide-act"} {
		if strings.Contains(html, reject) {
			t.Errorf("article html should not carry %q:\n%s", reject, html)
		}
	}
	if !g.Private {
		t.Error("a new article should start Private")
	}
}

// setBody keeps an article at exactly one section, whatever it held before.
func TestArticleSetBodyKeepsOneSection(t *testing.T) {
	g := Guide{Kind: KindArticle, Sections: []Section{
		{ID: "b", Markdown: "second", Order: 2},
		{ID: "a", Markdown: "first", Order: 1},
	}}
	g.setBody("new body")
	if len(g.Sections) != 1 || g.Sections[0].ID != "a" || g.body() != "new body" {
		t.Errorf("setBody should keep the first section only, got %+v", g.Sections)
	}
	var empty Guide
	empty.Kind = KindArticle
	empty.setBody("x")
	if len(empty.Sections) != 1 || empty.body() != "x" {
		t.Errorf("setBody on an empty article should create its section, got %+v", empty.Sections)
	}
}

// The markdown export of an article is title, image, body — no contents.
func TestRenderArticleMarkdown(t *testing.T) {
	g := newArticle("u", "T", "body text")
	g.ImageURL = "data:image/png;base64,AAAA"
	md := renderGuideMarkdown(g)
	if !strings.HasPrefix(md, "# T\n\n![T](data:image/png;base64,AAAA)\n\nbody text") {
		t.Errorf("unexpected article markdown:\n%s", md)
	}
	if strings.Contains(md, "Contents") {
		t.Error("an article export should have no contents block")
	}
}

// A page exported from here (and TechWriter's older shape) comes back as a
// title and a markdown body with the chrome stripped.
func TestArticleFromHTML(t *testing.T) {
	page := renderGuideStandaloneHTML(newArticle("u", "Backups & restores", "## Plan\n\nNightly."), "Acme", "Acme Wiki")
	title, body := articleFromHTML(page)
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
	title, body = articleFromHTML(tw)
	if title != "Old article" || strings.Contains(body, "June 1") || !strings.Contains(body, "Install it") {
		t.Errorf("techwriter page: title=%q body=%q", title, body)
	}
}

// TechWriter's library migrates once per user: articles become private
// article-kind documents keeping their ids and images, revisions become
// History, rules carry over, and a second run copies nothing.
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
	if !g.isArticle() || !g.Private || g.Owner != "u" || g.ImageURL != "https://x/y.png" || g.Title != "Disk report" {
		t.Errorf("migrated article = %+v", g)
	}
	if g.body() != "# Disk report\n\nfull" {
		t.Errorf("body = %q", g.body())
	}
	revs := listRevisions(dst, "a1")
	if len(revs) != 2 || revs[0].ID != "r1" || revs[1].ID != "r2" || revs[0].Guide.body() != "draft" {
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

// The list label tells the two kinds apart and keeps the sharing suffix last.
func TestListLabel(t *testing.T) {
	if got := listLabel(Guide{Title: "G"}, ""); got != "G" {
		t.Errorf("guide label = %q", got)
	}
	if got := listLabel(Guide{Kind: KindArticle, Title: "A"}, " · shared"); got != "A · article · shared" {
		t.Errorf("article label = %q", got)
	}
	if got := listLabel(Guide{Kind: KindArticle}, ""); got != "Untitled article · article" {
		t.Errorf("untitled article label = %q", got)
	}
}
