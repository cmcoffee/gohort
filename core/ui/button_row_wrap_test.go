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
	".ui-page-tabs":         "scrolls sideways (overflow-x: auto) so the tabs sit at a fixed offset on every app",
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
	m := regexp.MustCompile(`\.ui-modal-footer\s*\{([^}]*)\}`).FindStringSubmatch(runtimeCSS)
	if m == nil || !strings.Contains(strings.ReplaceAll(m[1], " ", ""), "flex-wrap:wrap") {
		t.Fatal(".ui-modal-footer must exist and wrap")
	}
	if !strings.Contains(runtimeJS, "bar.className = 'ui-modal-footer'") {
		t.Error("uiOpenModal's footer should use .ui-modal-footer rather than its own inline flex row")
	}
}
