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
	"github.com/cmcoffee/gohort/core/jsonapi"
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
	// A SOURCE rather than a fixed section: the Agent column's suggestions are
	// the requesting admin's agents, which is runtime data. Registered the same
	// way custom apps register theirs, and gated the same way — a source that
	// trusts its caller leaks the moment somebody renders it elsewhere.
	RegisterAdminSectionSource(func(r *http.Request) []AdminSectionEntry {
		if !AuthIsAdmin(AuthDB(), r) {
			return nil
		}
		return []AdminSectionEntry{{Section: adminSection(r), App: "/publish"}}
	})
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
	return "Where finished documents go: Confluence and other destinations."
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
	T.registerAgentDestinations()
	docs.RegisterPublishDestination(&targetsDest{app: T})
	// The deployment's configuration is admin-only: it names which credential
	// reaches out to which external system.
	admin := jsonapi.Authorize(func(w http.ResponseWriter, r *http.Request) bool {
		if !RequestIsAdmin(r) {
			http.Error(w, "Publishing settings are admin-only: they name which credential writes to which external system.", http.StatusForbidden)
			return false
		}
		return true
	})
	T.HandleFunc("/api/config", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleConfig, admin),
		"POST": jsonapi.Post(T.DB, T.handleConfigSave, admin),
	}.Serve)
	T.HandleFunc("/api/destinations", jsonapi.Get(T.DB, T.handleDestinations))
	// The Publishing targets section: the list and a save, one target, which
	// agents may publish to it, and a field's live options.
	T.HandleFunc("/api/targets", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleTargets),
		"POST": jsonapi.Post(T.DB, T.handleTargetSave, jsonapi.MaxBody(1<<18)),
	}.Serve)
	T.HandleFunc("/api/targets/{id}", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.handleTarget),
		"DELETE": jsonapi.Post(T.DB, T.handleTargetDelete),
	}.Serve)
	T.HandleFunc("/api/targets/{id}/agents", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleTargetAgents),
		"POST": jsonapi.Post(T.DB, T.handleTargetAgentSet, jsonapi.MaxBody(1<<16)),
	}.Serve)
	T.HandleFunc("/api/targets/{id}/options", jsonapi.Get(T.DB, T.handleTargetOptions))
	T.HandleFunc("/", http.NotFound)
}
