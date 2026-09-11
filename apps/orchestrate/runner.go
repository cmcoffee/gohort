package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// runPlan asks the orchestrator (thinking LLM) to decide its next
// move. The orchestrator picks ONE of three tools:
//
//   - respond_directly(text): reply to the user without spinning up
//     a worker or synthesis call. For chitchat, acknowledgements,
//     and questions the orchestrator can answer from its own
//     knowledge with confidence. One LLM round total.
//   - plan_set(steps): commit to a plan; runner executes each step.
//   - ask_user(question): pause and ask the user for clarification
//     before planning. The runner short-circuits, surfaces the
//     question as an assistant message, and waits for the user's
//     next turn before re-entering the planning round.
//
// Return contract: (steps, question, directReply, err). At most one
// of (steps, question, directReply) is non-empty.
//   - directReply non-empty → emit as the reply, end the round.
//   - steps non-empty → proceed with the plan.
//   - question non-empty → relay to user, end the round.
//   - all empty → orchestrator chose no tool. Caller substitutes a
//     one-step "Respond" plan so the flow still produces a reply.

// planRun is one orchestrator turn. runPlan was a single 1,800-line function
// whose closures shared all of this as locals; the locals are fields now so
// each phase of the turn is a method, and the callbacks the agent loop fires
// (stream, step, round start, settle, retract) are methods on the same value.
// One per runPlan call, never reused — the fields are the turn's live state.
type planRun struct {
	t    *chatTurn
	msgs []ChatMessage
	// telem is fed by onStepHandler and summarized when runPlan exits:
	// rounds used, tool-call breakdown, dup-args fingerprinting and the exit
	// reason, so budget-tuning and drift-pattern questions have data to
	// look at.
	telem *turnTelemetry

	// Prompt. triggerMsg is the newest user message, hoisted because the
	// phase machine runs ON it before there is a persona to put its findings
	// into, and the trigger hints read it too. sys is the byte-stable system
	// prompt; turnContext is the message-dependent tail that goes on the LAST
	// user message instead (see assemblePrompt for the cache reason). facts is
	// the same slice the prompt was built from — UncheckedClaims judges that
	// slice, not a second read of the store.
	triggerMsg  string
	mach        turnMachine
	sys         string
	turnContext string
	facts       []MemoryFact

	// orchCtx is the orchestrator's child context. A control tool cancels it
	// to stop the agent loop after the current round while t.ctx stays live;
	// the captured state below is dispatched after the loop returns.
	orchCtx    context.Context
	cancelOrch context.CancelFunc
	maxSteps   int

	// Captured by the control-tool handlers (plan_set, ask_user,
	// ask_user_form) and dispatched by finish.
	capturedSteps     []PlanStep
	capturedQuest     string
	capturedOptions   []string
	capturedMulti     bool
	capturedFormSteps []map[string]any
	capturedReply     string
	// withheldLeadIn is prose a tool round streamed and the length guard took
	// back, kept so the turn can put it back if the guard's bet does not pay.
	withheldLeadIn string

	// plan_set fixation guard: a Qwen failure mode is re-submitting a
	// rejected (single-step / vacuous) plan_set round after round, ignoring
	// the "use respond_directly" feedback. Count rejections; once we hit
	// planSetDropThreshold, RoundToolFilter drops plan_set from the catalog
	// for the rest of the turn so the model is FORCED off it.
	// forceNoThinkAfterReject makes the round right after a rejection skip
	// thinking — the model was burning 24k-token budgets deliberating itself
	// back into the same plan_set. Single-threaded per turn (handler + round
	// hooks run in the loop goroutine), so no locking needed.
	planSetRejects          int
	forceNoThinkAfterReject bool
	// compactRequested: set by the compact_context tool when the LLM wants
	// to proactively shed verbose tool-result bodies it's done with (e.g. a
	// smoke-test report). Consumed by the RoundCompactNow hook, which forces
	// an aggressive history compaction on the next round.
	compactRequested bool

	// The tool session and the catalog the model receives.
	sess     *ToolSession
	allTools []AgentToolDef
	sessID   string

	// Routing, and the keepalive that spans the loop.
	stopKeepalive func()
	tierPin       LLMTier
	routeKey      string
	think         bool

	// Per-round bubbles: each agent-loop round that streams text gets its own
	// assistant bubble, finalized at the round boundary by onStepHandler.
	// Tool-only rounds (no streamed content) don't materialize a bubble.
	// lastFinalizedID/Text track the MOST RECENT finalized bubble so the
	// post-loop captured-text dispatch can avoid emitting a duplicate when
	// the LLM streamed the same text it then passed to ask_user. The match is
	// near-duplicate against the last bubble only, NOT substring-over-all-
	// bubbles — that dropped a short reply whenever it happened to be a
	// substring of a larger earlier block. Dropping a reply is far worse than
	// an occasional double.
	//
	// holdStream: agents with an OUTPUT guardrail (pre_output/periodic) must
	// not paint tokens live — a blocked reply would flash on screen before
	// the verdict exists. Buffer silently and paint the whole bubble at round
	// close, AFTER the guardrail has passed it (paintHeldBubble); a blocked
	// round is retracted having shown nothing. streamFreshBubble tracks
	// whether THIS round's bubble was minted here (so paint must open it) vs
	// adopted from a tool bubble already on screen (paint only appends).
	streamMsgID       string
	streamedBuf       strings.Builder
	lastFinalizedID   string
	lastFinalizedText string
	holdStream        bool
	streamFreshBubble bool
	// produced: what this turn has run a deliverable producer for, tracked
	// live off the step callback. Read by the phantom-delivery guard, which
	// has to answer "was this turn making a picture?" while the loop is still
	// running — long before the transcript the after-the-fact backstop reads.
	produced *deliveryWatch

	// Round budget: soft cap, explorer hard cap, and the loop's absolute
	// ceiling (initRoundCaps); roundCounter paces the nudges, orchRoundsUsed
	// is what StopRound governs against.
	maxR, orchHardCap, absoluteCeiling int
	orchRoundsUsed, roundCounter       int

	// The loop's input and outcome.
	llmMsgs   []Message
	gDecline  string
	userSaid  string
	orchStart time.Time
	resp      *Response
	loopErr   error

	// cat is the catalog build in progress; see buildCatalog.
	cat catalogState
}

// planSetDropThreshold is how many plan_set rejections in one turn drop the
// tool from the catalog (see planRun.planSetRejects).
const planSetDropThreshold = 2

func (t *chatTurn) newPlanRun(msgs []ChatMessage) *planRun {
	pr := &planRun{
		t:          t,
		msgs:       msgs,
		telem:      newTurnTelemetry(),
		maxSteps:   resolveMaxPlanSteps(t.agent),
		holdStream: agentHasOutputGuardrail(t.agent),
		produced:   new(deliveryWatch),
	}
	pr.orchCtx, pr.cancelOrch = context.WithCancel(t.ctx)
	return pr
}

// runPlan is the orchestrator round: assemble the prompt and the catalog, run
// the agent loop with the control tools that end it, then dispatch whatever
// the loop captured. Each phase is a method on planRun; this is the order
// they run in and the defers that bracket them.
func (t *chatTurn) runPlan(msgs []ChatMessage) (steps []PlanStep, question, directReply string, err error) {
	pr := t.newPlanRun(msgs)
	// Telemetry summary at function exit; see planRun.telem.
	defer func() {
		softCap := resolveMaxWorkerRounds(t.agent)
		hardCap := softCap
		if t.agent.AllowExplorer {
			if ec := resolveExplorerHardCap(t.agent); ec > hardCap {
				hardCap = ec
			}
		}
		exitReason := classifyOrchestratorExit(
			err,
			pr.telem.rounds, softCap,
			directReply != "",
			question != "",
			len(steps) > 0,
			t.ctx.Err() != nil,
		)
		label := "orchestrate.orch"
		Log("%s", pr.telem.summary(label, softCap, hardCap, exitReason)+" agent="+t.agent.ID)
		if line := pr.telem.toolCallSummary(label); line != "" {
			Log("%s", line)
		}
		// Churn is a user-visible outcome, not just a log line — route it
		// to the ⚠ trail so the person who saw the turn go wrong can see
		// WHY without reading server logs.
		if kind, detail, ok := pr.telem.churnDiag(); ok {
			t.turnDiag(kind, detail)
		}
	}()

	pr.assemblePrompt()
	// Closes over t.machine, not pr.mach: change_phase may have moved the
	// turn somewhere else since, and the handoff belongs to the phase that
	// actually ended the turn.
	defer func() { t.completeMachine(t.machine) }()
	defer pr.cancelOrch()

	Debug("[orchestrate.orch] runPlan: building tool session")
	pr.sess = t.newToolSession()
	// Persist any managed-workspace switch this inline turn performs so
	// the next step/turn's session lands in the same workspace.
	defer t.captureActiveWorkspace(pr.sess)
	if err := pr.buildCatalog(); err != nil {
		return nil, "", "", err
	}
	pr.resolveRouting()
	pr.initRoundCaps()
	pr.prepareMessages()
	pr.runLoop()
	return pr.finish()
}

func (pr *planRun) assemblePrompt() {
	t := pr.t
	// Assembly order matters — LLMs weight more-recent prompt
	// content heavier. We want the agent's persona to be the most
	// authoritative directive, so the universal "How this round
	// works" framework block goes BEFORE the persona, not after.
	// Agents with detailed phased personas (Builder) skip the
	// universal block entirely — their persona governs the rhythm.
	persona := t.gatedPersona(t.agent.OrchestratorPrompt)
	if !isBuilderAgent(t.agent.ID) {
		persona = roundShapePreamble(resolveMaxPlanSteps(t.agent)) + persona
	}
	// The newest user message, hoisted above the persona because the phase
	// machine needs it: a decompose phase runs ON this message, before
	// there is a persona to put its findings into. Also feeds the trigger
	// hints further down, which is where it used to be computed.
	pr.triggerMsg = ""
	for i := len(pr.msgs) - 1; i >= 0; i-- {
		if pr.msgs[i].Role == "user" {
			pr.triggerMsg = pr.msgs[i].Content
			break
		}
	}
	// Session-resident phase machine (machine.go, docs/agent-machines.md).
	// Walks any transient phases at the head of this turn and returns the
	// phase that owns the reply. Inert — and free — for an agent with no
	// machine, which is every agent unless its author picked one.
	//
	// The phase layer goes AFTER the persona for the same reason the
	// round-shape preamble goes before it: recency weights, and the phase
	// is the most authoritative instruction in the turn. It is also
	// byte-stable across a resident run, so it costs no cache.
	pr.mach = t.enterMachine(pr.triggerMsg)
	persona += pr.mach.Block()
	// Incognito (clean-room) session: inherit NOTHING — no memory facts and no
	// cortex standing context. A one-off with no baggage. Connected sessions
	// (the default) get both.
	// facts() and operatingNotes() each enforce incognito themselves, so this
	// turn gets the clean room without a second copy of the rule here — and so
	// do the worker and synthesis prompts, which this function cannot reach.
	// The flag is still read below, where the cortex standing context is a
	// separate inheritance this session also severs.
	incognito := t.session != nil && t.session.Incognito
	pr.facts = t.facts()
	notes := t.operatingNotes()
	pr.sys = prependAgentContext(persona, t.agent, pr.facts, notes)
	// Cortex awareness injection — recent STANDING context (received channel
	// messages, monitor fires) as read-only background so the agent greets you
	// already aware. Concise live-read; empty when nothing's recent. Cross-session
	// FACT continuity rides memory, not this. Two cases:
	//
	//   - Normal (owner/admin): a NON-cortex session seeds from the agent's real
	//     cortex in the caller's own namespace; the cortex's OWN thread skips it
	//     (it already holds these messages).
	//
	//   - Granted user (non-owner): runs in their OWN namespace, whose cortex is
	//     blank — so seeding from t.udb gives them nothing. Inject the OWNER's
	//     real cortex read-only, so they get the agent that "knows the things"
	//     without ever reaching the thread itself (the dashboard exposes no cortex
	//     thread at all). No opt-in: publishing the agent + granting access IS the
	//     consent to share its standing awareness. Skipped for seed agents (no
	//     single owner namespace).
	if !incognito && t.agent.Cortex && t.session != nil {
		fromOwner := t.agent.Owner != "" && t.agent.Owner != seedOwner && t.user != t.agent.Owner
		switch {
		case fromOwner:
			if odb := UserDB(t.app.DB, t.agent.Owner); odb != nil {
				pr.sys += cortexContextBlock(odb, t.agent.ID)
			}
		case t.session.ID != cortexSessionID(t.agent.ID):
			pr.sys += cortexContextBlock(t.udb, t.agent.ID)
		}
	}
	// (credentialFirstGuidance is Builder-persona territory now: the
	// credential-draft tools were pulled from the non-Builder catalog — see the
	// tool-count note at the tool_def append below — so the 4K guidance block
	// that taught every agent how to use them left with them. Builder carries
	// the equivalent doctrine in its own persona.)
	// Any skill the LLM activated mid-turn via activate_skill is
	// re-injected here as well — the orchestrator round sees the
	// skill via the tool result in conversation history naturally,
	// but a fresh prompt build (e.g. round-2 re-entry after a
	// catalog change) wouldn't carry that history forward without
	// this anchor.
	// Triggered skills: inject the instructions of any allowed skill whose
	// triggers match this turn (deterministic — framework decides by
	// trigger match, not the LLM). No-op when none match.
	// Per-turn, MESSAGE-DEPENDENT content (triggered-skill instructions +
	// skill/agent trigger hints) is collected into turnContext and appended
	// to the LAST user message below — deliberately NOT spliced into the
	// system prompt. Message-varying text in the sys prefix changes the
	// prefix every turn, which forces a full KV-cache re-prefill on the
	// recurrent/hybrid worker model (the orchestrate turn-latency bug: every
	// turn re-prefilled ~16k instead of reusing the cached prefix). Keeping
	// sys byte-stable lets turns 2+ reuse the prefix; the hints also belong
	// next to the user message (highest salience) per their own design intent.
	pr.turnContext = t.renderTriggeredSkills()
	// full instructions for skills already consulted
	pr.turnContext += t.renderSkillTriggerHints(pr.triggerMsg)
	// soft nudge for skills whose triggers matched
	// ("Available skills" block moved into appendAgentCapabilityBlocks below, so
	// the channel/dispatch path renders the same set — do NOT re-append here or
	// it doubles.)
	// "Available agents" block — lists the user's OTHER agents so
	// the host LLM knows what specialists exist and can dispatch to
	// them via agents(action="run", agent=..., message=...). Without
	// this block the LLM has to call agents(action="list") to
	// discover them, which it almost never does speculatively.
	pr.sys += t.renderAvailableAgentsBlock()
	// Per-turn dispatch nudge: when this turn matches an agent's triggers,
	// hint "dispatch to it FIRST" right after the catalog — the salient,
	// turn-specific signal the static block alone doesn't provide. Soft;
	// the model still decides. No-op when no agent's triggers match.
	pr.turnContext += t.renderAgentTriggerHints(pr.triggerMsg)
	// see turnContext note above — appended to user msg, not sys
	// Recall hints: a cheap scored-pointer nudge toward the agent's own
	// knowledge corpus (opt-in per agent), so it pulls relevant material with
	// knowledge_search instead of missing it. Pointers only, next to the message
	// — no chunk bodies injected. No-op when off or nothing scores high enough.
	pr.turnContext += t.renderRecallHints(pr.triggerMsg)
	// Active dispatch threads: remind the host of agents it already
	// delegated to THIS session so a follow-up re-dispatches instead of
	// being answered inline (the host's own history hides the delegation,
	// and a generic follow-up won't re-match the agent's triggers). Near
	// the user message for salience, like the trigger hint. No-op when no
	// dispatch thread is open this session.
	pr.turnContext += t.renderActiveDispatchThreads()
	// "Available sources" block — the catalog for query_source: lists each
	// admin-exposed source hook (name — what it covers / when to use), so
	// the model picks a source and calls query_source(source, query). One
	// dispatcher + this menu instead of N per-hook tools (the agents
	// pattern). No-op when no hooks are exposed.
	if t.app != nil {
		pr.sys += RenderAvailableSourcesBlock(t.app.DB)
	}
	// Builder-only: list the user's existing persistent custom tools
	// as READ-ONLY awareness. They're hidden from Builder's executable
	// catalog (see newToolSession's isBuilderAgent skip) — this block
	// is how Builder still knows what exists. No-op for every other
	// agent.
	pr.sys += t.renderBuilderExistingToolsBlock()
	// "Known topics" block — lists the snake_case slugs this
	// (user, agent) has already used so the LLM reuses them when
	// calling memory_save / memory_search instead of minting near-
	// duplicates ("ssh_keys" vs "sshkey" vs "ssh-keys"). Replaces
	// the per-turn topic auto-classifier — now the LLM picks.
	pr.sys += t.renderKnownTopicsBlock()
	// ("Available knowledge collections" block unmounted alongside
	// dispatch_to_worker — there's no LLM-facing tool that
	// references collections by ID right now, so advertising them
	// would only confuse the LLM. Collections still surface via
	// agent.AttachedCollections → knowledge_search and skill.AttachedCollections → knowledge_search.)
	// Append a "search order" stub when the agent has both a knowledge
	// store AND web tools in its catalog. Without it, many models
	// default to web_search for any question they're unsure about,
	// even when the agent's own corpus has the answer. The stub
	// reorders that default: knowledge first, web only as fallback.
	// Per-agent capability guidance (plan guidance, available-skills, search-order,
	// pre-mortem) — routed through the SHARED assembler so the exact same set
	// reaches the channel/dispatch path too (see appendAgentCapabilityBlocks).
	// Each block self-gates on agent config; kept here (after the other blocks)
	// so it's the single source both surfaces share.
	pr.sys = appendAgentCapabilityBlocks(pr.sys, t.agent, t.udb, t.user, true)
}

func (pr *planRun) planSetToolDef() AgentToolDef {
	t := pr.t
	stepBudget := fmt.Sprintf("up to %d step%s", pr.maxSteps, plural(pr.maxSteps))
	return AgentToolDef{
		Tool: Tool{
			Name:        "plan_set",
			Description: fmt.Sprintf("Commit to a multi-step plan for this turn. Each step is {title, intent, worker_brief}; the framework spins up a fresh focused worker LLM per step. Use ONLY when the turn genuinely needs decomposition (research with multiple branches, layered analysis, complex multi-tool workflows). For single-tool turns CALL THE TOOL DIRECTLY (you have web_search, fetch_url, calculate, etc.) instead of going through plan_set — it's much faster. **MINIMUM 2 STEPS** — a 1-step plan is wasteful (planner + worker + synthesis for what would be one inline call); the handler rejects it. Budget: %s.", stepBudget),
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: fmt.Sprintf("Ordered list of steps, %s. Each step is an object with title, intent, and worker_brief. The brief is the worker LLM's system prompt for that step.", stepBudget),
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":        {Type: "string", Description: "Short step name (1 line)."},
							"intent":       {Type: "string", Description: "One-sentence statement of what this step is looking for or aims to produce. Shown to the user BEFORE the worker runs."},
							"worker_brief": {Type: "string", Description: "The SYSTEM PROMPT the worker LLM gets for this step. 2-5 sentences. Be specific about what to produce, output format, and what to avoid. The framework auto-injects available tools + agent rules; you don't need to repeat them. **Always end the brief with: \"Lead your final response with ONE concrete sentence summarizing what you accomplished — that line surfaces on the plan card as the step's outcome. Then write the details underneath.\"** Workers that lead with process narration (\"I searched for X...\") leave the user with a misleading status line; leading with the outcome (\"Found 3 candidate Foundation cast updates from Apple TV's October press release.\") makes the plan card honest. NOT visible to the user."},
							"tools": {
								Type:        "array",
								Description: rewriteMemoryToolNames("Explicit tool surface for THIS step's worker — tight list of tool names the worker actually needs to complete the brief. Pick from the tools currently in YOUR catalog (the worker can't access tools you don't see). Be tight: 2-5 names is typical; one tool is common when the step is a focused lookup. The knowledge/memory tools and agents are always appended for the worker — don't list them. The worker has NO plan_set and NO ask_user, so the brief must stand on its own: fold any clarification the step needs into the brief up front. If you're unsure which tools the worker needs, list none and the worker gets the agent's default pool (broader catalog, more LLM cognitive load)."),
								Items:       &ToolParam{Type: "string"},
							},
						},
						Required: []string{"title", "worker_brief"},
					},
				},
			},
			Required: []string{"steps"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			steps := parsePlanSteps(args["steps"], pr.maxSteps)
			// 1-step minimum on Builder; 2-step minimum elsewhere.
			//
			// The general rationale for ≥2 stands for research /
			// chat: a 1-step plan_set wraps planner+worker+synthesis
			// around what should be one inline call, and the model
			// has the inline tool available anyway. But Builder's
			// orchestrator catalog deliberately EXCLUDES the
			// workhorses (create_agent, add_tool, tool_def — see
			// builderAuthoringTools), so Builder has no "do it
			// inline" path. A genuine single-step authoring task
			// ("create this one tool") is forced through plan_set,
			// and a hard ≥2 here would loop into the rejection-then-
			// improvise bypass that motivated this whole change.
			minSteps := 2
			if isBuilderAgent(t.agent.ID) {
				minSteps = 1
			}
			if len(steps) < minSteps {
				pr.planSetRejects++
				pr.forceNoThinkAfterReject = true
				return "", fmt.Errorf("plan_set requires at least %d step(s) (got %d). For a single tool call, invoke the tool directly inline — plan_set's planner+worker+synthesis overhead is only worth it for genuinely multi-step work", minSteps, len(steps))
			}
			// Reject vacuous plans — steps whose intent is "just
			// respond" or "summarize and reply" with no actual work.
			// These show up when the LLM wraps a respond_directly in
			// plan_set ceremony instead of calling respond_directly
			// inline. The whole point of plan_set is to gather info
			// across multiple workers; if every step is a synthesis
			// step, the plan adds no value.
			if vacuous := looksLikeVacuousPlan(steps); vacuous != "" {
				pr.planSetRejects++
				pr.forceNoThinkAfterReject = true
				return "", fmt.Errorf("plan_set rejected: %s. For a turn that needs no tool work, just write the answer as your reply text — there is no reply tool. plan_set is for genuinely multi-step research / decomposition", vacuous)
			}
			pr.capturedSteps = steps
			pr.cancelOrch()
			return fmt.Sprintf("Plan committed (%d step%s); the worker pipeline will now execute it.", len(pr.capturedSteps), plural(len(pr.capturedSteps))), nil
		},
	}
}

func (pr *planRun) askUserToolDef() AgentToolDef {
	t := pr.t
	return AgentToolDef{
		Tool: Tool{
			Name:        "ask_user",
			Description: "Pause and ask the user a clarifying question. Use whenever GUESSING is the alternative — not when SEARCHING is: 2+ plausible matches you'd be picking between arbitrarily, a choice between meaningfully different approaches, personal info only they have, or an ambiguity no tool could resolve. Don't ask for what you could look up. **DEFAULT TO `options`** whenever the answer space is bounded — one tap beats typing — and never write the choices into the question TEXT instead; without `options` this renders as plain chat text, no card and no buttons. For multi-step builds, pass `plan` to paint a checklist card above the question.",
			Parameters: map[string]ToolParam{
				"question": {
					Type:        "string",
					Description: "Singular. The question to ask the user, in plain text. Field name is 'question' (NOT 'questions'). If you need to ask multiple things, you can include several questions in this one string, but multi-step clarifications work better via ask_user_form.",
				},
				"options": {
					Type:        "array",
					Description: "**STRONGLY PREFERRED whenever the answer has natural choices.** Array of STRINGS, one label each — options=[\"yes\", \"edit\", \"no\"] — not a count, not a number, not a JSON-encoded string. Renders radios (checkboxes when multi=true) plus a free-text fallback. Labels 1-4 words, 8 max. Omit only for genuinely open-ended typed answers.",
					Items:       &ToolParam{Type: "string"},
				},
				"multi": {
					Type:        "boolean",
					Description: "When true (and options is non-empty), the user can select multiple options. When false, single-select (radios). Default false. Ignored when options is empty.",
				},
				"plan": {
					Type:        "array",
					Description: "Optional build-plan steps, rendered as a checklist above the question. Array of OBJECTS with 'title' and optional 'detail' — e.g. [{\"title\":\"Create agent shell\",\"detail\":\"create_agent\"},{\"title\":\"Add search tool\",\"detail\":\"add_tool(mode=api)\"}]. Not a count, not a list of numbers, not a string. Later authoring calls flip rows to ✓.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":  {Type: "string", Description: "Required. One-line step title shown in the checklist."},
							"detail": {Type: "string", Description: "Optional one-line detail under the title (tool name + key args)."},
						},
						Required: []string{"title"},
					},
				},
			},
			Required: []string{"question"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			pr.capturedQuest = strings.TrimSpace(stringArg(args, "question"))
			// Defensive: smaller LLMs occasionally typo "questions"
			// (plural) for "question". Accept either so the call
			// doesn't lose its primary content.
			if pr.capturedQuest == "" {
				pr.capturedQuest = strings.TrimSpace(stringArg(args, "questions"))
			}
			// Same alias tolerance as a form step's choices: options under a
			// near-miss key are not "no options", and reading only the
			// documented one turns a multiple-choice question into a bare
			// prose one with the choices silently gone.
			pr.capturedOptions = formStepOptions(args)
			if v, ok := args["multi"].(bool); ok {
				pr.capturedMulti = v
			}
			// If plan is provided, set up the build-plan state + emit
			// the orchestrate_plan SSE block before the ask card lands.
			// Lets Builder make ONE tool call at Phase 2 end instead of
			// two (present_build_plan + ask_user) — the failure mode
			// where Builder skipped present_build_plan and then
			// mark_step_done called against nothing.
			if raw, ok := args["plan"]; ok && raw != nil {
				if planSteps := buildPlanStepsFromArg(raw); len(planSteps) > 0 && t.session != nil {
					t.session.BuildPlan = &BuildPlanState{
						ID:    "build-plan-" + t.session.ID,
						Steps: planSteps,
					}
					emitBuildPlanBlock(t.sse, t.session.BuildPlan)
					// Same execution-budget grant present_build_plan gives:
					// this path is the canonical one-call Phase-2 shape, and
					// without the grant the build competes with whatever
					// exploration already spent the round cap.
					if grant := t.currentRound + buildPlanRoundsPerStep*len(planSteps); grant > t.planBudgetCap {
						t.planBudgetCap = grant
						Log("[orchestrate.build_plan] plan presented via ask_user: %d step(s), round budget lifted to %d (at round %d)",
							len(planSteps), t.planBudgetCap, t.currentRound)
					}
				}
			}
			pr.cancelOrch()
			return "Question relayed to the user; the framework will wait for their reply.", nil
		},
	}
}

func (pr *planRun) askUserFormToolDef() AgentToolDef {
	t := pr.t
	return AgentToolDef{
		Tool: Tool{
			Name:        "ask_user_form",
			Description: "Pause and collect SEVERAL pieces of info from the user in one pass. Two shapes, depending on whether you need CHOICES or typed ENTRY:\n• CHOICES: each step has discrete options (e.g. language + deployment target + timeline) — the user clicks through them one at a time. PREFER this over a single ask_user with a numbered list in the question text.\n• ENTRY FIELDS: give a step a type (\"text\", \"number\", \"textarea\", \"select\", \"password\") when the user must TYPE a specific value — an API base URL, a key, a count, an endpoint. Any step with a type turns the whole thing into a single FORM: every field shows at once with one Submit, instead of a step-through. Mix freely — a select field with options plus text/number fields. For ONE question, use ask_user instead.",
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: "Ordered list of steps. A CHOICE step is {question, options?, multi?}; an ENTRY-FIELD step is {question, type, placeholder?, options? (for select)}. Keep it short; 2-6 steps is the sweet spot. As soon as ANY step has a type, the client renders all steps at once as a form.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"question": {Type: "string", Description: "The question or field LABEL shown for this step."},
							"type": {
								Type:        "string",
								Enum:        []string{"text", "number", "textarea", "select", "password"},
								Description: "Optional. Set this to make the step a typed ENTRY field: \"text\" (one line), \"number\", \"textarea\" (multi-line), \"select\" (dropdown built from options), or \"password\" (masked, for secrets/keys). Omit for a plain choice/open step. Any typed step switches the form to all-fields-at-once with one Submit.",
							},
							"options": {
								Type:        "array",
								Description: "Choices for this step. For a choice step the user sees checkboxes (multi) or radios (single) plus a free-text input; for a type=select step these are the dropdown values. Array of strings.",
								Items:       &ToolParam{Type: "string"},
							},
							"placeholder": {Type: "string", Description: "Optional hint text inside an entry field (type=text/number/textarea/password), e.g. \"https://api.example.com\"."},
							"multi":       {Type: "boolean", Description: "Choice steps only: when true (and options is non-empty), multiple options can be picked. Default false."},
						},
						Required: []string{"question"},
					},
				},
			},
			Required: []string{"steps"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			raw, _ := args["steps"]
			// Smaller models wrap the array in fallback shapes: a
			// JSON-encoded string, or a bare single step object. Coerce
			// both so the form renders instead of arriving stepless.
			if s, ok := raw.(string); ok {
				var arr []any
				if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &arr); err == nil {
					raw = arr
				} else {
					var one map[string]any
					if uerr := json.Unmarshal([]byte(strings.TrimSpace(s)), &one); uerr == nil {
						raw = []any{one}
					}
				}
			}
			if m, ok := raw.(map[string]any); ok {
				raw = []any{m}
			}
			var steps []map[string]any
			switch v := raw.(type) {
			case []any:
				for _, x := range v {
					if m, ok := x.(map[string]any); ok {
						step, warn := normalizeFormStep(m)
						if warn != "" {
							t.turnDiag("form-step-degraded", warn)
						}
						steps = append(steps, step)
					} else {
						// Diagnostic breadcrumb — a discarded step used to
						// vanish without trace, making "the form is missing a
						// question" impossible to attribute afterward.
						Debug("[orchestrate.ask_form] discarding non-object step: %.200v", x)
						t.turnDiag("form-step-discarded", fmt.Sprintf("A form step the agent authored was discarded (not an object): %.200v", x))
					}
				}
			case []map[string]any:
				for _, m := range v {
					step, warn := normalizeFormStep(m)
					if warn != "" {
						t.turnDiag("form-step-degraded", warn)
					}
					steps = append(steps, step)
				}
			default:
				Debug("[orchestrate.ask_form] steps arg in unusable shape %T: %.300v", raw, raw)
				t.turnDiag("form-steps-unusable", fmt.Sprintf("The agent's form arrived in an unusable shape (%T) and rendered without questions.", raw))
			}
			pr.capturedFormSteps = steps
			pr.cancelOrch()
			return fmt.Sprintf("Form relayed to the user (%d step%s); the framework will wait for their reply.", len(steps), plural(len(steps))), nil
		},
	}
}

// catalogState is what buildCatalog's phases hand each other.
type catalogState struct {
	catalogStart time.Time
	workerTools  []AgentToolDef
	workerNames  []string
	err          error
	controlTools []AgentToolDef
	// Three orthogonal layers; each gates its own tool group:
	//
	//   - Knowledge (uploaded files, read-only): knowledge_search —
	//     always available; the layer is harmless when the corpus is
	//     empty.
	//
	//   - Explicit Memory (always-in-prompt facts): store_fact +
	//     forget_fact → gated by explicitOff (DisableExplicit). The
	//     framing of WHAT goes in here is shaped by MemoryMode
	//     (agent = lessons; chatbot = user-personalization + notes).
	//
	//   - Reference Memory (vector-grown derived chunks): memory_save +
	//     memory_search + memory_forget → gated by inferredOff
	//     (DisableInferred OR per-turn Clean toggle).
	// Knowledge tools (knowledge_search + fetch_knowledge_doc) only
	// surface when the agent has something to retrieve — its own
	// AttachedCollections, IngestAttachments, an active skill carrying
	// collections, or deployment-default collections (the open-pool
	// case when AttachedCollections is empty). Without this gate,
	// agents like Builder that have no corpus see the tools in their
	// catalog, reach for them anyway, and hallucinate doc_ids that
	// the handler then has to refuse with "not found" — burns rounds
	// for zero value.
	// Framework conversational tools — the always-on set shared VERBATIM with
	// the channel/dispatch surface via frameworkConversationalTools(): knowledge
	// (introspect always; search/fetch when there's a corpus), find_tools,
	// send_status, stay_silent/keep_going, load_tool, skills, the memory layers
	// (Reference + Explicit + Graph), and cortex deliverables.
	// Single source of truth so the web and channel catalogs can't drift.
	knowTools         []AgentToolDef
	directCustomTools []AgentToolDef
	lazyCustomPrompt  string
	allNames          []string
}

// buildCatalog assembles the tools the orchestrator round can call: the
// worker tools, the control tools, the framework's conversational set, the
// custom and attached tools, then the log line and prompt notices. Each
// phase is a method on the run's catalogState; the first that fails stops.
func (pr *planRun) buildCatalog() error {
	for _, phase := range []func() error{
		pr.catalogWorkerTools,
		pr.catalogControlTools,
		pr.catalogKnowTools,
		pr.catalogAssemble,
		pr.catalogLog,
	} {
		if err := phase(); err != nil {
			return err
		}
	}
	return nil
}

func (pr *planRun) catalogWorkerTools() error {
	t := pr.t
	pr.cat.catalogStart = time.Now()
	Debug("[orchestrate.orch] runPlan: resolving worker tools")
	// forOrchestrator=true — this is Builder's own chat-orchestrator
	// round, NOT a worker step. The Builder branch in resolveWorkerTools
	// returns the LEAN authoring catalog so Builder must decompose via
	// plan_set instead of reaching for create_agent / add_tool / tool_def
	// inline.
	pr.cat.workerTools, pr.cat.workerNames, pr.cat.err = t.resolveWorkerTools(pr.sess, true)
	if pr.cat.err != nil {
		Debug("[orchestrate.orch] runPlan: resolveWorkerTools error: %v", pr.cat.err)
		return fmt.Errorf("resolve tools: %w", pr.cat.err)
	}
	Debug("[orchestrate.orch] runPlan: resolved %d worker tools", len(pr.cat.workerTools))
	// No per-turn classifier-trim. Every allowed tool on the agent
	// is shipped to the LLM verbatim — the model's own attention
	// disambiguates better than a cosine-similarity classifier ever
	// did, and a stateless trim broke follow-ups like "get me another"
	// (turn 1 used get_meme; turn 2's bare text scored a different tool
	// higher and the LLM never saw get_meme). find_tools is still
	// registered as the escape hatch for any future massive-catalog
	// case (a 200-tool MCP plug-in); today's agents fit fine
	// without trimming.
	pr.cat.workerTools, pr.cat.workerNames = filterToolAuthoringWithoutFocus(pr.cat.workerTools, pr.cat.workerNames, t.session)
	t.gateAgentCRUDTools(pr.cat.workerTools)
	t.wrapToolsForActivity(pr.sess, pr.cat.workerTools, t.agent)
	return nil
}

func (pr *planRun) catalogControlTools() error {
	t := pr.t
	// Wrap control tools too so they emit cmd rows in the activity
	// pane (transparency: user sees "plan_set was called" / "ask_user
	// was called" alongside the rest of the orchestrator's tool use).
	// Control tools have no attachment surface — pass nil sess.
	// respond_directly was removed: it was an OPTIONAL terminator whose
	// effect is identical to the implicit path (stream the reply text and
	// end the round with no tool call, handled below where resp.Content is
	// non-empty). Offering it alongside "just reply as text" invited the
	// model to do BOTH — stream the answer AND call respond_directly with
	// the same text — producing a double reply. Workers never had it
	// (runWorkerStep builds its own catalog), so this is lead-path only.
	pr.cat.controlTools = []AgentToolDef{pr.askUserToolDef(), pr.askUserFormToolDef()}
	// One plan mechanism per agent. plan_set fans this turn out to fresh-context
	// workers and ends the round; a TRACKED plan (AgentRecord.WorkPlan) is a
	// durable checklist the agent works itself across turns. Offering both would
	// leave the model deciding which kind of plan it meant on exactly the turns
	// that are already hard. The framework's plan_set prompt block is gated on
	// the same flag, so the persona cannot promise a tool that is not there.
	if planTools := t.workPlanTools(); len(planTools) > 0 {
		pr.cat.controlTools = append(pr.cat.controlTools, planTools...)
		t.restoreWorkPlanCard()
	} else {
		pr.cat.controlTools = append(pr.cat.controlTools, pr.planSetToolDef())
	}
	t.wrapToolsForActivity(nil, pr.cat.controlTools, t.agent)
	return nil
}

func (pr *planRun) catalogKnowTools() error {
	t := pr.t
	pr.cat.knowTools = append(pr.cat.knowTools, t.frameworkConversationalTools(pr.sess)...)
	// Host-app tools (e.g. a workbench's co-author "add_section") — supplied by
	// the app that dispatched this turn, callable directly by the orchestrator.
	if len(t.appTools) > 0 {
		pr.cat.knowTools = append(pr.cat.knowTools, t.appTools...)
	}
	// compact_context — LLM-driven context management. Lets the model
	// proactively discard the bodies of EARLIER tool results it has
	// consumed and no longer needs (a smoke-test report, a big fetch, a
	// verbose listing), instead of carrying them until the automatic
	// budget compaction kicks in. Framework meta-tool: always available,
	// not classifier-trimmed (it's in knowTools).
	pr.cat.knowTools = append(pr.cat.knowTools, AgentToolDef{
		Tool: Tool{
			Name:        "compact_context",
			Description: "Free up context: discard the bodies of EARLIER tool results you've already read and no longer need — e.g. after judging a long smoke-test report, a big page fetch, or a verbose listing. Their bodies are replaced with a short marker (re-run the tool if you need the data again); the most recent result and the whole conversation stay intact. Call this at a natural breakpoint when you're carrying long tool outputs you're done with, to keep a long session from bloating its context. No arguments.",
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			pr.compactRequested = true
			return "Acknowledged — earlier verbose tool-result bodies you've consumed will be released on your next step. Continue with your next action.", nil
		},
	})
	// show_html — the viewer/previewer pane (preview_tool.go). Framework
	// display tool, always available like compact_context: emits a generic
	// html_artifact block the client renders in a slide-in side pane
	// (authored HTML sandboxed, or a same-origin page preview by url), and
	// upserts the artifact onto the session (UIBlocks) so it survives
	// reload. No Caps — display-only, so it also survives private mode.
	pr.cat.knowTools = append(pr.cat.knowTools, t.showHTMLToolDef())
	// show_link — the navigation counterpart (preview_tool.go): a clickable
	// link card in the transcript for "go here" moments — the app Builder
	// just created, a settings page, an external console. Display-only
	// like show_html, so it's ungated and always available.
	pr.cat.knowTools = append(pr.cat.knowTools, t.showLinkToolDef())
	// (skills + the memory layers now come from frameworkConversationalTools
	// above; dispatch_to_worker stays unmounted — the LLM wasn't reaching for it
	// reliably and the surface area diluted agent dispatch. Skills still
	// auto-activate inline via the classifier; cross-agent work uses
	// agents(action="run", ...).)
	// Build-plan UI — present_build_plan paints the visible
	// checklist when Builder reaches end of Phase 2; mark_step_done
	// updates it as each Phase 4 tool call completes. Builder-only
	// — these tools only make sense in the structured authoring
	// rhythm Builder follows. Other agents (Chat, Research, etc.)
	// don't need a build-plan card. Reduces their tool surface
	// without touching Builder's authoring flow.
	// The appeal channel, only for agents that actually have a rule worth
	// appealing. Mounting it everywhere would put a tool about guardrails in
	// front of agents that have none, which is both noise and a hint.
	if agentCanAuthor(t.agent) {
		pr.cat.knowTools = append(pr.cat.knowTools,
			t.presentBuildPlanToolDef(),
			t.markStepInProgressToolDef(),
			t.markStepDoneToolDef(),
			t.markStepBlockedToolDef(),
			t.reviseBuildPlanToolDef(),
			t.reportBuildGapsToolDef(),
			// pipeline (create / update / run / list / get / delete) —
			// declarative multi-stage workflow authoring + management.
			// Builder-only: authoring pipelines is Builder's job, same as
			// create_agent. OTHER agents RUN pipelines via their attached
			// run_<pipeline> tools (buildAttachedPipelineToolDefs), not
			// this management surface — that keeps their catalog lean and
			// centralizes authoring (no more pipeline-tool clutter +
			// LLM oscillation on general agents).
			t.pipelineGroupedToolDef(),
			// machine (create / update / list / get / delete) — author
			// session-resident phase machines. Builder-only for the same
			// reason as pipeline: shaping how an agent's conversation
			// runs is authoring work. Other agents don't manage machines;
			// they RUN the one they're pointed at, and leave it with
			// change_phase.
			t.machineGroupedToolDef(),
			// app_def (create / update / list / get / delete) — author
			// data-driven gohort APPS (real in-dashboard surfaces served
			// by customapps at /apps/<slug>/). Builder-only, same
			// rationale as pipeline: composing an app is authoring work.
			// This is what lets Builder answer "build me an app" with an
			// actual gohort app instead of a standalone HTML file.
			t.appDefToolDef(),
		)
	}
	pr.cat.knowTools = append(pr.cat.knowTools,
		// agents (list / get / run) — single entry point for agent
		// operations. Replaces the legacy trio (list_agents,
		// get_agent, dispatch_to_agent) for new code. The legacy
		// tools stay registered for backward compat with agent
		// records that explicitly name them in AllowedTools.
		//
		// Builder gets the READ-ONLY variant (list / get only) —
		// its job is authoring/composition, not delegation. With
		// run enabled Builder reaches for Chat ("ask Chat about
		// X") and Chat's authoring-intent routing sends control
		// right back into Builder, an A→B→A cycle the chain guard
		// catches only after the round-trip already happened.
		// Builder's actual delegation surface is plan_set workers,
		// which spawn with their own catalog (web_search /
		// fetch_url for any specialist-knowledge sub-task).
		// Builder gets run too — needed for smoke-testing the just-
		// created agent. The self-dispatch guard (target.ID == t.agent.ID)
		// and dispatchChain cycle detection in agentsRunAction prevent
		// Builder→Builder and Builder→Chat→Builder loops; everything
		// else is fair game.
		t.agentsGroupedToolDef(true),
	)
	// Explorer mode — LLM-triggered round-budget lift for API-mapping
	// tasks. Mounted only when the agent can actually use it: for
	// everyone else the ~450-word description was pure prefill cost in
	// front of a handler that refuses.
	if t.agent.AllowExplorer {
		pr.cat.knowTools = append(pr.cat.knowTools, t.enterExplorerModeToolDef())
	}
	// Recurring per-session interval tasks — but NOT for Fleet agents. A Fleet
	// agent schedules recurring work through create_standing_agent (real cron
	// timing, and it surfaces in the Enabled-agents console where the user can
	// pause/cancel it); the generic per-session "recurring" scheduler bypasses
	// the fleet and stays invisible to the console, so we keep it off them.
	// (The earlier dropToolsByName in resolveWorkerTools was dead — recurring
	// is added HERE, after that assembly, so it was never in that list.)
	if !t.agent.Fleet {
		pr.cat.knowTools = append(pr.cat.knowTools, t.recurringToolDef())
	}
	// Session spin-off — web chat only (this assembly path is never used by
	// channel relays / dispatch / scheduled fires, and the handler's sse
	// guard backstops that): the agent can open a fresh titled session with
	// a seeded handoff note and offer the user a link to continue there.
	pr.cat.knowTools = append(pr.cat.knowTools, t.openSessionToolDef())
	// Tool authoring: any agent can author its OWN tools via tool_def (the way
	// phantom always could before it was centralized). Builder already has
	// tool_def via its authoring catalog, so don't double it. AGENT and
	// PIPELINE authoring still route to Builder; only tools are self-serve.
	if !isBuilderAgent(t.agent.ID) {
		pr.cat.knowTools = append(pr.cat.knowTools, ChatToolToAgentToolDefWithSession(temptool.BuildToolDef(), pr.sess))
		// Tool authoring stays self-serve, but CREDENTIAL authoring is Builder's
		// job: the five credential tools (draft_oauth_credential /
		// draft_api_credential / update_api_credential / store_credential_secret
		// / check_credential) used to ride along here for every agent — 5 schemas
		// + the 4K credentialFirstGuidance block on every single turn — and were
		// essentially never used outside Builder (tool-count audit). An agent
		// whose api-mode tool_def needs a credential that doesn't exist gets a
		// clear error and points the user at Builder, which carries the full
		// credential suite + doctrine in its authoring catalog.
	}
	// create_pipeline_tool is NOT added to the catalog — add_tool with
	// mode="pipeline" covers the same use case via a unified surface.
	// Having both visible caused pattern-match loops (LLM oscillated
	// between the two). The handler stays in the codebase for
	// backward-compat with any future agent that explicitly opts in
	// via AllowedTools, but the closure-bound default registration is
	// removed.
	t.wrapToolsForActivity(pr.sess, pr.cat.knowTools, t.agent)
	return nil
}

func (pr *planRun) catalogAssemble() error {
	t := pr.t
	// Persistent temp tools also flow into the static set so the
	// rewriter can collapse them when they're members of an admin-
	// curated group. Otherwise vapi-style user-defined tools sit at
	// the top of the catalog despite being grouped, and the LLM
	// picks them at random instead of going through the toolbox's
	// expand_tool_group path. Snapshot the names so DynamicTools
	// below knows to skip them (and only surface NEW temp tools
	// created mid-turn).
	Debug("[orchestrate.orch] runPlan: building temp tool defs")
	// Custom (temp) tools — the unbounded, per-user, often-verbose category.
	// Resolved via the shared setupCustomTools so the web and channel/dispatch
	// surfaces present them identically (zero-arg → direct, has-args → load_tool
	// + prompt section). The staticTempToolNames snapshot it populates is what
	// the DynamicTools feed below uses to surface only NEW mid-turn temp tools.
	// Tier-1 tool elevation matches against the latest user message on the
	// interactive surface (dispatch/scheduled surfaces stamp IntentText on
	// their sessions instead).
	if t.intentText == "" {
		for i := len(pr.msgs) - 1; i >= 0; i-- {
			if pr.msgs[i].Role == "user" && strings.TrimSpace(pr.msgs[i].Content) != "" {
				t.intentText = pr.msgs[i].Content
				break
			}
		}
	}
	pr.cat.directCustomTools, pr.cat.lazyCustomPrompt = t.setupCustomTools(pr.sess)
	pr.sys += pr.cat.lazyCustomPrompt
	// The authoring index, when the catalog was deferred. Set during
	// resolveWorkerTools, which ran before this point.
	pr.sys += t.authoringLazyPrompt
	pr.allTools = append(pr.cat.controlTools, pr.cat.knowTools...)
	pr.allTools = append(pr.allTools, pr.cat.workerTools...)
	pr.allTools = append(pr.allTools, pr.cat.directCustomTools...)
	// Attached pipelines — one callable tool per pipeline bolted onto
	// this agent (AgentRecord.AttachedPipelines). Curated + tiny schema,
	// so direct (not lazy load_tool). Wrap for activity so a pipeline run
	// shows its tool_call / result in the convo + activity pane.
	if attachedPipes := t.buildAttachedPipelineToolDefs(); len(attachedPipes) > 0 {
		t.wrapToolsForActivity(pr.sess, attachedPipes, t.agent)
		pr.allTools = append(pr.allTools, attachedPipes...)
		t.noteAttachedTools(attachedPipes)
		Log("[orchestrate.tools] surfaced %d attached pipeline tool(s) for agent=%s", len(attachedPipes), t.agent.ID)
	}
	// Attached reference sources (servitor systems, workspaces, connected doc
	// spaces). Same treatment as pipelines: curated, few, surfaced directly.
	if attachedSrc := t.buildAttachedSourceToolDefs(pr.sess); len(attachedSrc) > 0 {
		t.wrapToolsForActivity(pr.sess, attachedSrc, t.agent)
		pr.allTools = append(pr.allTools, attachedSrc...)
		t.noteAttachedTools(attachedSrc)
		Log("[orchestrate.tools] surfaced %d attached source tool(s) for agent=%s", len(attachedSrc), t.agent.ID)
	}
	// Private-mode backstop for dynamically built AgentToolDefs (the
	// `agents` grouped tool, temp tools, source-hooked tools, etc.).
	// resolveWorkerTools only filters tools that exist in the global
	// ChatTool registry; per-turn AgentToolDefs are constructed here
	// and never registered, so they slip past that filter even when
	// they declare CapNetwork in Tool.Caps. Without this pass, a
	// Private turn could still dispatch into a sub-agent (via `agents`)
	// whose own tools call the network — leaking the turn. Drop any
	// AgentToolDef whose declared Caps include CapNetwork.
	if t.privateMode {
		filtered := pr.allTools[:0]
		dropped := []string{}
		for _, td := range pr.allTools {
			hasNet := false
			for _, c := range td.Tool.Caps {
				if c == CapNetwork {
					hasNet = true
					break
				}
			}
			if hasNet {
				dropped = append(dropped, td.Tool.Name)
				continue
			}
			filtered = append(filtered, td)
		}
		pr.allTools = filtered
		if len(dropped) > 0 {
			Log("[orchestrate.orch] private mode dropped %d network-capable dynamic tool(s): %v", len(dropped), dropped)
		}
	}
	// Says "assembled", not "rewriting": the runtime group rewriter this line
	// used to announce was retired, and the sentence outlived it. A stale verb
	// on a line that also prints the catalog SIZE reads as a size-triggered
	// rewrite, which is a mechanism that no longer exists — and it is the first
	// thing anyone greps when tools go missing between turns.
	Debug("[orchestrate.orch] runPlan: assembled catalog of %d tools", len(pr.allTools))
	// Building the catalog is the other phase big enough to be felt: a hundred
	// tools resolved, temp tools loaded from the store, schemas built.
	t.prep.mark("tools", time.Since(pr.cat.catalogStart))
	return nil
}

func (pr *planRun) catalogLog() error {
	t := pr.t
	// (Runtime tool-group rewriting retired. The per-turn
	// classifier-trim that preceded this block is also gone — every
	// allowed tool ships to the LLM verbatim; the model's attention
	// disambiguates better than a cosine-similarity classifier did.
	// find_tools stays registered as the explicit-search fallback for
	// any future massive-catalog case. ToolGroup records still exist
	// as admin-side organizational metadata — they just don't gate
	// the runtime catalog anymore.)

	// Full-surface log every turn — the ASSEMBLED catalog. If a follow-up
	// turn lacks a tool the first turn had, this line will show the
	// regression. Includes both control and worker tools so a bug that drops
	// one but not the other is visible immediately.
	//
	// One filter still runs downstream of this: a machine phase narrows the
	// catalog to the tools that phase allows (see mach.narrowCatalog). On
	// those turns this line reports the wider pre-phase set, and the
	// tools_to_llm_effective line printed there is the authoritative one.
	pr.cat.allNames = make([]string, 0, len(pr.allTools))
	for _, td := range pr.allTools {
		pr.cat.allNames = append(pr.cat.allNames, td.Tool.Name)
	}
	pr.sessID = ""
	if t.session != nil {
		pr.sessID = t.session.ID
	}
	Log("[orchestrate.orch] session=%s msgs=%d tools_to_llm[%d]=%v (worker_subset=%v private=%v)",
		pr.sessID, len(pr.msgs), len(pr.allTools), pr.cat.allNames, pr.cat.workerNames, t.privateMode)
	// The three blocks that DESCRIBE the catalog are appended by
	// appendCatalogPromptBlocks, at the end of prepareMessages. They used to
	// land here, and both halves of that were wrong: they were built before
	// the machine-phase narrowing that decides the final catalog, and the
	// tool roster was built from the WORKER SUBSET while the request carried
	// the whole thing. See appendCatalogPromptBlocks.
	return nil
}

// appendCatalogPromptBlocks adds the prompt blocks that describe the catalog,
// once the catalog has stopped changing.
//
// The roster block asserts "every tool named here is live and callable this
// turn". A prompt that says that about the wrong list does not merely omit a
// tool, it OVERRIDES the schemas in the same request: the model reads the
// sentence, not the payload.
//
// Observed live, and it cost days. The roster was built from
// pr.cat.workerTools, nine tools, while thirty-five schemas shipped. A support
// agent read the nine, reported accurately and repeatedly that knowledge_search
// was not in its callable set, and refused to call it. When a guardrail forced
// the call it SUCCEEDED and returned real documentation, whereupon the agent
// told the user that result was not genuine and should not be trusted, which is
// the correct inference from a false premise and worse than the silence it
// replaced. The line immediately below the bug already used pr.allTools, which
// is what made it read as a slip rather than a decision.
//
// Called after the phase narrowing in prepareMessages rather than during
// catalog assembly, because a machine phase can still remove tools after the
// catalog is built. Listing a tool the phase just took away teaches the model
// to call it and be refused; the same false-roster failure pointed the other
// way.
func (pr *planRun) appendCatalogPromptBlocks() {
	if len(pr.allTools) > 0 {
		pr.sys += "\n\n" + buildToolUseDirective(pr.allTools)
	}
	pr.sys += noWebAccessNotice(pr.allTools)
	// Per-tool prompt fragments (opt-in via AgentToolDef.Prompt). Kept
	// adjacent to the roster so the model reads per-tool usage notes
	// alongside the tool list.
	if frag := RenderToolPromptFragments(pr.allTools); frag != "" {
		pr.sys += "\n\n" + frag
	}
}

func (pr *planRun) resolveRouting() {
	t := pr.t
	pr.stopKeepalive = startKeepalive(t.sse)
	// Tier and reasoning come off ONE resolution (turnRouting), so a machine
	// phase that pins a tier also picks up that tier's thinking preference.
	pr.tierPin, pr.routeKey = t.turnRouting()
	pr.think = true
	if p := RouteThink(pr.routeKey); p != nil {
		pr.think = *p
	}
	// Per-agent override wins over the route default — the author may
	// have decided this agent always reasons (planners, synthesizers)
	// or never reasons (fast specialists). Empty Think means "auto":
	// keep the route default we just picked up.
	switch t.agent.Think {
	case "on":
		pr.think = true
	case "off":
		pr.think = false
	}
	// A machine phase is the most specific setting in that chain, so it
	// goes last: a cheap classify phase can turn reasoning off inside an
	// agent that otherwise always reasons.
	pr.think = pr.mach.Think(pr.think)
	if pr.mach.on {
		// Only while a machine is running, because that is when somebody asks
		// "why did my lead step run on the worker" and needs one line rather
		// than an inference across three settings. An ordinary turn's log is
		// not made longer for a question nobody is asking of it.
		Log("[orchestrate.routing] agent=%s step=%q model=%q think=%q → route=%s pin=%v reasoning=%v",
			t.agent.ID, pr.mach.Name(), pr.mach.phase.Model, pr.mach.phase.Think, pr.routeKey, pr.tierPin, pr.think)
	}
}

func (pr *planRun) paintHeldBubble(id, text string) {
	t := pr.t
	if !pr.holdStream || id == "" || text == "" {
		return
	}
	if pr.streamFreshBubble {
		t.sse.Send(map[string]any{"kind": "message", "role": "assistant", "id": id, "text": ""})
	}
	t.sse.Send(map[string]any{"kind": "chunk", "id": id, "text": text})
}

func (pr *planRun) streamHandler(chunk string) {
	t := pr.t
	if chunk == "" {
		return
	}
	// If a tool fired this round before any text streamed, it
	// already lazy-materialized a bubble via ensureBubbleForTool.
	// Adopt that bubble for the streamed text rather than
	// creating a second one.
	if pr.streamMsgID == "" {
		if existing := t.getCurrentMsgID(); existing != "" {
			pr.streamMsgID = existing
			pr.streamFreshBubble = false // already on screen (tool made it)
		} else {
			pr.streamMsgID = fmt.Sprintf("orch-%d", time.Now().UnixNano())
			pr.streamFreshBubble = true
			if !pr.holdStream {
				t.sse.Send(map[string]any{
					"kind": "message",
					"role": "assistant",
					"id":   pr.streamMsgID,
					"text": "",
				})
			}
			t.setCurrentMsgID(pr.streamMsgID)
		}
	}
	pr.streamedBuf.WriteString(chunk)
	if !pr.holdStream {
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   pr.streamMsgID,
			"text": chunk,
		})
	}
}

// Telemetry record fires at the top of the onStep callback; the
// telem var is declared at function entry and summarized in the
// deferred block above.

func (pr *planRun) onStepHandler(info StepInfo) {
	t := pr.t
	pr.telem.record(info)
	pr.produced.note(info.ToolCalls)
	// Tool-only round with no text and no lazy-bubble: nothing
	// to finalize, nothing visible. (Tool calls in that round
	// already created their own bubble via ensureBubbleForTool;
	// streamMsgID picks that up via getCurrentMsgID below.)
	id := pr.streamMsgID
	if id == "" {
		id = t.getCurrentMsgID()
	}
	if id == "" {
		return
	}
	// Models on llama.cpp sometimes emit <tool_call><function=…>
	// XML as TEXT instead of native tool_calls — the agent loop
	// catches it and dispatches via ParseTextToolCall, but the
	// markup already streamed to the user's bubble. Strip on
	// round close so what they're left looking at is clean
	// narration, not raw XML.
	raw := pr.streamedBuf.String()
	cleaned := strings.TrimSpace(StripToolCallMarkup(raw))
	if cleaned != strings.TrimSpace(raw) {
		t.sse.Send(map[string]any{
			"kind": "chunk_replace",
			"id":   id,
			"text": cleaned,
		})
	}
	// Transient narration vs the answer. A round that ALSO calls
	// tools (info.Done == false) is not the answer round — the model
	// will continue and reply in a later, tool-free round. Any text
	// it streamed here was live "working" narration, so we clear it
	// from the bubble and do NOT finalize/persist it as an answer
	// card. This is the deterministic half of the double-emit fix:
	// without it, a model that writes a full answer in a tool round
	// AND again in the final round produces two answer cards (we
	// faithfully render both). Keep the bubble open so this round's
	// tool pills stay and the next round folds in. Remember the text
	// in case the model front-loaded its answer into a tool round
	// and the final round comes back empty.
	if !info.Done {
		// A round that calls tools is not the answer round, but the text
		// the model streamed here is its lead-in — "Let me grab that
		// video." — written in its own voice (roundShapePreamble asks for
		// exactly one such sentence before a tool call). FINALIZE it as
		// its own message bubble, exactly like a normal streamed reply,
		// then close it so the next round opens a fresh bubble. No clear,
		// no separate status card — it reads as the assistant chatting as
		// it works, with no flicker. captureMidTurnBubble persists it
		// (with the tool calls it triggered) so a reload replays the same
		// transcript.
		//
		// Guard: prose longer than leadInMaxLen is a full ANSWER mis-
		// emitted before a tool, not a lead-in — finalizing it here AND
		// again in the final round would double the answer (the original
		// double-emit bug). Clear those instead; the loop's history still
		// holds the text so the model's final reply carries the answer.
		if cleaned == "" {
			pr.streamedBuf.Reset()
			return
		}
		// Builder presents a PLAN (intentionally long) before it acts —
		// exempt it from the length clear so the user actually sees what it
		// intends to do. The clear exists for ORDINARY agents that mis-emit a
		// full answer before a tool (and then repeat it at the end); Builder's
		// pre-tool prose is the plan, not a doubled answer.
		// A round whose only tools SHOW the user something is the one case
		// where long prose beside a call is correct rather than early: the
		// tool is the delivery and the prose is the explanation that goes
		// with it. Clearing it leaves a link with nothing said about it.
		if len(cleaned) > leadInMaxLen && !isBuilderAgent(t.agent.ID) && !presentationOnlyRound(info.ToolCalls) {
			t.sse.Send(map[string]any{"kind": "chunk_replace", "id": id, "text": ""})
			// Held, not dropped. The guard is betting the final round will say
			// this again; restoreWithheldLeadIn collects if it does not.
			pr.withheldLeadIn = cleaned
			t.turnDiag("lead-in-withheld", fmt.Sprintf(
				"%d characters the assistant wrote alongside a tool call were held back: prose that long before a tool is usually an answer written early and repeated at the end, which would show twice. It is restored after the reply if the final answer comes back much shorter.",
				len(cleaned)))
			pr.streamedBuf.Reset()
			return
		}
		pr.paintHeldBubble(id, cleaned) // held stream: paint the (now-cleared) lead-in
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.captureMidTurnBubble(cleaned)
		pr.streamMsgID = ""
		t.setCurrentMsgID("")
		pr.streamedBuf.Reset()
		return
	}
	// Final (tool-free) round — this round's text is the answer.
	// (Any text the model emitted in earlier tool rounds was already
	// settled as its own card above, so there's nothing to restore.)
	// Tool-only final round with no text: nothing to finalize — keep
	// the bubble open (subsequent tool pills fold in).
	if cleaned == "" {
		pr.streamedBuf.Reset()
		return
	}
	pr.paintHeldBubble(id, cleaned) // held stream: paint the answer now that pre_output has passed it
	t.sse.Send(map[string]any{"kind": "message_done", "id": id})
	pr.lastFinalizedID = id
	pr.lastFinalizedText = cleaned
	// Persist the answer so a reloaded session replays the same
	// bubble the user saw live. handleSend drains the buffer right
	// before appending the final assistant message.
	t.captureMidTurnBubble(cleaned)
	pr.streamMsgID = ""
	pr.streamedBuf.Reset()
	t.setCurrentMsgID("")
}

// settleRound finalizes whatever the CURRENT round already streamed into
// its own bubble, then opens the way for a fresh one — the agent loop calls
// it (via AgentLoopConfig.SettleRound) right before a correction guard
// re-prompts and continues. Without it, the rejected round's text is left
// in an open bubble and the retry round concatenates into it (the
// "…What API?Fair point…" double). Deliberately mirrors the onStep
// final-round finalize but OMITS the !info.Done length-clear: a correction
// retry is not guaranteed to repeat the earlier text, so clearing it could
// lose real content — finalize instead (lossless), and setting
// lastFinalizedText lets emitCapturedAsBubble suppress any echo the retry
// emits. No-op when nothing visible streamed (e.g. reasoning-collapse):
// the empty open bubble is left for the retry to adopt.

func (pr *planRun) settleRound() {
	t := pr.t
	id := pr.streamMsgID
	if id == "" {
		id = t.getCurrentMsgID()
	}
	if id == "" {
		pr.streamedBuf.Reset()
		return
	}
	raw := pr.streamedBuf.String()
	cleaned := strings.TrimSpace(StripToolCallMarkup(raw))
	if cleaned == "" {
		pr.streamedBuf.Reset()
		return
	}
	if cleaned != strings.TrimSpace(raw) {
		t.sse.Send(map[string]any{"kind": "chunk_replace", "id": id, "text": cleaned})
	}
	pr.paintHeldBubble(id, cleaned) // held stream: this settled round passed (non-guardrail correction) — show it
	t.sse.Send(map[string]any{"kind": "message_done", "id": id})
	pr.lastFinalizedID = id
	pr.lastFinalizedText = cleaned
	t.captureMidTurnBubble(cleaned)
	pr.streamMsgID = ""
	pr.streamedBuf.Reset()
	t.setCurrentMsgID("")
}

// retractRound DISCARDS the current round's streamed bubble instead of
// settling it — the agent loop calls it (via AgentLoopConfig.RetractRound)
// when a guardrail BLOCKS a round. Unlike settleRound it must NOT persist or
// deliver the text: it's the very content the rule protects. So it rewrites
// the open bubble to empty in the client (chunk_replace, same primitive the
// over-long lead-in clear uses) and drops the buffer WITHOUT
// captureMidTurnBubble — nothing reaches t.midTurnBubbles or sess.Messages.
// The retry opens a fresh bubble. (Tokens streamed live before the round
// closed already reached the client; chunk_replace clears them — a brief
// flicker is the residual cost of streaming ahead of the output check.)

func (pr *planRun) retractRound() {
	t := pr.t
	id := pr.streamMsgID
	if id == "" {
		id = t.getCurrentMsgID()
	}
	if id != "" {
		t.sse.Send(map[string]any{"kind": "chunk_replace", "id": id, "text": ""})
	}
	pr.streamMsgID = ""
	pr.streamedBuf.Reset()
	t.setCurrentMsgID("")
}

// presentationOnlyRound reports whether every tool this round called exists to
// SHOW the user something.
//
// Prose beside one of those is not an answer written too early, which is what
// the length guard is looking for. It is the explanation that belongs with the
// thing being shown, and the tool call IS the delivery, so there is no later
// round that would repeat it and no double to prevent.
func presentationOnlyRound(calls []ToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, c := range calls {
		switch c.Name {
		case "show_link", "show_html":
		default:
			return false
		}
	}
	return true
}

// restoreWithheldLeadIn puts back the prose the length guard took, on the turns
// where the bet it made did not pay.
//
// The guard clears a long tool-round reply on the reasoning that the model will
// say the same thing in its final round, since the loop's history still carries
// the text. Usually true. When it is not, everything the user was going to be
// told is gone and nothing anywhere says so. Observed on a turn that created a
// Jira issue, linked it, wrote 1109 characters about what it had done alongside
// show_link, and then finished with a 30-character "done": the work happened,
// the account of it did not survive, and the only trace was a Debug line about
// an unrelated dedup.
//
// Restores only when what the user ended up seeing is less than half of what
// was taken. A model that genuinely restated its answer clears that bar without
// trying, so the double-emit the guard exists to prevent stays prevented.
//
// The restored text lands after the short reply rather than in its original
// place, which reads slightly out of order. That is the price of only knowing
// the bet failed once the turn is over, and it beats the alternative.
func (pr *planRun) restoreWithheldLeadIn(question, shown string) {
	held := strings.TrimSpace(pr.withheldLeadIn)
	pr.withheldLeadIn = ""
	// A turn that ends by ASKING is not one whose answer went missing, and
	// dropping a paragraph in after the question would talk over it.
	if held == "" || strings.TrimSpace(question) != "" {
		return
	}
	visible := strings.TrimSpace(shown)
	if visible == "" {
		visible = strings.TrimSpace(pr.lastFinalizedText)
	}
	if len(visible)*2 >= len(held) {
		return // the final reply carried it, exactly as the guard assumed
	}
	Log("[orchestrate.orch] restoring %d withheld character(s): the final reply came back at %d", len(held), len(visible))
	pr.t.turnDiag("lead-in-restored", fmt.Sprintf(
		"%d characters written alongside a tool call were held back, and the final reply came back at %d, so the held text was restored below it rather than lost.",
		len(held), len(visible)))
	pr.emitBubble(held)
	pr.t.captureMidTurnBubble(held)
}

// emitCapturedAsBubble produces a new bubble for captured
// control-tool text (respond_directly's text, ask_user's
// question). Dedup keys on lastFinalizedText (the LAST shown
// bubble): skip ONLY when this exact text was just shown
// (stream-then-respond_directly with identical content). Two earlier
// guards both dropped real replies and were removed: lastRoundHadContent
// suppressed a 7k reply because the round streamed an 82-char preamble;
// the shownText substring match dropped a 338-char reply because it was
// a substring of a larger earlier block. Exact-match-last-bubble is the
// narrowest dedup that still catches the true duplicate case.
// emitBubble is the raw send, with NO dedup. Split out because an ASK must
// never be suppressed: see the ask_user path below.

func (pr *planRun) emitBubble(text string) {
	t := pr.t
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	id := fmt.Sprintf("orch-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   id,
		"text": text,
	})
	t.sse.Send(map[string]any{"kind": "message_done", "id": id})
	t.emitStats(id, pr.resp, pr.orchStart)
}

func (pr *planRun) emitCapturedAsBubble(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	// Suppress when the captured reply is a near-duplicate of the
	// LAST shown bubble — the stream-then-respond_directly /
	// stream-draft-then-revised-conclusion case. Near-duplicate
	// catches the "same analysis, different ending" pattern that
	// exact-match missed; it's still narrow enough not to drop a
	// short reply that coincidentally shares an opener with a
	// longer earlier block (LCP/short ratio < 0.6 keeps it).
	if last := strings.TrimSpace(pr.lastFinalizedText); last != "" && isNearDuplicate(trimmed, last) {
		Debug("[orchestrate.orch] captured reply (%d ch) is a near-duplicate of last shown bubble (%d ch) — suppressing", len(trimmed), len(last))
		return
	}
	Debug("[orchestrate.orch] emitting captured reply as bubble (%d ch, last bubble %d ch)", len(trimmed), len(strings.TrimSpace(pr.lastFinalizedText)))
	pr.emitBubble(text)
}

func (pr *planRun) initRoundCaps() {
	t := pr.t
	// Budget injection — surface the round counter to the LLM so it
	// can pace itself instead of calling tools until it hits the cap
	// blind. OnRoundStart fires AFTER history is appended but BEFORE
	// the LLM call, so each round sees the same brief note appended
	// to history.
	pr.maxR = resolveMaxWorkerRounds(t.agent)
	// soft cap (the pace-against target)
	// Explorer wiring for the ORCHESTRATOR loop (mirrors runWorkerStep):
	// MaxRounds is the HARD cap, StopRound enforces the soft cap (maxR)
	// UNTIL the LLM flips explorerMode via enter_explorer_mode — then it
	// runs on to orchHardCap. Without this the orchestrator hard-stops at
	// maxR and enter_explorer_mode (which only flips a flag) is a no-op
	// inline, so a build that runs short can't self-extend. Non-explorer
	// agents keep orchHardCap == maxR, so StopRound is a plain cap.
	pr.orchHardCap = pr.maxR
	if t.agent.AllowExplorer {
		if ec := resolveExplorerHardCap(t.agent); ec > pr.orchHardCap {
			pr.orchHardCap = ec
		}
	}
	// absoluteCeiling is the loop's hard MaxRounds — set high enough that
	// StopRound (the dynamic governor: soft cap → explorer cap →
	// plan-scaled cap) always decides first. present_build_plan can lift
	// the budget above the explorer ceiling, so add the maximum plan grant
	// on top. Non-plan agents never reach it — StopRound stops earlier.
	pr.absoluteCeiling = pr.orchHardCap + buildPlanRoundsPerStep*resolveMaxPlanSteps(t.agent)
	pr.roundCounter = 0
}

func (pr *planRun) onRoundStartHandler() []Message {
	t := pr.t
	pr.roundCounter++
	t.currentRound = pr.roundCounter
	// Pace against the SOFT cap normally; once the LLM has flipped
	// explorer mode, pace against the hard cap (StopRound lets it run
	// there); once a build plan is presented, pace against the
	// plan-scaled cap. Explorer NUDGES only fire when NOT already
	// exploring.
	cap := pr.maxR
	if t.explorerMode {
		cap = pr.orchHardCap
	}
	if t.planBudgetCap > cap {
		cap = t.planBudgetCap
	}
	remaining := cap - pr.roundCounter
	canExtend := t.agent.AllowExplorer && !t.explorerMode
	if remaining <= 0 {
		// At the cap. If the agent can still self-extend (explorer-
		// capable and not yet exploring), offer it — extending beats
		// getting cut off mid-build and shipping a half-built agent.
		if canExtend {
			return []Message{{
				Role: "user",
				Content: fmt.Sprintf(
					"[Round %d/%d — FINAL round. If you are NOT finished (still mid-build / tools left to add or verify), call enter_explorer_mode NOW to extend your budget. Otherwise produce your final answer from what you have.]",
					pr.roundCounter, cap,
				),
			}}
		}
		return []Message{{
			Role: "user",
			Content: fmt.Sprintf(
				"[Round %d/%d — FINAL round. No more tool calls. Produce your final answer NOW from what you have so far.]",
				pr.roundCounter, cap,
			),
		}}
	}
	// Few rounds left and able to self-extend: nudge enter_explorer_mode
	// so a large build / discovery stretches instead of getting cut off.
	if canExtend && remaining <= 5 {
		return []Message{{
			Role: "user",
			Content: fmt.Sprintf(
				"[Round %d/%d — only %d round%s left. enter_explorer_mode extends your budget to %d rounds for this step. Call it if you're (a) mid-build with tools still to add or verify, (b) mapping an unfamiliar API / system surface that keeps revealing more, (c) figuring out HOW to do something multi-step where each result reveals the next move (e.g. \"scrape this for a video\" — find container, identify format, locate manifest, resolve segments), or (d) troubleshooting a misbehaving tool — probing variant args / inspecting related state to narrow down the failure mode before you can work around it or report cleanly. If you're nearly done, wrap up.]",
				pr.roundCounter, cap, remaining, plural(remaining), pr.orchHardCap,
			),
		}}
	}
	// General pacing nudge — ONLY when the budget is actually getting
	// tight. On early rounds with ample budget this note is (a) noise and
	// (b) a CACHE POISON. OnRoundStart appends it right after the user
	// message, but it's ephemeral: next turn the persisted assistant reply
	// occupies that slot instead, so the prompt prefix diverges at exactly
	// that point and the (recurrent/hybrid) worker re-prefills the ENTIRE
	// prompt instead of reusing the cached prefix — the orchestrate turn-2
	// latency bug (every follow-up turn paid a full ~16k re-prefill).
	// Returning nil on ample-budget rounds keeps the prefix byte-stable so
	// the worker's context checkpoint stays usable across turns. The
	// FINAL-round and explorer nudges above still fire near the cap, where
	// an occasional cache miss is irrelevant.
	const pacerNudgeWithin = 8 // rounds-left threshold below which to start pacing aloud
	if remaining > pacerNudgeWithin {
		return nil
	}
	return []Message{{
		Role: "user",
		Content: fmt.Sprintf(
			"[Round %d/%d — %d round%s left before this turn ends. Pace accordingly: if the answer needs more searches than that, use plan_set instead of iterating inline.]",
			pr.roundCounter, cap, remaining, plural(remaining),
		),
	}}
}

func (pr *planRun) prepareMessages() {
	t := pr.t
	// Attach any image attachments to the most recent user message
	// so vision-capable LLMs see them. Images are ephemeral — only
	// passed for this turn; not persisted in session history (the
	// raw bytes would balloon the DB).
	pr.llmMsgs = toLLMMessages(pr.msgs)
	if len(t.userImages) > 0 {
		for i := len(pr.llmMsgs) - 1; i >= 0; i-- {
			if pr.llmMsgs[i].Role == "user" {
				pr.llmMsgs[i].Images = t.userImages
				// Seeing a picture is not the same as being able to name one.
				// Without this the model has the pixels and no id, so a request
				// to edit or combine them resolves to whatever handle it DOES
				// have — usually an image it made earlier.
				pr.llmMsgs[i].Content += uploadedImageHandles(len(t.userImages))
				break
			}
		}
	}
	// Append per-turn, message-dependent content (skill/agent trigger hints +
	// triggered-skill instructions, collected into turnContext during sys
	// assembly) onto the LAST user message — AFTER the stable system prompt +
	// tools + history. This keeps the cacheable prefix byte-identical across
	// turns so the worker model reuses it instead of re-prefilling ~16k every
	// turn. The new user message is new each turn anyway, so carrying the
	// hints there costs nothing in cache terms.
	if tc := strings.TrimSpace(pr.turnContext); tc != "" {
		for i := len(pr.llmMsgs) - 1; i >= 0; i-- {
			if pr.llmMsgs[i].Role == "user" {
				if strings.TrimSpace(pr.llmMsgs[i].Content) == "" {
					pr.llmMsgs[i].Content = tc
				} else {
					pr.llmMsgs[i].Content += "\n\n" + tc
				}
				break
			}
		}
	}
	// (Auto-inject removed — knowledge retrieval is now exclusively
	// pull-driven via knowledge_search. The LLM decides when to query,
	// scopes the search itself, and reads excerpts before deciding
	// whether to fetch more. Eliminates contamination from
	// tangentially-related chunks getting silently injected, and saves
	// an Embed call per turn for every agent with a corpus.)

	// Dedupe by catalog name, keeping the FIRST occurrence. The framework
	// control tools (find_tools / send_status / stay_silent / keep_going) are
	// force-added by frameworkConversationalTools AND already present in an
	// open-pool agent's base catalog, so they'd otherwise arrive twice and the
	// loop would dedupe them noisily every turn. Both copies are session-wired
	// (the base catalog is built via GetAgentToolsWithSession), so keeping the
	// first is harmless — this just moves the dedupe upstream of the log spam.
	pr.allTools = dedupeToolDefsByName(pr.allTools)
	// Narrow the catalog to what the current machine phase may reach. A
	// phase that names no tools (and every agent with no machine) gets
	// the catalog back unchanged, so this can sit on the main path.
	//
	// Loudly, because this is the one place the agent's reach changes
	// BETWEEN turns of one session — the phase advances and tools the model
	// used two turns ago stop resolving. Silently, that reads to the model
	// as a name it got wrong, and it burns the turn retrying spellings.
	narrowed, phaseDropped, phaseUnmatched, phaseFellBack := pr.mach.narrowCatalog(pr.allTools, t.attachedToolNames)
	if len(phaseDropped) > 0 {
		Log("[orchestrate.orch] machine phase %q narrowed the catalog %d → %d tool(s); dropped: %v",
			pr.mach.Name(), len(pr.allTools), len(narrowed), phaseDropped)
	}
	if len(phaseUnmatched) > 0 {
		// The phase asked for something the catalog doesn't carry under that
		// name. Nothing else reports it: the filter is a name match, so a miss
		// looks exactly like a phase that meant to allow fewer tools.
		Log("[orchestrate.orch] machine phase %q names %d tool(s) not in this agent's catalog: %v",
			pr.mach.Name(), len(phaseUnmatched), phaseUnmatched)
	}
	if phaseFellBack {
		// Every name missed. The literal reading of that is "no tools", which
		// is both unlikely to be meant and unsurvivable — it takes the control
		// plane with it, so the model can neither answer nor leave the phase.
		Log("[orchestrate.orch] machine phase %q matched NOTHING in this agent's catalog — running the full %d-tool catalog rather than none",
			pr.mach.Name(), len(narrowed))
		t.turnDiag("machine_phase_tools_unmatched", fmt.Sprintf(
			"phase %q allows %v, and NONE of those names exist in this agent's catalog, so the phase ran with the full catalog instead of an empty one. Fix the phase's tool names — they must match the catalog exactly, and a remote MCP tool is exposed as \"<server>_<tool>\" in lowercase (getConfluencePage on the server is atlassian_getconfluencepage here).",
			pr.mach.Name(), pr.mach.phase.Tools))
	} else if len(phaseUnmatched) > 0 {
		t.turnDiag("machine_phase_tool_missing", fmt.Sprintf(
			"phase %q allows %v, but %v are not in this agent's catalog under those names, so the phase ran without them. Tool names in a phase must match the catalog exactly — a remote MCP tool is exposed as \"<server>_<tool>\", not its raw remote name.",
			pr.mach.Name(), pr.mach.phase.Tools, phaseUnmatched))
	}
	pr.allTools = narrowed
	// The catalog the model ACTUALLY receives. The full-surface line above
	// runs before this narrowing, so on a machine turn it reports the wider
	// pre-phase set — which is what sent a reader chasing catalog size when
	// the phase filter was what moved.
	if len(phaseDropped) > 0 || len(phaseUnmatched) > 0 {
		effective := make([]string, 0, len(pr.allTools))
		for _, td := range pr.allTools {
			effective = append(effective, td.Tool.Name)
		}
		Log("[orchestrate.orch] session=%s phase=%s tools_to_llm_effective[%d]=%v",
			pr.sessID, pr.mach.Name(), len(pr.allTools), effective)
	}
	// The catalog is final here, so the blocks that describe it can be
	// written. Still the tail of the system prompt, exactly where they were.
	pr.appendCatalogPromptBlocks()
	// pre_input guardrail: judge the incoming request before round 1 so a
	// topical/disclosure rule ("never mention salary") is caught at the door,
	// not after the model has already narrated the answer in an interim turn.
	pr.llmMsgs, pr.gDecline = t.applyInputGuardrail(pr.llmMsgs)
	// What the USER actually said, captured before the loop writes on it. The
	// loop appends turn-scoped context (the image manifest) and prepends the
	// date stamp to this same trailing message in place, and the graph extractor
	// below runs after that — so reading it afterwards would file the
	// framework's own scaffolding as things the user stated.
	pr.userSaid = LatestUserContent(pr.llmMsgs)
}

func (pr *planRun) runLoop() {
	t := pr.t
	pr.orchStart = time.Now()
	// Everything above this line happened while the person waited and nothing
	// was accounting for it: [agent_loop] turn time starts on the next line.
	t.prep.done()
	Debug("[orchestrate.orch] entering RunAgentLoop (msgs=%d tools=%d sys_chars=%d)", len(pr.llmMsgs), len(pr.allTools), len(pr.sys))
	// What the prompt is MADE OF, every turn. Without this, "the turn is slow
	// and the prompt is 205k" is a number with nowhere to go: the system
	// prompt, the tool catalog and the conversation are all plausible and only
	// one of them is ever the answer. Cost is one marshal of the catalog per
	// turn, against a request that is about to process every one of these
	// tokens anyway.
	logPromptComposition("plan", pr.sessID, pr.sys, pr.allTools, pr.llmMsgs)
	pr.resp, _, pr.loopErr = t.app.RunAgentLoop(pr.orchCtx, pr.llmMsgs, pr.loopConfig())
	pr.stopKeepalive()
	// Consume the silence flag. stay_silent writes ToolSession.Silenced and,
	// until now, nothing read it: the only thing that worked was agent_loop's
	// hardcoded break, which ends the LOOP. Recording it on the turn is what
	// lets the plan driver honor "this turn is now closed" — the promise the
	// tool's own result makes to the model.
	if pr.sess != nil && pr.sess.Silenced {
		t.turnClosed = true
	}
	// Off-hot-path graph population: after a clean turn, best-effort extract the
	// entity relationships the user stated into the graph. Single-flight +
	// cooldown + own goroutine (never blocks the turn, self-throttles on the
	// shared GPU); gated off by default.
	if pr.loopErr == nil {
		// Close the books on what this turn said it would do. A turn that called
		// a tool retires whatever was outstanding; one that ended on a fresh
		// promise and did nothing records it for the next turn to answer for.
		if pr.resp != nil {
			recordTurnCommitment(t.udb, t.chatSessionID(), pr.resp.Content, len(t.persistedToolCalls()) > 0)
		}
		maybeExtractGraph(t.udb, factsNamespace(t.agent.ID), pr.userSaid, t.app.WorkerChat)
	}
	{
		respLen := 0
		if pr.resp != nil {
			respLen = len(strings.TrimSpace(pr.resp.Content))
		}
		errStr := ""
		if pr.loopErr != nil {
			errStr = pr.loopErr.Error()
		}
		Debug("[orchestrate.orch] RunAgentLoop returned (elapsed=%s, resp.content=%dch, ctx.err=%v, orchCtx.err=%v, loopErr=%q, capturedSteps=%d, capturedReply=%dch, capturedQuest=%dch, capturedForm=%d)",
			time.Since(pr.orchStart),
			respLen,
			t.ctx.Err(), pr.orchCtx.Err(), errStr,
			len(pr.capturedSteps),
			len(pr.capturedReply),
			len(pr.capturedQuest),
			len(pr.capturedFormSteps),
		)
	}
}

func (pr *planRun) finish() (steps []PlanStep, question, directReply string, err error) {
	// Deferred so it sees what the turn actually ended up showing, including
	// the bubbles the branches below emit.
	defer func() { pr.restoreWithheldLeadIn(question, directReply) }()
	t := pr.t
	// Catch a final round whose OnStep didn't fire (rare — happens
	// when the loop terminates between content stream and the OnStep
	// dispatch). Finalize and treat it as the last finalized bubble.
	finalID := pr.streamMsgID
	if finalID == "" {
		finalID = t.getCurrentMsgID()
	}
	if finalID != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": finalID})
		if pr.streamedBuf.Len() > 0 {
			pr.lastFinalizedText = strings.TrimSpace(StripToolCallMarkup(pr.streamedBuf.String()))
		}
		pr.lastFinalizedID = finalID
		// Persist the final round's narration too — same gap as the
		// per-round capture in onStepHandler, only on the rare path
		// where the loop terminates after streaming but before OnStep.
		t.captureMidTurnBubble(pr.lastFinalizedText)
		pr.streamMsgID = ""
		t.setCurrentMsgID("")
	}
	// Stats land on the last bubble we finalized; RunAgentLoop only
	// returns the last round's resp, so per-round stats aren't
	// available without backend changes.
	if pr.lastFinalizedID != "" {
		t.emitStats(pr.lastFinalizedID, pr.resp, pr.orchStart)
	}
	// Loop end conditions:
	//   1. A control tool fired → orchCtx is canceled (but t.ctx
	//      is still live). Captured state below drives dispatch.
	//   2. Loop hit MaxRounds → resp may carry content; treat as
	//      implicit respond_directly.
	//   3. Loop ended naturally (no tool call this round) → resp.Content
	//      is the implicit reply.
	//   4. Real error (network, LLM, etc.) → return it.
	if pr.loopErr != nil && t.ctx.Err() == nil && pr.orchCtx.Err() == nil {
		return nil, "", "", pr.loopErr
	}
	if len(pr.capturedFormSteps) > 0 {
		// Multi-step form — render as a single block with all the
		// steps; client walks the user through them one at a time
		// and submits a compiled answer at the end. Return a brief
		// placeholder as the captured question so the caller's
		// session-persistence logic has something to record.
		t.sse.Send(map[string]any{
			"kind":  "block",
			"type":  "ui_ask_form",
			"id":    fmt.Sprintf("askform-%d", time.Now().UnixNano()),
			"steps": pr.capturedFormSteps,
		})
		if t.session != nil {
			t.session.AwaitingUserConfirm = true
		}
		return nil, fmt.Sprintf("(form with %d question%s)", len(pr.capturedFormSteps), plural(len(pr.capturedFormSteps))), "", nil
	}
	if pr.capturedQuest != "" {
		// A bare question — no options — renders as PROSE, not a card. The card
		// used to render always ("consistent affordance"), but with no buttons
		// its only control is a textarea floating directly above the composer,
		// which is already a textarea: two identical answer boxes for one
		// question, and the card one gates the turn. The card earns its place
		// exactly when it has choices to click. AwaitingUserConfirm is still
		// set: it is still an ask, and the next turn's gated tools (agent CRUD
		// after a Builder confirmation) depend on the flag, not the rendering.
		if len(pr.capturedOptions) == 0 {
			// emitBubble, NOT emitCapturedAsBubble. The near-duplicate guard is
			// right for a REPLY (a repeat is noise) and catastrophic for an ASK:
			// the model routinely streams a lead-in ("Several things are
			// ambiguous:") and then captures a question that BEGINS with those
			// same words, which scores as a duplicate on longest-common-prefix
			// and drops the entire ask. AwaitingUserConfirm is still set below,
			// so the turn ended parked on a question the user was never shown —
			// a lead-in, a colon, and nothing. Live, in Guides. A repeated
			// sentence is a cosmetic cost; a swallowed question is a dead turn.
			pr.emitBubble(pr.capturedQuest)
			if t.session != nil {
				t.session.AwaitingUserConfirm = true
			}
			return nil, pr.capturedQuest, "", nil
		}
		// With options, the card is the whole point: click-to-choose.
		t.sse.Send(map[string]any{
			"kind":     "block",
			"type":     "ui_ask",
			"id":       fmt.Sprintf("ask-%d", time.Now().UnixNano()),
			"question": pr.capturedQuest,
			"options":  pr.capturedOptions,
			"multi":    pr.capturedMulti,
		})
		// Mark this session as awaiting user confirmation. Gated tools
		// (agent CRUD) will fire on the NEXT user turn after this
		// pause — without this flag, those tools refuse to run.
		if t.session != nil {
			t.session.AwaitingUserConfirm = true
		}
		return nil, pr.capturedQuest, "", nil
	}
	if len(pr.capturedSteps) > 0 {
		// Plan_set's pre-amble narration already streamed; the plan
		// card will render below. Nothing further to emit here.
		return pr.capturedSteps, "", "", nil
	}
	if pr.capturedReply != "" {
		pr.emitCapturedAsBubble(pr.capturedReply)
		return nil, "", pr.capturedReply, nil
	}
	if pr.resp != nil && strings.TrimSpace(pr.resp.Content) != "" {
		// Implicit respond_directly path — the LLM streamed text in
		// its final round but didn't call any control tool. The
		// per-round finalizer already finalized that bubble; we
		// just need a clean copy for the persisted history.
		clean := strings.TrimSpace(StripToolCallMarkup(pr.resp.Content))
		// Emit unconditionally and let emitCapturedAsBubble's near-duplicate
		// check decide whether to actually show it. The old guard ("nothing was
		// finalized this turn") was too coarse: the forced-final-answer rescue
		// calls T.LLM.Chat NON-streaming (core/agent_loop.go), so its content was
		// never streamed — but if ANY mid-turn narration bubble rendered first,
		// lastFinalizedText was non-empty and the guard dropped a brand-new final
		// synthesis from the LIVE path while still persisting it (visible in
		// history, blank on screen). emitCapturedAsBubble already suppresses only
		// a TRUE near-duplicate of the last streamed bubble, so the normal
		// streamed-then-rescued repeat is still deduped, and a genuinely different
		// final answer now renders as its own bubble.
		pr.emitCapturedAsBubble(clean)
		return nil, "", clean, nil
	}
	// No content anywhere. If the loop ran out of its round budget (rather
	// than ending naturally or being deliberately silenced via stay_silent,
	// which returns from inside the loop without HitRoundCap), don't leave
	// the user staring at a blank turn — say so explicitly so they can narrow
	// the ask or retry instead of assuming the agent is broken. This is the
	// "ran out of turns, got nothing back" failure (common for retrieval-heavy
	// agents that exhaust rounds mid-investigation).
	if pr.resp != nil && pr.resp.HitRoundCap {
		t.turnDiag("round-cap", "This turn ran out of worker rounds before finishing — raise the round limit or narrow the ask.")
		msg := "I ran out of working rounds for this turn before I could finish, and didn't have a partial answer to show. Try narrowing the question, or ask me to continue and I'll pick up from here."
		// Same as the resp.Content path above: gate on the near-duplicate check,
		// not a coarse "already rendered something" boolean, so this shows even
		// after a mid-turn narration bubble.
		pr.emitCapturedAsBubble(msg)
		return nil, "", msg, nil
	}
	// Nothing captured and nothing said. Reaching here with a loop error means
	// the error was DROPPED by the guard above, which only reports when neither
	// context is still good — and an LLM that cannot be reached is precisely the
	// case that hangs until the turn's deadline and then fails. So the one
	// failure a user is most likely to hit was the one that returned no content
	// and no error: the thread kept their message, grew no reply, and the
	// composer re-enabled. Indistinguishable from being ignored.
	//
	// A deliberate cancel is not a failure and stays quiet — the caller already
	// reports that in its own words.
	if pr.loopErr != nil && !errors.Is(t.ctx.Err(), context.Canceled) {
		t.turnDiag("llm-unreachable", "The model could not be reached for this turn: "+pr.loopErr.Error())
		return nil, "", "", pr.loopErr
	}
	// No error, no content, no cancel. stay_silent returns from inside the loop
	// and never lands here, so this is a turn that produced nothing for a reason
	// nobody recorded. Say so rather than rendering a blank: the round-cap
	// branch above already sets that precedent, and a silent turn reads as the
	// assistant ignoring the request.
	if t.ctx.Err() == nil {
		t.turnDiag("empty-turn", "This turn produced no reply and reported no error.")
		return nil, "", "", fmt.Errorf("the turn finished without producing a reply")
	}
	return nil, "", "", nil
}

// loopConfig is the AgentLoopConfig for the orchestrator round: the prompt
// and catalog, the callbacks the loop fires (methods on planRun), and the
// budget governors. Read alongside runLoop, which is the call.
func (pr *planRun) loopConfig() AgentLoopConfig {
	t := pr.t
	return AgentLoopConfig{
		// A terminal-rule pre_input block refused this request outright: the loop
		// delivers this text and never calls a model. Empty on every other turn.
		PreEmptedReply: pr.gDecline,
		SendGuardKey:   sendGuardKey,
		SystemPrompt:   pr.sys,
		Tools:          pr.allTools,
		// A machine phase may pin the tier for turns spent in it.
		// TierUnset (no machine, or a phase that names no model) follows
		// the route stage exactly as before. Resolved with the route key
		// above so the two cannot disagree — see turnRouting.
		TierOverride:         pr.tierPin,
		StampLocation:        UserLocation(t.user), // stamp the turn in the interactive user's zone
		DynamicTools:         t.dynamicNewTempTools(pr.sess),
		ToolFallbackResolver: t.lazyToolFallback,
		Stream:               pr.streamHandler,
		OnStep:               pr.onStepHandler,
		OnRoundStart:         pr.onRoundStartHandler,
		// Route the loop's silent correction guards into this session's ⚠
		// diagnostics trail, so a re-prompt the framework issued on the user's
		// behalf (e.g. named-a-tool-but-didn't-call-it) leaves a breadcrumb
		// instead of vanishing into Debug logs.
		OnDiag: t.turnDiag,
		// One-shot advice for the failure-shape guard: three hits on one wall
		// means the arguments aren't what's wrong, so ask a model that can
		// ANSWER instead of nudging the one that's stuck. See consult.go.
		Consult: t.consult,
		// Settle the rejected round's streamed bubble before a correction
		// re-prompts, so the retry opens a fresh bubble instead of
		// concatenating into an orphaned one (the double-emit fix).
		SettleRound:  pr.settleRound,
		RetractRound: pr.retractRound,
		// Feed view_video's sampled frames to the model on the next round so it
		// actually sees the clip instead of describing it blind.
		DrainViewImages: pr.sess.DrainViewImages,
		BeforeToolRound: func() { SnapshotImageRefs(pr.sess) },
		// Hand over the recent-image ids when the user is talking about a
		// picture. The space can't live in the tool schema (it changes on every
		// image operation and would re-pay cold prefill), but the newest user
		// turn is the volatile tail that never caches anyway — same place the
		// date stamp goes, and free for the same reason.
		TurnNotes: func(user string) string { return turnNotes(pr.sess, t.udb, t.chatSessionID(), user) },
		// Last look before the reply goes out: is it true about what this turn
		// actually did? Backstops the phrase-list guards on the shapes they don't
		// know. See turn_judge.go.
		CapturePrompt:  t.agent.CapturePrompt,
		TurnClaimJudge: t.app.turnClaimJudge(t.ctx),
		// What ran for this turn BEFORE the loop did — the machine steps. The
		// loop cannot see them (they run during system-prompt assembly, on a
		// session of their own), so without this the judge reads a turn whose
		// step did the searching as a turn that did nothing and convicts the
		// reply for reporting it.
		PriorWork: t.priorWorkForJudge,
		// And what this agent's own scheduled runs already filed into the
		// thread — the work a recap is about, which this turn did not do.
		PriorReports: t.priorReportsForJudge,
		// And what EARLIER turns of this conversation ran, so a reply asked to
		// write up the work so far is read as the recap it is rather than as a
		// claim about a turn that called nothing.
		PriorTurnWork: t.priorTurnWorkForJudge,
		// And whether the reply KNOWS what it asserts. Scope is the notes the
		// memory block marked unchecked, so a turn holding none never reaches a
		// model call. See grounding_judge.go.
		TurnGroundingJudge: t.app.turnGroundingJudge(t.ctx),
		//
		// The SAME slice the prompt was built from, not a second read of the
		// store. Re-reading here once scoped the judge to every marked note the
		// agent had while an incognito prompt contained none — so the judge ran
		// on a turn it is documented never to run on, and could convict the
		// reply for failing to attribute a note the model was never shown. One
		// slice, rendered and judged, makes the scope true by construction
		// rather than by two reads agreeing about a condition either could
		// forget. (facts() now enforces incognito itself, so a second read would
		// agree today — the point stands for whatever the next condition is.)
		UncheckedClaims:    UncheckedFactNotes(pr.facts),
		DeliveredCount:     func() int { return len(pr.sess.Images) + len(pr.sess.Videos) + len(pr.sess.Files) },
		Backgrounded:       func() bool { return pr.sess.Detach.Any() },
		BackgroundEstimate: func() string { return pr.sess.Detach.EstimateText() },
		// Catch a reply that presents a picture the turn never produced, while
		// the loop can still do something about it. The dispatch path has had
		// this since the phantom-delivery work; interactive chat never did, so
		// the only protection here was recoverClaimedDelivery — a backstop that
		// ships a staged file and has nothing to say when there is no file. A
		// generation that failed left the caption standing on its own.
		PhantomDeliveryRefs: func(reply string) []string {
			return phantomDeliveryRefs(pr.sess, reply, pr.produced.producedKind())
		},
		// Drain mid-flight user injections EACH ROUND so the orchestrator
		// incorporates them during inline work — not just at plan-step
		// boundaries / synthesis. Without this, a note injected while the
		// orchestrator iterated inline sat in the queue until end-of-turn,
		// so the agent "kept doing what it was doing" and only acknowledged
		// the note at synthesis. Separate hook from OnRoundStart (which is
		// the always-returns-content budget pacer); InjectionDrain MUST
		// return empty when nothing's queued or the pre-finalize re-drain
		// would loop. drainNotes() empties the shared queue, so the
		// plan-step/synthesis drains coexist (first drain wins).
		InjectionDrain: func() []Message {
			notes := t.drainNotes()
			if len(notes) == 0 {
				return nil
			}
			block := notesContextBlock(notes)
			if block == "" {
				return nil
			}
			return []Message{{Role: "user", Content: block}}
		},
		// plan_set fixation guard. Once the model has had plan_set
		// rejected planSetDropThreshold times this turn, drop it from the
		// catalog so it physically can't keep re-submitting the same
		// vacuous plan (a real Qwen loop — see planSetRejects above).
		RoundToolFilter: func(name string) bool {
			// A phase's deny holds for tools that arrive AFTER the catalog was
			// narrowed — a temp tool authored mid-turn, a credential minting its
			// own, a lazily hydrated custom tool. Those never pass through
			// narrowCatalog, so without this the one control written to keep a
			// step off a tool could be walked around by creating it again under
			// the same name.
			if t.machine.Denies(name) {
				return false
			}
			return !(name == "plan_set" && pr.planSetRejects >= planSetDropThreshold)
		},
		// The round right after a plan_set rejection skips thinking — the
		// model was burning huge thinking budgets deliberating itself back
		// into plan_set. Consume the flag so it applies to exactly one round.
		RoundChatOptions: func() []ChatOption {
			if pr.forceNoThinkAfterReject {
				pr.forceNoThinkAfterReject = false
				return []ChatOption{WithThink(false)}
			}
			return nil
		},
		// ContextSize is deliberately UNSET: RunAgentLoop now fills it from
		// the running tiers for every loop in the tree, and its answer is
		// better than the one that was here. This asked for the LEAD window,
		// which a turn running on the worker tier can exceed — the loop can
		// escalate mid-turn, so the budget has to be safe for whichever tier
		// it lands on, and only the smaller window is.
		// LLM-driven compaction: when the model calls compact_context (it
		// just consumed a long output it's done with), force an aggressive
		// shed on the next round.
		RoundCompactNow: func() bool {
			if pr.compactRequested {
				pr.compactRequested = false
				return true
			}
			return false
		},
		// Cap orchestrator iterations. MaxRounds is the HARD ceiling;
		// StopRound enforces the soft cap (maxR) until the LLM flips
		// explorer mode, then lets it run to orchHardCap. Most chat turns
		// need 1-3 rounds; deep research / large builds bump via the
		// agent's worker-rounds budget + enter_explorer_mode.
		MaxRounds:     pr.absoluteCeiling,
		ThinkBudget:   t.agent.ThinkBudget,  // per-agent override; 0 = inherit route/global
		ActionQuotas:  t.agent.ActionQuotas, // per-agent 24h caps; empty = uncapped
		BudgetKey:     t.agent.ID,
		DailySpendUSD: t.agent.DailySpendUSD,
		// StopRound is the dynamic governor (MaxRounds is just the safety
		// ceiling). Effective cap = soft cap, raised to the explorer cap
		// once explorer mode is on, raised again to the plan-scaled cap
		// once a build plan is presented. max() semantics — a later phase
		// never lowers the budget a prior one granted.
		StopRound: func() bool {
			pr.orchRoundsUsed++
			cap := pr.maxR
			if t.explorerMode {
				cap = pr.orchHardCap
			}
			if t.planBudgetCap > cap {
				cap = t.planBudgetCap
			}
			return pr.orchRoundsUsed > cap
		},
		// Escalation policy hook (confirm.go): calls through a
		// credential flagged "Require confirm" park on an in-chat
		// approval card; every other NeedsConfirm tool (delete_agent
		// etc.) auto-approves as before so nothing hangs on stdin.
		Confirm:             t.confirmFuncFor(pr.sess),
		GuardrailCheck:      t.guardrailEnforcer().Check,
		GuardrailActionGate: t.guardrailEnforcer().ActionGate,
		GuardrailHalted:     t.guardrailEnforcer().Halted,
		GuardrailReject:     t.guardrailEnforcer().Reject,
		GuardrailDeclines:   t.agent.GuardrailDeclines,
		// Control tools end the round immediately. If the LLM bundles
		// ask_user with create_agent in the same response, only ask_user
		// fires and the turn pauses for the user's actual answer.
		RoundAbortTools: orchestratorAbortTools,
		// (No SingleFireGroups for image/video producers anymore.
		// Under the old auto-attach architecture, multiple find_image
		// calls in one batch produced multiple unintended attachments
		// — the group was the structural fix. With the new write-
		// to-workspace + explicit workspace(attach) flow, multi-fire
		// of producers is intentional: the user CAN ask for "three
		// ducks" and the LLM produces three workspace files + three
		// attach calls. The architecture itself enforces deliberate
		// per-file delivery now.)
		ChatOptions: []ChatOption{
			WithRouteKey(pr.routeKey),
			WithThink(pr.think),
		},
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
