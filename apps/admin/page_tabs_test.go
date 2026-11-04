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
	code, models := get("/admin/models")
	if code != http.StatusOK {
		t.Fatalf("models tab: %d", code)
	}
	if first == models {
		t.Error("two tabs served the same page")
	}
	// The first page is Overview: the status panel and the cost chart, and
	// not a model's settings; the Models page is the reverse.
	if !strings.Contains(first, "api/status") || !strings.Contains(first, "api/cost-history") || strings.Contains(first, "api/worker-llm") {
		t.Error("the first page is not the Overview tab alone")
	}
	if strings.Contains(models, "api/status") || strings.Contains(models, "api/cost-history") || !strings.Contains(models, "api/worker-llm") {
		t.Error("the Models page is not the Models tab alone")
	}
	for _, want := range []string{`"label":"Overview"`, `"label":"Models"`, `"url":"/admin/models"`, `"active":true`} {
		if !strings.Contains(models, want) {
			t.Errorf("the Models page's nav lacks %s", want)
		}
	}
	// A tab's sections are a rail down its left: tabs above them as well
	// read as a second header.
	if !strings.Contains(models, `"section_nav":true`) {
		t.Error("the admin page does not ask for the section rail")
	}
	if !strings.Contains(models, "location.replace(tabs[t]+location.hash)") {
		t.Error("a #tab/section link has no way to reach its tab's page")
	}
	if code, _ := get("/admin/no-such-tab"); code != http.StatusNotFound {
		t.Errorf("an unknown tab: %d, want 404", code)
	}
	if code, _ := get("/admin/models/more"); code != http.StatusNotFound {
		t.Errorf("a path below a tab: %d, want 404", code)
	}
}

// adminTabs lists each group once, in order, the first at the root.
func TestAdminTabsAreTheGroupsInOrder(t *testing.T) {
	tabs := adminTabs("/admin", []ui.Section{{Group: "Overview"}, {Group: "People"}, {Group: "Overview"}, {Group: "Models"}})
	if len(tabs) != 3 || tabs[0].url != "/admin/" || tabs[1].url != "/admin/people" || tabs[2].slug != "models" {
		t.Errorf("tabs: %+v", tabs)
	}
}

// A tab from an earlier layout is a bookmark somebody has. It goes to the tab
// that took its sections, and every retired address goes to a tab that exists.
func TestRetiredTabsGoWhereTheirSectionsWent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(LLMTable, "provider", "openai")
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux, "/admin")
	live := map[string]bool{"": true}
	for _, tab := range adminLayout {
		live[ui.SectionSlug(tab.tab)] = true
	}
	for old, to := range retiredTabs {
		if live[old] && old != "" {
			t.Errorf("%q is retired but is still a tab", old)
		}
		if !live[to] {
			t.Errorf("%q retires to %q, which is not a tab", old, to)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/admin/"+old, nil))
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/admin/"+to {
			t.Errorf("/admin/%s answered %d %q, want /admin/%s", old, w.Code, w.Header().Get("Location"), to)
		}
	}
}

// A #tab/section link written before the section moved still reaches it: the
// page knows where every section is and goes there.
func TestASectionLinkFollowsTheSection(t *testing.T) {
	js := adminTabRedirectScript([]adminTab{{name: "Services", slug: "services", url: "/admin/services"}},
		map[string]string{"connectors": "services"}, "overview")
	for _, want := range []string{`"connectors":"services"`, "where[s]", "tabs[at]+'#'+at+'/'+s"} {
		if !strings.Contains(js, want) {
			t.Errorf("the redirect script lacks %s", want)
		}
	}
}
