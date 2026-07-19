package orchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// AgentRecord is the single first-class concept — the persona AND the
// named workspace rolled into one. Previously this was Template (the
// recipe) + Instance (a named bake of a template). Collapsed because
// instances were always 1:1 with the conversation the user wanted to
// have; per-session isolation already exists at the chat layer, so
// the extra indirection wasn't earning its keep.
//
// To get the "shareable recipe" pattern back, Clone copies an agent's
// persona into a new agent with fresh ID + empty session history.
//
// Owner scopes agents to a user. Built-in starter agents use a
// sentinel owner ("system") and are read-only — users clone them to
// customize.
//
// Named AgentRecord (not Agent) because core.Agent is the dot-imported
// interface used for CLI-tier agents and the names would collide.
type AgentRecord struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Mode selects how the agent is presented. Empty / "chat" (default) =
	// session-based chat — the standard Agency surface with a session list.
	// "orchestrator" = a single ongoing-thread console (the Operator): one
	// continuous conversation plus the controller nav (Chat / Enabled agents
	// / Authorizations). A general per-agent property; any controller-style
	// agent can opt in. Differentiates an ongoing-conversation orchestrator
	// from the session-based chat agents.
	Mode string `json:"mode,omitempty"`

	// Cortex, when true, gives the agent a persistent "home thread" — its
	// mind: a single ongoing conversation it resumes alongside (not instead
	// of) its normal sessions, where event-monitor wakes and standing-agent
	// reports land, with ongoing-thread compaction (running summary + recent
	// tail) applied to keep it bounded. Cortex agents also get the management
	// sidebar (the pinned hero row + Permissions / Enabled agents / Event
	// monitors box). A Cortex agent CAN be published (each public visitor gets
	// their own per-(user, agent) Cortex thread) — the only publish gate is
	// Fleet (publiclyExposable = Exposed && !Fleet); Cortex is not checked.
	// Independent of Fleet — an agent can have a Cortex without delegation
	// tools, or vice versa.
	// This is the generalized form of what used to be Mode == "orchestrator".
	//
	// The JSON/storage key stays "channel" (legacy) so existing agent records
	// keep working without a migration; the concept and all code is Cortex.
	// The word "Channel" is now reserved for messaging surfaces attached to
	// an agent (see docs/channels-and-agents.md).
	Cortex bool `json:"channel,omitempty"`

	// Fleet, when true, attaches the fleet-management toolset to the agent:
	// delegate, create/list/run/pause/delete standing agents, the run
	// ledger, event-monitor management, and history recall. Independent of
	// Channel. Unlike the old orchestrator Mode, Fleet does NOT impose a
	// "controller, not a doer" restriction (that was only prompt wording) —
	// it just grants the tools; the agent still does work directly.
	Fleet bool `json:"fleet,omitempty"`

	// TagName, when true, prefixes this agent's OUTBOUND messages on a
	// messaging channel/bridge with its own name — "[Assistant] …" — so the
	// recipient can tell the agent's texts apart from the owner's own messages
	// in the same thread. Per-agent because the tag IS the agent's identity: one
	// agent may sign its messages while another shouldn't. Off by default (it
	// changes what recipients see). Applied at the transport's outbound
	// chokepoint from the name the runner stamps on the reply.
	TagName bool `json:"tag_name,omitempty"`

	// Triggers are substring/glob patterns (same shape as a skill's
	// Triggers) matched against the user message each turn. When one
	// matches, the framework injects a per-turn HINT next to the message —
	// "this turn is <agent>'s domain, dispatch FIRST" — far more salient
	// than the static Available-agents block (which the model reads but
	// often doesn't act on for domains it has priors in). A SOFT nudge: the
	// model still decides. Empty = no per-turn hint (the agent is still in
	// the catalog, just no trigger nudge). Author specific patterns
	// ("penal code", "PC ", "felony") — loose ones over-fire and train the
	// model to ignore the hint.
	Triggers []string `json:"triggers,omitempty"`

	// OrchestratorPrompt drives the thinking LLM that talks to the user,
	// decomposes work into plan steps, AND briefs the worker per step
	// (via the worker_brief field on each plan_set step). The agent has
	// no separate worker-side persona anymore — the orchestrator owns
	// worker behavior on a per-step basis, with rules + memory layered
	// in by the framework.
	OrchestratorPrompt string `json:"orchestrator_prompt"`

	// PlanGuidance is appended to the orchestrator's system prompt and
	// nudges decomposition style — "prefer 2-4 steps", "always start by
	// restating the goal", etc. Optional.
	PlanGuidance string `json:"plan_guidance,omitempty"`

	// Rules is a newline-separated list of explicit operating-policy
	// constraints the user has authored ("always cite a URL", "never
	// quote prices from training, fetch live"). Prepended at the very
	// top of BOTH orchestrator and worker system prompts — above
	// memory and above the persona — so they're the strongest signal
	// in the prompt order. Distinct from PlanGuidance (decomposition
	// style, orchestrator-only) and from auto-consolidated memory
	// (descriptive observations, not prescriptive policy).
	Rules string `json:"rules,omitempty"`

	// AllowedTools is the explicit allowlist of worker tool names this
	// agent may call. Empty/nil = the agent gets the default pool (every
	// non-blocked tool with Read or Network capability). Non-empty =
	// strict allowlist; only the named tools are exposed to the worker.
	//
	// The available pool is computed from the chat tool registry minus
	// BlockedTools, filtered by cap. See availableWorkerToolOptions().
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// DisabledPersistentTools is an explicit DENY-LIST of admin-
	// approved persistent temp-tool names this agent should NOT see.
	// Persistent temp tools flow into every agent's catalog by default
	// — the approval gate is already deployment-wide trust, so per-
	// agent allow-listing on top of that is double-gating. Agents
	// that want a tighter focus name specific persistent tools here
	// to suppress them. Distinct from AllowedTools (which gates the
	// REGISTERED chat-tool pool, not persistent temp tools).
	//
	// Empty / nil = include every approved persistent tool. Names
	// not currently in the persistent pool are ignored silently — a
	// stale entry in the deny list is harmless.
	DisabledPersistentTools []string `json:"disabled_persistent_tools,omitempty"`

	// MaxPlanSteps caps the number of steps the orchestrator may
	// commit to in one turn. Zero = use defaultMaxPlanSteps. The
	// orchestrator sees this cap embedded in its system prompt + the
	// plan_set tool description; the runner also clips the returned
	// list defensively in case the LLM ignores the budget.
	//
	// MaxWorkerRounds caps the agent-loop iterations for each step.
	// Zero = use defaultMaxWorkerRounds. Each round is one LLM call
	// plus any tool execution; raise this for "deep research" style
	// agents that need to chain many tool calls per step, lower it
	// for snappy lookup agents.
	MaxPlanSteps    int `json:"max_plan_steps,omitempty"`
	MaxWorkerRounds int `json:"max_worker_rounds,omitempty"`

	// ThinkBudget overrides the thinking_budget_tokens for this agent's LLM
	// calls (orchestrator + worker rounds). Zero = inherit the resolution
	// chain (per-route budget, else the deployment default of 4096). The
	// admin-configured global budget is a hard CEILING: setting this ABOVE it
	// has no effect (the call is clamped down), so this can only LOWER an
	// agent's budget for snappier turns. Only meaningful when thinking is on.
	// Applied by passing it into AgentLoopConfig.ThinkBudget at each run path.
	ThinkBudget int `json:"think_budget,omitempty"`

	// LeadModel, when true, escalates THIS agent's main reasoning (the
	// orchestrator plan + synthesis turns) to the lead/precision LLM instead
	// of the worker-locked default. Opt-in per agent; the dispatched plan_set
	// step phases still run on the worker. Three gates apply:
	//   1. Degrades to worker automatically when no distinct lead model is
	//      configured (LeadChat falls back to the worker LLM).
	//   2. Never escalates when the agent is ForcePrivate or the turn is
	//      Private-toggled — the conversation stays local (gate in
	//      chatTurn.shouldUseLeadModel + LeadChat's NoLead guard).
	//   3. Routes through the non-private app.orchestrate.orchestrator.lead
	//      stage, so admin keeps a global worker-or-lead ceiling.
	// Builder ignores this flag — it has its own always-lead stage.
	LeadModel bool `json:"lead_model,omitempty"`

	// Exposed publishes this agent under the public /agents/ surface
	// (apps/agents) so non-admin users can chat with it. The exposed
	// surface is a stripped chat pane — only the chat + per-user
	// Memory toolbar button; no agent CRUD, no Tools/Rules/etc.
	// (those are admin concerns and stay in orchestrate). Each
	// end-user gets their own session history + memory under the
	// exposed agent (per-(user, agent) scoping is preserved).
	//
	// Slug for the URL is derived from Name via snakeFromDisplay.
	// Conflicts between same-name agents owned by different users
	// are handled by suffixing the owner-hash; that bit lives in
	// the agents package.
	Exposed bool `json:"exposed,omitempty"`

	// MCPExposed makes this agent reachable through gohort's INBOUND MCP server
	// (apps/mcpserver, /mcp/) — an external MCP client (e.g. Claude Desktop) can
	// dispatch to it via the ask_agent tool. Default off so a bridge key can't
	// reach every agent; the owner opts each one in. Independent of Exposed (the
	// public /agents/ web surface) — an agent can be on one, both, or neither.
	MCPExposed bool `json:"mcp_exposed,omitempty"`

	// PublicName overrides the display name + URL slug on the public
	// /agents/ surface. Left blank, the public view uses Name; set
	// this when the internal agent name doesn't read well as an
	// end-user-facing app title (e.g. internal "Resume v3 Reviewer"
	// → public "Resume Reviewer"). Slug derives from PublicName when
	// set, else Name — note that renaming changes the slug and breaks
	// bookmarks, same caveat as Name.
	PublicName string `json:"public_name,omitempty"`

	// AllowExplorer enables the enter_explorer_mode tool for this
	// agent. When the LLM calls it during a worker step, the round
	// budget for that step is lifted from the agent's
	// MaxWorkerRounds (the soft cap) to an absolute exploration
	// hard cap (50). Use for agents whose job involves mapping
	// unfamiliar APIs / surfaces where 5-8 rounds may not be
	// enough. Off by default — runaway round usage is the bigger
	// risk for most agents.
	AllowExplorer bool `json:"allow_explorer,omitempty"`

	// ExplorerHardCap overrides the default explorer ceiling (50) for
	// this agent. Only meaningful with AllowExplorer. Builder runs
	// higher (80) because authoring against an unfamiliar API is
	// exploration-heavy. 0 = inherit the default. See
	// resolveExplorerHardCap.
	ExplorerHardCap int `json:"explorer_hard_cap,omitempty"`

	// (DisableKnowledge removed — Knowledge is always available; the
	// layer is read-only and harmless when the corpus is empty.
	// knowledge_search and curated-chunk auto-injection always run.
	// If you want an agent to ignore the corpus, just don't upload
	// anything to it.)

	// DisableExplicit turns OFF the agent's Explicit Memory layer:
	// the always-in-prompt structured entries (store_fact /
	// list_facts / forget_fact + the "Saved facts" prompt block).
	// When set:
	//   - store_fact / list_facts / forget_fact stripped from catalog
	//   - facts pre-injection block omitted from system prompt
	//
	// Use for agents that should NOT accumulate any always-in-prompt
	// state across turns — KB readers, one-shot transformers,
	// stateless tools. Builder leaves this ON and uses KnowledgeFraming
	// to reframe the facts as "lessons learned" (the same machinery,
	// shaped by the directive).
	//
	// Composes orthogonally with DisableInferred — an agent can have
	// Explicit on and Inferred off (Builder-style: keep lessons,
	// no vector compounding), or both off (KB-style).
	DisableExplicit bool `json:"disable_explicit,omitempty"`

	// DisableInferred turns OFF the agent's Reference Memory layer:
	// the vector-grown derived store (memory_save findings, synthesis
	// auto-ingest, closer findings). When set:
	//   - memory_save / memory_search / memory_forget stripped from catalog
	//   - synthesis auto-ingest at end of turn → skipped
	//   - skills classifier suppressed for the turn (skills emit
	//     derived chunks via self-training)
	//
	// Use for agents that should answer from authoritative sources
	// only and never grow their own fuzzy recall (KB readers,
	// compliance bots). The per-turn Clean toggle on the chat
	// surface is the same switch scoped to a single turn —
	// "give me a fresh-slate answer, ignore my own prior derivations."
	DisableInferred bool `json:"disable_inferred,omitempty"`

	// ContextDepth caps how many recent messages a persistent thread keeps
	// verbatim in the run-view — the Cortex home thread AND each Channel
	// room. 0 = framework default (12). Older messages fold into a rolling
	// summary (unless DisableCompaction), so storage keeps the full thread
	// while the prompt stays bounded.
	ContextDepth int `json:"context_depth,omitempty"`

	// DisableCompaction turns OFF the rolling-summary fold on the agent's
	// persistent threads: older messages are dropped down to the
	// ContextDepth tail rather than summarized. Both states are bounded;
	// this just chooses summarize-old vs forget-old. Default false (fold).
	DisableCompaction bool `json:"disable_compaction,omitempty"`

	// MemoryMode shapes the Explicit Memory layer (store_fact + the
	// always-in-prompt facts block) — selects which "what to put here"
	// directive the LLM sees. Two modes:
	//
	//   "agent" (default, empty == "agent"): focused on job-related
	//     learnings. Header "## Lessons learned"; store_fact for
	//     gotchas, working approaches, "X failed, do Y instead". NOT
	//     user personalization. Right for task agents (Builder, KB,
	//     research, code review, etc.) — the job IS the agent.
	//
	//   "chatbot": general personalization for chat-style agents.
	//     Header "## Saved facts"; store_fact for anything worth
	//     always-in-prompt — user prefs, name, time zone, recurring
	//     details, working notes. The catch-all mode.
	//
	// Default is "agent" because most agents are task-focused; only
	// general chatbots (like the Chat seed) want "chatbot" mode.
	// No-op when DisableExplicit is true.
	MemoryMode string `json:"memory_mode,omitempty"`

	// EnableNotes opts this agent into the Working-notes layer — a single
	// bounded, agent-rewritable block of RUNNING STATE injected always-in-
	// prompt (distinct from the append-list Explicit Memory facts). Off by
	// default; most task agents don't need running state. Surfaces the
	// update_notes tool and the "## Working notes" prompt block. See
	// core/notesstore.go.
	EnableNotes bool `json:"enable_notes,omitempty"`

	// SeedNotes is Builder's initial text for the Working-notes block. It
	// renders (and the agent can rewrite it) until the (user, agent) store
	// gets its first update_notes; the record's seed stays the durable
	// fallback. No-op when EnableNotes is false.
	SeedNotes string `json:"seed_notes,omitempty"`

	// IngestAttachments turns ON the "attachments become Knowledge"
	// path for this agent. On each turn that has extracted
	// attachment text (PDF / DOCX / plain text uploads), the text is
	// also ingested into the per-agent Knowledge vector store under
	// topic="attachments" so future sessions can recall it via
	// knowledge_search. Without this, attachments live only in the
	// current turn's context + the chat history.
	//
	// Attachments land in Knowledge (the authoritative read-only
	// layer), not Memory — uploaded files are user-provided source-
	// of-truth content. Right for document-Q&A, resume-reviewer,
	// contract-analyzer style agents where the uploaded file IS the
	// thing being discussed. Off by default — most agents handle
	// files transactionally and don't benefit from persisting them.
	IngestAttachments bool `json:"ingest_attachments,omitempty"`

	// AllowPrivateMode publishes a "Private" toggle on the public
	// /agents/<slug>/ surface. When the user flips it on, network-
	// capability tools (web_search, fetch_url, …) are filtered out
	// of the agent's allowed tool set for that turn so nothing
	// leaves the deployment. Off by default — admin opts in per
	// agent because a Research-style agent becomes useless without
	// network tools.
	AllowPrivateMode bool `json:"allow_private_mode,omitempty"`

	// ForcePrivate locks the agent into knowledge-only mode — every
	// turn behaves as if the user's Private toggle were ON, regardless
	// of what the user clicked, AND the public Private toggle is
	// hidden from the UI (no choice for the user to make). Use for
	// agents that should NEVER reach the network: compliance bots,
	// confidential-doc assistants, family/kid-facing agents. The
	// existing CapNetwork filter does the work — this flag just
	// forces the filter on.
	//
	// Composes with AllowPrivateMode: when ForcePrivate is true,
	// AllowPrivateMode's value is moot (we hide the toggle and force
	// the filter regardless).
	ForcePrivate bool `json:"force_private,omitempty"`

	// DisableSkills turns OFF the skills classifier for this agent.
	// When set, no skill ever activates on this agent's turns: no
	// per-skill addendums appended to the system prompt, no extra
	// tools added to the catalog, no skill_knowledge corpus injection,
	// no per-skill activity rows in the activity pane.
	//
	// Use for agents whose job is to faithfully report what a specific
	// knowledge source says (KB readers, doc-Q&A bots, compliance look-
	// ups, research summarizers). An auto-activated skill can otherwise
	// smuggle in its own corpus chunks or tone instructions and
	// contaminate the agent's answer — the user can't tell which part
	// came from the official KB vs. from a skill the classifier fired.
	//
	// Composes with the per-turn Clean toggle: when Clean is on, skills
	// are suppressed for that turn regardless of this flag. This flag
	// is the agent-level default; Clean is the per-turn override.
	// Default OFF (skills auto-activate as usual).
	DisableSkills bool `json:"disable_skills,omitempty"`

	// AllowedSkills is the strict allowlist of skill IDs the
	// classifier may consider for this agent. Every skill is opt-in
	// per agent — the owner curates which ones can fire from the
	// Knowledge surface. Empty (default) = no skills active for this
	// agent; the classifier is silent until the owner adds something.
	//
	// This replaced the old "auto skills fire everywhere unless
	// flagged OptInOnly" shape because that defaulted to bleed:
	// agents had to predict which skills *might* misfire and flag
	// them. Now the owner always sees and chooses; nothing surprises.
	AllowedSkills []string `json:"allowed_skills,omitempty"`

	// AttachedCollections lists Document Collection IDs this agent
	// pulls reference material from. Each collection's chunks merge
	// into the agent's RAG search at recall time. Many-to-many: one
	// agent can attach N collections; one collection can sit on N
	// agents. Empty means the agent searches only its own private
	// surfaces (uploaded knowledge + Reference Memory).
	AttachedCollections []string `json:"attached_collections,omitempty"`

	// AttachedPipelines lists pipeline def IDs this agent can run as
	// tools. Each attached pipeline surfaces as a callable tool on the
	// agent's catalog (lazy-loaded, like custom tools): calling it runs
	// the pipeline on the supplied input and returns the final output.
	// Lets a pipeline be a reusable capability bolted onto an agent,
	// the same way attached_collections bolt on reference corpora.
	AttachedPipelines []string `json:"attached_pipelines,omitempty"`

	// DisabledPipelines is a DENY-LIST of GLOBAL pipeline IDs this agent
	// opts out of — the pipeline analogue of DisabledPersistentTools. A
	// global pipeline (PipelineDef.Global) reaches every agent except those
	// listing its ID here. Ignored for non-global pipelines (those are
	// gated by AttachedPipelines instead). Managed via the scope pill.
	DisabledPipelines []string `json:"disabled_pipelines,omitempty"`

	// DisabledCredentials is a DENY-LIST of SecureAPI credential names this
	// agent may NOT dispatch through. Credentials are global (all agents)
	// by default; listing one here revokes it for this agent — any tool
	// whose .Credential matches is dropped from the agent's kit. Managed
	// via the scope pill on the admin Credentials section.
	DisabledCredentials []string `json:"disabled_credentials,omitempty"`

	// Hidden controls whether this agent is discoverable / callable
	// by OTHER agents in the fleet. Default false = public: appears
	// in every other agent's "Available agents" prompt block and is
	// dispatchable via agents(action="run"). True = hidden: dropped
	// from the global block AND dispatch refused — UNLESS a specific
	// caller agent has this agent's ID on its AllowedDispatchTargets
	// list (the explicit-link escape hatch). Use for personal agents
	// the user wants to chat with directly but doesn't want the
	// fleet routing to.
	Hidden bool `json:"hidden,omitempty"`

	// AllowedDispatchTargets is the per-caller dispatch allowlist.
	// Two modes depending on emptiness:
	//
	//   - Empty (default): "allow any" — this agent can dispatch to
	//     any non-Hidden agent in the fleet (the standard rule). The
	//     "Available agents" prompt block lists all of them.
	//   - Non-empty: "allowlist" — this agent can dispatch ONLY to the
	//     listed agent IDs (Hidden status ignored — the explicit pick
	//     wins both ways, so you can also use it to reach a Hidden
	//     specialist without exposing it globally). The "Available
	//     agents" block is filtered to just these entries.
	// The DispatchMode field below generalizes this; when DispatchMode is
	// empty a non-empty list is still read as "only" for back-compat.
	AllowedDispatchTargets []string `json:"allowed_dispatch_targets,omitempty"`

	// DispatchMode selects how AllowedDispatchTargets is interpreted, so the
	// list can express a denylist or a hard block, not just an allowlist:
	//   "" / "all" — dispatch to any non-Hidden agent (the default). When
	//                empty AND AllowedDispatchTargets is non-empty, it is read
	//                as "only" (back-compat with pre-DispatchMode allowlists).
	//   "only"     — allowlist: dispatch ONLY to the listed agents (Hidden
	//                ignored; the explicit pick wins).
	//   "except"   — denylist: dispatch to any non-Hidden agent EXCEPT the
	//                listed ones.
	//   "none"     — dispatch to NO agent at all (hard block).
	// Resolve via effectiveDispatchMode, never read raw — that helper applies
	// the back-compat inference and the deleted-target self-heal.
	DispatchMode string `json:"dispatch_mode,omitempty"`

	// OwnedBy names a parent agent that owns this one as a sub-agent.
	// Two effects:
	//   - Cascade delete: when the parent is deleted, every agent
	//     owned by it gets deleted too (record, sessions, memory,
	//     knowledge — same dropAgentSideData treatment as a direct
	//     delete). Prevents orphans when the parent goes away.
	//   - Implicit dispatch authority: the parent can dispatch to this
	//     sub-agent via agents(action="run") regardless of whether the
	//     sub-agent's ID appears in the parent's AllowedDispatchTargets
	//     list — ownership IS the link. Sub-agents typically also set
	//     Hidden=true so they don't appear in the global fleet block;
	//     the OwnedBy relationship keeps them reachable from the parent
	//     without an explicit allowlist entry.
	// Empty (default) = no parent, standard top-level agent.
	OwnedBy string `json:"owned_by,omitempty"`

	// Locked protects this agent from being edited or deleted BY ANOTHER AGENT
	// (the agent-CRUD tools). Only the human — via the dashboard/editor — can
	// change a locked agent. Off by default; the user opts in to protect agents
	// they don't want an authoring agent (Builder, a misfiring fleet) to touch.
	Locked bool `json:"locked,omitempty"`

	// InheritParentTools makes an owned sub-agent (OwnedBy set) resolve the
	// PARENT's inheritable tool catalog at runtime in ADDITION to its own
	// AllowedTools. "Inheritable" is the parent's non-consequential set — its
	// normal worker tools plus the read-only phantom tools (read_phantom_chat,
	// list_phantom_chats), but NOT the consequential Fleet tools (delegate,
	// message_contact, notify_me, standing-agent / monitor management). Lets a
	// Builder-authored summarizer read a phantom chat without being able to text
	// people or run the fleet. Opt-in (default false) so existing sub-agents are
	// unaffected. Only meaningful with OwnedBy set.
	InheritParentTools bool `json:"inherit_parent_tools,omitempty"`

	// PendingApproval holds a freshly-authored agent OUT of service until the
	// owner approves it. Set when a dispatched Builder mints an agent on a
	// parent's behalf: the record is saved but excluded from dispatch / run /
	// listing, and an Authorization (action=activate_sub_agent) lands in the
	// parent owner's queue. Approving flips this off. Default false = live.
	PendingApproval bool `json:"pending_approval,omitempty"`

	// Think overrides the LLM's reasoning mode for this agent's turns.
	// Tri-state stored as a string so it round-trips cleanly through
	// the form panel (HTML select) AND the LLM CRUD tool (string arg):
	//   - "" or "auto" = let the route / caller decide. Worker LLM's
	//     preserve_thinking + per-request thinking_budget govern.
	//   - "on" = force reasoning ON for every turn. Use for agents
	//     that decompose / plan / synthesize (Builder, top-level
	//     conversational agents, sub-agents whose job is reasoning).
	//   - "off" = force reasoning OFF for every turn. Use for fast
	//     focused specialists where reasoning just adds latency
	//     without improving the answer (lookup-shaped sub-agents,
	//     transformers, routers).
	//
	// Applied at the call site: when "on" or "off", the agent's value
	// overrides whatever the route would otherwise pick. CREATE defaults
	// (set by parseThinkArg): top-level agents default "on", sub-agents
	// default "off" — the right mode for each role's typical usage.
	Think string `json:"think,omitempty"`

	// (ForceClean removed — its semantic moved to DisableInferred.
	// Same behavior: stop the LLM-derived layer from growing and
	// exclude derived chunks from recall. The new name is more
	// honest about what it does.)

	// Evals is the agent's saved test cases — admin-curated prompts
	// with optional pass criteria. Run via the eval harness endpoint
	// (POST .../api/agents/{id}/eval) to catch prompt regressions
	// after edits to OrchestratorPrompt / AllowedTools / Tools.
	Evals []EvalCase `json:"evals,omitempty"`

	// Tools are agent-scoped temp tools that auto-load whenever this
	// agent runs. Same TempTool shape as session-scoped or persistent
	// tools (shell / api / pipeline modes all supported), but the
	// scope is THIS agent only — two agents can each have their own
	// "research_company" pipeline with totally different prompts and
	// they don't collide because they don't live in the user-wide
	// pool. No separate approval gate: agent-scoped tools live inside
	// the AgentRecord and inherit its trust boundary (whoever can
	// edit the agent's prompt can edit its tools).
	Tools []TempTool `json:"tools,omitempty"`

	// IntakeForm is an optional list of fields the user fills before
	// the FIRST turn of a new session. Empty list (the default) =
	// normal chat input on session open. When non-empty, the chat
	// surface shows the form instead of the text input; submitting
	// packs the values into a markdown user message and proceeds
	// with the normal turn flow. Subsequent turns in the same
	// session use the regular input. Useful for agents whose work
	// always starts from structured input (e.g. a marketing copy
	// agent that needs company / product / audience / deadline up
	// front).
	IntakeForm IntakeFormSpec `json:"intake_form,omitempty"`

	// GapCheck enables a post-plan structural-gap review pass. After
	// the orchestrator's worker steps finish (and before the final
	// synthesis), the runner asks the worker LLM to scan the accumulated
	// step outputs for failure modes (abstract sections without named
	// examples, evidence asymmetry, mechanism gaps) and produce 0-N
	// targeted follow-up subquestions. Detected gaps run as additional
	// worker steps that fold into the same plan; synthesis then sees the
	// full set. Cheap when the worker output is solid (returns "no gaps")
	// — pays the LLM round-trip + targeted fills when the output is
	// hollow. Best for research-flavored agents; leave off for chat.
	GapCheck bool `json:"gap_check,omitempty"`

	// PreMortem makes the agent plan-FIRST on real goals: before executing a
	// multi-step task it lays out the plan, critiques it (missing info, approval
	// gates, failure points, and steps whose result arrives LATER), and for each
	// deferred step provisions an await (await_result) and yields instead of
	// blocking or faking completion. Off by default — best for orchestrator-style
	// agents that accomplish goals, not chat agents that answer questions. Drives
	// the preMortemPlanningBlock system-prompt section.
	PreMortem bool `json:"pre_mortem,omitempty"`

	// KnowledgeModel is a Phase 3 placeholder.
	KnowledgeModel string `json:"knowledge_model,omitempty"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// (KnowledgeFramingConfig removed — the per-agent free-form facts
// framing collapsed into the MemoryMode enum on AgentRecord. Two
// built-in copy sets cover the actual use cases; custom per-agent
// copy was overkill.)

// ChatSession is one chat thread inside an agent. Messages are the
// orchestrator-user conversation visible to the user; Plans are the
// per-round decomposition snapshots that drove the activity pane.
// Participant identifies one member of a thread (channel). See
// docs/channel-model.md: every conversation is a thread; a 1:1 session is
// the case whose only agent participant is the lead. Kind is "human" or
// "agent"; ID is the user (human) or agent id (agent).
type Participant struct {
	Kind string `json:"kind"` // ParticipantHuman | ParticipantAgent
	ID   string `json:"id"`
}

const (
	ParticipantHuman = "human"
	ParticipantAgent = "agent"
)

type ChatSession struct {
	ID       string         `json:"ID"`
	AgentID  string         `json:"-"`
	Title    string         `json:"Title"`
	Messages []ChatMessage  `json:"Messages,omitempty"`
	Plans    []PlanSnapshot `json:"Plans,omitempty"`
	Created  time.Time      `json:"Created"`
	LastAt   time.Time      `json:"LastAt"`

	// Participants is the thread's membership (channel model — see
	// docs/channel-model.md). Stage 1 holds only the single lead agent; the
	// human owner is implicit in the user-scoped db. Additive and not yet read
	// by behavior — it gives Stage 2's shared channels + addressing a place to
	// live so the migration there is mechanical, not a schema change.
	Participants []Participant `json:"Participants,omitempty"`

	// LastSeen is when the user last viewed this session (set on open and
	// after their own turn). A background append — a monitor wake, a
	// standing-agent report, a goal-conversation completion — bumps LastAt
	// but NOT LastSeen, so the session reads unread until reopened. Per-user
	// already (sessions live in the user's db).
	LastSeen time.Time `json:"LastSeen,omitempty"`
	// Unread is computed at list time (LastAt.After(LastSeen)) for the
	// sidebar's unread dot. Not persisted — derived from the two timestamps.
	Unread bool `json:"unread,omitempty"`

	// Incognito marks a "clean room" session (cortex preset): it does NOT inherit
	// the agent's cortex standing context OR its memory/facts, and (write side)
	// does not store facts back. A one-off with no baggage in and nothing out.
	// Set at creation from the request; persisted for the session's life.
	Incognito bool `json:"incognito,omitempty"`

	// AwaitingUserConfirm is set when the previous orchestrator turn
	// ended via ask_user / ask_user_form. The next user message is
	// presumed to be the answer, and gated tools (agent CRUD, etc.)
	// may fire that turn. Reset to false at the END of any turn that
	// did NOT end with ask_user — without this, a smaller model can
	// emit a "Save this?" question as plain text alongside the
	// create_agent call and produce two messages to the user in one
	// turn (the question + the confirmation), with the agent already
	// saved. The runtime gate makes the two-turn flow an invariant.
	// Persisted so the gate survives a restart between asking and
	// answering.
	AwaitingUserConfirm bool `json:"AwaitingUserConfirm,omitempty"`

	// AuthoringAgentID is set when create_agent fires successfully in
	// this session — that agent becomes the implicit "authoring focus"
	// for the rest of the session. create_pipeline_tool /
	// create_temp_tool / create_api_tool calls that omit for_agent
	// auto-default to this agent, so the LLM doesn't have to re-state
	// which agent it's building tools for. Also surfaced in the
	// orchestrator's system prompt as a "## Authoring in progress"
	// section. Cleared when a new session begins; V1 has no explicit
	// "end authoring" — the next session is the natural reset.
	AuthoringAgentID string `json:"AuthoringAgentID,omitempty"`

	// BuildPlan holds the visible plan card state when Builder is
	// walking the user through a multi-step authoring flow. Set by
	// present_build_plan; updated by mark_step_done. Drives the
	// orchestrate_plan SSE block so the user sees a live checklist
	// updating as each step's tool fires.
	BuildPlan *BuildPlanState `json:"BuildPlan,omitempty"`

	// ActiveSkillIDs is VESTIGIAL — skills are per-turn now (the LLM
	// re-activates each turn it stays in-domain; nothing carries across
	// turns), so this is no longer read or written. Kept only so older
	// stored sessions that have the JSON deserialize cleanly.
	ActiveSkillIDs []string `json:"ActiveSkillIDs,omitempty"`

	// UIBlocks are session-level UI artifacts (show_html panes and any
	// future block-rendered surface) that a tool emitted live as a
	// {kind:"block"} SSE event, upserted by block ID so an updated
	// artifact stays ONE card rather than stacking. The panel replays
	// them through the block-renderer dispatch after the transcript on
	// session load, so an artifact survives reload. The live emission's
	// auto-open hint is NOT persisted — reloading never pops a pane.
	UIBlocks []UIBlock `json:"ui_blocks,omitempty"`
}

// UIBlock is one persisted UI artifact on a session — the durable half
// of a {kind:"block"} SSE emission. Type names the client-side renderer
// (e.g. "html_artifact"); ID is the block identity used for upserts;
// Title plus ONE of HTML (authored document, sandboxed render) or URL
// (same-origin page preview) are that renderer's payload.
type UIBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	HTML  string `json:"html,omitempty"`
	URL   string `json:"url,omitempty"`
	// Text is a short secondary line under the title — the link_hint
	// block's note. Distinct from HTML (a full document body).
	Text string `json:"text,omitempty"`
	// DataURLs is the html mode's declared live-data allowlist: the
	// same-origin GET paths the rendered document may fetch through the
	// pane's postMessage bridge (gohort.fetch). Persisted so a replayed
	// artifact keeps exactly the grants it was created with.
	DataURLs []string `json:"data_urls,omitempty"`
	// Data carries renderer-specific string fields that don't warrant
	// their own column (e.g. a credential_setup card's cred_type/grant).
	// Live SSE emissions mirror the same keys under "data" so a replayed
	// block and a live block present identically to the renderer.
	Data map[string]string `json:"data,omitempty"`
}

// BuildPlanState is the persisted plan-card state for Builder's
// multi-step execution flow. ID stays stable across emissions so
// the renderer's onUpdate hook re-renders the same card rather than
// stacking new ones. Steps are 1-indexed in user-facing land but
// stored 0-indexed in the slice.
type BuildPlanState struct {
	ID    string          `json:"id"`
	Steps []BuildPlanStep `json:"steps"`

	// RevisionCount tracks how many times revise_build_plan has been
	// called this session. Capped at BuildPlanRevisionLimit (3) to
	// prevent the model from re-shuffling instead of executing — same
	// guard servitor's Plan.IncrRevision enforces. Reset to zero when
	// present_build_plan replaces the plan wholesale.
	RevisionCount int `json:"revision_count,omitempty"`

	// GapsReported flips when report_build_gaps has been called.
	// Builder's Phase 4 → Phase 5 transition demands an explicit gap
	// report before the final user-facing reply, so the model is
	// honest about blocked or skipped steps. Set once per plan;
	// re-presenting the plan resets it.
	GapsReported bool `json:"gaps_reported,omitempty"`
}

// BuildPlanRevisionLimit caps how many times revise_build_plan can
// fire per session. Mirrors servitor's PlanRevisionLimit = 3 — a
// hard ceiling that keeps the model from reshuffling indefinitely
// instead of executing the plan it already has.
const BuildPlanRevisionLimit = 3

// BuildPlanStep mirrors the fields the orchestrate_plan renderer
// consumes: title (line text), status (pending / in_progress / done
// / blocked), what_to_find (sub-line detail), findings (one-line
// result after step done).
type BuildPlanStep struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	WhatToFind    string `json:"what_to_find,omitempty"`
	Status        string `json:"status"` // pending | in_progress | done | blocked
	Findings      string `json:"findings,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// ChatMessage mirrors the conventional {role, content} pair the
// AgentLoopPanel SSE protocol expects in its replay payload, plus
// Created (wall-clock at save time, surfaced as a hover-only
// timestamp in the UI) and an optional Usage payload (assistant
// turns only, persisted so the per-message stats footer survives
// session reload — the SSE flow already emits the same shape live
// via emitStats).
type ChatMessage struct {
	Role    string            `json:"role"`
	Content string            `json:"content"`
	Created time.Time         `json:"created,omitempty"`
	Usage   *ChatMessageUsage `json:"usage,omitempty"`
	// ToolCalls captures every tool the orchestrator + worker steps
	// fired during this turn. Persisted so the session export gives a
	// full debug trace (live UI shows them via SSE during the turn
	// and they vanish on reload otherwise). Only set on assistant
	// messages — user messages leave this nil.
	ToolCalls []PersistedToolCall `json:"tool_calls,omitempty"`
	// Hidden marks a message that already has a visible record in the
	// PREVIOUS bubble (e.g. an ask_user card whose submitted-state
	// shows the picked options). The replay path skips rendering a
	// bubble for it while the LLM still sees the content in history.
	Hidden bool `json:"hidden,omitempty"`
	// IntakeValues, when non-empty, marks this user message as the
	// result of submitting the agent's intake form. Map is keyed by
	// field NAME (matches IntakeFormSpec.Name) — labels are derived at
	// render time from the agent's current IntakeForm spec. Stored so
	// re-edit on replayed sessions can rehydrate the form with the
	// original values instead of degrading to text editing.
	IntakeValues map[string]string `json:"intake_values,omitempty"`
	// ReportFrom, when set, marks this assistant message as an automated
	// report from a producer (a standing agent, a monitor) rather than a turn
	// in the back-and-forth. It carries the producer's display name; the UI
	// renders such messages as a distinct card (header = name + fire time, from
	// Created) instead of a normal chat bubble. The body stays clean; the LLM
	// still gets a "[standing agent …]" context marker prepended at history-
	// build time (see toLLMMessages).
	ReportFrom string `json:"report_from,omitempty"`
	// ReportKind classifies a ReportFrom card so the UI can pick a fitting
	// icon — a channel message from a person reads differently from a scheduled
	// report or a monitor wake. One of: "message" (a channel inbound),
	// "scheduled" (a standing-agent report), "monitor" (an event-monitor wake),
	// "deliverable" (a filed artifact pointer). Empty falls back to the default
	// (scheduled) icon. Display-only — the LLM context marker keys off ReportFrom.
	ReportKind string `json:"report_kind,omitempty"`
	// Sender names who authored this message, for channel-room transcripts
	// where the session is a multi-party messaging thread: the contact's
	// display name on inbound (user) messages, the bound agent's name on its
	// replies. Lets a GROUP channel render real who-said-what (the session
	// title alone can't, since many contacts share one room). Empty on plain
	// web sessions, where the anonymous you/assistant bubbles are correct.
	Sender string `json:"sender,omitempty"`
}

// PersistedToolCall is one tool invocation persisted alongside the
// assistant message that owns it. Args is the LLM-supplied argument
// map (raw, not normalized); Result is what the handler returned (or
// Err when the call failed). Used by the session export and any
// future "show me what happened" UI affordance.
type PersistedToolCall struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
	Result string         `json:"result,omitempty"`
	Err    string         `json:"err,omitempty"`
	Cached bool           `json:"cached,omitempty"` // true = wrapper returned cached body (no fresh dispatch)
}

// ChatMessageUsage is the per-assistant-message token / throughput
// snapshot. Field names match the SSE stats event so the UI's
// replay path can hand it straight to renderMessageStats without
// further mapping.
type ChatMessageUsage struct {
	InputTokens     int     `json:"input_tokens,omitempty"`
	OutputTokens    int     `json:"output_tokens,omitempty"`
	ReasoningTokens int     `json:"reasoning_tokens,omitempty"`
	TokensPerSec    float64 `json:"tokens_per_sec,omitempty"`
	PromptPerSec    float64 `json:"prompt_per_sec,omitempty"`
	ElapsedMs       int64   `json:"elapsed_ms,omitempty"`
}

// PlanSnapshot captures the plan as it stood at the end of one user
// round. Replayed on session load so the user can scroll back through
// the activity pane and see what was decided each turn.
type PlanSnapshot struct {
	RoundIndex int        `json:"round_index"`
	Steps      []PlanStep `json:"steps"`
}

// PlanStep is one item in a plan. Status flips pending → in_progress
// → done as the worker executes each step.
//
// Field roles:
//   - Title         — short step name (1 line), declared by the orchestrator.
//   - Intent        — what the step is LOOKING FOR / aims to accomplish.
//     Visible to the user BEFORE the worker runs so they see
//     what the agent is about to do. Optional.
//   - WorkerBrief   — the SYSTEM PROMPT the worker receives for this step.
//     Authored by the orchestrator at plan time. Specific
//     about what to produce, what tools to prefer, what
//     format to use. Framework prepends rules + memory and
//     appends the tool-use directive automatically — the
//     brief itself should focus on this step's deliverable.
//     Not user-visible. Optional (a fallback is synthesized
//     from title + intent when empty).
//   - Findings      — short summary surfaced inline once the step completes.
//     Derived from the first paragraph of Output when not
//     set explicitly. Optional.
//   - Output        — full worker text. Lives in the collapsible.
//   - BlockedReason — error message when status=blocked.
type PlanStep struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Intent      string `json:"intent,omitempty"`
	WorkerBrief string `json:"worker_brief,omitempty"`
	// Tools is the orchestrator's explicit per-step tool surface.
	// When set, the worker for THIS step sees exactly these tools
	// (intersected with the agent's allowed pool for safety) +
	// framework essentials — no classifier-trim, no surplus. Empty
	// = fall back to the agent's full pool (degraded behavior; the
	// orchestrator should specify when it knows the answer).
	Tools         []string `json:"tools,omitempty"`
	Status        string   `json:"status"` // "pending" | "in_progress" | "done" | "blocked"
	Findings      string   `json:"findings,omitempty"`
	Output        string   `json:"output,omitempty"`
	BlockedReason string   `json:"blocked_reason,omitempty"`
}

// Plan-step status constants. Strings (not iota) so JSON roundtrips
// readably and the wire shape matches the AgentLoopPanel block payload.
const (
	StepPending    = "pending"
	StepInProgress = "in_progress"
	StepDone       = "done"
	StepBlocked    = "blocked"
)

// IntakeFormSpec is the on-disk shape of an agent's intake form.
// Wrapped in a named type so we can implement UnmarshalJSON to accept
// either the canonical array shape (what the API/clone path posts)
// OR a JSON string carrying that array (what the editor textarea
// posts since FormField type="textarea" returns a string). Empty /
// whitespace string → no intake form, matching the empty-array
// behavior.
type IntakeFormSpec []IntakeField

// UnmarshalJSON accepts either:
//
//   - A JSON array of IntakeField objects (canonical wire shape).
//   - A JSON string containing the array as text (editor textarea).
//   - A JSON null or empty string → no intake form (nil slice).
//
// Anything else is a hard error so a misformed textarea entry surfaces
// to the admin instead of silently dropping their authored fields.
func (f *IntakeFormSpec) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*f = nil
		return nil
	}
	switch trimmed[0] {
	case '[':
		var arr []IntakeField
		if err := json.Unmarshal(data, &arr); err != nil {
			return err
		}
		*f = arr
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = nil
			return nil
		}
		var arr []IntakeField
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			return fmt.Errorf("intake_form must be a JSON array of field objects: %w", err)
		}
		*f = arr
		return nil
	}
	return fmt.Errorf("intake_form must be an array or a JSON-string array, got %s", string(data))
}

// IntakeField is one field on an agent's IntakeForm. Shape mirrors
// FormField in core/ui but tighter — intake forms are structured
// data collection, not a settings panel.
type IntakeField struct {
	Name        string   `json:"name"`                  // key for the markdown pack ("**<label>:** value")
	Label       string   `json:"label"`                 // user-facing label rendered above the input
	Type        string   `json:"type,omitempty"`        // "text" (default), "textarea", "select", "checklist", "number", "file", "button"
	Placeholder string   `json:"placeholder,omitempty"` // hint shown inside an empty input
	Help        string   `json:"help,omitempty"`        // optional explanatory copy under the input
	Required    bool     `json:"required,omitempty"`    // when true, blank value blocks submit (for checklist: at least one box checked with a non-empty value)
	Options     []string `json:"options,omitempty"`     // for type=select / checklist / button; ignored otherwise. checklist joins selected values comma-separated when packing the intake markdown.
	AllowOther  bool     `json:"allow_other,omitempty"` // for type=checklist only. When true, renders an extra "Other:" row with a free-text input. Non-empty text becomes a list value, joined with the other picks ("**Topics:** AI, Healthcare, my custom thing"). Lets the user contribute outside the curated options without forcing the LLM to pre-imagine every answer.
}

// EvalCase is one saved test case on an agent — admin-curated prompt
// + grading criteria. The eval harness runs every case as an
// independent fresh session, captures the agent's reply, and grades
// it against MustInclude / MustNotInclude / JudgePrompt.
type EvalCase struct {
	Name           string   `json:"name"`                       // short label, e.g. "asks_clarifying"
	Prompt         string   `json:"prompt"`                     // user message to send the agent
	MustInclude    []string `json:"must_include,omitempty"`     // case-insensitive substrings expected in the reply
	MustNotInclude []string `json:"must_not_include,omitempty"` // case-insensitive substrings that must NOT appear
	// Tool-USE grading — is the model effective at using the tools we built?
	// MustCallTools: tool names the model must call at least once this run
	// (e.g. ["await_result"] for a deferred-reply scenario). MustNotCallTools:
	// tools it must NOT call. Graded from the actual tool-call trace, not the
	// reply text, so it catches "narrated it but never called the tool".
	MustCallTools    []string `json:"must_call_tools,omitempty"`
	MustNotCallTools []string `json:"must_not_call_tools,omitempty"`
	// StubResults (eval STUB mode only) scripts what each tool RETURNS instead of
	// running for real — keyed by tool name. Lets a scenario hand the model a
	// realistic result ("Queued a message…", "Stored at find-x.jpg…") so a
	// multi-step case behaves like production, without the side effect. A tool
	// with no entry returns a generic stub notice. Ignored unless stub mode is on.
	StubResults map[string]string `json:"stub_results,omitempty"`
	JudgePrompt string            `json:"judge_prompt,omitempty"` // optional. When set, an LLM judge grades the reply against this criterion (yes/no)
	Notes       string            `json:"notes,omitempty"`        // admin notes, not used by the grader
}

// EvalResult is one row from a harness run.
type EvalResult struct {
	Name string `json:"name"`
	// Passed is strict — every run passed. Runs/Passes carry the pass RATE, which
	// is the real signal for a non-deterministic model ("awaited 27/30").
	Passed      bool     `json:"passed"`
	Runs        int      `json:"runs,omitempty"`         // how many times the case was run
	Passes      int      `json:"passes,omitempty"`       // how many of those passed
	Output      string   `json:"output"`                 // the agent's reply (truncated for display)
	Reasons     []string `json:"reasons,omitempty"`      // why a case failed (or "ok" entries on pass)
	ToolsCalled []string `json:"tools_called,omitempty"` // distinct tools the model called this run
	ErrText     string   `json:"error,omitempty"`        // populated when the agent itself errored mid-run
}
