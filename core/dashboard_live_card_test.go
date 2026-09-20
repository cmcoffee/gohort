package core

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/webui"
)

// dashboardHTML renders the dashboard the way a browser gets it.
func dashboardHTML(t *testing.T) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	serve_dashboard(w, r, nil, nil)
	return w.Body.String()
}

// rule returns the declaration block of the first CSS rule whose selector list
// contains sel, so a test asserts against THAT rule and not a substring that
// happens to appear elsewhere on the page.
func rule(t *testing.T, html, sel string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)(^|[\n,{}])\s*` + regexp.QuoteMeta(sel) + `\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("no CSS rule for %q on the dashboard", sel)
	}
	return m[2]
}

// A live row is badges + topic + status on one flex line, and the rail it sits
// in is 320px wide. The label used to be flex:1 — basis 0, min-width auto — so
// once the badges and the status had taken the width it was handed whatever
// pixels were left, which was sometimes a handful. Text in a track that narrow
// wraps one character per line: the topic stacks on itself, the row grows far
// taller than the badges next to it, and the tail runs past the card's border.
func TestDashboardLiveRowCannotCrushTheLabel(t *testing.T) {
	html := dashboardHTML(t)

	item := rule(t, html, ".live-item")
	if !strings.Contains(item, "flex-wrap: wrap") {
		t.Error("the row must be allowed to become two lines; without wrap the label has nowhere to go but narrower")
	}

	label := rule(t, html, ".live-label")
	if strings.Contains(label, "flex: 1;") || strings.Contains(label, "flex: 1 1 0") {
		t.Error("a zero flex-basis is the crush: the label gets only leftover space")
	}
	if !strings.Contains(label, "min-width: 0") {
		t.Error("the label needs min-width: 0, or its content overflows the card instead of wrapping in it")
	}
	if !strings.Contains(label, "overflow-wrap: anywhere") {
		t.Error("a topic with no break opportunity must break anywhere rather than run out of the box")
	}

	badge := rule(t, html, ".live-badge")
	if !strings.Contains(badge, "flex: 0 0 auto") {
		t.Error("badges must not shrink; a nowrap badge that is shrunk clips its own word")
	}

	status := rule(t, html, ".live-status")
	if !strings.Contains(status, "min-width: 0") || !strings.Contains(status, "overflow-wrap: anywhere") {
		t.Error("the status is the other long string on the row and needs the same treatment")
	}
}

// Topic, app and status are all user content arriving over /api/live and going
// straight into innerHTML. An unescaped "<" opens a tag and eats the rest of
// the row — which looks exactly like a layout bug.
func TestDashboardLiveRowEscapesUserText(t *testing.T) {
	html := dashboardHTML(t)

	js := html[strings.Index(html, "function refreshLive"):]
	for _, raw := range []string{"+ it.app +", "+ it.status +", "+ (it.topic || it.label || 'Untitled') +"} {
		if strings.Contains(js, raw) {
			t.Errorf("user text interpolated raw into innerHTML: %s", raw)
		}
	}
	for _, want := range []string{"esc(it.app)", "esc(it.status)", "esc(label)"} {
		if !strings.Contains(js, want) {
			t.Errorf("expected %s in the live-row builder", want)
		}
	}
}

// The dashboard rail is not the only place a live row is drawn by hand. The
// floating ribbon on pages that don't render through core/ui has the same
// shape — badges, then a topic — inside a fixed 360px box, and it shipped with
// no rule for the label at all. It exists in two copies: webui's base.css, and
// an inlined duplicate in webapp_html.go for pages that don't pull base.css in.
// Both are checked here so a fix to one can't quietly leave the other behind.
func TestLiveRibbonRowCannotCrushTheLabel(t *testing.T) {
	for name, css := range map[string]string{
		"webui/static/base.css":   webui.BaseCSS(),
		"webapp_html.go (inline)": liveRibbonCSS,
	} {
		item := rule(t, css, "#webui-live-ribbon .item")
		if !strings.Contains(item, "flex-wrap: wrap") {
			t.Errorf("%s: the ribbon row must be allowed a second line", name)
		}
		label := rule(t, css, "#webui-live-ribbon .item .label")
		if !strings.Contains(label, "min-width: 0") || !strings.Contains(label, "overflow-wrap: anywhere") {
			t.Errorf("%s: the topic must wrap inside the ribbon rather than walk out through its border", name)
		}
		if !strings.Contains(label, "flex: 1 1 7rem") {
			t.Errorf("%s: without a flex floor the label takes whatever the badges leave over, which is sometimes nothing", name)
		}
		badge := rule(t, css, "#webui-live-ribbon .badge")
		if !strings.Contains(badge, "flex: 0 0 auto") || !strings.Contains(badge, "white-space: nowrap") {
			t.Errorf("%s: a badge that shrinks clips its own word", name)
		}
	}
}

// The bell rides a FIXED bar, so it follows you down the dashboard. The panel
// it opens has to follow too.
//
// It used to be absolutely positioned, which resolves against the page rather
// than the viewport: the bell came with you and the panel stayed pinned near
// the top of the document, so clicking it anywhere but the very top opened it
// off-screen above. Whatever anchors the button has to anchor what the button
// opens, and this pins that they agree.
func TestTheNotificationsPanelFollowsItsBell(t *testing.T) {
	css := dashboardPageSource(t)
	// Two-space indent picks the top-level rule; the four-space twins inside
	// the mobile media query only nudge the offsets.
	bar := cssBlock(css, "\n  .auth-bar {")
	panel := cssBlock(css, "\n  .notify-panel {")
	if bar == "" || panel == "" {
		t.Fatal("the auth bar or the notifications panel lost its rule")
	}
	if !strings.Contains(bar, "position: fixed") {
		t.Fatal("the auth bar is no longer fixed; this test's premise needs rechecking")
	}
	if !strings.Contains(panel, "position: fixed") {
		t.Error("the panel is not fixed while the bell that opens it is: " +
			"it will open off-screen for anybody who has scrolled")
	}
	// Above the bar, not below it. The bar sits at a very high z-index and a
	// panel under it would open behind the controls it belongs to.
	if !strings.Contains(panel, "z-index: 10000") {
		t.Error("the panel no longer sits above the fixed bar it hangs from")
	}
}

// dashboardPageSource reads this package's dashboard template, which is a Go
// string rather than an asset — so the source is the only place to check it.
func dashboardPageSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("dashboard_page.go")
	if err != nil {
		t.Fatalf("reading the dashboard page: %v", err)
	}
	return string(raw)
}

// cssBlock returns the declarations of the first rule opening with sel.
func cssBlock(src, sel string) string {
	i := strings.Index(src, sel)
	if i < 0 {
		return ""
	}
	rest := src[i+len(sel):]
	end := strings.Index(rest, "}")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// The dashboard draws its own bell, because it is hand-rolled HTML that never
// loads the runtime. Two copies of one control are already a liability; two
// copies that look different are worse, so the glyph is pinned to the same
// artwork the framework header uses.
func TestTheDashboardBellIsTheDrawnOne(t *testing.T) {
	src := dashboardPageSource(t)
	if strings.Contains(src, "\U0001F514") {
		t.Error("the dashboard bell is a platform-drawn emoji again: it renders at a " +
			"different size on every device and cannot be muted")
	}
	runtime, err := os.ReadFile(filepath.Join("ui", "assets", "runtime", "71_notice_bell.js"))
	if err != nil {
		t.Fatalf("reading the runtime bell: %v", err)
	}
	// The path data is the artwork. If one copy is redrawn and the other is
	// not, the same control looks like two controls.
	for _, d := range []string{
		"M32 6c-9 0-16 7-16 16v8c0 7-2 11-6 15-1 1 0 3 2 3h40c2 0 3-2 2-3-4-4-6-8-6-15v-8c0-9-7-16-16-16z",
		"M23 53h18c-1 6-4 9-9 9s-8-3-9-9z",
	} {
		if !strings.Contains(src, d) {
			t.Errorf("the dashboard bell no longer draws the shared artwork (%.20s…)", d)
		}
		if !strings.Contains(string(runtime), d) {
			t.Errorf("the header bell no longer draws the shared artwork (%.20s…)", d)
		}
	}
}
