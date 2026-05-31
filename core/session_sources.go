// Extra session sources — registry that lets non-Agency apps
// surface their own agent-conversation history alongside Agency's
// native sessions list. Today phantom (iMessage bridge) uses this:
// phantom dispatches stay agent-scoped but live under a per-chat
// runtime user (phantom:<chatID>), so they don't appear in Agency's
// own session table. Phantom registers a source that enumerates
// those scopes; Agency's /api/sessions merges them in with a
// `source:"phantom"` tag so the UI can render a badge.
//
// Pattern is decoupled in both directions:
//   - core defines the interface only; no apps imported here.
//   - apps register their sources in their package init() so the
//     registry is populated before any HTTP request fires.
//   - orchestrate queries the registry without knowing which apps
//     contribute. Adding a new source = new RegisterSessionSource
//     call in the new app, no orchestrate edits.

package core

import (
	"sort"
	"sync"
	"time"
)

// ExtraSessionItem is the shape Agency's sessions-list response
// expects for non-native rows. Mirrors the ChatSession fields the
// list rail reads — ID, Title, AgentID, LastAt — plus the source
// tagging fields the UI uses to render a badge / disambiguate.
type ExtraSessionItem struct {
	ID      string    `json:"id"`
	AgentID string    `json:"agent_id"`
	Title   string    `json:"title"`
	LastAt  time.Time `json:"last_at"`
	// Source identifies which app contributed this row. Free-form
	// string; the UI checks for known values (e.g. "phantom") to
	// render a per-source visual marker.
	Source string `json:"source,omitempty"`
	// ChatID — for source="phantom", the iMessage chat ID this
	// dispatch belongs to. UI can show it as a small "from X"
	// badge. Empty for other sources.
	ChatID string `json:"chat_id,omitempty"`
}

// ExtraSessionsSource is implemented by apps that contribute rows
// to Agency's sessions list AND own the load/delete flow for those
// rows (since the canonical storage scope differs from the
// requesting user's DB — phantom dispatches live under
// phantom:<chatID>, not the admin's own scope).
//
// All three methods receive the orchestrate app DB. Sources are
// responsible for verifying that the requesting user has the right
// to see / mutate the row before returning data.
type ExtraSessionsSource interface {
	// ListForAgent enumerates contributing rows for the given agent
	// + current user. Tag the returned items with this source's
	// name (matches the registration key) so the UI can route
	// later loads back to the same source.
	ListForAgent(db Database, agentID, user string) []ExtraSessionItem
	// LoadSession returns the full transcript for a row this source
	// claims. chatID is the per-row routing key (e.g. phantom's
	// iMessage chat ID) — opaque to orchestrate; sources interpret
	// it. Returns the session payload (any JSON-serializable shape
	// the UI understands, typically the source's own ChatSession
	// type) and true; returns nil + false when the row doesn't
	// exist or the user isn't allowed to read it.
	LoadSession(db Database, agentID, user, sessionID, chatID string) (any, bool)
}

var (
	extraSessionsMu      sync.RWMutex
	extraSessionsSources = map[string]ExtraSessionsSource{}
)

// RegisterExtraSessionsSource records a source under the given name.
// Re-registering the same name replaces the prior impl (useful for
// hot-reload during dev). Call from package init() so the source
// is wired before any /api/sessions request arrives. Name must
// match what the source tags onto its ExtraSessionItem.Source
// values; orchestrate uses it to route /api/sessions/<id>?source=X
// back to the right impl on load.
func RegisterExtraSessionsSource(name string, source ExtraSessionsSource) {
	if name == "" || source == nil {
		return
	}
	extraSessionsMu.Lock()
	defer extraSessionsMu.Unlock()
	extraSessionsSources[name] = source
}

// LookupExtraSessionsSource fetches a single source by name. Used by
// orchestrate's load handler to delegate to the source that owns
// the row being loaded.
func LookupExtraSessionsSource(name string) (ExtraSessionsSource, bool) {
	extraSessionsMu.RLock()
	defer extraSessionsMu.RUnlock()
	s, ok := extraSessionsSources[name]
	return s, ok
}

// CollectExtraSessions queries every registered source for the
// agent + user and returns the merged (date-sorted) result. Empty
// when no sources are registered or none have matching rows.
func CollectExtraSessions(db Database, agentID, user string) []ExtraSessionItem {
	extraSessionsMu.RLock()
	srcs := make([]ExtraSessionsSource, 0, len(extraSessionsSources))
	for _, s := range extraSessionsSources {
		srcs = append(srcs, s)
	}
	extraSessionsMu.RUnlock()

	var out []ExtraSessionItem
	for _, s := range srcs {
		out = append(out, s.ListForAgent(db, agentID, user)...)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastAt.After(out[j].LastAt)
	})
	return out
}
