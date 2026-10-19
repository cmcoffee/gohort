// A tool's Security panel: who can use it and how it runs, in one place.
//
// The answers used to be four separate things on the row: Access (which
// agents, Builder-only, bound only, never unattended), Share with users,
// Lock, and an ask-before-every-call mark that could only be set from the
// permissions screens. The Security button opens one panel with three parts,
// the same shape an API's has:
//
//   - How it runs: never run unattended, and ask me before every call. There
//     is no Lock: a change to an existing tool asks the owner every time
//     (apps/orchestrate/change_approval.go);
//   - Who can use it: All my agents, Only these agents (with the list), or
//     Apps only. Builder-only is not a choice of its own:
//     it is Only these agents with none ticked, since Builder reaches every
//     tool when the owner opens it;
//   - Other people: who it is shared with, for pool tools.

package extensions

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// toolRunSection serves the "How it runs" pills: GET the pills, POST one
// toggle {target, on}, the same contract the access pills use.
func toolRunSection(w http.ResponseWriter, r *http.Request, db Database, user, name string) {
	row, inPool := UserToolByName(db, user, name)
	switch r.Method {
	case http.MethodGet:
		type pill struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			On    bool   `json:"on"`
		}
		items := []pill{
			{Key: "never_unattended", Label: "Never run unattended", On: inPool && row.Tool.NoUnattended},
			{Key: "ask_first", Label: "Ask me before every call", On: UserToolAsksInChat(db, user, "", name)},
		}
		note := "Never run unattended: it is held for you in a schedule or a channel message, where nobody is there to ask. " +
			"Ask me before every call: every agent asks you first, in the chat or your approval queue. " +
			"Changes to it by Builder or an agent always ask you first."
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"items": items, "note": note})
	case http.MethodPost:
		var body struct {
			Target string `json:"target"`
			On     bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if code, err := toolRunApply(db, user, name, strings.TrimSpace(body.Target), body.On); err != nil {
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// toolRunApply sets one How it runs setting on one tool: ask_first (the
// fleet-wide ask-before-every-call mark) or never_unattended (on the tool's
// record, so only a tool kept for the user's agents). Shared by the tool's
// panel and its category's.
func toolRunApply(db Database, user, name, target string, on bool) (int, error) {
	switch target {
	case "ask_first":
		if !SetUserToolAsksInChat(db, user, "", name, on) {
			return http.StatusBadRequest, Error("could not mark the tool")
		}
	case "never_unattended":
		row, inPool := UserToolByName(db, user, name)
		if !inPool {
			return http.StatusBadRequest, Error("keep the tool for your agents first (Who can use it), then set this")
		}
		if row.Tool.NoUnattended == on {
			return http.StatusOK, nil
		}
		row.Tool.NoUnattended = on
		if !UpdatePersistentTempTool(db, user, row.Tool) {
			return http.StatusBadRequest, Error("update failed")
		}
	default:
		return http.StatusBadRequest, Error("unknown setting " + target)
	}
	return http.StatusOK, nil
}

// toolSharePickerJSON is the "Other people" picker as the panel mounts it,
// with __NAME__ where the tool's name goes. Built from the same ui.ACLPicker
// the row used to carry, so the picker's wire form stays core/ui's.
func toolSharePickerJSON() string {
	b, _ := json.Marshal(ui.ACLPicker(ui.ACLPickerConfig{
		OptionsSource: "api/user-candidates?groups=1",
		RecordSource:  "api/tools?share=__NAME__",
		Field:         "shared_with",
		PostTo:        "api/tools?action=share&name=__NAME__",
		Method:        "POST",
		Noun:          "user",
		Intro: "People, or a team you are on. They can take this tool into their own catalog, and it runs in THEIR session against their own credentials. " +
			"Nothing loads for their agents until they take it: a share is an offer, not a push.",
		EmptyText:  "No other users to share with yet.",
		Invalidate: []string{"api/tools"},
	}))
	return string(b)
}

// toolScopeToggle switches one scope target for a tool: "global" (every one
// of the user's agents) or an agent id. A tool not in any scope yet (a session
// draft, an orphan whose agent was deleted) is kept or re-homed by the first
// target switched on; switching one off is then a no-op. The status code goes
// with the error.
func toolScopeToggle(prov ScopeProvider, db Database, user, name, target string, on bool) (int, error) {
	if _, found := prov.State(db, user, name); found {
		if err := prov.Set(db, user, name, target, on); err != nil {
			return http.StatusBadRequest, err
		}
		return http.StatusOK, nil
	}
	if !on {
		return http.StatusOK, nil
	}
	// Orphan first: it is already a committed tool that simply lost its
	// agent, so it re-homes rather than promotes.
	for _, o := range LoadOrphanedTempTools(db, user) {
		if o.Tool.Name != name {
			continue
		}
		if AdminRehomeOrphanTool == nil {
			return http.StatusServiceUnavailable, Error("re-homing unavailable")
		}
		if err := AdminRehomeOrphanTool(db, user, name, target); err != nil {
			return http.StatusBadRequest, err
		}
		return http.StatusOK, nil
	}
	sid, agentID := "", ""
	for _, d := range ListScopedTools(user) {
		if d.Scope == ScopeSessionTool && d.Tool.Name == name {
			sid, agentID = d.SessionID, d.AgentID
			break
		}
	}
	if sid == "" {
		return http.StatusNotFound, Error("not found")
	}
	promoteTarget := ScopeTargetGlobal
	if target != scopeAllAgents {
		promoteTarget = ScopeTargetAgent
		agentID = target // keep it on the agent whose pill was switched on
	}
	if _, err := PromoteScopedTool(user, agentID, sid, name, promoteTarget); err != nil {
		return http.StatusBadRequest, err
	}
	return http.StatusOK, nil
}

// Who can use a tool, as one choice:
//
//   - all: every one of the user's agents (the user-wide pool);
//   - agents: only the agents ticked. None ticked is the tool's Builder-only
//     flag: no agent loads it, and Builder still does when the owner opens it,
//     since Builder reaches every tool of the owner's then
//     (apps/orchestrate/builder_delegation.go). The scope under the flag is
//     kept, so the first agent ticked again is all it takes;
//   - bound ("Apps only"): only apps that attach it on purpose (a Servitor
//     system's tools, for instance), none of the user's agents; the BoundOnly
//     flag.
//
// Leaving "Only these agents" remembers who was ticked (toolWhoRemembered),
// and choosing it again puts them back.
const (
	toolWhoAll    = "all"
	toolWhoAgents = "agents"
	toolWhoBound  = "bound"
)

// toolWhoSection serves "Who can use it". GET -> {mode, agents, note}; mode is
// "" for a tool not kept anywhere yet. POST {mode} picks one of the three, and
// POST {agent, on} ticks one agent under "agents".
func toolWhoSection(w http.ResponseWriter, r *http.Request, db Database, user, name string) {
	prov, ok := ScopeProviderFor("tool")
	if !ok {
		http.Error(w, "tool scope unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, toolWhoState(prov, db, user, name))
	case http.MethodPost:
		var body struct {
			Mode  string `json:"mode"`
			Agent string `json:"agent"`
			On    bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var code int
		var err error
		if a := strings.TrimSpace(body.Agent); a != "" {
			code, err = toolWhoTick(prov, db, user, name, a, body.On)
		} else {
			code, err = toolWhoPick(prov, db, user, name, strings.TrimSpace(body.Mode))
		}
		if err != nil {
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type toolWhoAgent struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	On    bool   `json:"on"`
	Under string `json:"under,omitempty"`
}

func toolWhoState(prov ScopeProvider, db Database, user, name string) map[string]any {
	st, found := prov.State(db, user, name)
	row, kept := UserToolByName(db, user, name)
	agents := []toolWhoAgent{}
	listed := map[string]bool{}
	for _, a := range st.Agents {
		if listed[a.ID] {
			continue // one checkbox per agent
		}
		listed[a.ID] = true
		agents = append(agents, toolWhoAgent{ID: a.ID, Label: a.Name, On: a.On && !(kept && row.Tool.BuilderOnly), Under: a.ParentID})
	}
	out := map[string]any{"agents": agents}
	switch {
	case kept && row.Tool.BoundOnly:
		out["mode"] = toolWhoBound
		out["note"] = "None of your agents load it. Apps that attach it on purpose use it (a Servitor system's tools, for instance), and Builder can still test and fix it."
	case kept && row.Tool.BuilderOnly:
		out["mode"] = toolWhoAgents
		out["note"] = "No agent can use it. Builder still can when you open it yourself."
	case found && st.Global:
		out["mode"] = toolWhoAll
		out["note"] = "Every one of your agents can use it."
	case found:
		out["mode"] = toolWhoAgents
		out["note"] = "Only the agents ticked can use it."
	default:
		out["mode"] = ""
		out["note"] = "Not kept yet: choose who can use it to keep it."
		for _, o := range LoadOrphanedTempTools(db, user) {
			if o.Tool.Name == name {
				out["note"] = "Orphaned: the agent that held it was deleted. Choose who can use it to keep it, or Delete to discard."
				break
			}
		}
	}
	if len(st.Missing) > 0 {
		out["note"] = "Missing dependency: " + strings.Join(st.Missing, ", ") + ". " + out["note"].(string)
	}
	return out
}

// toolWhoFlags writes the Builder-only and bound-only flags, homing an orphan
// to the user-wide pool first so it has a record to carry them.
func toolWhoFlags(db Database, user, name string, builderOnly, boundOnly bool) (int, error) {
	row, ok := UserToolByName(db, user, name)
	if !ok && AdminRehomeOrphanTool != nil {
		for _, o := range LoadOrphanedTempTools(db, user) {
			if o.Tool.Name == name {
				if err := AdminRehomeOrphanTool(db, user, name, scopeAllAgents); err != nil {
					return http.StatusBadRequest, err
				}
				row, ok = UserToolByName(db, user, name)
				break
			}
		}
	}
	if !ok {
		return http.StatusNotFound, Error("tool not found")
	}
	if row.Tool.BuilderOnly == builderOnly && row.Tool.BoundOnly == boundOnly {
		return http.StatusOK, nil
	}
	row.Tool.BuilderOnly, row.Tool.BoundOnly = builderOnly, boundOnly
	if !UpdatePersistentTempTool(db, user, row.Tool) {
		return http.StatusBadRequest, Error("update failed")
	}
	return http.StatusOK, nil
}

// toolWhoPick moves a tool to one of the three choices.
func toolWhoPick(prov ScopeProvider, db Database, user, name, mode string) (int, error) {
	switch mode {
	case toolWhoAll:
		toolWhoRemember(prov, db, user, name)
		if code, err := toolScopeToggle(prov, db, user, name, scopeAllAgents, true); err != nil {
			return code, err
		}
		return toolWhoFlags(db, user, name, false, false)
	case toolWhoBound:
		toolWhoRemember(prov, db, user, name)
		return toolWhoFlags(db, user, name, false, true)
	case toolWhoAgents:
		if _, kept := UserToolByName(db, user, name); !kept {
			if _, found := prov.State(db, user, name); !found {
				return http.StatusOK, nil // kept by the first agent ticked
			}
		}
		// The agents ticked last time, if any are still there.
		if ids := toolWhoRemembered(db, user, name); len(ids) > 0 {
			if code, err := toolWhoOnly(prov, db, user, name, ids); err != nil || code != http.StatusNoContent {
				return code, err
			}
			return http.StatusOK, nil
		}
		// Nobody ticked yet: no agent loads it until one is.
		return toolWhoFlags(db, user, name, true, false)
	}
	return http.StatusBadRequest, Error("unknown choice " + mode)
}

// toolWhoTick ticks or unticks one agent under "Only these agents".
func toolWhoTick(prov ScopeProvider, db Database, user, name, agent string, on bool) (int, error) {
	row, kept := UserToolByName(db, user, name)
	st, found := prov.State(db, user, name)
	if !found {
		return toolScopeToggle(prov, db, user, name, agent, on)
	}
	if on {
		if kept && (row.Tool.BuilderOnly || row.Tool.BoundOnly) {
			// Leaving "nobody" (or bound-only): the agent ticked is the only
			// one, whatever scope sat under the flag.
			if code, err := toolWhoOnly(prov, db, user, name, []string{agent}); err != nil {
				return code, err
			}
			return http.StatusOK, nil
		}
		return toolScopeToggle(prov, db, user, name, agent, true)
	}
	// Unticking the last agent leaves nobody: that is Builder-only, with the
	// last agent's scope kept under it rather than taken away.
	others := 0
	for _, a := range st.Agents {
		if a.On && a.ID != agent {
			others++
		}
	}
	if others == 0 && !st.Global {
		return toolWhoFlags(db, user, name, true, false)
	}
	return toolScopeToggle(prov, db, user, name, agent, false)
}

// toolWhoOnly makes exactly these agents the ones that use the tool (any that
// no longer exist are skipped) and clears both flags. Returns
// http.StatusNoContent when it applied a selection, http.StatusOK with nothing
// applied when none of the agents exist any more.
func toolWhoOnly(prov ScopeProvider, db Database, user, name string, ids []string) (int, error) {
	st, _ := prov.State(db, user, name)
	exists := map[string]bool{}
	for _, a := range st.Agents {
		exists[a.ID] = true
	}
	want := map[string]bool{}
	for _, id := range ids {
		if exists[id] {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return toolWhoFlags(db, user, name, true, false)
	}
	if st.Global {
		if code, err := toolScopeToggle(prov, db, user, name, scopeAllAgents, false); err != nil {
			return code, err
		}
	}
	for id := range want {
		if code, err := toolScopeToggle(prov, db, user, name, id, true); err != nil {
			return code, err
		}
	}
	st, _ = prov.State(db, user, name)
	off := map[string]bool{}
	for _, a := range st.Agents {
		// Once per agent: an agent the list names twice (a sub-agent under
		// its parent and again on its own) is turned off once.
		if a.On && !want[a.ID] && !off[a.ID] {
			off[a.ID] = true
			if code, err := toolScopeToggle(prov, db, user, name, a.ID, false); err != nil {
				return code, err
			}
		}
	}
	if code, err := toolWhoFlags(db, user, name, false, false); err != nil {
		return code, err
	}
	return http.StatusNoContent, nil
}

// toolWhoMemory is where a tool's last "Only these agents" selection is kept
// while another choice is in force. A side table rather than a field on the
// tool: writing the tool record re-versions a shared tool.
const toolWhoMemory = "tool_who_agents"

func toolWhoMemoryKey(user, name string) string { return user + "\x00" + name }

// toolWhoRemember keeps the agents ticked now, when "Only these agents" is
// what is in force and somebody is ticked.
func toolWhoRemember(prov ScopeProvider, db Database, user, name string) {
	if db == nil {
		return
	}
	st, found := prov.State(db, user, name)
	if !found || st.Global {
		return
	}
	if row, kept := UserToolByName(db, user, name); kept && (row.Tool.BuilderOnly || row.Tool.BoundOnly) {
		return
	}
	var ids []string
	for _, a := range st.Agents {
		if a.On {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) > 0 {
		db.Set(toolWhoMemory, toolWhoMemoryKey(user, name), ids)
	}
}

// toolWhoRemembered is the last "Only these agents" selection, if any.
func toolWhoRemembered(db Database, user, name string) []string {
	var ids []string
	if db != nil {
		db.Get(toolWhoMemory, toolWhoMemoryKey(user, name), &ids)
	}
	return ids
}

// toolStepsSection serves "Replies may ask for": a toolbox action's declared
// next steps (TempToolAction.FollowUps), the service's own protocol that a
// reply to that action asks the agent for, such as a post the service answers
// with a verification challenge to send back. The injection check reads such
// a reply as that step, not an attack. GET the actions and the declared
// steps; POST {action, follow, on} to declare or drop one, with action "*"
// for the reply to every action (TempTool.FollowUpsFromAny).
//
// The owner's say, as data on the tool: not free text for the check to
// weigh, which an attacker's post could argue past, but a pair of this tool's
// own actions, which it cannot reach.
func toolStepsSection(w http.ResponseWriter, r *http.Request, db Database, user, name string) {
	tool, save, found := ownedToolbox(db, user, name)
	if !found {
		http.Error(w, "not one of your toolboxes", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		type step struct {
			Action string `json:"action"`
			Follow string `json:"follow"`
		}
		actions := []string{}
		steps := []step{}
		for _, f := range tool.FollowUpsFromAny {
			steps = append(steps, step{"*", f})
		}
		for _, a := range tool.Actions {
			actions = append(actions, a.Name)
			for _, f := range a.FollowUps {
				steps = append(steps, step{a.Name, f})
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"actions": actions, "steps": steps,
			"note": "A step the service asks for in its own reply, such as a post it answers with a verification challenge to send back. " +
				"When a reply to the first action asks for the second, the injection check treats it as the service's next step rather than an attack. " +
				"Pick the step, then which actions' replies may ask for it. Only those replies count, and only this tool's own actions can be asked for, so text in anything else the agent reads never does. " +
				"Every action includes what it reads, a feed or a search, where other accounts write: that is safe because only the step you picked is allowed, never anything else."})
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
			Follow string `json:"follow"`
			On     bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := setFollowUp(tool, strings.TrimSpace(body.Action), strings.TrimSpace(body.Follow), body.On)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := save(updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// setFollowUp declares (on) or drops a follow-up of one action of tool,
// held to another action of the same toolbox. action "*" is the reply to
// every action (TempTool.FollowUpsFromAny).
func setFollowUp(tool TempTool, action, follow string, on bool) (TempTool, error) {
	has := map[string]bool{}
	for _, a := range tool.Actions {
		has[a.Name] = true
	}
	if action == "*" {
		if !has[follow] {
			return tool, Error("no action " + follow + " on this tool")
		}
		tool.FollowUpsFromAny = toggled(tool.FollowUpsFromAny, follow, on)
		return tool, nil
	}
	switch {
	case !has[action]:
		return tool, Error("no action " + action + " on this tool")
	case !has[follow]:
		return tool, Error("no action " + follow + " on this tool")
	case action == follow:
		return tool, Error("an action is not its own next step")
	}
	tool.Actions = append([]TempToolAction(nil), tool.Actions...)
	for i, a := range tool.Actions {
		if a.Name != action {
			continue
		}
		tool.Actions[i].FollowUps = toggled(a.FollowUps, follow, on)
	}
	return tool, nil
}

// toggled is list with name in it (on) or out of it, nil when empty.
func toggled(list []string, name string, on bool) []string {
	var kept []string
	for _, f := range list {
		if f != name {
			kept = append(kept, f)
		}
	}
	if on {
		kept = append(kept, name)
	}
	return kept
}

// ownedToolbox finds one of the user's toolboxes, in their pool or kept on
// their agents, and how to save it back: the pool record, or every agent's
// copy, as the page's other per-tool edits do.
func ownedToolbox(db Database, user, name string) (TempTool, func(TempTool) error, bool) {
	if row, inPool := UserToolByName(db, user, name); inPool && row.Tool.Mode == TempToolModeToolbox {
		return row.Tool, func(t TempTool) error {
			if !UpdatePersistentTempTool(db, user, t) {
				return Error("update failed")
			}
			return nil
		}, true
	}
	var agents []string
	var tool TempTool
	for _, st := range ListScopedTools(user) {
		if st.Shadowed || st.Scope != ScopeAgentTool || st.Tool.Name != name || st.Tool.Mode != TempToolModeToolbox {
			continue
		}
		if len(agents) == 0 {
			tool = st.Tool
		}
		agents = append(agents, st.AgentID)
	}
	if len(agents) == 0 {
		return TempTool{}, nil, false
	}
	return tool, func(t TempTool) error {
		for _, id := range agents {
			if err := AttachToolToAgent(AuthDB(), user, id, t); err != nil {
				return err
			}
		}
		return nil
	}, true
}
