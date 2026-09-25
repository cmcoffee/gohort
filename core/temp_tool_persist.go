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
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/gohort/core/textutil"
)

const (
	pendingTempToolsTable    = "pending_temp_tools"
	persistentTempToolsTable = "persistent_temp_tools"
	// askInChatToolsTable holds the ask-before-every-call marks, keyed by
	// USER, as a set of tool names.
	//
	// Separate from the tool record because the flag used to live ON one, and
	// so could only be set for a tool somebody had authored. That is backwards:
	// the framework's own tools are the consequential ones - the searches, the
	// browsing, the fetches - and they carry no record, so the tools most worth
	// stopping on were the only ones that could not be.
	//
	// Keyed by (owner, AGENT) so the marks belong to an agent, like every
	// other permission in this codebase: the unattended policy, the workspace
	// reach, the dispatch policy. An empty agent is the fleet-wide form, the
	// same widening contacts and delegation already use.
	//
	// It was per user alone, on the reasoning that a tool's riskiness belongs
	// to the tool. That is wrong here. web_search wanting supervision on an
	// agent somebody else runs, and not on your own, is an ordinary thing to
	// want, and a permission belongs to the principal holding it.
	askInChatToolsTable   = "ask_in_chat_tools"
	sessionTempToolsTable = "session_temp_tools"
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

// ToolScopeAgent is one agent's relationship to a tool, for the pill UI.
// On means the tool is currently available to that agent (for a global
// tool: not disabled/denied; for an agent-scoped tool: it owns a copy).
type ToolScopeAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	On   bool   `json:"on"`
	// ParentID is set for a SUB-AGENT and names the agent that owns it, so a
	// picker can show the relationship instead of a flat list in which a
	// specialist and its parent look like peers.
	//
	// Sub-agents are scope targets in their own right: they run their own turns
	// with their own Tools, and inheriting the parent's kit is opt-in
	// (AgentRecord.InheritParentTools). Excluding them from the picker made a
	// sub-agent's tools unreachable from any UI — the only way to give one a
	// tool was to have the assistant author it there.
	ParentID string `json:"parent_id,omitempty"`
	// Partial marks a target that only SOME members of a group hold. It is set
	// only by a grouped provider (a category, whose state is the union of the
	// tools claiming it); a single tool is either on a target or it is not.
	// The pill renders as an indeterminate third state rather than lying in
	// either direction, and clicking it sets every member the same way.
	Partial bool `json:"partial,omitempty"`
}

// ToolScopeState is the full scope picture for one tool, driving the pill
// control on both the admin page and the in-chat Tools modal. Global marks
// whether the tool lives in the user-wide pool; Agents lists every one of
// the owner's agents with its on/off state; Missing carries any broken
// dependency descriptors (e.g. "credential:stripe") for a badge.
type ToolScopeState struct {
	Name    string           `json:"name"`
	Global  bool             `json:"global"`
	Missing []string         `json:"missing,omitempty"`
	Agents  []ToolScopeAgent `json:"agents"`
	// GlobalPartial is the Global pill's version of ToolScopeAgent.Partial:
	// some members of a group are user-wide and others are agent-scoped.
	GlobalPartial bool `json:"global_partial,omitempty"`
	// Custom reports that the members of a group do not all share one scope,
	// so there is no single answer to "what access does this group have". Set
	// only by a grouped provider; a caller renders it as the group's access
	// rather than picking one member's answer and presenting it as the whole.
	//
	// Derived on every read rather than stored. A stored flag would be one more
	// thing to keep true — every path that changes ONE tool's scope would have
	// to remember to mark its category custom, and the first one that forgot
	// would leave the group claiming an access it no longer has.
	Custom bool `json:"custom,omitempty"`
}

// AdminToolScopeState, when set, returns the current scope picture for a
// tool (owner-scoped). Wired by orchestrate (owns agent records). Second
// return is false when the tool can't be found in any scope.
var AdminToolScopeState func(db Database, owner, toolName string) (ToolScopeState, bool)

// TrialToolTTL is how long an UNCONFIRMED authored tool survives before the
// reaper drops it. Generous on purpose: the cost of reaping too early is
// destroying work someone meant to keep, while the cost of reaping too late is
// a stale row in a list. Set to 0 to disable reaping entirely.
var TrialToolTTL = 14 * 24 * time.Hour

// ReapTrialTools, when set, drops a user's unconfirmed authored tools whose TTL
// has elapsed and returns how many went. Wired by the app that owns agent
// records; nil means no reaping (nothing to walk).
//
// This is what makes ephemerality an ATTRIBUTE rather than a storage scope: the
// session-pool design got automatic cleanup for free by tying tools to a
// conversation's lifetime, at the cost of a scope nobody could see or reason
// about. Keeping the cleanup as an explicit, logged sweep is the trade.
var ReapTrialTools func(db Database, owner string) int

// ConfirmAgentTool, when set, clears the Trial flag on a tool attached to an
// agent — the user vouching for something the assistant authored. Wired by the
// app that owns agent records.
var ConfirmAgentTool func(db Database, owner, agentID, toolName string) error

// AttachToolToAgent, when set, commits a tool onto an agent's record (replace
// by name). Wired by the app that owns agent records; nil where there are none.
//
// This is what lets an authored tool land on the agent that asked for it
// instead of in a per-chat-session pool. A tool on its own agent's record is
// callable by that agent immediately, survives the conversation, and is reached
// by the normal access controls — none of which was true of a session draft.
var AttachToolToAgent func(db Database, owner, agentID string, t TempTool) error

// ListUserAgentTools returns every tool bundled to any of the owner's agents'
// own records (AgentRecord.Tools), across all agents. It's what makes the tool
// namespace UNIQUE PER USER: the create-time collision guard checks a proposed
// name against these too, so one agent can't author a name another agent
// already holds — which is the invariant that makes "the tool Builder edits is
// the tool the agent runs" true by construction. Nil ⇒ no agent enumeration
// wired, and the guard falls back to session + pool scope only.
var ListUserAgentTools func(db Database, owner string) []TempTool

// FindUserAgentTool resolves a tool by name across ALL of the owner's agents'
// own records (AgentRecord.Tools), returning the tool, the id of the agent that
// holds it, and whether it was found. It's the read half of in-place editing:
// Builder's own resolver only sees the user-wide pool and its own session
// tools, so a tool bundled to another of the user's agents is invisible to it
// without this. Returns the FIRST match; a name carried by more than one agent
// is ambiguous and the caller decides how to disambiguate. Nil ⇒ no agent-tool
// lookup wired (host without orchestrate), so bundled tools stay unreachable.
var FindUserAgentTool func(db Database, owner, name string) (TempTool, string, bool)

// DetachToolFromAgent removes a tool from an agent's record by name — the
// delete half of in-place editing. Update resolves a tool bundled to another
// of the user's agents via FindUserAgentTool and writes back with
// AttachToolToAgent; without this seam, delete had no symmetric path: it
// removed only the session copy ("Removed temp tool from this session") while
// the durable copy on the agent record survived and reloaded next turn — or
// reported "no temp tool named X" outright. Wired by the app that owns agent
// records; nil ⇒ cross-agent delete unavailable.
var DetachToolFromAgent func(db Database, owner, agentID, toolName string) error

// AdminSetToolScope, when set, applies ONE pill toggle. target is either
// "global" (the Global pill) or an agent id; on is the desired state.
// The orchestrate impl interprets the transition against current state:
//
//	target=global, on=true  → promote agent-scoped → user-wide pool
//	target=global, on=false → demote: descope to the currently-ON agents
//	target=<agent>, on=true  → enable (global: un-deny / allow; scoped: add copy)
//	target=<agent>, on=false → disable (global: deny/de-allow; scoped: drop copy)
var AdminSetToolScope func(db Database, owner, toolName, target string, on bool) error

// ToolVerifyRecorder, when set, records whether an authored tool currently
// stands VERIFIED for the calling chat session. Set by the app that owns the
// authoring plan (orchestrate); called by every surface that can change a
// tool's verification standing, wherever that outcome is actually known:
//
//   - a verification that passed        → passed=true
//   - one that failed                   → passed=false, reason = why
//   - a create/update                   → passed=false, reason = "edited"
//     (a write invalidates any earlier pass — the tool is not the tool that
//     was tested)
//
// It exists because verification outcomes were pure prose in a tool result:
// they scrolled by and nothing downstream could see them. The build-plan gate
// therefore graded on self-reported step completion, and a model that marked
// its own step done after a FAILED verify was told "All steps completed
// successfully — no gaps to report", then reported success to the user.
//
// nil is a no-op, so tools work normally outside an authoring session.
var ToolVerifyRecorder func(sess *ToolSession, toolName string, passed bool, reason string)

// RecordToolVerification is the nil-safe way to call ToolVerifyRecorder.
func RecordToolVerification(sess *ToolSession, toolName string, passed bool, reason string) {
	if ToolVerifyRecorder == nil || sess == nil || strings.TrimSpace(toolName) == "" {
		return
	}
	ToolVerifyRecorder(sess, toolName, passed, reason)
}

// AdminRehomeOrphanTool, when set, re-homes an orphaned tool and removes it
// from the orphan store. target is "global" (into the user-wide pool) or an
// agent id (bundled onto that agent). Wired by orchestrate.
var AdminRehomeOrphanTool func(db Database, owner, toolName, target string) error

// ScopeProvider is one kind's scope backend (tool, pipeline, credential).
// State returns the current pill picture for a named item (false when the
// item isn't found in any scope); Set applies ONE pill toggle — target is
// "global" (the primary pill) or an agent id, on is the desired state. The
// ToolScopeState shape is shared across kinds: Global marks the "all agents"
// scope, Agents lists each agent's on/off, Missing carries dependency
// badges. This is the generalization of the tool-only AdminToolScopeState /
// AdminSetToolScope vars so the same pill UI + HTTP handlers drive pipelines
// and credentials too — the app registers one provider per kind.
type ScopeProvider struct {
	State func(db Database, owner, name string) (ToolScopeState, bool)
	Set   func(db Database, owner, name, target string, on bool) error
}

var scopeProviders = map[string]ScopeProvider{}

// RegisterScopeProvider registers the scope backend for a kind ("tool",
// "pipeline", "credential"). Called from the owning app's init(). Last
// registration wins, so a kind can be overridden in tests.
func RegisterScopeProvider(kind string, p ScopeProvider) {
	scopeProviders[kind] = p
}

// ScopeProviderFor returns the provider registered for kind, or false when
// none is wired. The HTTP handlers use this to dispatch by ?kind=.
func ScopeProviderFor(kind string) (ScopeProvider, bool) {
	p, ok := scopeProviders[kind]
	return p, ok
}

const orphanedTempToolsTable = "orphaned_temp_tools"

// OrphanedTempTool is a formerly agent-scoped tool whose owning agent was
// deleted. Captured at delete time (the tool lived inside AgentRecord.Tools,
// so it would otherwise vanish with the record) so the admin can re-home or
// discard it deliberately.
type OrphanedTempTool struct {
	Tool            TempTool  `json:"tool"`
	FormerAgentID   string    `json:"former_agent_id"`
	FormerAgentName string    `json:"former_agent_name"`
	OrphanedAt      time.Time `json:"orphaned_at"`
}

// LoadOrphanedTempTools returns the orphan pool for a user, newest-first.
func LoadOrphanedTempTools(db Database, username string) []OrphanedTempTool {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return nil
	}
	var out []OrphanedTempTool
	if !db.Get(orphanedTempToolsTable, username, &out) {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OrphanedAt.After(out[j].OrphanedAt) })
	return out
}

// AddOrphanedTempTools appends orphans to a user's pool (replace-by-name so
// re-deleting an agent that re-acquired a name doesn't duplicate).
func AddOrphanedTempTools(db Database, username string, orphans []OrphanedTempTool) {
	db = tempToolStore(db)
	if db == nil || username == "" || len(orphans) == 0 {
		return
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	existing := LoadOrphanedTempTools(db, username)
	byName := make(map[string]bool, len(orphans))
	for _, o := range orphans {
		byName[o.Tool.Name] = true
	}
	kept := existing[:0]
	for _, e := range existing {
		if !byName[e.Tool.Name] {
			kept = append(kept, e)
		}
	}
	kept = append(kept, orphans...)
	db.Set(orphanedTempToolsTable, username, kept)
}

// RemoveOrphanedTempTool drops one orphan by name. Returns true if removed.
func RemoveOrphanedTempTool(db Database, username, name string) bool {
	db = tempToolStore(db)
	if db == nil || username == "" || name == "" {
		return false
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	return removeOrphanedTempToolLocked(db, username, name)
}

// removeOrphanedTempToolLocked is RemoveOrphanedTempTool's body for callers
// that already hold tempToolPersistMu (the mutex is not reentrant). db must
// already be resolved through tempToolStore.
func removeOrphanedTempToolLocked(db Database, username, name string) bool {
	existing := LoadOrphanedTempTools(db, username)
	kept := existing[:0]
	removed := false
	for _, e := range existing {
		if e.Tool.Name == name {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if removed {
		db.Set(orphanedTempToolsTable, username, kept)
	}
	return removed
}

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
	// ScopeAgents carries the agent scope the tool should have ONCE approved
	// (see PersistentTempTool.ScopeAgents). Set when an imported agent recipe
	// brings its tools with it: they wait here for review, and approval lands
	// them in that agent's kit rather than widening them to every agent.
	ScopeAgents []string `json:"scope_agents,omitempty"`
}

// PersistentTempTool is an approved tool that loads into every new
// session for its owning user. ApprovedAt records when the human
// admin approved it; LastUsedAt is updated on each invocation. When
// Shared is set, the tool is published to the DEPLOYMENT-WIDE catalog,
// where other users may adopt it. What they run is its ToolRelease, the
// version an administrator approved, not this record: this record is the
// owner's working copy. See LoadSharedPersistentTempTools.
type PersistentTempTool struct {
	// ID names this tool for as long as it exists, through every edit: minted
	// when the tool is created, never reused.
	// A release and an adoption record it, so a tool deleted and recreated
	// under the same name is a different tool to both, and an adopter is not
	// quietly reattached to code they never chose.
	ID         string    `json:"id,omitempty"`
	Tool       TempTool  `json:"tool"`
	ApprovedAt time.Time `json:"approved_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	Shared     bool      `json:"shared,omitempty"`
	// AllowedUsers gates WHO may adopt this tool from the global catalog, and is
	// meaningful only when Shared is set. Empty = open to every user (the catalog
	// offers it to all). Non-empty = only those usernames see it in the catalog
	// and may adopt it. A user's own (unshared) pool is always fully theirs, so
	// this is ignored when Shared is false. Mirrors SecureCredential.AllowedUsers
	// — one ACL concept across creds and tools. See docs/sharing-governance.md.
	AllowedUsers []string `json:"allowed_users,omitempty"`
	// SharedWith is the OWNER's own rung: colleagues who may adopt this tool
	// without it ever reaching the deployment catalog. Distinct from
	// AllowedUsers above, which narrows who may adopt an ALREADY-published
	// one — see SetPersistentTempToolSharedWith for why the two stay apart.
	SharedWith []string `json:"shared_with,omitempty"`
	// ScopeAgents restricts the tool to the listed agent IDs (the FLATTENED
	// namespace: one record per (user, name), scope as data). Empty/nil = the
	// legacy pool semantics — visible to ALL the user's agents, subject to the
	// per-agent allow/deny lists. Non-empty = the tool is part of ONLY those
	// agents' kits: the replacement for the old AgentRecord.Tools embedded
	// copies, which let the same name live in two homes and fork (the Moltbook
	// "Builder fixed it but the agent ran the other copy" failure). Gob/JSON
	// compatible: records written before this field decode as nil ⇒ shared.
	ScopeAgents []string `json:"scope_agents,omitempty"`
}

// ScopedToAgent reports whether the record is agent-scoped AND includes the
// given agent. Shared records (empty ScopeAgents) return false — visibility
// of shared tools is decided by the per-agent allow/deny lists, not here.
func (p PersistentTempTool) ScopedToAgent(agentID string) bool {
	for _, id := range p.ScopeAgents {
		if id == agentID {
			return true
		}
	}
	return false
}

// SharedUserTools returns only the records with pool semantics — empty
// ScopeAgents, visible to all the user's agents (subject to per-agent
// allow/deny lists). Consumers that used LoadPersistentTempTools to mean
// "the user-wide pool" switch to this under the flattened namespace, so an
// agent-scoped record never leaks into another agent's catalog.
func SharedUserTools(db Database, username string) []PersistentTempTool {
	var out []PersistentTempTool
	for _, p := range LoadPersistentTempTools(db, username) {
		if len(p.ScopeAgents) == 0 {
			out = append(out, p)
		}
	}
	return out
}

// AgentScopedTools returns the user's tools whose ScopeAgents lists the given
// agent — the agent's own kit under the flattened namespace (what
// AgentRecord.Tools used to hold as embedded copies).
func AgentScopedTools(db Database, username, agentID string) []PersistentTempTool {
	if agentID == "" {
		return nil
	}
	var out []PersistentTempTool
	for _, p := range LoadPersistentTempTools(db, username) {
		if p.ScopedToAgent(agentID) {
			out = append(out, p)
		}
	}
	return out
}

// UserToolByName resolves one tool in the user's unified store by name,
// regardless of scope.
func UserToolByName(db Database, username, name string) (PersistentTempTool, bool) {
	for _, p := range LoadPersistentTempTools(db, username) {
		if p.Tool.Name == name {
			return p, true
		}
	}
	return PersistentTempTool{}, false
}

// SetUserToolScopeAgents replaces a tool's ScopeAgents list (nil = shared
// with all agents). Returns false when no tool of that name exists.
func SetUserToolScopeAgents(db Database, username, name string, agents []string) bool {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return false
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	list := LoadPersistentTempTools(db, username)
	for i := range list {
		if list[i].Tool.Name == name {
			list[i].ScopeAgents = agents
			db.Set(persistentTempToolsTable, username, list)
			return true
		}
	}
	return false
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

// LoadSharedPersistentTempTools returns the deployment-wide catalog: one
// entry per published tool, carrying the RELEASE an administrator approved
// (not the owner's working copy, which may have moved on since), with the
// owner's adopt list and disable switch. Sorted by name.
func LoadSharedPersistentTempTools(db Database) []PersistentTempTool {
	db = tempToolStore(db)
	var out []PersistentTempTool
	for _, p := range publishedTools(db) {
		out = append(out, p.PersistentTempTool)
	}
	return out
}

// SetPersistentTempToolShared publishes (shared=true) or withdraws a tool in a
// user's persistent pool. Returns an error when the named tool isn't in that
// user's pool. Admin-driven (the persistent-tools page), and the owner's own
// withdraw.
//
// Publishing freezes the tool AS IT IS NOW into its release, version 1: what
// adopters run until an administrator approves an update. Publishing a tool
// that is already published changes nothing, so the direct Share button can
// never slip the owner's newer working copy past the update review.
// Withdrawing takes the release out of the catalog and keeps it as a
// tombstone (withdrawnToolRelease).
func SetPersistentTempToolShared(db Database, username, name string, shared bool) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	// Registered before the unlock's defer so it runs AFTER it: the notice
	// reads the store, and the store lock is not reentrant.
	notify := false
	defer func() {
		if notify {
			noteToolWithdrawn(username, name, nil)
		}
	}()
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	idx := -1
	for i := range approved {
		if approved[i].Tool.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errString("no persistent tool named " + name)
	}
	if !shared {
		withdrew := withdrawToolReleaseLocked(db, username, approved[idx])
		approved[idx].Shared = false
		db.Set(persistentTempToolsTable, username, approved)
		// Everybody who took it is told, naming their agents that used it.
		notify = withdrew
		// An update asked for before the withdrawal is moot; left pending,
		// approving it would put the tool straight back in the catalog.
		reqID := PromotionRequestKey("tool", username, name)
		if req, ok := GetPromotionRequest(db, reqID); withdrew && ok && req.State == PromotionPendingState {
			_ = SetPromotionRequestState(db, reqID, PromotionDeniedState, "")
		}
		return nil
	}
	published, err := publishToolReleaseLocked(db, username, &approved[idx], nil, "")
	if err != nil {
		return err
	}
	if !published {
		return nil // already in the catalog; its release stands
	}
	db.Set(persistentTempToolsTable, username, approved)
	db.Unset(toolUpdateRequestsTable, PromotionRequestKey("tool", username, name))
	// Publishing FULFILLS any pending publish request for this tool, however it
	// got published (admin Approve, the direct Share button) — otherwise the
	// request queue and the owner's "Publish requested" badge go stale on a tool
	// that is, in fact, already shared. Un-sharing leaves the request untouched,
	// and so does a no-op publish: a pending request on a published tool is an
	// UPDATE, which only its approval answers.
	reqID := PromotionRequestKey("tool", username, name)
	if req, ok := GetPromotionRequest(db, reqID); ok && req.State == PromotionPendingState {
		_ = SetPromotionRequestState(db, reqID, PromotionApprovedState, req.DecidedBy)
	}
	return nil
}

// SetPersistentTempToolAllowedUsers replaces the adopt-ACL (AllowedUsers) on a
// tool in a user's persistent pool. Empty/nil = open to every user; the list is
// trimmed, de-duped, and sorted for a stable store. Meaningful only when the tool
// is Shared (see CanAdoptGlobalTool); harmless to set otherwise. Returns an error
// when the named tool isn't in that user's pool. Admin-driven (Global Tools page).
func SetPersistentTempToolAllowedUsers(db Database, username, name string, users []string) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	set := map[string]bool{}
	for _, u := range users {
		if u = strings.TrimSpace(u); u != "" {
			set[u] = true
		}
	}
	clean := make([]string, 0, len(set))
	for u := range set {
		clean = append(clean, u)
	}
	sort.Strings(clean)
	notify := false
	defer func() {
		if notify {
			// nil: everybody who took it; the notice skips anyone the new
			// list still admits.
			noteToolWithdrawn(username, name, nil)
		}
	}()
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	found := false
	for i := range approved {
		if approved[i].Tool.Name == name {
			// Narrowing a published tool is taking it away from whoever the
			// new list leaves out.
			notify = approved[i].Shared && len(clean) > 0 && !sameStringSet(approved[i].AllowedUsers, clean)
			approved[i].AllowedUsers = clean
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

// sharedToolAllowedUsers returns the adopt-ACL for a Shared global tool by name:
// the AllowedUsers list on the owner's record, plus whether a published tool of
// that name exists at all. An empty list with found=true means the tool is open
// to everyone. (The deployment publishes one tool per name.)
func sharedToolAllowedUsers(db Database, name string) (allowed []string, found bool) {
	for _, p := range LoadSharedPersistentTempTools(db) {
		if p.Tool.Name == name {
			return p.AllowedUsers, true
		}
	}
	return nil, false
}

// CanAdoptGlobalTool reports whether user is PERMITTED (ACL-wise) to adopt the
// named global tool — it is the catalog-visibility and adopt-guard predicate.
// The rule is an ACL check, not an existence check:
//   - A published Shared tool with an empty AllowedUsers is open to everyone.
//   - A published Shared tool with a non-empty AllowedUsers admits only its
//     members. Admins are NOT auto-allowed — adoption is a per-user fleet choice,
//     so an admin who wants a restricted tool must be named in the list.
//   - A name NOT in the shared pool is permitted (true): pre-adopting an
//     unpublished name is harmless (it simply won't resolve until published), and
//     no ACL exists to deny it. Existence is a separate concern from permission.
//
// Anonymous ("") is never permitted.
func CanAdoptGlobalTool(db Database, user, name string) bool {
	if user == "" {
		return false
	}
	allowed, found := sharedToolAllowedUsers(db, name)
	if !found || len(allowed) == 0 {
		return true
	}
	for _, u := range allowed {
		if u == user {
			return true
		}
	}
	return false
}

const adoptedGlobalToolsTable = "adopted_global_tools"

// toolAdoptionsTable holds what an adoption knows beyond its name and owner
// (toolAdoption: the tool's ID, a colleague's tool's frozen copy), keyed by
// user. adoptedGlobalToolsTable stays the list of WHAT is adopted: every write
// keeps both, and a record whose name has left the list, or whose owner no
// longer matches it, counts for nothing. So anything that reads or writes the
// list alone still sees, and decides, the whole of it.
const toolAdoptionsTable = "tool_adoptions"

// An adoption on the list is stored as "name<TAB>owner": the tool the user
// took, and whose. A bare "name" is an adoption from before the owner was
// recorded.
//
// The owner is the point. Adoption used to be a name and nothing else, and the
// runtime loaded any peer-shared or published tool answering to it, from
// whoever: when a second user published or shared a tool under the same name,
// the adopters' agents started running that user's code, in their own
// sessions, with their own credentials.
const adoptionOwnerSep = "\t"

func splitAdoption(entry string) (name, owner string) {
	name, owner, _ = strings.Cut(entry, adoptionOwnerSep)
	return strings.TrimSpace(name), strings.TrimSpace(owner)
}

// toolAdoption is one tool a user took from a colleague or from the catalog.
type toolAdoption struct {
	Name string `json:"name"`
	// Owner is whose tool it is; "" for an adoption recorded before owners
	// were, pinned the first time exactly one owner offers the name.
	Owner string `json:"owner,omitempty"`
	// ID is the tool's ID (PersistentTempTool.ID) when it was taken, so the
	// owner deleting it and making another under the same name does not hand
	// the adopter the new one. "" until first resolved, for older entries.
	ID string `json:"id,omitempty"`
	// Copy is, for a tool a colleague shared, the definition the user took:
	// what their agents run. The colleague's later edits reach them only when
	// they accept them, by taking the tool again. Nil for a published tool,
	// whose release an administrator approves for everybody at once.
	Copy *TempTool `json:"copy,omitempty"`
	// At is when Copy was taken.
	At time.Time `json:"at,omitempty"`
}

// loadAdoptions reads the user's adoptions by name: the list, completed from
// the records that still match it. A pinned list entry wins over a bare one.
func loadAdoptions(db Database, username string) map[string]toolAdoption {
	out := map[string]toolAdoption{}
	if db == nil || username == "" {
		return out
	}
	for name, owner := range loadAdoptionPins(db, username) {
		out[name] = toolAdoption{Name: name, Owner: owner}
	}
	var recs []toolAdoption
	db.Get(toolAdoptionsTable, username, &recs)
	for _, r := range recs {
		if l, listed := out[r.Name]; listed && r.Owner != "" && (l.Owner == "" || l.Owner == r.Owner) {
			out[r.Name] = r
		}
	}
	return out
}

// loadAdoptionPins reads the user's adoption list as name -> owner, "" for one
// recorded before owners were. A pinned entry wins over a bare one.
func loadAdoptionPins(db Database, username string) map[string]string {
	out := map[string]string{}
	if db == nil || username == "" {
		return out
	}
	var entries []string
	db.Get(adoptedGlobalToolsTable, username, &entries)
	for _, e := range entries {
		name, owner := splitAdoption(e)
		if name == "" {
			continue
		}
		if prev, seen := out[name]; !seen || prev == "" {
			out[name] = owner
		}
	}
	return out
}

// saveAdoptionsLocked writes the user's whole adoption list, and the records
// completing it. Caller holds tempToolPersistMu.
func saveAdoptionsLocked(db Database, username string, recs map[string]toolAdoption) {
	list := make([]string, 0, len(recs))
	out := make([]toolAdoption, 0, len(recs))
	for name, r := range recs {
		if r.Owner == "" {
			list = append(list, name)
			continue
		}
		list = append(list, name+adoptionOwnerSep+r.Owner)
		out = append(out, r)
	}
	sort.Strings(list)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	db.Set(adoptedGlobalToolsTable, username, list)
	db.Set(toolAdoptionsTable, username, out)
}

// LoadAdoptedGlobalTools returns the set of global (Shared) tool NAMES the user
// has adopted into their fleet. Global tools are opt-IN: a Shared tool loads for
// a user's agents only once they've adopted it from the catalog (their Account
// page). Empty set when the user has adopted none. Which owner's tool a name
// resolves to is AdoptedToolsFor's question, not this one's.
func LoadAdoptedGlobalTools(db Database, username string) map[string]bool {
	db = tempToolStore(db)
	out := map[string]bool{}
	for name := range loadAdoptions(db, username) {
		out[name] = true
	}
	return out
}

// adoptionCandidates are the tools answering to name that user may take: those
// shared with them by a colleague first (the colleague's current definition,
// Version 0), then the releases the deployment publishes whose adopt list
// admits them. Every owner's, not the first one found.
func adoptionCandidates(db Database, user, name string) []LentTool {
	var out []LentTool
	for _, p := range PeerSharedToolsFor(db, user) {
		if p.Tool.Name == name {
			out = append(out, p)
		}
	}
	for _, p := range publishedTools(db) {
		if p.Owner != user && !p.Tool.Disabled && p.Tool.Name == name &&
			(len(p.AllowedUsers) == 0 || sliceHas(p.AllowedUsers, user)) {
			out = append(out, p)
		}
	}
	return out
}

// pickAdoption is the candidate an adoption resolves to: its owner's, and the
// very tool it was taken from once its ID is known.
func pickAdoption(cands []LentTool, rec toolAdoption) (LentTool, bool) {
	for _, c := range cands {
		if c.Owner == rec.Owner && (rec.ID == "" || c.ID == rec.ID) {
			return c, true
		}
	}
	return LentTool{}, false
}

// distinctOwners lists the owners among candidates, in order.
func distinctOwners(cands []LentTool) []string {
	var out []string
	for _, c := range cands {
		if !sliceHas(out, c.Owner) {
			out = append(out, c.Owner)
		}
	}
	return out
}

// SetGlobalToolAdopted adds (adopted=true) or removes (adopted=false) one global
// tool from the user's adoption list, pinned to owner. An empty owner is
// resolved when exactly one owner offers the name, and refused when more than
// one does: whose code the user's agents will run is not a thing to guess.
// Un-adopting is always allowed, so a tightened ACL never strands a tool.
//
// Taking a colleague's tool freezes a copy of its definition as it is now, and
// taking it again replaces that copy with the current one: that is how a user
// accepts a colleague's update (LentTool.Update says there is one).
func SetGlobalToolAdopted(db Database, username, name, owner string, adopted bool) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("adoption requires an authenticated user")
	}
	name, owner = strings.TrimSpace(name), strings.TrimSpace(owner)
	if name == "" {
		return errString("tool name required")
	}
	var took LentTool
	if adopted {
		cands := adoptionCandidates(db, username, name)
		owners := distinctOwners(cands)
		switch {
		case len(owners) == 0:
			return errString("not permitted to adopt tool " + name)
		case owner == "" && len(owners) > 1:
			return errString("more than one person offers a tool called " + name + " (" + strings.Join(owners, ", ") + "); say whose to take")
		case owner == "":
			owner = owners[0]
		case !sliceHas(owners, owner):
			return errString("not permitted to adopt " + owner + "'s tool " + name)
		}
		took, _ = pickAdoption(cands, toolAdoption{Owner: owner})
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	recs := loadAdoptions(db, username)
	if adopted {
		rec := toolAdoption{Name: name, Owner: owner, ID: took.ID}
		if took.Version == 0 {
			frozen := took.Tool
			rec.Copy, rec.At = &frozen, time.Now()
		}
		recs[name] = rec
	} else {
		delete(recs, name)
	}
	saveAdoptionsLocked(db, username, recs)
	return nil
}

// AdoptedToolsFor resolves the user's adoptions to the tools their agents load:
// each name to the tool of the owner it was adopted from, still shared with
// them or still published and still admitting them. The one resolver the chat
// runtime and the watch runtime both use, so they cannot disagree.
//
// What loads is never the owner's working copy. A published tool loads its
// release, the version an administrator approved. A colleague's tool loads the
// copy the user took (LentTool.Update carries the colleague's newer definition
// when there is one, for the user to accept or not).
//
// An adoption recorded before owners were is pinned here the first time exactly
// one owner offers the name. When several do it loads none of them and says so:
// the old behaviour picked whichever was found first. An adoption recorded
// before IDs and copies were is completed the first time it resolves, with the
// tool's ID and, for a colleague's tool, a copy of its definition then: the
// same thing the user was running.
func AdoptedToolsFor(db Database, user string) []LentTool {
	db = tempToolStore(db)
	if db == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	recs := loadAdoptions(db, user)
	names := make([]string, 0, len(recs))
	for name := range recs {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []LentTool
	learned := map[string]toolAdoption{}
	// Adoptions of the user's OWN tools, which can never load: nobody adopts
	// what they already own, and the candidates above leave their own out by
	// design. They got here from the migration that grandfathered everybody
	// into the global tools they used to see, their own published ones
	// included, and they read downstream as tools somebody took back.
	owned := map[string]bool{}
	for _, p := range LoadPersistentTempTools(db, user) {
		owned[p.Tool.Name] = true
	}
	stale := map[string]toolAdoption{}
	for _, name := range names {
		rec := recs[name]
		cands := adoptionCandidates(db, user, name)
		if rec.Owner == user || (rec.Owner == "" && owned[name] && len(cands) == 0) {
			stale[name] = rec
			continue
		}
		if rec.Owner == "" {
			owners := distinctOwners(cands)
			if len(owners) > 1 {
				Log("[temp_tool_persist] %s adopted %q before owners were recorded, and %s all offer one: loading none of them until it is taken again from one owner", user, name, strings.Join(owners, ", "))
				continue
			}
			if len(owners) == 0 {
				continue
			}
			rec.Owner = owners[0]
		}
		c, ok := pickAdoption(cands, rec)
		if !ok {
			continue
		}
		if rec.ID == "" {
			rec.ID = c.ID
		}
		if c.Version == 0 {
			if rec.Copy == nil {
				frozen := c.Tool
				rec.Copy, rec.At = &frozen, time.Now()
			}
			current := c.Tool
			c.Tool = *rec.Copy
			if !current.SameDefinition(c.Tool) {
				c.Update = &current
			}
		}
		if rec != recs[name] {
			learned[name] = rec
		}
		out = append(out, c)
	}
	if len(learned) > 0 || len(stale) > 0 {
		tempToolPersistMu.Lock()
		cur := loadAdoptions(db, user)
		for name, rec := range stale {
			// Only the record read above: one taken again since is the
			// newer word.
			if was, still := cur[name]; still && was.Owner == rec.Owner {
				delete(cur, name)
				Log("[temp_tool_persist] %s: dropped the adoption of %q: it is their own tool, which cannot be adopted", user, name)
			}
		}
		for name, rec := range learned {
			// Fill in only what is still missing. An adoption removed or taken
			// again since the read above is the newer word.
			was, still := cur[name]
			if !still || (was.Owner != "" && was.Owner != rec.Owner) || (was.ID != "" && was.ID != rec.ID) {
				continue
			}
			if was.Copy != nil {
				rec.Copy, rec.At = was.Copy, was.At
			}
			cur[name] = rec
		}
		saveAdoptionsLocked(db, user, cur)
		tempToolPersistMu.Unlock()
	}
	return out
}

// MergeAdoptedGlobalTools unions the given names into the user's adoption list.
// Used by the one-time opt-in migration to grandfather every existing user into
// the global tools they saw under the old auto-load model, without clobbering
// anything they'd already adopted. A merged name has no owner yet; it is pinned
// the first time it resolves unambiguously.
func MergeAdoptedGlobalTools(db Database, username string, names []string) {
	db = tempToolStore(db)
	if db == nil || username == "" || len(names) == 0 {
		return
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	recs := loadAdoptions(db, username)
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			if _, have := recs[n]; !have {
				recs[n] = toolAdoption{Name: n}
			}
		}
	}
	saveAdoptionsLocked(db, username, recs)
}

// QueuePendingTempTool adds a tool to the approval queue. Returns an
// error if a same-named tool is already approved, or already pending with
// a different definition (avoid silent overwrites — the user should
// explicitly delete the old one first), or if the name is one no authoring
// path would accept.
func QueuePendingTempTool(db Database, username string, t TempTool, sessionID string) error {
	return QueuePendingTempToolScoped(db, username, t, sessionID, nil)
}

// QueuePendingTempToolScoped is QueuePendingTempTool for a tool that should be
// scoped to particular agents once approved (an imported agent's own tools).
func QueuePendingTempToolScoped(db Database, username string, t TempTool, sessionID string, scopeAgents []string) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("persistence requires an authenticated user")
	}
	// Both import paths queue here, so the name rules every authoring path
	// applies are checked here once (see toolArtifact.importRefusal).
	if why := (toolArtifact{}).importRefusal(t.Name); why != "" {
		return errString(why)
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
	// A same-named tool already pending is NOT replaced. Replacing was meant
	// for an author iterating on a draft, but no authoring path queues here
	// any more: the callers are imports (a tool bundle, an agent recipe's
	// tools), and two of those naming one tool are two different authors. The
	// second silently took the first's place, code and scope both, so the
	// agent the first was scoped to lost its tool on approval.
	//
	// The same definition is the same tool: it keeps its place in the queue
	// and gains the new scope (empty on either side means every agent, which
	// already covers the other). A different one is refused, and the caller
	// reports it; the pending tool is left as it was.
	pending := LoadPendingTempTools(db, username)
	for i, p := range pending {
		if p.Tool.Name != t.Name {
			continue
		}
		if !p.Tool.SameDefinition(t) {
			return errString("a different tool named " + t.Name + " is already waiting for approval; approve or reject it first, or rename this one")
		}
		switch {
		case len(p.ScopeAgents) == 0:
		case len(scopeAgents) == 0:
			pending[i].ScopeAgents = nil
		default:
			for _, id := range scopeAgents {
				if !sliceHas(pending[i].ScopeAgents, id) {
					pending[i].ScopeAgents = append(pending[i].ScopeAgents, id)
				}
			}
		}
		Log("[temp_tool_persist] %s: %q queued again with the same definition; kept the pending one, scope now %v", username, t.Name, pending[i].ScopeAgents)
		db.Set(pendingTempToolsTable, username, pending)
		return nil
	}
	rest := append(pending, PendingTempTool{
		Tool:             t,
		RequestedAt:      time.Now(),
		RequestedSession: sessionID,
		ScopeAgents:      scopeAgents,
	})
	db.Set(pendingTempToolsTable, username, rest)
	return nil
}

// SameDefinition reports whether t and o do the same thing: equal once the
// governance flags an owner sets (lock, disable, builder-only, bound-only,
// trial, confirm-in-chat) are set aside. A method so callers outside core can
// ask it without a new top-level export.
func (t TempTool) SameDefinition(o TempTool) bool { return !toolDefinitionChanged(t, o) }

// toolDefinitionChanged reports whether what a tool DOES changed between two
// versions, ignoring the governance flags an owner sets on it (lock, disable,
// builder-only, bound-only, trial, confirm-in-chat).
func toolDefinitionChanged(a, b TempTool) bool {
	return !reflect.DeepEqual(withoutGovernance(a), withoutGovernance(b))
}

// withoutGovernance clears the flags an owner sets ON a tool rather than IN
// it, leaving what the tool does.
func withoutGovernance(t TempTool) TempTool {
	t.Locked, t.Disabled, t.BuilderOnly, t.BoundOnly = false, false, false, false
	t.Trial, t.TrialSince, t.ConfirmInChat = false, time.Time{}, false
	return t
}

// AdminPersistTempTool writes a TempTool directly into the per-user
// persistent pool, skipping the pending-approval queue. Used by the
// admin-driven "promote a session draft to user-wide" surface in the
// chat Tools modal — the admin is already authorized to approve, so
// the queue step would be pure ceremony. Replaces any existing entry
// with the same name.
// AdminReconfigureTempTool replaces the TOOL DEFINITION of an existing persistent
// tool in place, preserving its wrapper metadata (ApprovedAt, LastUsedAt, Shared,
// AllowedUsers) — the safe save path for provenance-driven "Configure". A plain
// AdminPersistTempTool would mint a fresh wrapper and silently drop the tool's
// share + adopt-ACL state. Errors if no tool of that name exists for the owner
// (reconfigure is an edit, not a create).
func AdminReconfigureTempTool(db Database, username string, t TempTool) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	list := LoadPersistentTempTools(db, username)
	found := false
	for i := range list {
		if list[i].Tool.Name == t.Name {
			// Keeps ID/ApprovedAt/LastUsedAt/Shared/AllowedUsers. A published
			// tool stays published: adopters run its release, which this
			// does not touch.
			list[i].Tool = t
			found = true
			break
		}
	}
	if !found {
		return errString("no persistent tool named " + t.Name)
	}
	db.Set(persistentTempToolsTable, username, list)
	return nil
}

func AdminPersistTempTool(db Database, username string, t TempTool) error {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return errString("admin action requires authenticated user")
	}
	Debug("[temp_tool_persist] AdminPersistTempTool %q: acquiring tool lock", t.Name)
	tempToolPersistMu.Lock()
	Debug("[temp_tool_persist] AdminPersistTempTool %q: lock acquired", t.Name)
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	rest := approved[:0]
	next := PersistentTempTool{Tool: t, ApprovedAt: time.Now()}
	for i := range approved {
		if approved[i].Tool.Name != t.Name {
			rest = append(rest, approved[i])
			continue
		}
		// Preserve the USER-set governance flags across an AI re-persist. These
		// are set from Extensions › Tools, never through tool_def's
		// create-args, so a Builder edit that reconstructs the record would
		// otherwise silently clear them (e.g. re-enable a disabled tool). Locked
		// tools can't be re-persisted at all (the tool_def guard blocks it), but
		// carry it too for completeness.
		next.Tool.Locked = approved[i].Tool.Locked
		next.Tool.Disabled = approved[i].Tool.Disabled
		next.Tool.BuilderOnly = approved[i].Tool.BuilderOnly
		next.Tool.BoundOnly = approved[i].Tool.BoundOnly
		// CONFIRMATION is one of these flags and was missing from the list.
		//
		// Trial says nobody has vouched for the tool yet, and clearing it is a
		// user action taken in Extensions › Tools — never something tool_def
		// carries. So a Builder edit reconstructing the record silently
		// UN-CONFIRMED a tool the user had confirmed, and the consequence was
		// invisible: a trial tool is kept out of the inline catalog and
		// reachable only via load_tool, so the agent stopped seeing it and
		// reached for whatever it could see instead.
		//
		// Reported live as "the moltbook agent keeps using fetch_url over the
		// moltbook tools" — a tool that had been confirmed, was edited, and
		// quietly left the catalog. Nothing failed; it simply was not offered.
		//
		// Carried in BOTH directions: a tool that was Trial before an edit
		// stays Trial, because an edit is not a vouching either.
		next.Tool.Trial = approved[i].Tool.Trial
		next.Tool.TrialSince = approved[i].Tool.TrialSince
		// ConfirmInChat is governance of the same kind, and the same trap. A
		// tool set to ask before every call, then edited by Builder, would
		// have come back silent: the owner's decision about risk cleared by a
		// rewrite of the tool's body, with nothing saying so and the next call
		// going through unasked.
		//
		// Preserved only on UPDATE, which is the whole of this loop: a tool
		// being created has no prior value, so an author declaring
		// confirm_in_chat on a genuinely dangerous new tool still lands. After
		// that it is the owner's, changed from a governance surface.
		next.Tool.ConfirmInChat = approved[i].Tool.ConfirmInChat
		// Wrapper-level state survives a re-persist too. ScopeAgents is the
		// flattened namespace's scope — dropping it would silently promote an
		// agent-scoped tool to shared on every Builder edit. Shared /
		// AllowedUsers were ALSO silently dropped here before the flatten (a
		// published tool became unpublished on any edit); LastUsedAt is
		// telemetry worth keeping.
		next.ScopeAgents = approved[i].ScopeAgents
		next.Shared = approved[i].Shared
		next.AllowedUsers = approved[i].AllowedUsers
		// And the owner's own share list, dropped here the same way: every
		// edit silently revoked the tool from the colleagues it was shared
		// with, while the share index still listed them.
		next.SharedWith = approved[i].SharedWith
		next.LastUsedAt = approved[i].LastUsedAt
		// The same tool, edited: it keeps its ID, so its release and the
		// adoptions of it still name it. The edit reaches only the owner's
		// own agents; adopters run the release until an update is approved.
		next.ID = approved[i].ID
	}
	if next.ID == "" {
		next.ID = UUIDv4()
	}
	rest = append(rest, next)
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
	// Same invariant, one pool over: a name that is live again cannot ALSO
	// still be sitting in the orphan pool. Orphaning happens when the last
	// agent carrying a tool is deleted (captureOrphanedTools) — re-creating
	// or re-homing the name is the resolution of that orphan, so clear it
	// here rather than leaving two rows with the same name for the admin UI
	// to render side by side. Without this the stale copy also stayed
	// re-homeable, and re-homing it would overwrite the live definition.
	if removeOrphanedTempToolLocked(db, username, t.Name) {
		Debug("[temp_tool_persist] persist %q: cleared the stale orphan of the same name", t.Name)
	}
	// Eager session-draft cleanup — same rationale as in
	// ApprovePendingTempTool: prevent the new persistent entry from
	// being shadowed by any stale draft of the same name in any of
	// the user's chat sessions.
	if n := cleanupSessionDraftsByNameLocked(db, username, t.Name); n > 0 {
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
			continue
		}
		// The record it replaces goes, and a release of it goes with it,
		// kept as a tombstone: the new record is a new tool (a new ID) that
		// nobody approved for the catalog.
		withdrawToolReleaseLocked(db, username, approved[i])
	}
	deduped = append(deduped, PersistentTempTool{
		ID:          UUIDv4(),
		Tool:        moved.Tool,
		ApprovedAt:  time.Now(),
		ScopeAgents: moved.ScopeAgents,
	})
	db.Set(pendingTempToolsTable, username, rest)
	db.Set(persistentTempToolsTable, username, deduped)
	// Clean up the originating session draft. The mutex is already
	// held, so call the lock-free core: the locking form would deadlock on
	// this same non-reentrant mutex and freeze tool persistence
	// process-wide. Best-effort — the draft may already be gone by
	// approval (session deleted, draft manually dropped).
	if moved.RequestedSession != "" {
		removeSessionTempToolLocked(db, moved.RequestedSession, name)
	}
	// AND scan ALL the user's chat sessions for stale drafts with the
	// same name — the originating-session cleanup above misses cases
	// where the LLM re-authored the same tool in a different session
	// (or where chat itself wrote a draft via add_tool while Builder
	// also queued one). The lazy filter at handleSessionToolsList
	// catches these on next modal open, but eager cleanup here makes
	// the "exactly one of session/persistent" invariant immediate.
	if n := cleanupSessionDraftsByNameLocked(db, username, name); n > 0 {
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
	notify := false
	defer func() {
		if notify {
			noteToolWithdrawn(username, name, nil)
		}
	}()
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	approved := LoadPersistentTempTools(db, username)
	rest := approved[:0]
	for i := range approved {
		if approved[i].Tool.Name == name {
			// Deleting a published tool withdraws its release, and the
			// release is kept as a tombstone: adopters lose the tool either
			// way, and a record of what they had is what lets them be
			// offered it back as their own copy.
			if withdrawToolReleaseLocked(db, username, approved[i]) {
				notify = true
			}
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

// cleanupSessionDraftsByNameLocked removes stale drafts of toolName from THIS USER's
// chat sessions — the drafts a freshly committed tool of the same name has just
// superseded. Returns the number cleaned.
//
// TENANCY: it must enumerate the user's sessions through the registered draft
// lister, NOT by walking the session_temp_tools table. That table is global,
// keyed by chat-session id with no owner on the row (tempToolStore resolves to
// RootDB), so a bare walk cleans by NAME ACROSS EVERY USER: one user persisting
// a tool called "get_weather" would silently delete another user's unrelated
// session draft of the same name. The previous implementation did exactly that,
// while its own comment claimed to be "naturally scoped to the user" because it
// expected a per-user DB that tempToolStore had already overridden. Same class
// of bug as the credential audit log keyed on a bare name.
//
// With no lister registered (a deployment without the sessions app) there are
// no sessions to clean, so this is a no-op rather than a global sweep.
// NOTE: BOTH callers (AdminPersistTempTool, ApprovePendingTempTool) invoke
// this while HOLDING tempToolPersistMu, hence the Locked suffix and the
// lock-free removal below. Calling the exported RemoveSessionTempTool here
// deadlocked on the non-reentrant mutex — and because the mutex is
// process-global, the stuck goroutine froze tool persistence for everything.
//
// This is the same defect as the direct ApprovePendingTempTool call, one level
// deeper: this function does not lock, so a one-level scan for "locked
// function calls locking function" walked straight past it. The reachability
// test alongside these is transitive for that reason.
func cleanupSessionDraftsByNameLocked(db Database, username, toolName string) int {
	db = tempToolStore(db)
	if db == nil || strings.TrimSpace(username) == "" || toolName == "" {
		return 0
	}
	cleaned := 0
	seen := map[string]bool{} // a session is cleaned once even if it held duplicates
	for _, d := range ListSessionDrafts(username) {
		if d.Tool.Name != toolName || seen[d.SessionID] {
			continue
		}
		seen[d.SessionID] = true
		if removeSessionTempToolLocked(db, d.SessionID, toolName) {
			cleaned++
		}
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
// TouchPersistentTempTool bumps LastUsedAt. Pure telemetry, and it runs after
// EVERY custom tool execution — which makes it the widest possible blast
// radius for a stall on the shared tool mutex.
//
// That is not hypothetical: while ApprovePendingTempTool deadlocked holding
// this mutex, every agent turn that called any custom tool ran its tool
// successfully and then froze HERE, on a timestamp write, so the work
// completed but no reply was ever sent.
//
// So it does not wait. TryLock means a contended moment costs one LastUsedAt
// value, which is worth nothing, instead of the turn, which is worth
// everything. Best-effort was always the stated contract at the call sites;
// this makes the code match it.
func TouchPersistentTempTool(db Database, username, name string) {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return
	}
	if !tempToolPersistMu.TryLock() {
		return
	}
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
	return removeSessionTempToolLocked(db, chatSessionID, name)
}

// removeSessionTempToolLocked is the body of RemoveSessionTempTool with the
// locking removed, for callers that ALREADY hold tempToolPersistMu.
//
// This split exists because ApprovePendingTempTool called the locking form
// while holding the lock. sync.Mutex is not reentrant, so that goroutine
// blocked forever — and because the mutex is process-global, it took every
// other tool-persist operation down with it: later requests kept arriving and
// completing their own work but could never publish, because the reply path
// needed the same lock. A hung tool_def update froze tool persistence for the
// whole process.
//
// The call site carried a comment asserting "RemoveSessionTempTool takes no
// lock of its own so this is safe." That was true once; the lock was added
// later and the comment was not revisited. Hence the explicit Locked suffix —
// a name that cannot silently become wrong.
func removeSessionTempToolLocked(db Database, chatSessionID, name string) bool {
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

// FindSharedToolWithOwner locates a DEPLOYMENT-WIDE shared tool by name and
// returns it along with the username that owns the record.
//
// Exists because the shared pool is otherwise write-blind: shared tools enter
// an agent's catalog (see the adopted-global branch in the runner's tool
// assembly) and are perfectly callable, but every tool_def lookup searched
// only the CALLER's own pool. A tool you could invoke all day answered
// `tool_def(action="get")` with "no tool named X" — which reads as "your tool
// vanished" and sent at least one authoring session chasing a framework bug
// that did not exist.
//
// Returns the owner so callers can tell "this is yours, edit it" apart from
// "this belongs to someone else", which are different answers.
//
// The tool returned is the RELEASE, what adopters run, not the owner's working
// copy.
func FindSharedToolWithOwner(db Database, name string) (tool PersistentTempTool, owner string, found bool) {
	db = tempToolStore(db)
	if db == nil || strings.TrimSpace(name) == "" {
		return PersistentTempTool{}, "", false
	}
	for _, p := range publishedTools(db) {
		if p.Tool.Name == name {
			return p.PersistentTempTool, p.Owner, true
		}
	}
	return PersistentTempTool{}, "", false
}

// SharedToolOwners returns name → owning-username for every DEPLOYMENT-WIDE
// shared tool, in one pass over the pools.
//
// The per-name variant (FindSharedToolWithOwner) walks every user's pool, so
// calling it once per row of a listing would be quadratic. Callers rendering a
// catalog take this map instead.
func SharedToolOwners(db Database) map[string]string {
	db = tempToolStore(db)
	out := map[string]string{}
	for _, p := range publishedTools(db) {
		out[p.Tool.Name] = p.Owner
	}
	return out
}

// ToolClaimNote is the advisory to append when a generic fetch (fetch_url,
// browse_page) targets a host one of the caller's OWN tools already serves.
// Returns "" when nothing claims it, which is the overwhelmingly common case.
//
// This is the tool-level counterpart of SecureAPI.AutoRouteCredential, and it
// exists for the same observed reason: the always-available generic tool wins
// the model's attention over the purpose-built one, and the generic call then
// fails in a way that doesn't look like failure — a 401 there, a consent
// banner here. A credential declares its host with BaseURL; a tool declares
// its hosts in the URL templates it already stores, so the claim is DERIVED
// and no host is ever named in framework code.
//
// The claim is read from the tools resolved for THIS turn rather than reloaded
// from the store: that set is already scoped to the caller and to what this
// agent may call, so the note can never point at a tool the reader cannot
// reach, and the lookup costs no DB read on a path that runs on every fetch.
//
// Shell-mode tools claim nothing. Their CommandTemplate is a command line, and
// reading a host out of it would claim on the strength of a URL that merely
// appears in an argument.
func ToolClaimNote(sess *ToolSession, rawURL string) string {
	host := textutil.HostKey(rawURL)
	if host == "" || sess == nil {
		return ""
	}
	var claims []textutil.ToolClaim
	for _, tt := range sess.CopyTempTools() {
		if tt == nil || tt.Disabled {
			continue
		}
		claim := textutil.ToolClaim{Tool: tt.Name}
		matched := strings.EqualFold(strings.TrimSpace(tt.Mode), "api") &&
			textutil.HostKey(tt.CommandTemplate) == host
		for _, a := range tt.Actions {
			if a.Disabled || textutil.HostKey(a.URLTemplate) != host {
				continue
			}
			matched = true
			if n := strings.TrimSpace(a.Name); n != "" {
				claim.Actions = append(claim.Actions, n)
			}
		}
		if !matched {
			continue
		}
		sort.Strings(claim.Actions)
		claims = append(claims, claim)
	}
	return textutil.ClaimNote(host, claims)
}

// The tool kind's approve side effect: publish it to the deployment-wide
// catalog, or, for a tool already there, make the requested definition its
// next version. Registered here, next to the primitive it calls, so the admin
// queue approves a tool the same way it approves every other kind — through
// the registry — and needs no per-kind switch of its own.
//
// The request hook freezes what the owner asked for at the moment they asked,
// so the administrator approves the definition they were shown, not whatever
// the working copy says by the time they click.
func init() {
	promotion.RegisterApprover("tool", func(owner, name string) error {
		if AuthDB == nil {
			return errString("auth store not initialized")
		}
		return approveToolRelease(AuthDB(), owner, name)
	})
	promotion.RegisterRequestHook("tool", func(owner, name string) error {
		return snapshotToolRequest(nil, owner, name)
	})
}

// ----------------------------------------------------------------------
// Published releases
// ----------------------------------------------------------------------

const (
	// toolReleasesTable is what the deployment catalog serves, keyed by tool
	// NAME: the deployment publishes one tool per name.
	toolReleasesTable = "tool_releases"
	// withdrawnToolReleasesTable keeps the last release of a tool its owner
	// withdrew or deleted, keyed by the tool's ID.
	withdrawnToolReleasesTable = "withdrawn_tool_releases"
	// toolUpdateRequestsTable holds the definition a publish or update request
	// asked for, keyed by the request's id.
	toolUpdateRequestsTable = "tool_update_requests"
	// toolReleaseHistoryCap is how many replaced versions a release keeps to
	// roll back to.
	toolReleaseHistoryCap = 5
)

// ToolRelease is a published tool as its adopters run it: a frozen copy of the
// owner's definition, taken when an administrator approved it.
//
// Separate from the owner's own record because an approval covers the code
// that was reviewed. Adopters used to load the owner's live record, so every
// edit reached them unreviewed, and the fix for that (un-publishing on any
// edit) took the tool away from all of them instead. Now the owner's record is
// a working copy that only their own agents run; the owner asks for it to
// become the next version, and an administrator approves that against a diff.
type ToolRelease struct {
	ID         string    `json:"id"`
	Owner      string    `json:"owner"`
	Name       string    `json:"name"`
	Tool       TempTool  `json:"tool"`
	Version    int       `json:"version"`
	ApprovedAt time.Time `json:"approved_at"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	// History is the versions this one replaced, newest first, at most
	// toolReleaseHistoryCap: what an administrator can roll back to. Entries
	// carry no history of their own.
	History []ToolRelease `json:"history,omitempty"`
	// WithdrawnAt is set only on the tombstone kept after its owner withdrew
	// or deleted the tool.
	WithdrawnAt time.Time `json:"withdrawn_at,omitempty"`
}

// ToolReleases lists what the deployment catalog serves, one release per
// published tool, sorted by name: the administrator's view of versions and
// history. Adopters resolve through AdoptedToolsFor, never through this.
func ToolReleases(db Database) []ToolRelease {
	db = tempToolStore(db)
	var out []ToolRelease
	for _, p := range publishedTools(db) {
		if rel, ok := loadToolRelease(db, p.Tool.Name); ok {
			out = append(out, rel)
		}
	}
	return out
}

func loadToolRelease(db Database, name string) (ToolRelease, bool) {
	var rel ToolRelease
	if db == nil || name == "" {
		return rel, false
	}
	return rel, db.Get(toolReleasesTable, name, &rel)
}

// releaseOwnerRow finds the owner's record a release was taken from, and
// reports whether the release is LIVE: that record still exists (same ID, so
// not a same-named successor) and is still published.
func releaseOwnerRow(db Database, rel ToolRelease) (PersistentTempTool, bool) {
	for _, p := range LoadPersistentTempTools(db, rel.Owner) {
		if p.Tool.Name == rel.Name && p.ID == rel.ID {
			return p, p.Shared
		}
	}
	return PersistentTempTool{}, false
}

// publishedTools is the deployment catalog as adopters see it: every live
// release, with its owner and version, and the owner's governance on it (who
// may adopt it, and the disable switch, which is a stop rather than a change
// to what runs). Sorted by name.
func publishedTools(db Database) []LentTool {
	if db == nil {
		return nil
	}
	var out []LentTool
	for _, name := range db.Keys(toolReleasesTable) {
		rel, ok := loadToolRelease(db, name)
		if !ok || rel.Name != name {
			continue
		}
		row, live := releaseOwnerRow(db, rel)
		if !live {
			continue
		}
		t := rel.Tool
		t.Disabled = row.Tool.Disabled
		out = append(out, LentTool{
			PersistentTempTool: PersistentTempTool{
				ID: rel.ID, Tool: t, ApprovedAt: rel.ApprovedAt, LastUsedAt: row.LastUsedAt,
				Shared: true, AllowedUsers: row.AllowedUsers,
			},
			Owner: rel.Owner, Version: rel.Version,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	return out
}

// nextReleaseVersion is one past every version the release has held, so a
// rollback never lets a later update reuse a number.
func nextReleaseVersion(rel ToolRelease) int {
	next := rel.Version
	for _, h := range rel.History {
		if h.Version > next {
			next = h.Version
		}
	}
	return next + 1
}

// pushReleaseHistory files prev (the version being replaced) into rel's
// history, newest first, keeping the last toolReleaseHistoryCap.
func pushReleaseHistory(rel *ToolRelease, prev ToolRelease) {
	prev.History, prev.WithdrawnAt = nil, time.Time{}
	hist := append([]ToolRelease{prev}, rel.History...)
	sort.SliceStable(hist, func(i, j int) bool { return hist[i].Version > hist[j].Version })
	if len(hist) > toolReleaseHistoryCap {
		hist = hist[:toolReleaseHistoryCap]
	}
	rel.History = hist
}

// publishToolReleaseLocked makes p's tool (or def, the definition a request
// froze) the catalog's release of it, and marks p published; the caller writes
// p back. Reports false, changing nothing, when p is already published: an
// already-published tool changes only by an approved update. Refuses a name
// another owner's live release holds. Caller holds tempToolPersistMu.
func publishToolReleaseLocked(db Database, owner string, p *PersistentTempTool, def *TempTool, by string) (bool, error) {
	name := p.Tool.Name
	if cur, ok := loadToolRelease(db, name); ok {
		if _, live := releaseOwnerRow(db, cur); live {
			if cur.Owner != owner {
				// One published tool per name, the rule skills already follow.
				// Two would leave every lookup by name to pick one, and
				// whichever it picked is whose code an adopter's agents run.
				return false, errString("the deployment already publishes a tool called " + name + " (" + cur.Owner + "'s); rename this one before publishing it")
			}
			if cur.ID == p.ID && p.Shared {
				return false, nil
			}
		}
	}
	if p.ID == "" {
		p.ID = UUIDv4()
	}
	d := p.Tool
	if def != nil {
		d = *def
	}
	// Published again after a withdrawal, the numbering carries on, so a
	// version number never names two different definitions of one tool.
	version := 1
	if gone, ok := withdrawnToolRelease(db, p.ID); ok {
		version = nextReleaseVersion(gone)
	}
	db.Set(toolReleasesTable, name, ToolRelease{
		ID: p.ID, Owner: owner, Name: name, Tool: d, Version: version,
		ApprovedAt: time.Now(), ApprovedBy: by,
	})
	p.Shared = true
	Log("[temp_tool_persist] published %s's tool %q as version %d", owner, name, version)
	return true, nil
}

// withdrawToolReleaseLocked takes p's release out of the catalog, if it has
// one, and keeps it as a tombstone. Caller holds tempToolPersistMu.
func withdrawToolReleaseLocked(db Database, owner string, p PersistentTempTool) bool {
	cur, ok := loadToolRelease(db, p.Tool.Name)
	if !ok || cur.Owner != owner || cur.ID != p.ID {
		return false
	}
	cur.WithdrawnAt = time.Now()
	db.Set(withdrawnToolReleasesTable, tombstoneKey(cur), cur)
	db.Unset(toolReleasesTable, cur.Name)
	Log("[temp_tool_persist] %s's tool %q (version %d) left the catalog; its release is kept as a tombstone", owner, cur.Name, cur.Version)
	return true
}

func tombstoneKey(rel ToolRelease) string {
	if rel.ID != "" {
		return rel.ID
	}
	return rel.Owner + "\x00" + rel.Name
}

// withdrawnToolRelease returns the last release of a tool its owner withdrew or
// deleted, by the tool's ID: the definition its adopters were running, which
// is what they can be offered back as a copy of their own.
func withdrawnToolRelease(db Database, id string) (ToolRelease, bool) {
	var rel ToolRelease
	if db == nil || id == "" {
		return rel, false
	}
	return rel, db.Get(withdrawnToolReleasesTable, id, &rel)
}

// RecreateLostTool offers back, as the user's OWN tool, a tool they took that
// is gone: the last approved release of a published tool its owner withdrew
// or deleted, or the frozen copy of a colleague's tool its owner deleted. The
// user's agents were running exactly that definition, so the copy is the same
// code under their own name, and later changes to it are theirs to make.
//
// Not offered when the tool still exists and the owner took it away from THIS
// user (a share revoked, or dropped from a published tool's adopt list): that
// is a decision about them, and recreating the tool would undo it.
//
// With apply=false it only says whether recreating is possible and from what,
// for the editor to decide whether to show the button.
func RecreateLostTool(db Database, user, name string, apply bool) (TempTool, error) {
	db = tempToolStore(db)
	user, name = strings.TrimSpace(user), strings.TrimSpace(name)
	if db == nil || user == "" || name == "" {
		return TempTool{}, errString("a user and a tool name are required")
	}
	ad, took := loadAdoptions(db, user)[name]
	if !took {
		return TempTool{}, errString("you did not take a tool called " + name)
	}
	for _, p := range AdoptedToolsFor(db, user) {
		if p.Tool.Name == name {
			return TempTool{}, errString(name + " still works for your agents: there is nothing to recreate")
		}
	}
	for _, p := range LoadPersistentTempTools(db, user) {
		if p.Tool.Name == name {
			return TempTool{}, errString("you already have a tool called " + name)
		}
	}
	var def TempTool
	found := false
	ownerRow, ownerStill := PersistentTempTool{}, false
	for _, p := range LoadPersistentTempTools(db, ad.Owner) {
		if p.Tool.Name == name && (ad.ID == "" || p.ID == ad.ID) {
			ownerRow, ownerStill = p, true
		}
	}
	switch {
	case ad.Copy != nil:
		// A colleague's tool: offered only when they deleted it. Still there
		// and no longer shared with this user is a revocation.
		if ownerStill {
			return TempTool{}, errString(ad.Owner + " stopped sharing " + name + " with you; ask them if you need it")
		}
		def, found = *ad.Copy, true
	default:
		if ownerStill && ownerRow.Shared {
			// Still published: this user was left off its adopt list.
			return TempTool{}, errString(name + " is still published, but not to you; ask an administrator if you need it")
		}
		key := ad.ID
		if key == "" {
			key = ad.Owner + "\x00" + name
		}
		if rel, ok := withdrawnToolRelease(db, key); ok && rel.Owner == ad.Owner {
			def, found = rel.Tool, true
		}
	}
	if !found {
		return TempTool{}, errString("no copy of " + name + " was kept, so it cannot be recreated")
	}
	if !apply {
		return def, nil
	}
	def.Locked = false
	if err := AdminPersistTempTool(db, user, def); err != nil {
		return TempTool{}, err
	}
	// Their own copy now answers to the name; the adoption would only ever
	// point at the tool that is gone.
	_ = SetGlobalToolAdopted(db, user, name, "", false)
	Log("[temp_tool_persist] %s recreated %s's withdrawn tool %q as their own", user, ad.Owner, name)
	return def, nil
}

// toolRequestSnapshot is the definition a publish or update request asked
// for, frozen when it was asked.
type toolRequestSnapshot struct {
	ID   string    `json:"id,omitempty"`
	Tool TempTool  `json:"tool"`
	At   time.Time `json:"at"`
}

// snapshotToolRequest freezes the owner's working copy as what their publish
// or update request asks for. Refuses an update request with nothing in it:
// a working copy that is already the published version. Whether the
// requester owns the tool is the caller's check, made before filing; with no
// such tool there is nothing to freeze, and approving would find none.
func snapshotToolRequest(db Database, owner, name string) error {
	db = tempToolStore(db)
	if db == nil {
		return nil
	}
	p, ok := UserToolByName(db, owner, name)
	if !ok {
		return nil
	}
	if cur, ok := loadToolRelease(db, name); ok && p.Shared && cur.Owner == owner && cur.ID == p.ID &&
		cur.Tool.SameDefinition(p.Tool) {
		return errString("your copy of " + name + " is the published version " + strconv.Itoa(cur.Version) +
			", so there is no update to ask for; change it first")
	}
	db.Set(toolUpdateRequestsTable, PromotionRequestKey("tool", owner, name),
		toolRequestSnapshot{ID: p.ID, Tool: p.Tool, At: time.Now()})
	return nil
}

// Requested returns the definition a PENDING publish or update request for
// this tool asked for: what approving the request would publish. Keyed by the
// release's owner and name, so it answers for a tool not yet published too.
func (r ToolRelease) Requested(db Database) (TempTool, bool) {
	db = tempToolStore(db)
	if db == nil {
		return TempTool{}, false
	}
	key := PromotionRequestKey("tool", r.Owner, r.Name)
	if req, ok := GetPromotionRequest(db, key); !ok || req.State != PromotionPendingState {
		return TempTool{}, false
	}
	var snap toolRequestSnapshot
	if db.Get(toolUpdateRequestsTable, key, &snap) {
		return snap.Tool, true
	}
	// A request filed before requests froze their definition asks for the
	// working copy.
	if p, ok := UserToolByName(db, r.Owner, r.Name); ok {
		return p.Tool, true
	}
	return TempTool{}, false
}

// approveToolRelease is an administrator approving a tool request: the first
// publish of a tool, or the next version of a published one. What it publishes
// is the definition the request froze (the working copy, for a request from
// before requests froze one). The version replaced goes into the history.
func approveToolRelease(db Database, owner, name string) error {
	db = tempToolStore(db)
	if db == nil || owner == "" {
		return errString("admin action requires authenticated user")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	list := LoadPersistentTempTools(db, owner)
	idx := -1
	for i := range list {
		if list[i].Tool.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errString("no persistent tool named " + name)
	}
	p := &list[idx]
	key := PromotionRequestKey("tool", owner, name)
	def := p.Tool
	var snap toolRequestSnapshot
	if db.Get(toolUpdateRequestsTable, key, &snap) {
		if snap.ID != "" && p.ID != "" && snap.ID != p.ID {
			return errString(owner + " deleted " + name + " and made a new one since asking; deny this and let them ask again")
		}
		def = snap.Tool
	}
	cur, ok := loadToolRelease(db, name)
	if row, live := releaseOwnerRow(db, cur); !ok || !live || cur.Owner != owner || row.ID != p.ID {
		if _, err := publishToolReleaseLocked(db, owner, p, &def, ""); err != nil {
			return err
		}
		db.Set(persistentTempToolsTable, owner, list)
	} else if !cur.Tool.SameDefinition(def) {
		prev := cur
		pushReleaseHistory(&cur, prev)
		cur.Tool, cur.Version = def, nextReleaseVersion(prev)
		cur.ApprovedAt, cur.ApprovedBy = time.Now(), ""
		db.Set(toolReleasesTable, name, cur)
		Log("[temp_tool_persist] %s's tool %q: version %d approved, replacing version %d", owner, name, cur.Version, prev.Version)
	}
	db.Unset(toolUpdateRequestsTable, key)
	return nil
}

// RollBack makes one of the release's kept versions the one adopters run. The
// version it replaces is kept in turn, so a rollback can itself be undone.
func (r ToolRelease) RollBack(db Database, version int, by string) error {
	db = tempToolStore(db)
	if db == nil {
		return errString("tool store not initialized")
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	cur, ok := loadToolRelease(db, r.Name)
	if !ok || cur.Owner != r.Owner || cur.ID != r.ID {
		return errString(r.Name + " is no longer published")
	}
	if version == cur.Version {
		return errString(r.Name + " is already at version " + strconv.Itoa(version))
	}
	at := -1
	for i, h := range cur.History {
		if h.Version == version {
			at = i
			break
		}
	}
	if at < 0 {
		return errString("version " + strconv.Itoa(version) + " of " + r.Name + " is not kept; the last " + strconv.Itoa(toolReleaseHistoryCap) + " versions are")
	}
	target := cur.History[at]
	prev := cur
	cur.History = append(append([]ToolRelease{}, cur.History[:at]...), cur.History[at+1:]...)
	pushReleaseHistory(&cur, prev)
	cur.Tool, cur.Version = target.Tool, target.Version
	cur.ApprovedAt, cur.ApprovedBy = time.Now(), by
	db.Set(toolReleasesTable, r.Name, cur)
	Log("[temp_tool_persist] %s rolled %s's tool %q back to version %d from %d", by, r.Owner, r.Name, version, prev.Version)
	return nil
}

// MigrateToolReleases gives every tool an ID and every published tool the
// release its adopters run, version 1 being its definition now, so nothing
// anybody runs changes on upgrade. Called once at startup; marker-guarded,
// and idempotent besides.
func MigrateToolReleases(db Database) {
	NewMigrationRunner("core", "").Once("tool_releases:v1", func() int { return migrateToolReleases(db) })
}

// migrateToolReleases is MigrateToolReleases' body; returns records changed.
//
// Two published tools of one name (the deployment allowed that before it
// published one per name) cannot both have a release. The first owner in name
// order keeps it, which is also whose tool the old first-found lookup served;
// the other is un-published, and says so in the log.
func migrateToolReleases(db Database) int {
	db = tempToolStore(db)
	if db == nil {
		return 0
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	changed := 0
	owners := db.Keys(persistentTempToolsTable)
	sort.Strings(owners)
	for _, owner := range owners {
		list := LoadPersistentTempTools(db, owner)
		dirty := false
		for i := range list {
			if list[i].ID == "" {
				list[i].ID = UUIDv4()
				dirty = true
				changed++
			}
		}
		if dirty {
			db.Set(persistentTempToolsTable, owner, list)
		}
	}
	for _, owner := range owners {
		list := LoadPersistentTempTools(db, owner)
		dirty := false
		for i := range list {
			if !list[i].Shared {
				continue
			}
			// A no-op for a tool that already has its release.
			published, err := publishToolReleaseLocked(db, owner, &list[i], nil, "")
			if err != nil {
				Log("[temp_tool_persist] migration: %s's published tool %q un-published: %v", owner, list[i].Tool.Name, err)
				list[i].Shared = false
			}
			if published || err != nil {
				dirty = true
				changed++
			}
		}
		if dirty {
			db.Set(persistentTempToolsTable, owner, list)
		}
	}
	return changed
}

// ----------------------------------------------------------------------
// The named rung for a tool
// ----------------------------------------------------------------------

// sharedToolsTable indexes peer shares: recipient -> (owner, tool name).
const sharedToolsTable = "shared_tools"

// SetPersistentTempToolSharedWith is the owner's own rung: which colleagues may
// take a copy of this tool into their catalog.
//
// A separate field from AllowedUsers, which reads similarly and means something
// else. That one is the ADOPT-acl on a tool an administrator has already
// published to the whole deployment: "of everybody, these may take it". This is
// "nobody has it, except these". Folding them together would make the same list
// mean two things depending on another flag, which is how one of them quietly
// stops being enforced.
//
// A share is a POINTER, not a push. The tool appears in the recipient's catalog
// to adopt, and loads for their agents only once they do — the same two steps
// the global catalog has had since global tools were flipped from push to pull,
// and more obviously right here: a colleague should not be able to put code in
// your agents' hands without you saying so.
//
// REFUSED for a tool that dispatches through a SECURED credential. A secured
// credential has no user list at all: access follows the tools bound to it, so
// whoever can run a bound tool spends that key. An administrator decided which
// tools are bound; letting the tool's owner then decide who runs it would hand
// them the other half of a grant that was never theirs. The way to widen such a
// tool is to ask for it to be published, which is the same administrator
// answering the same question.
func SetPersistentTempToolSharedWith(db Database, owner, name string, users []string) error {
	store := tempToolStore(db)
	if store == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
		return errString("owner and tool name are required")
	}
	clean := cleanRecipients(users, owner)
	tempToolPersistMu.Lock()
	list := LoadPersistentTempTools(store, owner)
	var target *TempTool
	for i := range list {
		if list[i].Tool.Name == name {
			target = &list[i].Tool
			list[i].SharedWith = clean
			break
		}
	}
	if target == nil {
		tempToolPersistMu.Unlock()
		return errString("no persistent tool named " + name)
	}
	if len(clean) > 0 {
		if why := securedCredentialBlock(*target, owner); why != "" {
			tempToolPersistMu.Unlock()
			return errString(why)
		}
	}
	store.Set(persistentTempToolsTable, owner, list)
	tempToolPersistMu.Unlock()
	peershare.SetRecipients(store, sharedToolsTable, owner, name, clean)
	return nil
}

// securedCredentialBlock says why this tool may not be handed out by its owner,
// or "" when it may.
func securedCredentialBlock(t TempTool, owner string) string {
	cred := strings.TrimSpace(t.Credential)
	if cred == "" {
		return ""
	}
	c, ok := Secure().Resolve(cred, owner)
	if !ok || !Secure().EffectiveSecured(c, owner) {
		return ""
	}
	return "\"" + t.Name + "\" dispatches through the secured credential \"" + cred + "\", whose access follows the tools an administrator bound to it. " +
		"Sharing it would decide who spends that key, which is the administrator's half of the grant. Ask for the tool to be published instead."
}

// PeerSharedToolsFor returns the tools other people have shared WITH this user.
//
// The record is the source and the index is derived: a revoked share is gone
// here even if an index entry lingers, because a derived thing that can outvote
// its source is how a revoked share keeps working. A disabled tool is nobody's
// to run, its owner's included.
func PeerSharedToolsFor(db Database, user string) []LentTool {
	store := tempToolStore(db)
	if store == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	var out []LentTool
	for _, ref := range peershare.List(store, sharedToolsTable, user) {
		for _, p := range LoadPersistentTempTools(store, ref.Owner) {
			if p.Tool.Name != ref.ID || p.Tool.Disabled || !sliceHas(p.SharedWith, user) {
				continue
			}
			out = append(out, LentTool{PersistentTempTool: p, Owner: ref.Owner})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	return out
}

// LentTool is a peer-shared tool carried with the colleague who shared it.
//
// The owner rides along rather than being looked up again because every caller
// needs it: whose code you are about to run in your own session is the first
// thing a catalog has to say about an entry, and the last thing to have to go
// and find out separately.
type LentTool struct {
	PersistentTempTool
	Owner string
	// Version is the release version of a PUBLISHED tool (1 and up); 0 for a
	// tool a colleague shared directly, which has no releases.
	Version int
	// Update is, for a colleague's tool the user has taken, the colleague's
	// current definition when it differs from the copy the user runs: an
	// update they can accept by taking the tool again. Nil otherwise.
	Update *TempTool
}

func cleanRecipients(users []string, owner string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range users {
		if u = strings.TrimSpace(u); u != "" && u != owner && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

func sliceHas(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// SetUserToolConfirmInChat turns a tool's ask-before-every-call flag on or off.
//
// The same shape as SetUserToolScopeAgents beside it, and for the same reason:
// the flag lives on the tool RECORD, so it is one write to the pool rather
// than something an agent carries. A tool's riskiness is a property of the
// tool, and two agents holding it should not disagree about whether it asks.
//
// Reports whether a tool of that name was found, so a caller can tell "set" from
// "there is no such tool" instead of both looking like success.
// UserToolAsksInChat reports whether this tool stops and asks before every
// call, for this user.
//
// Reads BOTH stores. The name set is where the mark lives now; the flag on a
// tool record is where it used to, and a tool marked before this existed must
// not quietly stop asking because the storage moved underneath it.
// askKey names one mark set. An empty agent is the fleet-wide set, which binds
// every agent; the legacy key is the bare username, which is what the marks
// were stored under before they were scoped and means the same thing.
func askKey(owner, agentID string) string { return owner + ":" + agentID }

// askSetHas reports whether one mark set holds this name, and reads a FAILED
// read as holding it.
//
// TryGet rather than Get, because the two answers Get collapses fall opposite
// ways here. "The set is there and this name is not in it" means the tool runs
// without asking. "I could not read the set" means nothing is known, and the
// safe reading of an unknown RESTRICTION is that it applies: a turn that stops
// and asks a person who is sitting there costs a click, and one that does not
// ask runs a tool the owner marked. This is the hazard core/database.go records
// on TryGet in the words "a caller whose empty case GRANTS something has to be
// found and told which way to fall" - this is one of those callers, and it fell
// the wrong way.
//
// Its sibling on the unattended path already does this: credentialAlwaysConfirms
// returns true for a credential it cannot resolve. One question, two halves, and
// they used to fail in opposite directions.
func askSetHas(db Database, key, name string) bool {
	var names []string
	found, err := db.TryGet(askInChatToolsTable, key, &names)
	if err != nil {
		Log("[temptool] could not read the ask-before-every-call marks at %q (%v): treating %q as marked", key, err, name)
		return true
	}
	if !found {
		return false
	}
	return slices.Contains(names, name)
}

func UserToolAsksInChat(db Database, username, agentID, name string) bool {
	db = tempToolStore(db)
	// Not a failed read - there is nothing here to read. A nil store means the
	// binary installed no auth layer at all (a test harness, an embedded mode
	// with no users), so there are no marks and nobody to ask; and no tool name
	// means no question was asked. Failing CLOSED on these would stop every
	// tool call in every turn that runs without a user store, which is not a
	// restriction holding, it is the framework refusing to work.
	//
	// The direction that matters is inside askSetHas, where a store that IS
	// there and cannot be read now reads as marked.
	if db == nil || username == "" || strings.TrimSpace(name) == "" {
		return false
	}
	// This agent's own mark, then the fleet-wide one that binds every agent,
	// then the key the marks lived under before they were scoped. A mark made
	// before this must not quietly stop working because the key moved.
	if agentID != "" && askSetHas(db, askKey(username, agentID), name) {
		return true
	}
	if askSetHas(db, askKey(username, ""), name) || askSetHas(db, username, name) {
		return true
	}
	return askRecordFlag(db, username, name)
}

// askRecordFlag reads the mark's OLD home - the flag on the tool's own record -
// and falls the same way askSetHas does when the read fails.
//
// Its own read rather than LoadPersistentTempTools, which collapses a failed
// read into an empty pool. That loader has many callers and an empty pool is
// the right answer for most of them; here it means "this tool does not ask",
// which is the one reading that grants something.
func askRecordFlag(db Database, username, name string) bool {
	var pool []PersistentTempTool
	found, err := db.TryGet(persistentTempToolsTable, username, &pool)
	if err != nil {
		Log("[temptool] could not read %s's tool pool (%v): treating %q as marked ask-before-every-call", username, err, name)
		return true
	}
	if !found {
		return false
	}
	for _, p := range pool {
		if p.Tool.Name == name {
			return p.Tool.ConfirmInChat
		}
	}
	return false
}

// AskInChatTools lists every tool this user has marked, so a surface can show
// the marks it holds without asking about each name it happens to know.
func AskInChatTools(db Database, username, agentID string) []string {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return nil
	}
	var names []string
	for _, key := range []string{askKey(username, agentID), askKey(username, ""), username} {
		if agentID == "" && key == askKey(username, "") {
			continue // already asked for, as the first key
		}
		var got []string
		if db.Get(askInChatToolsTable, key, &got) {
			for _, n := range got {
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
	}
	for _, p := range LoadPersistentTempTools(db, username) {
		if !p.Tool.ConfirmInChat {
			continue
		}
		if !slices.Contains(names, p.Tool.Name) {
			names = append(names, p.Tool.Name)
		}
	}
	return names
}

// SetUserToolAsksInChat marks or unmarks ANY tool, whether or not the user
// authored it. Always reports true: there is no "no such tool" here, because
// the mark is about a NAME and the framework's tools have no record to look up.
func SetUserToolAsksInChat(db Database, username, agentID, name string, ask bool) bool {
	db = tempToolStore(db)
	name = strings.TrimSpace(name)
	if db == nil || username == "" || name == "" {
		return false
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	key := askKey(username, agentID)
	var names []string
	db.Get(askInChatToolsTable, key, &names)
	kept := names[:0:0]
	for _, n := range names {
		if n != name {
			kept = append(kept, n)
		}
	}
	if ask {
		kept = append(kept, name)
	}
	db.Set(askInChatToolsTable, key, kept)
	// The legacy flag is fleet-wide in meaning, so only a change to the
	// FLEET-wide mark touches it. Clearing one agent's mark must not silently
	// clear a mark that binds every agent - that is a narrowing nobody asked
	// for, on agents the owner was not looking at.
	if agentID != "" {
		return true
	}
	list := LoadPersistentTempTools(db, username)
	for i := range list {
		if list[i].Tool.Name == name && list[i].Tool.ConfirmInChat != ask {
			list[i].Tool.ConfirmInChat = ask
			db.Set(persistentTempToolsTable, username, list)
			break
		}
	}
	return true
}

func SetUserToolConfirmInChat(db Database, username, name string, confirm bool) bool {
	db = tempToolStore(db)
	if db == nil || username == "" {
		return false
	}
	tempToolPersistMu.Lock()
	defer tempToolPersistMu.Unlock()
	list := LoadPersistentTempTools(db, username)
	for i := range list {
		if list[i].Tool.Name == name {
			list[i].Tool.ConfirmInChat = confirm
			db.Set(persistentTempToolsTable, username, list)
			return true
		}
	}
	return false
}

// sameStringSet reports whether a and b hold the same strings, in any order.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		if seen[x] == 0 {
			return false
		}
		seen[x]--
	}
	return true
}
