package knowledge

import (
	"strings"
	"testing"
)

// The description's ✨ is the shared assistant, on the collection's own
// assist: the conversation every other ✨ Get assistance opens, not a
// one-shot that writes a description and cannot be steered. Empty, it opens
// already asking for a suggestion; what is kept waits for Save.
func TestTheDescriptionGetsTheSharedAssistant(t *testing.T) {
	page := documentsDetailBody + documentsDetailAssets
	if !strings.Contains(page, `id="docs-desc-suggest"`) || !strings.Contains(page, "✨ Get assistance</button>") {
		t.Error("the description's button is not Get assistance")
	}
	if strings.Contains(page, "suggest-description") || strings.Contains(page, "Suggest with AI</button>") {
		t.Error("the one-shot suggestion is still wired")
	}
	js := page[strings.Index(page, "$('#docs-desc-suggest').addEventListener"):]
	js = js[:strings.Index(js, "\n  });\n")]
	for _, want := range []string{"window.uiOpenAssist(", "'/api/collections/' + encodeURIComponent(cid) + '/assist'",
		"ask: cur.trim() ? '' :", "onAccept:", "$('#docs-desc-save').disabled"} {
		if !strings.Contains(js, want) {
			t.Errorf("the button does not %s", want)
		}
	}
	if strings.Contains(js, "saveDescription(") || strings.Contains(js, "method: 'PATCH'") {
		t.Error("keeping a draft saves it; Save is the person's")
	}
}

// The collection's header says who keeps it current and jumps to the setting,
// which sits far down the page under everything done by hand.
func TestTheHeaderSaysWhoKeepsTheCollection(t *testing.T) {
	page := documentsDetailBody + documentsDetailAssets
	for _, want := range []string{`<div id="docs-keeper"`, `c.curator_agent`, `'In charge of this collection ↓'`, `scrollIntoView`} {
		if !strings.Contains(page, want) {
			t.Errorf("the collection page is missing %s", want)
		}
	}
}
