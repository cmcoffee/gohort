package orchestrate

// The chat of an app whose conversation runs on an orchestrate agent.
//
// Scribe, Anvil, Agents and the other apps that put an orchestrate agent
// behind their chat each wired the panel by hand, and each picked a
// different subset: edit and retry on a message, rename, rejoining a turn
// after a reload, tool confirmations, question cards, the guard notices.
// Every one of those is a URL on the panel, and a URL left off is a control
// that is not there, with nothing to say so. A feature added to orchestrate's
// chat then reached none of them.
//
// AppChat is the one wiring. The app keeps what is its own: the send, which
// carries the app's tools, and its labels and fields. Everything else is
// filled in by AppChat.Panel (agenthost.Chat) and answered by ServeAppChat.

import (
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/agenthost"
)

// AppChat is agenthost.Chat, which apps outside orchestrate use to reach it.
type AppChat = agenthost.Chat

// ServeAppChat answers one of the endpoints AppChat.Panel names, for agent.
// sub is the request path relative to the app (so "chat/sessions/x" or
// "api/runs/x/stream"), and listScope is what the session list is filtered
// to, "" for all of the caller's sessions with the agent. It reports false
// for a path that is not one of them, which the app routes itself; the send
// is always the app's own.
func (T *OrchestrateApp) ServeAppChat(w http.ResponseWriter, r *http.Request, a agenthost.Agent, c AppChat, sub, listScope string) bool {
	agent := appRecord(a)
	switch {
	case sub == agenthost.ChatRunsPath+"active":
		T.PublicHandleRunsActive(w, r)
		return true
	case strings.HasPrefix(sub, agenthost.ChatRunsPath):
		// The dispatcher checks the caller owns the run.
		T.PublicHandleRunsDispatch(w, r)
		return true
	case !strings.HasPrefix(sub, c.Prefix):
		return false
	}
	rest := strings.TrimPrefix(sub, c.Prefix)
	switch {
	case rest == "cancel":
		T.PublicHandleCancel(w, r, a)
	case rest == "confirm":
		T.PublicHandleConfirm(w, r)
	case rest == "inject":
		T.PublicHandleInject(w, r)
	case rest == "settings" || rest == "settings/data" || rest == "settings/reset":
		T.serveAppAgentSettings(w, r, agent, c, rest)
	case rest == "sessions":
		T.PublicHandleSessionListFor(w, r, agent.ID, listScope)
	case strings.HasPrefix(rest, "sessions/"):
		T.serveAppChatSession(w, r, agent, strings.TrimPrefix(rest, "sessions/"))
	default:
		return false
	}
	return true
}

// serveAppChatSession answers sessions/<id>[/...].
func (T *OrchestrateApp) serveAppChatSession(w http.ResponseWriter, r *http.Request, agent AgentRecord, rest string) {
	sid, tail, _ := strings.Cut(rest, "/")
	switch {
	case tail == "" || tail == "export":
		T.PublicHandleSessionOne(w, r, agent.ID, rest)
	case tail == "rename":
		T.PublicHandleSessionRename(w, r, agent.ID)
	case tail == "diagnostics":
		T.PublicHandleSessionDiag(w, r, agent.ID, sid)
	case strings.HasPrefix(tail, "blocks/") && strings.HasSuffix(tail, "/resolve"):
		block := strings.TrimSuffix(strings.TrimPrefix(tail, "blocks/"), "/resolve")
		if block == "" || strings.Contains(block, "/") {
			http.NotFound(w, r)
			return
		}
		T.PublicHandleBlockResolve(w, r, agent.ID, sid, block)
	default:
		http.NotFound(w, r)
	}
}
