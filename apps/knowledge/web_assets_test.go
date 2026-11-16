package knowledge

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	for _, want := range []string{`<div id="docs-keeper"`, `c.curator_agent`, `'Curation ↓'`, "'app-knowledge-curator'", `scrollIntoView`} {
		if !strings.Contains(page, want) {
			t.Errorf("the collection page is missing %s", want)
		}
	}
}

// The collection pages' scripts parse. One stray quote in a string stops the
// whole script, and with it everything the page draws by hand: the documents
// list went blank when a "plan's" closed a single-quoted string early.
func TestTheCollectionPagesScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	pages := map[string]string{"list": documentsListBody + documentsListAssets, "detail": documentsDetailBody + documentsDetailAssets}
	for name, page := range pages {
		found := scripts.FindAllStringSubmatch(page, -1)
		if len(found) == 0 {
			t.Errorf("the %s page has no script", name)
		}
		for i, m := range found {
			f := filepath.Join(t.TempDir(), name+".js")
			if err := os.WriteFile(f, []byte(m[1]), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
				t.Errorf("the %s page's script %d does not parse: %v\n%s", name, i, err, out)
			}
		}
	}
}

// The page offers each viewer only what their level may do (the record's
// "access"): every control is marked with the level it needs, and the styles
// hide it from anyone below, and from everyone until the record says.
func TestTheCollectionPageOffersOnlyWhatTheViewerMayDo(t *testing.T) {
	page := documentsDetailBody + documentsDetailAssets
	for _, want := range []string{
		`.docs-detail:not([data-access]) [data-need],`,
		`.docs-detail[data-access="read"] [data-need],`,
		`.docs-detail[data-access="add"] [data-need="maintain"],`,
		`.docs-detail[data-access="add"] [data-need="owner"],`,
		`.docs-detail[data-access="maintain"] [data-need="owner"] { display: none !important; }`,
		`id="docs-rename" class="ui-row-btn" data-need="owner"`,
		`id="docs-delete" class="ui-row-btn danger" data-need="owner"`,
		`<div class="docs-desc-actions" data-need="maintain">`,
		`id="docs-keeper" class="docs-detail-meta" data-need="owner"`,
		`<div class="docs-section" data-need="add">
    <div class="docs-section-title">Upload</div>`,
		`<div class="docs-section" data-need="add">
    <div class="docs-section-title">From the web</div>`,
		`id="docs-articles-input" data-need="maintain"`,
		`id="docs-articles-add" class="ui-row-btn" data-need="maintain"`,
		`id="docs-audit" class="ui-row-btn" data-need="maintain"`,
		`id="docs-bulk-bar" data-need="maintain"`,
		"header.setAttribute('data-need', 'maintain');",
		"sel.setAttribute('data-need', 'maintain');",
		"del.textContent = 'Remove';\n          del.setAttribute('data-need', 'maintain');",
		"del.setAttribute('data-need', 'maintain');",
		"document.querySelector('.docs-detail').setAttribute('data-access', access);",
		"descBox.readOnly = (access === 'read' || access === 'add');",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the collection page is missing %s", want)
		}
	}
	// Export and search are reads: nothing hides them.
	for _, id := range []string{`id="docs-export" class="ui-row-btn" title=`, `<input id="docs-q" type="text"`} {
		if !strings.Contains(page, id) {
			t.Errorf("a reader's control is marked or gone: %s", id)
		}
	}
}
