// Package account is the per-user settings surface — every logged-in user's own
// page (NOT admin-gated, distinct from the admin Site Settings). It gives the
// previously-scattered per-user preferences a home, and is where per-user
// integrations (individual OAuth connections) get connected/disconnected once
// the per-credential Scope work lands. Reached from the dashboard header
// (Account link next to Logout), not a tile (WebHidden).
package account

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(Account)) }

type Account struct {
	AppCore
}

func (T Account) Name() string         { return "account" }
func (T Account) SystemPrompt() string { return "" }
func (T Account) Desc() string         { return "Apps: your personal account + preferences." }
func (T *Account) Init() error         { return T.Flags.Parse() }
func (T *Account) Main() error {
	Log("account is a dashboard-only app. Start with: gohort serve")
	return nil
}

func (T *Account) WebPath() string { return "/account" }
func (T *Account) WebName() string { return "Account" }
func (T *Account) WebDesc() string { return "Your personal preferences + connected accounts." }

// WebHidden keeps Account off the app grid — it's reached from the dashboard
// header (the Account link next to Logout), not as an app tile competing with
// the real apps.
func (T *Account) WebHidden() bool { return true }

func (T *Account) Routes() {
	T.HandleFunc("/api/prefs", T.handlePrefs)
	T.HandleFunc("/", T.servePage)
}

// --- preferences endpoint ----------------------------------------------------

// handlePrefs GET returns the user's personal defaults; POST updates whichever
// fields are present (the FormPanel auto-saves per toggle).
func (T *Account) handlePrefs(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	db := AuthDB()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]bool{
			"notify":            AuthGetNotifyDefault(db, user),
			"private_mode":      AuthGetPrivateMode(db, user),
			"inferred_disabled": AuthGetInferredDisabled(db, user),
		})
	case http.MethodPost:
		var req struct {
			Notify           *bool `json:"notify,omitempty"`
			PrivateMode      *bool `json:"private_mode,omitempty"`
			InferredDisabled *bool `json:"inferred_disabled,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Notify != nil {
			AuthSetNotifyDefault(db, user, *req.Notify)
		}
		if req.PrivateMode != nil {
			AuthSetPrivateMode(db, user, *req.PrivateMode)
		}
		if req.InferredDisabled != nil {
			AuthSetInferredDisabled(db, user, *req.InferredDisabled)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- page --------------------------------------------------------------------

func (T *Account) servePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	ui.Page{
		Title:     "Account",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "640px",
		Sections: []ui.Section{
			{
				Title:    "Preferences",
				Subtitle: "Personal defaults, applied across your agents. Saved as you toggle.",
				Body: ui.FormPanel{
					Source: "api/prefs",
					Fields: []ui.FormField{
						{Field: "notify", Label: "Email notifications", Type: "toggle",
							Help: "Receive email when an agent finishes work for you."},
						{Field: "private_mode", Label: "Private mode by default", Type: "toggle",
							Help: "Mask network-capable tools (web search, fetch, …) by default — keeps turns local. Per-agent overrides still apply."},
						{Field: "inferred_disabled", Label: "Clean mode by default", Type: "toggle",
							Help: "Suppress the Reference Memory layer by default — agents answer fresh from your question + knowledge, without prior derived findings. Per-agent overrides still apply."},
					},
				},
			},
			{
				Title:    "Connected accounts",
				Subtitle: "Integrations you authorize with your own account (read or write as you).",
				Body: ui.EmptyState{
					Icon:  "🔌",
					Title: "No per-user integrations available yet",
					Hint:  "When your admin enables an integration that connects with your own account, you'll authorize and manage it here.",
				},
			},
		},
	}.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
