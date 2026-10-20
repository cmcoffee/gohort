package sections

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

// AdminAppEntry is an app the admin page administers that was not compiled in:
// one people author while the server runs. Each is a row in the admin page's
// list of apps, and a page of its own reached from that row.
//
// A row and a page rather than a section, because a section per app made the
// admin tab's index grow with every app anybody built. The list is read down a
// column; one app's controls are opened on their own.
type AdminAppEntry struct {
	// Key names the app among the entries, stable across renders. It is the
	// address of the app's page, so it must not change when the app is renamed.
	Key   string
	Name  string
	Owner string
	Desc  string
	// Disabled marks an app switched off; Access says who can open it, in a
	// few words ("its owner only", "shared - public link").
	Disabled bool
	Access   string
	// Settings says what can be set on its page ("4 controls"), and Page
	// builds that page's sections. Page is called only when the page is
	// opened; nil means there is nothing to set, and the row opens nothing.
	Settings string
	Page     func() []ui.Section
	// Head is ExtraHeadHTML the page's controls need; may be empty.
	Head string
}

// AdminAppSource lists the authored apps for one request. Called on each
// render, so the list tracks what exists. Access checks are the source's own,
// as for AdminSectionSource.
type AdminAppSource func(r *http.Request) []AdminAppEntry

var adminAppSources []AdminAppSource

// RegisterAdminAppSource adds a source of authored apps to the admin page.
func RegisterAdminAppSource(fn AdminAppSource) {
	if fn != nil {
		adminAppSources = append(adminAppSources, fn)
	}
}

// AdminAppEntriesFor returns every authored app for one request, in the
// sources' registration order.
func AdminAppEntriesFor(r *http.Request) []AdminAppEntry {
	var out []AdminAppEntry
	for _, src := range adminAppSources {
		out = append(out, src(r)...)
	}
	return out
}
