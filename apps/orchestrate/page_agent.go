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
		Help:        "Who this agent is, what it's accountable for, and how it sounds. Two or three sentences.",
		Detail:      "Tone lives here rather than in its own slot: nobody writes a persona and then a separate paragraph about its tone, and splitting them just leaves one box empty."},
	{Title: "Approach", Mode: "prose",
		Help: "How it works a request: what it does first, how it decomposes, when it asks instead of assuming."},
	{Title: "Rules", Mode: "list",
		Placeholder: "always cite a source URL",
		Help:        "Hard constraints, one per line. Stated as instructions the agent can check itself against."},
	{Title: "Failure modes", Mode: "list",
		Placeholder: "no results found: say so, don't guess",
		Help:        "What commonly goes wrong in this domain, and what to do about it.",
		Detail:      "The highest-value section. Defaults rarely fit."},
	{Title: "Output format", Mode: "prose",
		Help:   "The shape of the reply: length, structure, citations, code blocks or tables.",
		Detail: "Distinct from voice: this is what the answer looks like, not what the agent sounds like."},
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
			Label:  "Assistant: a conversational agent that works with people",
			Values: map[string]any{"channel": true, "memory_mode": "chatbot", "fleet": false, "recall_hints": true},
		},
		{
			Label:  "Specialist: a focused agent for one job, used directly or by dispatch",
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
	// The access picture, composed while the record is in hand (agent_access.go).
	// Rendered as a section below rather than as another field: the owner grants
	// these things one editor control at a time and has never been shown what
	// they add up to.
	accessSummary, accessEmpty := "", ""
	// Dispatch policy to surface first in the editor's select. Ordering the
	// effective mode first means a legacy record (no stored dispatch_mode) seeds
	// that value on save instead of the form's first-option fallback silently
	// converting a legacy allowlist into allow-all. Recomputed from rec below.
	// shareRec carries the loaded record out to the sharing section, which has
	// to say something different once the agent is published.
	var shareRec AgentRecord
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
			shareRec = rec
			agentLocked = rec.Locked
			leadModelLocked = agentForcesPrivate(rec) && !AllLLMsPrivate()
			dispatchModeFirst = effectiveDispatchMode(rec)
			accessSummary = agentAccessSummary(rec, agentReach(udb, user, rec))
			accessEmpty = agentToolsEmptyText(rec)
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
			Help:   "Words in a message that push this agent to the front of the queue that turn.",
			Detail: "A pattern with * or ? matches attachment filenames; anything else is a case-insensitive substring of the message. It is a per-turn hint, stronger than the description alone for domains the host already has priors in (law, medicine, finance).\n\nAuthor SPECIFIC patterns the domain's questions actually contain. A criminal-law agent wants \"penal code\", \"PC \", \"felony\", \"misdemeanor\", \"charged with\". Loose ones over-fire and get tuned out. Empty means no per-turn nudge; the agent is still in the catalog."},
		{Type: "header", Label: "Persona",
			Help: "How the agent thinks and decomposes work."},
		{Field: "orchestrator_prompt", Type: "sections", Label: "Orchestrator prompt", Rows: 12,
			Help:       "Voice, decomposition style, and synthesis approach.",
			Detail:     "The orchestrator also briefs the worker per step, so spell out how to handle this agent's common failure modes: ambiguous matches, empty results, conflicting sources. Defaults rarely fit.",
			SuggestURL: "../api/agents/suggest",
			AssistPrompt: "You are writing the system prompt for an AI agent, which the user will run. " +
				"Write in the second person, addressing the agent directly (\"You are…\", \"When a request is ambiguous, ask…\"). " +
				"Be concrete about behavior rather than aspirational about quality: \"cite the URL you read it from\" beats \"be accurate\". " +
				"Do not list tools, mention plan_set or ask_user, or describe the orchestration machinery: the framework supplies all of that around your text.",
			// The outline is a suggestion, not a schema: the prompt is
			// still one markdown string, an existing free-form prompt
			// opens intact in the Intro block above these slots, and the
			// author can add sections of their own. Empty slots write
			// nothing into the prompt the model sees.
			SectionsAllowFree: true,
			Sections:          orchestratorPromptSections},
		{Field: "plan_guidance", Type: "textarea", Label: "Plan guidance", Rows: 3,
			Help:       "Optional. Appended to the orchestrator prompt: nudges decomposition style.",
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
		{Field: "max_worker_rounds", Type: "number", Label: "Max worker rounds per step", Min: 1, Max: maxWorkerRoundsCeiling,
			Placeholder: fmt.Sprintf("%d", defaultMaxWorkerRounds),
			Help:        fmt.Sprintf("How many LLM call + tool-execution cycles the worker may use for a single step. Each round is one model call. Leave blank for the default (%d); raise when the worker chains many tool calls (research with cross-references, or surveying a command before writing it down); lower for fast single-tool answers. Anything under %d is raised to %d: a cap too low to finish an action is worse than no cap.", defaultMaxWorkerRounds, minWorkerRounds, minWorkerRounds),
			SuggestURL:  "../api/agents/suggest"},
		{Field: "gap_check", Type: "toggle", Label: "Gap detection",
			Help: "Post-plan review pass that fills structural gaps before synthesis. Worth it for research; off for chat."},
		{Field: "work_plan", Type: "toggle", Label: "Tracked plan",
			Help:   "The agent commits to a visible checklist and works it.",
			Detail: "Each step is started, then closed with findings or marked blocked with a reason, and anything left unfinished is stated in the answer instead of quietly dropped. The checklist survives the turn, so a plan begun in one message is still the plan in the next.\n\nReplaces this agent's plan_set, which fans a single turn out to workers and ends the round. Worth it for work with several results that build on each other, overhead for questions one call answers."},
		{Type: "header", Label: "Reasoning", Collapsed: true,
			Help: "Override the LLM's reasoning mode for this agent's turns."},
		{Field: "think", Type: "select", Label: "Think mode",
			Options: []ui.SelectOption{
				{Value: "auto", Label: "Auto: follow the deployment routing (" + currentAutoThinkLabel() + ")"},
				{Value: "on", Label: "On: force reasoning for every turn"},
				{Value: "off", Label: "Off: force no reasoning (faster)"},
			},
			Help:   "Whether this agent reasons before it answers.",
			Detail: "Top-level conversational agents default On, because reasoning helps planners and synthesizers. Sub-agent specialists default Off, for faster lookups. Pick Auto only when you want the framework route to decide."},
		{Field: "think_budget", Type: "number", Label: "Think budget (tokens)", Min: 0, Max: 32768,
			Placeholder: "0",
			Help:        "Max thinking tokens per LLM call. 0 inherits the deployment default (4096).",
			Detail:      "The admin global budget is a hard ceiling, so this can only LOWER the budget, for snappier turns. A value above the ceiling is clamped. Only applies when Think is on."},
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
		// Both of these are limits the FRAMEWORK keeps. Written into the
		// prompt instead — "post at most six times a day" — they are rules
		// the model has to count for itself, and one did: it counted its own
		// posts out of a listing, read UTC timestamps as local, and posted
		// nine before reporting the cap as reached.
		{Field: "action_quotas", Type: "tags", Label: "Action limits (per 24 hours)",
			Placeholder: "moltbook/create_post = 6",
			Help:        "How often one action may run in a rolling 24 hours, one per line as `action = number`.",
			Detail: "Name a grouped tool's action (moltbook/create_post) or a whole tool (send_email). The action wins where both are set." +
				"\n\nCounted here, not by the agent: it is refused when the allowance is spent, and told when it frees up. Only SUCCESSFUL calls count, so an outage never spends the day. Empty = no limit."},
		{Field: "daily_spend_usd", Type: "number", Label: "Spend limit (US$ per 24 hours)", Min: 0, Max: 1000,
			Placeholder: "0",
			Help:        "What this agent may cost in a rolling 24 hours. 0 = no limit.",
			Detail: "A turn already running is never cut off. Crossing the line drops the rest of it to the local worker model, and the NEXT turn is declined until the window frees up." +
				"\n\nPriced from what the provider reports, so it does nothing on a deployment with no cost rates configured. Worth setting on anything scheduled against a paid model: one unattended turn can cost more than a day of chat."},
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
			Help:        "What this agent may call on a scheduled or standing run without asking first.",
			Detail:      "Sub-agents inherit it. Tick the consequential tools you trust it to run unattended: a credential-backed tool, a channel send. Anything unticked is refused on the first unattended fire and queued in the Permissions pane, and approving it there ticks it here. Read-only tools never prompt and are not listed."},
		// The other direction, and the one the default makes necessary. A tool
		// attached to an agent is a tool it may use, on a timer as in chat, so
		// what is worth writing down is the exception: the thing you want done
		// only while somebody is there to stop it.
		//
		// Same option set as above deliberately: both questions are about tools
		// that DO something, and a list offering to restrict web_search is a
		// longer list answering nothing.
		{Field: "no_unattended_tools", Type: "checklist", Label: "Never unattended",
			Options:     approvableToolOptions(user),
			Placeholder: "(nothing here has effects worth withholding)",
			Help:        "What this agent may use in chat but never on a run with nobody watching.",
			Detail:      "Refused outright on a scheduled or standing fire, and NOT queued: this is your decision, not a request for one, so nothing lands in the Permissions pane waiting on you. The run says it stopped and why. Unlike pre-approval, this inherits DOWNWARD: a sub-agent cannot do what the agent that built it was told not to."},
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
			ui.FormField{Field: "capture_prompt", Type: "toggle", Label: "Capture prompt text",
				Help:   "Keeps each run's round-1 prompt as text, readable with inspect_run.",
				Detail: "For answering \"what was actually in the prompt\". Switch it off again afterwards: it stores the whole conversation, once per turn."},
			ui.FormField{Field: "allow_explorer", Type: "toggle", Label: "Allow explorer mode",
				Help: "Lets the worker lift its round budget mid-turn. For agents mapping unfamiliar APIs."},
			ui.FormField{Field: "explorer_hard_cap", Type: "number", Label: "Explorer ceiling",
				Help: "Max rounds once explorer mode is active. Blank/0 = default 50. Only applies when explorer mode is allowed."},
			ui.FormField{Type: "header", Label: "Memory", Collapsed: true,
				Help: "What the agent remembers across turns. Knowledge (uploaded files) is always available."},
			ui.FormField{Field: "memory_mode", Type: "select", Label: "Memory mode",
				Options: []ui.SelectOption{
					{Value: "agent", Label: "Agent: generalized lessons only"},
					{Value: "chatbot", Label: "Chatbot: lessons + user personalization"},
				},
				Help:   "Shapes what the agent pins with remember (pin=true).",
				Detail: "Agent, the default, pins generalized lessons only; specifics go to remember (pin=false), which is searchable rather than always in prompt. Chatbot pins the same, plus user personalization and conversation notes."},
			ui.FormField{Field: "disable_explicit", Type: "toggle", Label: "Disable Explicit Memory",
				Help:   "Strips the pinned-notes half of remember and forget, and the pre-injected facts block.",
				Detail: "For impersonal or stateless agents."},
			ui.FormField{Field: "disable_inferred", Type: "toggle", Label: "Disable Reference Memory",
				Help:   "Strips the searchable half of remember, recall and forget, and drops derived chunks from recall.",
				Detail: "For agents that should answer from authoritative sources only. The per-turn Clean toggle does the same thing, scoped to one turn."},
			// The Memory PANE has always had a Working-notes editor and has always
			// told a disabled one to "enable them in the agent editor" — which
			// had no such control, so the instruction pointed at a door that did
			// not exist. The only writers were Builder's authoring tools.
			//
			// Reads as an ENABLE among two DISABLEs, which is worth a wince and
			// not worth flipping a stored field's polarity over: the label says
			// what the switch is, and the help says what ON does.
			ui.FormField{Field: "enable_notes", Type: "toggle", Label: "Working notes (running-state scratchpad)",
				Help:   "Gives the agent one rewritable block of current state, always in prompt.",
				Detail: "It also gets the update_notes tool to rewrite that block. Different from the memory above: facts accumulate, notes get replaced wholesale as the work moves.\n\nFor long-running project or conversational agents. Most task agents have no running state worth carrying. Edit the text itself under Configure, then Memory."},
			ui.FormField{Field: "recall_hints", Type: "toggle", Label: "Recall hints",
				Help:   "Each turn, surface a scored list of knowledge that looks relevant to the message.",
				Detail: "Pointers only, a title plus a relevance, not the content. The agent pulls one with recall if it fits, so it stops missing material it should look up. Best for agents with a real corpus: attached collections, uploaded docs. The thresholds are deployment tunables."},
			// (disable_skills toggle removed — redundant: skills only fire when a
			// skill is ATTACHED (AllowedSkills), so "no skills" = attach none; the
			// per-turn Clean toggle covers ad-hoc suppression. Field kept for the
			// CRUD tools.)

			ui.FormField{Type: "header", Label: "Context", Collapsed: true,
				Help:   "How much of a persistent thread the agent carries into the prompt.",
				Detail: "A persistent thread is the Cortex home thread, or each Channel room. Storage always keeps the full thread; these only bound the run-view."},
			ui.FormField{Field: "context_depth", Type: "number", Label: "Context depth (recent messages)", Min: 0, Max: 200,
				Placeholder: "0",
				Help:        "How many recent messages are kept verbatim. 0 = framework default (12).",
				Detail:      "Older messages fold into a rolling summary unless that is disabled below."},
			ui.FormField{Field: "disable_compaction", Type: "toggle", Label: "Disable rolling summary",
				Help:   "Whether older messages are summarized or simply dropped.",
				Detail: "Off, the default, summarizes older messages into a running summary. On drops them to the context-depth tail instead. Both stay bounded; this only chooses summarize-old over forget-old."},

			ui.FormField{Type: "header", Label: "Cortex & capability", Collapsed: true,
				Help:   "Standing behaviors and capability grants.",
				Detail: "Whether the agent keeps a Cortex thread, plus the two toolsets it may hold: conductor (scheduling, monitors, delegate) and authoring (build agents, tools, apps).\n\nThese add TOOLS; they do not govern who the agent may call. That is the Delegation section further down, which is open by default because its target list sits directly beneath it." + appGrantHelp(user, id)},
			ui.FormField{Field: "channel", Type: "toggle", Label: "Maintain a Cortex thread",
				Help:   "Gives the agent a persistent Cortex thread: its mind, pinned above its ordinary sessions.",
				Detail: "The 🧠 row at the top of the rail is where event-monitor wakes and standing-agent reports land, kept bounded by a rolling summary. It also surfaces the Permissions queue and the Manage menu in the topbar, and is reached only from Agents.\n\nWhen the agent is published to the dashboard, granted users do not see the Cortex thread. They get ordinary chat sessions, each seeded read-only from the agent's standing awareness, so it shows up already aware; publishing and granting access is the consent to share that. Publishable as long as the delegation and management tools below are off."},
			ui.FormField{Field: "fleet", Type: "toggle", Label: "Conductor tools (scheduling, monitors, delegate)",
				Help:   "Grants the conductor toolset: delegate, scheduling, monitors, run-ledger, history-recall.",
				Detail: "This is DISTINCT from \"the fleet\", the collection of all your agents, which every agent is in. It is also NOT the master switch for agent-to-agent calls: every non-sub agent can call peers via agents(action=\"run\") regardless, governed by the Dispatch policy below. Set that to \"Allow none\" to fully ground this agent.\n\nIt does not stop the agent doing work itself, it just adds the tools. An agent carrying these tools is never published publicly, since they reach owner-only management endpoints."},
			authorCapabilityField(id),
			ui.FormField{Field: "tag_name", Type: "toggle", Label: "Sign outbound messages with this agent's name",
				Help:   "Prefixes every message this agent sends over a channel with its name.",
				Detail: "For example, \"[Assistant] on my way\". Lets the recipient tell the agent's texts apart from your own messages in the same thread. Off by default; turn it on for agents that reply in conversations you also text in."},

			ui.FormField{Type: "header", Label: "Access & visibility", Collapsed: true,
				Help:   "Who can use this agent, fleet visibility, and Private-mode policy.",
				Detail: "The edit and delete lock is the 🔒 icon at the top-right."},
			ui.FormField{Field: "exposed", Type: "toggle", Label: "Everyone in the deployment can use it",
				Help:   "The widest rung. An administrator approves it; naming specific people below is yours alone.",
				Detail: "This reaches every signed-in user, so it is requested rather than applied: your request appears on the administrator's Pending promotions queue and takes effect once approved. Turning it off is yours and takes effect at once.\n\nWhat the agent uses goes with it either way — your tools, documents and skills, readable through this agent and nowhere else. Each person gets their own sessions and memory under it.\n\nThis used to be the same switch as the dashboard shortcut below, which meant you could not give somebody a shortcut without widening who could use the agent."},
			ui.FormField{Field: "show_on_dashboard", Type: "toggle", Label: "Shortcut on the dashboard",
				Help:   "A card on the dashboard and a page of its own, for the people who can already use it.",
				Detail: "Presentation, not access: it grants nothing, so nobody has to approve it. Somebody who cannot use the agent does not see the card.\n\nUseful for an agent you reach often, and for one you have hidden from the fleet list, which otherwise leaves you no way to open it."},
			ui.FormField{Field: "mcp_exposed", Type: "toggle", Label: "Reachable over MCP",
				Help:   "Lets an external MCP client dispatch to this agent over gohort's /mcp/ endpoint. An administrator approves it.",
				Detail: "For example Claude Desktop, with a bridge key, calling the ask_agent tool. Off by default, and requested rather than applied for the same reason the rung above is: it takes this agent, with your tools and your documents, outside the deployment. Turning it off is yours. Independent of both switches above it."},
			ui.FormField{Field: "public_name", Type: "text", Label: "Published app name",
				Placeholder: "(uses the agent name above when blank)",
				Help:        "Optional. Name shown on the dashboard card and URL slug.",
				Detail:      "Set it when the internal name reads awkwardly as an app title.",
				SuggestURL:  "../api/agents/suggest"},
			ui.FormField{Field: "allow_private_mode", Type: "toggle", Label: "Allow Private mode",
				Help:   "Shows a Private toggle on the public chat, which drops network tools per turn.",
				Detail: "Leave it off for Research-style agents that need network."},
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
				Help:   "Both directions of agent-to-agent calling.",
				Detail: "Who may call THIS agent (fleet visibility), and who this agent may call (dispatch policy plus the target list below, which is only consulted in the two \"selected\" modes)."},
			ui.FormField{Field: "hidden", Type: "toggle", Label: "Hide from agent fleet",
				Help:   "Off (default) = globally callable. On drops the agent from the fleet and refuses dispatch.",
				Detail: "Globally callable means it appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). Hidden, it is dropped from that block and dispatch is refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list.\n\nThis affects FLEET visibility only. The agent still appears in your own Agents picker and stays reachable at its dashboard URL when published. Use it for personal agents, or Builder-authored sub-agents you do not want the fleet routing to."},

			ui.FormField{Field: "dispatch_mode", Type: "select", Label: "Dispatch policy",
				Options: dispatchModeOptions(dispatchModeFirst),
				Help:    "Which other agents this one may call via agents(action=\"run\").",
				Detail:  "This governs ordinary agent-to-agent calls whether or not the conductor tools above are on. Allow all means any non-hidden agent, and is the default. Only allow, and Allow all except, draw from the target list directly below. Allow none blocks all dispatch and is the actual delegation kill switch. Same control as the in-chat Configure, then Security & Access modal."},
			ui.FormField{Field: "allow_builder_dispatch", Type: "toggle", Label: "Can dispatch Builder",
				Help:   "Lets this agent hand work to Builder, to author an agent, tool or app on its behalf.",
				Detail: "The call is agents(action=\"run\", agent=\"builder\"). Off by default and normally reserved to conductor agents (Chat), because authoring expects a human in the loop: the intake conversation, its clarifying pauses, and your review of the draft. Turning it on trades that for reach; whatever Builder creates on a dispatch still lands held for your approval rather than going live.\n\nThis is a separate grant from \"Authoring tools\" above. That one has the agent build things ITSELF, this one has it ask Builder to. Builder appears in this agent's Available Agents block only while it is on, and it is overridden by Dispatch policy = Allow none."},
			ui.FormField{Type: "header", Label: "Intake & evals", Collapsed: true,
				Help: "Optional structured input form + saved test cases."},
			ui.FormField{Field: "evals", Type: "textarea", Label: "Eval cases (JSON)", Rows: 6,
				Help:        "Optional. Saved test cases for the eval harness.",
				Detail:      "Run them via POST /api/agents/<id>/eval to grade the agent against each case. POST /api/agents/<id>/eval-suite copies them into a standalone eval SUITE: the same cases plus the things a field cannot have, namely a run history, a fingerprint of the version each run graded, and a surface to watch a long run on. The copy leaves these cases exactly as they are.\n\nFormat: a JSON array of {name, prompt, must_include, must_not_include, judge_prompt, notes}. must_include and must_not_include are case-insensitive substring checks; judge_prompt is an optional LLM-as-judge criterion. Use it to lock in expected behavior before editing the orchestrator_prompt, so regressions are visible.",
				Placeholder: "[\n  {\"name\": \"asks_clarifying\", \"prompt\": \"I want to compare these products\",\n   \"judge_prompt\": \"the reply asks at least one clarifying question rather than guessing which products\"},\n  {\"name\": \"cites_sources\", \"prompt\": \"What's TS3's default port?\",\n   \"must_include\": [\"10080\"], \"judge_prompt\": \"the reply cites the source URL\"}\n]",
				SuggestURL:  "../api/agents/suggest"},
			ui.FormField{Field: "intake_form", Type: "textarea", Label: "Intake form (JSON)", Rows: 6,
				Help:        "Optional. A form shown instead of the text input on the first turn of a new session.",
				Detail:      "Submitting packs the values into a markdown user message and uploads any file fields as attachments. PDFs and DOCX get text-extracted server-side, images go to vision. Leave it blank for a normal chat-first agent.\n\nFormat: a JSON array of {name, label, type, placeholder, help, required, options}. type is \"text\" (the default), \"textarea\", \"select\" (single-choice dropdown), \"checklist\" (multi-pick checkboxes, whose selected values get comma-joined into the packed markdown), \"number\", \"file\", or \"button\" (self-submitting). options is an array of strings, used by select, checklist and button.",
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
		Subtitle: "Identity, prompts, and behavior.",
		Detail:   "Clone an existing agent from the landing page if you want a quick copy to tweak.",
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
			agentSection.Subtitle = "Owned by parent agent: " + parentName + ". Sub-agents are focused capability components called by their parent via dispatch: public surfaces, intake form, memory, and explorer mode are structurally off and hidden from this editor."
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

	// What this agent can do, and what it can reach — the two halves of the
	// question an owner asks after granting things one control at a time. Read
	// from the record, so an agent that has never run still answers.
	if id != "" {
		sections = append(sections,
			ui.Section{
				Title:    "What this agent can do",
				Subtitle: accessSummary + " " + accessCaveat,
				Body: ui.Table{
					Source:    "../api/agent-access?id=" + id,
					RowKey:    "name",
					EmptyText: accessEmpty,
					Columns: []ui.Col{
						{Field: "name", Label: "Tool"},
						{Field: "detail", Label: "What it does", Mute: true},
						{Field: "policy", Label: "Unattended", Type: "badge"},
					},
				},
			},
			ui.Section{
				Title: "What it can hand work to",
				Subtitle: "Delegation reaches past this agent's own tools: whatever it hands work to runs with ITS catalog. " +
					"Only targets that add something are listed; a recipe appears when one of its steps runs an agent.",
				Body: ui.Table{
					Source:    "../api/agent-access?id=" + id + "&view=reach",
					RowKey:    "name",
					EmptyText: "Nothing. This agent cannot hand work to anything that would widen it.",
					Columns: []ui.Col{
						{Field: "name", Label: "Target"},
						{Field: "kind", Label: "Kind", Mute: true},
						{Field: "adds", Label: "What it adds", Mute: true},
					},
				},
			},
		)
	}

	// External credentials — tier-2 per-agent scoping, relocated here
	// from the admin credential page (which now only governs tier-1: which USERS
	// may use a credential). Agent-centric so each user sees only their own fleet.
	// The picker is an allowlist (checked = may use); the endpoint inverts it onto
	// the AgentRecord.DisabledCredentials opt-out. Existing, non-sub agents only.
	if id != "" && !subAgent {
		sections = append(sections, ui.Section{
			Title:    "External credentials",
			Subtitle: "The APIs you've been granted. All are on by default.",
			Detail:   "Uncheck any this agent should not reach; that drops the tools which dispatch through them from its kit.\n\nSecured credentials are not listed. Their access follows their tool bindings, not per-agent scope.",
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
			Subtitle: "Every picture this agent has kept for reuse. Look at them.",
			Detail:   "A name, a caption and an origin can all be confidently wrong together, and only the picture settles it.\n\n\"Unrecorded\" origin means nobody captured where it came from. It may be something the agent made, so do not trust it as a likeness until you have looked.\n\nForget what should not be here, and label anyone the agent has not identified, so a request naming them finds the right face. If two rows show the same person, both are flagged: the agent will pick one and you will not know which, so forget whichever is wrong.",
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
		// ONE rail entry with its parts nested under it, not three siblings.
		// Sharing is one operation asked in three steps — who gets it, what
		// they get, what it depends on — and a flat rail drew them as three
		// unrelated settings, so somebody who went to Share found the
		// recipient picker and no reason to believe the rest existed.
		sections = append(sections, ui.Section{
			Title:    "Share",
			Subtitle: shareSubtitleFor(shareRec),
			Detail: "They run your agent, and what it uses travels with it: your tools, your documents, your skills, readable through this agent and nowhere else. They cannot attach any of it to an agent of their own.\n\n" +
				"A credential is the exception, because it is whose identity a call goes out as rather than a copy anybody is missing. Decide that per key on its row under What it reaches.\n\n" +
				"Once an admin has PUBLISHED this agent, the list below narrows inside their grant rather than adding to it: somebody has to be allowed the app AND be on your list. Leaving it empty means everybody the admin allowed. An admin can audit or revoke shares either way.",
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
		// How it reaches them, between the list of WHO and the inventory of
		// WHAT. The three questions are one decision and they are asked in the
		// order they are answered: who gets it, what they meet when they open
		// it, and what it depends on.
		sections = append(sections, ui.Section{
			Title:    "What they get",
			Indent:   1,
			Subtitle: "Everyone you share with is a reader. These say how much of what this agent knows they read.",
			Detail: "Nobody you share with can change this agent: not its persona, its rules, its tools, its documents, or who else has it. None of that is a setting.\n\n" +
				"Two things are theirs and only theirs. Their conversations with it, and anything they upload to it. Neither reaches you, and neither reaches anybody else you shared with.",
			Body: ui.FormPanel{
				Source: source,
				// PATCH, and the id in the QUERY, exactly as splitAgentFormSections
				// builds every other section on this page. A POST here sends this
				// panel's four fields AS THE WHOLE RECORD and wipes the rest of the
				// agent, which is the reason that function exists at all.
				PostURL: "../api/agents?id=" + url.QueryEscape(id),
				Method:  "PATCH",
				Fields: []ui.FormField{
					{
						Field: "share_hold_cortex", Type: "toggle", Label: "Keep its standing activity to yourself",
						Help: "Off, the default, means they see it. This is what makes a shared agent feel like it knows things.",
						Detail: "The cortex is the agent's own mind: recent events on its channels and monitors, which you shaped by pointing it at them. A recipient reads it and can never open it as a thread, and their turns never write into it.\n\n" +
							"Turn this on when the cortex has become a record of your own week rather than the agent's job.",
					},
					{
						Field: "share_hold_reference", Type: "toggle", Label: "Keep what it worked out to yourself",
						Help: "Off, the default, means their searches also cover it.",
						Detail: "The least deliberate thing the agent holds: what it inferred across your conversations without being asked to. Documents you uploaded are not this and travel either way, the same as the collections you attached.\n\n" +
							"Read what is in it before deciding. This is the layer most likely to carry a sentence you have forgotten saying.",
					},
					{
						Field: "share_memory_explicit", Type: "toggle", Label: "Let them see its saved notes",
						Help: "Off, the default. This layer has never travelled.",
						Detail: "The facts the agent kept while talking to you, in every turn's prompt. Not curated: whatever came up, including things you never decided to tell anybody. Read them before you turn this on.\n\n" +
							"Theirs sit above yours, so where the two disagree the person in the conversation has the last word. They cannot edit or forget any of yours.",
					},
					{
						Field: "share_no_uploads", Type: "toggle", Label: "They may not add documents of their own",
						Help: "Off means they can upload; their files stay private to them.",
						Detail: "Anything they upload is searched for their turns alongside this agent's collections, and is not visible to you or to anybody else you shared with.\n\n" +
							"Turn this on where the agent must answer from an approved corpus and nothing else.",
					},
				},
			},
		})

		// What the agent actually reaches, right underneath the picker that
		// decides who gets it. The two questions are asked together — "share
		// this with my team" is one request, and the tools, documents, skills
		// and recipes behind it are consequences of it rather than four more
		// things to remember.
		sections = append(sections, ui.Section{
			Title:    "What it reaches",
			Indent:   1,
			Wide:     true,
			Subtitle: "Everything it depends on, and how far each of those goes today.",
			Detail: "Everything here travels with the agent and is scoped to it: whoever runs it reads your documents, runs your tools and activates your skills THROUGH this agent, and nowhere else. They cannot attach any of it to an agent of their own.\n\n" +
				"One exception, and it is the only thing a share has to ask about. A credential is not a copy somebody is missing — it is whose identity the call goes out as — so it resolves by name in the namespace of whoever is running, and you decide per key whether to lend yours or let them bring their own. That choice is made when you share, in Sharing.\n\n" +
				"Nothing here changes anything. It is the list to check before you share, and the answer to \"why does it work for me and not for them\" afterwards.",
			Body: ui.Table{
				Source: source + "/reach",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "kind", Label: "Kind", Flex: 0},
					{Field: "name", Flex: 1},
					{Field: "reach", Label: "Reach", Flex: 1},
					{Field: "missing", Label: "", Flex: 1},
					{Field: "fix", Label: "To decide", Flex: 2},
					{Field: "how", Label: "How a run finds it", Mute: true, Flex: 3},
				},
				EmptyText: "This agent depends on nothing of yours. Anybody you share it with gets all of it.",
				// The one row kind with a decision on it. Everything else here
				// states a fact, so it gets no control; a credential is whose
				// identity the call goes out as, and that is set per key, on
				// the row that raised it, rather than only inside a share flow
				// that ran once and cannot be revisited.
				RowActions: []ui.RowAction{
					{
						Type: "segmented", Field: "lend", OnlyIf: "decide",
						PostTo: source + "/reach/credential",
						Options: []ui.SelectOption{
							{Value: credOwn, Label: "Theirs"},
							{Value: credRead, Label: "Mine, reads"},
							{Value: credWrite, Label: "Mine, writes"},
							{Value: shareSkip, Label: "Off"},
						},
					},
				},
			},
		})
	}

	// (Phantom dispatch + wipe sections removed — phantom's per-chat dispatch
	// surface is retiring with phantom; channel threads are inspected via the
	// rail + the channel-scoped chat tools now.)

	// Built from: shown only for an agent that FOLLOWS a framework shape.
	// Tracking is invisible otherwise, and an agent whose prompt can change
	// under its owner has to say so somewhere they will see it. The exit is
	// here beside the explanation rather than in a menu, because the question
	// "will this change without me?" and the answer "not if I stop it" belong
	// on the same screen.
	if id != "" && !isSeedID(id) {
		if rec, ok := loadAgent(udb, id); ok && rec.ShapeID != "" {
			shapeName := rec.ShapeID
			if doc, ok := archetypeBySlug(rec.ShapeID); ok {
				shapeName = doc.Slug
			}
			kept := "Nothing yet, so all of it follows the framework."
			if n := len(rec.OverriddenFields); n > 0 {
				kept = fmt.Sprintf("%d field%s stay yours; everything else follows the framework.",
					n, plural(n))
			}
			sections = append(sections, ui.Section{
				Title: "Built from the " + shapeName + " shape",
				Subtitle: "Fields you have changed here are yours permanently. " + kept +
					" That is how a framework improvement reaches an agent you made months ago. Stop following to freeze this agent exactly as it reads now.",
				Body: ui.DisplayPanel{
					Source: "../api/agents/" + id,
					Pairs:  []ui.DisplayPair{},
					Actions: []ui.ToolbarAction{
						{
							Label:   "Stop following the shape",
							Method:  "POST",
							URL:     "../api/agents/" + id + "/detach",
							Confirm: "Freeze this agent as it reads now? It keeps everything it has, and stops receiving framework improvements to the fields you never changed. This cannot be undone.",
						},
					},
				},
			})
		}
	}

	// Delete — the human's authoritative remove for any existing agent the editor
	// is open on, INCLUDING a sub-agent reached via the picker (which agents
	// can't delete once the cross-agent lock is in place). Non-seed only: seeds
	// revert via their own path, they aren't "deleted". The DELETE handler
	// cascades sub-agents and cleans channels / monitors / standing agents /
	// dispatch-allowlist references.
	if id != "" && !isSeedID(id) {
		sections = append(sections, ui.Section{
			Title:    "Delete agent",
			Subtitle: "Permanently remove this agent. This cannot be undone.",
			Detail:   "Its sessions, memory, knowledge and any sub-agents it owns go with it. Channels, monitors and standing agents bound to it are cleaned up too.",
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
		lockHead += agentAssistHTML(id)
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
/* Greyed controls when the record is locked: non-interactive + visibly dimmed,
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
    b.title=locked?'Locked, only you can edit or delete (click to unlock)':'Unlocked, click to lock so other agents cannot edit or delete';
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
    // FIRST section card's header-right slot: the lock belongs on the record,
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

// agentAssistHTML mounts the "Assist" button beside the lock, and the dialog
// behind it: a conversation about the WHOLE agent that answers with changes
// you accept.
//
// App-specific behavior, so it rides in through ExtraHeadHTML rather than into
// core/ui, same as the lock icon. It reuses uiOpenModal (THE modal) and the
// existing PATCH endpoint to apply, which is what keeps it small: no new
// persistence path, no form-field plumbing, and a reload afterwards so the
// editor is showing what was actually stored rather than what we hoped.
//
// Why a whole-agent dialog next to a per-field one: the field workbench sees
// the field it was opened on, so it cannot notice that an agent has no rules,
// or that its allowlist and its persona disagree about whether it browses. The
// answer to "stop it making things up" is a rule plus a line in the persona,
// not one line in whichever box happened to be focused.
func agentAssistHTML(id string) string {
	return fmt.Sprintf(`<style>
#agent-assist{cursor:pointer;border:none;background:none;font-size:1.05rem;line-height:1;opacity:.85;padding:0 .2rem}
#agent-assist:hover{opacity:1;transform:scale(1.1)}
#agent-assist[disabled]{opacity:.4;cursor:wait}
.aa-log{display:flex;flex-direction:column;gap:.5rem;margin-bottom:.75rem;max-height:38vh;overflow-y:auto}
.aa-msg{padding:.45rem .6rem;border-radius:6px;font-size:.9rem;line-height:1.45;white-space:pre-wrap}
.aa-you{background:var(--bg-2);align-self:flex-end;max-width:85%%}
.aa-them{background:var(--bg-2);border-left:3px solid var(--accent,#6366f1)}
.aa-change{border:1px solid var(--border);border-radius:6px;padding:.5rem .6rem;margin:.4rem 0;background:var(--bg-2)}
.aa-change label{display:flex;gap:.5rem;align-items:baseline;cursor:pointer;font-weight:600}
.aa-why{font-size:.85rem;opacity:.8;margin:.2rem 0 .35rem 1.4rem}
.aa-val{margin-left:1.4rem;font-size:.85rem;white-space:pre-wrap;max-height:9rem;overflow:auto;padding:.4rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px}
.aa-row{display:flex;gap:.5rem;align-items:flex-end}
.aa-row textarea{flex:1;min-height:3.2rem;resize:vertical}
</style>
<script>
(function(){
  var id=%q, history=[], pending=[];
  function el(t,a,kids){var n=document.createElement(t);a=a||{};for(var k in a){if(k==='class')n.className=a[k];else if(k==='text')n.textContent=a[k];else n.setAttribute(k,a[k]);}
    (kids||[]).forEach(function(c){n.appendChild(c);});return n;}

  function open(){
    window.uiOpenModal({
      title:'Assist',
      subtitle:'Ask for a change and review what it proposes. Nothing is saved until you apply.',
      width:'760px',
      actions:[],
      mount:function(body,api){
        var log=el('div',{class:'aa-log'});
        var changes=el('div');
        var input=el('textarea',{placeholder:'What should be different? For example: it keeps answering from memory, make it stick to sources.'});
        var send=el('button',{type:'button',class:'ui-btn primary',text:'Ask'});
        var apply=el('button',{type:'button',class:'ui-btn',text:'Apply selected'});
        apply.style.display='none';
        var close=el('button',{type:'button',class:'ui-btn',text:'Close'});
        close.onclick=function(){api.close();};

        function say(role,text){
          log.appendChild(el('div',{class:'aa-msg '+(role==='you'?'aa-you':'aa-them'),text:text}));
          log.scrollTop=log.scrollHeight;
        }
        function renderChanges(list){
          changes.innerHTML=''; pending=list||[];
          apply.style.display=pending.length?'':'none';
          pending.forEach(function(c,i){
            var box=el('div',{class:'aa-change'});
            var cb=el('input',{type:'checkbox'}); cb.checked=true; cb.dataset.i=String(i);
            var lab=el('label'); lab.appendChild(cb); lab.appendChild(el('span',{text:c.label}));
            box.appendChild(lab);
            if(c.why) box.appendChild(el('div',{class:'aa-why',text:c.why}));
            box.appendChild(el('div',{class:'aa-val',text:c.value}));
            changes.appendChild(box);
          });
        }
        function ask(){
          var msg=(input.value||'').trim();
          if(!msg) return;
          say('you',msg); input.value=''; send.disabled=true; send.textContent='Thinking…';
          fetch('../api/agents/'+id+'/assist',{method:'POST',headers:{'Content-Type':'application/json'},
            body:JSON.stringify({message:msg,history:history})})
            .then(function(r){ if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status));}); return r.json(); })
            .then(function(d){
              history.push({role:'user',content:msg});
              if(d.reply) history.push({role:'assistant',content:d.reply});
              say('them',d.reply||'(no reply)');
              renderChanges(d.changes);
            })
            .catch(function(e){ say('them','Could not ask: '+((e&&e.message)||e)); })
            .then(function(){ send.disabled=false; send.textContent='Ask'; });
        }
        send.onclick=ask;
        input.addEventListener('keydown',function(ev){
          if(ev.key==='Enter'&&(ev.metaKey||ev.ctrlKey)){ ev.preventDefault(); ask(); }
        });
        apply.onclick=function(){
          var patch={},n=0;
          // Index loop, not NodeList.forEach: this ships to whatever WebView
          // the user's phone has, and the rest of the runtime walks nodes the
          // same way for the same reason.
          var boxes=changes.querySelectorAll('input[type=checkbox]');
          for(var bi=0;bi<boxes.length;bi++){
            var cb=boxes[bi];
            if(!cb.checked) continue;
            var c=pending[parseInt(cb.dataset.i,10)];
            if(!c) continue;
            // triggers is a list on the record; every other assistable field
            // is text. Sending a string here would store one trigger that is
            // the whole box.
            patch[c.field]=(c.field==='triggers')
              ? c.value.split('\n').map(function(s){return s.trim();}).filter(Boolean)
              : c.value;
            n++;
          }
          if(!n){ say('them','Nothing selected.'); return; }
          apply.disabled=true; apply.textContent='Applying…';
          fetch('../api/agents/'+id,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify(patch)})
            .then(function(r){ if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status));}); location.reload(); })
            .catch(function(e){ apply.disabled=false; apply.textContent='Apply selected'; say('them','Could not apply: '+((e&&e.message)||e)); });
        };

        body.appendChild(log);
        body.appendChild(changes);
        body.appendChild(el('div',{class:'aa-row'},[input,send]));
        var foot=el('div',{class:'aa-row'}); foot.style.marginTop='.75rem'; foot.style.justifyContent='flex-end';
        foot.appendChild(apply); foot.appendChild(close);
        body.appendChild(foot);
        say('them','Tell me what is not working about this agent, or what you want it to do differently. I will propose changes you can review before anything is saved.');
        input.focus();
      }
    });
  }

  var b=document.createElement('button');
  b.id='agent-assist'; b.type='button'; b.textContent='✨';
  b.title='Assist: talk about the whole agent and review proposed changes';
  b.onclick=open;
  var tries=0;
  function mount(){
    if(document.getElementById('agent-assist')) return;
    var slot=document.querySelector('.ui-section .ui-section-h-r');
    if(slot){ slot.insertBefore(b, slot.firstChild); return; }
    if(tries++ < 180) requestAnimationFrame(mount);
  }
  mount();
})();
</script>`, id)
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
		return "Currently **Only allow selected**: this agent may call ONLY the agents and pipelines ticked here, including any Hidden agents you pick." + where
	case dispatchExcept:
		return "Currently **Allow all except selected**: this agent may call any non-hidden agent, and any pipeline, EXCEPT the ones ticked here." + where
	case dispatchNone:
		return "Currently **Allow none**: this agent dispatches to nobody, agents and pipelines alike, so this list has no effect until you change the policy." + where
	default:
		return "Currently **Allow all**: this agent may call any non-hidden agent and any of your pipelines, so this list has no effect. It applies only in \"Only allow selected\" or \"Allow all except selected\" mode." + where
	}
}

func dispatchModeOptions(first string) []ui.SelectOption {
	all := []ui.SelectOption{
		{Value: dispatchAll, Label: "Allow all: any non-hidden agent (default)"},
		{Value: dispatchOnly, Label: "Only allow selected (target list below)"},
		{Value: dispatchExcept, Label: "Allow all except selected (target list below)"},
		{Value: dispatchNone, Label: "Allow none: no dispatch at all"},
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
	b.WriteString("\n\nGRANTED BY OTHER APPS: read-only here; the framework knows WHICH app granted what, and only the app knows what its permission means, so the detail stays where it can be edited honestly.\n")
	for _, s := range summaries {
		fmt.Fprintf(&b, "%s: %s", s.Label, s.Text)
		var detail []string
		for _, g := range s.Grants {
			if g.Detail != "" {
				detail = append(detail, g.Label+" · "+g.Detail)
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
			Label: "Authoring tools: always on for Builder",
			Help:  "Builder holds the authoring catalog as its IDENTITY, not as a grant, so there is nothing to switch here.",
			Detail: "The catalog is survey, create/update/clone agents, tool_def, app_def, skill_def, credential drafting, and bridge/connector. To have an agent that builds without being Builder, turn this capability on for that agent instead." +
				"\n\nAuthoring is owner-only at runtime: if a turn runs as someone other than this agent's owner, the catalog is withheld and the reason is recorded in the session diagnostics.",
		}
	}
	return ui.FormField{Field: "author", Type: "toggle", Label: "Authoring tools (build agents, tools, apps)",
		Help:   "Grants the full authoring toolset: the same catalog the Builder agent holds.",
		Detail: "That is survey (map what already exists), create/update/clone agents, tool_def, app_def, skill_def, the credential draft and probe tools, bridge/connector, and, when you own the agent and it also holds the conductor tools, scheduling and monitors to wire a built tool live.\n\nThis is the de-silo of Builder: authoring is a capability any capable agent can hold, so it can BUILD new agents, tools and apps on the gohort framework the way Builder does, not just run pre-built ones. Independent of the conductor tools above. Like them, an authoring agent reaches owner-only endpoints, so it is never published publicly."}
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
	identity := panel(first)
	// History hangs off the FIRST section only. Every section here is its own
	// FormPanel over the same record, so setting it on the shared panel builder
	// would put the same button on each one — one control, repeated eight
	// times, all opening the same list.
	identity.HistoryURL = "../api/agents/" + url.PathEscape(id) + "/revisions"
	identity.HistoryLabel = "Version history"
	out := []ui.Section{{
		Title:    "Agent",
		Subtitle: identitySubtitle,
		Body:     identity,
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
		Help:   "Run this agent's orchestrator and synthesis turns on the lead model, not the local worker.",
		Detail: "The lead model is remote and costs more per turn; the worker is local and free. The dispatched per-step worker phases still run on the worker. Off by default.\n\nAutomatically ignored on a Private turn, where the conversation stays local. The exception is Admin, LLMs, Model Privacy saying every model is private, in which case escalating keeps it local too.\n\nUsually you do not need this. An agent holding the consult tool already asks the lead ONE self-contained question when it hits a wall, at a fraction of the cost of escalating every round. Reach for this toggle when the agent's own reasoning, rather than one hard question, is what needs the stronger model.",
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
	opts := []ui.SelectOption{{Value: "", Label: "None: the persona above governs every turn"}}
	for _, d := range defs {
		label := d.Name + " (" + strconv.Itoa(len(d.Phases)) + " phases)"
		if desc := strings.TrimSpace(d.Description); desc != "" {
			label += " · " + desc
		}
		opts = append(opts, ui.SelectOption{Value: d.ID, Label: label})
	}
	return ui.FormField{
		Field: "machine", Type: "select", Label: "Phase machine", Options: opts,
		Help: "Optional. A machine gives this agent phases it moves through and stays in.",
		Detail: "It works out what is being asked once, picks an approach once, then answers in that frame for the rest of the thread instead of re-deciding every turn. The persona above still supplies identity and voice; the machine supplies procedure. Sessions already open keep the machine they started with, so this applies to new ones." +
			// The shape people ask for by name and cannot find, because it is
			// not a setting anywhere: an agent that goes and LOOKS before it
			// answers. It is a two-phase machine, and the reason it is not a
			// checkbox here is that what \"look\" means — which tools, what
			// counts as determined — is different for every subject, and a
			// checkbox has nowhere to say it.
			"\n\nThis is also how you make an agent that INVESTIGATES before it answers: a first phase that goes and looks (set its reach to read-only, so it can inspect and never act), then a phase that answers only from what it found. Its probes never enter the conversation, so the thread stays small. " +
			"\n\nAuthor machines from chat with the `machine` tool, or describe one in plain words at Extensions, Machines, Describe one.",
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

// shareSubtitleFor says what the list in front of somebody actually decides,
// which differs entirely once an agent is published.
//
// Before publication it is the whole grant. After, it is a narrowing inside
// the admin's, and a line that still said "let specific users run this" would
// be describing a control that no longer does that on its own.
func shareSubtitleFor(a AgentRecord) string {
	if !a.Everyone && !a.MCPExposed {
		return "Let specific other users run this agent. Empty means private to you."
	}
	if len(a.AllowedUsers) == 0 {
		return "Published: everybody the admin has granted this app can run it. Name people here to narrow it to them."
	}
	return "Published, and narrowed by you to " + strings.Join(a.AllowedUsers, ", ") +
		". They also need the admin's grant of the app; this list can only narrow it, never widen it."
}
