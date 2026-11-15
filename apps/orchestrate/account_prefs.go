// Orchestrate's personal-preferences hook into /account
// (RegisterAccountSection): the Main agent and the Default agent. Core stores
// both strings on AuthUser; this file owns what they mean. Main agent:
// main_agent.go. Default agent: "" = the main agent, the "last" sentinel =
// last-accessed (tracked client-side in a cookie by pinAgentInURL),
// anything else = a specific agent ID validated against the agents the user
// can actually see.

package orchestrate

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	// lastAccessedSentinel is a DefaultAgent value meaning "open on
	// whichever agent I last had selected". The unset preference ("")
	// opens on the main agent.
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
	// The main agent first, as the unset default; then last-accessed, then
	// any agent the picker offers.
	mainLabel := "Your main agent"
	if m, ok := mainAgentFor(user); ok {
		mainLabel += " (" + chFirst(m.Name, m.ID) + ")"
	}
	opts := []ui.SelectOption{
		{Value: "", Label: mainLabel},
		{Value: lastAccessedSentinel, Label: "Last accessed: wherever you left off"},
	}
	agents := listAgents(udb, user)
	grouped, _, _ := agentPickerOptions(pickerAgents(agents))
	opts = append(opts, grouped...)
	// The main agent is one of their own standing agents.
	var mainOpts []ui.SelectOption
	for _, a := range agents {
		if mainAgentCandidate(a, user) {
			mainOpts = append(mainOpts, ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID)})
		}
	}
	if len(mainOpts) == 0 {
		mainOpts = []ui.SelectOption{{Value: "", Label: "(create your assistant first)"}}
	}
	return ui.Section{
		Title:    "Agents",
		Subtitle: "Preferences for the Agents chat surface.",
		Body: ui.FormPanel{
			Source: T.WebPath() + "/api/default-agent",
			Fields: []ui.FormField{
				{Field: "main_agent", Type: "select", Label: "Main agent",
					Options: mainOpts,
					Help:    "Your go-to agent: where an ask that names no agent goes, over MCP, and where Agents opens unless you choose otherwise below.",
					Detail:  "Your first assistant, unless you choose another of your own agents here."},
				{Field: "default_agent", Type: "select", Label: "Default agent",
					Options: opts,
					Help:    "Which agent Agents opens on when you arrive without a link to one.",
					Detail:  "Your main agent unless you choose otherwise. Last accessed remembers your selection per browser, so per device. Picking a specific agent applies everywhere. An agent's own link still wins."},
			},
		},
	}, true
}

// defaultAgentGet is the /api/default-agent endpoint behind the account
// section's FormPanel: {default_agent}.
func (T *OrchestrateApp) defaultAgentGet(c *jsonapi.Call) (any, error) {
	return map[string]any{
		"main_agent":    mainAgentID(c.User),
		"default_agent": AuthGetDefaultAgent(AuthDB(), c.User),
	}, nil
}

// defaultAgentSave saves it after validating that a specific agent ID is one
// this user can see (so a stale or forged value can't point the surface at
// nothing).
func (T *OrchestrateApp) defaultAgentSave(c *jsonapi.Call, req struct {
	MainAgent    string `json:"main_agent"`
	DefaultAgent string `json:"default_agent"`
}) (any, error) {
	// The form saves both fields together: the main agent changes only when
	// it is set to a different agent of theirs.
	if m := strings.TrimSpace(req.MainAgent); m != "" && m != mainAgentID(c.User) {
		if err := setMainAgent(c.User, m); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	v := strings.TrimSpace(req.DefaultAgent)
	if v != "" && v != lastAccessedSentinel && !userCanSeeAgent(c.UDB, c.User, v) {
		return nil, jsonapi.BadRequest("unknown agent")
	}
	AuthSetDefaultAgent(AuthDB(), c.User, v)
	return jsonapi.Empty(http.StatusNoContent), nil
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
// wins; unset means the main agent (main_agent.go), or last-accessed while
// there is none; the "last" sentinel means last-accessed via the cookie;
// anything unresolvable lands on fallback. The ?agent= deep link is handled
// by the caller and wins over all of this.
func resolveDefaultAgent(r *http.Request, user, fallback string, agentOpts []ui.SelectOption) string {
	want := AuthGetDefaultAgent(AuthDB(), user)
	if want == "" {
		if m := mainAgentID(user); m != "" {
			for _, opt := range agentOpts {
				if opt.Value == m {
					return m
				}
			}
		}
		want = lastAccessedSentinel
	}
	if want == lastAccessedSentinel {
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
