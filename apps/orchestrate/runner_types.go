package orchestrate

import (
	"context"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// inflightCancels keys per-session cancel funcs so /api/cancel can
// abort an in-flight runner. Keyed by sessionID; runner cleans up
// its own entry on exit.
//
// inflightConnectors does the same for the per-turn NetworkConnector
// so the chat surface can flip privacy LIVE mid-turn: when the user
// toggles Private ON during a running turn, the privacy endpoint
// looks up the active connector and calls SetAllowed(false). All
// in-flight + subsequent tool refusal sites re-read Allowed() on
// each call, so the flip propagates immediately.
var (
	inflightCancels    sync.Map // sessionID -> context.CancelFunc
	inflightConnectors sync.Map // sessionID -> *NetworkConnector
)

// Per-agent limits fall back to these when the agent record leaves
// the field at zero (older records, freshly cloned starters). Kept
// modest — agents that need more should set the field explicitly so
// the budget shows up in the editor.
const (
	defaultMaxPlanSteps    = 5
	defaultMaxWorkerRounds = 15 // 15 rounds + the 5 wrap-up grace rounds (grace only arms at MaxRounds >= 10)
	minWorkerRounds        = 6  // floor: a too-low per-agent cap starves multi-step tasks (fetch → send) and forces a mid-action wrap-up; the cap is a MAXIMUM, so this never forces extra rounds on a snappy agent
	// buildPlanRoundsPerStep is how many execution rounds each build-plan
	// step grants once Builder calls present_build_plan. A step is
	// typically draft script → test → fix → verify → mark_step_done, so
	// ~4 rounds/step. The grant lands on top of the round count already
	// spent exploring, giving execution its own runway.
	buildPlanRoundsPerStep = 4
)

func resolveMaxPlanSteps(a AgentRecord) int {
	if a.MaxPlanSteps > 0 {
		return a.MaxPlanSteps
	}
	return defaultMaxPlanSteps
}

func resolveMaxWorkerRounds(a AgentRecord) int {
	if a.MaxWorkerRounds <= 0 {
		return defaultMaxWorkerRounds
	}
	if a.MaxWorkerRounds < minWorkerRounds {
		return minWorkerRounds // floor a too-low explicit setting so it can finish
	}
	return a.MaxWorkerRounds
}

// orchestratorAbortTools names the tools that close an orchestrator round the
// moment they succeed: the turn is now waiting on the user, on a direct reply,
// or on a plan.
var orchestratorAbortTools = []string{"ask_user", "ask_user_form", "respond_directly", "plan_set"}

// workerAbortTools is the orchestrator's set MINUS plan_set — a worker step is
// already inside a plan and has none to set.
//
// Derived rather than retyped. These were two literals hundreds of lines apart,
// with the relationship stated only in a comment; the test guarding it grepped
// the source for `strings.Count(src, "RoundAbortTools:") >= 2` and for the
// worker literal, so adding a fifth abort tool to the orchestrator left the
// worker set stale and the test green. Subtracting from the real list makes the
// relationship the compiler's problem.
var workerAbortTools = func() []string {
	out := make([]string, 0, len(orchestratorAbortTools))
	for _, n := range orchestratorAbortTools {
		if n != "plan_set" {
			out = append(out, n)
		}
	}
	return out
}()

// chatTurn is the per-request state shared across the runner
// pipeline (plan → step* → synthesis → consolidation). Bundling the
// (ctx, sse, udb, user, agent) quartet here keeps method signatures
// honest about what's actually shared vs. per-step input — a step
// only takes its own (prior, cur), the rest is on the receiver.
//
// Memory is cached per-turn: loaded once on first access so each
// pipeline phase sees the same snapshot, and any notes the
// consolidator writes in the background goroutine don't race the
// already-fired prompt injection.
type chatTurn struct {
	app *OrchestrateApp
	ctx context.Context
	// prep measures how long the person waited before their message reached a
	// model. nil on every turn that is not a live send — a fire, a dispatch, a
	// test — and every method on it tolerates that.
	prep  *prepClock
	sse   *sseWriter
	udb   Database
	user  string
	agent AgentRecord
	// machineTools is the catalog a machine's steps draw from, built at
	// most once per turn and only when a step actually names tools (see
	// chatTurn.machineCatalog). The turn's own session does not exist
	// when a step runs, so this is a session of the step's own — the
	// same shape a pipeline sub-run uses.
	machineTools []AgentToolDef
	// workPlan is this turn's tracked-plan group (AgentRecord.WorkPlan),
	// built at most once because the six tools close over ONE plan.
	workPlan *WorkPlanToolSet
	// machineSess is the session those tools were resolved against —
	// held because the approval hook reads session state to find which
	// credential a call rides on.
	machineSess *ToolSession
	// attachedToolNames is what this turn's ATTACHMENTS minted: the tools
	// of the agent's attached sources and pipelines, by name.
	//
	// Tracked because a phase's Tools list must not silently revoke them.
	// The list is a selection out of the worker pool — that is the pool
	// the picker offers and the only one an author is choosing from — and
	// an attachment is a separate, deliberate grant made in the Sources
	// picker. Subtracting one because it was not on a list it was never
	// offered on is a decision nobody made. See narrowCatalog.
	attachedToolNames map[string]bool
	// priorWork is what ran for this turn BEFORE its loop began — machine
	// steps, and whatever a step delegated. Guarded by toolMu.
	priorWork []string
	// detach is this TURN's background-job ledger, shared by every session the
	// turn mints. It has to live here rather than on a session because a plan
	// runs each step on its OWN session (runWorkerStep), so a per-session cap
	// gives a three-step plan three allowances and the user three deliveries for
	// one request. Same reasoning as stagedFiles below. Minted on first use so
	// no construction site has to remember it.
	detach     *DetachLedger
	detachOnce sync.Once
	// turnClosed records that a control tool ended this turn — today
	// stay_silent, whose tool result promises "this turn is now closed".
	// It closes the LOOP it fires in (agent_loop breaks server-side), but a
	// plan's steps are driven by an ordinary for-loop OUTSIDE that call, so
	// without this the queue kept running: the model said it was done and then
	// watched nine more rounds of its own work it could no longer stop.
	//
	// Set from ToolSession.Silenced after a loop returns — that flag was
	// written by the tool and read by nobody.
	turnClosed bool

	// guardrailBlocks counts enforced-guardrail blocks across THIS turn, at any
	// hook. Lives on the turn because the check hook and the halt predicate are
	// separate callbacks that must agree on one count — escalation is a property
	// of the turn, not of a single interception point.
	guardrailBlocks int

	// guardrailRulesHit names each DISTINCT rule that blocked something this
	// turn, in order. The block path already logs the rule to the server log and
	// drops a session breadcrumb; this keeps it in reach of the CALLER, so a
	// background run can put the rule in its ledger entry instead of finishing
	// with a status that says nothing about why.
	guardrailRulesHit []string

	// diagAgentID / diagSessionID identify the trail for a turn that has no
	// live *session — a scheduled fire, a monitor wake, a dispatched sub-agent.
	// Those turns still fire guards, and without this every breadcrumb they
	// dropped went nowhere (turnDiag returned early on the nil session). Set by
	// the caller that owns the session record.
	diagAgentID   string
	diagSessionID string

	// guardrails caches this turn's enforcement set (see guardrailEnforcer).
	guardrails *guardrailEnforcement

	// scanner is this turn's tool-result injection scanner and scannerInit
	// records that we tried to build it (nil is a real answer — no LLM wired —
	// and must not be retried on every tool call). Its own mutex: toolMu is
	// held across parts of the tool-call path this runs inside of. See
	// chatTurn.toolScanner in toolscan.go.
	scanMu      sync.Mutex
	scanner     ToolScanner
	scannerInit bool

	// scanTaint is what THIS turn has been told to do by content it read —
	// one entry per detection, holding the flagged span.
	//
	// Its presence is what makes the turn tainted: while it is non-empty, the
	// pre_action gate widens and every consequential call is judged against
	// what the injection asked for. Guarded by scanMu, because a detection can
	// land from a parallel tool call while another is being judged.
	scanTaint  []string
	taintJudge TaintedActionJudge
	taintInit  bool
	// outboundTools names the wrapped tools that can carry data OUT. Read by
	// the widened pre_action gate on a tainted turn; see guardrailActionGate.
	outboundTools map[string]bool
	// taintBlocks counts actions stopped by that check, so the diagnostic can
	// say whether the tightening did anything.
	taintBlocks int

	// machine is the phase this turn is running under, if the agent has a
	// machine (machine.go). Lives on the turn because change_phase can
	// move it MID-turn, and the end-of-turn handoff has to close over
	// where the turn actually ended rather than where it started.
	// phaseChanges caps that tool at maxPhaseChangesPerTurn.
	machine      turnMachine
	phaseChanges int

	// Appeal state for contestable rules (guardrail_appeal.go). appealOffer is
	// the block the agent is currently invited to dispute — nil when there is
	// nothing to appeal, which is what makes the tool refuse rather than invite
	// speculative use. appealSpent caps it at one attempt per turn, so a context
	// probing for a wording that gets through cannot grind. appealWon records
	// rules an appeal cleared, and the check hook honours it for the rest of the
	// turn: without that the very next round re-blocks on the same blind verdict
	// and the win evaporates.
	appealOffer *guardrailAppealOffer
	appealSpent bool
	appealWon   map[string]bool

	// appTools are extra per-run tools supplied by the HOST APP dispatching this
	// turn (e.g. a workbench's co-author tool that writes into the open document's
	// record store). Injected into the orchestrator's catalog so the agent can call
	// them; the host builds them as closures with its own data access, so core/
	// orchestrate stays ignorant of the app's storage. Empty for ordinary runs.
	appTools    []AgentToolDef
	queue       *injectionQueue   // pending mid-flight user notes for this session
	session     *ChatSession      // mutable session pointer so drained notes can be persisted
	privateMode bool              // per-turn: drop internet tools from worker pool
	network     *NetworkConnector // SHARED instance: ctx + sess.Network + inflight registry all reference this same pointer so SetAllowed flips propagate to every read site mid-turn
	// inferredDisabled is the per-turn snapshot of the user's "Clean"
	// toggle preference — when true (or when agent.DisableInferred is
	// true), the Reference Memory layer is suppressed for this turn:
	// memory_save / memory_search / memory_forget stripped, synthesis
	// auto-ingest skipped, skills classifier suppressed (skills emit
	// derived chunks via self-training). Combined helper
	// t.inferredOff() returns the effective state. The Knowledge layer
	// (uploaded files) and Explicit Memory (facts) are unaffected.
	inferredDisabled bool
	isNewSession     bool     // first turn for this session; gates background title generation
	userImages       [][]byte // decoded image attachments from the chat panel; attached to the orchestrator's last user message
	// fromDesktopClient is true when THIS request came from the gohort-desktop
	// viewer (its proxy stamped the bridge key). Gates the from_client_* tool
	// surface so local-machine capabilities are reachable only from the
	// desktop app, never a remote browser/phone on the same account.
	fromDesktopClient bool

	// topic is the snake_case slug used to scope memory_save /
	// memory_search to a per-subject bucket. The LLM picks the slug
	// via the topic= arg when it calls those tools — there is no
	// auto-classifier. Empty defaults to generalTopic. Stays on the
	// turn for sub-agent dispatches that want to inherit a parent's
	// scope (agents_grouped_tool.go:run carries it forward).
	topic string

	// staticTempToolNames is the set of persistent temp tool names
	// that were included in the orchestrator's static catalog at
	// turn start. dynamicTempTools filters these out of its per-
	// round output so the same temp tool isn't both in the static
	// set AND surfaced freshly on every round.
	staticTempToolNames map[string]bool

	// lazyCustomToolNames is the set of custom (temp) tools that take
	// arguments and are therefore presented to the LLM by name +
	// description only (in a prompt section), NOT as full tool defs.
	// They're loaded on demand via load_tool — keeping their verbose
	// schemas out of the per-turn catalog. Zero-arg custom tools skip
	// this (name+desc IS their schema, so they stay directly callable).
	lazyCustomToolNames map[string]bool

	// toolSuccessNoted dedupes Tier-3 elevation recording within a turn:
	// recordToolSuccess is already idempotent per (tool, session), but it
	// reads the store to find that out, and a loop calling one tool twenty
	// times should not pay for that twenty times.
	toolSuccessNoted map[string]bool

	// loadedCustomTools tracks which lazy custom tools the LLM has
	// pulled in via load_tool this turn. The DynamicTools feed surfaces
	// a lazy tool's full def only once it's here, so the schema enters
	// the catalog exactly when the model commits to using it.
	loadedCustomTools map[string]bool

	// lazyCustomToolDefs maps a lazy custom tool's name to its full
	// (already activity-wrapped) AgentToolDef, so load_tool can return
	// the schema + mark it loaded without rebuilding the temp-tool set.
	lazyCustomToolDefs map[string]AgentToolDef

	// consultCount counts one-shot advice calls spent this turn, across BOTH
	// surfaces that make them (the consult tool and the agent loop's
	// failure-shape guard), so the per-turn cap can't be doubled by using one
	// of each. See consult.go.
	consultCount int

	// agentOwnTools is the set of custom tools UNIQUELY attached to this
	// agent (appended from agent.Tools), excluding ones skipped because
	// they're already in the user's persistent pool. The agent's deliberate
	// kit — first-classed in the lazy split; pool-shared tools stay lazy.
	agentOwnTools map[string]bool

	// dispatchableFleet result, memoized for the turn. Computing it lists
	// all agents from the DB; the three consumers (agents prompt block,
	// trigger hints, active-thread block) each called it independently, so
	// it re-listed + re-logged 3× per turn. fleetDone gates the memo (a nil
	// catalog is a valid result, so use a flag rather than a nil check).
	fleetCatalog []AgentRecord
	fleetDone    bool

	// activeWorkspaceID carries the session's active managed-workspace ID
	// ACROSS the per-step / inline sessions of a single turn. Each call to
	// newToolSession() mints a fresh ToolSession whose WorkspaceDir defaults
	// to the per-user root; without this, a workspace(create) isolation
	// switch made in one authoring step is lost in the next step's fresh
	// session — a script written into the isolated workspace then can't be
	// found when a later step tries to run it (the dropped-file symptom
	// Builder hit). newToolSession restores this; the post-call capture in
	// runPlan / runWorkerStep writes back any switch the step performed.
	// Empty = no managed workspace active yet → per-user root (the default).
	activeWorkspaceID string

	// stagedFiles are the workspace files THIS turn's tools created, carried
	// across the per-step sessions the same way activeWorkspaceID is. The
	// delivery backstop needs to know what the turn made, and a turn makes
	// things in several sessions while the workspace root it writes into is
	// shared with every other turn this user has.
	stagedFiles []string
	stagedMu    sync.Mutex

	// Active orchestrator bubble id — set by runPlan's streamHandler
	// when text begins, cleared by onStepHandler at the round
	// boundary. wrapToolsForActivity reads this to attach tool_call
	// and tool_result SSE events to the right bubble so the user
	// sees inline tool affordances on the right message.
	currentMsgID string
	currentMu    sync.Mutex // guards currentMsgID (handler runs in goroutines)

	// Ids of the attachments this turn delivered, kept so the persisted
	// assistant message can point at them and a reloaded thread still shows
	// its pictures. Accumulated at the SSE flush — the one place every
	// delivered image passes through, across the turn's several step
	// sessions — and drained onto the final message. They land under the
	// final bubble rather than the mid-turn one that produced them, which a
	// stored message has no id to distinguish anyway.
	deliveredAtt []string
	attMu        sync.Mutex

	// lastUsage holds the most-recent assistant-turn stats payload
	// (tokens, throughput, elapsed) captured by emitStats. handleSend
	// reads + clears it when persisting the assistant ChatMessage so
	// the saved record carries per-message usage — the AgentLoopPanel
	// then renders the same hover-only stats footer on session reload
	// that it shows live via the SSE stats event.
	lastUsageMu sync.Mutex
	lastUsage   *ChatMessageUsage

	// midTurnBubbles collects every finalized assistant bubble the
	// turn streams BEFORE the final synthesis/respond_directly/question.
	// runPlan's onStepHandler and runWorkerStep's onStep append to it
	// as each round closes with non-empty text; handleSend drains it
	// into sess.Messages immediately before the final assistant message,
	// so a reloaded session replays the same sequence the user saw live
	// instead of only seeing the closing reply (the gap the user
	// reported — "anything it writes mid-turn is lost").
	bubblesMu      sync.Mutex
	midTurnBubbles []ChatMessage

	// pipelineDepth tracks recursion into pipeline-mode sub-agents.
	// Capped at maxPipelineDepth so a pipeline tool calling another
	// pipeline tool calling another doesn't tear through the budget.
	pipelineDepth int

	// dispatchChain carries the IDs of agents already invoked higher
	// up in this dispatch chain. agentsRunAction refuses to dispatch
	// to an agent already in the chain — catches cycles like A→B→A.
	// Includes the current turn's agent ID when propagated to a
	// sub-turn.
	//
	// Its LENGTH is also how deep the chain runs (chatTurn.dispatchHops),
	// which is what all three run targets check against maxDispatchDepth.
	// A separate depth counter used to live here and measured neither
	// thing correctly; see dispatchHops for what it got wrong.
	dispatchChain []string

	// dispatchOrigin carries the dispatch authority of the agent that
	// ORIGINATED this chain, when this turn is itself a sub-run. nil means this
	// turn IS the origin (a human-driven turn, a scheduled fire, a monitor wake).
	//
	// It exists because a dispatch allow-list was only ever a ONE-HOP fence:
	// every check ran against the immediate caller, so A(allow:[B]) → B, then
	// B(allow:[C]) → C, and A reached C while its list said "B and nothing
	// else". Depth and cycle guards both miss this — the chain is short and
	// acyclic; what grows is AUTHORITY. Carried unchanged down every hop and
	// never widened, so a chain can only ever narrow.
	//
	// Same principle the network connector already enforces one field up: a
	// sub-agent can never be more permissive than its host.
	dispatchOrigin *dispatchAuthority

	// agentDispatchCounts caps repeated agents(action="run") dispatches to the
	// SAME target within one user turn (keyed by target agent ID). Distinct from
	// dispatchCounts above (that one keys on (name,args) and only caps cacheable
	// READ tools — agent dispatch is state-mutating, so it's exempt there). It's
	// also distinct from dispatchChain, which measures how DEEP the chain runs
	// and catches cycles, and so never sees a chat agent re-firing agents(run,
	// X) round after round at the same level. This accumulates across the whole
	// turn and is the hard stop for the "answers and runs the app over and
	// over" loop — which the signature-based agent_loop guard also misses
	// because each sub-run returns different text. NOT propagated to
	// sub-turns; resets per user message.
	//
	// Guarded by dispatchMu, NOT toolMu: the cap is a read-modify-write, the
	// three run targets all reach it, and sibling dispatches can be in flight
	// at once. toolMu is held across parts of the tool-call path a dispatch
	// runs inside of, so taking it here would be re-entrant.
	agentDispatchCounts map[string]int
	dispatchMu          sync.Mutex

	// ownerDB / ownerUser are the FLEET view — set on phantom-
	// dispatched (and other foreign-user) runs where the runtime
	// identity (udb / user) is a synthetic per-chat user that owns
	// sessions/memory/facts, but the AGENT RECORDS themselves live
	// in the original owner's per-user DB. Without this split,
	// renderAvailableAgentsBlock + agents(action="run") would scope
	// to the synthetic user's DB and find no peers — leaving the LLM
	// to either hallucinate plausible names ("OSINT Family Tracker")
	// or refuse to dispatch. Unset on direct interactive turns where
	// udb already owns the fleet.
	ownerDB   Database
	ownerUser string

	// requesterName / requesterChannel describe the HUMAN behind this turn when
	// one is identifiable and is not the owner — the contact name and surface on
	// an inbound channel message. Consumed by chatTurn.requester() so a guardrail
	// can name an audience instead of being written for the worst-case asker.
	//
	// requesterName is attacker-controlled (a contact chooses their own display
	// name); it is never what decides the owner classification. Both empty on
	// interactive owner turns and on agent-to-agent dispatch, where there is no
	// third party to name.
	// Deferred authoring catalog — an Author-flagged agent's authoring tools,
	// held out of the inline catalog (which costs ~18.7k tokens on every turn)
	// and served through load_tool instead. THESE MAPS ARE OWNED BY
	// registerLazyAuthoringTools, which creates them; nothing else may create or
	// replace them. That ownership is the lesson of v0.5.692's panic-and-revert:
	// the first attempt borrowed lazyCustomToolDefs, whose lifecycle belongs to
	// setupCustomTools — which runs LATER and rebuilds its maps, so the borrowed
	// entries were first a nil-map write and then, guarded, would have been
	// silently discarded.
	deferredAuthoringDefs   map[string]AgentToolDef
	deferredAuthoringLoaded map[string]bool
	// authoringLazyPrompt is the "Authoring tools (load before use)" index that
	// replaces the deferred catalog in the system prompt. Empty for Builder
	// (inline catalog) and for agents that cannot author.
	authoringLazyPrompt string

	requesterName    string
	requesterChannel string
	// requesterOwnerHandle records that this channel inbound came from the OWNER's
	// own handle, as the transport reported it and the bridge confirmed it. Set
	// only on the channel path, where the runtime identity is synthetic and
	// therefore says nothing about who is actually typing.
	requesterOwnerHandle bool
	// requesterHandle is the transport's raw attribution of who sent this — the
	// phone/email/chat-id an inbound arrived from. Kept alongside the boolean
	// above because the boolean answers only "is this the owner", and an
	// authorized-identities roster has to be matched against the handle itself.
	// Empty everywhere except the channel path. Never set from message content.
	requesterHandle string

	// explorerMode is flipped by the enter_explorer_mode tool when
	// AllowExplorer is set on the agent. While true, the worker
	// loop's StopRound hook stops enforcing the soft cap, so rounds
	// can continue up to the absolute hard cap (explorerHardCap).
	// Resets per worker step.
	explorerMode   bool
	explorerReason string

	// currentRound mirrors the orchestrator loop's round counter so
	// mid-loop tool handlers (e.g. present_build_plan) know where they
	// are in the budget. Updated at the top of every round.
	currentRound int
	// planBudgetCap is the absolute round number the budget is lifted to
	// once Builder presents a build plan: set to currentRound +
	// buildPlanRoundsPerStep × len(steps) when present_build_plan fires.
	// 0 = no plan grant. Lets a plan's EXECUTION phase get its own rounds
	// on top of whatever exploration already cost — so mapping the API
	// doesn't starve the build+verify rounds. Never lowers the budget.
	planBudgetCap int

	// skillsActive is vestigial (always empty) — skills are no longer
	// in-context-activated. Kept because the main knowledge_search passes
	// it as the "scope skills" arg (empty = agent's own corpus only).
	skillsActive []SkillRecord

	// embedMemo caches query embeddings for the duration of ONE turn, keyed by
	// the exact query text — so a query embedded more than once this turn (the
	// recall-hint nudge embeds the user message at prompt-assembly time; a
	// same-turn knowledge_search / memory_search that passes the same text, which
	// the tool docs steer the model to do, would otherwise embed it again) costs
	// a single round-trip. Retrieval tools run in goroutines, so guard with mu.
	embedMemo   map[string][]float32
	embedMemoMu sync.Mutex

	// hintedDocIDs is the set of knowledge doc_ids the recall-hint nudge surfaced
	// THIS turn. fetch_knowledge_doc checks it to log whether a pull acted on a
	// hint — the "did the agent follow the nudge?" half of the recall telemetry
	// loop that tunes the threshold. Populated by renderRecallHints; read under mu.
	hintedDocIDs   map[string]bool
	hintedDocIDsMu sync.Mutex
	// deliveredSkills tracks which skills' instructions have been shown
	// THIS turn (via read_skill, skill_knowledge_search, or trigger
	// injection) so they aren't repeated. Per-turn only — never persisted,
	// so there's no cross-turn state for the LLM to track. Init'd per turn.
	deliveredSkills map[string]bool

	// Per-turn tool log + dedup cache. Wrapped handlers append to
	// the log on first call and short-circuit to the cached result
	// on every subsequent identical call. Subsequent step prompts
	// include the log so the LLM can see "web_search('X latest')
	// intentText is the turn's driving text (the latest user message, a
	// standing mission, a dispatch brief) — what Tier-1 tool elevation
	// matches lazy tool names against so a tool the intent literally names
	// rides the direct catalog this turn. Set by each surface before
	// setupCustomTools runs; empty disables mention-based elevation.
	intentText string
	// already returned Y" and skip the round entirely.
	toolMu    sync.Mutex
	toolCalls []toolCallRecord
	// lastBubbleToolIdx tracks how many tool calls had fired the
	// last time captureMidTurnBubble took a snapshot. Subsequent
	// captures slice from this index forward so each mid-turn bubble
	// only carries the calls that fired AFTER the previous bubble's
	// text — attributing each call to the assistant message that
	// triggered it. persistedToolCalls (the final-message helper)
	// reads from the same index so the final message picks up any
	// trailing calls without double-counting earlier ones.
	lastBubbleToolIdx int

	// userDocsThisTurn flips true when the inbound chat send carried
	// at least one extracted document (PDF/DOCX/audio/text). Used by
	// the consolidation loop-break gate (turnHasFreshExternalContent)
	// alongside CapNetwork tool calls — both count as "new info
	// entered the conversation this turn."
	userDocsThisTurn bool
	// docNames holds the filenames of this turn's attached documents, so
	// behavior-skill glob triggers (e.g. "*.pdf") can match against them.
	docNames  []string
	toolCache map[string]string // canonical(name, args) → result
	// cacheServes counts how many times each cached result has been RE-SERVED
	// this turn. The first cache hit re-serves the full body (compaction's
	// "re-run the tool if you still need it" marker depends on that); the
	// second+ returns a short stub instead. Serving identical bytes again is
	// what turns a repetitive model into a STABLE loop — near-identical
	// context in, identical round out, five times over (the "That's a real
	// concept…" ×5 transcript) — and each re-serve stuffed duplicate result
	// bodies into history (~10K tokens across that loop). The stub changes
	// the context and says stop, breaking the orbit at round 2 instead of
	// waiting for repeatSame to bite at round 6.
	cacheServes map[string]int
	// dispatchCounts tracks how many times each unique (name, args)
	// pair has been DISPATCHED this turn — regardless of whether the
	// result was successful or errored. Distinct from toolCache, which
	// only stores SUCCESSFUL results. Used by the dispatch-cap path
	// to refuse the Nth identical call (default cap = dispatchCallCap)
	// so a loop on a transient-error tool can't burn the round budget
	// by re-dispatching 16 times. Empty until first cap-eligible call.
	dispatchCounts map[string]int
}

// dispatchCallCap is the number of times one (tool name, args) pair
// can be dispatched in a single turn before the wrapper refuses
// further dispatches. Two allows ONE retry for genuinely transient
// errors (503, timeout) while bounding the loop pattern (same URL
// 16 times). Applies only to tools in cacheableTools — pure-read
// tools where re-dispatching identical args genuinely produces the
// same answer. State-mutating tools (tool_def, create_agent, etc.)
// aren't subject to the cap because legitimate workflows may call
// them multiple times with the same args.
const dispatchCallCap = 2

// toolCallRecord is one entry in the per-turn tool log. Used to build
// the "## Tool calls already made this turn" prompt section that every
// step 2+ sees, so the worker stops re-running identical searches.
type toolCallRecord struct {
	Name   string
	Args   map[string]any
	Result string
	Err    string // set when the original call failed
	Cached bool   // true when the wrapper returned a cached body (no fresh dispatch)
}

// isNetworkTool reports whether a registered ChatTool contacts the
// internet. Checks BOTH signals a tool can declare network access:
//   - IsInternetTool() bool — legacy explicit declaration
//   - Caps() containing CapNetwork — modern capability-style
//
// Either signal is sufficient. Tools that declare neither are
// treated as local-only and pass the Private-mode filter.
func isNetworkTool(ct ChatTool) bool {
	if ct == nil {
		return false
	}
	if it, ok := ct.(InternetTool); ok && it.IsInternetTool() {
		return true
	}
	if cp, ok := ct.(CapabilityTool); ok {
		for _, c := range cp.Caps() {
			if c == CapNetwork {
				return true
			}
		}
	}
	return false
}
