package ui

import (
	"strings"
	"testing"
)

// Full-height panels size themselves as "a viewport minus the page chrome".
// The chrome used to be a hardcoded guess — 70px, 90px, 120px — and the guess
// was wrong on a phone: a 44px tap-target back link plus #ui-root's top and
// bottom padding plus the home-indicator safe area overshoot 120px, so the page
// overflowed the viewport and the composer landed below the fold. It's now
// measured at runtime (syncViewport in 99_epilogue.js) and published as
// --ui-chrome-h. The literals remain as var() fallbacks for the paint before
// the measurement lands, which means a new panel copied from an existing rule
// can silently reintroduce the bug by keeping the fallback and dropping the
// var(). That pairing is what this pins.
//
// Note what is deliberately NOT checked: which viewport a panel measures
// against. --ui-vh (the visual viewport, which shrinks for the on-screen
// keyboard) is right only for a panel that owns exactly one viewport. The
// mobile agent layout instead relies on the document being TALLER than the
// viewport so the header can be scrolled off the top, and sizing it to the
// visual viewport collapsed that surplus the moment the keyboard opened —
// scrollY clamped to 0 and the chrome snapped back over the conversation. So
// 100dvh is a legitimate choice there and must stay allowed; only the chrome
// subtraction is ever a guess.

// stripCSSComments removes /* … */ runs. Without this the scan below reads a
// comment that merely NAMES --ui-vh as if the rule declared it — which is
// exactly what these rules' comments do.
func stripCSSComments(css string) string {
	var b strings.Builder
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			b.WriteString(css)
			return b.String()
		}
		b.WriteString(css[:i])
		j := strings.Index(css[i+2:], "*/")
		if j < 0 {
			return b.String() // unterminated; nothing meaningful follows
		}
		css = css[i+2+j+2:]
	}
}

// cssDeclBlocks returns every innermost declaration list in the stylesheet —
// the bodies with no further nested block, i.e. actual rules rather than
// @media wrappers.
func cssDeclBlocks(css string) []string {
	css = stripCSSComments(css)
	var out []string
	var opens []int // stack of '{' positions
	nested := map[int]bool{}
	for i := 0; i < len(css); i++ {
		switch css[i] {
		case '{':
			if len(opens) > 0 {
				nested[opens[len(opens)-1]] = true // parent holds a block
			}
			opens = append(opens, i)
		case '}':
			if len(opens) == 0 {
				continue
			}
			start := opens[len(opens)-1]
			opens = opens[:len(opens)-1]
			if !nested[start] {
				out = append(out, css[start+1:i])
			}
		}
	}
	return out
}

// subtractsPixelsFromViewport reports whether a declaration sizes `height` to a
// viewport MINUS a fixed pixel amount — i.e. it is guessing at the page chrome.
// A bare `height: 100dvh` guesses nothing and is not flagged.
func subtractsPixelsFromViewport(decl string) bool {
	d := strings.TrimSpace(decl)
	if !strings.HasPrefix(d, "height:") {
		return false // min-height/max-height are floors and caps, not the fill
	}
	v := d[len("height:"):]
	if !strings.Contains(v, "100vh") && !strings.Contains(v, "100dvh") {
		return false
	}
	// Any "- <digits>px" term is a chrome constant. The measured form spells it
	// var(--ui-chrome-h, 70px), so the block-level check below still passes.
	for i := strings.Index(v, "- "); i >= 0; i = strings.Index(v, "- ") {
		v = v[i+2:]
		j := 0
		for j < len(v) && v[j] >= '0' && v[j] <= '9' {
			j++
		}
		if j > 0 && strings.HasPrefix(v[j:], "px") {
			return true
		}
	}
	return false
}

func TestFullHeightPanelsMeasureTheChrome(t *testing.T) {
	for _, block := range cssDeclBlocks(runtimeCSS) {
		var guesses, measured bool
		for _, decl := range strings.Split(block, ";") {
			if subtractsPixelsFromViewport(decl) {
				guesses = true
			}
			if strings.Contains(decl, "--ui-chrome-h") {
				measured = true
			}
		}
		if guesses && !measured {
			t.Errorf("rule subtracts a hardcoded page chrome from the viewport "+
				"without the measured var(--ui-chrome-h, …):\n%s",
				strings.TrimSpace(block))
		}
	}
}

func TestNavShellMeasuresTheChrome(t *testing.T) {
	// nav_shell sets its height in an inline style from JS, so the CSS scan
	// above can't see it. It's the app-shell surface (the Operator), the one
	// most likely to host a composer, so check it directly.
	if !strings.Contains(runtimeJS, "var(--ui-chrome-h") {
		t.Fatal("nav_shell no longer sizes itself from the measured chrome")
	}
	// The runtime must actually publish what everything else reads.
	for _, prop := range []string{"--ui-vh", "--ui-chrome-h"} {
		if !strings.Contains(runtimeJS, "setProperty('"+prop+"'") {
			t.Errorf("runtime never sets %s — every panel falls back to a guess", prop)
		}
	}
}
