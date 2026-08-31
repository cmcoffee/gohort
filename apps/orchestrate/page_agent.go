package orchestrate

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// orchestratorPromptSections is the outline the prompt editor offers.
//
// Shared with the suggest endpoint, which looks a section's Help up by
// title to tell the model what that section is for. One declaration, so
// the guidance the author reads and the guidance the model gets can't
// drift apart. The outline is a suggestion, not a schema: the prompt is
// still one markdown string, an existing free-form prompt opens intact
// in the Intro block above these slots, empty slots contribute nothing,
// and the author can add sections of their own.
var orchestratorPromptSections = []ui.SectionSpec{
	{Title: "Role & voice", Mode: "prose", Required: true,
		Placeholder: "You are a…",
		Help:        "Who this agent is, what it's accountable for, and how it sounds. Two or three sentences. Tone lives here rather than in its own slot: nobody writes a persona and then a separate paragraph about its tone, and splitting them just leaves one box empty."},
	{Title: "Approach", Mode: "prose",
		Help: "How it works a request: what it does first, how it decomposes, when it asks instead of assuming."},
	{Title: "Rules", Mode: "list",
		Placeholder: "always cite a source URL",
		Help:        "Hard constraints, one per line. Stated as instructions the agent can check itself against."},
	{Title: "Failure modes", Mode: "list",
		Placeholder: "no results found — say so, don't guess",
		Help:        "What commonly goes wrong in this domain and what to do about it. The highest-value section: defaults rarely fit."},
	{Title: "Output format", Mode: "prose",
		Help: "The shape of the reply: length, structure, whether to cite, when to use code blocks or tables. Distinct from voice — this is what the answer looks like, not what the agent sounds like."},
}

// handleAgentPage routes the agent editor.
//
//	GET /agent/new        — create a new blank agent
//	GET /agent/{id}       — edit existing agent
//
// agentTypeTemplates returns the editor's "Agent type" presets (create mode).
// Picking one STAMPS sensible defaults for the character-defining flags —
// Cortex (a standing mind, the "channel" json field) and memory mode — so you
// choose WHAT KIND of agent this is instead of reasoning about each flag. Two
// types only, kept in lockstep with the wizard's wizard_kinds (a test asserts
// it): the identity question is "a companion that knows its people, or a
// focused tool for a job?" — standing mind and memory behavior are dials,
// editable here and surfaced in the wizard's Memory step, not species. The
// type is a starting point, not a lock. Fleet is deliberately OFF on both —
// granting the fleet (delegate / standing agents / monitors) is an explicit
// per-agent choice, not a type default. See docs/channels-and-agents.md.
//
//	Assistant  — conversational agent for people (you, a room, a contact):
//	             Cortex on, personalized memory (attributed by name).
//	Specialist — a focused agent for one job (a research agent with an
//	             intake form, a report generator), used directly or
//	             dispatched to by other agents: no Cortex, lessons-only.
func agentTypeTemplates() []ui.FormTemplate {
	return []ui.FormTemplate{
		{
			Label:  "Assistant — a conversational agent that works with people",
			Values: map[string]any{"channel": true, "memory_mode": "chatbot", "fleet": false, "recall_hints": true},
		},
		{
			Label:  "Specialist — a focused agent for one job, used directly or by dispatch",
			Values: map[string]any{"channel": false, "memory_mode": "agent", "fleet": false, "recall_hints": true},
		},
	}
}

func (T *OrchestrateApp) handleAgentPage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/agent/")
	if rest == "" || strings.Contains(rest, "/") {
		http.NotFound(w, r)
		return
	}
	if rest == "new" {
		T.renderAgentEditor(w, r, user, udb, "")
		return
	}
	// The guided create flow — the "New agent" button's default
	// destination. The full editor stays at agent/new ("Advanced
	// editor" link on the wizard, and the sub-agent create path).
	if rest == "wizard" {
		T.renderAgentWizard(w, r, user)
		return
	}
	T.renderAgentEditor(w, r, user, udb, rest)
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
func (T *OrchestrateApp) renderAgentEditor(w http.ResponseWriter, r *http.Request, user string, udb Database, id string) {
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
	// so the "Use Lead model" toggle is hidden for them — unless the operator
	// has declared every model private, in which case the lead is not remote
	// and there is nothing for gate 2 to protect. Hiding a toggle the runtime
	// would honor is a control that reads as broken.
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
			leadModelLocked = agentForcesPrivate(rec) && !AllLLMsPrivate()
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
		{Field: "orchestrator_prompt", Type: "sections", Label: "Orchestrator prompt", Rows: 12,
			Help:       "Voice, decomposition style, and synthesis approach. The orchestrator also briefs the worker per step, so spell out how to handle this agent's common failure modes (ambiguous matches, empty results, conflicting sources) — defaults rarely fit.",
			SuggestURL: "../api/agents/suggest",
			AssistPrompt: "You are writing the system prompt for an AI agent, which the user will run. " +
				"Write in the second person, addressing the agent directly (\"You are…\", \"When a request is ambiguous, ask…\"). " +
				"Be concrete about behavior rather than aspirational about quality: \"cite the URL you read it from\" beats \"be accurate\". " +
				"Do not list tools, mention plan_set or ask_user, or describe the orchestration machinery — the framework supplies all of that around your text.",
			// The outline is a suggestion, not a schema: the prompt is
			// still one markdown string, an existing free-form prompt
			// opens intact in the Intro block above these slots, and the
			// author can add sections of their own. Empty slots write
			// nothing into the prompt the model sees.
			SectionsAllowFree: true,
			Sections:          orchestratorPromptSections},
		{Field: "plan_guidance", Type: "textarea", Label: "Plan guidance", Rows: 3,
			Help:       "Optional. Appended to the orchestrator prompt — nudges decomposition style.",
			SuggestURL: "../api/agents/suggest"},
		// Sits under Persona because that is what it changes: the persona
		// above stays the agent's identity, and the machine supplies the
		// procedure layered on top of it per phase.
		machineSelectField(udb, user),
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
		{Field: "work_plan", Type: "toggle", Label: "Tracked plan",
			Help: "The agent commits to a visible checklist and works it: each step is started, then closed with findings or marked blocked with a reason, and anything left unfinished is stated in the answer instead of quietly dropped. The checklist survives the turn, so a plan begun in one message is still the plan in the next. Replaces this agent's plan_set (which fans a single turn out to workers and ends the round) — worth it for work with several results that build on each other, overhead for questions one call answers."},
		{Type: "header", Label: "Reasoning", Collapsed: true,
			Help: "Override the LLM's reasoning mode for this agent's turns."},
		{Field: "think", Type: "select", Label: "Think mode",
			Options: []ui.SelectOption{
				{Value: "auto", Label: "Auto — follow the deployment routing (" + currentAutoThinkLabel() + ")"},
				{Value: "on", Label: "On — force reasoning for every turn"},
				{Value: "off", Label: "Off — force no reasoning (faster)"},
			},
			Help: "Top-level conversational agents default On (reasoning helps planners / synthesizers). Sub-agent specialists default Off (faster lookups). Pick Auto only when you want the framework route to decide."},
		{Field: "think_budget", Type: "number", Label: "Think budget (tokens)", Min: 0, Max: 32768,
			Placeholder: "0",
			Help:        "Max thinking tokens per LLM call for this agent. 0 = inherit the deployment default (4096). The admin global budget is a hard ceiling — this can only LOWER the budget (snappier turns); a value above the admin ceiling is clamped. Only applies when Think is on."},
		// Which MODEL does the reasoning — a Reasoning setting, not an
		// Autonomous-runs one. It sat under Autonomous runs purely by
		// position (a header owns the fields until the next header), so it
		// read as an unattended-run option when it governs every turn.
		//
		// Shown only when a distinct lead is actually wired (HasDistinctLead):
		// otherwise it degrades straight back to the worker and the control
		// would be a no-op. Hidden for ForcePrivate agents — their
		// conversation must never leave for the remote lead model (gate 2).
		leadModelField(T.HasDistinctLead() && !leadModelLocked),
		{Type: "header", Label: "Autonomous runs", Collapsed: true,
			Help: "What this agent may do on a scheduled/standing fire, when no one is present to click Approve."},
		// Ticked, not typed. A misspelling here grants nothing and looks
		// exactly like a grant: the tool is refused on the first unattended
		// fire, at whatever hour that run is scheduled for, and the list in
		// front of the person still reads as though they had allowed it.
		//
		// Only the tools that WOULD stop and ask are offered. Read-only ones
		// never prompt, so listing one is a decision with no effect, and a
		// list of those is harder to read than a short one.
		{Field: "auto_approve_tools", Type: "checklist", Label: "Pre-approved tools",
			Options:     approvableToolOptions(user),
			Placeholder: "(nothing here prompts for approval)",
			Help:        "What this agent — and its sub-agents, by inheritance — may call on a SCHEDULED or standing run WITHOUT a per-call approval prompt. Tick the consequential tools you trust it to run unattended (a credential-backed tool, a channel send). Anything unticked is refused on the first unattended fire and queued in the Permissions pane, and approving it there ticks it here. Read-only tools never prompt and are not listed."},
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

			ui.FormField{Type: "header", Label: "Cortex & capability", Collapsed: true,
				Help: "Standing behaviors and capability GRANTS: whether the agent keeps a Cortex thread, plus the two toolsets it may hold — conductor (scheduling, monitors, delegate) and authoring (build agents, tools, apps). These add TOOLS; they do not govern who the agent may call. That is the Delegation section further down, which is open by default because its target list sits directly beneath it." + appGrantHelp(user, id)},
			ui.FormField{Field: "channel", Type: "toggle", Label: "Maintain a Cortex thread",
				Help: "Gives the agent a persistent Cortex thread (its mind — the 🧠 row pinned at the top of the rail, above its ordinary sessions) where event-monitor wakes and standing-agent reports land, kept bounded by a rolling summary. It also surfaces the Permissions queue and the Manage menu in the topbar. Reached only from Agents. When published to the dashboard, granted users don't see the Cortex thread — they get ordinary chat sessions, each seeded read-only from the agent's standing awareness so it shows up already aware (publishing + granting access is the consent to share that). Publishable as long as the delegation & management tools (below) are off."},
			ui.FormField{Field: "fleet", Type: "toggle", Label: "Conductor tools (scheduling, monitors, delegate)",
				Help: "Grants the conductor toolset: the delegate tool + standing-agent scheduling + event-monitors + run-ledger + history-recall. This is DISTINCT from \"the fleet\" (the collection of all your agents — every agent is in that), and it is NOT the master switch for agent-to-agent calls: every non-sub agent can call peers via agents(action=\"run\") regardless, governed by the Dispatch policy below (set it to \"Allow none\" to fully ground this agent). It does NOT stop the agent doing work itself; it just adds the tools. An agent carrying these tools is never published publicly, since they reach owner-only management endpoints."},
			authorCapabilityField(id),
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
			// (Dispatch policy lives in the "Cortex & delegation" section above,
			// next to the conductor-tools toggle — the two delegation controls
			// were split across sections and read as one switch when they are
			// two: conductor toolset vs the agents(run) governor.)
			// (Lock moved to the 🔒/🔓 icon in the top-right of the editor —
			// toggled live via handleAgentLock, preserved across form saves.)

			// Delegation sits LAST and uncollapsed on purpose: the fields
			// render directly above the "Dispatch target list" card, which is
			// the list this policy governs. They used to live inside the
			// collapsed "Cortex & delegation" accordion, so that card pointed
			// at a control the reader could not see.
			//
			// ONE section, both directions. There were two headers here, both
			// called Delegation and adjacent — an uncollapsed one holding the
			// fleet toggle and a collapsed one holding the policy — which is
			// what two separate moves toward the target list leave behind when
			// neither removes the other. The split also put the wrong half
			// away: the target-list card is governed by Dispatch policy, and
			// that was the field inside the accordion, so the card still
			// pointed at a control the reader could not see.
			//
			// Ordered inbound then outbound, and within outbound the governor
			// before its exception: Allow none overrides the Builder grant, so
			// reading the grant first states a permission the next field can
			// take away.
			ui.FormField{Type: "header", Label: "Delegation",
				Help: "Both directions of agent-to-agent calling. Who may call THIS agent (fleet visibility), and who this agent may call (dispatch policy + the target list below, which is only consulted in the two \"selected\" modes)."},
			ui.FormField{Field: "hidden", Type: "toggle", Label: "Hide from agent fleet",
				Help: "Off (default) = globally callable: appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). On = dropped from the fleet block and dispatch refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list. Affects FLEET visibility only — the agent still appears in your own Agents picker and stays reachable at its dashboard URL when Published. Use for personal agents or Builder-authored sub-agents you don't want the fleet routing to."},

			ui.FormField{Field: "dispatch_mode", Type: "select", Label: "Dispatch policy",
				Options: dispatchModeOptions(dispatchModeFirst),
				Help:    "Which OTHER agents this one may call via agents(action=\"run\") — this governs ordinary agent-to-agent calls whether or not the conductor tools above are on. Allow all = any non-hidden agent (default). Only allow / Allow all except draw from the target list directly below. Allow none blocks all dispatch — the actual delegation kill switch. Same control as the in-chat Configure → Security & Access modal."},
			ui.FormField{Field: "allow_builder_dispatch", Type: "toggle", Label: "Can dispatch Builder",
				Help: "Lets this agent hand work to Builder — agents(action=\"run\", agent=\"builder\") — to author an agent, tool, or app on its behalf. Off by default and normally reserved to conductor agents (Chat), because authoring expects a human in the loop: the intake conversation, its clarifying pauses, and your review of the draft. Turning it on trades that for reach; whatever Builder creates on a dispatch still lands held for your approval rather than going live. This is a separate grant from \"Authoring tools\" above — that one has the agent build things ITSELF, this one has it ask Builder to. Builder appears in this agent's Available Agents block only while it's on, and it's overridden by Dispatch policy = Allow none."},
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
	// EDIT mode splits the form's header groups into page-level sections, so
	// they appear in the page's own left rail (SectionNav) instead of eight
	// accordions stacked inside one section. Each section is its own
	// FormPanel over the SAME record, saving with PATCH — which merges one
	// field onto the stored copy. A POST would have sent only that section's
	// fields as the whole record and wiped the rest, which is exactly why
	// this split wasn't possible before the PATCH handler.
	//
	// CREATE mode stays one POST form: there is no record to PATCH yet, and a
	// new agent needs its fields submitted together with the templates picker.
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
	if id != "" {
		if split := splitAgentFormSections(id, source, fields, agentSection.Subtitle); len(split) > 0 {
			sections = split
		}
	}

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
		// The subtitle names WHERE the policy lives and what it is set to
		// right now. It used to say "the Dispatch policy above", which
		// pointed at a select inside the collapsed "Cortex & delegation"
		// accordion — a different widget, folded shut by default — so the
		// card referenced a control the reader could not see.
		// The picker is FOLDED INTO the Delegation section when the form was
		// split, so the policy select and the list it chooses from sit
		// together. Only when there is no Delegation section (create mode,
		// which doesn't split) does it stand alone.
		targetPicker := ui.ChipPicker{
			OptionsSource: "../api/agents?role=dispatch-target&self=" + id,
			RecordSource:  source,
			Field:         "allowed_dispatch_targets",
			PostTo:        source,
			Method:        "POST",
			NameField:     "id",
			LabelField:    "name",
			DescField:     "description",
		}
		if !foldIntoDelegation(sections, targetPicker) {
			sections = append(sections, ui.Section{
				Title:    "Dispatch target list",
				Subtitle: dispatchTargetSubtitle(dispatchModeFirst),
				Body:     targetPicker,
			})
		}
	}

	// Credentials this agent may use — tier-2 per-agent scoping, relocated here
	// from the admin credential page (which now only governs tier-1: which USERS
	// may use a credential). Agent-centric so each user sees only their own fleet.
	// The picker is an allowlist (checked = may use); the endpoint inverts it onto
	// the AgentRecord.DisabledCredentials opt-out. Existing, non-sub agents only.
	if id != "" && !subAgent {
		sections = append(sections, ui.Section{
			Title:    "Credentials this agent may use",
			Subtitle: "The APIs you've been granted. All are available to this agent by default; uncheck any this agent shouldn't reach — that drops the tools that dispatch through them from its kit. Secured credentials aren't listed: their access follows their tool bindings, not per-agent scope.",
			Body: ui.ChipPicker{
				Mode:          "attach",
				OptionsSource: "../api/agent-credentials?id=" + id,
				RecordsField:  "credentials",
				AttachedField: "enabled_credentials",
				PostTo:        "../api/agent-credentials?id=" + id,
				SaveKey:       "enabled_credentials",
				NameField:     "value",
				LabelField:    "label",
				DescField:     "desc",
				Noun:          "credential",
				Intro:         "Checked = this agent may use it.",
				EmptyText:     "No credentials have been granted to you yet.",
			},
		})
	}

	// The picture library — the FIRST surface that shows the owner what is in it
	// rather than describing it in the agent's own words. That gap is not
	// theoretical: asked for three people's reference photos, an agent handed
	// back three of its own renders and called them real, and there was nowhere
	// to look and catch it. The thumbnail is the point; the rest is context for
	// deciding whether to keep the row.
	if id != "" {
		sections = append(sections, ui.Section{
			Title:    "Picture library",
			Subtitle: "Every picture this agent has kept for reuse. Look at them: a name, a caption and an origin can all be confidently wrong together, and only the picture settles it. \"Unrecorded\" origin means nobody captured where it came from — it may be something the agent made, so don't trust it as a likeness until you've looked. Forget what shouldn't be here; label anyone the agent hasn't identified, so a request naming them finds the right face. If two rows show the same person, both are flagged: the agent will pick one and you won't know which, so forget whichever is wrong.",
			Body: ui.Table{
				Source:    "../api/agent-images?id=" + id,
				RowKey:    "name",
				EmptyText: "This agent hasn't kept any pictures yet.",
				Columns: []ui.Col{
					{Field: "thumb", Label: "", Type: "image"},
					{Field: "subject", Label: "Of", Flex: 2},
					{Field: "origin", Label: "Origin", Flex: 2, Mute: true},
					{Field: "ref", Label: "Id", Flex: 2, Mute: true},
					{Field: "shows", Label: "Notes", Flex: 4, Mute: true},
					{Field: "kept", Label: "Kept", Flex: 1, Mute: true},
					// Not muted, unlike its neighbours: this column is empty on
					// nearly every row, and the few times it is not are the
					// only times this page has something urgent to say.
					{Field: "duplicate", Label: "", Flex: 2},
				},
				RowActions: []ui.RowAction{
					{
						Type: "button", Label: "Label", Method: "client",
						PostTo: "agent_image_label", HideIf: "inherited",
					},
					{
						Type: "button", Label: "Forget", Variant: "danger",
						// Inherited entries belong to the parent agent and are
						// refused server-side; hiding the button says so before
						// the click rather than after it.
						HideIf:  "inherited",
						PostTo:  "../api/agent-images/action?id=" + id + "&action=forget&name={name}",
						Confirm: "Forget this picture? The agent will no longer be able to use it, and this can't be undone.",
					},
				},
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
		// The relabel prompt for the picture library. App-specific behavior, so
		// it rides in through a client action rather than into core/ui.
		lockHead += imageLibraryHeadHTML(id)
	}
	page := ui.Page{
		Title:     title,
		ShowTitle: true,
		BackURL:   backURL,
		// Left-rail section nav, one section at a time — same shape as admin and
		// Extensions. The editor has grown several sections (Agent, dispatch list,
		// credentials, sharing, delete); stacking them made the page a long scroll.
		MaxWidth:      "1100px",
		SectionNav:    true,
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
#agent-lock{cursor:pointer;border:none;background:none;font-size:1.2rem;line-height:1;opacity:.85;padding:0 .2rem}
#agent-lock:hover{opacity:1;transform:scale(1.1)}
#agent-lock[disabled]{opacity:.4;cursor:wait}
/* Greyed controls when the record is locked — non-interactive + visibly dimmed,
   but readable. The lock button itself is excluded so it stays clickable. */
.agent-locked-ctl{opacity:.5;pointer-events:none}
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
  // Grey out (or restore) every change control on the page when locked. Covers
  // inputs/selects/textareas/buttons across ALL editor sections (the section
  // nav swaps which one is visible, so hidden sections must be disabled too),
  // skipping the lock button and the section-nav rail so you can still read the
  // record, flip the lock, and move between sections.
  function applyLock(){
    var ctls=document.querySelectorAll('input,select,textarea,button,[contenteditable]');
    for(var i=0;i<ctls.length;i++){
      var c=ctls[i];
      if(c.id==='agent-lock') continue;
      if(c.closest && (c.closest('.ui-section-nav')||c.closest('nav'))) continue;
      if(locked){ c.setAttribute('disabled','disabled'); c.classList.add('agent-locked-ctl'); }
      else { c.removeAttribute('disabled'); c.classList.remove('agent-locked-ctl'); }
    }
  }
  draw();
  b.onclick=function(){
    var next=!locked; b.disabled=true;
    fetch('../api/agents/'+id+'/lock',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({locked:next})})
      .then(function(r){ if(!r.ok) throw new Error('request failed'); locked=next; draw(); applyLock(); })
      .catch(function(e){ alert('Could not change lock: '+(e&&e.message||e)); })
      .then(function(){ b.disabled=false; });
  };
  var tries=0;
  function mount(){
    if(document.getElementById('agent-lock')){ applyLock(); return; }
    // FIRST section card's header-right slot — the lock belongs on the record,
    // not on the page banner. Falls back to the page title only if the section
    // hasn't rendered (it always does, but keep the loop honest).
    var slot=document.querySelector('.ui-section .ui-section-h-r');
    if(slot){ slot.appendChild(b); applyLock(); return; }
    if(tries++ < 180) requestAnimationFrame(mount);
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
// dispatchTargetSubtitle explains the target-list picker in terms of the
// policy CURRENTLY set, and says where that policy lives. Without the
// current value the reader can't tell whether the list they're editing
// does anything at all — the two most common modes ignore it entirely.
func dispatchTargetSubtitle(mode string) string {
	const where = " The policy itself is the **Dispatch policy** select under **Cortex & delegation** above (collapsed by default)."
	// Pipelines are listed here beside agents because they are dispatch targets
	// too: an agent restricted to a few targets used to reach every pipeline
	// its owner had, which made "only these" mean something other than what it
	// says. A pipeline you don't tick in Only mode is one this agent can't run.
	switch mode {
	case dispatchOnly:
		return "Currently **Only allow selected** — this agent may call ONLY the agents and pipelines ticked here, including any Hidden agents you pick." + where
	case dispatchExcept:
		return "Currently **Allow all except selected** — this agent may call any non-hidden agent, and any pipeline, EXCEPT the ones ticked here." + where
	case dispatchNone:
		return "Currently **Allow none** — this agent dispatches to nobody, agents and pipelines alike, so this list has no effect until you change the policy." + where
	default:
		return "Currently **Allow all** — this agent may call any non-hidden agent and any of your pipelines, so this list has no effect. It applies only in \"Only allow selected\" or \"Allow all except selected\" mode." + where
	}
}

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

// authorCapabilityField renders the "Authoring tools" control.
//
// For an ordinary agent it is a real toggle: authoring is a capability you
// grant. For the BUILDER SEED it is not, and must not pretend to be —
// agentCanAuthor() returns true for seed-builder by IDENTITY, OR'd ahead of
// the flag, so the stored value is never consulted for it. Rendering a live
// toggle there showed "off" on an agent that had the full authoring catalog,
// which is how one debugging session concluded authoring was disabled when
// it was not. A control that cannot affect anything is worse than no control.
// appGrantFields renders one read-only row per app that can grant this agent
// access to something it owns.
//
// Here because this is where someone asks what an agent may do — the editor
// already lists cortex, conductor and authoring as capability grants, and
// "which of my machines can it reach" is the same question. It was previously
// answerable only from the granting app's own page, which meant knowing to look
// there first.
//
// EVERY app appears, including those granting nothing. A row reading "none" is
// what tells an owner the capability exists and this agent does not hold it;
// hiding empties would make an app invisible until it mattered.
//
// Read-only, and never rendered for an unsaved agent: a grant is keyed by agent
// id, and an agent with no id yet cannot hold one.
func appGrantHelp(user, agentID string) string {
	summaries := AgentGrantSummaries(user, agentID)
	if strings.TrimSpace(agentID) == "" || len(summaries) == 0 {
		// No apps grant anything here, or the agent has no id yet — a grant is
		// keyed by agent id, so an unsaved one cannot hold any.
		return ""
	}
	// Appended to this section's HELP rather than added as its own field: a
	// "header" field starts a new page section (splitAgentFormSections), so a
	// row here would have cut the capability section in two and stranded the
	// toggles below it. The text belongs beside the other capability grants,
	// not in a section of its own.
	var b strings.Builder
	b.WriteString("\n\nGRANTED BY OTHER APPS — read-only here; the framework knows WHICH app granted what, and only the app knows what its permission means, so the detail stays where it can be edited honestly.\n")
	for _, s := range summaries {
		fmt.Fprintf(&b, "%s: %s", s.Label, s.Text)
		var detail []string
		for _, g := range s.Grants {
			if g.Detail != "" {
				detail = append(detail, g.Label+" — "+g.Detail)
			}
		}
		if len(detail) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(detail, "; "))
		}
		if s.ManageURL != "" {
			fmt.Fprintf(&b, " · manage at %s", s.ManageURL)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func authorCapabilityField(agentID string) ui.FormField {
	if isBuilderAgent(agentID) {
		return ui.FormField{
			Type:  "header",
			Label: "Authoring tools — always on for Builder",
			Help: "Builder holds the authoring catalog (survey, create/update/clone agents, tool_def, app_def, skill_def, credential drafting, bridge/connector) as its IDENTITY, not as a grant, so there is nothing to switch here. To have an agent that builds without being Builder, turn this capability on for that agent instead. " +
				"Note: authoring is owner-only at runtime — if a turn runs as someone other than this agent's owner, the catalog is withheld and the reason is recorded in the session diagnostics.",
		}
	}
	return ui.FormField{Field: "author", Type: "toggle", Label: "Authoring tools (build agents, tools, apps)",
		Help: "Grants the full authoring toolset — the SAME catalog the Builder agent holds: survey (map what already exists), create/update/clone agents, tool_def, app_def, skill_def, the credential draft + probe tools, bridge/connector, and (when you own the agent, plus the conductor tools) scheduling/monitors to wire a built tool live. This is the de-silo of Builder: authoring is a capability any capable agent can hold, so it can BUILD new agents/tools/apps on the gohort framework the way Builder does — not just run pre-built ones. Independent of the conductor tools above. Like them, an authoring agent reaches owner-only endpoints, so it is never published publicly."}
}

// splitAgentFormSections turns one long form into page-level sections, split
// at its "header" fields, so the page's own left rail navigates them.
//
// Each section is a separate FormPanel over the SAME agent record, saving with
// Method "PATCH" — one changed field merged onto the stored copy. With POST
// each panel would send only ITS fields as the whole record and blank
// everything else, which is what made this split unsafe until the PATCH
// handler existed.
//
// Fields BEFORE the first header lead the first section rather than becoming a
// group of their own: they are the agent's identity (name, description,
// triggers), and burying them behind a rail entry would hide the one thing you
// always want to see. Returns nil when the form has no headers to split on.
func splitAgentFormSections(id, source string, fields []ui.FormField, identitySubtitle string) []ui.Section {
	type group struct {
		title, help string
		fields      []ui.FormField
	}
	var lead []ui.FormField
	var groups []group
	for _, f := range fields {
		if f.Type == "header" {
			groups = append(groups, group{title: f.Label, help: f.Help})
			continue
		}
		if len(groups) == 0 {
			lead = append(lead, f)
			continue
		}
		groups[len(groups)-1].fields = append(groups[len(groups)-1].fields, f)
	}
	if len(groups) == 0 {
		return nil
	}
	panel := func(ff []ui.FormField) ui.FormPanel {
		return ui.FormPanel{
			Source: source,
			// The id rides in the QUERY, not the body: a FormPanel's PATCH body
			// is exactly {changed_field: value} and carries no record id, so
			// the target has to be named in the URL.
			PostURL: "../api/agents?id=" + url.QueryEscape(id),
			Method:  "PATCH",
			Fields:  ff,
		}
	}
	// The identity fields lead the first group so the rail's first entry holds
	// both, rather than spending an entry on two inputs.
	first := append(append([]ui.FormField{}, lead...), groups[0].fields...)
	out := []ui.Section{{
		Title:    "Agent",
		Subtitle: identitySubtitle,
		Body:     panel(first),
	}}
	for _, g := range groups[1:] {
		out = append(out, ui.Section{
			Title:    g.title,
			Subtitle: g.help,
			Body:     panel(g.fields),
		})
	}
	return out
}

// leadModelField is the "Use Lead model" toggle, or a hidden no-op field when
// the deployment has no distinct lead wired (or the agent is ForcePrivate).
//
// Returns a field either way so it can sit INLINE in the Reasoning group where
// it belongs, rather than being appended to the end of the form — which is how
// it ended up filed under "Autonomous runs".
func leadModelField(show bool) ui.FormField {
	if !show {
		// Type "hidden" with no Default renders nothing and contributes
		// nothing to the save payload.
		return ui.FormField{Field: "lead_model", Type: "hidden"}
	}
	return ui.FormField{
		Field: "lead_model", Type: "toggle", Label: "Use Lead model for reasoning",
		Help: "Run this agent's orchestrator + synthesis turns on the lead (precision) model instead of the local worker. The lead model is remote and costs more per turn; the worker is local and free. The dispatched per-step worker phases still run on the worker. Off by default. Automatically ignored on a Private turn — the conversation stays local, unless Admin \u2192 LLMs \u2192 Model Privacy says every model is private, in which case escalating keeps it local too.\n\nUsually you do not need this: an agent holding the `consult` tool already asks the lead ONE self-contained question when it hits a wall, at a fraction of the cost of escalating every round. Reach for this toggle when the agent's own reasoning — not one hard question — is what needs the stronger model.",
	}
}

// machineSelectField renders the phase-machine picker for the agent
// editor (docs/agent-machines.md).
//
// Options are baked at render time from the user's saved machines rather
// than fetched, because FormField has no options URL and this is a
// short, rarely-changing list — the same choice every other select on
// this page makes.
//
// A user with no machines gets a HIDDEN field rather than an empty
// select. A control whose only option is "None" teaches nothing and
// invites a support question; the tool that authors machines is where
// someone learns they exist. Hidden with no Default contributes nothing
// to the save payload, so an agent that already has a machine attached
// (by tool or API) keeps it.
func machineSelectField(udb Database, user string) ui.FormField {
	defs := ListMachineDefs(udb, user)
	if len(defs) == 0 {
		return ui.FormField{Field: "machine", Type: "hidden"}
	}
	opts := []ui.SelectOption{{Value: "", Label: "None — the persona above governs every turn"}}
	for _, d := range defs {
		label := d.Name + " (" + strconv.Itoa(len(d.Phases)) + " phases)"
		if desc := strings.TrimSpace(d.Description); desc != "" {
			label += " — " + desc
		}
		opts = append(opts, ui.SelectOption{Value: d.ID, Label: label})
	}
	return ui.FormField{
		Field: "machine", Type: "select", Label: "Phase machine", Options: opts,
		Help: "Optional. A machine gives this agent PHASES it moves through and stays in: it works out what is being asked once, picks an approach once, then answers in that frame for the rest of the thread instead of re-deciding every turn. The persona above still supplies identity and voice — the machine supplies procedure. Sessions already open keep the machine they started with; this applies to new ones. Author machines from chat with the `machine` tool.",
	}
}

// foldIntoDelegation appends the dispatch-target picker into the section that
// holds the dispatch policy, so the select and the list it draws from live in
// one place.
//
// Split apart they were one rail entry away from each other: you would set
// "Only allow" and then have to find a different section to say WHICH agents.
// Returns false when no section holds the policy (create mode, which does not
// split), leaving the caller to add a standalone one.
//
// Found by the FIELD it serves, not by the section's title. It matched the
// title "Delegation" exactly, and the moment those headers were merged under a
// fuller name the match stopped hitting — silently, because the caller's
// fallback is a standalone card, which is precisely the split this closes. A
// renamed heading is a normal thing to do to a form; quietly undoing a layout
// decision is not what it should cost.
func foldIntoDelegation(sections []ui.Section, picker ui.ChipPicker) bool {
	for i := range sections {
		panel, ok := sections[i].Body.(ui.FormPanel)
		if !ok {
			continue
		}
		if !panelHasField(panel, "dispatch_mode") {
			continue
		}
		sections[i].Body = ui.Stack{Children: []ui.Component{panel, picker}}
		return true
	}
	return false
}

func panelHasField(panel ui.FormPanel, field string) bool {
	for _, f := range panel.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

// "Auto" told the reader nothing. It means "this agent declines to override, so
// the deployment's routing decides" — and the deployment's routing lives in a
// different app, under a key most people editing an agent have never seen. A
// setting whose effect you cannot discover from where you set it is not a choice,
// it is a shrug.
//
// So the label resolves it and says what Auto does RIGHT NOW. The alternatives
// were worse: hiding the value in a record the Builder writes trades an opaque
// label for an invisible one, and picks a model's guess over an owner's decision.
//
// Both helpers resolve through the SAME functions the call sites use, with a
// blank record standing in for "no agent-level override", so the label cannot
// drift from the behaviour it describes.

func currentAutoThinkLabel() string {
	if resolveDispatchThink(AgentRecord{}) {
		return "currently reasoning ON"
	}
	return "currently reasoning OFF"
}
