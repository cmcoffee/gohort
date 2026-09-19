package orchestrate

// The Notifications page: what the agents needed to say, and where it goes.
//
// One-way and one row per thing, with repeats folded into a count. See
// core/notices for why the count is the feature and notifications.go for who
// writes them.

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// handleConsoleNotifications lists an owner's notices.
func (T *OrchestrateApp) handleConsoleNotifications(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Field order matters: the cards layout takes the first non-underscore
	// field as the title, then the detail, then the pill.
	type row struct {
		What   string `json:"What"`
		Detail string `json:"Detail,omitempty"`
		Status string `json:"Status,omitempty"`
		When   string `json:"When,omitempty"`
		ID     string `json:"_id"`
		Unread bool   `json:"_unread,omitempty"`
	}
	out := []row{}
	for _, n := range notices.List(RootDB, user) {
		who := n.Agent
		if rec, found := loadAgent(udb, n.Agent); found && rec.Name != "" {
			who = rec.Name
		}
		what := n.Title
		if who != "" {
			what = who + ": " + n.Title
		}
		// The count is stated only when it is more than one. "1 time" is noise
		// on every row for the sake of the few that are chronic, and a reader
		// scanning for the chronic ones wants them to stand out.
		status := noticeStatusLabel(n.Kind)
		if n.Count > 1 {
			status += fmt.Sprintf(" · %d times", n.Count)
		}
		out = append(out, row{
			What: what, Detail: n.Body, Status: status,
			When: n.Last.In(UserLocation(user)).Format("Jan 2 3:04 PM"),
			ID:   n.ID, Unread: !n.Read,
		})
	}
	writeJSON(w, out)
}

// noticeStatusLabel says what the reader has to decide, which is the only thing
// the kinds are for.
func noticeStatusLabel(kind string) string {
	switch kind {
	case notices.KindBlocked:
		return "Waiting on you"
	case notices.KindStopped:
		return "Stopped"
	default:
		return "Note"
	}
}

// handleConsoleNotificationRead marks one notice read, or all of them.
//
// Marking read does not reset the count. How often a thing has happened stays
// true after you have read about it, and clearing it would throw away the one
// piece of evidence that says chronic rather than blip.
func (T *OrchestrateApp) handleConsoleNotificationRead(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		notices.MarkRead(RootDB, user, id)
	} else {
		notices.MarkAllRead(RootDB, user)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleNotificationDismiss deletes one notice. It comes back if the
// thing happens again, which is right for a condition and is why this is not a
// way to silence anything.
func (T *OrchestrateApp) handleConsoleNotificationDismiss(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	notices.Remove(RootDB, user, id)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleNotificationForward reads or sets where notifications go beyond
// being kept.
//
//	GET  -> {"where": "" | "email" | "phone" | "both", "email_ok": bool, "phone_ok": bool}
//	POST ?where=...
//
// It reports whether each transport is actually available, because an option
// that silently does nothing is worse than one that is not offered: the owner
// turns it on, believes they will be told, and is not.
func (T *OrchestrateApp) handleConsoleNotificationForward(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		phoneOK := false
		if link, up := ActiveMessagingLink(); up {
			_, phoneOK = link.OwnerHandle(user)
		}
		writeJSON(w, map[string]any{
			"where": AuthGetNotifyForward(AuthDB(), user),
			// Mail only reaches a username that IS an address, which is the
			// existing contract of NotifyUser everywhere else in the app.
			"email_ok": EmailConfigured() && strings.Contains(user, "@"),
			"phone_ok": phoneOK,
		})
	case http.MethodPost:
		AuthSetNotifyForward(AuthDB(), user, strings.TrimSpace(r.URL.Query().Get("where")))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
