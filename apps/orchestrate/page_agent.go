package orchestrate

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
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
			Values: map[string]any{"channel": true, "memory_mode": memoryModeChatbot, "fleet": false, "recall_hints": true},
		},
		{
			Label:  "Specialist: a focused agent for one job, used directly or by dispatch",
			Values: map[string]any{"channel": false, "memory_mode": memoryModeAgent, "fleet": false, "recall_hints": true},
		},
	}
}

func (T *OrchestrateApp) handleAgentPage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/agent/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	// One sub-path, for the surfaces that are ABOUT an agent rather than part
	// of editing it.
	if id, action, found := strings.Cut(rest, "/"); found {
		// The fleet page under the same path, because it answers the same
		// question one level up: "all" is not an agent id, and an agent cannot
		// be called that (findAgentByNameOrID would have to match it first).
		if action == "access" && id == fleetSecurityID {
			T.renderFleetSecurity(w, r, user, udb)
			return
		}
		if action == "access" && id != "" {
			T.renderAgentAccess(w, r, user, udb, id)
			return
		}
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
	ed := &agentEditor{T: T, w: w, r: r, user: user, udb: udb, id: id}
	if !ed.answerAppAgent() {
		return
	}
	ed.load()
	ed.formFields()
	ed.templates()
	ed.mainSection()
	ed.splitSections()
	ed.picturesSection()
	ed.shareSection()
	ed.shapeSection()
	ed.usedBySection()
	ed.deleteSection()
	ed.serve()
}

// agentEditor is one render of the agent editor: who the agent is (or
// that it is new, or a sub-agent), its form, and the sections an existing
// agent adds below it. Each field is a local of renderAgentEditor, which is
// now the order of the phases.
type agentEditor struct {
	T    *OrchestrateApp
	w    http.ResponseWriter
	r    *http.Request
	user string
	udb  Database
	id   string

	// Who it is (load).
	source           string
	title            string
	subAgent         bool
	parentName       string
	subAgentParentID string
	leadModelLocked  bool
	editRec          AgentRecord // for fields that say where their answer came from

	// The form and the sections it becomes.
	redirectURL    string
	fields         []ui.FormField
	agentTemplates []ui.FormTemplate
	agentSection   ui.Section
	sections       []ui.Section
}

func (ed *agentEditor) answerAppAgent() bool {
	T := ed.T
	w := ed.w
	r := ed.r
	udb := ed.udb
	id := ed.id
	if spec, isApp := appagents.AppAgentByID(id); isApp {
		T.renderAppAgentEditor(w, r, udb, id, spec)
		return false
	}
	return true
}

func (ed *agentEditor) load() {
	r := ed.r
	udb := ed.udb
	id := ed.id
	ed.source = ""
	ed.title = "New agent"
	ed.subAgent = false
	ed.parentName = ""
	// owned_by from the URL marks "create as a sub-agent owned by this
	// parent" — set by the Create button's confirm dialog. Drives the
	// sub-agent layout (mask publishing / intake / memory) and gets
	// baked into the form via a hidden field so the POST persists the
	// parent link.
	ed.subAgentParentID = ""
	if id == "" {
		if v := strings.TrimSpace(r.URL.Query().Get("owned_by")); v != "" {
			if parent, ok := loadAgent(udb, v); ok {
				ed.subAgentParentID = v
				ed.subAgent = true
				ed.parentName = parent.Name
				ed.title = "New sub-agent"
			}
		}
	}
	// ForcePrivate agents can't escalate to the remote lead model (gate 2),
	// so the "Use Lead model" toggle is hidden for them — unless the operator
	// has declared every model private, in which case the lead is not remote
	// and there is nothing for gate 2 to protect. Hiding a toggle the runtime
	// would honor is a control that reads as broken.
	ed.leadModelLocked = false
	if id != "" {
		ed.source = "../api/agents/" + id
		ed.title = "Edit agent"
		// Agents live in the user's per-user DB (udb), NOT the global
		// T.DB — reading from the wrong store leaves OwnedBy empty and
		// the editor falls back to the top-level shape (intake form,
		// publishing, etc. still rendering for what's actually a
		// sub-agent).
		if rec, ok := loadAgent(udb, id); ok {
			ed.editRec = rec
			ed.leadModelLocked = agentForcesPrivate(rec) && !AllLLMsPrivate()
			if rec.OwnedBy != "" {
				ed.subAgent = true
				if parent, pok := loadAgent(udb, rec.OwnedBy); pok {
					ed.parentName = parent.Name
				}
				ed.title = "Edit sub-agent"
			}
		}
	}
}

func (ed *agentEditor) formFields() {
	T := ed.T
	user := ed.user
	id := ed.id
	// On save, redirect back to the chat surface AND pre-select the
	// agent the user was editing — landing on Chat (the dropdown's
	// default) after editing a research agent makes the save feel
	// disconnected. For new agents we don't know the id yet (server
	// assigns on save), so they land on the dropdown default and the
	// user picks manually.
	ed.redirectURL = ".."
	if id != "" {
		ed.redirectURL = "..?agent=" + id
	}

	// (Skill / expert / collection pickers moved out of the editor
	// — curation now lives on the in-chat Knowledge + Skills
	// buttons so the editor stays focused on full agent shape.)

	ed.fields = []ui.FormField{
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
		// No machine picker here. A machine is assigned from its own page
		// (Extensions, Machines: "Assign to agents") or the chat's Configure
		// menu. Listed on every agent, the picker offered each one every
		// other agent's machine, named for the agent it was built for. The
		// save keeps an agent's machine when this form does not send one.
		// Nor the bulletins it follows: those are in the Knowledge dialog,
		// beside its collections, and kept the same way when not sent.
		// Autonomous runs are NOT here. What this agent may do when nobody is
		// present to click Approve is the Security page: the Unattended ladder
		// on each tool, and the Limits tab for how much and how often.
		//
		// They are limits the FRAMEWORK keeps, which is the whole reason they
		// are settings rather than prompt text. Written into a prompt - "post
		// at most six times a day" - they are rules the model has to count for
		// itself, and one did: it counted its own posts out of a listing, read
		// UTC timestamps as local, and posted nine before reporting the cap as
		// reached.
		// Which tools may run unwatched is NOT here. It is the Unattended
		// ladder on each tool's row, under Security -> Tools.
		//
		// These were two checklists over exactly the fields that ladder
		// writes, AutoApproveTools and NoUnattendedTools. Two controls over
		// one fact drift, and these could disagree in a way neither of them
		// showed: a tool could sit on BOTH lists from here, which the ladder
		// cannot produce because setting one policy clears the other.
		//
		// The ladder is the better shape for it too. A tool has three answers,
		// not two independent checkboxes: runs, asks first, never.
	}
	// Budgets and Reasoning close the form; the app-agent editor shows only
	// these (renderAppAgentEditor).
	ed.fields = append(ed.fields, T.budgetReasoningFields(ed.editRec, ed.leadModelLocked)...)
	// Sub-agent create flow (chat-toolbar Create → "sub-agent of X")
	// bakes the parent ID into the form via a hidden field so the POST
	// to /api/agents includes owned_by=<parent_id>. enforceSubAgentPosture
	// then pins the sub-agent posture flags at load. Only relevant on
	// the new-agent path; editing an existing sub-agent already knows
	// its OwnedBy from the loaded record.
	if ed.subAgentParentID != "" {
		ed.fields = append(ed.fields, ui.FormField{
			Field:   "owned_by",
			Type:    "hidden",
			Default: ed.subAgentParentID,
		})
	}

	if ed.subAgent {
		// A sub-agent has none of the memory settings below, only whether it
		// keeps memory at all (enforceSubAgentPosture).
		ed.fields = append(ed.fields,
			ui.FormField{Type: "header", Label: "Memory",
				Help: "Whether it remembers what it learns from one run to the next. Its knowledge (uploaded files) is always available."},
			ui.FormField{Field: "keeps_memory", Type: "toggle", Label: "Keeps its own memory",
				Help: "On, it keeps pinned facts and searchable findings between runs. Off, each run starts from its prompt and knowledge alone.",
				Detail: "Its memory is its own: the parent never reads it, so what this sub-agent builds up (a map of a system, where things are, what it checked last time) stays out of the parent's context. The parent gets only what it reports back.\n\n" +
					"Turn it on for a sub-agent that gets better at its job by remembering, and leave it off for a one-shot specialist, where notes from an earlier run would colour a fresh lookup. A sub-agent that inherits its parent's tools always keeps memory."},
			ui.FormField{Field: "memory_mode", Type: "select", Label: "What its memory is for", ShowWhen: "keeps_memory",
				Options: []ui.SelectOption{
					{Value: memoryModeAgent, Label: "Lessons: what works for its job"},
					{Value: memoryModeCartographer, Label: "Cartographer: lessons + a map of what it works on"},
				},
				Help: "A Cartographer builds a map in its graph memory as it goes, and keeps it current."},
		)
	}
	if !ed.subAgent {
		ed.fields = append(ed.fields,
			ui.FormField{Field: "capture_prompt", Type: "toggle", Label: "Capture prompt text",
				Help:   "Keeps each run's round-1 prompt as text, readable with inspect_run.",
				Detail: "For answering \"what was actually in the prompt\". Switch it off again afterwards: it stores the whole conversation, once per turn."},
			// Explorer mode and its ceiling moved with the other limits, to
			// Security -> Limits. A budget the agent may lift is a ceiling
			// somebody sets on it, not a description of what it is.
			ui.FormField{Type: "header", Label: "Memory", Collapsed: true,
				Help: "What the agent remembers across turns. Knowledge (uploaded files) is always available."},
			// Only shapes Explicit Memory, so it steps aside when that is off.
			ui.FormField{Field: "memory_mode", Type: "select", Label: "Memory mode", ShowWhen: "!disable_explicit",
				Options: []ui.SelectOption{
					{Value: memoryModeAgent, Label: "Agent: generalized lessons only"},
					{Value: memoryModeChatbot, Label: "Chatbot: lessons + user personalization"},
					{Value: memoryModeCartographer, Label: "Cartographer: lessons + a map of what it works on"},
				},
				Help:   "What its memory is for: lessons, the people it talks with, or a map.",
				Detail: "Agent, the default, pins generalized lessons only; specifics go to remember (pin=false), which is searchable rather than always in prompt. Chatbot pins the same, plus user personalization and conversation notes.\n\nCartographer pins lessons like Agent, and builds a map of what it works on in its graph memory: the parts of a codebase or system and how they connect, recorded as it finds them and kept current. For an agent whose job is mapping, and one you attach to other agents as a Source so they can read the map."},
			ui.FormField{Field: "session_recall", Type: "select", Label: "Session recall",
				Options: []ui.SelectOption{
					{Value: "", Label: "Default: on in Chatbot mode, off in Agent and Cartographer modes"},
					{Value: "on", Label: "On"},
					{Value: "off", Label: "Off"},
				},
				Help:   "Lets the agent find earlier sessions with recall and link you to them.",
				Detail: "After each turn the session keeps a marker: its title and dates, what you asked, what it made or changed, and the files it delivered. recall searches the markers, and reading a hit reads the conversation itself, so the agent answers from what was said rather than from a summary of it, and gives you the link. Nothing is added to the cortex. Incognito sessions and Clean turns leave no marker, and deleting a session deletes its marker."},
			ui.FormField{Field: "disable_explicit", Type: "toggle", Label: "Disable Explicit Memory",
				Help:   "Strips the pinned-notes half of remember and forget, and the pre-injected facts block.",
				Detail: "For impersonal or stateless agents."},
			ui.FormField{Field: "disable_inferred", Type: "toggle", Label: "Disable Reference Memory",
				Help:   "Strips the searchable half of remember, recall and forget, and drops derived chunks from recall.",
				Detail: "For agents that should answer from authoritative sources only. The per-turn Clean toggle does the same thing, scoped to one turn."},
			// (Working notes toggle removed: agent-wide notes were retired in
			// favour of task and link-group notes on the Scheduler, and an
			// agent's existing notes were kept as facts. See notes_retire.go.)
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
				Detail: "Whether the agent reads its Cortex, plus the conductor toolset (scheduling, monitors, delegate). Building is not a toolset any more: an agent hands it to Builder.\n\nThese add TOOLS; they do not govern who the agent may call. That is Delegation, on the agent's Security page." + appGrantHelp(user, id)},
			ui.FormField{Field: "channel", Type: "toggle", Label: "Reads its Cortex",
				Help:   "Every agent keeps a Cortex, the record of what reached it. On, the agent also reads it: its recent lines ride into every session, and the thread becomes its own standing home.",
				Detail: "Off, the Cortex is a record for you: the 📋 row pinned at the top of the rail, read-only, listing messages, requests from other agents, scheduled runs, monitor fires and new conversations. The agent never sees it and it costs nothing per turn.\n\nOn, it becomes the agent's mind: the 🧠 row, which the agent resumes and where event-monitor wakes and standing-agent reports land by default, kept bounded by a rolling summary. Its recent lines go into every other session's prompt, so the agent arrives aware of what has been happening. It also surfaces the Permissions queue and the Manage menu in the topbar, and is reached only from Agents.\n\nWhen the agent is published to the dashboard, granted users do not see the Cortex thread. They get ordinary chat sessions, each seeded read-only from the agent's standing awareness, so it shows up already aware; publishing and granting access is the consent to share that. Publishable as long as the delegation and management tools below are off."},
			ui.FormField{Field: "fleet", Type: "toggle", Label: "Conductor tools (scheduling, monitors, delegate)",
				Help:   "Grants the conductor toolset: delegate, scheduling, monitors, run-ledger, history-recall.",
				Detail: "This is DISTINCT from \"the fleet\", the collection of all your agents, which every agent is in. It is also NOT the master switch for agent-to-agent calls: every non-sub agent can call peers via agents(action=\"run\") regardless, governed by the Dispatch policy below. Set that to \"Allow none\" to fully ground this agent.\n\nIt does not stop the agent doing work itself, it just adds the tools. An agent carrying these tools is never published publicly, since they reach owner-only management endpoints."},
		)
		ed.fields = append(ed.fields, authorCapabilityFields(id)...)
		ed.fields = append(ed.fields,
			ui.FormField{Field: "tag_name", Type: "toggle", Label: "Sign outbound messages with this agent's name",
				Help:   "Prefixes every message this agent sends over a channel with its name.",
				Detail: "For example, \"[Assistant] on my way\". Lets the recipient tell the agent's texts apart from your own messages in the same thread. Off by default; turn it on for agents that reply in conversations you also text in."},

			// Where it shows up is not here. Your own dashboard is the gear
			// on its Agents section, which pins any of your agents; the
			// card for the people it is shared with is the Share tab of its
			// Security page, beside who they are. Who may USE it, and MCP,
			// are that Share tab too: reach an administrator grants, not
			// something the form for what the agent IS should set.
			// None of the ENFORCEMENT is here any more. Private mode and the
			// workspace ceiling are the Network tab of the agent's Security
			// window; the switched-off sub-actions and the credential scoping
			// are its Tools tab. This form is for what the agent IS, and what
			// it may reach is a different errand.
			//
			// Workspace reach went first and taught the rule: it was in BOTH
			// places for a while, which is worse than being in the wrong one,
			// because two controls over one fact drift and the one you did not
			// use is the one you go on believing.

			// Delegation is NOT here. Who may call this agent and who it may
			// call are the blast radius, which is a security question asked
			// when you are not editing, and they live on the agent's Security
			// page under Delegation with the target list beside them.
			ui.FormField{Type: "header", Label: "Intake form",
				Help: "Optional structured input form for a new session's first turn."},
			ui.FormField{Field: "intake_form", Type: "textarea", Label: "Intake form (JSON)", Rows: 6,
				Help:        "Optional. A form shown instead of the text input on the first turn of a new session.",
				Detail:      "Submitting packs the values into a markdown user message and uploads any file fields as attachments. PDFs and DOCX get text-extracted server-side, images go to vision. Leave it blank for a normal chat-first agent.\n\nFormat: a JSON array of {name, label, type, placeholder, help, required, options}. type is \"text\" (the default), \"textarea\", \"select\" (single-choice dropdown), \"checklist\" (multi-pick checkboxes, whose selected values get comma-joined into the packed markdown), \"number\", \"file\", or \"button\" (self-submitting). options is an array of strings, used by select, checklist and button.",
				Placeholder: "[\n  {\"name\": \"company\", \"label\": \"Company name\", \"type\": \"text\", \"required\": true},\n  {\"name\": \"audience\", \"label\": \"Target audience\", \"type\": \"textarea\"},\n  {\"name\": \"deadline\", \"label\": \"Deadline\", \"type\": \"select\", \"options\": [\"This week\", \"This month\", \"No rush\"]},\n  {\"name\": \"topics\", \"label\": \"Topics of interest\", \"type\": \"checklist\", \"options\": [\"AI\", \"Healthcare\", \"Finance\", \"Education\"]}\n]",
				SuggestURL:  "../api/agents/suggest"},
		)
	}
	// (Sub-agent surface has no extra toggles — memory / publishing / intake are
	// pinned off structurally, and disable_skills was dropped as redundant.)
}

func (ed *agentEditor) templates() {
	// "Agent type" presets — create mode only (a template stamps fields, which
	// would clobber a real agent's values when editing; flags stay editable in
	// Advanced after). Picking a type sets the character-defining defaults
	// (Cortex + memory mode, Fleet off) so you choose what KIND of agent it is.
	id := ed.id
	if id == "" && !ed.subAgent {
		ed.agentTemplates = agentTypeTemplates()
	}
}

func (ed *agentEditor) mainSection() {
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
	ed.agentSection = ui.Section{
		Title:    "Agent",
		Subtitle: "Identity, prompts, and behavior.",
		Detail:   "Clone an existing agent from the landing page if you want a quick copy to tweak.",
		Body: ui.FormPanel{
			Assist:         true,
			Source:         ed.source,
			PostURL:        "../api/agents",
			Method:         "POST",
			SubmitLabel:    "Save agent",
			RedirectURL:    ed.redirectURL,
			RedirectTarget: "_self",
			Templates:      ed.agentTemplates,
			TemplatesLabel: "Agent type",
			Fields:         ed.fields,
		},
	}
	if ed.subAgent {
		ed.agentSection.Title = "Sub-agent"
		if ed.parentName != "" {
			ed.agentSection.Subtitle = "Owned by parent agent: " + ed.parentName + ". Sub-agents are focused capability components called by their parent via dispatch: public surfaces, intake form, and explorer mode are structurally off and hidden from this editor, and memory is off unless it keeps its own."
		} else {
			ed.agentSection.Subtitle = "Sub-agent owned by another agent. Public surfaces, intake form, and explorer mode are structurally off and hidden from this editor, and memory is off unless it keeps its own."
		}
	}
}

func (ed *agentEditor) splitSections() {
	id := ed.id
	ed.sections = []ui.Section{ed.agentSection}
	if id != "" {
		if split := splitAgentFormSections(id, ed.source, ed.fields, ed.agentSection.Subtitle); len(split) > 0 {
			ed.sections = split
		}
	}
}

func (ed *agentEditor) picturesSection() {
	id := ed.id
	// (Channels section removed from the agent editor — channels are managed in
	// the chat rail's Channels area and in the Bridges app, scoped to the agent
	// you're viewing, so the editor no longer carries a duplicate attach form.)

	// The dispatch TARGET LIST is not here either. It is only read by the
	// two "selected" dispatch modes, so it belongs beside the policy that
	// reads it, which is now on the Security page.

	// The security console is NOT linked from here.
	//
	// It used to be two sections at the bottom of this form, then one button
	// at the bottom of this form, which is the same mistake with fewer rows:
	// reviewing what you granted is its own errand and you are usually not
	// editing when you do it. Leaving the door inside the editor meant the
	// page had moved out and its entrance had not.
	//
	// It is reached from the Permissions control in the agent's own topbar,
	// where the standing decisions already are, and from nowhere else. One
	// home for the question rather than two that drift.

	// External credentials moved on again, to the Tools tab of the agent's
	// Security window. Which APIs an agent may reach is what it can DO, not
	// what it is - the same errand as the sub-actions and the tool bands, and
	// they are now read in one place instead of three.

	// The picture library — the FIRST surface that shows the owner what is in it
	// rather than describing it in the agent's own words. That gap is not
	// theoretical: asked for three people's reference photos, an agent handed
	// back three of its own renders and called them real, and there was nowhere
	// to look and catch it. The thumbnail is the point; the rest is context for
	// deciding whether to keep the row.
	if id != "" {
		ed.sections = append(ed.sections, ui.Section{
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
}

func (ed *agentEditor) shareSection() {
	id := ed.id
	// Share with users — peer-sharing (namespacing phase 5). Existing, non-seed,
	// top-level agents only: a seed is framework-owned and a sub-agent is a
	// component of its parent, neither is independently shareable. The recipient
	// runs the OWNER's agent, but its credentials + tools resolve in the
	// RECIPIENT's namespace, so no secret travels with the share.
	if id != "" && !ed.subAgent && !isSeedID(id) {
		// Who may RUN this agent, and what travels with it, are not here.
		// They are the Share tab of its Security page.
		//
		// Both are enforcement: a person either reaches the agent or does
		// not, and a recipient either reads a memory layer or does not,
		// whatever the agent would say. This form is for what the agent IS.
		ed.sections = append(ed.sections, ui.Section{
			Title:    "What it reaches",
			Indent:   1,
			Wide:     true,
			Subtitle: "Everything it depends on, and how far each of those goes today.",
			Detail: "Everything here travels with the agent and is scoped to it: whoever runs it reads your documents, runs your tools and activates your skills THROUGH this agent, and nowhere else. They cannot attach any of it to an agent of their own.\n\n" +
				"One exception, and it is the only thing a share has to ask about. A credential is not a copy somebody is missing — it is whose identity the call goes out as — so it resolves by name in the namespace of whoever is running, and you decide per key whether to lend yours or let them bring their own. That choice is made when you share, in Sharing.\n\n" +
				"Nothing here changes anything. It is the list to check before you share, and the answer to \"why does it work for me and not for them\" afterwards.",
			Body: ui.Table{
				Source: ed.source + "/reach",
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
						PostTo: ed.source + "/reach/credential",
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
}

func (ed *agentEditor) shapeSection() {
	udb := ed.udb
	id := ed.id
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
			ed.sections = append(ed.sections, ui.Section{
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
}

func (ed *agentEditor) usedBySection() {
	user := ed.user
	udb := ed.udb
	id := ed.id
	if id != "" {
		if rec, ok := loadAgent(udb, id); ok {
			if sec, ok := agentMoveSection(udb, user, rec); ok {
				ed.sections = append(ed.sections, sec)
			}
			// Used by: what runs through this agent, so it is plain before
			// anyone changes or deletes it (agent_used_by.go). Only when
			// something does.
			if uses := agentUsedBy(udb, user, rec); len(uses) > 0 {
				var pairs []ui.DisplayPair
				for _, k := range []struct{ field, label string }{{"apps", "Apps"}, {"pipelines", "Pipelines"}, {"machines", "Machines"}} {
					if usedByKind(uses, strings.TrimSuffix(k.field, "s")) != "" {
						pairs = append(pairs, ui.DisplayPair{Label: k.label, Field: k.field})
					}
				}
				ed.sections = append(ed.sections, ui.Section{
					Title:    "Used by",
					Subtitle: "These run through this agent. Changing it changes them; deleting it stops them working until they are pointed at another agent.",
					Body: ui.DisplayPanel{
						Source: "../api/agents/" + id + "/used-by",
						Pairs:  pairs,
					},
				})
			}
		}
	}
}

func (ed *agentEditor) deleteSection() {
	id := ed.id
	// Delete — the human's authoritative remove for any existing agent the editor
	// is open on, INCLUDING a sub-agent reached via the picker (which other
	// agents can't delete). Non-seed only: seeds
	// revert via their own path, they aren't "deleted". The DELETE handler
	// cascades sub-agents and cleans channels / monitors / standing agents /
	// dispatch-allowlist references.
	if id != "" && !isSeedID(id) {
		deleteRedirect := ".."
		if ed.editRec.OwnedBy != "" {
			deleteRedirect = "..?agent=" + url.QueryEscape(ed.editRec.OwnedBy)
		}
		ed.sections = append(ed.sections, ui.Section{
			Title:    "Delete agent",
			Subtitle: "Permanently remove this agent. This cannot be undone.",
			Detail:   "Its sessions, memory, knowledge and any sub-agents it owns go with it, and its channels are removed. The schedules it runs can be kept parked for 30 days, to be relinked to a replacement agent, or deleted with it: the delete asks.",
			Body: ui.DisplayPanel{
				Source: "../api/agents/" + id,
				Pairs:  []ui.DisplayPair{},
				Actions: []ui.ToolbarAction{
					{
						Label: "Delete this agent",
						// A client action: it asks what happens to the
						// schedules the agent runs before it deletes
						// (parked_schedules.go). Its page has nothing left to
						// show afterwards: a sub-agent goes back to its
						// parent, anything else to the agent list.
						Method:  "client",
						URL:     "orchestrate_delete_agent_ask",
						Data:    agentDeleteData(id, ed.editRec.Name, deleteRedirect),
						Variant: "danger",
					},
				},
			},
		})
	}
}

func (ed *agentEditor) serve() {
	w := ed.w
	r := ed.r
	id := ed.id
	// Carry the edited agent's ID back to Agents so the picker
	// reopens on the agent the user was just editing instead of
	// snapping to Chat. Empty id (create form) skips the param.
	backURL := ".."
	if id != "" {
		backURL = "..?agent=" + url.QueryEscape(id)
	}
	// App-specific behavior rides in via ExtraHeadHTML per the core/ui
	// domain-agnostic rule: the picture library's relabel prompt, Assist, and
	// the delete question.
	headHTML := ""
	if id != "" {
		headHTML = imageLibraryHeadHTML(id)
		headHTML += agentAssistHTML(id)
		headHTML += agentDeleteAskJS
	}
	page := ui.Page{
		Title:     ed.title,
		ShowTitle: true,
		BackURL:   backURL,
		// Left-rail section nav, one section at a time — same shape as admin and
		// Extensions. The editor has grown several sections (Agent, dispatch list,
		// credentials, sharing, delete); stacking them made the page a long scroll.
		MaxWidth:      "1100px",
		SectionNav:    true,
		Sections:      ed.sections,
		ExtraHeadHTML: headHTML,
	}
	page.ServeHTTP(w, r)
}

// agentAssistHTML mounts "✨ Get assistance" in the first section's header, and the dialog
// behind it: a conversation about the WHOLE agent that answers with changes
// you accept.
//
// App-specific behavior, so it rides in through ExtraHeadHTML rather than into
// core/ui. It reuses uiOpenModal (THE modal), the shared conversation pane
// (uiAssistChat, with its spinner, Stop and web lookups, as every other ✨
// has) and the existing PATCH endpoint to apply, which is what keeps it
// small: what is its own here is the list of proposed changes, no new
// persistence path, and a reload afterwards so the editor is showing what
// was actually stored rather than what we hoped.
//
// Why a whole-agent dialog next to a per-field one: the field workbench sees
// the field it was opened on, so it cannot notice that an agent has no rules,
// or that its allowlist and its persona disagree about whether it browses. The
// answer to "stop it making things up" is a rule plus a line in the persona,
// not one line in whichever box happened to be focused.
func agentAssistHTML(id string) string {
	return fmt.Sprintf(`<style>
@media (pointer: coarse){#agent-assist{min-height:var(--btn-h-sm, 2.25rem)}}
.aa-chat{display:flex;flex-direction:column;height:min(46vh, 26rem)}
.aa-chat .ui-assist-right{flex:1 1 auto;min-height:0}
.aa-changes{max-height:32vh;overflow-y:auto;margin-top:.6rem}
.aa-changes:empty{display:none}
.aa-change{border:1px solid var(--border);border-radius:6px;padding:.5rem .6rem;margin:.4rem 0;background:var(--bg-2)}
.aa-change label{display:flex;gap:.5rem;align-items:baseline;cursor:pointer;font-weight:600}
.aa-why{font-size:var(--fs-sm, .85rem);opacity:.8;margin:.2rem 0 .35rem 1.4rem}
.aa-val{margin-left:1.4rem;font-size:var(--fs-sm, .85rem);white-space:pre-wrap;max-height:9rem;overflow:auto;padding:.4rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px}
.aa-foot{display:flex;gap:.5rem;justify-content:flex-end;margin-top:.75rem}
@media (max-width: 760px){.aa-chat{height:52vh}}
</style>
<script>
(function(){
  var id=%q, pending=[];
  function el(t,a,kids){var n=document.createElement(t);a=a||{};for(var k in a){if(k==='class')n.className=a[k];else if(k==='text')n.textContent=a[k];else n.setAttribute(k,a[k]);}
    (kids||[]).forEach(function(c){n.appendChild(c);});return n;}

  function open(){
    window.uiOpenModal({
      title:'✨ Get assistance with this agent',
      subtitle:'Ask for a change to any part of it and review what it proposes. Nothing is saved until you apply.',
      width:'760px',
      actions:[],
      mount:function(body,api){
        var changes=el('div',{class:'aa-changes'});
        var apply=el('button',{type:'button',class:'ui-row-btn primary',text:'Apply selected'});
        apply.style.display='none';
        var close=el('button',{type:'button',class:'ui-row-btn',text:'Close'});
        close.onclick=function(){api.close();};

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
        var chat=window.uiAssistChat({
          web:true,
          intro:'Tell me what is not working about this agent, or what you want it to do differently. I will propose changes you can review before anything is saved.',
          placeholders:{write:'What should be different? For example: it keeps answering from memory, make it stick to sources.'},
          starters:['Look it over: what would you change?','What is this agent set up to do?'],
          send:window.uiAssistPost('../api/agents/'+id+'/assist'),
          // A reply that proposes changes replaces the ones shown; one that
          // only answers (a question about them, say) leaves them standing.
          onResult:function(res){
            if(!res||!Array.isArray(res.changes)||!res.changes.length) return '';
            renderChanges(res.changes);
            return 'Here is what I would change. Tick the ones to keep and apply them.';
          }
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
          if(!n){ window.uiToast('Nothing selected.'); return; }
          apply.disabled=true; apply.textContent='Applying…';
          fetch('../api/agents/'+id,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify(patch)})
            .then(function(r){ if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status));}); location.reload(); })
            .catch(function(e){ apply.disabled=false; apply.textContent='Apply selected'; window.uiAlert('Could not apply: '+((e&&e.message)||e)); });
        };

        body.appendChild(el('div',{class:'aa-chat'},[chat.right]));
        body.appendChild(changes);
        body.appendChild(el('div',{class:'aa-foot'},[apply,close]));
        chat.focus();
      }
    });
  }

  var b=document.createElement('button');
  // The form's own ✨ Get assistance pill (ui-form-assist), so it reads as
  // the same thing: a bare ✨ in a header said nothing about what it does.
  b.id='agent-assist'; b.type='button'; b.className='ui-form-assist'; b.textContent='✨ Get assistance';
  b.title='Talk about the whole agent and review the changes it proposes';
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
	const where = " The policy itself is the **Dispatch policy** select just above, under **Which agents it can call at all**."
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
		return "Currently **Allow none**: this agent dispatches to no other agent or pipeline, so this list has no effect until you change the policy. Its own sub-agents stay reachable: an agent can always call its own." + where
	default:
		return "Currently **Allow all**: this agent may call any non-hidden agent and any of your pipelines, so this list has no effect. It applies only in \"Only allow selected\" or \"Allow all except selected\" mode." + where
	}
}

func dispatchModeOptions(first string) []ui.SelectOption {
	all := []ui.SelectOption{
		{Value: dispatchAll, Label: "Allow all: any non-hidden agent (default)"},
		{Value: dispatchOnly, Label: "Only allow selected (target list below)"},
		{Value: dispatchExcept, Label: "Allow all except selected (target list below)"},
		{Value: dispatchNone, Label: "Allow none: only its own sub-agents"},
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

// authorCapabilityFields renders the "Authoring tools" line: a read-only note on
// Builder, and nothing on any other agent, since the Author flag is retired and
// building goes through Builder ("Can dispatch Builder", Security & Access).
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
				detail = append(detail, g.Label+" - "+g.Detail)
			}
		}
		if len(detail) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(detail, "; "))
		}
		if s.ManageURL != "" {
			fmt.Fprintf(&b, " - manage at %s", s.ManageURL)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func authorCapabilityFields(agentID string) []ui.FormField {
	if isBuilderAgent(agentID) {
		return []ui.FormField{{
			Type:  "header",
			Label: "Authoring tools: always on for Builder",
			Help:  "Builder holds the authoring catalog as its IDENTITY, not as a grant, so there is nothing to switch here.",
			Detail: "The catalog is survey, create/update/clone agents, tool_def, app_def, skill_def, credential drafting, and bridge/connector. No other agent holds it: an agent that needs something built hands it to Builder, which takes \"Can dispatch Builder\" under Security & Access (or the conductor tools)." +
				"\n\nAuthoring is owner-only at runtime: if a turn runs as someone other than this agent's owner, the catalog is withheld and the reason is recorded in the session diagnostics.",
		}}
	}
	return nil
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
		// No ✨ Get assistance on these: the page's own ✨ (agentAssistHTML)
		// is the agent's assist, and it sees the whole agent where a form's
		// helper would see one section of it. Two sparkles for one agent.
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

// leadUseField is the one choice of how the agent uses the lead model:
// every turn on it, ask it when stuck, or never. It replaces a "Use Lead
// model" toggle and a "Consult the Lead" select, which were one decision told
// as two (see applyLeadUse). Strongest first. The option the agent gets when
// it decides nothing says "(default)".
//
// Hidden, with no value, where the deployment has no distinct lead wired or
// the agent's conversation must not leave for it: there is nothing to choose.
func leadUseField(show bool, rec AgentRecord) ui.FormField {
	if !show {
		return ui.FormField{Field: "lead_use", Type: "hidden"}
	}
	def := leadUseDefault(RootDB, rec)
	// The choices are steps along one line, from the worker alone to the
	// lead for everything, so they are drawn as a scale in that order: how
	// much of each turn the lead sees, and so what a turn costs.
	stop := func(v, label, help string) ui.SelectOption {
		if v == def {
			help += " This is the default."
		}
		return ui.SelectOption{Value: v, Label: label, Help: help}
	}
	opts := []ui.SelectOption{stop(leadUseOff, "Worker only", "Every turn runs on the local worker; the lead is never used.")}
	// Consult is offered only where the deployment allows it.
	for _, c := range deploymentDefaultChoices(RootDB, defaultConsultLead) {
		if c == settingOn {
			opts = append(opts, stop(leadUseConsult, "Ask when stuck",
				"The worker runs the turn and may ask the lead one self-contained question when it hits a wall, at most three a turn. Only the question and its evidence go."))
		}
	}
	opts = append(opts, stop(leadUseReview, "Lead reviews",
		"The worker runs the turn; before the reply goes out, the lead reads it with what the turn ran and got back, and lets it go or sends it back with changes. Only turns that did work are reviewed."))
	// The same choice on Builder means something of its own: the lead designs
	// and reviews apps, and a worker writes them (builder_app_split.go).
	if isBuilderAgent(rec.ID) {
		opts = append(opts, stop(leadUsePlan, "Lead designs apps",
			"For an app, the lead designs it with you, a worker on the local model writes it, and the lead reads the code and sends changes until it is right. Everything else Builder makes is built on the lead."))
	} else {
		opts = append(opts, stop(leadUsePlan, "Lead plans",
			"The lead decides and writes the reply; steps on the worker do the work with the tools it picks for each. No tool list and no raw tool output reach the lead."))
	}
	opts = append(opts, stop(leadUseLead, "Lead every turn", "The lead runs the whole turn with every tool."))
	return ui.FormField{
		Field: "lead_use", Type: "scale", Label: "Lead model", Options: opts,
		Help: "How much of each turn the lead model does: left is the local worker alone, right is the lead for everything.",
		Detail: "Left to right, each step gives the lead more of the turn, and costs more. Worker only never uses it. Ask when stuck lets the worker ask it a question. Lead reviews has it read the finished reply against what the turn did, once, and send it back when something is wrong or missing; it never rewrites the reply itself. Lead plans has it decide and write while workers do the work (on Builder, design and review apps while a worker writes them). Lead every turn runs the whole turn on it.\n\n" +
			"A Private conversation never reaches the lead, whatever is chosen here.",
	}
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

// shareSubtitleFor says who the share list reaches now, and where Everyone
// stands with the administrator.
func shareSubtitleFor(a AgentRecord, pending bool) string {
	switch {
	case a.Everyone:
		return "Shared with everyone: every user the administrator has granted this app can run it, and the people and teams in the list besides."
	case pending:
		return "Everyone is waiting for an administrator to approve it. Until then, the people and teams in the list can run it."
	}
	return "Everyone, people, or a team you are on. Empty means private to you."
}

// budgetReasoningFields are the agent editor's Budgets and Reasoning sections:
// how much a turn may spend, and how it reasons. Shared by the full editor
// and the app-agent editor, so the two cannot drift apart.
func (T *OrchestrateApp) budgetReasoningFields(editRec AgentRecord, leadModelLocked bool) []ui.FormField {
	return []ui.FormField{
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
		{Field: "run_priority", Type: "select", Label: "Model priority",
			Options: []ui.SelectOption{
				{Value: "", Label: "By how it runs: a person waiting goes first, a schedule waits for people"},
				{Value: "lowest", Label: "Lowest: its scheduled runs wait for anything else to finish"},
				{Value: "urgent", Label: "Urgent: its runs never wait"},
			},
			Help: "Where this agent's own runs stand on the model. A person chatting with it is never made to wait."},
		// Hidden under a tracked plan: the gap pass reviews plan_set's steps,
		// and a tracked plan replaces plan_set, so it would never fire.
		{Field: "gap_check", Type: "toggle", Label: "Gap detection", ShowWhen: "!work_plan",
			Help: "Post-plan review pass that fills structural gaps before synthesis. Worth it for research; off for chat."},
		{Field: "work_plan", Type: "toggle", Label: "Tracked plan",
			Help:   "The agent commits to a visible checklist and works it.",
			Detail: "Each step is started, then closed with findings or marked blocked with a reason, and anything left unfinished is stated in the answer instead of quietly dropped. The checklist survives the turn, so a plan begun in one message is still the plan in the next.\n\nReplaces this agent's plan_set, which fans a single turn out to workers and ends the round. Worth it for work with several results that build on each other, overhead for questions one call answers."},
		{Type: "header", Label: "Reasoning", Collapsed: true,
			Help: "Override the LLM's reasoning mode for this agent's turns."},
		// One choice over Think and Effort (applyReasoning): a set effort
		// already decides whether the agent reasons, so two controls had
		// Effort Off and Think Off doing the same thing.
		{Field: "reasoning", Type: "select", Label: "Mode",
			Options: []ui.SelectOption{
				{Value: "", Label: "Default: follow the deployment routing (" + currentAutoThinkLabel() + ")"},
				{Value: "high", Label: "High"},
				{Value: "medium", Label: "Medium"},
				{Value: "low", Label: "Low"},
				{Value: "off", Label: "Off: no reasoning (faster)"},
			},
			Help:   "Whether this agent reasons before it answers, and how hard.",
			Detail: "Each model gets its own dial for the level: Claude's effort setting, OpenAI's reasoning effort, or a thinking budget sized for local models. The admin's maximum effort for the model tier still caps it.\n\nReasoning helps planners and synthesizers; Off suits fast lookups, transformers and routers. Default reasons unless the deployment routing is set otherwise; a new sub-agent starts Off."},
		// Shown while reasoning can be on.
		{Field: "think_budget", Type: "number", Label: "Think budget (tokens)", Min: 0, Max: 32768,
			ShowWhen:    "reasoning:!off",
			Placeholder: "0",
			Help:        "Max thinking tokens per LLM call. 0 inherits the deployment default (4096). Overrides the level.",
			Detail:      "The admin global budget is a hard ceiling, so this can only LOWER the budget, for snappier turns. A value above the ceiling is clamped. Only applies when the agent reasons."},
		// Which MODEL does the reasoning — a Reasoning setting, not an
		// Autonomous-runs one. It sat under Autonomous runs purely by
		// position (a header owns the fields until the next header), so it
		// read as an unattended-run option when it governs every turn.
		//
		// Shown only when a distinct lead is actually wired (HasDistinctLead):
		// otherwise it degrades straight back to the worker and the control
		// would be a no-op. Hidden for ForcePrivate agents — their
		// conversation must never leave for the remote lead model (gate 2).
		leadUseField(T.HasDistinctLead() && !leadModelLocked, editRec),
	}
}

// renderAppAgentEditor answers an app agent's editor URL. An app agent
// (Scribe's Guide Author, Servitor's investigator) is set up in its app, beside
// its chat (app_agent_settings.go), and orchestrate does not list it: the full
// editor here would give it schedules, channels, delegation and the rest, each
// a way to run it somewhere it cannot work. So the page says where to go.
func (T *OrchestrateApp) renderAppAgentEditor(w http.ResponseWriter, r *http.Request, udb Database, id string, spec appagents.AppAgentSpec) {
	app := chFirst(spec.OwningApp, "its app")
	name := chFirst(spec.Name, id)
	page := ui.Page{
		Title:     name,
		ShowTitle: true,
		BackURL:   "..",
		MaxWidth:  "720px",
		Sections: []ui.Section{{
			Title:    "Set up in " + app,
			Subtitle: name + " belongs to " + app + " and runs only there. Its settings are in " + app + ": Agent settings, beside its chat.",
		}},
	}
	page.ServeHTTP(w, r)
}
