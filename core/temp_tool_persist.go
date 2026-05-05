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
	"sync"
	"time"
)

const (
	pendingTempToolsTable    = "pending_temp_tools"
	persistentTempToolsTable = "persistent_temp_tools"
)

var tempToolPersistMu sync.Mutex

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

// LoadPendingTempTools returns the pending-approval queue for a user.
// Empty username returns nil (anonymous sessions can't queue tools).
func LoadPendingTempTools(db Database, username string) []PendingTempTool {
	if db == nil || username == "" {
		return nil
	}
	var out []PendingTempTool
	if !db.Get(pendingTempToolsTable, username, &out) {
		return nil
	}
	return out
}

// LoadPersistentTempTools returns the approved persistent tool pool
// for a user.
func LoadPersistentTempTools(db Database, username string) []PersistentTempTool {
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
	if db == nil || username == "" {
		return errString("persistence requires an authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	pending := LoadPendingTempTools(db, username)
	for _, p := range pending {
		if p.Tool.Name == t.Name {
			return errString("a tool named " + t.Name + " is already pending approval")
		}
	}
	approved := LoadPersistentTempTools(db, username)
	for _, p := range approved {
		if p.Tool.Name == t.Name {
			return errString("a tool named " + t.Name + " is already persisted; delete it first to redefine")
		}
	}
	pending = append(pending, PendingTempTool{
		Tool:             t,
		RequestedAt:      time.Now(),
		RequestedSession: sessionID,
	})
	db.Set(pendingTempToolsTable, username, pending)
	return nil
}

// ApprovePendingTempTool moves a pending tool into the persistent pool.
// Returns an error if the named tool isn't actually pending.
func ApprovePendingTempTool(db Database, username, name string) error {
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
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	rest := approved[:0]
	var hadArchive bool
	for i := range approved {
		if approved[i].Tool.Name == name {
			hadArchive = approved[i].Tool.ArchivePath != ""
			continue
		}
		rest = append(rest, approved[i])
	}
	if len(rest) == len(approved) {
		return errString("no persistent tool named " + name)
	}
	db.Set(persistentTempToolsTable, username, rest)
	if hadArchive {
		if err := DeleteToolArchive(username, name); err != nil {
			Log("[temptool] archive cleanup failed for %s/%s: %v", username, name, err)
		}
	}
	return nil
}

// TouchPersistentTempTool updates LastUsedAt for the named tool in the
// user's pool. Best-effort — silent no-op if the tool isn't found.
// Used for telemetry in the admin UI ("last used: 3h ago").
func TouchPersistentTempTool(db Database, username, name string) {
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
