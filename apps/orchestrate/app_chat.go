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
// filled in by AppChat.Panel and answered by ServeAppChat.

import (
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"
)

// AppChat is where an app routes its agent chat.
type AppChat struct {
	// Prefix is the app-relative path its chat endpoints live under, such as
	// "chat/". Runs are the exception: they live at "api/runs/", because the
	// run stream finds its id by that segment of the path.
	Prefix string
	// Query is appended to every session URL, carrying the app's own scope
	// placeholders ("guide={scope}", "project_id={project_id}") so the
	// session list follows what the app has open.
	Query string
}

// appChatRuns is the run registry's path under an app. handleRunsDispatch
// reads the run id from the "/api/runs/" segment, so it cannot move.
const appChatRuns = "api/runs/"

// url is an endpoint under the prefix, with the app's query.
func (c AppChat) url(path string) string {
	u := c.Prefix + path
	if c.Query != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + c.Query
	}
	return u
}

// Panel fills p's chat endpoints, leaving any the app set itself.
func (c AppChat) Panel(p ui.AgentLoopPanel) ui.AgentLoopPanel {
	set := func(field *string, v string) {
		if *field == "" {
			*field = v
		}
	}
	set(&p.CancelURL, c.Prefix+"cancel")
	set(&p.ConfirmURL, c.Prefix+"confirm")
	set(&p.InjectURL, c.Prefix+"inject")
	set(&p.ListURL, c.url("sessions"))
	set(&p.LoadURL, c.url("sessions/{id}"))
	set(&p.DeleteURL, c.url("sessions/{id}"))
	// Edit, Retry and Delete on a message: the thread is cut back to it.
	set(&p.TruncateURL, c.url("sessions/{id}"))
	set(&p.RenameURL, c.url("sessions/{id}/rename"))
	// Why a turn did what it did: a denied tool, a guard that stopped it.
	set(&p.DiagnosticsURL, c.url("sessions/{session}/diagnostics"))
	// An answered question card settles instead of replaying on every load.
	set(&p.BlockResolveURL, c.url("sessions/{id}/blocks/{block_id}/resolve"))
	// A reload or a closed tab rejoins the turn still running.
	set(&p.RunsURLBase, appChatRuns)
	return p
}

// ServeAppChat answers one of the endpoints AppChat.Panel names, for agent.
// sub is the request path relative to the app (so "chat/sessions/x" or
// "api/runs/x/stream"), and listScope is what the session list is filtered
// to, "" for all of the caller's sessions with the agent. It reports false
// for a path that is not one of them, which the app routes itself; the send
// is always the app's own.
func (T *OrchestrateApp) ServeAppChat(w http.ResponseWriter, r *http.Request, agent AgentRecord, c AppChat, sub, listScope string) bool {
	switch {
	case sub == appChatRuns+"active":
		T.PublicHandleRunsActive(w, r)
		return true
	case strings.HasPrefix(sub, appChatRuns):
		// The dispatcher checks the caller owns the run.
		T.PublicHandleRunsDispatch(w, r)
		return true
	case !strings.HasPrefix(sub, c.Prefix):
		return false
	}
	rest := strings.TrimPrefix(sub, c.Prefix)
	switch {
	case rest == "cancel":
		T.PublicHandleCancel(w, r, agent)
	case rest == "confirm":
		T.PublicHandleConfirm(w, r)
	case rest == "inject":
		T.PublicHandleInject(w, r)
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
