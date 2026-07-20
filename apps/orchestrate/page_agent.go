package orchestrate

import (
	"fmt"
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
//
// channelAgentPrompt is the persona for the Channel agent preset: a lean,
// on-message conversational base, no Agents-controller framing. Tuned for
// short text replies on a messaging Channel. Product output, so no
// em-dashes (universal style rule).
const channelAgentPrompt = "You are a helpful assistant answering messages on a messaging channel. Keep replies short, clear, and conversational; these are text messages, not essays. Answer the person directly and stay on topic. If you need information, use your tools quietly and reply with the result. Do not narrate your internal steps, your tools, or any other agents. If you genuinely can't help with something, say so briefly and suggest a next step."

// agentTypeTemplates returns the editor's "Agent type" presets (create mode).
// Picking one STAMPS sensible defaults for the character-defining flags —
// Cortex (a standing mind, the "channel" json field) and memory mode — so you
// choose WHAT KIND of agent this is instead of reasoning about each flag. The
// type is a starting point, not a lock: every flag stays editable in Advanced
// afterward. Fleet is deliberately OFF on all of them — granting the fleet
// (delegate / standing agents / monitors) to an agent is an explicit choice you
// add per agent, not a type default (dispatch to other agents is already
// available without it). See docs/channels-and-agents.md.
//
//	Assistant      — standing personal helper: Cortex on, personalized memory.
//	Conversational — a 1:1 chat persona that knows you: no Cortex, personalized.
//	Channel agent  — answers a messaging room/contact: Cortex on (for the
//	                 received→cortex feed), lessons-only memory (a room is many
//	                 people, so single-user personalization doesn't fit).
//	Specialist     — a focused, dispatchable worker: no Cortex, lessons-only.
func agentTypeTemplates() []ui.FormTemplate {
	return []ui.FormTemplate{
		{
			Label:  "Assistant — standing personal helper (continuous mind)",
			Values: map[string]any{"channel": true, "memory_mode": "chatbot", "fleet": false, "recall_hints": true},
		},
		{
			Label:  "Conversational — a 1:1 chat persona that knows you",
			Values: map[string]any{"channel": false, "memory_mode": "chatbot", "fleet": false, "recall_hints": true},
		},
		{
			Label: "Channel agent — answers a messaging room / contact",
			Values: map[string]any{
				"channel":             true, // Cortex on → the received→cortex feed
				"memory_mode":         "agent",
				"fleet":               false,
				"recall_hints":        true,
				"description":         "Conversational agent attached to a messaging channel (iMessage / Telegram / Slack).",
				"orchestrator_prompt": channelAgentPrompt,
			},
		},
		{
			Label:  "Specialist — a focused, dispatchable worker",
			Values: map[string]any{"channel": false, "memory_mode": "agent", "fleet": false, "recall_hints": true},
		},
	}
}

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
	agentLocked := false
	// Dispatch policy to surface first in the editor's select. Ordering the
	// effective mode first means a legacy record (no stored dispatch_mode) seeds
	// that value on save instead of the form's first-option fallback silently
	// converting a legacy allowlist into allow-all. Recomputed from rec below.
	dispatchModeFirst := dispatchAll
	// ForcePrivate agents can't escalate to the remote lead model (gate 2),
	// so the "Use Lead model" toggle is hidden for them.
	leadModelLocked := false
	if id != "" {
		source = "../api/agents/" + id
		title = "Edit agent"
		// Agents live in the user's per-user DB (udb), NOT the global
		// T.DB — reading from the wrong store leaves OwnedBy empty and
		// the editor falls back to the top-level shape (intake form,
		// publishing, etc. still rendering for what's actually a
		// sub-agent).
		if rec, ok := loadAgent(udb, id); ok {
			agentLocked = rec.Locked
			leadModelLocked = rec.ForcePrivate
			dispatchModeFirst = effectiveDispatchMode(rec)
			if rec.OwnedBy != "" {
				subAgent = true
				if parent, pok := loadAgent(udb, rec.OwnedBy); pok {
					parentName = parent.Name
				}
				title = "Edit sub-agent"
			}
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
		{Field: "triggers", Type: "tags", Label: "Dispatch triggers (optional)",
			Help: "Patterns that, when matched in the user's message, nudge the host to dispatch to THIS agent FIRST that turn — a salient per-turn hint, stronger than the description alone for domains the host has priors in (law, medicine, finance). A pattern with * or ? matches attachment filenames; anything else is a case-insensitive substring of the message. Author SPECIFIC patterns the domain's questions actually contain (a criminal-law agent: \"penal code\", \"PC \", \"felony\", \"misdemeanor\", \"charged with\") — loose ones over-fire and get tuned out. Empty = no per-turn nudge (the agent is still in the catalog)."},
		{Type: "header", Label: "Persona",
			Help: "How the agent thinks and decomposes work."},
		{Field: "orchestrator_prompt", Type: "textarea", Label: "Orchestrator prompt", Rows: 8, Expand: true,
			Help:       "Voice, decomposition style, and synthesis approach. The orchestrator also briefs the worker per step, so spell out how to handle this agent's common failure modes (ambiguous matches, empty results, conflicting sources) — defaults rarely fit.",
			SuggestURL: "../api/agents/suggest"},
		{Field: "plan_guidance", Type: "textarea", Label: "Plan guidance", Rows: 3,
			Help:       "Optional. Appended to the orchestrator prompt — nudges decomposition style.",
			SuggestURL: "../api/agents/suggest"},
		{Type: "header", Label: "Budgets", Collapsed: true,
			Help: "How much compute the agent may spend per turn."},
		{Field: "max_plan_steps", Type: "number", Label: "Max plan steps", Min: 1, Max: 12,
			Placeholder: fmt.Sprintf("%d", defaultMaxPlanSteps),
			Help:        fmt.Sprintf("How many steps the orchestrator may commit to per user turn. Leave blank for the default (%d) on general agents; raise for deep-research agents that need more decomposition; drop to 1-2 for snappy lookup agents.", defaultMaxPlanSteps),
			SuggestURL:  "../api/agents/suggest"},
		{Field: "max_worker_rounds", Type: "number", Label: "Max worker rounds per step", Min: 1, Max: 20,
			Placeholder: fmt.Sprintf("%d", defaultMaxWorkerRounds),
			Help:        fmt.Sprintf("How many LLM call + tool-execution cycles the worker may use for a single step. Each round is one model call. Leave blank for the default (%d); raise when the worker chains many tool calls (research with cross-references); lower for fast single-tool answers.", defaultMaxWorkerRounds),
			SuggestURL:  "../api/agents/suggest"},
		{Field: "gap_check", Type: "toggle", Label: "Gap detection",
			Help: "Post-plan review pass that fills structural gaps before synthesis. Worth it for research; off for chat."},
		{Type: "header", Label: "Reasoning", Collapsed: true,
			Help: "Override the LLM's reasoning mode for this agent's turns."},
		{Field: "think", Type: "select", Label: "Think mode",
			Options: []ui.SelectOption{
				{Value: "auto", Label: "Auto — use the route default"},
				{Value: "on", Label: "On — force reasoning for every turn"},
				{Value: "off", Label: "Off — force no reasoning (faster)"},
			},
			Help: "Top-level conversational agents default On (reasoning helps planners / synthesizers). Sub-agent specialists default Off (faster lookups). Pick Auto only when you want the framework route to decide."},
		{Field: "think_budget", Type: "number", Label: "Think budget (tokens)", Min: 0, Max: 32768,
			Placeholder: "0",
			Help:        "Max thinking tokens per LLM call for this agent. 0 = inherit the deployment default (4096). The admin global budget is a hard ceiling — this can only LOWER the budget (snappier turns); a value above the admin ceiling is clamped. Only applies when Think is on."},
	}
	// Per-agent lead-model escalation. Only offered when a distinct lead LLM
	// is actually wired (HasDistinctLead) — otherwise it degrades straight
	// back to the worker, so showing it would be a no-op control. Hidden for
	// ForcePrivate agents: their conversation must never leave for the remote
	// lead model (gate 2), so the option doesn't apply.
	if T.HasDistinctLead() && !leadModelLocked {
		fields = append(fields, ui.FormField{
			Field: "lead_model", Type: "toggle", Label: "Use Lead model for reasoning",
			Help: "Run this agent's orchestrator + synthesis turns on the lead (precision) model instead of the local worker. The lead model is remote and costs more per turn; the worker is local and free. The dispatched per-step worker phases still run on the worker. Off by default. Automatically ignored on a Private turn — the conversation stays local.",
		})
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
				Help: "Lets the worker lift its round budget mid-turn. For agents mapping unfamiliar APIs."},
			ui.FormField{Field: "explorer_hard_cap", Type: "number", Label: "Explorer ceiling",
				Help: "Max rounds once explorer mode is active. Blank/0 = default 50. Only applies when explorer mode is allowed."},
			ui.FormField{Type: "header", Label: "Memory", Collapsed: true,
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
			ui.FormField{Field: "recall_hints", Type: "toggle", Label: "Recall hints",
				Help: "Each turn, surface a short scored list of knowledge you already have that looks relevant to the message — pointers (title + relevance), not the content. The agent pulls one with knowledge_search only if it fits, so it stops missing material it should look up. Best for agents with a real corpus (attached collections / uploaded docs). Thresholds are deployment tunables."},
			// (disable_skills toggle removed — redundant: skills only fire when a
			// skill is ATTACHED (AllowedSkills), so "no skills" = attach none; the
			// per-turn Clean toggle covers ad-hoc suppression. Field kept for the
			// CRUD tools.)

			ui.FormField{Type: "header", Label: "Context", Collapsed: true,
				Help: "How much of a persistent thread (the Cortex home thread, each Channel room) the agent carries into the prompt. Storage always keeps the full thread; these only bound the run-view."},
			ui.FormField{Field: "context_depth", Type: "number", Label: "Context depth (recent messages)", Min: 0, Max: 200,
				Placeholder: "0",
				Help:        "How many recent messages are kept verbatim. 0 = framework default (12). Older messages fold into a rolling summary unless that's disabled below."},
			ui.FormField{Field: "disable_compaction", Type: "toggle", Label: "Disable rolling summary",
				Help: "Off (default) summarizes older messages into a running summary; on drops them to the context-depth tail instead. Both stay bounded — this just chooses summarize-old vs forget-old."},

			ui.FormField{Type: "header", Label: "Cortex & delegation", Collapsed: true,
				Help: "Always-on behaviors. Independent of each other."},
			ui.FormField{Field: "channel", Type: "toggle", Label: "Maintain a Cortex thread",
				Help: "Gives the agent a persistent Cortex thread (its mind — the 🧠 row pinned at the top of the rail, above its ordinary sessions) where event-monitor wakes and standing-agent reports land, kept bounded by a rolling summary. It also surfaces the Permissions queue and the Manage menu in the topbar. Reached only from Agents. When published to the dashboard, granted users don't see the Cortex thread — they get ordinary chat sessions, each seeded read-only from the agent's standing awareness so it shows up already aware (publishing + granting access is the consent to share that). Publishable as long as the delegation & management tools (below) are off."},
			ui.FormField{Field: "fleet", Type: "toggle", Label: "Delegation & management tools",
				Help: "Grants the conductor toolset: delegation to other agents + standing-agent scheduling + event-monitors + run-ledger + history-recall. This is DISTINCT from \"the fleet\" (the collection of all your agents — every agent is in that). It does NOT stop the agent doing work itself; it just adds the tools. An agent carrying these tools is never published publicly, since they reach owner-only management endpoints."},
			ui.FormField{Field: "tag_name", Type: "toggle", Label: "Sign outbound messages with this agent's name",
				Help: "Prefixes every message this agent sends over a messaging channel/bridge with its name — e.g. \"[Assistant] on my way\". Lets the recipient tell the agent's texts apart from your own messages in the same thread. Off by default; turn it on for agents that reply in conversations you also text in."},

			ui.FormField{Type: "header", Label: "Access & visibility", Collapsed: true,
				Help: "Who can use this agent, fleet visibility, and Private-mode policy. (The edit/delete lock is the 🔒 icon at the top-right.)"},
			ui.FormField{Field: "exposed", Type: "toggle", Label: "Publish App to Dashboard",
				Help: "Adds this agent to the dashboard as its own app (its own card + URL). NOT open to everyone — a user only sees and can use it once you grant them access (per-app permissions); admins always have access. Each user gets their own private sessions + data under the agent."},
			ui.FormField{Field: "mcp_exposed", Type: "toggle", Label: "Reachable over MCP",
				Help: "Lets an external MCP client (e.g. Claude Desktop, with a bridge key) dispatch to this agent via the ask_agent tool on gohort's /mcp/ endpoint. Off by default — turn on only the agents you want reachable from outside. Independent of publishing to the dashboard."},
			ui.FormField{Field: "public_name", Type: "text", Label: "Published app name",
				Placeholder: "(uses the agent name above when blank)",
				Help:        "Optional. Name shown on the dashboard card + URL slug. Set when the internal name reads awkwardly as an app title.",
				SuggestURL:  "../api/agents/suggest"},
			ui.FormField{Field: "allow_private_mode", Type: "toggle", Label: "Allow Private mode",
				Help: "Shows a Private toggle on the public chat — drops network tools per turn. Off for Research-style agents that need network."},
			ui.FormField{Field: "force_private", Type: "toggle", Label: "Force Private mode (network locked off)",
				Help: "Permanently drops network + sub-agent dispatch tools. For compliance / confidential / family-facing agents."},
			ui.FormField{Field: "hidden", Type: "toggle", Label: "Hide from agent fleet",
				Help: "Off (default) = globally callable: appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). On = dropped from the fleet block and dispatch refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list. Affects FLEET visibility only — the agent still appears in your own Agents picker and stays reachable at its dashboard URL when Published. Use for personal agents or Builder-authored sub-agents you don't want the fleet routing to."},
			ui.FormField{Field: "dispatch_mode", Type: "select", Label: "Dispatch policy",
				Options: dispatchModeOptions(dispatchModeFirst),
				Help:    "Which OTHER agents this one may call via agents(action=\"run\"). Allow all = any non-hidden agent (default). Only allow / Allow all except use the target list in the \"Dispatch target list\" section below. Allow none blocks all dispatch. Same control as the in-chat Configure → Security & Access modal."},
			// (Lock moved to the 🔒/🔓 icon in the top-right of the editor —
			// toggled live via handleAgentLock, preserved across form saves.)

			ui.FormField{Type: "header", Label: "Intake & evals", Collapsed: true,
				Help: "Optional structured input form + saved test cases."},
			ui.FormField{Field: "evals", Type: "textarea", Label: "Eval cases (JSON)", Rows: 6,
				Help:        "Optional. Saved test cases for the eval harness. Run via POST /api/agents/<id>/eval to grade the agent against each case. Format: a JSON array of {name, prompt, must_include, must_not_include, judge_prompt, notes}. must_include / must_not_include are case-insensitive substring checks; judge_prompt is an optional LLM-as-judge criterion. Use to lock in expected behavior before editing the orchestrator_prompt so regressions are visible.",
				Placeholder: "[\n  {\"name\": \"asks_clarifying\", \"prompt\": \"I want to compare these products\",\n   \"judge_prompt\": \"the reply asks at least one clarifying question rather than guessing which products\"},\n  {\"name\": \"cites_sources\", \"prompt\": \"What's TS3's default port?\",\n   \"must_include\": [\"10080\"], \"judge_prompt\": \"the reply cites the source URL\"}\n]",
				SuggestURL:  "../api/agents/suggest"},
			ui.FormField{Field: "intake_form", Type: "textarea", Label: "Intake form (JSON)", Rows: 6,
				Help:        "Optional. When set, the chat shows this form INSTEAD of the text input on the first turn of every new session. Submitting packs the values into a markdown user message + uploads any file fields as attachments (PDFs/DOCX get text-extracted server-side, images go to vision). Leave blank for a normal chat-first agent. Format: a JSON array of {name, label, type, placeholder, help, required, options}. type: \"text\" (default), \"textarea\", \"select\" (single-choice dropdown), \"checklist\" (multi-pick checkboxes — selected values get comma-joined in the packed markdown), \"number\", \"file\", \"button\" (self-submitting). options: array of strings, used by select / checklist / button.",
				Placeholder: "[\n  {\"name\": \"company\", \"label\": \"Company name\", \"type\": \"text\", \"required\": true},\n  {\"name\": \"audience\", \"label\": \"Target audience\", \"type\": \"textarea\"},\n  {\"name\": \"deadline\", \"label\": \"Deadline\", \"type\": \"select\", \"options\": [\"This week\", \"This month\", \"No rush\"]},\n  {\"name\": \"topics\", \"label\": \"Topics of interest\", \"type\": \"checklist\", \"options\": [\"AI\", \"Healthcare\", \"Finance\", \"Education\"]}\n]",
				SuggestURL:  "../api/agents/suggest"},
		)
	}
	// (Sub-agent surface has no extra toggles — memory / publishing / intake are
	// pinned off structurally, and disable_skills was dropped as redundant.)

	// "Agent type" presets — create mode only (a template stamps fields, which
	// would clobber a real agent's values when editing; flags stay editable in
	// Advanced after). Picking a type sets the character-defining defaults
	// (Cortex + memory mode, Fleet off) so you choose what KIND of agent it is.
	var agentTemplates []ui.FormTemplate
	if id == "" && !subAgent {
		agentTemplates = agentTypeTemplates()
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
			Templates:      agentTemplates,
			TemplatesLabel: "Agent type",
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

	// (Channels section removed from the agent editor — channels are managed in
	// the chat rail's Channels area and in the Bridges app, scoped to the agent
	// you're viewing, so the editor no longer carries a duplicate attach form.)

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
			Title:    "Dispatch target list",
			Subtitle: "The agents referenced by the Dispatch policy above: in \"Only allow\" mode these are the ONLY agents this one may call (also reaching Hidden agents you pick); in \"Allow all except\" mode these are the BLOCKED agents. Ignored when the policy is Allow all or Allow none.",
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

	// Share with users — peer-sharing (namespacing phase 5). Existing, non-seed,
	// top-level agents only: a seed is framework-owned and a sub-agent is a
	// component of its parent, neither is independently shareable. The recipient
	// runs the OWNER's agent, but its credentials + tools resolve in the
	// RECIPIENT's namespace, so no secret travels with the share.
	if id != "" && !subAgent && !isSeedID(id) {
		sections = append(sections, ui.Section{
			Title:    "Share with users",
			Subtitle: "Let specific other users run this agent. They run your agent, but its credentials and tools resolve in THEIR namespace — your secrets never travel with the share. Empty = private to you. An admin can audit or revoke shares.",
			Body: ui.ACLPicker(ui.ACLPickerConfig{
				OptionsSource: "../api/user-candidates",
				RecordSource:  source,
				Field:         "allowed_users",
				PostTo:        source,
				Method:        "POST",
				Noun:          "user",
				Intro:         "Users who may run this agent.",
				EmptyText:     "No other users to share with yet.",
			}),
		})
	}

	// (Phantom dispatch + wipe sections removed — phantom's per-chat dispatch
	// surface is retiring with phantom; channel threads are inspected via the
	// rail + the channel-scoped chat tools now.)

	// Delete — the human's authoritative remove for any existing agent the editor
	// is open on, INCLUDING a sub-agent reached via the picker (which agents
	// can't delete once the cross-agent lock is in place). Non-seed only: seeds
	// revert via their own path, they aren't "deleted". The DELETE handler
	// cascades sub-agents and cleans channels / monitors / standing agents /
	// dispatch-allowlist references.
	if id != "" && !isSeedID(id) {
		sections = append(sections, ui.Section{
			Title:    "Delete agent",
			Subtitle: "Permanently remove this agent — its sessions, memory, knowledge, and any sub-agents it owns. Channels, monitors, and standing agents bound to it are cleaned up too. This can't be undone.",
			Body: ui.DisplayPanel{
				Source: "../api/agents/" + id,
				Pairs:  []ui.DisplayPair{},
				Actions: []ui.ToolbarAction{
					{
						Label:   "Delete this agent",
						Method:  "DELETE",
						URL:     "../api/agents/" + id,
						Confirm: "Delete this agent permanently? Its sessions, memory, knowledge, and any sub-agents it owns are removed, and its channels / monitors / standing agents are cleaned up. This can't be undone.",
						Variant: "danger",
					},
				},
			},
		})
	}

	// Carry the edited agent's ID back to Agents so the picker
	// reopens on the agent the user was just editing instead of
	// snapping to Chat. Empty id (create form) skips the param.
	backURL := ".."
	if id != "" {
		backURL = "..?agent=" + url.QueryEscape(id)
	}
	// Lock icon — a 🔒/🔓 toggle pinned to the top-right of the editor for any
	// existing agent (seeds included — locking protects a seed shadow from being
	// rewritten by another agent too). Toggling it persists immediately via
	// /api/agents/{id}/lock (handleAgentLock); the form save preserves Locked, so
	// the icon is the single control. App-specific behavior, so it rides in via
	// ExtraHeadHTML per the core/ui domain-agnostic rule.
	lockHead := ""
	if id != "" {
		lockHead = agentLockIconHTML(id, agentLocked)
	}
	page := ui.Page{
		Title:         title,
		ShowTitle:     true,
		BackURL:       backURL,
		MaxWidth:      "900px",
		Sections:      sections,
		ExtraHeadHTML: lockHead,
	}
	page.ServeHTTP(w, r)
}

// agentLockIconHTML builds the lock toggle injected via ExtraHeadHTML. It sits
// inline in the page header, right after the title (next to the agent name),
// rather than floating at the viewport edge. 🔒 = locked (other agents can't
// edit/delete it), 🔓 = unlocked. Click POSTs to /api/agents/<id>/lock and
// re-draws. The header is built asynchronously by the runtime, so a short
// requestAnimationFrame poll waits for the title before inserting. No backticks
// (it lives in a Go raw string downstream); JS uses plain quotes + concatenation.
func agentLockIconHTML(id string, locked bool) string {
	return fmt.Sprintf(`<style>
#agent-lock{cursor:pointer;border:none;background:none;font-size:1.35rem;line-height:1;opacity:.85;padding:0 .25rem;align-self:center;margin-left:.55rem}
#agent-lock:hover{opacity:1;transform:scale(1.1)}
#agent-lock[disabled]{opacity:.4;cursor:wait}
</style>
<script>
(function(){
  var locked=%t, id=%q;
  var b=document.createElement('button');
  b.id='agent-lock'; b.type='button';
  function draw(){
    b.textContent=locked?'🔒':'🔓';
    b.title=locked?'Locked — only you can edit or delete (click to unlock)':'Unlocked — click to lock so other agents cannot edit or delete';
  }
  draw();
  b.onclick=function(){
    var next=!locked; b.disabled=true;
    fetch('../api/agents/'+id+'/lock',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({locked:next})})
      .then(function(r){ if(!r.ok) throw new Error('request failed'); locked=next; draw(); })
      .catch(function(e){ alert('Could not change lock: '+(e&&e.message||e)); })
      .then(function(){ b.disabled=false; });
  };
  var tries=0;
  function mount(){
    if(document.getElementById('agent-lock')) return;
    var title=document.querySelector('.ui-page-title');
    if(title){ title.insertAdjacentElement('afterend', b); return; }
    if(tries++ < 120) requestAnimationFrame(mount);
  }
  mount();
})();
</script>`, locked, id)
}

// dispatchModeOptions builds the "Dispatch policy" select options with `first`
// listed first, then the remaining modes in canonical order. Ordering the
// record's effective mode first is deliberate: the form seeds a select with no
// stored value from its FIRST option, so a legacy record (dispatch_mode never
// saved) would otherwise be silently rewritten to allow-all on the next save.
// Putting the effective mode first makes that seed preserve current behavior.
func dispatchModeOptions(first string) []ui.SelectOption {
	all := []ui.SelectOption{
		{Value: dispatchAll, Label: "Allow all — any non-hidden agent (default)"},
		{Value: dispatchOnly, Label: "Only allow selected (target list below)"},
		{Value: dispatchExcept, Label: "Allow all except selected (target list below)"},
		{Value: dispatchNone, Label: "Allow none — no dispatch at all"},
	}
	out := make([]ui.SelectOption, 0, len(all))
	for _, o := range all {
		if o.Value == first {
			out = append(out, o)
		}
	}
	for _, o := range all {
		if o.Value != first {
			out = append(out, o)
		}
	}
	return out
}
