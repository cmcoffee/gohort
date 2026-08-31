package sections

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
)

// A source is called on EVERY render, which is the whole reason it is a source
// and not a registration: its rows are records people write while the server is
// running, and a list fixed at startup is a list that is wrong by lunchtime.
func TestAdminSectionSourceIsAskedEveryTime(t *testing.T) {
	calls := 0
	rows := []AdminSectionEntry{}
	RegisterAdminSectionSource(func(r *http.Request) []AdminSectionEntry {
		calls++
		return rows
	})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)

	if got := len(AdminSectionEntriesFor(req)) - len(AdminSectionEntries()); got != 0 {
		t.Fatalf("an empty source contributed %d sections", got)
	}
	rows = []AdminSectionEntry{{Section: ui.Section{Title: "Written after startup"}}}
	after := AdminSectionEntriesFor(req)
	if len(after) != len(AdminSectionEntries())+1 {
		t.Fatalf("a row added after startup did not appear: %d entries", len(after))
	}
	if calls != 2 {
		t.Errorf("source called %d times across two renders", calls)
	}
	// Static first, so an order stable for releases stays stable and a
	// source's rows land after it rather than interleaved.
	if after[len(after)-1].Section.Title != "Written after startup" {
		t.Error("runtime sections must come after the statically registered ones")
	}
}

// The static reader keeps answering the question it always answered.
func TestStaticEntriesAreUnaffectedBySources(t *testing.T) {
	before := len(AdminSectionEntries())
	RegisterAdminSectionSource(func(r *http.Request) []AdminSectionEntry {
		return []AdminSectionEntry{{Section: ui.Section{Title: "runtime"}}}
	})
	if got := len(AdminSectionEntries()); got != before {
		t.Errorf("a source changed the static registry: %d then %d", before, got)
	}
	// A nil source is ignored rather than panicking a whole admin page.
	RegisterAdminSectionSource(nil)
	_ = AdminSectionEntriesFor(httptest.NewRequest(http.MethodGet, "/admin", nil))
}
