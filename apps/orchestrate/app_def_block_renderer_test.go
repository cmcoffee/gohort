package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// A declarative app has no ExtraHeadHTML, so the ONLY way it can register a
// custom pipeline block renderer is an html section carrying an inline
// <script>. That works only while the blob is spliced into the page (ui.Card
// revives inline scripts into the page's own window); a framed document
// registers into an iframe's window, where the panel cannot see it.
//
// TestHTMLSectionFramesFullDocument (app_def_roundtrip_test.go) already pins
// the card/frame split for LAYOUT reasons. These pin the two things that only
// matter once the fragment is carrying executable registration.

func TestHTMLFragmentCarriesRegistrationScriptVerbatim(t *testing.T) {
	const registration = `<script>window.uiRegisterBlockRenderer('verdict', function(d){return null;});</script>`
	sec, err := buildAppSection(AppSpec{Slug: "x", RecordKey: "id"},
		map[string]any{"kind": "html", "html": registration}, nil)
	if err != nil {
		t.Fatalf("building a fragment html section: %v", err)
	}
	card, ok := sec.Body.(ui.Card)
	if !ok {
		t.Fatalf("a fragment must render as ui.Card (spliced into the page); got %T", sec.Body)
	}
	// Unescaped and unmodified: components.card clones this text into a fresh
	// script node, so anything that mangles it here silently breaks execution.
	if !strings.Contains(card.HTML, "window.uiRegisterBlockRenderer('verdict'") {
		t.Fatalf("registration did not survive into the card body: %q", card.HTML)
	}
}

// The trap in the middle. isFullHTMLDocument looks for <body> ANYWHERE in the
// first 2KB, so a fragment that merely mentions the tag is reclassified as a
// whole document, framed, and its renderer never registers — with no error
// raised anywhere. An author hits this by shipping a stray tag or a comment.
func TestBareBodyTagFlipsAFragmentIntoAFrame(t *testing.T) {
	if !isFullHTMLDocument(`<div>hi</div><body>`) {
		t.Fatal("expected a stray <body> to read as a whole document")
	}
	if isFullHTMLDocument(`<div><script>window.uiRegisterBlockRenderer('v',function(){});</script></div>`) {
		t.Fatal("a plain registration fragment must not read as a whole document")
	}
}

// The ordering rule is invisible and fails silently, so the only thing standing
// between an author and a day of debugging is the help text saying so. A help
// rewrite that drops it reintroduces the trap; this notices.
func TestHTMLSectionHelpDocumentsTheOrderingRule(t *testing.T) {
	for _, want := range []string{
		"BEFORE the pipeline section", // rule 2, the ordering constraint
		"COPIES the renderer registry",
		"FRAGMENT, always", // rule 1, the frame/window constraint
		"STAGE KIND",       // what to register for
	} {
		if !strings.Contains(appDefHelpText, want) {
			t.Errorf("app_def help no longer documents %q — the html-section registration trap is undocumented again", want)
		}
	}
}
