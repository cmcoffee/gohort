package core

import (
	"net/http"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
)

// mockHubApp implements Agent + WebApp + WebAppHubTab, so it can be registered
// via RegisterApp (the path real apps like Agency/Bridges/Knowledge use).
type mockHubApp struct {
	path, label string
	order       int
}

func (m *mockHubApp) Get() *AppCore                                { return nil }
func (m *mockHubApp) Name() string                                 { return m.label }
func (m *mockHubApp) Desc() string                                 { return "" }
func (m *mockHubApp) SystemPrompt() string                         { return "" }
func (m *mockHubApp) Init() error                                  { return nil }
func (m *mockHubApp) Main() error                                  { return nil }
func (m *mockHubApp) WebPath() string                              { return m.path }
func (m *mockHubApp) WebName() string                              { return m.label }
func (m *mockHubApp) WebDesc() string                              { return "" }
func (m *mockHubApp) RegisterRoutes(mux *http.ServeMux, p string)  {}
func (m *mockHubApp) HubTab() (string, int)                        { return m.label, m.order }

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
