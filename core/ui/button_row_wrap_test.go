package ui

// A row of buttons wraps; it never runs past the edge of its box.
//
// .ui-row-btn does not shrink (flex-shrink: 0, a tap-sized minimum), so any
// flex row of them that cannot wrap overflows as soon as it is wider than its
// container: a row of seven type tabs in an edit dialog, a Delete +
// Cancel + Save footer in a narrow modal, anything on a phone. It kept being
// written that way because every row looked fine at the width it was built at.

import (
	"regexp"
	"strings"
	"testing"
)

// buttonRowSelector is the naming the stylesheet uses for a row whose job is
// to hold buttons.
var buttonRowSelector = regexp.MustCompile(`(?i)(actions|footer|btns|buttons|btn-row|-bar\b|toolbar|tabs|controls)`)

// rowsAllowedNoWrap are button rows that keep to one line on purpose, each
// for a reason wrapping would break.
var rowsAllowedNoWrap = map[string]string{
	".ui-page-tabs":         "scrolls sideways (overflow-x: auto) so the tabs stay on the header row",
	".ui-toolbar-menu.open": "a dropdown menu; its buttons are block-level, one per line",
	".ui-rows-actions":      "a fixed-width column in a table row, sized to its buttons",
}

func TestButtonRowsWrap(t *testing.T) {
	rule := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	var bad []string
	for _, m := range rule.FindAllStringSubmatch(runtimeCSS, -1) {
		sel := strings.TrimSpace(m[1])
		if i := strings.LastIndex(sel, "*/"); i >= 0 {
			sel = strings.TrimSpace(sel[i+2:])
		}
		body := strings.ReplaceAll(m[2], " ", "")
		if !buttonRowSelector.MatchString(sel) || !strings.Contains(body, "display:flex") {
			continue
		}
		if strings.Contains(body, "flex-wrap") || strings.Contains(body, "flex-direction:column") || strings.Contains(body, "overflow-x:auto") {
			continue
		}
		if _, ok := rowsAllowedNoWrap[sel]; ok {
			continue
		}
		bad = append(bad, sel)
	}
	if len(bad) > 0 {
		t.Errorf("these button rows cannot wrap, so their buttons run past the edge of the box when it is narrow; add flex-wrap: wrap (or list the row in rowsAllowedNoWrap with the reason it must stay on one line):\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// The shared modal's footer is the class every hand-built modal reuses, so it
// is the one that must wrap above all.
func TestModalFooterWraps(t *testing.T) {
	// The rule itself, at the start of a line: a modal that adjusts its own
	// footer (".ui-x .ui-modal-footer") is not the shared one.
	m := regexp.MustCompile(`(?m)^\.ui-modal-footer\s*\{([^}]*)\}`).FindStringSubmatch(runtimeCSS)
	if m == nil || !strings.Contains(strings.ReplaceAll(m[1], " ", ""), "flex-wrap:wrap") {
		t.Fatal(".ui-modal-footer must exist and wrap")
	}
	if !strings.Contains(runtimeJS, "bar.className = 'ui-modal-footer'") {
		t.Error("uiOpenModal's footer should use .ui-modal-footer rather than its own inline flex row")
	}
}

// On a phone a crowded row of buttons is one line that scrolls, never a
// wrap: wrapped, its last button (Delete, most often) dropped to a line of
// its own. These rows hold four or more buttons on common pages (table and
// card-list rows, the pipeline and report actions, the editors' toolbars),
// so each must be in the phone rule that keeps it whole.
func TestCrowdedRowsStayOneLineOnAPhone(t *testing.T) {
	i := strings.Index(runtimeCSS, "A ROW OF BUTTONS ON A PHONE")
	if i < 0 {
		t.Fatal("runtime.css has no one-line rule for crowded button rows")
	}
	block := runtimeCSS[i:]
	if j := strings.Index(block, "\n}\n"); j >= 0 {
		block = block[:j]
	}
	for _, c := range []string{".ui-row-actions", ".ui-card-controls", ".ui-toolbar-strip",
		".ui-pl-actions", ".ui-pl-modal-h-actions", ".ui-agent-actions"} {
		if !strings.Contains(block, c) {
			t.Errorf("%s is a crowded button row, and the phone rule does not keep it to one line", c)
		}
	}
	if !strings.Contains(block, "flex-wrap: nowrap") {
		t.Error("the phone rule for crowded rows no longer stops them wrapping")
	}
	// One line must not widen the page: a row whose one-line width set the
	// least its containers could be pushed My Apps' buttons off the screen.
	// Contained, a row's width must come from its container: a table row with
	// two-line cells does not stretch its items, and the row actions came out
	// zero wide with every button gone.
	if !regexp.MustCompile(`:is\(\.ui-row-actions[^)]*\)\s*\{[^}]*width:\s*100%`).MatchString(block) {
		t.Error("contained row actions no longer take their container's full width; in a table row with two-line cells they collapse to nothing")
	}
	if !strings.Contains(block, "contain: inline-size") {
		t.Error("the one-line rows no longer keep their width to what they are given; their buttons will push the page wider than the screen")
	}
}
