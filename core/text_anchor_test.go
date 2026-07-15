package core

import (
	"strings"
	"testing"
)

// NormalizeHeadingLinks rewrites internal anchors from their link text so
// LLM slug drift can't break TOC / cross-reference links.
func TestNormalizeHeadingLinks(t *testing.T) {
	md := `Intro paragraph.

## Table of Contents
- [1. Setup](#1-setup)
- [2. Distribution & Version](#2-distribution--version)
- [3. API Keys (sensitive)](#wrong-guess)

## 1. Setup

See [3. API Keys (sensitive)](#3-api-keys-sensitive) for the secrets.
Also see [an external doc](https://example.com/#frag) and [no such heading](#missing).

## 2. Distribution & Version

## 3. API Keys (sensitive)
`
	out := NormalizeHeadingLinks(md)

	// Every link whose text matches a heading gets the renderer's slug —
	// including ones where the model guessed the anchor wrong.
	for _, want := range []string{
		"[1. Setup](#1-setup)",
		// The renderer's slug keeps the double dash from " & " — the
		// normalizer must land on the renderer's exact spelling.
		"[2. Distribution & Version](#2-distribution--version)",
		"[3. API Keys (sensitive)](#3-api-keys-sensitive)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("normalized output missing %q", want)
		}
	}
	if strings.Contains(out, "#wrong-guess") {
		t.Errorf("stale anchor survived normalization:\n%s", out)
	}
	// Non-matching and external links pass through untouched.
	if !strings.Contains(out, "[no such heading](#missing)") {
		t.Error("link with no matching heading should be untouched")
	}
	if !strings.Contains(out, "(https://example.com/#frag)") {
		t.Error("external link should be untouched")
	}
}

// Headings inside fenced code blocks don't mint slugs, so links can't be
// retargeted at phantom headings.
func TestNormalizeHeadingLinksSkipsCodeFences(t *testing.T) {
	md := "## Real\n\n```\n## Fake Heading\n```\n\n[Fake Heading](#kept-as-is)\n"
	out := NormalizeHeadingLinks(md)
	if !strings.Contains(out, "[Fake Heading](#kept-as-is)") {
		t.Errorf("code-fenced heading must not capture links:\n%s", out)
	}
}
