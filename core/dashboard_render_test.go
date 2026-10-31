package core

// The dashboard page is drawn in parts: the cards (heroes, the Orchestrator
// family, a band per kind, the cards after them, the hidden bands named at
// the foot), the account bar, and the notices. Each part lands where its
// mark is in the page.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

type dashTestApp struct{ n string }

func (z dashTestApp) WebPath() string                       { return "/" + z.n }
func (z dashTestApp) WebName() string                       { return z.n }
func (z dashTestApp) WebDesc() string                       { return "" }
func (z dashTestApp) RegisterRoutes(*http.ServeMux, string) {}

type dashTestHub struct{ dashTestApp }

func (dashTestHub) HubTab() (string, int) { return "hub", 1 }

type dashTestFeatured struct{ dashTestApp }

func (dashTestFeatured) WebFeatured() bool { return true }

type dashTestWide struct{ dashTestApp }

func (dashTestWide) WebWide() bool { return true }

func TestTheDashboardDrawsEachPartWhereItBelongs(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	AuthSetUser(db, "viewer", "pw", false)
	tok := AuthCreateSession(db, "viewer")
	apps := []dashApp{
		{name: "Hub One", desc: "h", path: "/hub", app: dashTestHub{dashTestApp{"hub"}}},
		{name: "Feat", desc: "f", path: "/feat", app: dashTestFeatured{dashTestApp{"feat"}}},
		{name: "Wide", desc: "w", path: "/wide", group: "Apps", app: dashTestWide{dashTestApp{"wide"}}},
		{name: "Query <b>", desc: "q & a", path: "/agents/x?y=1", group: "Agents"},
		{name: "Loose", desc: "l", path: "/loose"},
		{name: "Tool", desc: "t", path: "/tool", group: "Tools"},
	}
	bands := dashBands{order: []string{"Featured", "Orchestrator", "Apps", "Empty band", "Tools"}, hidden: map[string]bool{"Tools": true}}
	notices := []DashboardNotice{{Text: "Fix <it>", Action: "Go", URL: "/fix"}, {Text: " "}}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: tok})
	w := httptest.NewRecorder()
	serve_dashboard(w, r, apps, notices, bands)
	page := w.Body.String()
	for _, want := range []string{
		`<div class="hero-wrap">`, `<a class="card featured" href="/feat/"`,
		`<span>Orchestrator</span>`, `href="/hub/"`,
		`<span>Apps</span>`, `<a class="card wide" href="/wide/"`,
		`<span>Empty band</span>`,
		`<span>Agents</span>`, `href="/agents/x?y=1"`, `Query &lt;b&gt;`,
		`<a class="card" href="/loose/"`,
		`data-band="Tools" title="Show Tools again"`,
		`<span class="auth-user">viewer</span>`,
		`<div class="notice"><span>Fix &lt;it&gt;</span><div class="notice-acts"><a class="notice-act" href="/fix">Go</a></div></div>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	for _, gone := range []string{"%CARDS%", "%AUTH%", "%NOTICES%", `href="/tool/"`} {
		if strings.Contains(page, gone) {
			t.Errorf("the page still has %q", gone)
		}
	}
	if strings.Count(page, `class="notice"`) != 1 {
		t.Error("a notice with no text should draw nothing")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page should never be served from a cache")
	}

	// Nobody signed in: no account bar.
	w = httptest.NewRecorder()
	serve_dashboard(w, httptest.NewRequest("GET", "/", nil), apps, nil, bands)
	if strings.Contains(w.Body.String(), `class="auth-bar"`) {
		t.Error("an anonymous viewer got the account bar")
	}
}
