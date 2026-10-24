package orchestrate

import (
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// cortexSessionID is the session id of a channel agent's persistent home
// thread. Per-agent so every channel agent has its own ongoing conversation.
// The client gets this value (per agent) from the host page, so it never
// hardcodes the scheme. (The retired Operator's pre-split "operator-thread"
// bridge was removed when the Operator seed was dropped — see
// dropLegacyOperator.)
func cortexSessionID(agentID string) string {
	return "channel:" + agentID
}

// cortexWriteRefusal is what a web write into a cortex is told.
const cortexWriteRefusal = "The cortex is read-only here: it holds what reaches the agent (messages, scheduled runs, monitor fires) and its own notes. Start a new session to talk to it."

// refusesWebWrite reports whether a message sent from the web UI must be
// refused because it would land in the agent's cortex. The one exception is
// an answer: the thread's last message is the agent asking the person
// something (ask_user or ask_user_form), and a run waiting on that answer
// would otherwise be stuck.
func refusesWebWrite(agentID, sessionID string, sess ChatSession) bool {
	if sessionID == "" || sessionID != cortexSessionID(agentID) {
		return false
	}
	return !awaitsAnswer(sess)
}

// awaitsAnswer reports whether a session's last message is the agent asking
// the person a question it has not had an answer to yet.
func awaitsAnswer(sess ChatSession) bool {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		m := sess.Messages[i]
		if m.Role == "user" {
			return false
		}
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Name == "ask_user" || tc.Name == "ask_user_form" {
				return true
			}
		}
		return false
	}
	return false
}

// Every scheduled thing reports into its agent's cortex: a monitor's fire, a
// standing agent's per-run report, a recurring task's conversation.
//
// There was a choice per schedule, "Where" on the Scheduler row and a surface
// argument on the tools: the cortex, the session the schedule was made in, or
// nowhere. The session's only use was the context of the conversation that
// made the schedule, and a schedule outlives it: weeks later a recurring task
// was running on a stale chat and burying it in fires. The schedule's own
// message is what carries what it needs. Nowhere left a schedule that texted
// out at 9am with no record of what it sent.
//
// The stored Surface field is no longer read, so a schedule made under any
// choice reports into the cortex from its next fire. An agent without a
// cortex gets one at its first fire (ensureScheduleCortex), the way attaching
// a channel gives it one.

// scheduleSession is the session a scheduled thing's fire lands in: the
// agent's cortex. home, the session it was made in, stands in only for a
// record that names no agent.
func scheduleSession(agentID, home string) string {
	if strings.TrimSpace(agentID) == "" {
		return home
	}
	return cortexSessionID(agentID)
}

// ensureScheduleCortex gives a top-level agent a cortex for its schedules to
// report into. A sub-agent has none of its own: its schedules are folded onto
// its parent (agent_family.go).
func (T *OrchestrateApp) ensureScheduleCortex(owner, agentID string) {
	if strings.TrimSpace(agentID) == "" {
		return
	}
	if udb := UserDB(T.DB, owner); udb != nil && topAgentOf(udb, agentID) == agentID {
		T.ensureAgentCortex(owner, agentID)
	}
}

// scheduleSourceNote is the line a fire carries linking the session its
// schedule was made in, so the cortex reads back to the conversation that
// asked for it. "" when it was made outside a session or in the cortex.
func (T *OrchestrateApp) scheduleSourceNote(udb Database, agentID, home string) string {
	home = strings.TrimSpace(home)
	if udb == nil || home == "" || strings.TrimSpace(agentID) == "" || home == cortexSessionID(agentID) {
		return ""
	}
	s, ok := loadChatSession(udb, agentID, home)
	if !ok {
		return ""
	}
	title := strings.NewReplacer("[", "(", "]", ")", "\n", " ").Replace(strings.TrimSpace(s.Title))
	if title == "" {
		title = "the conversation it was made in"
	}
	link := strings.TrimSuffix(T.WebPath(), "/") + "/?agent=" + url.QueryEscape(agentID) + "&session=" + url.QueryEscape(home)
	return "Scheduled from [" + title + "](" + link + ")."
}
