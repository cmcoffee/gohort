package orchestrate

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleAgentPage routes the agent editor.
//
//	GET /agent/new        — create a new blank agent
//	GET /agent/{id}       — edit existing agent
func (T *OrchestrateApp) handleAgentPage(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/agent/")
	if rest == "" || strings.Contains(rest, "/") {
		http.NotFound(w, r)
		return
	}
	if rest == "new" {
		T.renderAgentEditor(w, r, udb, "")
		return
	}
	T.renderAgentEditor(w, r, udb, rest)
}

// renderAgentEditor shows the agent editor. When id is empty the form
// is blank (create mode); otherwise FormPanel.Source loads the
// existing record so fields prefill.
//
// Layout depends on whether the agent is a sub-agent (OwnedBy set).
// Sub-agents are focused capability components called by their parent
// via dispatch — they don't have public surfaces, intake forms, memory,
// or explorer mode, so the editor hides those sections to keep the
// surface clean and prevent accidental misconfiguration. enforceSubAgentPosture
// at loadAgent is the runtime safety net even if the UI ever leaks.
func (T *OrchestrateApp) renderAgentEditor(w http.ResponseWriter, r *http.Request, udb Database, id string) {
	source := ""
	title := "New agent"
	subAgent := false
	parentName := ""
	// owned_by from the URL marks "create as a sub-agent owned by this
	// parent" — set by the Create button's confirm dialog. Drives the
	// sub-agent layout (mask publishing / intake / memory) and gets
	// baked into the form via a hidden field so the POST persists the
	// parent link.
	subAgentParentID := ""
	if id == "" {
		if v := strings.TrimSpace(r.URL.Query().Get("owned_by")); v != "" {
			if parent, ok := loadAgent(udb, v); ok {
				subAgentParentID = v
				subAgent = true
				parentName = parent.Name
				title = "New sub-agent"
			}
		}
	}
	if id != "" {
		source = "../api/agents/" + id
		title = "Edit agent"
		// Agents live in the user's per-user DB (udb), NOT the global
		// T.DB — reading from the wrong store leaves OwnedBy empty and
		// the editor falls back to the top-level shape (intake form,
		// publishing, etc. still rendering for what's actually a
		// sub-agent).
		if rec, ok := loadAgent(udb, id); ok && rec.OwnedBy != "" {
			subAgent = true
			if parent, pok := loadAgent(udb, rec.OwnedBy); pok {
				parentName = parent.Name
			}
			title = "Edit sub-agent"
		}
	}
	// On save, redirect back to the chat surface AND pre-select the
	// agent the user was editing — landing on Chat (the dropdown's
	// default) after editing a research agent makes the save feel
	// disconnected. For new agents we don't know the id yet (server
	// assigns on save), so they land on the dropdown default and the
	// user picks manually.
	redirectURL := ".."
	if id != "" {
		redirectURL = "..?agent=" + id
	}

	// (Skill / expert / collection pickers moved out of the editor
	// — curation now lives on the in-chat Knowledge + Skills
	// buttons so the editor stays focused on full agent shape.)

	fields := []ui.FormField{
		{Type: "header", Label: "Identity",
			Help: "Name + short description shown in the agent picker."},
		{Field: "name", Type: "text", Label: "Name", Placeholder: "Research helper",
			SuggestURL: "../api/agents/suggest"},
		{Field: "description", Type: "text", Label: "Description", Placeholder: "What this agent is for.",
			SuggestURL: "../api/agents/suggest"},
		{Type: "header", Label: "Persona",
			Help: "How the agent thinks and decomposes work."},
		{Field: "orchestrator_prompt", Type: "textarea", Label: "Orchestrator prompt", Rows: 8,
			Help:       "Voice, decomposition style, and synthesis approach. The orchestrator also briefs the worker per step, so spell out how to handle this agent's common failure modes (ambiguous matches, empty results, conflicting sources) — defaults rarely fit.",
			SuggestURL: "../api/agents/suggest"},
		{Field: "plan_guidance", Type: "textarea", Label: "Plan guidance", Rows: 3,
			Help:       "Optional. Appended to the orchestrator prompt — nudges decomposition style.",
			SuggestURL: "../api/agents/suggest"},
		{Type: "header", Label: "Budgets",
			Help: "How much compute the agent may spend per turn."},
		{Field: "max_plan_steps", Type: "number", Label: "Max plan steps", Min: 1, Max: 12,
			Placeholder: "5",
			Help:        "How many steps the orchestrator may commit to per user turn. Leave at 5 for general agents; raise for deep-research agents that need more decomposition; drop to 1-2 for snappy lookup agents.",
			SuggestURL:  "../api/agents/suggest"},
		{Field: "max_worker_rounds", Type: "number", Label: "Max worker rounds per step", Min: 1, Max: 20,
			Placeholder: "5",
			Help:        "How many LLM call + tool-execution cycles the worker may use for a single step. Each round is one model call. Raise when the worker chains many tool calls (research with cross-references); lower for fast single-tool answers.",
			SuggestURL:  "../api/agents/suggest"},
		{Field: "gap_check", Type: "toggle", Label: "Gap detection",
			Help: "Post-plan review pass that fills structural gaps before synthesis. Worth it for research; off for chat."},
		{Type: "header", Label: "Reasoning",
			Help: "Override the LLM's reasoning mode for this agent's turns."},
		{Field: "think", Type: "select", Label: "Think mode",
			Options: []ui.SelectOption{
				{Value: "auto", Label: "Auto — use the route default"},
				{Value: "on", Label: "On — force reasoning for every turn"},
				{Value: "off", Label: "Off — force no reasoning (faster)"},
			},
			Help: "Top-level conversational agents default On (reasoning helps planners / synthesizers). Sub-agent specialists default Off (faster lookups). Pick Auto only when you want the framework route to decide."},
	}
	// Sub-agent create flow (chat-toolbar Create → "sub-agent of X")
	// bakes the parent ID into the form via a hidden field so the POST
	// to /api/agents includes owned_by=<parent_id>. enforceSubAgentPosture
	// then pins the sub-agent posture flags at load. Only relevant on
	// the new-agent path; editing an existing sub-agent already knows
	// its OwnedBy from the loaded record.
	if subAgentParentID != "" {
		fields = append(fields, ui.FormField{
			Field:   "owned_by",
			Type:    "hidden",
			Default: subAgentParentID,
		})
	}

	if !subAgent {
		fields = append(fields,
			ui.FormField{Field: "allow_explorer", Type: "toggle", Label: "Allow explorer mode",
				Help: "Lets the worker lift its round budget mid-turn (up to 50). For agents mapping unfamiliar APIs."},
			ui.FormField{Type: "header", Label: "Memory",
				Help: "What the agent remembers across turns. Knowledge (uploaded files) is always available."},
			ui.FormField{Field: "memory_mode", Type: "select", Label: "Memory mode",
				Options: []ui.SelectOption{
					{Value: "agent", Label: "Agent — generalized lessons only"},
					{Value: "chatbot", Label: "Chatbot — lessons + user personalization"},
				},
				Help: "Shapes what store_fact stores. Agent (default): generalized lessons only — specifics go to memory_save (Inferred). Chatbot: same + user personalization + conversation notes."},
			ui.FormField{Field: "disable_explicit", Type: "toggle", Label: "Disable Explicit Memory",
				Help: "Strips store_fact tools + pre-injected facts block. For impersonal / stateless agents."},
			ui.FormField{Field: "disable_inferred", Type: "toggle", Label: "Disable Reference Memory",
				Help: "Strips memory_save / memory_search / memory_forget from the catalog and excludes derived chunks from recall. For agents that should answer from authoritative sources only. Per-turn Clean toggle = same, scoped to one turn."},
			ui.FormField{Field: "disable_skills", Type: "toggle", Label: "Disable skills",
				Help: "Hides activate_skill from the catalog and drops the \"Available skills\" prompt block — no skills can be invoked regardless of the per-agent allowlist. For KB readers / doc-Q&A / compliance agents that should never load skill addendums."},

			ui.FormField{Type: "header", Label: "Publishing",
				Help: "Who can use this agent and under what restrictions."},
			ui.FormField{Field: "exposed", Type: "toggle", Label: "Publish as public app",
				Help: "Shows on /agents/ for non-admin users. Each end-user gets their own sessions + facts under the agent."},
			ui.FormField{Field: "public_name", Type: "text", Label: "Public app name",
				Placeholder: "(uses the agent name above when blank)",
				Help:        "Optional. Name shown in /agents/ + URL slug. Set when the internal name reads awkwardly as an app title.",
				SuggestURL:  "../api/agents/suggest"},
			ui.FormField{Field: "allow_private_mode", Type: "toggle", Label: "Allow Private mode",
				Help: "Shows a Private toggle on the public chat — drops network tools per turn. Off for Research-style agents that need network."},
			ui.FormField{Field: "force_private", Type: "toggle", Label: "Force Private mode (network locked off)",
				Help: "Permanently drops network + sub-agent dispatch tools. For compliance / confidential / family-facing agents."},
			ui.FormField{Field: "hidden", Type: "toggle", Label: "Hide from agent fleet",
				Help: "Off (default) = globally callable: appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). On = dropped from the fleet block and dispatch refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list. Affects FLEET visibility only — the agent still appears in your own Agency picker and stays reachable at its public URL when Published. Use for personal agents or Builder-authored sub-agents you don't want the fleet routing to."},

			ui.FormField{Type: "header", Label: "Intake & evals",
				Help: "Optional structured input form + saved test cases."},
			ui.FormField{Field: "evals", Type: "textarea", Label: "Eval cases (JSON)", Rows: 6,
				Help: "Optional. Saved test cases for the eval harness. Run via POST /api/agents/<id>/eval to grade the agent against each case. Format: a JSON array of {name, prompt, must_include, must_not_include, judge_prompt, notes}. must_include / must_not_include are case-insensitive substring checks; judge_prompt is an optional LLM-as-judge criterion. Use to lock in expected behavior before editing the orchestrator_prompt so regressions are visible.",
				Placeholder: "[\n  {\"name\": \"asks_clarifying\", \"prompt\": \"I want to compare these products\",\n   \"judge_prompt\": \"the reply asks at least one clarifying question rather than guessing which products\"},\n  {\"name\": \"cites_sources\", \"prompt\": \"What's TS3's default port?\",\n   \"must_include\": [\"10080\"], \"judge_prompt\": \"the reply cites the source URL\"}\n]",
				SuggestURL:  "../api/agents/suggest"},
			ui.FormField{Field: "intake_form", Type: "textarea", Label: "Intake form (JSON)", Rows: 6,
				Help: "Optional. When set, the chat shows this form INSTEAD of the text input on the first turn of every new session. Submitting packs the values into a markdown user message + uploads any file fields as attachments (PDFs/DOCX get text-extracted server-side, images go to vision). Leave blank for a normal chat-first agent. Format: a JSON array of {name, label, type, placeholder, help, required, options}. type: \"text\" (default), \"textarea\", \"select\" (single-choice dropdown), \"checklist\" (multi-pick checkboxes — selected values get comma-joined in the packed markdown), \"number\", \"file\", \"button\" (self-submitting). options: array of strings, used by select / checklist / button.",
				Placeholder: "[\n  {\"name\": \"company\", \"label\": \"Company name\", \"type\": \"text\", \"required\": true},\n  {\"name\": \"audience\", \"label\": \"Target audience\", \"type\": \"textarea\"},\n  {\"name\": \"deadline\", \"label\": \"Deadline\", \"type\": \"select\", \"options\": [\"This week\", \"This month\", \"No rush\"]},\n  {\"name\": \"topics\", \"label\": \"Topics of interest\", \"type\": \"checklist\", \"options\": [\"AI\", \"Healthcare\", \"Finance\", \"Education\"]}\n]",
				SuggestURL:  "../api/agents/suggest"},
		)
	} else {
		// Sub-agent surface: still allow the user to suppress skills.
		// Memory / publishing / intake are pinned off structurally; no
		// reason to display the toggles.
		fields = append(fields,
			ui.FormField{Field: "disable_skills", Type: "toggle", Label: "Disable skills",
				Help: "Suppresses the skills classifier for this sub-agent. Skills can contaminate focused-specialist answers with unrelated corpus chunks; off-by-default for sub-agents is usually right."},
		)
	}

	agentSection := ui.Section{
		Title:    "Agent",
		Subtitle: "Identity, prompts, and behavior. Clone an existing agent from the landing page if you want a quick copy to tweak.",
		Body: ui.FormPanel{
			Source:         source,
			PostURL:        "../api/agents",
			Method:         "POST",
			SubmitLabel:    "Save agent",
			RedirectURL:    redirectURL,
			RedirectTarget: "_self",
			Fields:         fields,
		},
	}
	if subAgent {
		agentSection.Title = "Sub-agent"
		if parentName != "" {
			agentSection.Subtitle = "Owned by parent agent: " + parentName + ". Sub-agents are focused capability components called by their parent via dispatch — public surfaces, intake form, memory, and explorer mode are structurally off and hidden from this editor."
		} else {
			agentSection.Subtitle = "Sub-agent owned by another agent. Public surfaces, intake form, memory, and explorer mode are structurally off and hidden from this editor."
		}
	}
	sections := []ui.Section{agentSection}

	// Sub-agent dispatch allowlist. Only renders for existing agents
	// (need a known ID to wire the picker's record/post URLs). The
	// picker shows every agent the user owns; toggle a row to add /
	// remove it from this agent's allowlist. Empty list = "any non-
	// hidden agent" (default fleet routing); any picks = "ONLY these"
	// (allowlist mode — overrides the default + reaches hidden agents).
	//
	// Hidden for sub-agents: a focused capability called by its parent
	// rarely needs its own fleet-dispatch surface, and the allowlist
	// adds clutter without a real use case. The parent already owns
	// the routing decisions.
	if id != "" && !subAgent {
		sections = append(sections, ui.Section{
			Title:    "Sub-agent dispatch allowlist",
			Subtitle: "Restrict which agents THIS agent can call via agents(action=\"run\"). Empty = any non-hidden agent (default). Any picks = ONLY those (restricted mode; also wires through to Hidden agents you pick).",
			Body: ui.ChipPicker{
				OptionsSource: "../api/agents",
				RecordSource:  source,
				Field:         "allowed_dispatch_targets",
				PostTo:        source,
				Method:        "POST",
				NameField:     "id",
				LabelField:    "name",
				DescField:     "description",
			},
		})
	}

	// Phantom dispatches surface. Phantom-driven runs use per-chat
	// synthetic users so they're invisible from the normal session
	// list (which reads only your DB). This read-only table walks
	// every phantom:* user, finds sessions in this agent's bucket,
	// and lets the operator drill into any of them via an expand
	// row-action that renders the full message history.
	//
	// Top-level agents only. Sub-agent dispatches via
	// agents(action="run") are stateless (no session persisted), so a
	// sub-agent's bucket under any phantom:<chatID> user is always
	// empty. Pre-stateless leftover threads under those buckets are
	// inaccessible here anyway — wiping the parent's phantom sessions
	// is the recovery path. Hiding this section on sub-agents stops
	// the editor from advertising a surface that's structurally empty.
	//
	// Wipe affordances at two granularities: per-row Delete (clears
	// one chat's session with this agent) and a section-level
	// "Wipe all" (nukes every phantom session for this agent across
	// all chats). The latter is the contaminated-context recovery
	// path — phantom chats accumulate dispatch history that the
	// LLM treats as recall, so a wipe is sometimes the only way to
	// stop a misbehavior loop.
	if id != "" && !subAgent {
		sections = append(sections, ui.Section{
			Title:    "Phantom dispatches",
			Subtitle: "Every phantom chat that has dispatched to this agent. Phantom-driven runs scope under per-chat synthetic users (phantom:<chatID>) for isolation; this is the only place to inspect — or wipe — their session history. Per-row Delete clears one chat; \"Wipe all\" below the table nukes every phantom session for this agent.",
			Body: ui.Table{
				Source:    "../api/agents/" + id + "/phantom-sessions",
				RowKey:    "session_id",
				EmptyText: "No phantom dispatches have reached this agent.",
				SortBy:    "last_at",
				SortDesc:  true,
				Columns: []ui.Col{
					{Field: "chat_id", Label: "Chat", Flex: 2},
					{Field: "title", Label: "Title", Flex: 3, Mute: true},
					{Field: "messages", Label: "Msgs"},
					{Field: "last_at", Label: "Last", Format: "reltime", Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.Expand("View", ui.HistoryPanel{
						Source:       "../api/agents/" + id + "/phantom-sessions/{session_id}?chat_id={chat_id}",
						EmptyText:    "(empty session)",
						RoleField:    "role",
						TextField:    "content",
						TimeField:    "created",
						AssistantTag: "assistant",
					}),
					{
						Type:    "button",
						Label:   "Delete",
						PostTo:  "../api/agents/" + id + "/phantom-sessions/{session_id}?chat_id={chat_id}",
						Method:  "DELETE",
						Confirm: "Delete this phantom session? The chat's running history with this agent is dropped; the next dispatch from that chat starts fresh.",
					},
				},
			},
		})
		// Wipe-all control. DisplayPanel sources the agent record
		// (benign fetch for the page; Pairs is empty so no labels
		// render), and the Actions row carries the bulk-wipe button.
		// Lets us reuse the framework's confirm + variant styling
		// without inventing a one-off bare-button component.
		sections = append(sections, ui.Section{
			Title:    "Wipe phantom dispatches",
			Subtitle: "Drop every phantom session for this agent across all chats. Use as the hard reset when the agent's phantom-side history has been contaminated and per-row cleanup isn't enough. iMessage chats remain (phantom-side state is separate); only the orchestrate session bucket for this agent under each phantom:<chatID> user gets cleared.",
			Body: ui.DisplayPanel{
				Source: "../api/agents/" + id,
				Pairs:  []ui.DisplayPair{},
				Actions: []ui.ToolbarAction{
					{
						Label:   "Wipe all phantom sessions",
						Method:  "DELETE",
						URL:     "../api/agents/" + id + "/phantom-sessions",
						Confirm: "Wipe every phantom session for this agent? Every phantom chat's accumulated history with this agent will be deleted. Their next dispatch starts fresh.",
						Variant: "danger",
					},
				},
			},
		})
	}

	// Carry the edited agent's ID back to Agency so the picker
	// reopens on the agent the user was just editing instead of
	// snapping to Chat. Empty id (create form) skips the param.
	backURL := ".."
	if id != "" {
		backURL = "..?agent=" + url.QueryEscape(id)
	}
	page := ui.Page{
		Title:     title,
		ShowTitle: true,
		BackURL:   backURL,
		MaxWidth:  "900px",
		Sections:  sections,
	}
	page.ServeHTTP(w, r)
}
