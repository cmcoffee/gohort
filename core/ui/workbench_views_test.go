package ui

// On a phone a panel with a document and an assistant (the workbench, the
// article editor, the code editor) shows them one at a time, behind a switch in
// the drawer header. Stacked, each got a sliver of a small screen and neither
// was readable (the document sat in about a third of the screen under the chat).

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPanelsShowOnePaneAtATimeOnAPhone(t *testing.T) {
	phone := regexp.MustCompile(`(?s)/\* --- Pane switch \(makePaneSwitch\) ---.*?@media \(max-width: 700px\) \{.*?\n\}`).FindString(runtimeCSS)
	if phone == "" {
		t.Fatal("the pane switch's phone block is gone from runtime.css")
	}
	if !strings.Contains(phone, ".ui-pane-off { display: none !important; }") {
		t.Error("a switched-off pane must hide on a phone")
	}
	if !strings.Contains(runtimeCSS, ".ui-pane-switch { display: none; }") {
		t.Error("the switch must stay hidden on a wide screen, where both panes show")
	}
	for _, f := range []string{"70_misc.js", "60_article_editor.js", "50_code_editor_panel.js"} {
		js := readRuntimeFile(t, f)
		if !strings.Contains(js, "makePaneSwitch({") || !strings.Contains(js, "drawer.mobileHdr.appendChild(panes.el);") {
			t.Errorf("%s does not put its panes behind the switch", f)
		}
	}
	wb := readRuntimeFile(t, "70_misc.js")
	for _, want := range []string{"cfg.viewer_label || 'Document'", "cfg.chat_label || 'Assistant'"} {
		if !strings.Contains(wb, want) {
			t.Errorf("workbench lost %q", want)
		}
	}
	// Picking a record from the drawer is a request to read it.
	if !regexp.MustCompile(`drawer\.closeDrawer\(\);\s*panes\.show\('viewer'\);`).MatchString(wb) {
		t.Error("picking an item should land on the viewer")
	}
	// The stacked 45% slices are gone from the phone layouts.
	if strings.Contains(phone, "45%") {
		t.Error("a phone pane is back to a stacked slice")
	}
}

// makePaneSwitch itself: one pane on, the rest switched off, and the dot that
// says a reply arrived while you were reading the other pane.
func TestPaneSwitchBehaves(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "00_prelude.js")
	i := strings.Index(src, "  function makePaneSwitch(opts) {")
	if i < 0 {
		t.Fatal("makePaneSwitch is gone")
	}
	end := strings.Index(src[i:], "\n  }\n")
	fn := src[i : i+end+len("\n  }\n")]
	harness := `
function cls() { var s = {}; return {
  toggle: function(c, on) { if (on) s[c] = 1; else delete s[c]; },
  add: function(c) { s[c] = 1; }, remove: function(c) { delete s[c]; },
  contains: function(c) { return !!s[c]; } }; }
function el(tag, attrs, kids) {
  return {attrs: attrs || {}, kids: kids || [], classList: cls(), _on: {},
    setAttribute: function(k, v) { this.attrs[k] = v; },
    addEventListener: function(k, f) { this._on[k] = f; },
    appendChild: function(c) { this.kids.push(c); return c; }};
}
var obs = null, replies = 0;
global.window = {MutationObserver: function(f) { obs = f; this.observe = function() {}; },
  dispatchEvent: function() {}};
global.Event = function() {};
` + fn + `
var doc = el('div'), chat = el('div');
chat.offsetParent = {};
chat.querySelectorAll = function() { return {length: replies}; };
var sw = makePaneSwitch({
  panes: [{key: 'doc', label: 'Doc', els: [doc]}, {key: 'chat', label: 'Chat', els: [chat, null]}],
  watch: {key: 'chat', el: chat, selector: '.reply'},
});
var docBtn = sw.el.kids[0], chatBtn = sw.el.kids[1];
if (!docBtn.classList.contains('active') || chat.classList.contains('ui-pane-off') !== true || doc.classList.contains('ui-pane-off'))
  throw new Error('the first pane should show and the second be switched off');
// History loading before the chat was ever opened does not light the dot.
chat.offsetParent = null; replies = 3; obs();
if (chatBtn.classList.contains('unread')) throw new Error('history lit the dot');
// Visit the chat, come back, and a new reply lights it.
chatBtn._on.click(); chat.offsetParent = {};
if (doc.classList.contains('ui-pane-off') !== true || chatBtn.attrs['aria-selected'] !== 'true') throw new Error('switching did not swap the panes');
docBtn._on.click(); chat.offsetParent = null;
obs();
if (chatBtn.classList.contains('unread')) throw new Error('no new reply yet the dot lit');
replies = 4; obs();
if (!chatBtn.classList.contains('unread')) throw new Error('a reply in the hidden chat should light the dot');
chatBtn._on.click();
if (chatBtn.classList.contains('unread')) throw new Error('opening the chat should clear the dot');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "pane.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("makePaneSwitch does not hold:\n%s", out)
	}
}

// On a phone the viewer toolbar keeps one row and moves what does not fit into
// a More menu. The moved buttons must still follow the selection, which is
// only true if the enable pass looks in the menu as well as the bar.
func TestWorkbenchToolbarOverflowsOnAPhone(t *testing.T) {
	js := readRuntimeFile(t, "70_misc.js")
	for _, want := range []string{
		"class: 'ui-wb-action-wrap ui-wb-more'",
		"window.matchMedia('(max-width: 700px)')",
		"var scopes = [actionBar, headActions, moreMenu];",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("workbench toolbar overflow lost %q", want)
		}
	}
	// The More button is never disabled by the selection, or an empty
	// workbench on a phone could not reach its library actions.
	if !regexp.MustCompile(`moreBtn = el\('button', \{[^}]*'data-ui-lib-action': '1'`).MatchString(js) {
		t.Error("the More button must be exempt from selection gating")
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 700px\) \{\s*\.ui-wb \{.*?\n\}`).FindString(runtimeCSS)
	if !strings.Contains(phone, ".ui-wb-actions { flex-wrap: nowrap; overflow: hidden; }") {
		t.Error("on a phone the toolbar must hold one row for the overflow to measure against")
	}
}

// A Subsection carries its title, subtitle, detail and body to the renderer.
func TestSubsectionMarshalsItsParts(t *testing.T) {
	b, err := Subsection{Title: "Shared with you", Subtitle: "Lent to you.", Detail: "More.",
		Body: Stack{}}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"type":"subsection"`, `"title":"Shared with you"`, `"subtitle":"Lent to you."`, `"detail":"More."`, `"body":{"type":"stack"`} {
		if !strings.Contains(js, want) {
			t.Errorf("subsection JSON lost %s: %s", want, js)
		}
	}
	if !strings.Contains(readRuntimeFile(t, "10_basics.js"), "components.subsection = function(cfg, ctx)") {
		t.Error("no renderer for subsection")
	}
}
