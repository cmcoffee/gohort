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

func sideHeaderRule(t *testing.T, selector string) string {
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
		if !strings.Contains(sideHeaderRule(t, sel), "flex-wrap: wrap") {
			t.Errorf("%s does not wrap, so enough list actions push the last one out of a pane that cannot scroll", sel)
		}
	}
}

// "flex: 1" alone will not shrink below the label's own content width, which is
// what pushed the buttons out in the first place. The label has to be the thing
// that gives up room.
func TestTheListLabelCanGiveUpRoom(t *testing.T) {
	rule := sideHeaderRule(t, ".ui-tw-side-h > span:first-child")
	if !strings.Contains(rule, "min-width: 0") {
		t.Error("the list label cannot shrink below its content, so it pushes the header's buttons past the edge")
	}
	if !strings.Contains(rule, "text-overflow: ellipsis") {
		t.Error("a shrunk label must ellipsize rather than spill")
	}
}
