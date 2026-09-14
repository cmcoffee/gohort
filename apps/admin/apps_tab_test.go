package admin

import (
	"net/http"
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

// Every app this tab lists must be one the summary lookup can resolve.
//
// It could resolve NONE of them. The sections are built from AllWebApps and the
// lookup read the direct-registration registry, which holds the admin panel and
// nothing else — and the admin panel is the one app the sections exclude. So
// the tab rendered a correct heading and a correct subtitle over a 404, once
// per app, on every deployment.
//
// This drives the lookup itself rather than comparing the section list against
// the registry it was built from. The first version of this test did the
// latter, and passed against a lookup hardcoded to find nothing — it pinned the
// half that was already right.
func TestEveryListedAppCanBeResolved(t *testing.T) {
	// Registered as an App implementing WebApp — how every real app arrives,
	// and the shape the broken lookup could not see. Without it this binary has
	// no apps and the check passes by having nothing to check.
	RegisterApp(fakeWebApp{path: "/sectionsourcetest"})
	rows := listableApps()
	if len(rows) == 0 {
		t.Fatal("no apps listed — this test would pass vacuously")
	}
	for _, rw := range rows {
		if findListedApp(rw.path) == nil {
			t.Errorf("the tab lists %q (%s) but the summary lookup cannot resolve it — that row renders a 404",
				rw.name, rw.path)
		}
	}
}

// A path nothing serves still has to come back nil, or the 404 the handler owes
// the caller never happens.
func TestAnUnservedPathResolvesToNothing(t *testing.T) {
	if findListedApp("/nothing-serves-this") != nil {
		t.Error("an unserved path must not resolve")
	}
}

// The subtitle on each section is the path its source asks about. If those ever
// drift the page shows one app's heading over another app's answer, which is
// worse than the 404 was: it looks right.
func TestASectionAsksAboutTheAppItNames(t *testing.T) {
	a := &AdminApp{}
	for _, sec := range a.appsTabSections() {
		dp, ok := sec.Body.(ui.DisplayPanel)
		if !ok {
			continue
		}
		if want := "api/app-summary?path=" + sec.Subtitle; dp.Source != want {
			t.Errorf("section %q sources %q, want %q", sec.Title, dp.Source, want)
		}
	}
}

// fakeWebApp is the shape an ordinary app arrives in: registered as an App that
// happens to implement WebApp. That is every app on this deployment except the
// admin panel — and precisely the shape the broken lookup could not see.
type fakeWebApp struct{ path string }

func (f fakeWebApp) WebPath() string                                  { return f.path }
func (f fakeWebApp) WebName() string                                  { return "Fake " + f.path }
func (f fakeWebApp) WebDesc() string                                  { return "a test app" }
func (f fakeWebApp) RegisterRoutes(mux *http.ServeMux, prefix string) {}
func (f fakeWebApp) Get() *AppCore                                    { return &AppCore{} }
func (f fakeWebApp) Name() string                                     { return "fake" + strings.ReplaceAll(f.path, "/", "") }
func (f fakeWebApp) Desc() string                                     { return "a test app" }
func (f fakeWebApp) SystemPrompt() string                             { return "" }
func (f fakeWebApp) Init() error                                      { return nil }
func (f fakeWebApp) Main() error                                      { return nil }
