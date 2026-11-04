package admin

// Which tab each admin section lands on.
//
// adminLayout names every tab and the sections on it, in order. A section it
// does not list keeps the group it declared, and an empty one renders as
// "General": nothing fails, the section simply appears somewhere nobody
// looked for it.

import (
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
)

// everyAdminSection is every section the page appends, by title.
func everyAdminSection(t *testing.T) []ui.Section {
	t.Helper()
	a := &AdminApp{}
	var out []ui.Section
	for _, build := range []func() []ui.Section{
		a.systemSections, a.llmSections, a.costSections, a.capabilitiesSections,
		a.maintenanceSections, a.credentialsSections, a.governanceSections,
		a.extensionsSections, a.researchSourcesSections, a.toolsSections, a.skillsSections,
	} {
		func() {
			defer func() { recover() }() // a builder needing a live store is not what this tests
			out = append(out, build()...)
		}()
	}
	return out
}

// A section with no entry in the map lands on "General", which is not a tab
// anybody designed — it is the absence of a decision.
func TestNoAdminSectionFallsIntoGeneral(t *testing.T) {
	groups, _ := adminSectionMaps(t)
	for _, s := range everyAdminSection(t) {
		if s.Title == "" || s.Group != "" {
			continue // untitled sections are dropped by Render; pre-grouped ones opted out
		}
		if _, ok := groups[s.Title]; !ok {
			t.Errorf("section %q has no tab, so it lands in General away from whatever it belongs with", s.Title)
		}
	}
}

// Every tab the layout names is reached by something, or it is a tab that
// never appears, which reads as a plan somebody abandoned. A tab listing no
// sections has to be one that sections declare for themselves.
func TestEveryTabInTheLayoutIsFilled(t *testing.T) {
	declared := map[string]bool{AppsTabGroup: true, "Tuning": true}
	for _, tab := range adminLayout {
		if len(tab.sections) == 0 && !declared[tab.tab] {
			t.Errorf("tab %q lists no sections and none declare it", tab.tab)
		}
	}
}

// A section listed twice is on whichever tab the second listing says, and the
// first listing is a lie about where it is.
func TestEachSectionIsListedOnce(t *testing.T) {
	seen := map[string]string{}
	for _, tab := range adminLayout {
		for _, title := range tab.sections {
			if prev, ok := seen[title]; ok {
				t.Errorf("%q is listed on %q and on %q", title, prev, tab.tab)
			}
			seen[title] = tab.tab
		}
	}
}

// The three maintenance groups are the ones that were actually wrong, and the
// ones most likely to drift back: they are declared far from the map.
func TestTheMaintenanceGroupsAreOnTheMaintenanceTab(t *testing.T) {
	groups, _ := adminSectionMaps(t)
	for _, title := range []string{"Reclaim space", "Reports", "Housekeeping", "Migrations"} {
		if groups[title] != "Maintenance" {
			t.Errorf("%q is on the %q tab, want Maintenance", title, groups[title])
		}
	}
}

// adminSectionMaps returns the section-to-tab map and the tab ranks, both
// read from adminLayout.
func adminSectionMaps(t *testing.T) (map[string]string, map[string]int) {
	t.Helper()
	if len(sectionGroup) == 0 || len(tabRank) == 0 {
		t.Fatal("could not read the section maps; adminLayout is empty")
	}
	return sectionGroup, tabRank
}

// Nothing on the admin page opens closed.
//
// Asked for twice: once as "collapsed things I'm not sure why it's collapsed",
// and again after the first answer moved them to the right tab instead of
// opening them. A section you have to click to discover is a section you do not
// know is there, and every one of these already has a heading saying what it
// is. This is a page an operator reads down, not a settings drawer.
//
// Covers both shapes, because they read identically on screen and only one of
// them was noticed the first time: a Section that starts closed, and a form
// header that folds the fields under it.
func TestNothingInAdminStartsCollapsed(t *testing.T) {
	for _, s := range everyAdminSection(t) {
		if s.Collapsed {
			t.Errorf("section %q starts closed", s.Title)
		}
		panel, ok := s.Body.(ui.FormPanel)
		if !ok {
			continue
		}
		for _, f := range panel.Fields {
			if f.Collapsed {
				t.Errorf("section %q folds its fields under the %q header", s.Title, f.Label)
			}
		}
	}
	// The builders above skip anything needing a live store, so the source is
	// checked too: a collapsed declaration added to one of those would not be
	// reached by the walk.
	for _, name := range []string{"page_maintenance.go", "page_llm.go", "page_system.go", "page_capabilities.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), "Collapsed: true") {
			t.Errorf("%s still declares Collapsed: true", name)
		}
	}
}

// The governance view answers "what reaches other people, and by whose
// decision". A per-user ADOPTION ledger answered something else: which of the
// tools somebody is already permitted to have they happen to want loaded.
// That is their own preference, an admin reaching into it was taking away a
// choice rather than a permission, and the row it produced multiplied with
// every user.
//
// This pins the replacement rather than the removal: the section lists the
// CATALOG, one row per published tool, with the two levers that actually
// govern it.
func TestGovernanceListsTheToolCatalogNotWhoAdoptedIt(t *testing.T) {
	var tools *ui.Section
	for i, s := range (&AdminApp{}).governanceSections() {
		if s.Title == "Shared tools" {
			tools = &(&AdminApp{}).governanceSections()[i]
		}
		if strings.Contains(strings.ToLower(s.Title), "adoption") {
			t.Errorf("the adoption ledger is back as %q: an admin governs permission, not preference", s.Title)
		}
	}
	if tools == nil {
		t.Fatal("the published-tool catalog is gone from governance")
	}
	table, ok := tools.Body.(ui.Table)
	if !ok {
		t.Fatalf("the catalog is not a table: %T", tools.Body)
	}
	if table.Source != "api/global-tools" {
		t.Errorf("source = %q, want the catalog listing", table.Source)
	}
	// One row per TOOL. Keying on anything carrying a username would be the
	// per-user ledger wearing a different name.
	if table.RowKey != "id" {
		t.Errorf("row key = %q", table.RowKey)
	}
	for _, c := range table.Columns {
		if c.Field == "user" || c.Field == "adopted_by" {
			t.Errorf("the catalog lists adopters again (column %q)", c.Field)
		}
	}
	// The two levers that govern a published tool: who may take it, and
	// whether it is published at all.
	var labels []string
	for _, a := range table.RowActions {
		labels = append(labels, a.Label)
	}
	for _, want := range []string{"Access", "Unshare"} {
		found := false
		for _, l := range labels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the catalog row has no %q; the levers are %v", want, labels)
		}
	}
}
