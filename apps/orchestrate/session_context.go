package orchestrate

// The context view — what a persistent thread actually carries.
//
// A long-lived thread (a cortex, a channel room, any conversation past the
// fold trigger) does not hand the model its whole history. It hands a rolling
// summary of the older part plus a verbatim tail, and the fold that moves a
// span from one to the other ran in the background with a server log line
// as its only record. So "why doesn't it remember what I said an hour ago"
// had no answer anywhere a person looks: the summary the agent was reading
// from was stored and rendered nowhere.
//
// This is that answer, behind the same status pill the machine drawer uses:
// how many folds, how much is summarized versus verbatim, and the summary
// itself. Read-only — the summary is the model's own digest and the fold
// cursor is load-bearing (trim drops only what it covers), so nothing here
// edits either. Clearing a thread is the existing Clear channel action.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// sessionContextView is the drawer's content: numbers first, the summary
// last because it is the long part.
type sessionContextView struct {
	Folds      int    `json:"Folds"`
	Summarized int    `json:"Messages in summary"`
	Verbatim   int    `json:"Messages verbatim"`
	Stored     int    `json:"Messages stored"`
	Note       string `json:"Note,omitempty"`
	Summary    string `json:"Rolling summary,omitempty"`
}

// sessionContextOf builds the view, or nil when the thread has never folded
// — a fresh conversation carries everything verbatim and there is nothing to
// show that the transcript does not already show.
func sessionContextOf(udb Database, sess ChatSession) *sessionContextView {
	st := loadCompactState(udb, sess.AgentID, sess.ID)
	if st.FoldSeq == 0 && strings.TrimSpace(st.Summary) == "" && st.SummarizedThrough == 0 {
		return nil
	}
	stored := len(sess.Messages)
	through := st.SummarizedThrough
	if through > stored {
		through = stored
	}
	v := &sessionContextView{
		Folds:      st.FoldSeq,
		Summarized: through,
		Verbatim:   stored - through,
		Stored:     stored,
		Summary:    strings.TrimSpace(st.Summary),
	}
	v.Note = "The model sees the rolling summary in place of the messages it covers, then the verbatim ones. Folded spans are archived, so recall_history can still reach their exact text."
	if st.FoldSeq > 0 && v.Summary == "" {
		v.Note = "This thread has folded, but the stored summary is empty — the fold's summariser returned nothing. Older turns survive only in the recall archive."
	}
	return v
}

// contextStatus is the status pill for a thread with no machine: a fold
// count, and the context view behind it. An empty object — the pill's
// "nothing to say" — until the first fold.
func contextStatus(udb Database, sess ChatSession) map[string]any {
	v := sessionContextOf(udb, sess)
	if v == nil {
		return map[string]any{}
	}
	label := "context: " + strconv.Itoa(v.Folds) + " fold"
	if v.Folds != 1 {
		label += "s"
	}
	title := "This thread has folded " + strconv.Itoa(v.Folds) + " time(s): " + strconv.Itoa(v.Summarized) +
		" older message(s) are carried as a rolling summary and " + strconv.Itoa(v.Verbatim) + " verbatim. Click to read the summary."
	q := "?agent=" + url.QueryEscape(sess.AgentID) + "&session=" + url.QueryEscape(sess.ID)
	return map[string]any{
		"label":      label,
		"title":      title,
		"tone":       "",
		"detail_url": "api/session-context" + q,
	}
}

// handleSessionContext serves the view.
//
//	GET /api/session-context?agent=<id>&session=<id>
func (T *OrchestrateApp) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if agentID == "" || sessionID == "" {
		http.Error(w, "agent and session are required", http.StatusBadRequest)
		return
	}
	sess, found := loadChatSession(udb, agentID, sessionID)
	if !found {
		http.NotFound(w, r)
		return
	}
	v := sessionContextOf(udb, sess)
	if v == nil {
		writeJSON(w, map[string]any{"Note": "This thread has not folded: the model still sees every message verbatim."})
		return
	}
	writeJSON(w, v)
}
