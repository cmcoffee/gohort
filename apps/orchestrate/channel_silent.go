// Recorded-only channel messages. When the wake-rule gatekeeper BLOCKS an
// inbound (channel_gatekeeper.go), the transport records it to its own shared
// log but the bound agent never runs — so the message historically never
// entered the agent's OWN transcript (Store B), and the agent woke amnesiac
// about anything said while it stayed silent. This mirrors each blocked inbound
// into the agent's ChatSession so it shows in the agent's chat immediately and
// is in-context on the next wake. The transport reaches this through the
// core.RegisterChannelSilentRecorder seam (same shape as the gatekeeper seam).
//
// The write shares a per-session append lock with the dispatch runner's final
// persist (RunAgentSyncContinuingRich), so a message that lands mid-run isn't
// clobbered by the run's stale in-memory copy on save (a load→append→save race).

package orchestrate

import (
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// sessionAppendMu serializes short load→append→save critical sections on a
// single ChatSession, keyed by agentID+sessionID. It is NOT held across a whole
// run — only around each append — so concurrent dispatches on OTHER sessions are
// unaffected and a run never blocks reads.
var (
	sessionAppendMu    = map[string]*sync.Mutex{}
	sessionAppendMapMu sync.Mutex
)

func sessionAppendLock(agentID, sessionID string) *sync.Mutex {
	key := agentID + "\x00" + sessionID
	sessionAppendMapMu.Lock()
	mu := sessionAppendMu[key]
	if mu == nil {
		mu = &sync.Mutex{}
		sessionAppendMu[key] = mu
	}
	sessionAppendMapMu.Unlock()
	return mu
}

// withSessionAppend runs fn while holding the per-session append lock.
func withSessionAppend(agentID, sessionID string, fn func()) {
	mu := sessionAppendLock(agentID, sessionID)
	mu.Lock()
	defer mu.Unlock()
	fn()
}

// overflowChannelReply routes an agent's UNDELIVERABLE channel reply (a
// bidirectional channel bound to a service with no output path) to a surface the
// owner will see, instead of stranding it in an outbox nothing drains. Preferred
// surface is the agent's cortex feed (a non-triggering awareness card); when
// cortex is OFF, the reply is already in the agent's session chat (the dispatch
// persisted it there), so we re-surface that thread as unread. Both are passive —
// no turn runs — so there's no reply→observe→reply loop.
func (app *OrchestrateApp) overflowChannelReply(in ChannelInbound, replyText string) {
	replyText = strings.TrimSpace(replyText)
	if replyText == "" || in.AgentID == "" {
		return
	}
	db := UserDB(app.DB, in.Owner)
	if db == nil {
		return
	}
	if ag, ok := loadAgent(db, in.AgentID); ok && ag.Cortex {
		appendCortexObs(db, in.AgentID, channelObsFrom(in), cortexKindOverflow, replyText)
		return
	}
	// Cortex off: the reply is already in the channel session (dispatch persisted
	// it). Re-save to bump LastAt (not LastSeen) so the thread lights UNREAD and the
	// owner notices the reply that never went out.
	sessionID := app.effectiveChannelSession(in.Owner, in.AgentID, in.SessionID)
	withSessionAppend(in.AgentID, sessionID, func() {
		s, ok := loadChatSession(db, in.AgentID, sessionID)
		if !ok || s.ID == "" {
			return
		}
		if _, err := saveChatSession(db, s); err != nil {
			Log("[channel.overflow] WARN failed to surface overflow session agent=%s sub=%s: %v", in.AgentID, sessionID, err)
		}
	})
}

// registerChannelSilentRecorder installs the recorded-only mirror. Call once at
// startup (orchestrate.go).
func registerChannelSilentRecorder(app *OrchestrateApp) {
	RegisterChannelSilentRecorder(func(in ChannelInbound) {
		app.recordChannelSilent(in)
	})
}

// recordChannelSilent appends a gatekeeper-blocked inbound to the bound agent's
// transcript as a plain contact message (no assistant reply follows — the agent
// stayed silent). Stored identically to a woken channel message (Role "user" +
// Sender), so the chat renders the full conversation as one thread and the next
// wake's history build sees the same "Sender: text" line. Bumps LastAt (not
// LastSeen) via saveChatSession, so the session lights unread until reopened.
func (app *OrchestrateApp) recordChannelSilent(in ChannelInbound) {
	if strings.TrimSpace(in.Text) == "" {
		return // media-only inbounds are handled on the wake path; nothing to mirror
	}
	db := UserDB(app.DB, in.Owner)
	if db == nil || in.AgentID == "" {
		return
	}
	// Resolve the same session id the runner and gatekeeper use (a dedicated
	// cortex agent collapses its per-contact id to the cortex session), so the
	// mirror lands in the thread the agent actually reads.
	sessionID := app.effectiveChannelSession(in.Owner, in.AgentID, in.SessionID)
	withSessionAppend(in.AgentID, sessionID, func() {
		s, ok := loadChatSession(db, in.AgentID, sessionID)
		if !ok || s.ID == "" {
			// Thread doesn't exist yet (agent never woken here) — open it with the
			// stable session id so it matches the runner's / chat page's key.
			s.ID = sessionID
			s.AgentID = in.AgentID
			s.Created = time.Now()
		}
		if s.Title == "" {
			s.Title = firstNonEmptyStr(in.ConversationName, in.SenderName)
		}
		s.Messages = append(s.Messages, ChatMessage{
			Role:    "user",
			Content: in.Text,
			Created: time.Now(),
			Sender:  in.SenderName,
		})
		if _, err := saveChatSession(db, s); err != nil {
			Log("[channel.silent] WARN failed to record silent inbound agent=%s sub=%s: %v", in.AgentID, sessionID, err)
		}
	})
}
