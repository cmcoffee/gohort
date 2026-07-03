// Servitor chat page. AgentLoopPanel scoped to an appliance, a
// chat-sessions list in the left rail, and a reserved terminal pane
// in the bottom-right.
//
// runSession emits probeEvents into a per-session queue; chat_bridge.go
// translates each event into the shape AgentLoopPanel understands.
// App-specific block renderers (intent, plan, notes_consumed, draft)
// live in web_assets.go.

package servitor

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleChatPage renders the servitor chat at /servitor/.
func (T *Servitor) handleChatPage(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}

	// Load appliances for this user so the picker has options at
	// render time. The select is part of ExtraFields so its value
	// (the appliance id) rides on every send body as `appliance_id`.
	// OptionPairs lets us show the human-readable name while keeping
	// the opaque UUID as the form value.
	applianceOpts := []ui.SelectOption{
		{Value: "", Label: "— select appliance —"},
	}
	// Client-side id→type map so the page can adapt per-type UI (e.g. hide
	// the terminal pane for repo appliances, which have nothing to attach to).
	applianceTypes := map[string]string{}
	// Bucket by type so the picker renders separated <optgroup> sections in a
	// stable order (SSH Hosts → Local Commands → Repositories), rather than one
	// flat mixed list.
	var sshOpts, cmdOpts, repoOpts, workspaceOpts []ui.SelectOption
	seen := map[string]bool{}
	add := func(a Appliance, sharedByOther bool) {
		if a.ID == "" || seen[a.ID] {
			return
		}
		seen[a.ID] = true
		name := a.Name
		if name == "" {
			name = a.ID
		}
		if sharedByOther {
			name += " (shared)"
		}
		t := a.Type
		if t == "" {
			t = "ssh"
		}
		applianceTypes[a.ID] = t
		switch t {
		case "command":
			cmdOpts = append(cmdOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Local Commands"})
		case "repo":
			repoOpts = append(repoOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Repositories"})
		case "workspace":
			workspaceOpts = append(workspaceOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Workspaces"})
		default:
			sshOpts = append(sshOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "SSH Hosts"})
		}
	}
	// The user's own appliances.
	if udb != nil {
		for _, key := range udb.Keys(applianceTable) {
			var a Appliance
			if udb.Get(applianceTable, key, &a) {
				add(a, false)
			}
		}
	}
	// Shared appliances owned by others — usable by everyone.
	for id, owner := range T.listSharedAppliances() {
		if owner == userID || seen[id] {
			continue
		}
		if ownerUDB := UserDB(T.DB, owner); ownerUDB != nil {
			var a Appliance
			if ownerUDB.Get(applianceTable, id, &a) {
				add(a, true)
			}
		}
	}
	applianceOpts = append(applianceOpts, sshOpts...)
	applianceOpts = append(applianceOpts, cmdOpts...)
	applianceOpts = append(applianceOpts, repoOpts...)
	applianceOpts = append(applianceOpts, workspaceOpts...)
	typeMapJSON, _ := json.Marshal(applianceTypes)
	applianceTypesScript := "<script>window.servitorApplianceTypes = " + string(typeMapJSON) + ";</script>"

	page := ui.Page{
		Title:     "Servitor",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		// ExtraHeadHTML loads servitor's block renderers + CSS for
		// app-specific events (intent, plan, etc.). See web_assets.go.
		ExtraHeadHTML: applianceTypesScript + servitorWebAssets + applianceMemoryModalScript,
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					// Left rail = chat sessions for the active appliance,
					// auto-created on the first message (see sessions.go).
					// {appliance_id} is substituted from the ExtraFields
					// value below; changing the picker re-fetches the list.
					// SESSION mode (default): clicking a row replays its
					// transcript and binds future sends to that session id;
					// the id rides back as session_id on every send.
					ListURL:       "api/sessions?appliance_id={appliance_id}",
					LoadURL:       "api/sessions/{id}?appliance_id={appliance_id}",
					DeleteURL:     "api/sessions/{id}?appliance_id={appliance_id}",
					MessagesField: "messages",
					ListTitle:     "Sessions",
					NewLabel:      "New session",
					// Same chat-app layout orchestrate uses: rail
					// extends full-height on the left, topbar lives
					// in the chat pane, Appliance picker lifts into a
					// top bundle alongside the action buttons.
					ListPosition:         "top",
					ExtraFieldsInSidebar: true,
					IDField:       "id",
					TitleField:    "name",
					DateField:     "updated",

					// SendURL returns JSON {session_id}; runtime then
					// subscribes to EventsURL. This separation buys
					// reconnect resilience — refreshing the page during
					// an in-flight chat picks up the same event stream.
					SendURL:       "api/chat",
					EventsURL:     "api/chat/v2/events",
					CancelURL:     "api/cancel",
					ConfirmURL:    "api/chat/v2/confirm",
					InjectURL:     "api/inject",
					DeepLinkParam: "session",
					EmptyText:     "Pick an appliance below, then ask anything.",
					Placeholder:   "Ask about this system…",
					Markdown:      true,
					Attachments:   true,
					BulkSelect:    true,
					// Terminal pane — framework provides the slot;
					// xterm.js wiring lives in apps/servitor/web_assets.go
					// where the script tags load from <head>. The
					// {appliance_id} placeholder is filled at connect
					// time from the appliance picker's current value.
					Terminal: &ui.AgentTerminal{
						URL:   "api/terminal?id={appliance_id}",
						Title: "Terminal",
					},
					// Appliance picker rides on every send body as
					// appliance_id. Toolbar actions reference it too
					// (Facts / Rules / Map).
					ExtraFields: []ui.ChatField{
						{
							Name:        "appliance_id",
							Label:       "Appliance",
							Type:        "select",
							OptionPairs: applianceOpts,
						},
					},
					Actions: []ui.ToolbarAction{
						{Label: "New", Title: "Create a new appliance",
							Method: "client", URL: "servitor_new_appliance"},
						{Label: "Edit", Title: "Edit the active appliance",
							Method: "client", URL: "servitor_edit_appliance"},
						{Label: "Profile", Title: "View the system profile",
							Method: "client", URL: "servitor_open_profile"},
						{Label: "Rules", Title: "Edit the assistant's rules for this appliance",
							Method: "client", URL: "servitor_open_rules"},
						{Label: "Memory", Title: "Manage this appliance's agent memory — Saved facts, Reference Memory, and Graph Memory",
							Method: "client", URL: "servitor_appliance_memory"},
						{Label: "✨ Map System", Title: "Discover the system layout",
							Method: "client", URL: "servitor_run_map", Variant: "primary"},
						{Label: "Refresh Repo", Title: "Re-clone the selected repository to pick up new code (repository appliances only)",
							Method: "client", URL: "servitor_refresh_repo"},
						// Rarely-used actions collapse into a single "More ▾"
						// overflow menu so the toolbar stays lean.
						{Label: "Map App", Title: "Enumerate a specific command's subcommands and flags",
							Method: "client", URL: "servitor_run_mapapp", Group: "More"},
						{Label: "Copy session", Title: "Copy the full session as markdown — every user message, every assistant round, every tool call/result — for pasting into a prompt-tuning chat.",
							Method: "client", URL: "copy_session", Group: "More"},
						{Label: "Export knowledge", Title: "Download this system's accumulated knowledge (profile, facts, techniques, logs) as a markdown file — credentials excluded — for handing to Claude to help build or improve a support tool.",
							Method: "client", URL: "servitor_export_knowledge", Group: "More"},
						{Label: "Clear Memory", Title: "Wipe stored profile, facts, knowledge, and notes for this appliance",
							Method: "client", URL: "servitor_clear_memory", Variant: "danger", Group: "More"},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

// handleChatConfirm is the AgentLoopPanel-facing confirm endpoint.
// The runtime POSTs {id, value} when the operator clicks one of
// the confirm card's action buttons. We translate the value
// (allow/always/deny) back to the boolean signal the legacy
// runSession's confirm channel expects.
func (T *Servitor) handleChatConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The runtime POSTs JSON {id, value}; the legacy handler at
	// /api/confirm uses query-param form. Translate by forwarding.
	if err := r.ParseForm(); err == nil {
		// no-op
	}
	var body struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Look up the active session and route the answer through the
	// existing confirm-channel infrastructure. The confirm_id from
	// the AgentLoopPanel is the translator-generated one, not the
	// session id; we need the active session id to route. For now,
	// use the same heuristic as the demo: route to any pending
	// session that has a confirm channel waiting. The legacy
	// handler reads from confirmChans keyed by session id.
	//
	// In practice servitor has at most one in-flight session per
	// user at a time, so this is unambiguous. A future revision
	// could carry the session id in the confirm event's id for
	// tighter routing.
	confirmChans.Range(func(_, val any) bool {
		ch := val.(chan bool)
		select {
		case ch <- body.Value == "allow" || body.Value == "always":
			return false // stop after first match
		default:
			return true
		}
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleProfile returns the saved system profile for an appliance
// (a markdown blob produced by the most recent Map run). Used by
// the Profile toolbar action.
func (T *Servitor) handleProfile(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("appliance_id")
	if id == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	a, _, _, found := T.resolveAppliance(userID, udb, id)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"name":    a.Name,
		"profile": a.Profile,
		"scanned": a.Scanned,
	})
}

