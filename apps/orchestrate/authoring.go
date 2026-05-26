// Authoring-in-progress side channel. ChatSession.AuthoringAgentID is
// the agent the user is currently authoring in a session; we use a
// dedicated flat table keyed by chat session id so the global
// create_agent handler (which only sees ToolSession, not chatTurn or
// ChatSession) can write without needing to plumb the per-agent
// session-storage shape (which is keyed by agentID + sessionID, info
// the handler doesn't have).
//
// chatTurn reads this table at turn start and stamps the value onto
// the in-memory ChatSession so chatTurn-bound handlers
// (create_pipeline_tool, etc.) can read it directly off the session.
//
// V1 has no auto-clear; the next session is the natural reset. A
// future "end_authoring" tool or auto-clear on user signal can land
// when the workflow needs it.

package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

const authoringTable = "orchestrate_authoring"

// saveAuthoringInProgress records the agent currently being authored
// in the given chat session. Idempotent — repeated saves with the
// same agentID are no-ops by way of Set's overwrite semantics.
func saveAuthoringInProgress(db Database, sessionID, agentID string) {
	if db == nil || sessionID == "" || agentID == "" {
		return
	}
	db.Set(authoringTable, sessionID, agentID)
}

// loadAuthoringInProgress returns the agent id currently being
// authored in the given session, or "" if none.
func loadAuthoringInProgress(db Database, sessionID string) string {
	if db == nil || sessionID == "" {
		return ""
	}
	var id string
	if !db.Get(authoringTable, sessionID, &id) {
		return ""
	}
	return id
}

// clearAuthoringInProgress drops the slot. Called when the session
// is deleted so the table doesn't accumulate dead pointers.
func clearAuthoringInProgress(db Database, sessionID string) {
	if db == nil || sessionID == "" {
		return
	}
	db.Unset(authoringTable, sessionID)
}
