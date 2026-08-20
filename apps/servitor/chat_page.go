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
	"strings"

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
	// stable order (SSH Hosts → Local Commands → Repositories → Evidence →
	// Workspaces), rather than one flat mixed list.
	var sshOpts, cmdOpts, repoOpts, bundleOpts, toolsetOpts, remoteOpts, workspaceOpts []ui.SelectOption
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
		// Reached through a peer: grouped by HOW it is reached rather than what
		// it is, because that is the fact an operator needs when picking — the
		// stored type is the far side's, and deliberately indistinguishable
		// from a local one everywhere else.
		if strings.TrimSpace(a.PeerName) != "" {
			remoteOpts = append(remoteOpts, ui.SelectOption{
				Value: a.ID, Label: name + " · via " + a.PeerName, Group: "Remote (on a peer)"})
			return
		}
		switch t {
		case "command":
			cmdOpts = append(cmdOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Local Commands"})
		case "repo":
			repoOpts = append(repoOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Repositories"})
		case "bundle":
			bundleOpts = append(bundleOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Evidence Bundles"})
		case "toolset":
			toolsetOpts = append(toolsetOpts, ui.SelectOption{Value: a.ID, Label: name, Group: "Tool-backed Services"})
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
	applianceOpts = append(applianceOpts, bundleOpts...)
	applianceOpts = append(applianceOpts, toolsetOpts...)
	applianceOpts = append(applianceOpts, remoteOpts...)
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
					IDField:              "id",
					TitleField:           "name",
					DateField:            "updated",

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
						{Label: "Access", Title: "Which agents may use this appliance, and the commands they have asked to run on it",
							Method: "client", URL: "servitor_appliance_access"},
						{Label: "Profile", Title: "View the system profile",
							Method: "client", URL: "servitor_open_profile"},
						{Label: "Rules", Title: "Edit the assistant's rules for this appliance",
							Method: "client", URL: "servitor_open_rules"},
						{Label: "Memory", Title: "Manage this appliance's agent memory — Saved facts, Reference Memory, and Graph Memory",
							Method: "client", URL: "servitor_appliance_memory"},
						{Label: "Refresh", Title: "Re-map the selected appliance. Repositories also pull the latest code first.",
							Method: "client", URL: "servitor_run_map", Variant: "primary"},
						// Rarely-used actions collapse into a single "More ▾"
						// overflow menu so the toolbar stays lean.
						{Label: "Map App", Title: "Enumerate a specific command's subcommands and flags",
							Method: "client", URL: "servitor_run_mapapp", Group: "More"},
						{Label: "Permissions", Title: "Choose which categories of risky command run without asking — database writes, file deletion, outbound calls, system control. Unchecked categories still prompt before each command.",
							Method: "client", URL: "servitor_permissions", Group: "More"},
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
// (allow/always/deny) back to the boolean signal the waiting
// runSession's confirm channel expects.
//
// The routing is by OWNER, and it has to be. The card's id is generated per
// event by the bridge and carries no session, so this endpoint cannot address
// a channel — it selects one. It used to select the first channel in the whole
// process that would accept, on the reasoning that "servitor has at most one
// in-flight session per user at a time". That is true per user and false per
// process: with two people working at once, either one's click answered
// whichever session the map happened to visit first. Since the thing being
// answered is the approval gate for running a destructive command over SSH,
// that made one user's Allow able to release another user's command, recorded
// against the other user.
//
// So the candidate set is now the caller's OWN interactive sessions, which is
// what the original reasoning assumed it was. Within that set the ambiguity
// the old comment described does survive: a user with two chat sessions both
// waiting gets the answer delivered to whichever Range reaches first. Closing
// that needs the session id carried in the confirm event itself, which is a
// change to the bridge's event shape rather than to this handler.
func (T *Servitor) handleChatConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Identity first: it is the routing key, not just a gate.
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	allow := body.Value == "allow" || body.Value == "always"

	delivered := deliverConfirm(user, allow)
	if delivered == "" {
		// Say so rather than answering 204. A silent success settles the card
		// as answered while the run stays blocked, which reads as servitor
		// ignoring the click; the runtime's catch re-enables the buttons.
		http.Error(w, "no pending confirmation on any of your sessions — it may have already been answered, or the run may have ended", http.StatusConflict)
		return
	}
	Log("[servitor] %s answered confirm on session %s: allow=%v", user, delivered, allow)
	w.WriteHeader(http.StatusNoContent)
}

// deliverConfirm hands one operator decision to a session that user owns and
// returns the session id it went to, or "" when none was waiting.
//
// Split out of the handler because this is the whole of the access decision:
// which channels a given user may write to. Keeping it a plain function of
// (user, allow) is what lets the rule be tested without standing up a session
// cookie, which is the difference between a rule that is checked and a rule
// that is merely written down.
func deliverConfirm(user string, allow bool) string {
	if strings.TrimSpace(user) == "" {
		return "" // no identity, no candidates — never fall through to "any"
	}
	delivered := ""
	confirmChans.Range(func(key, val any) bool {
		p, ok := val.(pendingConfirm)
		if !ok || p.ch == nil || !p.interactive {
			return true
		}
		// Empty owner never matches, so an untagged channel is unanswerable
		// rather than answerable by everyone.
		if p.owner == "" || p.owner != user {
			return true
		}
		select {
		case p.ch <- allow:
			delivered, _ = key.(string)
			return false // stop after the first match
		default:
			return true // nothing waiting on this one; keep looking
		}
	})
	return delivered
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
