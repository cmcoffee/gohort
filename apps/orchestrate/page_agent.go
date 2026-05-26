package orchestrate

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleAgentPage routes the agent editor.
//
//	GET /agent/new        — create a new blank agent
//	GET /agent/{id}       — edit existing agent
func (T *OrchestrateApp) handleAgentPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/agent/")
	if rest == "" || strings.Contains(rest, "/") {
		http.NotFound(w, r)
		return
	}
	if rest == "new" {
		T.renderAgentEditor(w, r, "")
		return
	}
	T.renderAgentEditor(w, r, rest)
}

// renderAgentEditor shows the agent editor. When id is empty the form
// is blank (create mode); otherwise FormPanel.Source loads the
// existing record so fields prefill.
func (T *OrchestrateApp) renderAgentEditor(w http.ResponseWriter, r *http.Request, id string) {
	source := ""
	title := "New agent"
	if id != "" {
		source = "../api/agents/" + id
		title = "Edit agent"
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

	sections := []ui.Section{
		{
			Title:    "Agent",
			Subtitle: "Identity, prompts, and behavior. Clone an existing agent from the landing page if you want a quick copy to tweak.",
			Body: ui.FormPanel{
				Source:         source,
				PostURL:        "../api/agents",
				Method:         "POST",
				SubmitLabel:    "Save agent",
				RedirectURL:    redirectURL,
				RedirectTarget: "_self",
				Fields: []ui.FormField{
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
					{Field: "allow_explorer", Type: "toggle", Label: "Allow explorer mode",
						Help: "Lets the worker lift its round budget mid-turn (up to 50). For agents mapping unfamiliar APIs."},
					{Type: "header", Label: "Memory",
						Help: "What the agent remembers across turns. Knowledge (uploaded files) is always available."},
					{Field: "memory_mode", Type: "select", Label: "Memory mode",
						Options: []ui.SelectOption{
							{Value: "agent", Label: "Agent — generalized lessons only"},
							{Value: "chatbot", Label: "Chatbot — lessons + user personalization"},
						},
						Help: "Shapes what store_fact stores. Agent (default): generalized lessons only — specifics go to memory_save (Inferred). Chatbot: same + user personalization + conversation notes."},
					{Field: "disable_explicit", Type: "toggle", Label: "Disable Explicit Memory",
						Help: "Strips store_fact tools + pre-injected facts block. For impersonal / stateless agents."},
					{Field: "disable_inferred", Type: "toggle", Label: "Disable Reference Memory",
						Help: "Strips memory_save/search/forget + synthesis auto-ingest. For agents that should answer from authoritative sources only. Per-turn Clean toggle = same, scoped to one turn."},
					{Field: "disable_skills", Type: "toggle", Label: "Disable skills",
						Help: "Suppresses the skills classifier entirely — no skills fire regardless of the per-agent allowlist. For KB readers / doc-Q&A / compliance agents that should never load skill addendums."},

					{Type: "header", Label: "Publishing",
						Help: "Who can use this agent and under what restrictions."},
					{Field: "exposed", Type: "toggle", Label: "Publish as public app",
						Help: "Shows on /agents/ for non-admin users. Each end-user gets their own sessions + facts under the agent."},
					{Field: "public_name", Type: "text", Label: "Public app name",
						Placeholder: "(uses the agent name above when blank)",
						Help:        "Optional. Name shown in /agents/ + URL slug. Set when the internal name reads awkwardly as an app title.",
						SuggestURL:  "../api/agents/suggest"},
					{Field: "allow_private_mode", Type: "toggle", Label: "Allow Private mode",
						Help: "Shows a Private toggle on the public chat — drops network tools per turn. Off for Research-style agents that need network."},
					{Field: "force_private", Type: "toggle", Label: "Force Private mode (network locked off)",
						Help: "Permanently drops network + sub-agent dispatch tools. For compliance / confidential / family-facing agents."},
					{Field: "hidden", Type: "toggle", Label: "Hide from agent fleet",
						Help: "Off (default) = globally callable: appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). On = hidden: dropped from the fleet block and dispatch is refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list. Use for personal agents or Builder-authored sub-agents you don't want the fleet routing to."},

					{Type: "header", Label: "Intake & evals",
						Help: "Optional structured input form + saved test cases."},
					{Field: "evals", Type: "textarea", Label: "Eval cases (JSON)", Rows: 6,
						Help:        "Optional. Saved test cases for the eval harness. Run via POST /api/agents/<id>/eval to grade the agent against each case. Format: a JSON array of {name, prompt, must_include, must_not_include, judge_prompt, notes}. must_include / must_not_include are case-insensitive substring checks; judge_prompt is an optional LLM-as-judge criterion. Use to lock in expected behavior before editing the orchestrator_prompt so regressions are visible.",
						Placeholder: `[
  {"name": "asks_clarifying", "prompt": "I want to compare these products",
   "judge_prompt": "the reply asks at least one clarifying question rather than guessing which products"},
  {"name": "cites_sources", "prompt": "What's TS3's default port?",
   "must_include": ["10080"], "judge_prompt": "the reply cites the source URL"}
]`,
						SuggestURL: "../api/agents/suggest"},
					{Field: "intake_form", Type: "textarea", Label: "Intake form (JSON)", Rows: 6,
						Help:        "Optional. When set, the chat shows this form INSTEAD of the text input on the first turn of every new session. Submitting packs the values into a markdown user message + uploads any file fields as attachments (PDFs/DOCX get text-extracted server-side, images go to vision). Leave blank for a normal chat-first agent. Format: a JSON array of {name, label, type, placeholder, help, required, options}. type: \"text\" (default), \"textarea\", \"select\" (single-choice dropdown), \"checklist\" (multi-pick checkboxes — selected values get comma-joined in the packed markdown), \"number\", \"file\", \"button\" (self-submitting). options: array of strings, used by select / checklist / button.",
						Placeholder: `[
  {"name": "company", "label": "Company name", "type": "text", "required": true},
  {"name": "audience", "label": "Target audience", "type": "textarea"},
  {"name": "deadline", "label": "Deadline", "type": "select", "options": ["This week", "This month", "No rush"]},
  {"name": "topics", "label": "Topics of interest", "type": "checklist", "options": ["AI", "Healthcare", "Finance", "Education"]}
]`,
						SuggestURL: "../api/agents/suggest"},
				},
			},
		},
	}

	// Sub-agent dispatch allowlist. Only renders for existing agents
	// (need a known ID to wire the picker's record/post URLs). The
	// picker shows every agent the user owns; toggle a row to add /
	// remove it from this agent's allowlist. Empty list = "any non-
	// hidden agent" (default fleet routing); any picks = "ONLY these"
	// (allowlist mode — overrides the default + reaches hidden agents).
	if id != "" {
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

	page := ui.Page{
		Title:     title,
		ShowTitle: true,
		BackURL:   "..",
		MaxWidth:  "900px",
		Sections:  sections,
	}
	page.ServeHTTP(w, r)
}
