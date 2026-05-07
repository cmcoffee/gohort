// Persistence + approval queue for temp tools the LLM marked persist=true.
//
// Two DB-backed pools, both keyed by username:
//   - pendingTempTools: tools awaiting human approval. Visible in the
//     admin UI with the full command_template displayed; user clicks
//     Approve to move into the active pool, or Reject to discard.
//   - persistentTempTools: approved tools that load into every fresh
//     chat session for that user. The user can delete them from the
//     admin UI to break out of any tool that misbehaves.
//
// The split exists so the LLM cannot silently make permanent changes
// to its own capability surface — every persisted tool passes through
// a human review gate where the command_template is fully visible.

package core

import (
	"sort"
	"sync"
	"time"
)

const (
	pendingTempToolsTable    = "pending_temp_tools"
	persistentTempToolsTable = "persistent_temp_tools"
	sessionTempToolsTable    = "session_temp_tools"
)

var tempToolPersistMu sync.Mutex

// tempToolStore returns the canonical DB for temp-tool persistence:
// the process-level RootDB. Temp-tool pools (pending/persistent/
// session-scoped) MUST live in a single shared store so the chat
// app's writes and the admin app's reads land at the same key —
// otherwise chat saves into its bucketed sub-DB and admin's root-DB
// queries find nothing. Falls back to the caller-supplied db when
// RootDB is unset (rare, e.g. early-init paths) so the call shape
// stays compatible. Always RootDB once the dashboard is running.
func tempToolStore(fallback Database) Database {
	if RootDB != nil {
		return RootDB
	}
	return fallback
}

// PendingTempTool is a tool the LLM asked to persist that's waiting on
// human approval. RequestedAt is when the LLM made the request;
// RequestedSession is the chat session ID it was created from (so the
// admin reviewer can read context if they want).
type PendingTempTool struct {
	Tool             TempTool  `json:"tool"`
	RequestedAt      time.Time `json:"requested_at"`
	RequestedSession string    `json:"requested_session,omitempty"`
}

// PersistentTempTool is an approved tool that loads into every new
// session for its owning user. ApprovedAt records when the human
// admin approved it; LastUsedAt is updated on each invocation.
type PersistentTempTool struct {
	Tool        TempTool  `json:"tool"`
	ApprovedAt  time.Time `json:"approved_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
}

// LoadPendingTempTools returns the pending-approval queue for a user,
// ordered newest-first by RequestedAt. Newest-first matches reviewer
// intuition: when checking the queue, the just-requested tool is what
// the user most recently asked for and is the freshest in their head.
// Empty username returns nil (anonymous sessions can't queue tools).
func LoadPendingTempTools(db Database, username string) []PendingTempTool {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return nil
	}
	var out []PendingTempTool
	if !db.Get(pendingTempToolsTable, username, &out) {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].RequestedAt.After(out[j].RequestedAt)
	})
	return out
}

// LoadPersistentTempTools returns the approved persistent tool pool
// for a user.
func LoadPersistentTempTools(db Database, username string) []PersistentTempTool {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return nil
	}
	var out []PersistentTempTool
	if !db.Get(persistentTempToolsTable, username, &out) {
		return nil
	}
	return out
}

// QueuePendingTempTool adds a tool to the approval queue. Returns an
// error if a same-named tool is already pending or approved (avoid
// silent overwrites — the user should explicitly delete the old one
// first).
func QueuePendingTempTool(db Database, username string, t TempTool, sessionID string) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("persistence requires an authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	// Persistent-pool conflict is still an error: a tool the admin
	// has already approved should require an explicit delete before
	// the LLM can redefine it. Surprise-replacing approved tools
	// silently is too easy to abuse.
	approved := LoadPersistentTempTools(db, username)
	for _, p := range approved {
		if p.Tool.Name == t.Name {
			return errString("a tool named " + t.Name + " is already persisted; delete it first to redefine")
		}
	}
	// Pending-pool conflict is allowed and replaces in place: if the
	// LLM is iterating on a tool definition pre-approval (typo, bad
	// schema, missing param), the admin should see the LATEST
	// version queued for review — not the original mistake. The new
	// RequestedAt timestamp also bumps the entry to the top of the
	// queue so the operator notices the update.
	pending := LoadPendingTempTools(db, username)
	rest := pending[:0]
	for _, p := range pending {
		if p.Tool.Name != t.Name {
			rest = append(rest, p)
		}
	}
	rest = append(rest, PendingTempTool{
		Tool:             t,
		RequestedAt:      time.Now(),
		RequestedSession: sessionID,
	})
	db.Set(pendingTempToolsTable, username, rest)
	return nil
}

// ApprovePendingTempTool moves a pending tool into the persistent pool.
// Returns an error if the named tool isn't actually pending.
func ApprovePendingTempTool(db Database, username, name string) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	pending := LoadPendingTempTools(db, username)
	var moved *PendingTempTool
	rest := pending[:0]
	for i := range pending {
		if pending[i].Tool.Name == name {
			tmp := pending[i]
			moved = &tmp
			continue
		}
		rest = append(rest, pending[i])
	}
	if moved == nil {
		return errString("no pending tool named " + name)
	}
	approved := LoadPersistentTempTools(db, username)
	approved = append(approved, PersistentTempTool{
		Tool:       moved.Tool,
		ApprovedAt: time.Now(),
	})
	db.Set(pendingTempToolsTable, username, rest)
	db.Set(persistentTempToolsTable, username, approved)
	return nil
}

// RejectPendingTempTool removes a pending tool without persisting it.
// The current session may still use it (it's already in sess.TempTools)
// but no future session sees it.
func RejectPendingTempTool(db Database, username, name string) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	pending := LoadPendingTempTools(db, username)
	rest := pending[:0]
	found := false
	for i := range pending {
		if pending[i].Tool.Name == name {
			found = true
			continue
		}
		rest = append(rest, pending[i])
	}
	if !found {
		return errString("no pending tool named " + name)
	}
	db.Set(pendingTempToolsTable, username, rest)
	return nil
}

// DeletePersistentTempTool removes an approved tool from the user's
// persistent pool. Used by the admin UI's "break-glass" delete and by
// delete_temp_tool when the LLM removes a name that happens to be
// persisted. If the deleted tool had a packed archive on disk, the
// archive is removed too (state dir is preserved — operator can
// inspect or manually clean if desired).
func DeletePersistentTempTool(db Database, username, name string) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	rest := approved[:0]
	for i := range approved {
		if approved[i].Tool.Name == name {
			continue
		}
		rest = append(rest, approved[i])
	}
	if len(rest) == len(approved) {
		return errString("no persistent tool named " + name)
	}
	db.Set(persistentTempToolsTable, username, rest)
	// Recipe content lives inline on the record, so no on-disk
	// cleanup is needed. State dir (if any) is left in place — the
	// operator can purge it explicitly via DeleteToolState.
	return nil
}

// TouchPersistentTempTool updates LastUsedAt for the named tool in the
// user's pool. Best-effort — silent no-op if the tool isn't found.
// Used for telemetry in the admin UI ("last used: 3h ago").
func TouchPersistentTempTool(db Database, username, name string) {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	changed := false
	for i := range approved {
		if approved[i].Tool.Name == name {
			approved[i].LastUsedAt = time.Now()
			changed = true
			break
		}
	}
	if changed {
		db.Set(persistentTempToolsTable, username, approved)
	}
}

// errString is a tiny string-error type used to keep this file free of
// fmt imports for one-off messages.
type errString string

func (e errString) Error() string { return string(e) }

// --- session-scoped temp tools ---
//
// Session-scoped tools sit between in-memory ToolSession.tempTools
// (lost when the HTTP request ends) and persistentTempTools (admin-
// approved, lifetime survives session boundaries). They live keyed
// by ChatSessionID so a tool the LLM creates with persist=false in
// message 1 of a chat is reloaded when message 2 arrives. The chat
// session deletion path is responsible for clearing them.

// LoadSessionTempTools returns the tools the LLM has registered in
// this chat session via persist=false creates. Empty chatSessionID
// returns nil — anonymous sessions can't have session-scoped tools
// because there's no key to load them by.
func LoadSessionTempTools(db Database, chatSessionID string) []TempTool {
	db = tempToolStore(db)
	if db == nil || chatSessionID == "" {
		return nil
	}
	var out []TempTool
	if !db.Get(sessionTempToolsTable, chatSessionID, &out) {
		return nil
	}
	return out
}

// SaveSessionTempTool upserts a session-scoped temp tool by name.
// Existing entries with the same name are replaced so re-creating a
// tool (e.g. the LLM iterating on the schema) doesn't accumulate
// duplicates. Silent no-op when chatSessionID is empty.
func SaveSessionTempTool(db Database, chatSessionID string, t TempTool) error {
	db = tempToolStore(db)
	if db == nil || chatSessionID == "" {
		return nil
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	existing := LoadSessionTempTools(db, chatSessionID)
	rest := existing[:0]
	for i := range existing {
		if existing[i].Name != t.Name {
			rest = append(rest, existing[i])
		}
	}
	rest = append(rest, t)
	db.Set(sessionTempToolsTable, chatSessionID, rest)
	return nil
}

// RemoveSessionTempTool drops a tool by name from the session pool.
// Returns true when a tool was removed, false when the name wasn't
// found.
func RemoveSessionTempTool(db Database, chatSessionID, name string) bool {
	db = tempToolStore(db)
	if db == nil || chatSessionID == "" || name == "" {
		return false
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	existing := LoadSessionTempTools(db, chatSessionID)
	rest := existing[:0]
	removed := false
	for i := range existing {
		if existing[i].Name == name {
			removed = true
			continue
		}
		rest = append(rest, existing[i])
	}
	if removed {
		if len(rest) == 0 {
			db.Unset(sessionTempToolsTable, chatSessionID)
		} else {
			db.Set(sessionTempToolsTable, chatSessionID, rest)
		}
	}
	return removed
}

// DeleteSessionTempTools wipes every session-scoped tool for a chat
// session. Called when the chat session itself is deleted so we
// don't leak tool definitions for sessions that no longer exist.
func DeleteSessionTempTools(db Database, chatSessionID string) {
	db = tempToolStore(db)
	if db == nil || chatSessionID == "" {
		return
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	db.Unset(sessionTempToolsTable, chatSessionID)
}
