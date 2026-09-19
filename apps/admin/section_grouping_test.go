package admin

// Which tab each admin section lands on.
//
// The map keys on TITLE and the rank keys on GROUP, so both are a name written
// twice with nothing checking they agree. A section missing from the first
// keeps the empty group that renders as "General"; a group missing from the
// second sorts to 0 and ties with System. Neither fails anywhere — the section
// simply appears somewhere nobody looked for it.

import (
	"os"
	"regexp"
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
		a.extensionsSections, a.sourceHooksSections, a.toolsSections, a.skillsSections,
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

// Every group named by the map has to be ranked, or it sorts to zero and its
// tab appears wherever System is.
func TestEveryGroupIsRanked(t *testing.T) {
	groups, ranks := adminSectionMaps(t)
	for title, g := range groups {
		if _, ok := ranks[g]; !ok {
			t.Errorf("section %q is grouped under %q, which has no rank: its tab sorts to the front", title, g)
		}
	}
	// And the reverse: a rank for a group nothing uses is a tab that never
	// appears, which reads as a plan somebody abandoned.
	// Groups whose sections declare their own Group rather than being mapped by
	// title here: the Apps tab rows, and the app-contributed sections that
	// register themselves through core (AdminSectionEntriesFor) so admin does
	// not import the app.
	used := map[string]bool{"Apps": true, "Prompts": true}
	for _, g := range groups {
		used[g] = true
	}
	for g := range ranks {
		if !used[g] {
			t.Errorf("group %q is ranked but nothing is in it", g)
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

// adminSectionMaps reads the two maps out of the page source. Read rather than
// exported, because making them package-level to test them is a change to the
// thing under test.
func adminSectionMaps(t *testing.T) (map[string]string, map[string]int) {
	t.Helper()
	src := readAdminPageSource(t)
	groups := map[string]string{}
	i := strings.Index(src, "sectionGroup := map[string]string{")
	j := strings.Index(src[i:], "\n\t}")
	for _, m := range pairRe.FindAllStringSubmatch(src[i:i+j], -1) {
		groups[m[1]] = m[2]
	}
	ranks := map[string]int{}
	i = strings.Index(src, "groupRank := map[string]int{")
	j = strings.Index(src[i:], "}\n")
	for _, m := range rankRe.FindAllStringSubmatch(src[i:i+j], -1) {
		ranks[m[1]] = 0
	}
	if len(groups) == 0 || len(ranks) == 0 {
		t.Fatal("could not read the section maps; page.go has been reshaped")
	}
	return groups, ranks
}

var (
	pairRe = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]+)"`)
	rankRe = regexp.MustCompile(`"([^"]+)"\s*:\s*\d+`)
)

// readAdminPageSource returns page.go, where both maps live.
func readAdminPageSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
