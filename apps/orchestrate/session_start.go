// Opening a conversation from somewhere that already knows what it is
// about.
//
//	POST /orchestrate/api/sessions/start
//	     {"agent":"<id-or-name>", "message":"…", "title":"…"}
//	  → {"session":"<id>", "agent":"<id>", "url":"/orchestrate/?agent=…&session=…"}
//
// The case that wanted it: an app finishes processing a bundle and hands
// off to an agent that can answer questions about it. Everything needed
// to start well is known at that moment — which agent, which folder,
// what to ask — and none of it survives a plain link to the chat page.
//
// A NEW session every time, deliberately. Two bundles in one thread is
// exactly the cross-contamination the investigation workflow's memory
// settings exist to prevent: a machine's blackboard holds the triage and
// the hypothesis for the life of a session, so a second subject in the
// same conversation inherits the first one's hunch.
//
// The message is STORED, not sent. Sending here would mean running a
// turn with nobody watching it, streaming into a page that is not open
// yet, and deciding what to do with a failure nobody can see. Instead
// the session carries an opening prompt, the chat panel sends it as the
// user's first message on open, and everything downstream — streaming,
// cancel, approval prompts, the machine's first phase — happens exactly
// as it does when a person types it.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxOpeningPrompt bounds what a caller may pre-fill. Generous enough
// for a paragraph of context about what was just processed; finite
// because this is stored on a session record and an unbounded field on a
// record that is read on every turn is a slow leak.
const maxOpeningPrompt = 8000

func (T *OrchestrateApp) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Agent   string `json:"agent"`
		Message string `json:"message"`
		Title   string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ref := strings.TrimSpace(body.Agent)
	if ref == "" {
		http.Error(w, "name the agent to open a session on", http.StatusBadRequest)
		return
	}
	// Name OR id, matching every other surface that takes an agent from a
	// caller: an app authoring a link knows the agent by the name its
	// author typed, not by the id the store assigned.
	agent, found := findAgentByNameOrID(udb, user, ref)
	if !found {
		http.Error(w, "no agent called "+strconv.Quote(ref)+" you can reach", http.StatusNotFound)
		return
	}

	msg := strings.TrimSpace(body.Message)
	if len(msg) > maxOpeningPrompt {
		msg = msg[:maxOpeningPrompt]
	}
	sess := ChatSession{
		ID:            "s" + generateRunID(),
		AgentID:       agent.ID,
		Title:         strings.TrimSpace(body.Title),
		Created:       time.Now(),
		LastAt:        time.Now(),
		OpeningPrompt: msg,
	}
	if _, err := saveChatSession(udb, sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	Log("[orchestrate.sessions] %s opened session %s on %s%s", user, sess.ID, chFirst(agent.Name, agent.ID),
		chOpeningNote(msg))

	writeJSON(w, map[string]any{
		"session": sess.ID,
		"agent":   agent.ID,
		// A ready-made link, because every caller of this would otherwise
		// build the same string and one of them would get the escaping
		// wrong.
		"url": T.WebPath() + "/?agent=" + url.QueryEscape(agent.ID) + "&session=" + url.QueryEscape(sess.ID),
	})
}

// chOpeningNote keeps the opening prompt OUT of the log while still
// saying whether there was one. The text is a user's question about
// their own data; the fact of it is what an operator needs.
func chOpeningNote(msg string) string {
	if msg == "" {
		return " (empty, waiting for them to type)"
	}
	return " with an opening prompt"
}
