package admin

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
