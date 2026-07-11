// Cross-app sub-session lifecycle index.
//
// Apps dispatch to other agents via different surfaces — orchestrate
// has `agents(run)` (inline tool, sync), phantom has dispatch_agent
// (async, default) and dispatch_agent_inline (sync). Each surface
// already has its own storage for the sub-agent's exchange (chat
// sessions, dispatch-result records). What was missing: a uniform
// LIFECYCLE INDEX that lets a host call ResolvePromotion at the top
// of its per-turn handler and find "the sub-agent the user is likely
// following up with."
//
// This file provides that index. SubSession records sit ALONGSIDE the
// existing per-app storage and carry only the lifecycle metadata
// needed to drive promotion: which host, which target, when did it
// last reply, how many follow-up turns has it served, and what's its
// status (active mid-run, idle and joinable, or retired).
//
// Storage:
//   - Lives in RootDB under SubSessionsTable for deployment-wide
//     reach. Promotion lookups go through one index regardless of
//     which app owns the parent session.
//   - Keyed by SubSessionID (caller-supplied stable string like
//     "dispatch:<host>:<target>" or "phantom:<chat>:<target>").
//   - HostSessionID is the column you query on for "find me an idle
//     sub-session to promote for THIS host."
//
// Lifecycle (Status field):
//
//   active   — sub-agent is currently running (sync caller is
//              blocked on it, or async goroutine is in flight).
//   idle     — sub-agent has produced a reply and is joinable; a
//              user follow-up within the stickiness window will be
//              routed back into this sub-session synchronously.
//   retired  — out of the promotion pool. Either the stickiness
//              window elapsed, the turn cap was hit, or the user
//              explicitly ended the side-conversation. Record stays
//              for audit / inspection but never intercepts again.

package core

import (
	"sort"
	"sync"
	"time"
)

// SubSessionsTable is the deployment-wide table holding sub-session
// lifecycle records. One row per (host, target) dispatch pair.
const SubSessionsTable = "sub_sessions"

// SubSessionStatus describes where a dispatched sub-agent sits in
// the promotion lifecycle. See file header for the state machine.
type SubSessionStatus string

const (
	SubSessionActive  SubSessionStatus = "active"
	SubSessionIdle    SubSessionStatus = "idle"
	SubSessionRetired SubSessionStatus = "retired"
)

// SubSessionMode describes how this sub-session was invoked.
//
//	sync   — host blocks on the run; reply integrates into the
//	         host's own turn output.
//	async  — host returns immediately; sub-agent runs in a goroutine
//	         and posts its reply back to the chat as a separate
//	         message when done.
//
// Sync dispatches typically transition straight active → retired
// (the host's turn was the only consumer). Async dispatches go
// active → idle (joinable for follow-up) and then retired when the
// promotion window closes or the cap trips.
type SubSessionMode string

const (
	SubSessionModeSync  SubSessionMode = "sync"
	SubSessionModeAsync SubSessionMode = "async"
)

// SubSessionKind separates "follow-up" sub-sessions (the default —
// promotion/injection semantics, a host-persona conversation the user
// drifts back into) from self-driving BACKGROUND tasks that own their
// own turn loop and must never be promoted to the host persona.
//
//	normal     — promotion router applies: an idle record is joinable
//	             via RoutePromote, ages out of the stickiness window,
//	             and falls through to the host's main LLM.
//	autonomous — a long-lived task that drives its OWN runner when a
//	             message arrives (RouteGoal) and is NEVER handed to the
//	             host persona. It is exempt from the promotion-window /
//	             turn-cap auto-retirement, because it spends most of its
//	             life idle waiting on an external party (a human texting
//	             back) — five minutes is the wrong yardstick. Retirement
//	             is the task's own responsibility (it finishes, hits its
//	             cap, or a timeout sweep retires it).
//
// Kept generic on purpose: core never names the app or the task. Phantom's
// operator-goal conversations are the first user; any later background-task
// surface reuses the same kind. See ResolveDispatchRoute.
type SubSessionKind string

const (
	SubSessionKindNormal     SubSessionKind = ""
	SubSessionKindAutonomous SubSessionKind = "autonomous"
)

// SubSession is the lifecycle index record. It does NOT carry the
// sub-agent's message history — that lives in the per-app storage
// (orchestrate's ChatSession, phantom's dispatchResult, etc.) and
// stays there. SubSession just tracks "is this sub-agent joinable
// right now and when was its last reply?"
type SubSession struct {
	SubSessionID  string           `json:"sub_session_id"`           // stable storage key; caller-supplied
	HostSessionID string           `json:"host_session_id"`          // parent session this sub-agent belongs to
	HostApp       string           `json:"host_app,omitempty"`       // "orchestrate" / "phantom" / etc.
	AgentID       string           `json:"agent_id"`                 // target agent's record ID
	AgentName     string           `json:"agent_name,omitempty"`     // for log lines / UI; not load-bearing
	OwnerUser     string           `json:"owner_user,omitempty"`     // user account that minted this dispatch
	Mode          SubSessionMode   `json:"mode"`                     // sync or async
	Kind          SubSessionKind   `json:"kind,omitempty"`           // "" normal (promotion); "autonomous" self-driving background task
	Status        SubSessionStatus `json:"status"`                   // active / idle / retired
	Started       time.Time        `json:"started"`                  // when the dispatch first ran
	LastReplyAt   time.Time        `json:"last_reply_at,omitempty"`  // most recent sub-agent reply (drives promotion window)
	TurnCount     int              `json:"turn_count,omitempty"`     // follow-up turns served after initial run (caps promotion)
	RetiredAt     time.Time        `json:"retired_at,omitempty"`     // when the record left the promotion pool
	RetiredReason string           `json:"retired_reason,omitempty"` // "window" / "cap" / "explicit" / "host_close"
}

// Sub-session storage is read on every host turn (for ResolvePromotion)
// and mutated on every state transition; a mutex serializes the RMW
// path so two concurrent dispatches from the same host can't lose
// updates to a race.
var subSessionMu sync.Mutex

// MintSubSession initializes a fresh sub-session record in active
// state. Apps call this at the start of a dispatch — before the
// agent loop runs — so any concurrent host turn that calls
// ResolvePromotion mid-dispatch sees an active (NOT idle) record and
// doesn't try to promote it. The caller picks the SubSessionID; the
// recommended shape is "<app>:<host>:<target>" so dispatches from
// the same (host, target) pair reuse the same record slot.
//
// Returns the persisted record (with Started stamped) so callers
// don't have to hold their own copy.
func MintSubSession(s SubSession) SubSession {
	if RootDB == nil || s.SubSessionID == "" {
		return s
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	now := time.Now()
	// Preserve identity + lifecycle counters if this dispatch slot
	// has been used before — repeated dispatches to the same target
	// build on prior context, including the follow-up turn counter
	// that drives the promotion cap. A re-mint on a promotion path
	// must NOT reset TurnCount, otherwise the cap never fires.
	var existing SubSession
	hasExisting := RootDB.Get(SubSessionsTable, s.SubSessionID, &existing)
	if hasExisting && !existing.Started.IsZero() {
		s.Started = existing.Started
		s.TurnCount = existing.TurnCount
	} else {
		s.Started = now
		s.TurnCount = 0
	}
	if s.Status == "" {
		s.Status = SubSessionActive
	}
	if s.Mode == "" {
		s.Mode = SubSessionModeSync
	}
	s.RetiredAt = time.Time{}
	s.RetiredReason = ""
	RootDB.Set(SubSessionsTable, s.SubSessionID, s)
	return s
}

// MarkSubSessionIdle moves an active sub-session to idle (joinable)
// and stamps LastReplyAt to now. Apps call this when the sub-agent
// has produced its final reply for the current invocation and is
// ready to be re-engaged on a follow-up turn. Returns the updated
// record, or zero-value SubSession if the ID isn't found.
func MarkSubSessionIdle(subSessionID string) SubSession {
	return mutateSubSession(subSessionID, func(s *SubSession) {
		s.Status = SubSessionIdle
		s.LastReplyAt = time.Now()
		// Stale retirement metadata cleared so a re-engagement
		// doesn't carry an "I was retired" tail.
		s.RetiredAt = time.Time{}
		s.RetiredReason = ""
	})
}

// MarkSubSessionActive bumps the record back to active state. Used
// when an idle sub-session gets promoted and the host turn is about
// to start running the sub-agent again. Increments TurnCount so the
// per-session cap eventually trips.
func MarkSubSessionActive(subSessionID string) SubSession {
	return mutateSubSession(subSessionID, func(s *SubSession) {
		s.Status = SubSessionActive
		s.TurnCount++
	})
}

// RetireSubSession moves the record out of the promotion pool. The
// reason string is recorded for observability ("window" / "cap" /
// "explicit" / "host_close"). Idempotent: retiring an already-retired
// record is a no-op.
func RetireSubSession(subSessionID, reason string) SubSession {
	return mutateSubSession(subSessionID, func(s *SubSession) {
		if s.Status == SubSessionRetired {
			return
		}
		s.Status = SubSessionRetired
		s.RetiredAt = time.Now()
		s.RetiredReason = reason
	})
}

// GetSubSession reads a record by its ID. Returns zero-value and
// false when no record exists.
func GetSubSession(subSessionID string) (SubSession, bool) {
	if RootDB == nil || subSessionID == "" {
		return SubSession{}, false
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	var s SubSession
	if RootDB.Get(SubSessionsTable, subSessionID, &s) {
		return s, true
	}
	return SubSession{}, false
}

// IdleSubSessionsFor returns every idle (joinable) sub-session
// belonging to a host, sorted by LastReplyAt descending (most-recent
// first). Promotion picks the head of this list when one exists.
// Empty result when no idle sub-sessions are joinable for this host.
func IdleSubSessionsFor(hostSessionID string) []SubSession {
	if RootDB == nil || hostSessionID == "" {
		return nil
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	var out []SubSession
	for _, k := range RootDB.Keys(SubSessionsTable) {
		var s SubSession
		if !RootDB.Get(SubSessionsTable, k, &s) {
			continue
		}
		if s.HostSessionID != hostSessionID || s.Status != SubSessionIdle {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastReplyAt.After(out[j].LastReplyAt)
	})
	return out
}

// SubSessionLivenessChecker reports whether a given sub-session is
// being actively served by a live in-process worker (goroutine, sync
// caller, etc.). Apps register a checker at startup so the routing
// layer can distinguish "the persisted Active status reflects reality"
// from "the goroutine that owned this died with the process / panicked
// in a corner / hung in an unrecoverable way." The persisted record
// can't tell the difference on its own — only the in-process owner can.
//
// Return true when the sub-session has a live worker; false when the
// caller knows there's no goroutine for it (typical case: an in-memory
// cancel/dispatch registry has no entry for the ID).
//
// Multiple checkers may register; ResolveDispatchRoute treats a
// sub-session as live if ANY registered checker says yes. With no
// checkers registered, every Active record is assumed live — same as
// the pre-checker behavior, so apps that don't opt in see no change.
type SubSessionLivenessChecker func(subSessionID string) bool

var (
	livenessMu       sync.RWMutex
	livenessCheckers []SubSessionLivenessChecker
)

// RegisterSubSessionLivenessChecker plugs an app's liveness check
// into the routing path. Typically called from an app's RegisterRoutes
// once its dispatch tracking is initialized.
func RegisterSubSessionLivenessChecker(fn SubSessionLivenessChecker) {
	if fn == nil {
		return
	}
	livenessMu.Lock()
	defer livenessMu.Unlock()
	livenessCheckers = append(livenessCheckers, fn)
}

// subSessionIsLive consults every registered checker. When no
// checkers are registered, returns true (defensive: assume the
// persisted record is honest). When checkers ARE registered, returns
// true iff at least one says yes — so an unowned sub-session can be
// safely declared dead without the app having to know about peers.
func subSessionIsLive(subSessionID string) bool {
	livenessMu.RLock()
	defer livenessMu.RUnlock()
	if len(livenessCheckers) == 0 {
		return true
	}
	for _, fn := range livenessCheckers {
		if fn(subSessionID) {
			return true
		}
	}
	return false
}

// RetireOrphanedActiveSubSessions walks every persisted SubSession
// and retires anything in Active state. Used at startup to scrub
// records left behind by a prior process whose goroutines died with
// it — without this, the first routing decision after a restart for
// each affected chat would route as inject (because the persisted
// status still reads Active) and ack forever, even though no
// goroutine exists to consume the inject queue.
//
// Returns the number of records retired. Apps that own sub-sessions
// should call this from their RegisterRoutes (or similar startup
// hook) BEFORE any inbound traffic starts flowing.
func RetireOrphanedActiveSubSessions() int {
	if RootDB == nil {
		return 0
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	now := time.Now()
	n := 0
	for _, k := range RootDB.Keys(SubSessionsTable) {
		var s SubSession
		if !RootDB.Get(SubSessionsTable, k, &s) {
			continue
		}
		if s.Status != SubSessionActive {
			continue
		}
		s.Status = SubSessionRetired
		s.RetiredAt = now
		s.RetiredReason = "orphaned_at_startup"
		RootDB.Set(SubSessionsTable, k, s)
		n++
	}
	return n
}

// ActiveSubSessionsFor returns every active (in-flight) sub-session
// belonging to a host. Used by cancellation — a "/stop" needs to find
// the running dispatches for a chat so it can cancel each. Empty when
// nothing is running for this host.
func ActiveSubSessionsFor(hostSessionID string) []SubSession {
	if RootDB == nil || hostSessionID == "" {
		return nil
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	var out []SubSession
	for _, k := range RootDB.Keys(SubSessionsTable) {
		var s SubSession
		if !RootDB.Get(SubSessionsTable, k, &s) {
			continue
		}
		if s.HostSessionID == hostSessionID && s.Status == SubSessionActive {
			out = append(out, s)
		}
	}
	return out
}

// mutateSubSession is the locked read-modify-write helper backing
// every transition function. Returns the post-mutation record; if no
// record existed, returns a zero-value SubSession unchanged.
func mutateSubSession(subSessionID string, fn func(*SubSession)) SubSession {
	if RootDB == nil || subSessionID == "" {
		return SubSession{}
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	var s SubSession
	if !RootDB.Get(SubSessionsTable, subSessionID, &s) {
		return SubSession{}
	}
	fn(&s)
	RootDB.Set(SubSessionsTable, subSessionID, s)
	return s
}
