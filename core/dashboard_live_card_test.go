package core

import (
	"net/http/httptest"
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
