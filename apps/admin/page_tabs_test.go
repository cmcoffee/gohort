package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The administrator UI is one page per tab: a page carries only its tab's
// sections, every tab as a link with the open one marked, and an address
// naming no tab is not a page.
func TestEachAdminTabIsItsOwnPage(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(LLMTable, "provider", "openai") // configured, so no setup-wizard redirect
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux, "/admin")
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code, w.Body.String()
	}
	code, first := get("/admin/")
	if code != http.StatusOK {
		t.Fatalf("first tab: %d", code)
	}
	code, costs := get("/admin/costs")
	if code != http.StatusOK {
		t.Fatalf("costs tab: %d", code)
	}
	if first == costs {
		t.Error("two tabs served the same page")
	}
	// The first page is System: it has the status panel and not the cost
	// chart; the Costs page has the chart and not the status panel.
	if !strings.Contains(first, "api/status") || strings.Contains(first, "api/cost-history") {
		t.Error("the first page is not the System tab alone")
	}
	if strings.Contains(costs, "api/status") || !strings.Contains(costs, "api/cost-history") {
		t.Error("the Costs page is not the Costs tab alone")
	}
	for _, want := range []string{`"label":"System"`, `"label":"Costs"`, `"url":"/admin/costs"`, `"active":true`} {
		if !strings.Contains(costs, want) {
			t.Errorf("the Costs page's nav lacks %s", want)
		}
	}
	if !strings.Contains(costs, "location.replace(tabs[t]+location.hash)") {
		t.Error("a #tab/section link has no way to reach its tab's page")
	}
	if code, _ := get("/admin/no-such-tab"); code != http.StatusNotFound {
		t.Errorf("an unknown tab: %d, want 404", code)
	}
	if code, _ := get("/admin/costs/more"); code != http.StatusNotFound {
		t.Errorf("a path below a tab: %d, want 404", code)
	}
}

// adminTabs lists each group once, in order, the first at the root.
func TestAdminTabsAreTheGroupsInOrder(t *testing.T) {
	tabs := adminTabs("/admin", []ui.Section{{Group: "System"}, {Group: "Costs"}, {Group: "System"}, {Group: "LLMs"}})
	if len(tabs) != 3 || tabs[0].url != "/admin/" || tabs[1].url != "/admin/costs" || tabs[2].slug != "llms" {
		t.Errorf("tabs: %+v", tabs)
	}
}
