// Orchestrate's personal-preferences hook into /account
// (RegisterAccountSection): the Default agent setting. Core stores the
// raw preference string on AuthUser (AuthGet/SetDefaultAgent); this file
// owns its semantics — "" = standard default (Chat), the "last" sentinel
// = last-accessed (tracked client-side in a cookie by pinAgentInURL),
// anything else = a specific agent ID validated against the agents the
// user can actually see.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	// lastAccessedSentinel is an explicit DefaultAgent value meaning "open
	// on whichever agent I last had selected". The UNSET preference ("")
	// means the same thing — last-accessed is the default behavior, so
	// there is no blessed framework agent baked in — but the sentinel
	// stays accepted for values stored while it was the select's option.
	lastAccessedSentinel = "last"
	// lastAgentCookie is written by the chat surface (pinAgentInURL in
	// web_assets) with the currently selected agent ID, so the server can
	// honor the last-accessed preference at page render without a
	// per-switch API write.
	lastAgentCookie = "gohort_last_agent"
)

// accountSection builds the "Agents" preferences block for /account.
// Options are per-user (the user's own fleet), which is why the account
// registry passes a builder the request instead of taking a static
// section. Hidden entirely for users without the Agents app grant.
func (T *OrchestrateApp) accountSection(r *http.Request, user string) (ui.Section, bool) {
	if !UserHasAppAccess(r, T.WebPath()) {
		return ui.Section{}, false
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		return ui.Section{}, false
	}
	// No blessed "Chat" option: the framework seeds are on their way out
	// of the user-facing picker, so the only choices are last-accessed
	// (the unset default) or one of the user's own visible agents.
	opts := []ui.SelectOption{
		{Value: "", Label: "Last accessed — wherever you left off"},
	}
	grouped, _, _ := agentPickerOptions(pickerAgents(listAgents(udb, user)))
	opts = append(opts, grouped...)
	return ui.Section{
		Title:    "Agents",
		Subtitle: "Preferences for the Agents chat surface.",
		Body: ui.FormPanel{
			Source: T.WebPath() + "/api/default-agent",
			Fields: []ui.FormField{
				{Field: "default_agent", Type: "select", Label: "Default agent",
					Options: opts,
					Help:    "Which agent the chat surface opens on when you arrive without a deep link. Last accessed remembers your selection per browser (per device); picking a specific agent applies everywhere. An agent's own link still wins."},
			},
		},
	}, true
}

// handleDefaultAgentPref is the /api/default-agent endpoint behind the
// account section's FormPanel: GET returns {default_agent}; POST saves
// it after validating that a specific agent ID is one this user can see
// (so a stale or forged value can't point the surface at nothing).
func (T *OrchestrateApp) handleDefaultAgentPref(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"default_agent": AuthGetDefaultAgent(AuthDB(), user),
		})
	case http.MethodPost:
		var req struct {
			DefaultAgent string `json:"default_agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		v := strings.TrimSpace(req.DefaultAgent)
		if v != "" && v != lastAccessedSentinel && !userCanSeeAgent(udb, user, v) {
			http.Error(w, "unknown agent", http.StatusBadRequest)
			return
		}
		AuthSetDefaultAgent(AuthDB(), user, v)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// userCanSeeAgent reports whether id is one of the agents the PICKER
// offers this user — the same pickerAgents filter every picker surface
// applies, so a retired seed can't be set as a default.
func userCanSeeAgent(udb Database, user, id string) bool {
	for _, a := range pickerAgents(listAgents(udb, user)) {
		if a.ID == id {
			return true
		}
	}
	return false
}

// resolveDefaultAgent turns the stored preference into the agent the
// chat page should open on, given the picker options being rendered
// (which already encode visibility). A specific, still-visible agent
// wins; unset (and the legacy "last" sentinel) means last-accessed via
// the cookie; anything unresolvable lands on fallback. The ?agent= deep
// link is handled by the caller and wins over all of this.
func resolveDefaultAgent(r *http.Request, user, fallback string, agentOpts []ui.SelectOption) string {
	want := AuthGetDefaultAgent(AuthDB(), user)
	if want == "" || want == lastAccessedSentinel {
		want = ""
		if c, err := r.Cookie(lastAgentCookie); err == nil {
			if v, uerr := url.QueryUnescape(c.Value); uerr == nil {
				want = v
			}
		}
	}
	if want != "" {
		for _, opt := range agentOpts {
			if opt.Value == want {
				return want
			}
		}
	}
	return fallback
}
