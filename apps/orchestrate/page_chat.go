package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleChatPage renders the orchestrate chat surface — a single
// AgentLoopPanel with an Agent picker in the ExtraFields strip
// (mirroring servitor's appliance dropdown). Switching agents
// re-fetches the conversation list and scopes every send to the
// active agent. CRUD on the agent itself (edit / clone / delete /
// new) is exposed via toolbar actions that pivot off whatever the
// dropdown currently has selected.
func (T *OrchestrateApp) handleChatPage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Load the user's visible agents into the picker. listAgents
	// already merges in-code seeds with any per-user shadows, so each
	// agent appears exactly once whether or not the user has tweaked
	// it. Built-ins (Builder, Chat, Research — in that explicit order)
	// at the top under the "Built-in" optgroup; everything else under
	// "Custom" sorted alphabetically. The "— select agent —"
	// placeholder is bare (no group) so it lands above both groups.
	agentOpts := []ui.SelectOption{
		{Value: "", Label: "— select agent —"},
	}
	agents := listAgents(udb, user)
	builtInOrder := map[string]int{
		"seed-chat":     0,
		"seed-builder":  1,
		"seed-research": 2,
		// seed-kb is intentionally absent: it's a clone-only TEMPLATE (Builder
		// clones it into real KB agents), skipped from the picker below so it's
		// never selectable/runnable directly.
	}
	// cortexAgents maps each channel agent's id (a.Cortex) to its home-thread
	// session id, so the client both knows which agents get the channel nav AND
	// what session to pin them to — without core/ui hardcoding the id scheme.
	// A present key = channel agent; the value = its pinned session.
	cortexAgents := map[string]string{}
	type pickerRow struct {
		ID    string
		Name  string
		Order int
		App   string // owning app, for App Agents grouping/sort
	}
	// subAgentsByParent groups every owned sub-agent under its parent
	// ID so the chat UI can render a contextual secondary picker. The
	// main dropdown only shows top-level agents (Hidden/OwnedBy filtered
	// out); the secondary picker appears only when the selected parent
	// has children. The secondary picker is what lets you chat directly
	// with a sub-agent for testing without needing to dispatch from the
	// parent.
	subAgentsByParent := map[string][]map[string]string{}
	// Three picker groups: the seeds (Built-in), the user's cortex-enabled
	// conversation agents (a standing brain — carved out so they're easy to
	// find), and everything else custom.
	var builtIns, conversation, customs, appAgents []pickerRow
	for _, a := range agents {
		if a.Cortex {
			cortexAgents[a.ID] = cortexSessionID(a.ID)
		}
		// Sub-agents (OwnedBy set → Hidden=true via enforceSubAgentPosture)
		// stay out of the main picker — they appear in the secondary
		// picker keyed by parent ID.
		if a.OwnedBy != "" {
			subAgentsByParent[a.OwnedBy] = append(subAgentsByParent[a.OwnedBy], map[string]string{
				"id":   a.ID,
				"name": a.Name,
			})
			continue
		}
		// Clone-only template seeds (e.g. seed-kb) are Builder's raw material,
		// not something a user runs directly — keep them out of the picker
		// entirely (all groups). Builder still clones them by ID.
		if isCloneOnlySeed(a.ID) {
			continue
		}
		// Hidden=true used to filter the agent out of THIS picker too —
		// which conflated two audiences. The flag's intent is "hide from
		// the fleet so other agents can't dispatch to me" (the runner's
		// renderAvailableAgentsBlock honors it; agents(action="run")
		// refuses it absent an AllowedDispatchTargets carve-out). The
		// user's own Agency picker is their home for managing their
		// agents — they need every top-level agent they own visible
		// here, Hidden or not. Filtering Hidden here just made KB agents
		// (published to /agents/ but marked Hidden to keep them out of
		// the fleet) silently disappear from Agency. Don't.
		// App agents (registered by an app via appagents.RegisterAppAgent) get
		// their own group, separate from the user's own agents — checked
		// first so a Cortex-enabled app agent still lands here, not in
		// Conversation Agents.
		if spec, isApp := appagents.AppAgentByID(a.ID); isApp {
			// App agents aren't the user's own to manage, so — unlike user
			// agents, which stay visible in their own Agency picker even when
			// Hidden — a Hidden app agent stays OUT of the picker. Lets a
			// secret-sauce or demo app agent register without cluttering the
			// menu (Hidden still also drops it from fleet dispatch).
			if a.Hidden {
				continue
			}
			appAgents = append(appAgents, pickerRow{ID: a.ID, Name: a.Name, App: spec.OwningApp})
		} else if ord, ok := builtInOrder[a.ID]; ok {
			builtIns = append(builtIns, pickerRow{ID: a.ID, Name: a.Name, Order: ord})
		} else if a.Cortex {
			conversation = append(conversation, pickerRow{ID: a.ID, Name: a.Name})
		} else {
			customs = append(customs, pickerRow{ID: a.ID, Name: a.Name})
		}
	}
	sort.Slice(builtIns, func(i, j int) bool { return builtIns[i].Order < builtIns[j].Order })
	sort.Slice(conversation, func(i, j int) bool { return conversation[i].Name < conversation[j].Name })
	sort.Slice(customs, func(i, j int) bool { return customs[i].Name < customs[j].Name })
	// App agents sort by owning app, then name — so a deployment with several
	// app-registered agents reads grouped-by-app within the one optgroup.
	sort.Slice(appAgents, func(i, j int) bool {
		if appAgents[i].App != appAgents[j].App {
			return appAgents[i].App < appAgents[j].App
		}
		return appAgents[i].Name < appAgents[j].Name
	})
	for _, a := range builtIns {
		agentOpts = append(agentOpts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Built-in"})
	}
	for _, a := range conversation {
		agentOpts = append(agentOpts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Conversation Agents"})
	}
	for _, a := range customs {
		agentOpts = append(agentOpts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Specialized Agents"})
	}
	// App Agents last — framework/app-provided, below the user's own agents.
	// Each app gets its OWN optgroup named after the app, so the menu reads
	// "<App> → its agents" rather than a flat "App Agents" bucket. Falls back
	// to "App Agents" when an app didn't supply a label.
	for _, a := range appAgents {
		group := a.App
		if group == "" {
			group = "App Agents"
		}
		agentOpts = append(agentOpts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: group})
	}

	// Default the dropdown to the requested agent if the URL carries
	// `?agent=<id>` AND the user can see it — used by the editor's
	// save-redirect so you land back on the agent you just edited
	// instead of snapping to the Chat seed. Falls back to "seed-chat"
	// when missing or unauthorized.
	defaultAgent := "seed-chat"
	if a := r.URL.Query().Get("agent"); a != "" {
		for _, opt := range agentOpts {
			if opt.Value == a {
				defaultAgent = a
				break
			}
		}
	}

	// Embed the worker-tool catalog into the page so the Tools modal
	// can render checkboxes without an extra round-trip. Same source
	// the editor uses (availableWorkerToolOptions) — sorted by group
	// + name. Inline JSON is fine; the list is short (<50 entries).
	catalogJSON, _ := json.Marshal(availableWorkerToolOptions(user))
	// Parallel list of tool names that contact the network. The Tools
	// button label uses this to subtract network-capability tools from
	// the count when Private mode is enabled (which hides them at
	// runtime — see filteredWorkerTools in runner.go). Kept as a
	// separate JS var rather than a field on SelectOption so the
	// shared SelectOption type stays domain-agnostic.
	internetJSON, _ := json.Marshal(internetWorkerToolNames())
	// subAgentsByParent → JS map for the secondary picker. Empty map
	// (no sub-agents in the fleet) is fine — the JS hides the picker
	// when the selected parent has no children.
	subAgentsJSON, _ := json.Marshal(subAgentsByParent)
	cortexAgentsJSON, _ := json.Marshal(cortexAgents)
	headHTML := "<script>window.ORCH_TOOL_CATALOG = " + string(catalogJSON) +
		";\nwindow.ORCH_INTERNET_TOOLS = " + string(internetJSON) +
		";\nwindow.ORCH_SUB_AGENTS = " + string(subAgentsJSON) +
		";\nwindow.ORCH_CHANNEL_AGENTS = " + string(cortexAgentsJSON) +
		";</script>\n" + TranscribeRuntimeFlagScript() + "\n" + orchestrateWebAssets

	// Builder handoff: a ?builder_brief=<id> deep-link (from the send_to_builder
	// tool or the toolbar "Send to Builder" button) carries a one-shot brief.
	// Read + consume it server-side and hand it to the chat panel as AutoSend,
	// so Builder receives it on mount through its own send path — no fragile
	// client-side fetch + DOM injection.
	builderBrief := ""
	if bid := strings.TrimSpace(r.URL.Query().Get("builder_brief")); bid != "" {
		if udb := UserDB(T.DB, user); udb != nil {
			var brief builderBriefRecord
			if udb.Get(builderBriefTable, bid, &brief) {
				udb.Unset(builderBriefTable, bid) // one-shot
				builderBrief = brief.Text
			}
		}
	}

	page := ui.Page{
		Title:         "Agency",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "100%",
		ExtraHeadHTML: headHTML,
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					// All session URLs carry {agent_id}; the runtime
					// substitutes it from the ExtraFields select value
					// every fetch. Sessions live in their agent's bucket
					// so switching the picker swaps the rail contents.
					ListURL:   "api/sessions?agent_id={agent_id}",
					LoadURL:   "api/sessions/{id}?agent_id={agent_id}",
					DeleteURL: "api/sessions/{id}?agent_id={agent_id}",
					// Channels rail section — its own region above Sessions with
					// add/edit/remove. {id} → channel id on delete; save upserts.
					ChannelsURL:      "api/channels?agent_id={agent_id}",
					ChannelSaveURL:   "api/channels?agent_id={agent_id}",
					ChannelDeleteURL: "api/channels?id={id}",
					ChannelAgentsURL: "api/agents",
					// Canonical default wake rule, so the channel editor can offer
					// "Reset to default" on the gatekeeper rules (source of truth is Go).
					DefaultGatekeeperRule: DefaultDMGatekeeperRule,
					TruncateURL:           "api/sessions/{id}?agent_id={agent_id}",
					// Per-turn scrub (✕ on each bubble) — replaces the separate
					// History view's row-delete; works on every thread, not just
					// the home thread. See docs/channel-model.md.
					MessageScrub:   true,
					RenameURL:      "api/sessions/rename?agent_id={agent_id}",
					MarkAllReadURL: "api/sessions/mark-all-read?agent_id={agent_id}",
					ListTitle:      "Sessions",
					NewLabel:       "New session",
					// Same chat-app layout the public /agents/ surface
					// uses: sessions rail extends full-height on the
					// left, topbar lives inside the chat pane (not
					// spanning the rail), action buttons sit on the
					// right of the topbar.
					ListPosition: "top",
					// Move the Agent picker into the rail (above the
					// session list). The rail's sessions are scoped to
					// the active agent, so the picker reads naturally
					// as a rail header rather than a topbar control.
					ExtraFieldsInSidebar: true,
					SendURL:              "api/send",
					CancelURL:            "api/cancel",
					ConfirmURL:           "api/confirm",
					InjectURL:            "api/inject",
					// Enables the run-resume probe in the chat panel:
					// on session-load the runtime asks
					// api/runs/active?session_id=… and, if there's an
					// in-flight run, opens api/runs/<id>/stream to pick
					// up live where the prior client left off. See
					// runs.go / runs_http.go for the server contract.
					RunsURLBase:   "api/runs/",
					DeepLinkParam: "session",
					AutoSend:      builderBrief,
					LockActivity:  true,
					EmptyText:     "Pick an agent from the rail, then ask anything. The orchestrator plans the steps, the worker runs each one (tool calls appear inline), then the orchestrator replies.",
					Placeholder:   "What do you want to do?",
					// Orchestrator-mode agents (the Operator) swap the session
					// list for this nav; normal agents ignore it entirely.
					// No "Channel" or "History" rows anymore (channel model — see
					// docs/channel-model.md): the home thread is just the pinned
					// session at the top of the list (open it to read it), and
					// per-turn scrubbing is the inline ✕ on each bubble (works on
					// every thread, not only the home thread). What remains is the
					// fleet-management views + the channel-wide actions.
					OrchestratorNav: []ui.OrchestratorNavItem{
						// Cards layout so each agent's mission (the standing brief it
						// runs with — "what it's told to do") renders as a detail line
						// under the name, alongside its schedule / status / next run.
						{Label: "Enabled agents", Source: "api/console/agents", Layout: "cards", RowActions: []ui.OrchestratorRowAction{
							{Label: "Pause", Method: "POST", URL: "api/console/agents/pause", HideIf: "_paused"},
							{Label: "Resume", Method: "POST", URL: "api/console/agents/resume", OnlyIf: "_paused"},
							{Label: "Delete", Method: "DELETE", URL: "api/console/agents/delete", Variant: "danger", Confirm: "Delete this standing agent and cancel its schedule?"},
						}},
						{Label: "Event monitors", Source: "api/console/monitors", RowActions: []ui.OrchestratorRowAction{
							{Label: "Pause", Method: "POST", URL: "api/console/monitors/pause", HideIf: "_paused"},
							{Label: "Resume", Method: "POST", URL: "api/console/monitors/resume", OnlyIf: "_paused"},
							{Label: "Delete", Method: "DELETE", URL: "api/console/monitors/delete", Variant: "danger", Confirm: "Delete this event monitor?"},
						}},
						// Permissions — pinned ABOVE the session list (it's an action
						// queue, not browse-config), and combines BOTH pending
						// approval requests AND the standing grants you've given on
						// one page. Approve/Always/Deny show only on pending rows
						// (_pending); Revoke only on granted rows (_granted). The
						// rail badge counts just the pending ones.
						// Permissions: pending requests render as approval cards
						// (Deny / Allow once / Always allow); standing-policy rows
						// render with a segmented Always allow · Needs approval ·
						// Blocked control + Remove. _pending vs _managed picks which.
						{Label: "Permissions", Icon: "🔑", Source: "api/console/permissions", Pinned: true, BadgeField: "_pending", Layout: "cards",
							StateField: "_policy",
							StateOptions: []ui.OrchestratorStateOption{
								{Label: "Always allow", Value: "allow", URL: "api/console/permissions/policy"},
								{Label: "Needs approval", Value: "ask", URL: "api/console/permissions/policy"},
								{Label: "Blocked", Value: "block", URL: "api/console/permissions/policy"},
							},
							RowActions: []ui.OrchestratorRowAction{
								{Label: "Deny", Method: "POST", URL: "api/console/approvals/deny", Variant: "danger", OnlyIf: "_pending"},
								{Label: "Allow once", Method: "POST", URL: "api/console/approvals/approve", OnlyIf: "_pending", Confirm: "Approve and run this once?"},
								{Label: "Always allow", Method: "POST", URL: "api/console/approvals/always", Variant: "success", OnlyIf: "_pending", Confirm: "Approve, run, and always allow this in future?"},
								{Label: "Remove", Method: "POST", URL: "api/console/permissions/remove", Variant: "danger", OnlyIf: "_managed", Confirm: "Forget this permission entirely? It returns to the default (needs approval)."},
							}},
						{Label: "Clear channel", ActionURL: "api/console/channel/clear", Variant: "warning",
							Confirm: "Clear this channel's conversation and rolling summary? Your monitors, standing agents, and approvals are kept."},
						{Label: "Decommission", ActionURL: "api/console/channel/decommission", Variant: "danger",
							Confirm: "Decommission: permanently delete this channel's event monitors and standing agents, and cancel all pending approvals? This cannot be undone."},
					},
					// core/ui is domain-agnostic: it reads the opt-in agent set
					// from the named window-global this app sets — an agentId→
					// pinned-session map — so it knows which agents get the nav
					// and what thread to pin each to, without hardcoding the id
					// scheme.
					AltNavFlag:      "ORCH_CHANNEL_AGENTS",
					AltPrimaryLabel: "Cortex",
					// "+ New ▾" offers a clean-room session. Picking it opens a
					// fresh thread and arms incognito on the first send, so the
					// runner stamps the session as a clean room at creation: no
					// cortex standing context, no memory/facts carried in, and
					// nothing stored back. A creation-time choice, which is why
					// it lives here rather than in the per-turn Modes pills.
					NewVariants: []ui.NewSessionVariant{
						{
							Label:  "New incognito session",
							Title:  "Start a clean-room session: no cortex standing context and no memory/facts carried in, and nothing stored back. Set when the session is created.",
							Extras: map[string]any{"incognito": true},
						},
					},
					Markdown:    true,
					BulkSelect:  true,
					Attachments: true,
					ExtraFields: []ui.ChatField{
						{
							Name:        "agent_id",
							Label:       "Agent",
							Type:        "select",
							OptionPairs: agentOpts,
							// Defaults to the Chat seed normally; when the
							// editor's save-redirect carries `?agent=<id>`
							// we land on THAT agent instead so the user
							// stays where they were editing.
							Default: defaultAgent,
						},
					},
					Modes: []ui.ChatMode{
						{
							Label:     "Private",
							Title:     "Drop internet tools (web_search, fetch_url, browse_page, …) for this and subsequent turns. Local + agent-management tools still work.",
							GetURL:    "api/settings/private",
							PostURL:   "api/settings/private/set",
							Field:     "private_mode",
							SendField: "private_mode",
						},
						{
							Label:     "Clean",
							Title:     "Suppress the Reference Memory layer for this turn — memory_save / memory_search / memory_forget stripped from the agent's catalog so it can't write to or read from its accumulated derived store. The agent answers fresh from the user's question plus the Knowledge layer (uploaded files) and Explicit Memory (facts), without prior memory_save findings coloring the response. Use when you want the agent unbiased by its own accumulated history.",
							GetURL:    "api/settings/memory",
							PostURL:   "api/settings/memory/set",
							Field:     "inferred_disabled",
							SendField: "inferred_disabled",
						},
					},
					// Incognito is NOT a live toggle — it's a creation-time
					// choice (it severs cortex standing context + memory for the
					// NEW session and stores nothing back). Presenting it as a
					// pill alongside Private/Clean read as a current-thread
					// switch, which it never was. It now rides the rail's
					// "+ New ▾" menu, where it actually applies: picking it
					// opens a fresh session and arms incognito onto that
					// session's first send (the server stamps it at creation).
					// Edit stays a flat button (the one you reach for mid-chat);
					// everything else collapses into Agent / Configure / Session
					// overflow menus so the toolbar isn't a wall of 15 buttons.
					Actions: []ui.ToolbarAction{
						{Group: "Agent", Label: "Edit", Title: "Edit the active agent",
							Method: "client", URL: "orchestrate_edit_agent"},
						{Group: "Agent", Label: "Create", Title: "Create a new agent. When a parent agent is currently selected, asks whether to mint a top-level agent or a sub-agent owned by that parent (sub-agent layout masks public / intake / memory fields).",
							Method: "client", URL: "orchestrate_create_agent"},
						{Group: "Agent", Label: "Clone", Title: "Clone the active agent into a new draft",
							Method: "client", URL: "orchestrate_clone_agent"},
						{Group: "Agent", Label: "Import", Title: "Import an agent recipe from a JSON file",
							Method: "client", URL: "orchestrate_import_agent"},
						{Group: "Agent", Label: "Export", Title: "Download the active agent as a portable JSON recipe",
							Method: "client", URL: "orchestrate_export_agent"},
						{Group: "Agent", Label: "Delete", Title: "Delete the active agent",
							Method: "client", URL: "orchestrate_delete_agent", Variant: "danger"},
						{Group: "Configure", Label: "Tools", Title: "Review and edit the active agent's tool allowlist",
							Method: "client", URL: "orchestrate_tools_modal"},
						{Group: "Configure", Label: "Memory", Title: "Review and prune the active agent's learned notes",
							Method: "client", URL: "orchestrate_memory_modal"},
						{Group: "Configure", Label: "Knowledge", Title: "Manage what data this agent draws on — your uploaded docs + attached Document Collections.",
							Method: "client", URL: "orchestrate_knowledge_modal"},
						{Group: "Configure", Label: "Rules", Title: "Review and edit the active agent's standing rules",
							Method: "client", URL: "orchestrate_rules_modal"},
						{Group: "Configure", Label: "Skills", Title: "Manage what this agent can do — allowlist skills (behavior modifications) and experts (consultable brains).",
							Method: "client", URL: "orchestrate_skills_modal"},
						{Group: "Configure", Label: "Pipelines", Title: "Attach saved multi-stage pipelines to this agent — each becomes a callable run_<pipeline> tool.",
							Method: "client", URL: "orchestrate_pipelines_modal"},
						{Group: "Session", Label: "Copy session", Title: "Copy the full session as markdown — every user message, every assistant round, every tool call/result — for pasting into a prompt-tuning chat.",
							Method: "client", URL: "copy_session"},
						{Group: "Session", Label: "Save log", Title: "Download the current session as a Markdown transcript (full trace with tool calls). Useful for sharing or debugging.",
							Method: "client", URL: "orchestrate_export_session"},
						{Group: "Session", Label: "Send to Builder", Title: "Had to correct this agent? Send the session to Builder so it can see where the agent went wrong and improve its prompt, rules, or tools.",
							Method: "client", URL: "orchestrate_send_to_builder"},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
