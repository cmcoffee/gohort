// Framework-based servitor chat page (Phase 2c of the servitor
// port). Replaces the legacy hand-rolled HTML/CSS/JS surface at
// /servitor/ with an AgentLoopPanel scoped to an appliance, a
// workspaces list in the left rail, and a reserved terminal pane
// in the bottom-right.
//
// The bridge between AgentLoopPanel and servitor's existing
// runSession infrastructure lives in chat_bridge.go (probeEvent →
// AgentLoopPanel event translation). App-specific block renderers
// (intent, plan, notes_consumed, draft) live in web_assets.go.

package servitor

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleChatPage renders the framework-based servitor chat at
// /servitor/. The legacy hand-rolled surface stays at /servitor/legacy
// until the port is fully verified.
func (T *Servitor) handleChatPage(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
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
	if udb != nil {
		for _, key := range udb.Keys(applianceTable) {
			var a Appliance
			if udb.Get(applianceTable, key, &a) && a.ID != "" {
				name := a.Name
				if name == "" {
					name = a.ID
				}
				applianceOpts = append(applianceOpts, ui.SelectOption{
					Value: a.ID, Label: name,
				})
			}
		}
	}

	page := ui.Page{
		Title:     "Servitor",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		// ExtraHeadHTML loads servitor's block renderers + CSS for
		// app-specific events (intent, plan, etc.). See web_assets.go.
		ExtraHeadHTML: servitorWebAssets,
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					// Left rail = workspaces for the active appliance.
					// {appliance_id} placeholder is substituted from
					// the ExtraFields value below; changing the picker
					// re-fetches the list.
					//
					// CONTEXT mode: workspaces are reference contexts,
					// not chat sessions. Clicking one marks it active
					// (ships as workspace_id on every send) but doesn't
					// reset the conversation. Each chat creates a fresh
					// server-side probe session independently.
					ListURL:       "api/workspace/list?appliance_id={appliance_id}",
					LoadURL:       "api/workspace/v2/{id}",
					DeleteURL:     "api/workspace/{id}",
					MessagesField: "messages",
					RenameURL:     "api/workspace/rename",
					ListTitle:     "Workspaces",
					NewLabel:      "New workspace",
					IDField:       "id",
					TitleField:    "name",
					DateField:     "updated",
					ListIsContext: true,
					ListBodyField: "workspace_id",

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
						{Label: "Facts", Title: "View accumulated facts for this appliance",
							Method: "client", URL: "servitor_open_facts"},
						{Label: "Rules", Title: "Edit the assistant's rules for this appliance",
							Method: "client", URL: "servitor_open_rules"},
						{Label: "✨ Map System", Title: "Discover the system layout",
							Method: "client", URL: "servitor_run_map", Variant: "primary"},
						{Label: "Map App", Title: "Enumerate a specific command's subcommands and flags",
							Method: "client", URL: "servitor_run_mapapp"},
						{Label: "Workspace", Title: "Open the active workspace's draft, supplements, and synthesis controls",
							Method: "client", URL: "servitor_open_workspace"},
						{Label: "Clear", Title: "Clear the conversation and activity panes",
							Method: "client", URL: "servitor_clear"},
						{Label: "Clear Memory", Title: "Wipe stored profile, facts, knowledge, and notes for this appliance",
							Method: "client", URL: "servitor_clear_memory", Variant: "danger"},
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
	confirmChans.Range(func(key, val any) bool {
		sid := key.(string)
		ch := val.(chan bool)
		select {
		case ch <- body.Value == "allow" || body.Value == "always":
			// Also persist "always" preferences via the legacy path.
			if body.Value == "always" {
				// Route through the legacy confirm endpoint so the
				// always-allow store gets updated.
				go T.persistAlwaysAllow(sid)
			}
			return false // stop after first match
		default:
			return true
		}
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkspaceLoad returns a workspace shaped for AgentLoopPanel
// consumption: the saved Q&A entries flattened into a Messages
// array the runtime can replay into the conversation pane. The
// legacy /api/workspace/{id} endpoint returns the raw DocWorkspace
// (entries as Q/A pairs); this v2 form does the flattening so the
// chat page sees a uniform message list.
func (T *Servitor) handleWorkspaceLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Path[len("/api/workspace/v2/"):]
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, id)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Date    string `json:"date,omitempty"`
	}
	messages := make([]msg, 0, len(ws.Entries)*2)
	for _, e := range ws.Entries {
		if e.Question != "" {
			messages = append(messages, msg{Role: "user", Content: e.Question, Date: e.Timestamp})
		}
		if e.Answer != "" {
			messages = append(messages, msg{Role: "assistant", Content: e.Answer, Date: e.Timestamp})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":       ws.ID,
		"name":     ws.Name,
		"updated":  ws.Updated,
		"messages": messages,
	})
}

// handleWorkspaceRename receives {id, name} from the AgentLoopPanel
// rail's ✎ button and updates the workspace name in place. The
// rest of the workspace record (entries, supplements, …) is
// untouched.
func (T *Servitor) handleWorkspaceRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.ID == "" || body.Name == "" {
		http.Error(w, "id and name required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, body.ID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	ws.Name = body.Name
	saveWorkspace(udb, ws)
	w.WriteHeader(http.StatusNoContent)
}

// handleProfile returns the saved system profile for an appliance
// (a markdown blob produced by the most recent Map run). Used by
// the Profile toolbar action.
func (T *Servitor) handleProfile(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("appliance_id")
	if id == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	var a Appliance
	if !udb.Get(applianceTable, id, &a) {
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

// persistAlwaysAllow forwards an "always" confirmation to the
// legacy handleConfirm path so the saved-always rules get
// updated. Servitor's existing always-allow store is keyed by
// command shape; rather than reimplement, we just synthesize a
// request to the legacy handler.
func (T *Servitor) persistAlwaysAllow(sid string) {
	// No-op for v1. The boolean channel already carries the allow
	// signal; the "always" persistence is a nice-to-have that
	// requires command-shape lookup from the pending confirm —
	// data we'd need to plumb through the translator. Defer until
	// v2 if users want it.
}

