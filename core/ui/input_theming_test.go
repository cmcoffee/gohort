package ui

// An app that writes a bare <input> — a search box, a topic field — gets the
// browser default: white box, black text. On a dark theme that's a glaring
// white rectangle in the middle of the page, and it's easy to ship, because the
// toolkit's own fields carry .ui-form-input and these don't.
//
// The stylesheet carries a floor so that can't happen. These guard the floor
// and the exclusions it needs.

import (
	"regexp"
	"strings"
	"testing"
)

// baseInputRule pulls the bare-element rule (the one starting at an unqualified
// `input:not(...)` selector at the left margin) out of the stylesheet.
func baseInputRule(t *testing.T, css string) string {
	t.Helper()
	re := regexp.MustCompile(`(?ms)^input:not\([^\n]*\n(?:[^\n]*\n)*?\}`)
	m := re.FindString(css)
	if m == "" {
		t.Fatal("no base rule for bare <input> — an unstyled app input will render browser-default white")
	}
	return m
}

func TestBareInputsInheritTheTheme(t *testing.T) {
	rule := baseInputRule(t, runtimeCSS)
	for _, want := range []string{"textarea", "select", "var(--bg-2)", "var(--text)", "var(--border)"} {
		if !strings.Contains(rule, want) {
			t.Errorf("base input rule is missing %q:\n%s", want, rule)
		}
	}
	// A hardcoded color here would defeat the theme system entirely.
	if regexp.MustCompile(`#[0-9a-fA-F]{3,6}`).MatchString(rule) {
		t.Errorf("base input rule hardcodes a color instead of using a token:\n%s", rule)
	}
}

func TestNativeControlsAreExcludedFromTheFloor(t *testing.T) {
	// Painting a background on these breaks the control — a checkbox becomes an
	// empty box, a file input loses its button.
	rule := baseInputRule(t, runtimeCSS)
	for _, kind := range []string{"checkbox", "radio", "file", "range", "color"} {
		if !strings.Contains(rule, `:not([type="`+kind+`"])`) {
			t.Errorf("base input rule must exclude type=%s:\n%s", kind, rule)
		}
	}
}

func TestPlaceholdersAreReadable(t *testing.T) {
	// Theming the box without the placeholder leaves dark-on-dark text.
	css := runtimeCSS
	if !strings.Contains(css, "input::placeholder") || !strings.Contains(css, "var(--text-mute)") {
		t.Error("bare inputs need a themed ::placeholder, or the hint text goes unreadable on a dark theme")
	}
}

func TestAutofillDoesNotRepaintTheFieldWhite(t *testing.T) {
	// Chrome paints autofilled fields with its own near-white background and
	// ignores `background`. The inset shadow is the only override.
	css := runtimeCSS
	if !strings.Contains(css, ":-webkit-autofill") {
		t.Error("no autofill override — Chrome repaints a themed field white the moment it fills it")
	}
	if !strings.Contains(css, "box-shadow: 0 0 0 1000px var(--bg-2) inset") {
		t.Error("autofill override must use an inset shadow in a theme token")
	}
}

func TestToolkitFieldsStillWinOverTheFloor(t *testing.T) {
	// The floor is element selectors on purpose: every .ui-* rule outranks it on
	// specificity. If .ui-form-input ever lost its own background it would
	// silently inherit the floor instead of its intended surface.
	css := runtimeCSS
	i := strings.Index(css, ".ui-form-input,")
	if i < 0 {
		t.Fatal(".ui-form-input rule not found")
	}
	block := css[i:]
	if end := strings.Index(block, "}"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "background: var(--bg-2)") {
		t.Errorf(".ui-form-input must set its own background rather than lean on the floor:\n%s", block)
	}
}
