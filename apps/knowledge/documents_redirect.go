// Documents → Knowledge URL redirect.
//
// The top-level surface was renamed from "Documents" (/documents/) to
// "Knowledge" (/knowledge/) when it became clear the surface is more
// about creating knowledge bundles for skills than about file storage.
// This file registers a tiny standalone app at /documents/ whose only
// job is to 302-redirect to the equivalent /knowledge/ path, so
// bookmarks, hard-coded paths, and any external links keep working.
//
// Hidden from the dashboard (no tile) so it doesn't show up as a
// duplicate app — only the real Knowledge app's tile is visible.

package knowledge

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterApp(new(documentsRedirectApp)) }

// documentsRedirectApp is a near-empty SimpleWebApp whose entire job
// is to 302-redirect /documents/* → /knowledge/*. Subsumes the legacy
// URL path so old bookmarks resolve cleanly.
type documentsRedirectApp struct {
	AppCore
}

func (T documentsRedirectApp) Name() string         { return "documents-redirect" }
func (T documentsRedirectApp) SystemPrompt() string { return "" }
func (T documentsRedirectApp) Desc() string {
	return "Internal: redirects legacy /documents/ URLs to /knowledge/."
}

func (T *documentsRedirectApp) Init() error { return T.Flags.Parse() }
func (T *documentsRedirectApp) Main() error { return nil }

func (T *documentsRedirectApp) WebPath() string { return "/documents" }
func (T *documentsRedirectApp) WebName() string { return "Documents (legacy)" }
func (T *documentsRedirectApp) WebDesc() string { return "Redirects to Knowledge." }
func (T *documentsRedirectApp) WebHidden() bool { return true }

func (T *documentsRedirectApp) Routes() {
	T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is relative to /documents (StripPrefix). Build
		// the equivalent /knowledge/<rest> URL, preserving the query
		// string for any future params we might add.
		target := "/knowledge" + r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
}
