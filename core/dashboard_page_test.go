package core

import (
	"strings"
	"testing"
)

// A notice offers Dismiss only when it names where to post it, and the
// address is escaped like the rest.
func TestANoticeOffersDismissWhenItCan(t *testing.T) {
	plain := dashboardNotices([]DashboardNotice{{Text: "Something.", Action: "Open", URL: "/x"}})
	if strings.Contains(plain, "data-dismiss") {
		t.Errorf("a notice with no dismiss address offers Dismiss: %s", plain)
	}
	with := dashboardNotices([]DashboardNotice{{Text: "Lead <failing>.", Action: "Open", URL: "/x", Dismiss: `/admin/api/lead-llm/dismiss?a="b"`}})
	if !strings.Contains(with, `data-dismiss="/admin/api/lead-llm/dismiss?a=&#34;b&#34;"`) || !strings.Contains(with, "Lead &lt;failing&gt;.") {
		t.Errorf("dismiss: %s", with)
	}
}

// A card carries its kind, which is what its colour says: gohort's own app,
// an agent, or an app somebody built.
func TestADashboardCardCarriesItsKind(t *testing.T) {
	var b strings.Builder
	renderDashCard(&b, dashApp{name: "<b>ravo", desc: "d", path: "/x", group: "My apps"}, "")
	card := b.String()
	if !strings.Contains(card, `<div class="card-name">&lt;b&gt;ravo</div>`) {
		t.Errorf("the name is not escaped: %s", card)
	}
	if !strings.Contains(card, `<a class="card" href="/x/" data-kind="made"`) {
		t.Errorf("an app somebody built is not marked as made: %s", card)
	}
	for _, c := range []struct {
		a    dashApp
		want string
	}{
		{dashApp{path: "/hub", app: dashTestHub{dashTestApp{"hub"}}}, "builtin"},
		{dashApp{path: "/agents/a", group: "Agents"}, "agent"},
		{dashApp{path: "/apps/s", group: "My apps"}, "made"},
		{dashApp{path: "/other", group: "More"}, "made"},
	} {
		if got := cardKind(c.a); got != c.want {
			t.Errorf("%s reads as %q, want %q", c.a.path, got, c.want)
		}
	}
	// The colours are the theme's accents, never its status colours.
	html := dashboardHTML(t)
	for _, sel := range []string{`.card[data-kind="made"]`, `.card[data-kind="agent"]`} {
		r := rule(t, html, sel)
		if strings.Contains(r, "--success") || strings.Contains(r, "--warning") || strings.Contains(r, "--danger") {
			t.Errorf("%s is coloured as a status: %s", sel, r)
		}
	}
}

// A running session's spinner turns by editing its glyph's text node, never by
// replacing the row: the rows are links, and swapping a link's content as it is
// clicked can swallow the click.
func TestTheLiveSpinnerTurnsInPlace(t *testing.T) {
	html := dashboardHTML(t)
	js := html[strings.Index(html, "var spinFrames"):]
	if !strings.Contains(js, "firstChild.nodeValue = spinFrames[spinAt]") {
		t.Error("the spinner does not edit its text node in place")
	}
	if !strings.Contains(js, "prefers-reduced-motion: reduce") {
		t.Error("the spinner turns under reduced motion")
	}
}
