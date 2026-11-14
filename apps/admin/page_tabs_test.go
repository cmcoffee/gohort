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

// A Tuning category named like a section on another tab stays on Tuning: the
// ceilings for people outside gohort once showed on People as a second
// section of the same name as the table there.
func TestATuningCategoryStaysOnTuning(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(LLMTable, "provider", "openai")
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	// The ceilings are registered by orchestrate, which this package does
	// not load: one of the same category stands in.
	RegisterTunable(TunableSpec{Key: "tune_outsidetabtest", Category: "External contacts", Label: "T",
		Kind: KindInt, Default: 1, Min: 0, Max: 10})
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux, "/admin")
	get := func(path string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		return w.Body.String()
	}
	for _, s := range buildTunableSections() {
		if g := sectionTab(s.Title, s.Group); g != tuningTab {
			t.Errorf("the Tuning category %q is moved to %q", s.Title, g)
		}
	}
	title := `"title":"External contacts"`
	if n := strings.Count(get("/admin/people"), title); n != 1 {
		t.Errorf("People has %d sections titled External contacts, want 1", n)
	}
	if n := strings.Count(get("/admin/tuning"), title); n != 1 {
		t.Errorf("Tuning has %d sections titled External contacts, want 1", n)
	}
}

// Adding someone is a button on Users that opens a dialog, not a section of
// its own.
func TestAddUserIsADialogOnUsers(t *testing.T) {
	var users *ui.Section
	for _, s := range (&AdminApp{}).systemSections() {
		if s.Title == "Add account" {
			t.Error("Add account is still a section")
		}
		if s.Title == "Users" {
			s := s
			users = &s
		}
	}
	if users == nil {
		t.Fatal("no Users section")
	}
	stack, ok := users.Body.(ui.Stack)
	if !ok || len(stack.Children) != 2 {
		t.Fatalf("the Users section is %T", users.Body)
	}
	btn, ok := stack.Children[0].(ui.ModalButton)
	if !ok || btn.Label != "Add user" {
		t.Fatalf("the Users section opens with %#v", stack.Children[0])
	}
	if card, ok := btn.Body.(ui.Card); !ok || !strings.Contains(card.HTML, "box.addAction") {
		t.Error("the Add user dialog's button does not join its footer")
	}
	if _, ok := stack.Children[1].(ui.Table); !ok {
		t.Errorf("the users list is %T", stack.Children[1])
	}
	if _, ok := sectionRank["Add account"]; ok {
		t.Error("Add account is still in the layout")
	}
}
