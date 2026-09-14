package ui

// A sidebar dropdown has to escape the pane it opens from.
//
// Nested in the rail it is at the mercy of every ancestor: the rail is
// overflow:hidden, on a phone it is a transformed full-screen drawer, and a
// list_position:"modal" panel puts the whole rail inside a scrolling dialog
// body. The symptom is a toggle that appears to do nothing — the menu opened,
// it just had nowhere to be, which is why it behaved differently on a phone
// than on a desktop with a tall rail and room to spare.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnchoredMenuPlacesItselfOnScreen(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "00_prelude.js")
	i := strings.Index(src, "window.uiAnchorMenu = function(")
	if i < 0 {
		t.Fatal("uiAnchorMenu is gone")
	}
	end := strings.Index(src[i:], "\n  };")
	if end < 0 {
		t.Fatal("could not bound uiAnchorMenu")
	}
	fn := src[i : i+end+len("\n  };")]

	harness := `
var appendedTo = null;
global.window = {innerWidth: 400, addEventListener: function() {}};
global.document = {body: {appendChild: function(n) { appendedTo = n; }}};
function stubMenu(width) {
  return {style: {}, offsetWidth: width};
}
function stubToggle(rect) { return {getBoundingClientRect: function() { return rect; }}; }
` + fn + `

// Left-aligned under its toggle, and out of every clipping ancestor.
var menu = stubMenu(150);
var a = window.uiAnchorMenu(stubToggle({left: 20, right: 46, bottom: 60}), menu);
if (appendedTo !== menu) throw new Error('the menu was not lifted out to the body');
if (menu.style.position !== 'fixed') throw new Error('nested positioning survives: ' + menu.style.position);
if (a.isOpen()) throw new Error('a menu starts closed');
a.open();
if (!a.isOpen()) throw new Error('open did not show it');
if (menu.style.top !== '64px') throw new Error('wrong top: ' + menu.style.top);
if (menu.style.left !== '20px') throw new Error('wrong left: ' + menu.style.left);

// Above the modal stack (1000 + depth*10) — a menu opened from inside a dialog
// has to be over it — and below toasts at 9000.
var z = parseInt(menu.style.zIndex, 10);
if (!(z > 1000 && z < 9000)) throw new Error('z-index does not clear modals and stay under toasts: ' + z);

// Right-aligned hangs off the toggle's right edge.
var m2 = stubMenu(150);
var b = window.uiAnchorMenu(stubToggle({left: 300, right: 340, bottom: 50}), m2, {align: 'right'});
b.open();
if (m2.style.left !== '190px') throw new Error('right-align wrong: ' + m2.style.left);

// A narrow screen pulls it back on rather than letting it run off the edge.
var m3 = stubMenu(150);
var c = window.uiAnchorMenu(stubToggle({left: 380, right: 396, bottom: 30}), m3);
c.open();
if (m3.style.left !== '246px') throw new Error('did not pull back on screen: ' + m3.style.left);

// And never off the left either, on a screen narrower than the menu.
global.window.innerWidth = 100;
var m4 = stubMenu(150);
var d = window.uiAnchorMenu(stubToggle({left: 10, right: 30, bottom: 20}), m4);
d.open();
if (m4.style.left !== '4px') throw new Error('went off the left edge: ' + m4.style.left);

d.close();
if (d.isOpen()) throw new Error('close did not hide it');

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "anchor.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("uiAnchorMenu does not place correctly:\n%s", out)
	}
}

// Placement lives in the helper. A stylesheet offset fights the inline value,
// and one that wins on a single axis stretches the menu across the viewport —
// which is what .ui-chat-new-wrap's right:0 would have done once left became
// inline.
func TestTheStylesheetDoesNotPlaceTheSideMenu(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("assets", "runtime.css"))
	if err != nil {
		t.Fatalf("reading runtime.css: %v", err)
	}
	css := string(b)
	i := strings.Index(css, ".ui-side-menu {")
	if i < 0 {
		t.Fatal(".ui-side-menu is gone from runtime.css")
	}
	rule := css[i : i+strings.Index(css[i:], "}")]
	for _, prop := range []string{"position:", "top:", "left:", "right:", "z-index:"} {
		if strings.Contains(rule, prop) {
			t.Errorf(".ui-side-menu sets %s — placement belongs to uiAnchorMenu, and a half-won offset stretches the menu", prop)
		}
	}
	if strings.Contains(css, ".ui-chat-new-wrap .ui-side-menu { left: auto; right: 0; }") {
		t.Error("the right-edge override is back; it fights the inline left and stretches the menu edge to edge")
	}
}

// A topbar that holds controls must be visible, and whether it holds any is a
// question about its contents, not about what the app declared.
//
// Several things land in that bar which cfg.actions knows nothing about — the
// Sessions button a list_position:"modal" panel needs, the nav dropdowns. The
// rule used to be "hide it when cfg.actions is empty", and what kept such a bar
// on screen was an unconditional force-show from a control that happened to
// always exist. When that control became conditional, a panel whose only topbar
// control was Sessions lost its way back to past conversations.
func TestTheActionBarShowsWhenItHasContents(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	if !strings.Contains(src, "actionsBar.style.display = actionsBar.childNodes.length ? '' : 'none'") {
		t.Error("the action bar's visibility is not decided by what is in it")
	}
	if strings.Contains(src, "if ((cfg.actions || []).length === 0) actionsBar.style.display = 'none'") {
		t.Error("the declaration count is back; it hides a bar that still holds the Sessions button")
	}
}
