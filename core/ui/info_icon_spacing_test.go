package ui

// The ⓘ sits next to the words it explains.
//
// The section header was a flex row with justify-content: space-between, which
// distributes free space BETWEEN the children. With a right-hand slot the icon
// drifted to the middle of the row; on a header that has no right slot — the
// bare section head builds one with only a title and an icon — it went to the
// far edge with the whole width between it and the word it belongs to, where
// it reads as an unrelated control.

import (
	"strings"
	"testing"
)

func sectionHeaderRule(t *testing.T) string {
	t.Helper()
	css := runtimeCSS
	i := strings.Index(css, ".ui-section-h {")
	if i < 0 {
		t.Fatal("no .ui-section-h rule")
	}
	j := strings.Index(css[i:], "}")
	if j < 0 {
		t.Fatal("unterminated .ui-section-h rule")
	}
	return css[i : i+j]
}

func TestTheSectionTitleAndItsIconStayTogether(t *testing.T) {
	rule := sectionHeaderRule(t)
	if strings.Contains(rule, "space-between") {
		t.Error("the header still spreads its children, which pushes the icon off the title")
	}
	if !strings.Contains(rule, "justify-content: flex-start") {
		t.Error("the header does not pack its children to the start")
	}
	// A gap on top of the icon's own margin loosens the one pair that has to
	// stay tight.
	if !strings.Contains(rule, "gap: 0;") {
		t.Error("a gap is reintroduced between the title and its icon")
	}
}

// The right-hand slot still goes right — that is what wanted space-between.
func TestTheHeadersRightSlotStillPushesRight(t *testing.T) {
	css := runtimeCSS
	i := strings.Index(css, ".ui-section-h-r {")
	if i < 0 {
		t.Fatal("no .ui-section-h-r rule")
	}
	rule := css[i : i+strings.Index(css[i:], "}")]
	if !strings.Contains(rule, "margin-left: auto") {
		t.Error("the right slot no longer pushes itself right, so it sits against the title")
	}
}
