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
// carry. Guides showed an empty "[orchestrate_ask]" line while the turn sat
// waiting on a question nobody could answer. Twice.
func TestAskCardRenderersAreRegisteredGlobally(t *testing.T) {
	js := string(runtimeJS)
	for _, want := range []string{
		`window.uiRegisterBlockRenderer('orchestrate_ask',`,
		`window.uiRegisterBlockRenderer('orchestrate_ask_form',`,
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
	for _, banned := range []string{"blockRenderers.orchestrate_ask =", "blockRenderers.orchestrate_ask_form ="} {
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
