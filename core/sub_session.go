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
//   sync   — host blocks on the run; reply integrates into the
//            host's own turn output.
//   async  — host returns immediately; sub-agent runs in a goroutine
//            and posts its reply back to the chat as a separate
//            message when done.
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

// SubSession is the lifecycle index record. It does NOT carry the
// sub-agent's message history — that lives in the per-app storage
// (orchestrate's ChatSession, phantom's dispatchResult, etc.) and
// stays there. SubSession just tracks "is this sub-agent joinable
// right now and when was its last reply?"
type SubSession struct {
	SubSessionID    string           `json:"sub_session_id"`               // stable storage key; caller-supplied
	HostSessionID   string           `json:"host_session_id"`              // parent session this sub-agent belongs to
	HostApp         string           `json:"host_app,omitempty"`           // "orchestrate" / "phantom" / etc.
	AgentID         string           `json:"agent_id"`                     // target agent's record ID
	AgentName       string           `json:"agent_name,omitempty"`         // for log lines / UI; not load-bearing
	OwnerUser       string           `json:"owner_user,omitempty"`         // user account that minted this dispatch
	Mode            SubSessionMode   `json:"mode"`                         // sync or async
	Status          SubSessionStatus `json:"status"`                       // active / idle / retired
	Started         time.Time        `json:"started"`                      // when the dispatch first ran
	LastReplyAt     time.Time        `json:"last_reply_at,omitempty"`      // most recent sub-agent reply (drives promotion window)
	TurnCount       int              `json:"turn_count,omitempty"`         // follow-up turns served after initial run (caps promotion)
	RetiredAt       time.Time        `json:"retired_at,omitempty"`         // when the record left the promotion pool
	RetiredReason   string           `json:"retired_reason,omitempty"`     // "window" / "cap" / "explicit" / "host_close"
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
