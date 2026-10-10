package publish

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/docs"
)

// Through the API, Confluence gets its own navigation: the table of contents
// becomes its TOC macro, a linked heading carries an Anchor macro, an in-page
// link becomes an ac:link to that anchor, and a link to no heading keeps its
// words without the dead link. Nothing else changes.
func TestConfluenceRebuildsTheNavigation(t *testing.T) {
	md := "# Runbook\n\n## Contents\n\n1. [Install](#install)\n2. [Run](#run)\n\n## Install\n\nSee [**running**](#run) and [the old page](#gone).\n\n## Run\n\nText.\n"
	out := confluenceNav(MarkdownToConfluence(md), docs.AnalyzeNav(md))
	if !strings.Contains(out, confTOCMacro) || strings.Contains(out, ">Contents<") {
		t.Fatalf("the contents were not replaced by the TOC macro:\n%s", out)
	}
	if !strings.Contains(out, "<h2>"+confAnchorMacro("run")+"Run</h2>") {
		t.Fatalf("the linked heading has no anchor:\n%s", out)
	}
	if !strings.Contains(out, `<ac:link ac:anchor="run"><ac:link-body><strong>running</strong></ac:link-body></ac:link>`) {
		t.Fatalf("the in-page link is not an anchor link:\n%s", out)
	}
	if strings.Contains(out, `href="#`) || !strings.Contains(out, "the old page") {
		t.Fatalf("a dead link survived, or its words did not:\n%s", out)
	}
	if !strings.Contains(out, "<p>Text.</p>") {
		t.Fatalf("other content changed:\n%s", out)
	}
	if plain := MarkdownToConfluence("# A\n\nB."); confluenceNav(plain, docs.AnalyzeNav("# A\n\nB.")) != plain {
		t.Fatal("a document with no navigation was changed")
	}
}

// A target a model publishes to is handed the navigation paragraph with the
// instruction, before the document.
func TestATargetIsToldToRebuildTheNavigation(t *testing.T) {
	md := "## Contents\n\n- [Setup](#setup)\n\n## Setup\n\nGo."
	req := docs.PublishRequest{Title: "Guide", Doc: docs.PublishDoc{Markdown: md}, Nav: docs.AnalyzeNav(md)}
	ins := targetInstruction(Target{Instructions: "Create a page."}, req)
	nav := strings.Index(ins, "Navigation within the document")
	if nav < 0 || nav > strings.Index(ins, "---") {
		t.Fatalf("the navigation paragraph is missing or after the document:\n%s", ins)
	}
	if ins := agentInstruction(AgentDestination{Prompt: "File it."}, req); !strings.Contains(ins, "Navigation within the document") {
		t.Fatalf("an agent destination is not told:\n%s", ins)
	}
}
