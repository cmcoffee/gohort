package ui

// The list header must never clip. Its pane is overflow:hidden, so a control
// pushed past the edge is not scrolled to — it is gone, and an action you
// cannot see is one you do not have. Three declared list_actions beside the
// label, + New and the close button was enough to lose the last two.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cssRule returns the declaration block of a runtime.css rule by selector.
// Shared by the other layout-fit tests in this package.
func cssRule(t *testing.T, selector string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("assets", "runtime.css"))
	if err != nil {
		t.Fatalf("reading runtime.css: %v", err)
	}
	css := string(b)
	i := strings.Index(css, selector+" {")
	if i < 0 {
		t.Fatalf("%s is gone from runtime.css", selector)
	}
	end := strings.Index(css[i:], "}")
	if end < 0 {
		t.Fatalf("could not bound %s", selector)
	}
	return css[i : i+end]
}

// Both side headers are built by the same renderSideHeader and both can be
// handed rightExtras, so both need the room.
func TestSideHeadersWrapRatherThanClip(t *testing.T) {
	for _, sel := range []string{".ui-tw-side-h", ".ui-chat-side-h", ".ui-wb-head", ".ui-wb-head-actions"} {
		if !strings.Contains(cssRule(t, sel), "flex-wrap: wrap") {
			t.Errorf("%s does not wrap, so enough list actions push the last one out of a pane that cannot scroll", sel)
		}
	}
}

// "flex: 1" alone will not shrink below the label's own content width, which is
// what pushed the buttons out in the first place. The label has to be the thing
// that gives up room.
func TestTheListLabelCanGiveUpRoom(t *testing.T) {
	rule := cssRule(t, ".ui-tw-side-h > span:first-child")
	if !strings.Contains(rule, "min-width: 0") {
		t.Error("the list label cannot shrink below its content, so it pushes the header's buttons past the edge")
	}
	if !strings.Contains(rule, "text-overflow: ellipsis") {
		t.Error("a shrunk label must ellipsize rather than spill")
	}
}

// An action's SCOPE decides when it is usable; where it sits is a separate
// question. Both workbench bars used to disable everything in them whenever no
// record was selected, which made the actions that matter most on an EMPTY
// collection — import into it, set the rules it is written under — the two that
// could not be clicked.
func TestLibraryActionsAreNotGatedOnASelection(t *testing.T) {
	src := readRuntimeFile(t, "70_misc.js")
	if !strings.Contains(src, "if (a.scope === 'library') b.setAttribute('data-ui-lib-action', '1');") {
		t.Error("a library-scoped action is still built disabled like a record one")
	}
	// Both build sites — the list header and the viewer bar — or a library
	// action is ungated in one bar and dead in the other.
	if n := strings.Count(src, "a.scope === 'library'"); n != 2 {
		t.Errorf("the scope is honoured at %d of the 2 action build sites", n)
	}
	if !strings.Contains(src, "if (btns[i].hasAttribute('data-ui-lib-action')) continue;") {
		t.Error("the selection toggle still greys out library actions")
	}
}

// The live pill's dropdown, which is the same question one surface over: a row
// of fixed-size parts and one piece of text that has to live in what is left.
//
// Every part of a live row but the label declares flex-shrink: 0 — the app
// name, the state badge, the status, the Stop button — so the label is the
// only thing that can give, and `flex: 1` (basis 0) means it gives down to
// nothing. In a menu narrowed to a phone the row still rendered, minus the one
// field that said which session it was.
func TestLiveMenuRowKeepsItsLabel(t *testing.T) {
	label := cssRule(t, ".ui-live-label")
	if !strings.Contains(label, "flex: 1 1 7rem") {
		t.Error("the label needs a floor; with basis 0 it is the only shrinkable thing on the row and collapses to zero")
	}
	if !strings.Contains(cssRule(t, ".ui-live-item"), "flex-wrap: wrap") {
		t.Error("a label that will not shrink past its floor needs somewhere to go — the next line")
	}
	if strings.Contains(cssRule(t, ".ui-live-status"), "flex-shrink: 0") {
		t.Error("the status ellipsizes, so it can afford to shrink; refusing to is what took the label's width")
	}
}

// The menu hangs off the pill at the right edge of the header, so anything it
// cannot fit grows leftward, past the side of the screen, where nothing can
// scroll to it. A 320px floor is wider than a 320px phone has left after the
// header's padding.
func TestLiveMenuFitsANarrowViewport(t *testing.T) {
	menu := cssRule(t, ".ui-live-menu")
	for _, want := range []string{"min-width: min(320px", "max-width: min(480px"} {
		if !strings.Contains(menu, want) {
			t.Errorf("the menu's width is not capped against the viewport (%s)", want)
		}
	}
	if strings.Count(menu, "100vw") < 2 {
		t.Error("both width bounds must be measured against the viewport, not just one")
	}
}
