package ui

import (
	"os"
	"strings"
	"testing"
)

// TestAskCardRenderersAreRegisteredGlobally — the clarifying-question cards
// (ask_user / ask_user_form) used to be defined into pipeline_panel's LOCAL
// renderer map, so they existed only on a page that mounted a PipelinePanel.
// AgentLoopPanel reads window.UIBlockRenderers, found nothing, and fell through
// to a status row rendering `d.text || d.title` — neither of which these blocks
// carry. A writer app showed an empty bracketed type name while the turn sat
// parked on a question nobody could answer. Reported twice.
func TestAskCardRenderersAreRegisteredGlobally(t *testing.T) {
	js := string(runtimeJS)
	for _, want := range []string{
		`window.uiRegisterBlockRenderer('ui_ask',`,
		`window.uiRegisterBlockRenderer('ui_ask_form',`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the runtime never registers %s globally; every AgentLoopPanel surface renders the ask as an empty status row", want)
		}
	}

	// And they must not go back into one panel's private map.
	raw, err := os.ReadFile("assets/runtime/40_pipeline_panel.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"blockRenderers.ui_ask =", "blockRenderers.ui_ask_form ="} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("%q is back in pipeline_panel's local map — it would render only where a PipelinePanel is mounted", banned)
		}
	}
}

// TestAskCardSubmitsToItsOwnPanel — the composer lookup was document-wide, so on
// a page with two chat panels the answer went to whichever came first in the
// DOM, regardless of which panel asked.
func TestAskCardSubmitsToItsOwnPanel(t *testing.T) {
	raw, err := os.ReadFile("assets/runtime/35_ask_cards.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if strings.Contains(src, `document.querySelector('.ui-agent-input')`) {
		t.Error("the ask card resolves the composer document-wide again; on a two-panel page it answers the wrong panel")
	}
	if !strings.Contains(src, "function uiAskPanelRoot(") {
		t.Error("uiAskPanelRoot is gone; nothing scopes the answer to the panel that asked")
	}
	// Both card types must route through the scoped submit, card first.
	if n := strings.Count(src, "uiAskSubmitAnswer(wrap,"); n != 2 {
		t.Errorf("uiAskSubmitAnswer(wrap, …) called %d time(s), want 2 (the options card and the multi-step form)", n)
	}
}

// TestBackLinkRetracesButKeepsItsHref — the back link is the page's SEMANTIC
// parent, and for a hub of peer apps that parent is the dashboard for all of
// them. Arriving at one hub page from another and pressing Back skipped the
// page you came from and dropped you at the top, so getting back to where you
// were meant pressing Forward.
//
// The behavioral half is driven under node by testdata/back_link_test.js. This
// pins the two properties that test cannot see, because they are about the
// element rather than the predicate.
func TestBackLinkRetracesButKeepsItsHref(t *testing.T) {
	raw, err := os.ReadFile("assets/runtime/99_epilogue.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	// The href stays. It is what runs with no JS, and it is what a
	// cmd/ctrl/middle-click opens in a new tab.
	if !strings.Contains(src, "href: cfg.back_url") {
		t.Error("the back link lost its href; with no JS, and on a new-tab click, it now goes nowhere")
	}
	if !strings.Contains(src, "history.go(-steps)") {
		t.Error("the back link no longer retraces; every hub page sends the reader to the dashboard again")
	}
	// Past the page's own sub-navigation, not through it. history.back() alone
	// stepped the reader back through the section rail they had just been
	// using, several presses before the arrow did what it is labelled for.
	if !strings.Contains(src, "uiPageDepth() + 1") {
		t.Error("the back link steps one entry at a time again; it will walk this page's own section rail before leaving")
	}
	// The rail's entries have to carry their depth, or there is nothing to skip
	// by. A plain location.hash set pushes an entry with no state on it.
	if strings.Contains(src, "if (slugs[si]) window.location.hash = slugs[si];") {
		t.Error("the section rail pushes unlabelled history entries again; the back link cannot tell them from a page")
	}
	// Modified clicks belong to the browser. preventDefault on all of them
	// silently eats opening the parent in a new tab, and nothing reports it.
	for _, key := range []string{"ev.metaKey", "ev.ctrlKey", "ev.shiftKey", "ev.altKey", "ev.button"} {
		if !strings.Contains(src, key) {
			t.Errorf("the back-link handler does not check %s; a modified click would be swallowed instead of opening a new tab", key)
		}
	}
}
