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

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
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

// appAgentSettingsPage is the page, with its data and reset at "settings/data"
// and "settings/reset" relative to it (it is served at "<prefix>settings").
func (T *OrchestrateApp) appAgentSettingsPage(rec AgentRecord, spec appagents.AppAgentSpec, back string) ui.Page {
	app := chFirst(spec.OwningApp, "its app")
	name := chFirst(rec.Name, spec.Name, rec.ID)
	var sections []ui.Section
	var cur *ui.Section
	var group []ui.FormField
	flush := func() {
		if cur != nil {
			cur.Body = ui.FormPanel{Source: "settings/data", PostURL: "settings/data", Method: "PATCH", Fields: group}
			sections = append(sections, *cur)
		}
		cur, group = nil, nil
	}
	for _, f := range T.appAgentSettingsFields(rec) {
		if f.Type == "header" {
			flush()
			cur = &ui.Section{Title: f.Label, Subtitle: f.Help}
			continue
		}
		group = append(group, f)
	}
	flush()
	if len(sections) > 0 {
		sections[0].Subtitle = name + " belongs to " + app + " and runs only here. These shape how it works; its prompt and tools are " + app + "'s. " + sections[0].Subtitle
	}
	sections = append(sections, ui.Section{
		Title:    "Reset to default",
		Subtitle: "Put " + name + " back as " + app + " set it up.",
		Detail:   "Every setting changed here goes back, tool approvals and rules saved on it included. Its memory and conversations are kept.",
		Body: ui.DisplayPanel{
			Source: "settings/data",
			Pairs:  []ui.DisplayPair{},
			Actions: []ui.ToolbarAction{{
				Label:   "Reset to default",
				Method:  "POST",
				URL:     "settings/reset",
				Confirm: "Reset " + name + " to its defaults? Every setting changed on it goes back to what " + app + " set up. Its memory and conversations are kept.",
				Variant: "danger",
			}},
		},
	})
	return ui.Page{
		Title:     name + " settings",
		ShowTitle: true,
		BackURL:   chFirst(back, ".."),
		MaxWidth:  "820px",
		Sections:  sections,
	}
}

// serveAppAgentSettings answers "settings", "settings/data" and
// "settings/reset" for an app agent. Anything else that is not an app agent
// (an exposed agent on the Agents app) has no settings here: those belong to
// its owner, in orchestrate.
func (T *OrchestrateApp) serveAppAgentSettings(w http.ResponseWriter, r *http.Request, agent AgentRecord, c AppChat, sub string) {
	spec, isApp := appagents.AppAgentByID(agent.ID)
	if !isApp || !c.Settings {
		http.NotFound(w, r)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rec, ok := loadAgent(udb, agent.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch sub {
	case "settings":
		page := T.appAgentSettingsPage(rec, spec, c.Back)
		page.ServeHTTP(w, r)
	case "settings/data":
		allowed := settableFields(T.appAgentSettingsFields(rec))
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, pickFields(rec, allowed))
		case http.MethodPatch, http.MethodPost:
			if err := saveAppAgentSettings(udb, user, rec, allowed, r); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "settings/reset":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := resetAppAgent(udb, agent.ID); err != nil {
			http.Error(w, "Not reset: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.NotFound(w, r)
	}
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
func saveAppAgentSettings(udb Database, user string, rec AgentRecord, allowed map[string]bool, r *http.Request) error {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("bad request")
	}
	for k := range body {
		if !allowed[k] {
			return fmt.Errorf("%q is not an app agent setting", k)
		}
	}
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("bad value: %v", err)
	}
	rec.Owner = user
	_, err := saveAgent(udb, rec)
	return err
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
	if stored.Locked {
		return fmt.Errorf("it is locked")
	}
	udb.Unset(agentsTable, id)
	return nil
}
