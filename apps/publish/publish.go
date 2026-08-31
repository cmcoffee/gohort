// Package publish is the outbound home for finished documents: it owns the
// concrete publish destinations (Confluence, a generic webhook), their
// deployment configuration, and the Publisher agent that a writer app opens
// when a user clicks Publish.
//
// It is deliberately NOT a writer app and has no page of its own. Guides (and
// later techwriter / codewriter) call BuildPublishTools to hand the Publisher
// agent a kit for one chat turn; everything about Confluence — its REST shape,
// its storage format, its version numbers — lives here and nowhere else. The
// writer app only ever knows the publish-destination registry in core/docs.
//
// The split that matters: the AGENT decides where a document goes and what it
// is called; this package's destinations do the WRITE. The agent never speaks
// HTTP, and the registry's PublishDocument refuses a target that isn't one the
// destination itself listed, so an outbound write can't land somewhere the
// model composed from memory.
package publish

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// registeredApp is the live instance the destinations read their configuration
// from at REQUEST time — they're registered in init(), before T.DB is wired, so
// they close over the app rather than over a config value. Same pattern as
// filestore's reference source.
var registeredApp *PublishApp

func init() {
	app := new(PublishApp)
	registeredApp = app
	RegisterApp(app)
	registerPublisherAgent()
	RegisterAdminSection(AdminSectionEntry{Section: adminSection(), App: "/publish"})
}

// PublishApp carries the framework boilerplate and the deployment's destination
// configuration. It has no dashboard card: publishing is reached from the
// document you're publishing, never from a page of its own.
type PublishApp struct {
	AppCore
}

func (T PublishApp) Name() string { return "publish" }
func (T PublishApp) Desc() string {
	return "Apps: publish finished documents out to Confluence and other destinations."
}
func (T PublishApp) SystemPrompt() string { return "" }
func (T *PublishApp) Init() error         { return T.Flags.Parse() }
func (T *PublishApp) Main() error {
	Log("publish is configured from the admin UI. Start with: gohort serve")
	return nil
}

func (T *PublishApp) WebPath() string { return "/publish" }
func (T *PublishApp) WebName() string { return "Publishing" }
func (T *PublishApp) WebDesc() string {
	return "Where finished documents go — Confluence and other destinations."
}

// WebHidden keeps Publishing off the dashboard. It configures a capability other
// apps use; there is nothing here for a user to open on its own. Its routes
// still serve the admin section's config form.
func (T *PublishApp) WebHidden() bool { return true }

func (T *PublishApp) Routes() {
	// Registered here, not in init, because Available() reads T.DB for the
	// configured credential and the store isn't live until now.
	docs.RegisterPublishDestination(&confluenceDest{app: T})
	docs.RegisterPublishDestination(&webhookDest{app: T})
	T.HandleFunc("/", T.route)
}

func (T *PublishApp) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/config":
		T.handleConfig(w, r)
	case "/api/destinations":
		T.handleDestinations(w, r)
	default:
		http.NotFound(w, r)
	}
}
