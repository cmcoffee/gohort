package admin

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// The tab is a VIEW over registries that keep working exactly as they did, so
// the honest answer for an app that has claimed nothing is "nothing", plus
// where its controls actually are. Anything else invites a hunt on this page
// for a dial that lives on another one.
func TestAnAppThatClaimsNothingSaysSo(t *testing.T) {
	got := describeAppControls(nil, "/nothing-claimed-this")
	if !strings.Contains(got, "none declared") {
		t.Errorf("unclaimed app summary = %q", got)
	}
	// And it names the tabs the controls are actually on, because "none" on
	// its own reads as "this app has no settings", which is false.
	for _, tab := range []string{"LLMs", "Tuning", "Extensions"} {
		if !strings.Contains(got, tab) {
			t.Errorf("summary should say where the controls are; missing %q in %q", tab, got)
		}
	}
}

func TestClaimedControlsAreCounted(t *testing.T) {
	RegisterRouteStage(RouteStage{Key: "app.summarytest.a", Label: "A", App: "/summarytest"})
	RegisterRouteStage(RouteStage{Key: "app.summarytest.b", Label: "B", App: "/summarytest"})
	RegisterTunable(TunableSpec{Key: "tune_summarytest", Category: "Limits", Label: "T",
		App: "/summarytest", Kind: KindInt, Default: 1, Min: 1, Max: 10})

	got := describeAppControls(nil, "/summarytest")
	if !strings.Contains(got, "2 routing dials") || !strings.Contains(got, "1 tunable") {
		t.Errorf("summary = %q, want the claimed counts", got)
	}
	if strings.Contains(got, "tunables") {
		t.Errorf("one tunable should not be pluralised: %q", got)
	}
}

// An admin reaches every app regardless of grants, so "nobody" would be wrong
// for an app with no grants — an operator reading it would conclude the app is
// unreachable and unused, and it is neither.
func TestAccessLineNamesAdminsSeparately(t *testing.T) {
	if got := describeAppAccess(nil, "/x"); !strings.Contains(got, "everyone") {
		t.Errorf("with no accounts configured, everything is open: %q", got)
	}
}

// The tab map keys on TITLE and an Apps row's title is an app's NAME, which
// nobody here chose. A collision must not move the row to another tab.
func TestAppsGroupSurvivesATitleCollision(t *testing.T) {
	if AppsTabGroup != "Apps" {
		t.Fatalf("AppsTabGroup = %q — the custom-app source hardcodes the same string", AppsTabGroup)
	}
}

// The switchboard must not offer the administrator panel or the framework's
// internal apps. Both exclusions are what keeps an operator from switching off
// the way back — the admin panel is the surface that re-enables things, and
// the hidden apps are the account page, the monitor and the API endpoints
// every other app is built on.
func TestSwitchboardWithholdsTheWayBack(t *testing.T) {
	for _, rw := range listableApps() {
		if rw.path == "/admin" {
			t.Error("the administrator panel must never be listed as switchable")
		}
	}
	for _, path := range []string{"/admin", "/account", "/monitor", "/v1", "/nonesuch"} {
		if isListableApp(path) {
			t.Errorf("%s must not be accepted by the enable/disable endpoint", path)
		}
	}
}

// The switch is the section an operator lands on, so it has to be first — the
// per-app cards below it are reference, not the control.
func TestAvailabilitySectionLeadsTheAppsTab(t *testing.T) {
	a := &AdminApp{}
	secs := a.appsTabSections()
	if len(secs) == 0 || secs[0].Title != "Enabled apps" {
		t.Fatalf("the Apps tab must lead with the switchboard, got %v", sectionTitles(secs))
	}
	sec := appsAvailabilitySection()
	if sec.Group != AppsTabGroup {
		t.Errorf("availability section group = %q, want %q", sec.Group, AppsTabGroup)
	}
	// The subtitle is the whole scope promise: what the switch does and, just
	// as importantly, what it leaves running.
	for _, want := range []string{"503", "restart", "grants", "web surface"} {
		if !strings.Contains(sec.Subtitle, want) {
			t.Errorf("availability subtitle should mention %q: %q", want, sec.Subtitle)
		}
	}
}

func sectionTitles(secs []ui.Section) []string {
	out := make([]string, 0, len(secs))
	for _, s := range secs {
		out = append(out, s.Title)
	}
	return out
}
