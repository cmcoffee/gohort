package ui

// The touch floor: on a touch screen every control is at least the compact
// tap size (--btn-h-sm, 36px), and a row in a menu the full one (--tap,
// 44px). The token buttons get there through the tokens; the classes that
// size themselves are named in the floor's lists in runtime.css. These tests
// keep that from quietly coming apart: a name in the lists that styles
// nothing, a touch rule that sets a control smaller again, or inline sizes
// that beat every rule.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// touchFloorClasses reads the class names out of the floor's :is() lists.
func touchFloorClasses(t *testing.T) []string {
	t.Helper()
	i := strings.Index(runtimeCSS, "THE TOUCH FLOOR")
	if i < 0 {
		t.Fatal("runtime.css has no touch floor block")
	}
	block := runtimeCSS[i:]
	if j := strings.Index(block, "\n}\n"); j >= 0 {
		block = block[:j]
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range regexp.MustCompile(`:is\(([^)]*)\)`).FindAllStringSubmatch(block, -1) {
		for _, c := range regexp.MustCompile(`\.(ui-[\w-]+)`).FindAllStringSubmatch(m[1], -1) {
			if !seen[c[1]] {
				seen[c[1]] = true
				out = append(out, c[1])
			}
		}
	}
	if len(out) < 40 {
		t.Fatalf("read only %d classes from the touch floor; the lists moved or changed shape", len(out))
	}
	return out
}

// Every name in the floor is a class something draws: a typo or a renamed
// class would leave its control small without anyone noticing.
func TestTouchFloorNamesRealClasses(t *testing.T) {
	for _, c := range touchFloorClasses(t) {
		if strings.Contains(runtimeJS, c) {
			continue
		}
		// A class drawn by Go-rendered markup rather than the runtime.
		found := false
		filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
			if err != nil || found || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), c) {
				found = true
			}
			return nil
		})
		if !found {
			t.Errorf("the touch floor names .%s, which nothing draws", c)
		}
	}
}

// No touch-screen rule sets a control's height below the compact tap size.
// var() values are the tokens and pass; a literal under 36px / 2.25rem is a
// control made small again where it most needs not to be.
func TestTouchRulesSetNoSmallHeights(t *testing.T) {
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(runtimeCSS, "")
	media := regexp.MustCompile(`@media[^{]*pointer:\s*coarse[^{]*\{`)
	decl := regexp.MustCompile(`(?:^|[;{\s])(min-height|height)\s*:\s*([0-9.]+)(px|rem)\b`)
	for _, loc := range media.FindAllStringIndex(css, -1) {
		depth, end := 1, loc[1]
		for end < len(css) && depth > 0 {
			switch css[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
			end++
		}
		body := css[loc[1]:end]
		for _, rule := range regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(body, -1) {
			sel := strings.TrimSpace(rule[1])
			if strings.Contains(sel, "::") {
				continue // a pseudo-element: a mark or a hit area, not a control
			}
			for _, d := range decl.FindAllStringSubmatch(rule[2], -1) {
				v, _ := strconv.ParseFloat(d[2], 64)
				px := v
				if d[3] == "rem" {
					px = v * 16
				}
				if px > 0 && px < 36 {
					t.Errorf("a touch-screen rule sets %s: %s%s on %q, under the 36px compact tap size", d[1], d[2], d[3], sel)
				}
			}
		}
	}
}

// Inline sizes beat every rule, the touch floor included: the agent top
// bar's buttons were pinned at 22px that way.
func TestTopBarButtonsAreNotSizedInline(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	for _, bad := range []string{"b.style.minHeight = '0'", "b.style.minWidth = '0'"} {
		if strings.Contains(src, bad) {
			t.Errorf("the agent panel sizes buttons inline again (%s)", bad)
		}
	}
}

// .ui-btn is not a class: a button wearing it renders as the browser's
// default. The framework's button is .ui-row-btn.
func TestNoButtonWearsAnUndefinedClass(t *testing.T) {
	if regexp.MustCompile(`\.ui-btn[\s{,:.]`).MatchString(runtimeCSS) {
		t.Skip(".ui-btn is defined now")
	}
	re := regexp.MustCompile(`class\s*:\s*['"]ui-btn(?:\s|['"])`)
	filepath.Walk("../../apps", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && re.Match(b) {
			t.Errorf("%s puts a button in the undefined .ui-btn class; use .ui-row-btn", p)
		}
		return nil
	})
}

// A rectangular button's corners are --btn-radius, and a pill is round
// (999px). The controls in the floor used 3, 4, 5, 6, 7, 8 and 10px between
// them, plus two pills at 12 and 18px.
func TestButtonsShareOneRadius(t *testing.T) {
	floor := map[string]bool{}
	for _, c := range touchFloorClasses(t) {
		floor[c] = true
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(runtimeCSS, "")
	radius := regexp.MustCompile(`border-radius\s*:\s*([^;]+)`)
	for _, rule := range regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(rule[1])
		if strings.Contains(sel, "::") {
			continue
		}
		subject := false
		for _, part := range strings.Split(sel, ",") {
			f := strings.Fields(part)
			if len(f) == 0 {
				continue
			}
			for _, c := range regexp.MustCompile(`\.(ui-[\w-]+)`).FindAllStringSubmatch(f[len(f)-1], -1) {
				if floor[c[1]] {
					subject = true
				}
			}
		}
		if !subject {
			continue
		}
		for _, m := range radius.FindAllStringSubmatch(rule[2], -1) {
			v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), "!important"))
			if strings.Contains(v, "var(--btn-radius") || strings.Contains(v, "999px") || v == "50%" || v == "0" {
				continue
			}
			t.Errorf("%q rounds its corners at %s; a button uses var(--btn-radius), a pill 999px", sel, v)
		}
	}
}
