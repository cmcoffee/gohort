package admin

// An app's pane links to the admin panels it contributes rather than rendering
// them a second time, and a hidden app that claims controls gets a pane but no
// switch.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

type fakeHiddenApp struct{ fakeWebApp }

func (fakeHiddenApp) WebHidden() bool { return true }

func init() {
	// Its only claim is a section already on the Apps tab: that section is its
	// row, so it gets no second one.
	RegisterApp(fakeHiddenApp{fakeWebApp{path: "/hiddenonappstab"}})
	RegisterAdminSection(AdminSectionEntry{App: "/hiddenonappstab",
		Section: ui.Section{Title: "On Apps Already", Group: AppsTabGroup}})
	// An untitled section, the only thing on its tab.
	RegisterApp(fakeHiddenApp{fakeWebApp{path: "/hiddenuntitled"}})
	RegisterAdminSection(AdminSectionEntry{App: "/hiddenuntitled",
		Section: ui.Section{Group: "Prompts"}})
	RegisterApp(fakeHiddenApp{fakeWebApp{path: "/hiddenpanetest"}})
	RegisterApp(fakeHiddenApp{fakeWebApp{path: "/hiddenclaimsnothing"}})
	RegisterAdminSection(AdminSectionEntry{App: "/hiddenpanetest",
		Section: ui.Section{Title: "Pane Panel Test", Group: "Extensions"}})
	// Declares one tab and is filed on another by the title map: the link has
	// to name where the section actually is.
	RegisterAdminSection(AdminSectionEntry{App: "/hiddenpanetest",
		Section: ui.Section{Title: "Templates", Group: "Somewhere Else"}})
}

func adminReq() *http.Request { return httptest.NewRequest("GET", "/admin", nil) }

func TestHiddenAppWithClaimsGetsAPaneButNoSwitch(t *testing.T) {
	var sawClaimed, sawEmpty bool
	for _, rw := range paneApps(adminReq()) {
		switch rw.path {
		case "/hiddenpanetest":
			sawClaimed = true
		case "/hiddenclaimsnothing":
			sawEmpty = true
		}
	}
	if !sawClaimed {
		t.Error("a hidden app that claims a panel should have a pane")
	}
	if sawEmpty {
		t.Error("a hidden app that claims nothing has nothing to show and should stay off the tab")
	}
	// The pane is not a switch: the reason hidden apps were kept off the
	// switchboard still holds.
	if isListableApp("/hiddenpanetest") {
		t.Error("a hidden app must not become switchable by having a pane")
	}
	for _, rw := range listableApps() {
		if rw.path == "/hiddenpanetest" {
			t.Error("a hidden app is on the switchboard")
		}
	}
}

func TestHiddenAppSummarySaysWhyThereIsNoSwitch(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	w := httptest.NewRecorder()
	a.handleAppSummary(w, httptest.NewRequest("GET", "/api/app-summary?path=/hiddenpanetest", nil))
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if state, _ := got["state"].(string); !strings.Contains(state, "no switch") {
		t.Errorf("state = %q, want it to say why there is no switch", state)
	}
}

func TestPanelLinksNameTheTabTheSectionIsOn(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	w := httptest.NewRecorder()
	a.handleAppPanels(w, httptest.NewRequest("GET", "/api/app-panels?path=/hiddenpanetest", nil))
	var rows []map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	want := map[string][2]string{
		"Pane Panel Test": {"Extensions", "#extensions/pane-panel-test"},
		// The title map files Templates under Extensions whatever it declares.
		"Templates": {"Extensions", "#extensions/templates"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v", rows)
	}
	for _, r := range rows {
		w, ok := want[r["title"]]
		if !ok || r["tab"] != w[0] || r["href"] != w[1] {
			t.Errorf("row %v, want tab %q href %q", r, w[0], w[1])
		}
	}
	if sectionTab("Templates", "Somewhere Else") == "Somewhere Else" {
		t.Error("test premise: Templates should be filed by the title map")
	}
}

// Linked, never rendered twice: a contributed section is a whole surface and
// two live copies of it on one page are two editors over one record.
func TestPaneLinksPanelsRatherThanRenderingThem(t *testing.T) {
	st, ok := appPaneBody(adminReq(), "/hiddenpanetest").(ui.Stack)
	if !ok {
		t.Fatalf("pane body = %T", appPaneBody(adminReq(), "/hiddenpanetest"))
	}
	// Summary and the link table, nothing else: this app claims no routing
	// or knobs, so any third child is a contributed section's body.
	if len(st.Children) != 2 {
		t.Fatalf("pane has %d children, want summary + link table", len(st.Children))
	}
	var tbl *ui.Table
	if t2, ok := st.Children[1].(ui.Table); ok && t2.Source == appPanelsSource("/hiddenpanetest") {
		tbl = &t2
	}
	if tbl == nil {
		t.Fatal("no panel-link table on the pane")
	}
	if tbl.Columns[0].Link != "href" {
		t.Errorf("title column is not a link: %+v", tbl.Columns[0])
	}
}

func TestASectionAlreadyOnTheAppsTabIsNotASecondRow(t *testing.T) {
	for _, rw := range paneApps(adminReq()) {
		if rw.path == "/hiddenonappstab" {
			t.Error("a hidden app whose only panel is on the Apps tab got a pane beside it")
		}
	}
}

// The untitled section is reached by its tab alone, with the slash kept: a bare
// "#prompts" would resolve to an Apps-tab row of the same name.
func TestAnUntitledPanelLinksToItsTab(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	w := httptest.NewRecorder()
	a.handleAppPanels(w, httptest.NewRequest("GET", "/api/app-panels?path=/hiddenuntitled", nil))
	var rows []map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if len(rows) != 1 || rows[0]["title"] != "Prompts" || rows[0]["href"] != "#prompts/" {
		t.Errorf("rows = %v, want one link titled after its tab to #prompts/", rows)
	}
}
