package orchestrate

// An app agent's settings, inside its app.
//
// An app agent (Scribe's Guide Author, Servitor's investigator) is its app's:
// it runs only inside the app, with the tools the app hands it each turn, and
// it is not dispatchable from anywhere else. So it is set up where it is used,
// beside its chat, and orchestrate does not list it: shown there, it picked up
// every agent surface that hangs off an agent (schedules, channels, cortex,
// delegation), each a way to run it somewhere it cannot work.
//
// The page is generic. Any app whose chat goes through AppChat with Settings
// set gets it under its chat prefix ("chat/settings"), served by ServeAppChat:
// the agent's budgets and reasoning, the same fields as orchestrate's own
// editor (budgetReasoningFields), and Reset to default. The save accepts those
// fields and nothing else, whatever the request carries.
//
// Each person has their own copy of each app agent, layered over the app's
// definition (agent_overlay.go): what they change is theirs, everything else
// follows the app, and Reset removes their copy.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// appAgentSettingsFields are what an app agent's settings page shows, open
// rather than folded: they are the whole page.
func (T *OrchestrateApp) appAgentSettingsFields(rec AgentRecord) []ui.FormField {
	fields := T.budgetReasoningFields(rec, agentForcesPrivate(rec) && !AllLLMsPrivate())
	for i := range fields {
		fields[i].Collapsed = false
	}
	return fields
}

// settableFields are the json names among fields a person can set: not the
// headers, and not a hidden field standing in for a control this deployment
// does not offer.
func settableFields(fields []ui.FormField) map[string]bool {
	out := map[string]bool{}
	for _, f := range fields {
		if f.Field != "" && f.Type != "header" && f.Type != "hidden" {
			out[f.Field] = true
		}
	}
	return out
}

// settingsAgents are the app agents an app's settings page covers, the chat's
// own first: every app agent its app registered, then the ones it runs that
// another app owns (AppChat.Agents). Only registered app agents, each once.
func settingsAgents(chat AgentRecord, c AppChat) []appagents.AppAgentSpec {
	spec, ok := appagents.AppAgentByID(chat.ID)
	if !ok {
		return nil
	}
	out := []appagents.AppAgentSpec{spec}
	seen := map[string]bool{spec.ID: true}
	var own []appagents.AppAgentSpec
	for _, sp := range appagents.AppAgents() {
		if !seen[sp.ID] && sp.OwningApp != "" && sp.OwningApp == spec.OwningApp {
			own = append(own, sp)
			seen[sp.ID] = true
		}
	}
	sort.SliceStable(own, func(i, j int) bool { return own[i].Name < own[j].Name })
	out = append(out, own...)
	for _, id := range c.Agents {
		if sp, ok := appagents.AppAgentByID(id); ok && !seen[id] {
			out = append(out, sp)
			seen[id] = true
		}
	}
	return out
}

// appAgentSettingsPage is the page: a section for each agent the app runs,
// each with its own form and Reset. Data and reset are at "settings/data"
// and "settings/reset" relative to it (it is served at "<prefix>settings"),
// with the agent in ?agent=.
func (T *OrchestrateApp) appAgentSettingsPage(udb Database, agents []appagents.AppAgentSpec, back string) ui.Page {
	if len(agents) == 0 {
		return ui.Page{Title: "Agent settings", ShowTitle: true, BackURL: chFirst(back, "..")}
	}
	app := chFirst(agents[0].OwningApp, "This app")
	var sections []ui.Section
	for _, sp := range agents {
		rec, _ := loadAgent(udb, sp.ID)
		name := chFirst(rec.Name, sp.Name, sp.ID)
		owner := chFirst(sp.OwningApp, "its app")
		q := "?agent=" + url.QueryEscape(sp.ID)
		about := sp.Description
		if owner != app {
			about = strings.TrimSpace(about + " It belongs to " + owner + ": a change here applies wherever it runs.")
		}
		sections = append(sections, ui.Section{
			Title:    name,
			Subtitle: about,
			Body: ui.Stack{Children: []ui.Component{
				ui.FormPanel{Assist: true, Source: "settings/data" + q, PostURL: "settings/data" + q, Method: "PATCH", Fields: T.appAgentSettingsFields(rec)},
				ui.DisplayPanel{
					Source: "settings/data" + q,
					Pairs:  []ui.DisplayPair{},
					Actions: []ui.ToolbarAction{{
						Label:   "Reset " + name + " to default",
						Method:  "POST",
						URL:     "settings/reset" + q,
						Confirm: "Reset " + name + " to its defaults? Every setting changed on it goes back to what " + owner + " set up, tool approvals and rules saved on it included. Its memory and conversations are kept.",
						Variant: "danger",
					}},
				},
			}},
		})
	}
	title := app + " agents"
	if len(agents) == 1 {
		title = chFirst(agents[0].Name, "Agent") + " settings"
	}
	return ui.Page{
		Title:      title,
		ShowTitle:  true,
		BackURL:    chFirst(back, ".."),
		MaxWidth:   "860px",
		SectionNav: len(sections) > 1,
		Sections:   sections,
	}
}

// serveAppAgentSettings answers "settings", "settings/data" and
// "settings/reset" for the app agents a chat's settings page covers
// (settingsAgents). A chat whose agent is not an app agent (an exposed agent
// on the Agents app) has no settings here: those belong to its owner, in
// orchestrate.
func (T *OrchestrateApp) serveAppAgentSettings(w http.ResponseWriter, r *http.Request, agent AgentRecord, c AppChat, sub string) {
	agents := settingsAgents(agent, c)
	if len(agents) == 0 || !c.Settings {
		http.NotFound(w, r)
		return
	}
	switch sub {
	case "settings":
		T.signedIn(func(w http.ResponseWriter, r *http.Request, _ string, udb Database) {
			T.appAgentSettingsPage(udb, agents, c.Back).ServeHTTP(w, r)
		})(w, r)
	case "settings/data":
		save := func(c *jsonapi.Call, body map[string]json.RawMessage) (any, error) {
			rec, err := settingsAgentAt(c, agents)
			if err != nil {
				return nil, err
			}
			if err := saveAppAgentSettings(c.UDB, c.User, rec, settableFields(T.appAgentSettingsFields(rec)), body); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		}
		jsonapi.Methods{
			"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
				rec, err := settingsAgentAt(c, agents)
				if err != nil {
					return nil, err
				}
				allowed := settableFields(T.appAgentSettingsFields(rec))
				out := pickFields(rec, allowed)
				view := withEditorChoices(RootDB, rec)
				for _, k := range []string{"lead_use", "reasoning"} {
					if allowed[k] {
						out[k] = view[k]
					}
				}
				return out, nil
			}),
			"PATCH": jsonapi.Patch(T.DB, save),
			"POST":  jsonapi.Post(T.DB, save),
		}.Serve(w, r)
	case "settings/reset":
		jsonapi.Post(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
			rec, err := settingsAgentAt(c, agents)
			if err != nil {
				return nil, err
			}
			if err := resetAppAgent(c.UDB, rec.ID); err != nil {
				return nil, jsonapi.BadRequest("Not reset: " + err.Error())
			}
			return map[string]any{"ok": true}, nil
		})(w, r)
	default:
		http.NotFound(w, r)
	}
}

// settingsAgentAt is the agent a settings call is about: ?agent= when it names
// one the page covers, the chat's own when it names none, and nothing
// otherwise.
func settingsAgentAt(c *jsonapi.Call, agents []appagents.AppAgentSpec) (AgentRecord, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("agent"))
	if id == "" {
		id = agents[0].ID
	}
	covered := false
	for _, sp := range agents {
		covered = covered || sp.ID == id
	}
	if !covered {
		return AgentRecord{}, errNotFound
	}
	rec, ok := loadAgent(c.UDB, id)
	if !ok {
		return AgentRecord{}, errNotFound
	}
	return rec, nil
}

// pickFields is rec as a JSON object holding only the allowed fields.
func pickFields(rec AgentRecord, allowed map[string]bool) map[string]json.RawMessage {
	raw, _ := json.Marshal(rec)
	var all map[string]json.RawMessage
	_ = json.Unmarshal(raw, &all)
	out := map[string]json.RawMessage{}
	for k, v := range all {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

// saveAppAgentSettings merges the allowed fields of the request body onto the
// person's copy of the agent. A field outside allowed is refused rather than
// dropped quietly: a request that names one is not one this page made.
func saveAppAgentSettings(udb Database, user string, rec AgentRecord, allowed map[string]bool, body map[string]json.RawMessage) error {
	for k := range body {
		if !allowed[k] {
			return jsonapi.BadRequest(fmt.Sprintf("%q is not an app agent setting", k))
		}
	}
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &rec); err != nil {
		return jsonapi.BadRequest(fmt.Sprintf("bad value: %v", err))
	}
	applyEditorChoices(RootDB, &rec, body)
	rec.Owner = user
	if _, err := saveAgent(udb, rec); err != nil {
		return jsonapi.BadRequest(err.Error())
	}
	return nil
}

// resetAppAgent removes the person's copy of an app agent, so it reads as its
// app registered it. Its memory and conversations are kept: a reset is about
// how the agent is set up, not what it has learned or been told. (Reverting a
// framework seed drops those too, because there the persona the memory grew
// under is what is being thrown away; an app agent's persona is the app's
// and never changed.)
func resetAppAgent(udb Database, id string) error {
	if _, isApp := appagents.AppAgentByID(id); !isApp {
		return fmt.Errorf("%q is not an app agent", id)
	}
	var stored AgentRecord
	if udb == nil || !udb.Get(agentsTable, id, &stored) {
		return fmt.Errorf("it is already at its defaults")
	}
	udb.Unset(agentsTable, id)
	return nil
}
