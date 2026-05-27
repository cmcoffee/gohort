package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"

	. "github.com/cmcoffee/gohort/core"
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
		"seed-builder":  0,
		"seed-chat":     1,
		"seed-research": 2,
		"seed-kb":       3,
	}
	type pickerRow struct {
		ID    string
		Name  string
		Order int
	}
	var builtIns, customs []pickerRow
	for _, a := range agents {
		if ord, ok := builtInOrder[a.ID]; ok {
			builtIns = append(builtIns, pickerRow{ID: a.ID, Name: a.Name, Order: ord})
		} else {
			customs = append(customs, pickerRow{ID: a.ID, Name: a.Name})
		}
	}
	sort.Slice(builtIns, func(i, j int) bool { return builtIns[i].Order < builtIns[j].Order })
	sort.Slice(customs, func(i, j int) bool { return customs[i].Name < customs[j].Name })
	for _, a := range builtIns {
		agentOpts = append(agentOpts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Built-in"})
	}
	for _, a := range customs {
		agentOpts = append(agentOpts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Custom"})
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
	headHTML := "<script>window.ORCH_TOOL_CATALOG = " + string(catalogJSON) +
		";\nwindow.ORCH_INTERNET_TOOLS = " + string(internetJSON) +
		";</script>\n" + TranscribeRuntimeFlagScript() + "\n" + orchestrateWebAssets

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
					ListURL:       "api/sessions?agent_id={agent_id}",
					LoadURL:       "api/sessions/{id}?agent_id={agent_id}",
					DeleteURL:     "api/sessions/{id}?agent_id={agent_id}",
					TruncateURL:   "api/sessions/{id}?agent_id={agent_id}",
					ListTitle:     "Sessions",
					NewLabel:      "New session",
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
					SendURL:       "api/send",
					CancelURL:     "api/cancel",
					ConfirmURL:    "api/confirm",
					InjectURL:     "api/inject",
					DeepLinkParam: "session",
					LockActivity:  true,
					EmptyText:     "Pick an agent from the rail, then ask anything. The orchestrator plans the steps, the worker runs each one (tool calls appear inline), then the orchestrator replies.",
					Placeholder:   "What do you want to do?",
					Markdown:      true,
					BulkSelect:    true,
					Attachments:   true,
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
							Title:     "Suppress the Reference Memory layer for this turn — no memory_save / memory_search / memory_forget tools, no synthesis auto-ingest, no derived chunks in auto-injection. The agent answers fresh from the user's question plus the Knowledge layer (uploaded files) and Explicit Memory (facts), without its own prior derived findings coloring the response. Use when you want the agent unbiased by its own accumulated history.",
							GetURL:    "api/settings/memory",
							PostURL:   "api/settings/memory/set",
							Field:     "inferred_disabled",
							SendField: "inferred_disabled",
						},
					},
					Actions: []ui.ToolbarAction{
						{Label: "New", Title: "Create a new agent",
							Method: "redirect", URL: "agent/new"},
						{Label: "Edit", Title: "Edit the active agent",
							Method: "client", URL: "orchestrate_edit_agent"},
						{Label: "Clone", Title: "Clone the active agent into a new draft",
							Method: "client", URL: "orchestrate_clone_agent"},
						{Label: "Export", Title: "Download the active agent as a portable JSON recipe",
							Method: "client", URL: "orchestrate_export_agent"},
						{Label: "Import", Title: "Import an agent recipe from a JSON file",
							Method: "client", URL: "orchestrate_import_agent"},
						{Label: "Tools", Title: "Review and edit the active agent's tool allowlist",
							Method: "client", URL: "orchestrate_tools_modal"},
						{Label: "Memory", Title: "Review and prune the active agent's learned notes",
							Method: "client", URL: "orchestrate_memory_modal"},
						{Label: "Knowledge", Title: "Manage what data this agent draws on — your uploaded docs + attached Document Collections.",
							Method: "client", URL: "orchestrate_knowledge_modal"},
						{Label: "Skills", Title: "Manage what this agent can do — allowlist skills (behavior modifications) and experts (consultable brains).",
							Method: "client", URL: "orchestrate_skills_modal"},
						{Label: "Pipelines", Title: "Attach saved multi-stage pipelines to this agent — each becomes a callable run_<pipeline> tool.",
							Method: "client", URL: "orchestrate_pipelines_modal"},
						{Label: "Rules", Title: "Review and edit the active agent's standing rules",
							Method: "client", URL: "orchestrate_rules_modal"},
						{Label: "Save log", Title: "Download the current session as a Markdown transcript (full trace with tool calls). Useful for sharing or debugging.",
							Method: "client", URL: "orchestrate_export_session"},
						{Label: "Delete", Title: "Delete the active agent",
							Method: "client", URL: "orchestrate_delete_agent", Variant: "danger"},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
