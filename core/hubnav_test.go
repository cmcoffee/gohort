package core

import (
	"net/http"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// mockHubApp implements Agent + WebApp + WebAppHubTab, so it can be registered
// via RegisterApp (the path real apps like Agency/Bridges/Knowledge use).
type mockHubApp struct {
	path, label string
	order       int
}

func (m *mockHubApp) Get() *AppCore                               { return nil }
func (m *mockHubApp) Name() string                                { return m.label }
func (m *mockHubApp) Desc() string                                { return "" }
func (m *mockHubApp) SystemPrompt() string                        { return "" }
func (m *mockHubApp) Init() error                                 { return nil }
func (m *mockHubApp) Main() error                                 { return nil }
func (m *mockHubApp) WebPath() string                             { return m.path }
func (m *mockHubApp) WebName() string                             { return m.label }
func (m *mockHubApp) WebDesc() string                             { return "" }
func (m *mockHubApp) RegisterRoutes(mux *http.ServeMux, p string) {}
func (m *mockHubApp) HubTab() (string, int)                       { return m.label, m.order }

// Regression: HubNav must enumerate apps registered via RegisterApp (where the
// real hub members live), not only RegisterWebApp. The original bug read only
// RegisteredWebApps() and returned an empty tab row — the tabs "disappeared".
func TestHubNavIncludesRegisterAppMembers(t *testing.T) {
	RegisterApp(&mockHubApp{path: "/mock-hub-abc", label: "MockHub", order: 42})

	nav := HubNav("/mock-hub-abc")

	var got *ui.NavLink
	for i := range nav {
		if nav[i].URL == "/mock-hub-abc" {
			got = &nav[i]
			break
		}
	}
	if got == nil {
		t.Fatal("HubNav omitted an app registered via RegisterApp — it must union RegisteredApps(), not just RegisteredWebApps()")
	}
	if got.Label != "MockHub" {
		t.Fatalf("tab label = %q, want MockHub", got.Label)
	}
	if !got.Active {
		t.Fatal("tab matching activePath should be Active")
	}
}

// TestASwitchedOffAppKeepsNoTab. The availability switch takes an app's
// dashboard card away and answers its routes with 503, but the shared hub tab
// row was built without consulting it — so a disabled Bridges still had its
// tab on the Agents, Extensions and Knowledge pages, and every one of those
// tabs led to the 503. Same reasoning the card gate already states: a tab that
// leads nowhere is worse than an absent one.
func TestASwitchedOffAppKeepsNoTab(t *testing.T) {
	savedWeb, savedApps, savedAgents := registeredWebApps, registeredApps, registeredAgents
	t.Cleanup(func() { registeredWebApps, registeredApps, registeredAgents = savedWeb, savedApps, savedAgents })
	registeredWebApps, registeredApps, registeredAgents = nil, nil, nil
	RegisterApp(&mockHubApp{path: "/bridges", label: "Bridges", order: 2})
	RegisterApp(&mockHubApp{path: "/knowledge", label: "Knowledge", order: 1})

	db := &DBase{Store: kvlite.MemStore()}
	savedAuth := AuthDB
	t.Cleanup(func() { AuthDB = savedAuth })
	AuthDB = func() Database { return db }

	labels := func() []string {
		var out []string
		for _, l := range HubNav("/knowledge") {
			out = append(out, l.Label)
		}
		return out
	}
	if got := labels(); len(got) != 2 {
		t.Fatalf("both apps should be tabbed while enabled, got %v", got)
	}

	if err := SetAppEnabled(db, "/bridges", false); err != nil {
		t.Fatal(err)
	}
	got := labels()
	if len(got) != 1 || got[0] != "Knowledge" {
		t.Fatalf("a switched-off app still has a tab: %v", got)
	}

	// And it comes back exactly as it was — the switch disturbs nothing else.
	if err := SetAppEnabled(db, "/bridges", true); err != nil {
		t.Fatal(err)
	}
	if got := labels(); len(got) != 2 {
		t.Fatalf("switching the app back on did not restore its tab: %v", got)
	}
}
