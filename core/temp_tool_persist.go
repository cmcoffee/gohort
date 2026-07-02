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

// OnTempToolApproved, when set, fires after a tool transitions into
// the user's persistent (active) pool — either through admin's
// ApprovePendingTempTool gate OR through AdminPersistTempTool (direct
// promotion from a session draft). The callback receives the same DB
// the approval ran against, the username, and the approved tool's
// name. Set this from a higher-level app (e.g. orchestrate) to react
// to approvals — surface the tool on default agents' allowlists,
// emit a SSE notification, kick a cache invalidation, etc.
//
// One subscriber slot (last-writer-wins). Core stays decoupled from
// orchestrate-specific concerns (agent records, AllowedTools) while
// still giving orchestrate an immediate-after-write hook.
//
// Fires AFTER the persistent pool write commits, so the callback can
// read the new state via LoadPersistentTempTools and see it.
var OnTempToolApproved func(db Database, username, toolName string)

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
// admin approved it; LastUsedAt is updated on each invocation. When
// Shared is set, the tool is published to the DEPLOYMENT-WIDE shared
// pool: it loads for every user's turn (in addition to their own
// pool), subject to the same per-agent gating. See
// LoadSharedPersistentTempTools.
type PersistentTempTool struct {
	Tool        TempTool  `json:"tool"`
	ApprovedAt  time.Time `json:"approved_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
	Shared      bool      `json:"shared,omitempty"`
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

// LoadSharedPersistentTempTools returns the deployment-wide shared pool:
// every persistent tool any user has marked Shared, deduped by tool name
// (first owner seen wins). These load for ALL users' turns, so an admin can
// publish a tool once and have it available everywhere — without copying it
// into each user's pool. Walks every user's persistent pool; cheap at the
// scale we expect (the admin persistent-tools page already does this walk).
func LoadSharedPersistentTempTools(db Database) []PersistentTempTool {
	db = tempToolStore(db)
	if db == nil {
		return nil
	}
	var out []PersistentTempTool
	seen := map[string]bool{}
	for _, u := range db.Keys(persistentTempToolsTable) {
		for _, p := range LoadPersistentTempTools(db, u) {
			if p.Shared && !seen[p.Tool.Name] {
				seen[p.Tool.Name] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// SetPersistentTempToolShared flips the deployment-wide Shared flag on a tool
// in a user's persistent pool. Returns an error when the named tool isn't in
// that user's pool. Admin-driven (from the persistent-tools page).
func SetPersistentTempToolShared(db Database, username, name string, shared bool) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	found := false
	for i := range approved {
		if approved[i].Tool.Name == name {
			approved[i].Shared = shared
			found = true
			break
		}
	}
	if !found {
		return errString("no persistent tool named " + name)
	}
	db.Set(persistentTempToolsTable, username, approved)
	return nil
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

// AdminPersistTempTool writes a TempTool directly into the per-user
// persistent pool, skipping the pending-approval queue. Used by the
// admin-driven "promote a session draft to user-wide" surface in the
// chat Tools modal — the admin is already authorized to approve, so
// the queue step would be pure ceremony. Replaces any existing entry
// with the same name.
func AdminPersistTempTool(db Database, username string, t TempTool) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	rest := approved[:0]
	for i := range approved {
		if approved[i].Tool.Name != t.Name {
			rest = append(rest, approved[i])
		}
	}
	rest = append(rest, PersistentTempTool{
		Tool:       t,
		ApprovedAt: time.Now(),
	})
	db.Set(persistentTempToolsTable, username, rest)
	// Dedupe against the pending queue — the tool was likely also
	// auto-queued by tool_def(create) when first authored. Without
	// this, the same name shows in BOTH the pending and active lists
	// in the admin UI (the user sees a "duplicate") until something
	// else dequeues. Inline here so every direct-persist call
	// preserves the "exactly one of pending/active" invariant.
	pending := LoadPendingTempTools(db, username)
	prest := pending[:0]
	dequeued := false
	for i := range pending {
		if pending[i].Tool.Name == t.Name {
			dequeued = true
			continue
		}
		prest = append(prest, pending[i])
	}
	if dequeued {
		db.Set(pendingTempToolsTable, username, prest)
	}
	// Eager session-draft cleanup — same rationale as in
	// ApprovePendingTempTool: prevent the new persistent entry from
	// being shadowed by any stale draft of the same name in any of
	// the user's chat sessions.
	if n := cleanupSessionDraftsByName(db, t.Name); n > 0 {
		Debug("[temp_tool_persist] persist %q: cleaned %d stale session draft(s)", t.Name, n)
	}
	if OnTempToolApproved != nil {
		OnTempToolApproved(db, username, t.Name)
	}
	return nil
}

// ApprovePendingTempTool moves a pending tool into the persistent pool.
// Returns an error if the named tool isn't actually pending. Also
// cleans up the originating session draft when the pending record
// carried a RequestedSession — otherwise the session would keep a
// stale shadow copy of the now-persistent tool (runtime would silently
// prefer the persistent one, but the storage carries a duplicate that
// confuses the Memory / Session-tools UI).
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
	// Replace-by-name when adding to active so re-approves of an
	// already-approved tool don't append a duplicate. Tools are
	// keyed by name across the whole pool; a second copy with the
	// same name and a fresh ApprovedAt is the natural "updated"
	// shape from an LLM iterating on its own design.
	approved := LoadPersistentTempTools(db, username)
	deduped := approved[:0]
	for i := range approved {
		if approved[i].Tool.Name != name {
			deduped = append(deduped, approved[i])
		}
	}
	deduped = append(deduped, PersistentTempTool{
		Tool:       moved.Tool,
		ApprovedAt: time.Now(),
	})
	db.Set(pendingTempToolsTable, username, rest)
	db.Set(persistentTempToolsTable, username, deduped)
	// Clean up the originating session draft. The mutex is already
	// held; RemoveSessionTempTool takes no lock of its own so this
	// is safe. Quiet on miss — drafts may have been pruned before
	// approval (session deleted, draft manually dropped).
	if moved.RequestedSession != "" {
		RemoveSessionTempTool(db, moved.RequestedSession, name)
	}
	// AND scan ALL the user's chat sessions for stale drafts with the
	// same name — the originating-session cleanup above misses cases
	// where the LLM re-authored the same tool in a different session
	// (or where chat itself wrote a draft via add_tool while Builder
	// also queued one). The lazy filter at handleSessionToolsList
	// catches these on next modal open, but eager cleanup here makes
	// the "exactly one of session/persistent" invariant immediate.
	if n := cleanupSessionDraftsByName(db, name); n > 0 {
		Debug("[temp_tool_persist] approve %q: cleaned %d stale session draft(s)", name, n)
	}
	if OnTempToolApproved != nil {
		OnTempToolApproved(db, username, name)
	}
	return nil
}

// DequeuePendingTempTool removes a name from the pending queue if
// present. Quiet — no error when the name isn't queued. Used by
// auto-dequeue paths (add_tool attaches a tool to an agent;
// create_agent's auto-copy claims a session tool; tool_def(action=
// delete) drops a tool the LLM is discarding) where we don't want
// failure to surface as an error to the caller.
func DequeuePendingTempTool(db Database, username, name string) {
	db = tempToolStore(db)
	if db == nil || username == "" || name == "" {
		return
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	pending := LoadPendingTempTools(db, username)
	rest := pending[:0]
	for i := range pending {
		if pending[i].Tool.Name == name {
			continue
		}
		rest = append(rest, pending[i])
	}
	if len(rest) == len(pending) {
		return // not found; nothing to write
	}
	db.Set(pendingTempToolsTable, username, rest)
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
	//
	// Strip the deleted name from every admin tool group's Members
	// list so a grouped temp tool doesn't leave a dangling member
	// reference after its underlying tool goes away. Cheap (one DB
	// read per group; cache stays consistent via the rewrite).
	cleanupToolGroupMemberRefs(name)
	return nil
}

// cleanupToolGroupMemberRefs scans the deployment-wide tool groups
// and removes the given tool name from any Members list that
// references it. Called when a tool is deleted from the registry-
// adjacent surfaces (persistent temp tools today) so the orphan
// reference doesn't sit in the group definition forever. Silent
// no-op when AuthDB isn't wired or no group references the name.
func cleanupToolGroupMemberRefs(toolName string) {
	if AuthDB == nil || toolName == "" {
		return
	}
	db := AuthDB()
	if db == nil {
		return
	}
	groups := LoadToolGroups(db)
	for _, g := range groups {
		kept := g.Members[:0]
		removed := false
		for _, m := range g.Members {
			if m == toolName {
				removed = true
				continue
			}
			kept = append(kept, m)
		}
		if !removed {
			continue
		}
		g.Members = kept
		if _, err := SaveToolGroup(db, g); err != nil {
			Debug("[tool_groups] failed to drop orphan %q from group %q: %v", toolName, g.Name, err)
		}
	}
}

// cleanupSessionDraftsByName walks every session_temp_tools entry in
// the given DB and removes any draft whose name matches toolName. Used
// by the approve / persist paths so a tool that's been promoted to
// the user-wide persistent pool doesn't linger as a stale duplicate
// in any chat session's "Session tools" view. The list-time filter
// in handleSessionToolsList already deduplicates lazily, but it only
// runs when the modal is opened — eager cleanup here guarantees the
// invariant immediately, including for any session modal currently
// open showing cached data. db is expected to be the user's per-user
// DB (different users live in different DBs); cleanup is naturally
// scoped to the user.
//
// Returns the number of session-draft entries cleaned.
func cleanupSessionDraftsByName(db Database, toolName string) int {
	db = tempToolStore(db)
	if db == nil || toolName == "" {
		return 0
	}
	cleaned := 0
	for _, sid := range db.Keys(sessionTempToolsTable) {
		var drafts []TempTool
		if !db.Get(sessionTempToolsTable, sid, &drafts) {
			continue
		}
		var rest []TempTool
		removed := false
		for _, t := range drafts {
			if t.Name == toolName {
				removed = true
				continue
			}
			rest = append(rest, t)
		}
		if !removed {
			continue
		}
		if len(rest) == 0 {
			db.Unset(sessionTempToolsTable, sid)
		} else {
			db.Set(sessionTempToolsTable, sid, rest)
		}
		cleaned++
	}
	return cleaned
}

// UpdatePersistentTempTool replaces an existing active tool's content
// in place. Used by the LLM-iteration path: when an LLM re-authors a
// tool whose name is already in the persistent pool (the original was
// admin-approved at some point), the new version overwrites the
// active entry directly — admin doesn't need to re-approve every
// iteration of an already-blessed tool. Preserves the original
// ApprovedAt so the audit trail shows "first approved at X, last
// updated at Y."
//
// Returns true when a replacement happened, false when no tool by
// that name was in the persistent pool (caller should fall through
// to the queue-for-review path in that case).
func UpdatePersistentTempTool(db Database, username string, t TempTool) bool {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return false
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	updated := false
	for i := range approved {
		if approved[i].Tool.Name == t.Name {
			approved[i].Tool = t // content + metadata replaced; ApprovedAt preserved
			updated = true
			break
		}
	}
	if !updated {
		return false
	}
	db.Set(persistentTempToolsTable, username, approved)
	Log("[temp_tool_persist] in-place update of active tool %q (LLM iteration; original approval preserved)", t.Name)
	return true
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
