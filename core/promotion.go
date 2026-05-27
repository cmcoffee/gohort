// Sub-session promotion router.
//
// When a host (phantom chat, orchestrate chat, …) just dispatched
// to a sub-agent and got a reply, the user's NEXT turn is usually a
// follow-up to that reply — not a fresh ask to the host. Promotion
// detects this case and routes the next turn synchronously through
// the sub-agent the user is talking to.
//
// Apps call ResolvePromotion at the top of their per-turn handler.
// When it returns a non-nil SubSession, the app routes the user's
// message through that sub-session (calling the sub-agent directly)
// instead of invoking its own main LLM. The sub-agent's reply lands
// in the chat as if the host produced it.
//
// Promotion is OPT-IN per host call site — apps that don't want this
// behavior just don't call ResolvePromotion. Sub-sessions that
// haven't been marked idle (still active, or already retired) never
// promote regardless.
//
// Termination is enforced inside ResolvePromotion itself: any idle
// sub-session whose stickiness window has elapsed or whose follow-up
// turn cap was hit gets auto-retired here before the promotion
// lookup returns. That keeps the "should this be retired?" check in
// one place rather than scattered across every host handler.

package core

import (
	"strings"
	"time"
)

// StripPromotionEscape detects "/back" or "/phantom" at the start of
// a user message and returns the message with the prefix stripped
// along with a flag indicating an escape was requested. Apps use
// this to let users deliberately break out of a promoted
// side-conversation back to the host agent.
//
// "/back" and "/phantom" are aliases — "/phantom" reads natural for
// chat surfaces named that way, but "/back" is the generic form.
// Both work everywhere. The prefix must be followed by end-of-message
// or whitespace/punctuation so "/backup the database" doesn't match.
func StripPromotionEscape(msg string) (string, bool) {
	for _, prefix := range []string{"/back", "/phantom"} {
		if !strings.HasPrefix(strings.ToLower(msg), prefix) {
			continue
		}
		rest := msg[len(prefix):]
		if len(rest) == 0 {
			return "", true
		}
		c := rest[0]
		if c == ' ' || c == '\n' || c == '\t' || c == ',' || c == '.' || c == ':' {
			return strings.TrimSpace(rest), true
		}
	}
	return msg, false
}

// PromotionWindow is the default stickiness window: how long an idle
// sub-session stays joinable after its last reply. Beyond this, the
// next user turn falls through to the host's main LLM and the
// sub-session retires.
//
// 5 minutes hits the sweet spot for chat-style follow-ups — users
// typically reply within seconds-to-minutes of a sub-agent's reply,
// and "I'll get back to it tomorrow" almost always indicates a
// topic shift that shouldn't be pulled back into the prior context.
const PromotionWindow = 5 * time.Minute

// PromotionTurnCap is the maximum number of follow-up turns a single
// sub-session will serve. Beyond this, the user is firmly in a
// sub-agent conversation and should switch contexts deliberately
// (re-dispatch with a fresh brief) rather than drift indefinitely.
//
// 8 turns is generous enough for a meaningful back-and-forth without
// crowding out the host's primary role.
const PromotionTurnCap = 8

// RouteAction is the tri-state result from ResolveDispatchRoute,
// telling the host how to handle the next user turn given the state
// of its currently-tracked sub-sessions.
type RouteAction int

const (
	// RouteNone — no sub-session intercepts this turn. Host runs its
	// own main LLM as usual.
	RouteNone RouteAction = iota

	// RoutePromote — an IDLE sub-session is joinable. Host should
	// load the sub-agent's prior messages, append the new user turn,
	// run the sub-agent loop synchronously, and post its reply as
	// the host's reply. The host's main LLM is bypassed this turn.
	RoutePromote

	// RouteInject — an ACTIVE sub-session is currently running.
	// Host should push the user's message into the sub-session's
	// injection queue (via LookupSubSessionInjectionQueue + Push)
	// and return a short ack to the user. The sub-agent loop will
	// drain the note between rounds and integrate it into its work.
	RouteInject
)

// ResolveDispatchRoute is the tri-state successor to the simpler
// ResolvePromotion. Returns the sub-session that should handle this
// turn (or nil) along with the RouteAction the host should take.
//
// Precedence: active > idle. If any active sub-session exists for
// this host, the user's incoming message is treated as a mid-flight
// addition (RouteInject) — the running sub-agent gets it. Otherwise
// the most-recent idle sub-session inside the stickiness window
// wins (RoutePromote). Otherwise RouteNone.
//
// Side effects: idle sub-sessions that have aged out of the
// stickiness window or hit the follow-up cap are auto-retired here
// before the decision is returned.
//
// Caller pattern (from a host's per-turn intake):
//
//	sub, action := core.ResolveDispatchRoute(hostSessionID)
//	switch action {
//	case core.RouteInject:
//	    q := core.LookupSubSessionInjectionQueue(sub.SubSessionID)
//	    if q != nil { q.Push(userMessage) }
//	    return shortAck()
//	case core.RoutePromote:
//	    core.MarkSubSessionActive(sub.SubSessionID)
//	    reply := runSubAgentInline(sub, userMessage)
//	    core.MarkSubSessionIdle(sub.SubSessionID)
//	    return reply
//	case core.RouteNone:
//	    // fall through to the host's main LLM
//	}
func ResolveDispatchRoute(hostSessionID string) (*SubSession, RouteAction) {
	if hostSessionID == "" {
		return nil, RouteNone
	}
	// Active sub-sessions take precedence — a user message arriving
	// while something is running is most-likely additional context
	// for that running thing.
	if active := mostRecentActive(hostSessionID); active != nil {
		return active, RouteInject
	}
	idle := IdleSubSessionsFor(hostSessionID)
	if len(idle) == 0 {
		return nil, RouteNone
	}
	now := time.Now()
	for _, s := range idle {
		if shouldRetire(s, now) {
			RetireSubSession(s.SubSessionID, retireReason(s, now))
			continue
		}
		return &s, RoutePromote
	}
	return nil, RouteNone
}

// ResolvePromotion is a thin wrapper over ResolveDispatchRoute that
// returns only the idle-promotion result (nil for active or no
// route). Use ResolveDispatchRoute when the caller wants to handle
// the mid-flight injection case too.
func ResolvePromotion(hostSessionID string) *SubSession {
	sub, action := ResolveDispatchRoute(hostSessionID)
	if action == RoutePromote {
		return sub
	}
	return nil
}

// mostRecentActive returns the most-recently-started active
// sub-session for a host, or nil if none. Picks the latest start
// time so a host with concurrent active dispatches (parallel async
// fan-out) routes the next user message to the one most likely
// relevant — the one the user just kicked off.
func mostRecentActive(hostSessionID string) *SubSession {
	if RootDB == nil {
		return nil
	}
	subSessionMu.Lock()
	defer subSessionMu.Unlock()
	var best *SubSession
	for _, k := range RootDB.Keys(SubSessionsTable) {
		var s SubSession
		if !RootDB.Get(SubSessionsTable, k, &s) {
			continue
		}
		if s.HostSessionID != hostSessionID || s.Status != SubSessionActive {
			continue
		}
		if best == nil || s.Started.After(best.Started) {
			scopy := s
			best = &scopy
		}
	}
	return best
}

// shouldRetire reports whether an idle SubSession has aged out of
// the promotion pool. Either the stickiness window has elapsed since
// the last reply, or the follow-up turn cap was hit on a previous
// promotion (the cap check fires AFTER an MarkSubSessionActive bumps
// TurnCount; this re-check catches it on the next intake).
func shouldRetire(s SubSession, now time.Time) bool {
	if !s.LastReplyAt.IsZero() && now.Sub(s.LastReplyAt) > PromotionWindow {
		return true
	}
	if s.TurnCount >= PromotionTurnCap {
		return true
	}
	return false
}

// retireReason picks the audit string for an auto-retirement. Order
// matters: window timeout is the common case, cap is the explicit
// hand-off case.
func retireReason(s SubSession, now time.Time) string {
	if !s.LastReplyAt.IsZero() && now.Sub(s.LastReplyAt) > PromotionWindow {
		return "window"
	}
	if s.TurnCount >= PromotionTurnCap {
		return "cap"
	}
	return "stale"
}
