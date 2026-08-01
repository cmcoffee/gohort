package ui

import (
	"strings"
	"testing"
)

// A no-chrome section skips the card wrapper so the panel inside it can own its
// own layout — and, for as long as that branch existed, it skipped the section's
// title and subtitle with it. An author who set both got neither, with no error
// and nothing in the stored page to suggest they had been dropped: the app_def
// pipeline section that prompted this carried a title AND a subtitle, and the
// page rendered neither.
//
// Source-scan rather than DOM: there is no browser in this package (see
// runtime_viewport_test.go for the same trade). What it pins is the pairing a
// future edit is most likely to break — one of the two no-chrome paths losing
// the call, which shows up only on a grid page and only for a section that
// happens to have a title.
func TestNoChromeSectionsRenderTheirHeading(t *testing.T) {
	src := mustRuntimePart(t, "99_epilogue.js")
	if !strings.Contains(src, "function bareSectionHead(") {
		t.Fatal("bareSectionHead is gone — a no-chrome section's title and subtitle are being dropped again")
	}
	// Both no-chrome paths: the grid one (wrapped in .ui-section-wide) and the
	// plain one (mounted straight into the host).
	if n := strings.Count(src, "bareSectionHead(s,"); n < 2 {
		t.Errorf("bareSectionHead is called %d time(s); both the grid and the plain no-chrome path need it", n)
	}
	// It must render NOTHING when there is no heading — every no-chrome section
	// that shipped before this sets neither field, and a stray empty box above
	// a chat panel would be a visible regression on every one of them.
	if !strings.Contains(src, "if (!s.title && !s.subtitle) return;") {
		t.Error("a no-chrome section with no title/subtitle must render no heading at all")
	}
	// The class it emits has to be styled, or the heading collides with the
	// panel: it has no card supplying padding.
	if css := runtimeCSS; !strings.Contains(css, ".ui-section-bare-head") {
		t.Error("no CSS for .ui-section-bare-head — outside a card it has no padding of its own")
	}
}

// mustRuntimePart reads one runtime fragment. The runtime is served as one
// concatenated blob, but the fragment is what a human edits, so a failure
// should name the file they have to open.
func mustRuntimePart(t *testing.T, name string) string {
	t.Helper()
	b, err := runtimeJSParts.ReadFile("assets/runtime/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// A full-height panel owns the viewport, and its width must not depend on what
// a STORED page happened to bake in when it was authored: an app whose panel
// arrived after the page was written opened in a 900px column with a 240px rail
// eating a quarter of it, fixable only by re-authoring.
func TestFullHeightPanelsClearTheWidthCap(t *testing.T) {
	src := mustRuntimePart(t, "99_epilogue.js")
	i := strings.Index(src, "root.style.maxWidth = '100%'")
	if i < 0 {
		t.Fatal("nothing widens a page for a full-height panel — a stored page's baked width wins again")
	}
	// Keyed on the panel roots themselves. "Any section" would also widen a
	// page whose no-chrome section holds ordinary content and asked for a
	// narrower column.
	sel := src[strings.LastIndex(src[:i], "root.querySelector("):i]
	for _, panel := range []string{".ui-chat", ".ui-agent", ".ui-pl", ".ui-wb"} {
		if !strings.Contains(sel, panel) {
			t.Errorf("%s hosts a full-height panel and must clear the cap too", panel)
		}
	}
	if strings.Contains(sel, ".ui-section") {
		t.Error("widening on any section would widen ordinary content pages")
	}
}
