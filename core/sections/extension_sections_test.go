package sections

// The Extensions registry. It exists so the Extensions page does not
// import another app's concepts to render them, which is the leak the
// other two section registries were created to prevent.

import (
	"net/http"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
)

func TestExtensionSectionsSortByOrderThenRegistration(t *testing.T) {
	prev := extensionSections
	t.Cleanup(func() { extensionSections = prev })
	extensionSections = nil

	mk := func(title string, order int) ExtensionSectionEntry {
		return ExtensionSectionEntry{
			Order: order,
			Build: func(*http.Request, string) (ui.Section, bool) {
				return ui.Section{Title: title}, true
			},
		}
	}
	RegisterExtensionSection(mk("late", 20))
	RegisterExtensionSection(mk("first", 0))
	RegisterExtensionSection(mk("also first", 0))
	RegisterExtensionSection(mk("middle", 10))

	var got []string
	for _, e := range ExtensionSectionEntries() {
		s, _ := e.Build(nil, "u")
		got = append(got, s.Title)
	}
	want := []string{"first", "also first", "middle", "late"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// Reading must not mutate the registry — a page builds on every
	// request, and a sort that reordered the source would make the page
	// depend on how many times it had been viewed.
	if extensionSections[0].Order != 20 {
		t.Error("reading the entries reordered the underlying registry")
	}
}

// A section whose Build says no is skipped, which is how an app hides a
// surface from users who cannot reach it.
func TestExtensionSectionCanDeclineForAUser(t *testing.T) {
	prev := extensionSections
	t.Cleanup(func() { extensionSections = prev })
	extensionSections = nil

	RegisterExtensionSection(ExtensionSectionEntry{
		Build: func(_ *http.Request, user string) (ui.Section, bool) {
			return ui.Section{Title: "Mine"}, user == "alice"
		},
	})
	if _, ok := ExtensionSectionEntries()[0].Build(nil, "bob"); ok {
		t.Error("a section must be able to decline for a user without the grant")
	}
	if _, ok := ExtensionSectionEntries()[0].Build(nil, "alice"); !ok {
		t.Error("and must still render for one who has it")
	}
}
