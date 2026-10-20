package admin

// Admin › Activity: the deployment's activity log (core/activity). Who signed
// in, who failed to and from where, and the changes an administrator wants a
// record of. The live views say what is running now; this says who did what,
// and when, afterwards.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/ui"
)

// activityListLimit is how many events the page loads: the newest ones, which
// is what the log is read for. Filter by kind or user to reach further back.
const activityListLimit = 2000

func (a *AdminApp) activitySections() []ui.Section {
	return []ui.Section{{
		Title:    "Activity Log",
		Subtitle: "Sign-ins, failed sign-ins, and changes to accounts, credentials, peers, approvals and settings, newest first.",
		Detail: "Each event names who acted (for a failed sign-in, the name that was tried), what happened, and the address and browser it came from. " +
			"Events are kept for the number of days set in Tuning › Security › Activity log retention, 90 by default. " +
			"Secrets are never recorded: a saved credential is logged by its name only. Filter the list with the box above it.",
		Body: ui.Table{
			Source:            "api/activity",
			RowKey:            "id",
			Search:            true,
			SearchPlaceholder: "Filter by user, address, or what happened…",
			EmptyText:         "Nothing recorded yet.",
			Columns: []ui.Col{
				{Field: "at", Label: "When", Format: "reltime", Mute: true},
				{Field: "kind", Label: "What", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: activity.SignIn, Label: "Signed in", Color: "success"},
					{Value: activity.SignInFailed, Label: "Failed sign-in", Color: "danger"},
					{Value: activity.SignOut, Label: "Signed out", Color: "mute"},
					{Value: activity.SignUp, Label: "Signed up", Color: "info"},
					{Value: activity.Account, Label: "Account", Color: "info"},
					{Value: activity.Credential, Label: "Credential", Color: "warning"},
					{Value: activity.Peer, Label: "Peer", Color: "info"},
					{Value: activity.Approval, Label: "Approval", Color: "info"},
					{Value: activity.Settings, Label: "Setting", Color: "mute"},
				}},
				{Field: "user", Label: "Who"},
				{Field: "ip", Label: "From", Mute: true},
				{Field: "detail", Mute: true, Flex: 3, Line: 2},
				{Field: "browser", Mute: true, Flex: 1, Line: 2},
			},
		},
	}}
}

func (a *AdminApp) registerActivityRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/activity", a.handleActivity)
}

// handleActivity lists the log, newest first: ?kind= and ?user= narrow it,
// ?limit= caps it (activityListLimit when unset).
func (a *AdminApp) handleActivity(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := activityListLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	events := activity.List(a.activityStore(), activity.Filter{
		Kind:  strings.TrimSpace(r.URL.Query().Get("kind")),
		User:  strings.TrimSpace(r.URL.Query().Get("user")),
		Limit: limit,
	})
	type row struct {
		activity.Event
		At string `json:"at"`
	}
	out := make([]row, 0, len(events))
	for _, e := range events {
		out = append(out, row{Event: e, At: e.At.Format(time.RFC3339)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// activityStore is where the log is kept: the auth store, beside the accounts
// and sessions it records.
func (a *AdminApp) activityStore() activity.Store {
	if AuthDB != nil {
		if db := AuthDB(); db != nil {
			return db
		}
	}
	return nil
}

// audited runs h and, when it answered with success, records what it did. A
// refused or failed request is not a change, and logging it as one would put
// something in the record that never happened.
func audited(w http.ResponseWriter, r *http.Request, kind, detail string, h func(http.ResponseWriter)) {
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	h(sw)
	if sw.status < 400 {
		RecordActivity(r, kind, AuthCurrentUser(r), detail)
	}
}

// adminChange records an administrator's change in the server log and the
// activity log, in the same words: "set session_days=7", "updated embeddings
// config (...)". The two said different things before only because the
// activity log did not exist; one call keeps them saying the same thing.
// Never pass a secret: both logs are read by people.
func adminChange(r *http.Request, kind, format string, args ...any) {
	user := AuthCurrentUser(r)
	detail := fmt.Sprintf(format, args...)
	Log("[admin] user %q %s", user, detail)
	RecordActivity(r, kind, user, detail)
}

// statusWriter remembers the status a handler answered with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
