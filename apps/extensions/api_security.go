// An API's Security panel: who may use it and how, in one place.
//
// The answers used to be scattered: whether to ask first and whether it was
// locked to tools sat in Edit among the base URL and the header name, and who
// it was lent to was two more buttons on the row. Edit now says what the API
// IS; this says who may reach it:
//
//   - Who can use it: all my agents, only tools that declare it, or only
//     these agents (OnlyAgents, enforced with the rest of an agent's
//     credential deny-list, so it fails closed for every agent left off it);
//   - Ask first: never, before writes, or before every call;
//   - Other people: whether it may be lent, then who has it for reads and
//     for writes, and the recent calls made through it.

package extensions

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// The answers to "who can use it".
const (
	apiReachAll    = "all"
	apiReachTools  = "tools"
	apiReachAgents = "agents"
)

// apiSecurity is the panel's record.
type apiSecurity struct {
	Reach      string   `json:"reach"`
	OnlyAgents []string `json:"only_agents"`
	Ask        string   `json:"ask"` // never | writes | always
	Lending    string   `json:"lending"`
}

func apiSecurityOf(c SecureCredential) apiSecurity {
	out := apiSecurity{Reach: apiReachAll, OnlyAgents: append([]string{}, c.OnlyAgents...), Ask: "never", Lending: c.Lending}
	switch {
	case c.Secured:
		out.Reach = apiReachTools
	case len(c.OnlyAgents) > 0:
		out.Reach = apiReachAgents
	}
	switch {
	case c.RequiresConfirm:
		out.Ask = "always"
	case c.ConfirmWrites:
		out.Ask = "writes"
	}
	return out
}

// saveAPISecurity is POST api/credentials?action=security&name=.
func (T *Extensions) saveAPISecurity(w http.ResponseWriter, r *http.Request, user string) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	c, found := Secure().LoadUser(user, name)
	if !found {
		http.Error(w, "no such credential", http.StatusNotFound)
		return
	}
	var body apiSecurity
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	secured, agents := false, []string(nil)
	switch body.Reach {
	case apiReachAll, "":
	case apiReachTools:
		secured = true
	case apiReachAgents:
		for _, a := range body.OnlyAgents {
			if a = strings.TrimSpace(a); a != "" {
				agents = append(agents, a)
			}
		}
		if len(agents) == 0 {
			http.Error(w, "pick at least one agent, or choose All my agents", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "who can use it must be all, tools or agents", http.StatusBadRequest)
		return
	}
	switch body.Ask {
	case "", "never":
		c.RequiresConfirm, c.ConfirmWrites = false, false
	case "writes":
		c.RequiresConfirm, c.ConfirmWrites = false, true
	case "always":
		c.RequiresConfirm, c.ConfirmWrites = true, false
	default:
		http.Error(w, "ask first must be never, writes or always", http.StatusBadRequest)
		return
	}
	switch body.Lending {
	case "", LendNone, LendRead, LendAny:
		c.Lending = body.Lending
	default:
		http.Error(w, "unknown lending setting", http.StatusBadRequest)
		return
	}
	// The record's own fields through Save (an empty secret keeps the stored
	// one); the lock and the agent list through their own setters, which Save
	// leaves alone.
	if err := Secure().Save(c, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := Secure().SetSecuredOwned(user, name, secured); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := Secure().SetOnlyAgentsOwned(user, name, agents); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[extensions] user=%q set security on API %q: reach=%s agents=%d ask=%s lending=%q", user, name, body.Reach, len(agents), body.Ask, c.Lending)
	w.WriteHeader(http.StatusNoContent)
}

// apiSecurityFields is the panel's form.
func apiSecurityFields() []ui.FormField {
	return []ui.FormField{
		{Field: "reach", Label: "Who can use it", Type: "select", Options: []ui.SelectOption{
			{Value: apiReachAll, Label: "All my agents"},
			{Value: apiReachTools, Label: "Only tools that declare it"},
			{Value: apiReachAgents, Label: "Only these agents"},
		},
			Help: "All my agents: each gets a fetch_url_<name> tool for it. Only tools: no such tool, it is reached only through tools you build that name it. Only these agents: the ones you pick below, and no other.",
			Detail: "Only these agents is enforced wherever an agent runs: an agent not on the list loses this API's tools and cannot reach its host directly either. " +
				"Builder you open yourself can always work on it. Builder sent a job by one of your agents can only if that agent is on the list. The secret is never handed to an agent or a tool either way; calls are signed server-side."},
		{Field: "only_agents", Label: "Agents that may use it", Type: "checklist",
			OptionsSource: "/orchestrate/api/console/agent-options", ShowWhen: "reach:agents"},
		{Field: "ask", Label: "Ask me first", Type: "select", Options: []ui.SelectOption{
			{Value: "never", Label: "Never"},
			{Value: "writes", Label: "Before a write (POST, PUT, PATCH, DELETE)"},
			{Value: "always", Label: "Before every call"},
		},
			Help:   "A write asks you first in the chat; in a run nobody is watching (a schedule, a channel message) it waits in your approval queue.",
			Detail: "Before every call suits anything that reaches real people or spends money. Before a write suits a key your agents mostly read with."},
		{Field: "lending", Label: "May this be lent to other people?", Type: "select",
			Options: []ui.SelectOption{
				{Value: "", Label: "Not decided"},
				{Value: LendNone, Label: "Nobody — never lend this key"},
				{Value: LendRead, Label: "Readers only"},
				{Value: LendAny, Label: "Readers and writers"},
			},
			Help: "Your standing answer, as against who has it today (below).",
			Detail: "Nobody removes the lend options wherever they are offered, and refuses them wherever they are written. Readers only allows GET and HEAD through your key and refuses a write lend: what somebody writes through your credential arrives at the far end as YOU.\n\n" +
				"Tightening this takes back what it now forbids. Setting Nobody over a key two people hold takes it from both; narrowing to Readers only leaves them the key and takes the writing."},
	}
}
