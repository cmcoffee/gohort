package docs

import (
	"strings"
	"testing"
)

const navDoc = "# Runbook\n\n## Contents\n\n1. [Install](#install)\n2. [Run It](#1-run-it)\n\n## Install\n\nSee [running it](#run-it) and [the old page](#gone).\n\n```\n## not a heading\n[nope](#nope)\n```\n\n## 1. Run It\n\nText.\n"

// The navigation is read off the markdown: headings with gohort's anchors,
// each in-page link and the heading it means (by anchor, else by its words),
// and the table of contents' heading. Fenced code is not navigation.
func TestAnalyzeNav(t *testing.T) {
	n := AnalyzeNav(navDoc)
	if n.Contents != "Contents" {
		t.Fatalf("contents = %q", n.Contents)
	}
	got := map[string]string{}
	for _, l := range n.Links {
		got[l.Anchor] = l.Heading
	}
	if got["install"] != "Install" || got["1-run-it"] != "1. Run It" || got["gone"] != "" {
		t.Fatalf("links = %+v", n.Links)
	}
	if _, inCode := got["nope"]; inCode {
		t.Fatal("a link inside fenced code was read as navigation")
	}
	if n.Empty() || !AnalyzeNav("# Plain\n\nNo links.").Empty() {
		t.Fatal("Empty is wrong")
	}
	ins := NavInstruction(n)
	for _, want := range []string{"\"Contents\"", "table of contents", "points at the heading \"Install\"", "\"the old page\" (#gone) points at no heading", "Do not change any other wording"} {
		if !strings.Contains(ins, want) {
			t.Errorf("instruction lacks %q:\n%s", want, ins)
		}
	}
	if NavInstruction(DocNav{}) != "" {
		t.Fatal("an instruction for nothing to rebuild")
	}
}
