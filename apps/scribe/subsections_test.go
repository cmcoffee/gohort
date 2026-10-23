package scribe

import (
	"strings"
	"testing"
)

// A numbered guide numbers each section's ### headings 1.1, 1.2 and lists
// them under it in the contents: in the viewer, where each entry links to its
// heading, and in the markdown every export and publish is built from. A
// heading inside a code fence is not one, deeper ones are left alone, and a
// heading already numbered is not numbered twice.
func TestSubsectionsAreNumberedAndListed(t *testing.T) {
	body := "Intro.\n\n### Install\n\nSteps.\n\n```\n### not a heading\n```\n\n#### Detail\n\n### 2.2 `Configure` it\n"
	numbered, subs := numberSubheadings(body, 2)
	if len(subs) != 2 || subs[0].Num != "2.1" || subs[0].Text != "Install" || subs[1].Num != "2.2" || subs[1].Text != "`Configure` it" {
		t.Fatalf("subsections: %+v", subs)
	}
	for _, want := range []string{"### 2.1 Install", "### 2.2 `Configure` it", "### not a heading", "#### Detail"} {
		if !strings.Contains(numbered, want) {
			t.Errorf("missing %q in:\n%s", want, numbered)
		}
	}
	if strings.Contains(numbered, "2.2 2.2") {
		t.Error("a heading already numbered was numbered again")
	}

	g := Guide{Title: "Guide", Sections: []Section{
		{ID: "a", Title: "Start", Markdown: "Hi.", Order: 1},
		{ID: "b", Title: "Setup", Markdown: body, Order: 2},
		{ID: "c", Title: "Use", Markdown: "Go.", Order: 3},
	}}
	md := renderGuideMarkdown(g)
	if !strings.Contains(md, "2. Setup\n   - 2.1 Install\n   - 2.2 `Configure` it\n3. Use") {
		t.Errorf("the contents lists the subsections under their section:\n%s", md)
	}
	if !strings.Contains(md, "## 2. Setup\n\nIntro.\n\n### 2.1 Install") {
		t.Errorf("the body carries the numbers:\n%s", md)
	}
	html := renderGuideHTML(g, false)
	if !strings.Contains(html, `<ol class="guide-toc-sub"><li><a href="#21-install">2.1 Install</a></li>`) {
		t.Errorf("the viewer's contents links each subsection:\n%s", html)
	}
	if strings.Contains(html, `href="#"`) {
		t.Errorf("an entry with no anchor: inline markup in a heading must still find it:\n%s", html)
	}

	// A short guide has no contents and no numbers, its headings as written.
	short := Guide{Title: "Short", Sections: []Section{{ID: "a", Title: "One", Markdown: "### Sub\n", Order: 1}}}
	if out := renderGuideMarkdown(short); strings.Contains(out, "1.1") || !strings.Contains(out, "### Sub") {
		t.Errorf("an unnumbered guide is left alone:\n%s", out)
	}
}
