// Bridge that lets Agency surface phantom-dispatched agent sessions
// alongside the user's own. Phantom dispatches store ChatSessions
// under a per-chat runtime user (phantom:<chatID>) so each iMessage
// thread's state stays isolated. That isolation is the right
// architecture, but it hides those sessions from Agency's normal
// sessions list, which only reads the logged-in user's own DB.
//
// Here we register a core ExtraSessionsSource that:
//   - Enumerates every chat phantom knows about (conversationTable).
//   - For each, loads the phantom:<chatID>-scoped sessions for the
//     given agent.
//   - Returns them as ExtraSessionItems tagged source="phantom" with
//     a friendly chat label, so Agency's rail can render a "from
//     <chat name>" badge alongside the row.
//   - On load (LoadSession), swaps to the phantom:<chatID> DB and
//     returns the full transcript so the chat pane can replay it.
//
// Filtering by the requesting Agency user: phantom is single-tenant
// (one phone, one admin per deployment — see phantomAgentOwner).
// Every chat belongs to that admin; non-admins get an empty list.
//
// chatID routing: ExtraSessionItem.ChatID is the user-facing label
// (display name or handle, see chatLabelForBadge) for the rail
// badge. The TRUE chat ID used for storage routing rides separately
// — we store the raw chat ID alongside the label in the rail row
// so LoadSession can recover it. Without that split the badge would
// show e.g. "iMessage;-;+14155551234" instead of a contact name.

package phantom

import (
	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// instance — the package-init registration needs the live DB once
// it's set on the app object. We grab the app instance the first
// time the source is queried (Init has run by then). Stored as a
// package-level pointer for lazy access.
var phantomAppForExport *Phantom

// setPhantomForExport is called from Phantom.Init() so the source
// has a handle to the live DB + state. Idempotent.
func setPhantomForExport(app *Phantom) {
	phantomAppForExport = app
}

// phantomSessionSource implements core.ExtraSessionsSource.
type phantomSessionSource struct{}

func init() {
	RegisterExtraSessionsSource("phantom", phantomSessionSource{})
}

// ListForAgent enumerates phantom dispatches for the given agent.
// Single-tenant gate: only the configured phantom admin owner
// receives results; anyone else gets an empty list (their own
// sessions still flow through Agency's native handler).
func (phantomSessionSource) ListForAgent(_ Database, agentID, user string) []ExtraSessionItem {
	app := phantomAppForExport
	if app == nil || app.DB == nil || agentID == "" {
		return nil
	}
	owner := phantomAgentOwner(app.DB)
	if owner == "" || owner != user {
		return nil
	}
	var out []ExtraSessionItem
	for _, chatID := range app.DB.Keys(conversationTable) {
		if chatID == "" {
			continue
		}
		udb := UserDB(app.DB, phantomDispatchRuntimeUser(chatID))
		if udb == nil {
			continue
		}
		label := chatLabelForBadge(app.DB, chatID)
		for _, s := range orchestrate.ListChatSessions(udb, agentID) {
			out = append(out, ExtraSessionItem{
				ID:      s.ID,
				AgentID: agentID,
				Title:   s.Title,
				LastAt:  s.LastAt,
				Source:  "phantom",
				// ChatID carries BOTH the routing key and the label:
				// "raw|label". Orchestrate hands this back on load
				// requests; we split it to find the right scope.
				// Encoding both avoids needing a second field on
				// ExtraSessionItem that all other sources would have
				// to leave empty.
				ChatID: chatID + "|" + label,
			})
		}
	}
	return out
}

// LoadSession resolves a phantom session back to its transcript.
// chatID carries our composite "<rawChatID>|<label>" — extract the
// raw chat ID, open the per-chat scope, hand the session back.
//
// Returns (payload, true) on success; (nil, false) on missing-row
// or user-not-allowed. Orchestrate translates a false return into
// the same 404 it serves for native-row misses, so a leaked source
// name from a non-admin user can't enumerate phantom session IDs.
func (phantomSessionSource) LoadSession(_ Database, agentID, user, sessionID, chatID string) (any, bool) {
	app := phantomAppForExport
	if app == nil || app.DB == nil || agentID == "" || sessionID == "" {
		return nil, false
	}
	owner := phantomAgentOwner(app.DB)
	if owner == "" || owner != user {
		return nil, false
	}
	rawChat := chatID
	for i := 0; i < len(chatID); i++ {
		if chatID[i] == '|' {
			rawChat = chatID[:i]
			break
		}
	}
	if rawChat == "" {
		return nil, false
	}
	udb := UserDB(app.DB, phantomDispatchRuntimeUser(rawChat))
	if udb == nil {
		return nil, false
	}
	sess, ok := orchestrate.LoadChatSession(udb, agentID, sessionID)
	if !ok {
		return nil, false
	}
	return sess, true
}

// chatLabelForBadge picks the most human-readable identifier for a
// phantom chat — display name > handle > raw chat ID. Used as the
// "from X" badge in Agency's rail.
func chatLabelForBadge(db Database, chatID string) string {
	var conv Conversation
	if db != nil && db.Get(conversationTable, chatID, &conv) {
		if conv.DisplayName != "" {
			return conv.DisplayName
		}
		if conv.Handle != "" {
			return conv.Handle
		}
	}
	return chatID
}
