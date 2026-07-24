package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
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

// dispatchSystemPrompt assembles the system prompt for an external/channel
// dispatch (RunAgentSync / RunAgentSyncContinuingRich): the agent's context
// (rules + facts) over its OrchestratorPrompt, then the Available-agents/skills
// block, then the "Your custom tools (load before use)" section, then — for a
// Cortex agent answering OUTSIDE its own cortex thread — its recent cortex feed.
// Single source of truth so adding a prompt block (as custom-tools just did)
// can't get appended to one dispatch site and forgotten on the other.
func dispatchSystemPrompt(target AgentRecord, subFacts []MemoryFact, availableBlock, customToolPrompt, sessID string, runtimeDB Database, user string) string {
	sysPrompt := prependAgentContext(target.OrchestratorPrompt, target, subFacts, agentOperatingNotes(runtimeDB, target))
	sysPrompt += availableBlock
	sysPrompt += customToolPrompt
	if target.Cortex && sessID != cortexSessionID(target.ID) {
		sysPrompt += cortexContextBlock(runtimeDB, target.ID)
	}
	// Same per-agent capability guidance the web turn gets, so a capability
	// follows the AGENT to the channel/dispatch surface instead of silently
	// thinning out. Without this, an agent reached over a channel (e.g. an
	// iMessage bridge) ran on a materially thinner prompt than in web chat —
	// the "works in the web UI but not over a channel" drift.
	sysPrompt = appendAgentCapabilityBlocks(sysPrompt, target, runtimeDB, user, false)
	return sysPrompt
}

// appendAgentCapabilityBlocks appends the per-agent capability guidance that
// must follow an agent to ANY surface — the web runPlan turn AND the channel/
// dispatch turn — so behavior doesn't diverge by which code path built the
// prompt. This is THE single place these blocks live; both paths call it, so a
// new capability block added here reaches every surface at once (the tool
// catalog was unified this way via frameworkConversationalTools; this does the
// same for the prompt). Every block self-gates on agent config, so a worker or
// plain chat agent that lacks a capability gets nothing extra. Message-dependent
// content (triggered-skill instructions, per-turn trigger hints) is deliberately
// NOT here — it rides the user message for prompt-cache stability.
func appendAgentCapabilityBlocks(sys string, agent AgentRecord, udb Database, user string, hasPlanSet bool) string {
	// Framework orchestration blocks lifted out of individual seed personas
	// (see framework_prompts.go) so every capable agent inherits them, not
	// just whichever seed happened to hand-author the prose. Gated by
	// capability; splices in ahead of the agent's own plan-guidance addendum.
	// Passes the prompt-so-far so a block a cloned persona already carries
	// isn't injected twice.
	sys += frameworkPromptBlocks(sys, agent, hasPlanSet)
	if g := strings.TrimSpace(agent.PlanGuidance); g != "" {
		sys += "\n\n## Plan guidance\n" + g
	}
	sys += availableSkillsBlock(agent, udb, user)
	sys += searchOrderGuidanceBlock(agent)
	// Plan-first + pre-mortem discipline. On for an explicit PreMortem opt-in AND
	// by default for any Cortex agent: a persistent channel/home-thread presence
	// IS an orchestrator that accomplishes goals over a channel, which is exactly
	// the case this discipline is for (the Wiwee/iMessage case). The block self-
	// scopes to GOALS, so a Cortex agent still handles casual chat directly — the
	// default costs nothing on ordinary messages.
	if agent.PreMortem || agent.Cortex {
		// await_result only mounts for Fleet agents; plan_set only exists on
		// the web runPlan surface (the caller knows which surface it is).
		sys += "\n\n" + preMortemPlanningBlock(hasPlanSet, agent.Fleet)
	}
	// The guidance blocks appended above (search-order guidance, plan/pre-mortem,
	// skills) also hardcode legacy tool names in prose. Re-run the mode-aware
	// rewrite over the full assembled prompt so those are covered too on the
	// interactive surfaces. Idempotent with the prependAgentContext pass — the
	// unified replacements carry no legacy token, so a second run changes nothing.
	return rewriteMemoryToolNames(sys)
}

// resolveDispatchThink decides whether a dispatched agent thinks, the SAME way
// the chat surface does — so an agent reached via a channel, an external
// dispatch, or an inline agents(run) runs with the same default as if invoked
// directly from Agency. Base = the orchestrator route default; the target's
// explicit Think="on"/"off" wins; empty Think falls through to the route default
// rather than a dispatch-only override. Single source of truth: this was
// copy-pasted at three dispatch sites.
func resolveDispatchThink(target AgentRecord) bool {
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}
	switch target.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	return think
}

// sseWriter wraps an SSE-frame destination. Each Send assembles
// one full SSE frame and writes it atomically, so a downstream
// per-Write consumer (Run.Append) captures whole frames.
//
// Two destinations, kept separate on purpose:
//
//   - live: the current HTTP response, what the originating client
//     sees in real time. Optional — run-only mode (after the
//     originator disconnected) leaves this nil.
//   - run: the in-memory Run buffer. Optional — exposed agents and
//     the design endpoint use the response-only path.
//
// Send / SendChatEvent write to BOTH (they're real events worth
// replaying). Ping writes ONLY to live (it's a keepalive comment;
// putting it in the buffer would inflate sequence numbers on the
// server without inflating the client's received-event counter,
// which would break the since=N reconnect protocol).
type sseWriter struct {
	live    io.Writer    // optional; nil = no live client
	flusher http.Flusher // optional; nil for non-flushable destinations
	run     *Run         // optional; nil = no buffer
	mu      sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{live: w, flusher: f}
}

// newTeeSSEWriter writes Send/SendChatEvent frames to BOTH the live
// HTTP response AND the given Run's event buffer. Pings go to live
// only. Used by handleSend so a fresh /api/runs/<id>/stream
// subscriber after a reconnect can replay every real event from any
// sequence number.
func newTeeSSEWriter(w http.ResponseWriter, run *Run) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{live: w, flusher: f, run: run}
}

// detachLive drops the live HTTP-response writer. After this returns,
// emit() writes only to the run buffer. Used by the disconnect
// watchdog in handleSend so a client that navigates away can't wedge
// the loop on a now-dead TCP write. Run-buffer subscribers (a fresh
// /api/runs/<id>/stream client) continue receiving events fine.
//
// Idempotent. Takes the same mutex emit() holds, so it serializes
// cleanly with in-flight writes: any current write completes (or
// errors), then live drops to nil before the next write.
func (s *sseWriter) detachLive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = nil
	s.flusher = nil
}

// emit writes one assembled SSE frame to whichever destinations are
// configured. The toBuffer flag distinguishes real events (true)
// from keepalive comments (false) so pings stay out of the replay
// buffer.
func (s *sseWriter) emit(frame []byte, toBuffer bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != nil {
		_, _ = s.live.Write(frame)
		if s.flusher != nil {
			s.flusher.Flush()
		}
	}
	if toBuffer && s.run != nil {
		s.run.Append(frame)
	}
}

// Send writes one SSE event in the AgentLoopPanel protocol shape:
// `data: <json>\n\n`. Buffered for replay.
func (s *sseWriter) Send(payload map[string]any) {
	if s == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := make([]byte, 0, len(body)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, body...)
	frame = append(frame, '\n', '\n')
	s.emit(frame, true)
}

// SendChatEvent writes an SSE event in the ChatPanel runtime's format:
// `event: <type>\ndata: <json>\n\n`. ChatPanel's parser dispatches on
// the SSE event-name and ignores any frame without one. Used by the
// design endpoint (which mounts a ChatPanel); AgentLoopPanel uses
// Send() — different parser, different shape, kept distinct so
// callers pick the one matching their UI primitive.
func (s *sseWriter) SendChatEvent(eventType string, payload map[string]any) {
	if s == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := make([]byte, 0, len(body)+len(eventType)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, eventType...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, body...)
	frame = append(frame, '\n', '\n')
	s.emit(frame, true)
}

// Ping writes an SSE comment line (`: keepalive\n\n`) to the live
// destination only. Comments are silently dropped by EventSource
// clients but keep the TCP connection alive through proxies / CDNs
// that close idle streams.
func (s *sseWriter) Ping() {
	if s == nil {
		return
	}
	s.emit([]byte(": keepalive\n\n"), false)
}

// startKeepalive fires SSE comment pings every 10 seconds until the
// returned stop function is called. Use during long LLM calls so the
// connection stays open through nginx/CDN proxy_read_timeout (60s
// default in nginx, 100s in CloudFlare).
func startKeepalive(sse *sseWriter) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sse.Ping()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

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
	app   *OrchestrateApp
	ctx   context.Context
	sse   *sseWriter
	udb   Database
	user  string
	agent AgentRecord
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
	// viewer (its proxy stamped the bridge key). Gates the from_client.* tool
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

	// Active orchestrator bubble id — set by runPlan's streamHandler
	// when text begins, cleared by onStepHandler at the round
	// boundary. wrapToolsForActivity reads this to attach tool_call
	// and tool_result SSE events to the right bubble so the user
	// sees inline tool affordances on the right message.
	currentMsgID string
	currentMu    sync.Mutex // guards currentMsgID (handler runs in goroutines)

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

	// dispatchDepth tracks recursion into dispatch_to_agent. Distinct
	// from pipelineDepth because they're separate failure modes —
	// pipelines compose tools, dispatch fans out to named specialists.
	// Both are capped to prevent runaway recursive chains.
	dispatchDepth int

	// dispatchChain carries the IDs of agents already invoked higher
	// up in this dispatch chain. agentsRunAction refuses to dispatch
	// to an agent already in the chain — catches cycles like A→B→A
	// that depth alone misses (depth resets to 0 on each sub-turn,
	// so A→B→A→B→… would have looked depth-fine for a long time
	// before the cap tripped). Includes the current turn's agent ID
	// when propagated to a sub-turn.
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
	// also distinct from dispatchDepth (a recursion guard that decrements as each
	// sub-run returns, so it never sees a chat agent re-firing agents(run, X)
	// round after round at the same level) and dispatchChain (cycles only). This
	// accumulates across the whole turn and is the hard stop for the "answers and
	// runs the app over and over" loop — which the signature-based agent_loop
	// guard also misses because each sub-run returns different text. NOT
	// propagated to sub-turns; resets per user message.
	agentDispatchCounts map[string]int

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

// (turnHasFreshExternalContent retired alongside the synthesis
// auto-ingest path. Reference Memory is now explicit-save only —
// the LLM calls memory_save when it consciously wants to record
// something, no framework-driven capture. The loop-break gate
// this function fed isn't needed anymore.)

// renderTriggeredSkills injects the FULL instructions of any allowed skill
// the LLM DREW ON this turn (read_skill / skill_knowledge_search →
// t.deliveredSkills), so a consulted skill's lens governs the whole turn —
// the synthesis reply especially, which builds a fresh system prompt. A
// mere trigger MATCH no longer injects here; it surfaces as a soft nudge
// via renderSkillTriggerHints. Empty when DisableSkills, no allowlist, or
// nothing was consulted.
func (t *chatTurn) renderTriggeredSkills() string {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(t.agent.AllowedSkills))
	for _, id := range t.agent.AllowedSkills {
		allowed[id] = true
	}
	var b strings.Builder
	for _, s := range LoadSkills(t.udb, t.user) {
		if s.Disabled || !allowed[s.ID] {
			continue
		}
		if !t.deliveredSkills[s.ID] {
			continue
		}
		b.WriteString(SkillPromptSection(s))
	}
	return b.String()
}

// renderSkillTriggerHints surfaces a soft HINT for allowed skills whose
// triggers match this turn (last user message + attached doc filenames for
// glob triggers like "*.pdf") but that the LLM hasn't consulted yet — a
// nudge to reach for them, not an injection. Skills already delivered this
// turn are skipped (no point hinting what's already loaded). The match is a
// relevance signal; the LLM still decides.
func (t *chatTurn) renderSkillTriggerHints(userMsg string) string {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(t.agent.AllowedSkills))
	for _, id := range t.agent.AllowedSkills {
		allowed[id] = true
	}
	var names []string
	for _, s := range LoadSkills(t.udb, t.user) {
		if s.Disabled || !allowed[s.ID] || t.deliveredSkills[s.ID] {
			continue
		}
		if SkillTriggersMatch(s, userMsg, t.docNames) {
			names = append(names, s.Name)
		}
	}
	return SkillTriggerHintBlock(names)
}

// renderAvailableSkillsBlock produces the "Available skills" section —
// every skill the agent can reach (via AllowedSkills), which the LLM
// draws on via read_skill / skill_knowledge_search / skill_knowledge_fetch_doc.
// Empty when DisableSkills, no allowlist, or no allowed skill.
func (t *chatTurn) renderAvailableSkillsBlock() string {
	if t == nil {
		return ""
	}
	return availableSkillsBlock(t.agent, t.udb, t.user)
}

// availableSkillsBlock is the chatTurn-free form so the shared capability
// assembler (used by BOTH the web and channel/dispatch paths) can render it
// without a chatTurn. See appendAgentCapabilityBlocks.
func availableSkillsBlock(agent AgentRecord, udb Database, user string) string {
	if agent.DisableSkills || len(agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(agent.AllowedSkills))
	for _, id := range agent.AllowedSkills {
		allowed[id] = true
	}
	var avail []SkillRecord
	for _, s := range LoadSkills(udb, user) {
		if s.Disabled || !allowed[s.ID] {
			continue
		}
		avail = append(avail, s)
	}
	// Rendering lives in core (shared with phantom); this function only
	// computes the per-agent available set.
	return RenderAvailableSkills(avail)
}

// IsFrameworkToolDef reports whether an AgentToolDef wraps a
// framework-tagged tool. Kept after the per-turn classifier-trim was
// ripped out (find_tools still lives in the registry and uses the
// framework flag to avoid recommending itself).
func IsFrameworkToolDef(td AgentToolDef) bool {
	if ct, ok := LookupChatTool(td.Tool.Name); ok {
		return IsFrameworkTool(ct)
	}
	return false
}

// fleetView returns the (db, user) pair to use for agent-record
// lookups (Available agents block + agents(action="run") dispatch).
// On phantom and other foreign-user runs ownerDB / ownerUser are set
// so the fleet read hits the original owner's per-user DB even
// though session/memory/facts stay scoped to the runtime user. On
// interactive owner-runs where the fields are unset, falls back to
// udb / user — same behavior as before this split.
func (t *chatTurn) fleetView() (Database, string) {
	if t == nil {
		return nil, ""
	}
	if t.ownerDB != nil && t.ownerUser != "" {
		return t.ownerDB, t.ownerUser
	}
	return t.udb, t.user
}

// dispatchableFleet returns the agents this turn's agent may dispatch
// to — the shared source of truth for BOTH the "Available agents"
// prompt block AND the per-agent consult_<name> tools. Empty when the
// current agent can't dispatch at all.
//
// Excludes: the current agent (don't dispatch to yourself), Builder
// (separate routing concern — needs the user directly), and (in default
// mode) Hidden agents. Returns nil for a sub-agent leaf (OwnedBy set —
// no agents tool) or a restricted catalog without the `agents` tool, so
// neither the block nor the consult tools tell an agent to do something
// its catalog physically prevents.
// dispatchableFleet returns this turn's dispatch catalog, computed once and
// memoized on the chatTurn. Its three consumers (available-agents block,
// trigger hints, active-dispatch-threads block) share the result instead of
// each re-listing agents from the DB and re-logging the catalog.
func (t *chatTurn) dispatchableFleet() []AgentRecord {
	if t == nil {
		return nil
	}
	if t.fleetDone {
		return t.fleetCatalog
	}
	t.fleetCatalog = t.computeDispatchableFleet()
	t.fleetDone = true
	return t.fleetCatalog
}

func (t *chatTurn) computeDispatchableFleet() []AgentRecord {
	if t == nil {
		return nil
	}
	// Audit trail (Debug): the available-agents catalog silently not
	// rendering was a costly bug, so every exit point says what happened.
	// Grep "available-agents" to confirm it shows N agents each turn, or
	// catch a suppression (and its reason) if it ever regresses.
	if t.agent.OwnedBy != "" {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — sub-agent leaf (no dispatch surface)", t.agent.ID)
		return nil
	}
	fleetDB, fleetUser := t.fleetView()
	if fleetDB == nil || fleetUser == "" {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — no fleet view (db/user unresolved)", t.agent.ID)
		return nil
	}
	// The `agents` grouped tool is force-added to EVERY non-leaf agent's
	// catalog (see the unconditional knowTools append in the catalog
	// builder — it is NOT gated on AllowedTools). So an explicit
	// AllowedTools list that doesn't happen to name "agents" still HAS
	// dispatch. The old gate here checked for a literal "agents" in
	// AllowedTools and bailed when absent — which silently suppressed the
	// whole catalog for any agent with a materialized tool list (seed-chat
	// after the first tool approval, every custom agent). The tool was
	// present but the model never saw WHAT it could dispatch to, so it
	// fell back to agents(action="list") or just didn't delegate. Gate
	// only on the no-tools sentinel: an agent an admin set to zero tools
	// genuinely shouldn't be told to delegate.
	if isNoToolsSentinel(t.agent.AllowedTools) {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — no-tools sentinel (admin set zero tools)", t.agent.ID)
		return nil
	}
	// Dispatch policy (see AgentRecord.DispatchMode / effectiveDispatchMode):
	// all = any non-hidden; only = allowlist (explicit pick wins over Hidden);
	// except = any non-hidden minus the list; none = nothing. Only count targets
	// that STILL EXIST — a stale (deleted) id must not keep an allowlist in
	// restrict-mode, which would silently hide every other agent. Self-heals a
	// legacy "only" list whose members were all deleted by falling back to all.
	all := listAgents(fleetDB, fleetUser)
	exists := make(map[string]bool, len(all))
	for _, a := range all {
		exists[a.ID] = true
	}
	mode := effectiveDispatchMode(t.agent)
	if mode == dispatchNone {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — dispatch policy is Allow none", t.agent.ID)
		return nil
	}
	listed := map[string]bool{}
	for _, id := range t.agent.AllowedDispatchTargets {
		if exists[id] {
			listed[id] = true
		}
	}
	if mode == dispatchOnly && len(listed) == 0 {
		mode = dispatchAll // self-heal: every allowlisted target was deleted
	}
	var available []AgentRecord
	for _, a := range all {
		if a.ID == t.agent.ID || isBuilderAgent(a.ID) || isFleetRetiredSeed(a.ID) {
			continue
		}
		// A sub-agent owned by ANOTHER agent is private to its owner — never surface
		// it in this agent's fleet view (mirrors the dispatch gate, which refuses to
		// dispatch to it). The owner still sees its own sub-agents per the Hidden /
		// mode rules below.
		if a.OwnedBy != "" && a.OwnedBy != t.agent.ID {
			continue
		}
		switch mode {
		case dispatchOnly:
			if !listed[a.ID] {
				continue
			}
		case dispatchExcept:
			if listed[a.ID] || a.Hidden {
				continue
			}
		default: // dispatchAll
			if a.Hidden {
				continue
			}
		}
		available = append(available, a)
	}
	if len(available) == 0 {
		Debug("[orchestrate] available-agents: 0 in catalog for agent=%q — no OTHER dispatchable agents in the fleet (not a bug if the user has none)", t.agent.ID)
	} else {
		names := make([]string, 0, len(available))
		for _, a := range available {
			names = append(names, a.Name)
		}
		Debug("[orchestrate] available-agents: %d in catalog for agent=%q this turn: %s", len(available), t.agent.ID, strings.Join(names, ", "))
	}
	return available
}

// renderAvailableAgentsBlock surfaces the OTHER agents in the user's
// fleet so the host LLM knows what it can dispatch to via
// agents(action="run", agent=..., message=...). Without this block
// the LLM has to call agents(action="list") first to discover them,
// which it almost never does speculatively — the result is that
// authored specialist agents (Pickleball Expert, Code Reviewer,
// etc.) are effectively invisible. Empty when the user has no other
// agents or the current agent is the only one.
func (t *chatTurn) renderAvailableAgentsBlock() string {
	if t == nil {
		return ""
	}
	available := t.dispatchableFleet()
	if len(available) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available agents\n\n")
	b.WriteString("Specialists the user has authored. **If a question lands in one of these agents' domains, DELEGATE FIRST.** Rely on the agent for the work it's built for — when the use case fits it gives the best result: its own persona, tools, and grounded sources beat your general knowledge. This holds EVEN WHEN you could handle it with your own tools — for a question in a listed agent's domain, delegate rather than web_searching it yourself; a tool call is not a substitute for the specialist. Dispatch as your FIRST move on such a question — don't run several of your own searches and fall back to the agent only when they come up short; the specialist IS the move, not the backup. Answer yourself only when no agent's domain fits — NOT because you feel you already know it or could look it up. And don't narrate that you'll consult an agent and then answer anyway: either dispatch, or answer plainly as you.\n\nDelegate via `agents(action=\"run\", agent=\"<name>\", message=\"<the brief>\")`. **The agent remembers within this session.** It re-threads your prior dispatches to it this session (ephemeral continuity) on top of its own persona, saved facts, and knowledge base, so a follow-up to the same agent can be brief without repeating earlier context. A RELATED FOLLOW-UP goes back to the SAME agent; don't interpret or answer it yourself from the earlier result. This dispatch memory is ephemeral, scoped to this session. Re-dispatch, including the prior context in the brief: \"Earlier you summarized Acme Corp as <X>. Now tell me more about their B2B presence.\" You own the context; the sub-agent answers the question in front of it.\n\nIntegrate the answers into your reply as if they were your own — don't say \"I asked X\" or \"the X agent said\"; the user doesn't know the fleet structure. Just answer with the substance.\n\nFormat: **name** — when to delegate.\n\n")
	for _, a := range available {
		// Full description — it's the routing cue (descriptions are
		// model-facing, per the Builder guidance), shown un-truncated so
		// no part of the cue is hidden from the dispatch decision.
		desc := strings.TrimSpace(a.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(a.Name)
		b.WriteString("** — ")
		b.WriteString(desc)
		// Deterministic dispatch contract: a sub-agent gets its structured
		// input from the parent's brief, not a form (intake_form isn't applied
		// on dispatch), so an agent that declares an intake_form is telling us
		// exactly what its brief needs. Surface those field labels so the
		// orchestrator packs them up front instead of dispatching a vague brief
		// and forcing a clarifying round-trip. Derived at render time from the
		// agent's CURRENT spec — nothing stored, never stale.
		b.WriteString(dispatchBriefHint(a))
		b.WriteString("\n")
	}
	return b.String()
}

// dispatchBriefHint returns a one-line "put this in the brief" cue built from an
// agent's intake_form field labels, or "" when it declares none. Required
// fields are marked; a file field asks for the document's text (a text brief
// can't carry an upload); button fields are skipped (self-submitting actions,
// not inputs to supply).
func dispatchBriefHint(a AgentRecord) string {
	if len(a.IntakeForm) == 0 {
		return ""
	}
	var parts []string
	for _, f := range a.IntakeForm {
		if f.Type == "button" {
			continue
		}
		label := strings.TrimSpace(f.Label)
		if label == "" {
			label = strings.TrimSpace(f.Name)
		}
		if label == "" {
			continue
		}
		if f.Type == "file" {
			label += " (as document text)"
		}
		if f.Required {
			label += " (required)"
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return ""
	}
	return " When you dispatch, include in the brief: " + strings.Join(parts, ", ") + "."
}

// renderAgentTriggerHints surfaces a SOFT per-turn nudge for dispatchable
// agents whose Triggers match this turn — "this turn is <agent>'s domain,
// dispatch FIRST" — placed right after the catalog (near the user
// message, the highest-salience spot). The static Available-agents block
// alone doesn't bind a model with strong priors in the domain: it reads
// the block, agrees, and web_searches anyway (observed on PC-372 legal
// Qs). A trigger match is a relevance signal, not a command — a wrong
// hint is one line the model ignores. Mirrors the skill trigger-hint
// mechanism. Empty when nothing matches or the agent can't dispatch.
func (t *chatTurn) renderAgentTriggerHints(userMsg string) string {
	if t == nil {
		return ""
	}
	fleet := t.dispatchableFleet()
	if len(fleet) == 0 {
		return ""
	}
	var names []string
	for _, a := range fleet {
		if TriggersMatch(a.Triggers, userMsg, t.docNames) {
			names = append(names, a.Name)
		}
	}
	return agentTriggerHintBlock(names)
}

// agentTriggerHintBlock formats the per-turn dispatch nudge for the given
// agent names. Empty names → "". Mirrors SkillTriggerHintBlock's shape.
func agentTriggerHintBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "**" + n + "**"
	}
	return "\n\n[Likely this turn (agent triggers matched): the question lands in " + strings.Join(quoted, ", ") + "'s domain — dispatch to it via agents(action=\"run\", agent=\"<name>\", message=\"<brief>\") as your FIRST move, before web_search or answering from memory. A trigger match is a strong nudge, not a command: skip it only if it plainly doesn't fit.]\n\n"
}

// renderActiveDispatchThreads surfaces the agents this session has ALREADY
// dispatched to, so the host LLM knows it has live, re-threadable
// conversations open. The dispatch continuity (the prior exchange) lives
// only on the sub-agent side (dispatch:<sess>:<agentID>); the host's own
// history hides delegation entirely ("integrate as your own, don't say 'I
// asked X'"), so without this cue a follow-up like "tell me more" reads to
// the host as a question it can answer from its own memory — and it does,
// instead of re-dispatching to the agent that actually has the context. The
// trigger-hint nudge doesn't cover this: a generic follow-up rarely matches
// the original agent's keyword triggers. This block is the missing signal:
// name the open threads + the directive to send follow-ups back to the same
// agent. Empty when this session has no open dispatch threads.
func (t *chatTurn) renderActiveDispatchThreads() string {
	if t == nil || t.udb == nil || t.session == nil || t.session.ID == "" {
		return ""
	}
	fleet := t.dispatchableFleet()
	if len(fleet) == 0 {
		return ""
	}
	var parts []string
	for _, a := range fleet {
		subSessID := "dispatch:" + t.session.ID + ":" + a.ID
		sess, ok := loadChatSession(t.udb, a.ID, subSessID)
		if !ok || len(sess.Messages) == 0 {
			continue
		}
		part := "**" + a.Name + "**"
		if topic := lastDispatchTopic(sess.Messages); topic != "" {
			part += " (last: " + topic + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n[Active dispatch threads (this session): you've already delegated to " +
		strings.Join(parts, "; ") +
		". If the user's message is a FOLLOW-UP to one of these — \"tell me more\", \"what about X\", drilling into the same topic — re-dispatch it to that SAME agent with the prior context via agents(action=\"run\", agent=\"<name>\", message=\"<brief>\"); do NOT answer it yourself from the earlier result. You delegated it before because it's that agent's domain — that hasn't changed, and the agent re-threads its own prior turns so a brief follow-up is enough.]\n\n"
}

// lastDispatchTopic returns a short label for a dispatch thread — the most
// recent user brief sent to that agent, with the delegation marker stripped
// and truncated. Used only for the active-threads hint so the host can tell
// which open thread a follow-up belongs to. Empty when no user message.
func lastDispatchTopic(msgs []ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		topic := strings.TrimSpace(msgs[i].Content)
		// Briefs are wrapped by markAsDelegated as "[DELEGATED INVOCATION]
		// …\n\nBrief: <text>"; show just the brief.
		if idx := strings.LastIndex(topic, "Brief: "); idx >= 0 {
			topic = strings.TrimSpace(topic[idx+len("Brief: "):])
		}
		topic = strings.ReplaceAll(topic, "\n", " ")
		if len(topic) > 80 {
			topic = strings.TrimSpace(topic[:80]) + "…"
		}
		return topic
	}
	return ""
}

// renderBuilderExistingToolsBlock lists the user's persistent (admin-
// approved) custom tools as READ-ONLY awareness for Builder. Builder's
// executable catalog hides these on purpose — see the persistent-tool
// load skip in newToolSession — so the LLM can't accidentally "use" a
// pre-existing tool when it should be authoring a new one. But Builder
// still needs to KNOW what exists so it can:
//
//   - Spot name collisions ("user wants a news_summary tool — does one
//     already exist?")
//   - Pick the iteration path when authoring with an existing name
//     (re-author with same name overwrites the active entry in place,
//     no admin re-approval needed — handled by UpdatePersistentTempTool
//     from the queueForReview path)
//   - Reference an existing tool by name in pipeline_tools (pipeline
//     mode resolves by name at dispatch, doesn't need the tool in the
//     executable catalog)
//
// Returns empty when there are no persistent custom tools or when the
// current agent isn't Builder. Format mirrors renderAvailableAgentsBlock
// — one bullet per tool, name + one-line description.
func (t *chatTurn) renderBuilderExistingToolsBlock() string {
	if t == nil || t.udb == nil || t.user == "" {
		return ""
	}
	if !isBuilderAgent(t.agent.ID) {
		return ""
	}
	persistent := LoadPersistentTempTools(t.udb, t.user)
	if len(persistent) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Existing custom tools in this user's environment (READ-ONLY for awareness)\n\n")
	b.WriteString("Tools the user already authored + an admin approved. **These are NOT in your executable catalog — you cannot dispatch them.** They're listed here so you can: (a) check for name collisions before authoring (re-authoring with an existing name OVERWRITES the active entry — no admin re-approval needed, that's the canonical iteration path); (b) reference one in pipeline_tools when composing (pipeline mode resolves by name at dispatch, doesn't need the tool in your callable catalog).\n\nFormat: **name** (mode) — one-line description.\n\n")
	for _, p := range persistent {
		desc := strings.TrimSpace(p.Tool.Description)
		if len(desc) > 140 {
			desc = desc[:140] + "…"
		}
		if desc == "" {
			desc = "(no description)"
		}
		mode := strings.TrimSpace(p.Tool.Mode)
		if mode == "" {
			mode = "shell"
		}
		b.WriteString("- **")
		b.WriteString(p.Tool.Name)
		b.WriteString("** (")
		b.WriteString(mode)
		b.WriteString(") — ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	return b.String()
}

// renderKnownTopicsBlock lists the snake_case topic slugs this
// (user, agent) has already used for memory_save. Surfaced so the
// LLM reuses an existing slug instead of minting near-duplicates
// when it calls memory_save / memory_search. Replaces the per-turn
// classifier — the LLM picks the slug now, not a side worker call.
// Empty when there's no accumulator yet.
func (t *chatTurn) renderKnownTopicsBlock() string {
	if t == nil || t.app == nil || t.app.DB == nil {
		return ""
	}
	topics := listAgentTopics(t.app.DB, t.user, t.agent.ID)
	if len(topics) == 0 {
		return ""
	}
	// Cap to the most-recently-used slugs (the tail — recordAgentTopic keeps the
	// list recency-ordered). Dropped slugs still work; the agent just mints or
	// reuses them without the hint. 0 = show all.
	if maxN := TuneInt(tuneKnownTopicsMax); maxN > 0 && len(topics) > maxN {
		topics = topics[len(topics)-maxN:]
	}
	var b strings.Builder
	b.WriteString("\n\n## Known topics\n\n")
	saveVerb := "memory(save)"
	if unifiedMemoryEnabled() {
		saveVerb = "remember (findings)"
	}
	fmt.Fprintf(&b, "Snake_case slugs already used for %s in this agent's bucket. Reuse one when the current finding fits — picking a fresh slug for material that belongs alongside existing entries makes retrieval split across buckets that should be one. Mint a new slug only when the subject genuinely doesn't fit.\n\n", saveVerb)
	for _, name := range topics {
		b.WriteString("- ")
		b.WriteString(name)
		b.WriteString("\n")
	}
	return b.String()
}

// skillToolDefs builds the per-turn skill tools (read_skill,
// skill_knowledge_search, skill_knowledge_fetch_doc) gated on the agent's
// skill allowlist. All three are stateless one-shot calls in the agent's
// OWN context — no sub-agent, no activation. The search/fetch callbacks
// reuse the agent's own scoped knowledge tooling so a skill's collections
// search the same way the agent's corpus does; the per-turn deliveredSkills
// set dedupes instruction delivery across read_skill / search / triggers.
// Returns nil when skills are disabled or none are allowed.
func (t *chatTurn) skillToolDefs() []AgentToolDef {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return nil
	}
	allowed := t.agent.AllowedSkills
	return []AgentToolDef{
		BuildReadSkillTool(t.udb, t.user, allowed, t.deliveredSkills),
		BuildSkillKnowledgeSearchTool(t.udb, t.user, allowed, t.deliveredSkills,
			func(skill SkillRecord, query string) string {
				res, _ := t.knowledgeToolDefScoped([]SkillRecord{skill}).Handler(map[string]any{"query": query})
				return res
			}),
		BuildSkillKnowledgeFetchDocTool(t.udb, t.user, allowed, t.deliveredSkills,
			func(skill SkillRecord, docID string) (string, error) {
				return t.fetchKnowledgeDocScoped([]SkillRecord{skill}).Handler(map[string]any{"doc_id": docID})
			}),
	}
}

// roundShapePreamble returns the universal "How this round works"
// framework block that sits ABOVE the agent persona for agents
// without their own detailed phased rhythm. Builder skips it
// entirely — its phases govern the rhythm.
//
// Position above-persona is deliberate: LLMs weight recent prompt
// content heavier, so the persona (more recent) reads as the
// authoritative voice. The preamble is reference material for when
// the persona is silent on a question.
func roundShapePreamble(maxSteps int) string {
	stepBudget := fmt.Sprintf("up to %d step%s", maxSteps, plural(maxSteps))
	return "## How this round works\n\n" +
		"Call tools inline (call → see result → call again → reply; multi-round is fine) or end the round with **ask_user / ask_user_form** (pause for input) or **plan_set** (hand off to fresh-context workers, " + stepBudget + ", min 2, research-style \"investigate A and B in parallel\" — not a wrapper for sequential tool calls). To reply, just write your answer as text; that ends the turn. There is no separate reply tool. The persona below wins on anything it addresses; this is the default otherwise.\n\n" +
		"**Before a tool call, write ONE short sentence in your own voice saying what you're about to do** — \"Let me grab that video.\" / \"Checking your calendar…\" / \"Pulling the latest numbers.\" The user sees it right away, so they're never left watching dead air while the tool runs. Keep it to a sentence. Do NOT write your actual ANSWER before a tool call — that's not the place for it, and you'd just repeat yourself once the result is back. Save the real answer for your final, tool-free reply AFTER you have the results.\n\n" +
		"**Delivering files.** Producer tools (image, video, screenshot_page, custom tools that save a file) write to your workspace and return the path — they do NOT auto-attach. To deliver, follow up with `workspace(action=\"attach\", path=\"<returned-path>\", cleanup=true)`. cleanup=true for one-shot deliveries, cleanup=false when the file is also work product. Multiple files in one turn is fine — chain one workspace(attach) per file.\n\n" +
		"Pure conversation (greetings, opinions, follow-ups already answered): just reply as text.\n\n"
}

// drainNotes pulls all queued interjections and persists them as
// user messages on the session (so reload sees them too). Returns
// the drained slice for the caller to fold into the next prompt.
// Empty when there's nothing queued or no queue at all.
func (t *chatTurn) drainNotes() []injectionNote {
	if t == nil || t.queue == nil {
		return nil
	}
	taken := t.queue.Drain()
	if len(taken) == 0 {
		return nil
	}
	if t.session != nil {
		now := time.Now()
		for _, n := range taken {
			t.session.Messages = append(t.session.Messages, ChatMessage{
				Role: "user", Content: n.Text, Created: now,
			})
		}
		if saved, err := saveChatSession(t.udb, *t.session); err == nil {
			*t.session = saved
		}
	}
	ids := make([]string, len(taken))
	for i, n := range taken {
		ids[i] = n.ID
	}
	// Tell the client to mark these interjection bubbles as consumed
	// (servitor's pattern — the framework runtime already tags the
	// bubbles with data-note-id on submit).
	t.sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_notes_consumed",
		"ids":  ids,
	})
	return taken
}

// notesContextBlock formats a drained slice for prepending to a
// round's user content. Returns empty string when nothing was drained
// so callers can append unconditionally.
func notesContextBlock(notes []injectionNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## User notes added mid-flight\n")
	b.WriteString("The user added the following notes after the turn started. Treat as additional context / direction:\n\n")
	for _, n := range notes {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(n.Text), "\n", " "))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// newToolSession constructs the per-turn ToolSession, then loads the
// user's approved persistent temp tools onto it so the LLM sees them
// alongside the built-in registry. Private mode mirrors the chat
// app's policy: shell-mode persistent tools stay (no network), API-
// mode persistent tools (HTTP calls) get dropped.
//
// sess.DB is the user-scoped sub-store (t.udb), not the app's root
// DB — agent-CRUD tools (create_agent / update_agent / etc.) write
// AgentRecord entries via this DB, and listAgents in the chat page
// reads from the user-scoped store. Using the app DB here would save
// agents to a bucket nobody reads from, making them invisible.
//
// Shared by runPlan (orchestrator's inline tool surface) and
// runWorkerStep (worker step) so both paths get the same persistent
// tool pool without drift.
// captureActiveWorkspace records a managed-workspace switch a session
// performed (via workspace create/use, which set sess.WorkspaceID) so
// the next newToolSession() this turn restores the same workspace.
// No-op when the session stayed on the per-user root (WorkspaceID
// empty) — that case keeps activeWorkspaceID empty so we don't pin a
// transient root path. Safe to call via defer on any session.
func (t *chatTurn) captureActiveWorkspace(sess *ToolSession) {
	if sess != nil && sess.WorkspaceID != "" {
		t.activeWorkspaceID = sess.WorkspaceID
	}
}

// loadAgentTempTools hydrates sess.TempTools with the agent's custom (temp)
// tools and records the agent's uniquely-attached kit in t.agentOwnTools. Two
// sources: the user's approved PERSISTENT pool (drawn from poolUser/poolDB — the
// AGENT OWNER, so a channel/dispatch run under a synthetic runtime user still
// gets the owner's tools, not the empty pool of "phantom:<chat>") gated by the
// agent's AllowedTools allow-list + per-agent deny list + private mode + the
// Builder-authors-fresh skip; and the agent-scoped AgentRecord.Tools kit (no
// allow-list gate — attached directly to the record). Extracted from
// newToolSession so the web and channel/dispatch surfaces hydrate IDENTICALLY:
// without this, a dispatched/channel session built its own ToolSession and
// loaded ZERO custom tools, so an agent-authored tool (e.g. ts3_client_status)
// worked in the web chat but was invisible over a channel.
func (t *chatTurn) loadAgentTempTools(sess *ToolSession, poolUser string, poolDB Database) {
	if sess == nil || poolDB == nil || poolUser == "" {
		return
	}
	noTools := isNoToolsSentinel(t.agent.AllowedTools)
	// Treat any agent backed by an in-code seed as a seed regardless of
	// whether its per-user shadow carries Owner==seedOwner. The seed-chat
	// shadow may have Owner==username when the user saved customizations
	// through the admin UI, but the AllowedTools gate must still be nil so
	// all approved persistent tools (including toolbox-mode tools like vapi
	// that bypass the OnTempToolApproved path) auto-load.
	_, isSeedBacked := seedAgentByID(t.agent.ID)
	isSeed := t.agent.Owner == seedOwner || isSeedBacked
	disabledPersistent := make(map[string]bool, len(t.agent.DisabledPersistentTools))
	for _, n := range t.agent.DisabledPersistentTools {
		disabledPersistent[n] = true
	}
	// User-crafted agents only: an explicit AllowedTools list still gates
	// persistent temp tools. Empty list = default pool = include all. Nil for
	// seed agents (admin approval is enough).
	var allowPersistent map[string]bool
	if !isSeed && !noTools && len(t.agent.AllowedTools) > 0 {
		allowPersistent = make(map[string]bool, len(t.agent.AllowedTools))
		for _, n := range t.agent.AllowedTools {
			allowPersistent[canonicalToolName(n)] = true
		}
	}
	// The Builder loads the full tool pool like every other agent. It used to
	// "author fresh" — skipping the user's existing tools from its EXECUTABLE
	// catalog (enumerate-only via tool_def(action="list")) — but the Builder's job
	// is to build AND verify, so it needs to actually run existing tools: call an
	// api tool it just wrote, test one that dispatches through a credential, etc.
	// This is orthogonal to Secured credentials: a secured cred still exposes no
	// generic call tool (no improvising) and still refuses NEW tools that declare
	// it (the authoring gate); the Builder only gains the ability to use the
	// EXISTING declaring tools, which is the point.
	//
	// Deployment-shared (global) persistent tools are OPT-IN: a Shared tool loads
	// for this user's agents only once the user has adopted it from the global-
	// tool catalog (Account page). The user's own pool always loads; the shared
	// pool is filtered to the user's adoption set, deduped by name (own copy
	// wins). The per-agent gates below (private-mode, disabled list, user-crafted
	// allow-list) then apply uniformly. Existing users were grandfathered into
	// their previously auto-loaded shared tools by migrateGlobalToolAdoption.
	loaded := LoadPersistentTempTools(poolDB, poolUser)
	own := make(map[string]bool, len(loaded))
	for _, p := range loaded {
		own[p.Tool.Name] = true
	}
	adoptedGlobal := LoadAdoptedGlobalTools(poolDB, poolUser)
	for _, p := range LoadSharedPersistentTempTools(poolDB) {
		if !own[p.Tool.Name] && adoptedGlobal[p.Tool.Name] {
			loaded = append(loaded, p)
		}
	}
	for _, p := range loaded {
		if noTools {
			continue
		}
		// Governance visibility: a Disabled tool (turned off in My tools) and a
		// Builder-only tool are both hidden from every agent EXCEPT Builder.
		// Builder always loads the full pool so it can load, RUN, test, fix, and
		// re-enable them — building AND verifying is its whole job; a disabled
		// tool it couldn't run would be unfixable.
		if (p.Tool.Disabled || p.Tool.BuilderOnly) && !isBuilderAgent(t.agent.ID) {
			continue
		}
		// Private mode hides API-mode temp tools (network side effects);
		// shell-mode stays (sandboxed local).
		if t.privateMode && p.Tool.Mode == TempToolModeAPI {
			continue
		}
		if disabledPersistent[p.Tool.Name] { // per-agent opt-out
			continue
		}
		if allowPersistent != nil && !allowPersistent[p.Tool.Name] { // user-crafted allow-list
			continue
		}
		tool := p.Tool
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[orchestrate.tools] persistent temp tool %q failed to load: %v", tool.Name, err)
		}
	}
	if n := len(loaded); n > 0 {
		Log("[orchestrate.tools] loaded %d persistent temp tool(s) for %s", n, poolUser)
	}
	// Agent-scoped tools (AgentRecord.Tools) layer on top — attached directly to
	// the record, so NO AllowedTools gate; deduped against the persistent pool.
	agentToolsSkipped := 0
	if t.agentOwnTools == nil {
		t.agentOwnTools = map[string]bool{}
	}
	for i := range t.agent.Tools {
		tool := t.agent.Tools[i]
		if t.privateMode && tool.Mode == TempToolModeAPI {
			continue
		}
		// Per-agent disable: a bundled tool the user turned OFF for this agent
		// (from the tool's Access panel) stays on the record — so its definition
		// survives and Builder can still repair it in place — but doesn't load
		// into the agent's kit. Non-destructive, unlike unscoping.
		if tool.Disabled {
			agentToolsSkipped++
			continue
		}
		if sess.HasTempTool(tool.Name) {
			agentToolsSkipped++
			continue
		}
		t.agentOwnTools[tool.Name] = true // the agent's deliberate kit (first-classed in setupCustomTools)
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[orchestrate.tools] agent-scoped tool %q failed to load: %v", tool.Name, err)
		}
	}
	if agentToolsSkipped > 0 {
		Debug("[orchestrate.tools] %d agent-scoped tool(s) already present from the persistent pool (not re-loaded) for agent=%s", agentToolsSkipped, t.agent.ID)
	}
	if n := len(t.agentOwnTools); n > 0 {
		Log("[orchestrate.tools] attached %d uniquely-agent-scoped tool(s) for agent=%s", n, t.agent.ID)
	}
	// Expose the bundled set + an unbundle path to tool_def so a
	// record-attached tool is legible as such (list/get tag it) and
	// actually removable (delete routes through the agent record). Every
	// tool in agent.Tools is bundled, even the ones already covered by
	// the persistent pool — the point is that tool_def's delete must
	// reach the RECORD, not just the session copy. Scope the callback to
	// this agent + owner so a delete can't touch another agent's kit.
	// Wire the agent-scope authoring + bundle-management callbacks, all
	// owner-scoped to THIS agent: BundleTool attaches a newly authored
	// tool to the record, UnbundleTool removes one, BundledToolNames tags
	// the already-bundled set. CanScopeGlobal gates whether tool_def may
	// instead persist a tool user-wide — Builder only; every other agent
	// is forced to agent scope. BundleTool + CanScopeGlobal are wired
	// unconditionally (even from an empty Tools[]) so a FIRST agent-scoped
	// tool has a durable home; BundledToolNames only matters once the
	// record already carries tools.
	base := t.agent
	agentID, owner, poolDBRef := t.agent.ID, poolUser, poolDB
	sess.CanScopeGlobal = isBuilderAgent(agentID)
	sess.BundleTool = func(tt TempTool) error {
		return bundleAgentTool(poolDBRef, owner, base, tt)
	}
	sess.UnbundleTool = func(name string) error {
		return unbundleAgentTool(poolDBRef, owner, agentID, name)
	}
	if len(t.agent.Tools) > 0 {
		bundled := make(map[string]bool, len(t.agent.Tools))
		for i := range t.agent.Tools {
			bundled[t.agent.Tools[i].Name] = true
		}
		sess.BundledToolNames = bundled
	}
}

// unbundleAgentTool removes a tool from an agent's record-attached kit
// (AgentRecord.Tools) and persists — the durable half of tool_def's
// delete for an agent-bundled ("zombie") tool. Without it, delete drops
// only the session copy and the record reconstitutes the tool next
// turn. Owner-scoped through the same load/save path the editor uses.
func unbundleAgentTool(db Database, owner, agentID, name string) error {
	if db == nil {
		return fmt.Errorf("no db")
	}
	rec, ok := loadAgent(db, agentID)
	if !ok {
		return fmt.Errorf("agent %q not found", agentID)
	}
	if rec.Owner != "" && owner != "" && rec.Owner != owner {
		return fmt.Errorf("not your agent")
	}
	kept := rec.Tools[:0]
	found := false
	for _, tl := range rec.Tools {
		if tl.Name == name {
			found = true
			continue
		}
		kept = append(kept, tl)
	}
	if !found {
		return fmt.Errorf("tool %q is not bundled on agent %q", name, rec.Name)
	}
	rec.Tools = kept
	_, err := saveAgent(db, rec)
	return err
}

// bundleAgentTool attaches (or replaces by name) a tool on an agent's
// record-attached kit (AgentRecord.Tools) and persists — the durable
// half of tool_def(create) at agent scope and the wired target for
// sess.BundleTool. Mirrors unbundleAgentTool: owner-scoped through the
// same load/save path so an agent can only grow its OWN kit. When the
// agent has no saved record yet (a first-ever agent-scoped tool on a
// seed persona), it shadows the in-memory base record so the tool still
// gets a durable home.
func bundleAgentTool(db Database, owner string, base AgentRecord, t TempTool) error {
	if db == nil {
		return fmt.Errorf("no db")
	}
	// App agents never hold LLM-authored tools — their kit is app-declared. This
	// is the runtime chokepoint (an app agent authoring a tool mid-session goes
	// through here); refuse regardless of caller. Keyed on the registry so a
	// drift-able Owner can't slip a tool onto an app agent.
	if isAppAgent(base.ID) {
		return fmt.Errorf("cannot bundle a tool onto app agent %q — app agents get their tools from the owning app, not the LLM-authored plane", base.Name)
	}
	rec, ok := loadAgent(db, base.ID)
	if !ok {
		rec = base
		if rec.Owner == "" {
			rec.Owner = owner
		}
	}
	if rec.Owner != "" && owner != "" && rec.Owner != owner {
		return fmt.Errorf("not your agent")
	}
	replaced := false
	for i := range rec.Tools {
		if rec.Tools[i].Name == t.Name {
			rec.Tools[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		rec.Tools = append(rec.Tools, t)
	}
	_, err := saveAgent(db, rec)
	return err
}

// wireLiveCallbacks attaches the mid-turn user-facing hooks (send_status and the
// per-user OAuth Connect prompt) to a ToolSession — the main turn session AND any
// sub-agent session (pipeline stages) that should reach the live conversation.
// Both only fire when this turn has a live SSE stream: a standing-agent / wake
// turn has no watcher, so the tools fall through to their no-op guidance instead
// of emitting into a dead writer.
func (t *chatTurn) wireLiveCallbacks(sess *ToolSession) {
	if sess == nil || t.sse == nil {
		return
	}
	// send_status → a PERSISTENT muted line in the conversation flow
	// (kind:status_note → convoLog), above the eventual reply — NOT the topbar
	// status bar, which is cleared on 'done' so a mid-turn status would vanish
	// before the user could read it.
	sess.StatusCallback = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		t.sse.Send(map[string]any{"kind": "status_note", "text": text})
	}
	// A tool needs the user to authorize a per-user OAuth integration (e.g. an
	// MCP server backing this agent/pipeline). Emit a connect_required block: the
	// client renders a Connect button that opens the consent popup at the account
	// connect endpoint, so the user authorizes in-place and retries — no trip to
	// the Account page. Site-absolute path since chat runs under a different app
	// root than /account.
	sess.ConnectPrompt = func(server string) {
		server = strings.TrimSpace(server)
		if server == "" {
			return
		}
		t.sse.Send(map[string]any{
			"kind":   "block",
			"type":   "connect_required",
			"id":     fmt.Sprintf("connect-%d", time.Now().UnixNano()),
			"server": server,
			"url":    "/account/mcp/connect?server=" + url.QueryEscape(server),
		})
	}
	// Inline sub-agent approval card — surfaces a drafted-but-held sub-agent as an
	// Approve/Deny card right in the conversation, wired to the same authorization
	// the Permissions pane shows. See ToolSession.ApprovalPrompt / create_agent.
	sess.ApprovalPrompt = func(authID, agentName, brief string) {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			return
		}
		t.sse.Send(map[string]any{
			"kind":    "block",
			"type":    "agent_approval",
			"id":      "agent-approval-" + authID,
			"auth_id": authID,
			"name":    agentName,
			"brief":   brief,
		})
	}
}

func (t *chatTurn) newToolSession() *ToolSession {
	sess := &ToolSession{
		LLM:      t.app.LLM,
		LeadLLM:  t.app.LeadLLM,
		Username: t.user,
		DB:       t.udb,
		// Whose turn this is — an authoring tool commits what it writes to
		// this agent rather than to a per-chat-session pool.
		AgentID: t.agent.ID,
		// …unless the turn is authoring FOR another agent, which is Builder's
		// whole job. Read live: focus moves when get_agent / create_agent runs.
		AuthoringAgentFn: func() string {
			if t.session != nil {
				return t.session.AuthoringAgentID
			}
			return ""
		},
		// Network connector — the SAME instance carried on t.ctx +
		// the inflight registry. Sharing one pointer is what makes
		// the mid-turn cutoff propagate: when the privacy endpoint
		// flips it, sess.NetworkAllowed() and
		// NetworkAllowedFromContext(ctx) both see the new state.
		Network: t.network,
		// Turn context — tools that spawn synchronous sub-runs (delegate)
		// root them here via sess.Context() so a Stop / cancel of this
		// turn also cancels the outgoing agent call instead of leaving it
		// detached on context.Background().
		Ctx: t.ctx,
		// Pipeline-mode temp tools dispatch through this runner. Caps
		// recursion depth via t.pipelineDepth (incremented on entry,
		// decremented on exit) so pipeline-calls-pipeline can't
		// infinite-loop. See runPipelineSubAgent.
		SubAgentRunner: t.runPipelineSubAgent,
	}
	// Credential scope — the set of credentials this agent has been denied
	// (scope pill). Carried on the session so the fetch_url auto-route (LLM
	// tool + script gohort.fetch_url) blocks a covered host whose credential
	// is revoked, closing the bypass that the tool-kit filter alone leaves
	// open (a plain fetch to the host instead of a credential-bound tool).
	sess.DeniedCredentials = credentialDenySet(t.agent, sess.Username)
	// Tag with the active chat session id so SaveSessionTempTool /
	// LoadSessionTempTools can scope tool drafts to this conversation.
	// Tools the LLM authors mid-conversation (via create_pipeline_tool
	// or create_agent's inline tools[]) land in the session_temp_tools
	// bucket keyed by this id so they're callable in this session
	// immediately for verification before any other commit step.
	if t.session != nil {
		sess.ChatSessionID = t.session.ID
	}
	// send_status delivery for chat. Renders as a PERSISTENT muted line in
	// the conversation flow (kind:status_note → appended to convoLog),
	// above the eventual reply — NOT the topbar status bar, which is
	// cleared on 'done' so a mid-turn status vanished before the user
	// could read it (the "send_status isn't working" symptom). Only wired
	// when this turn has a live SSE stream — a standing-agent / wake turn
	// has no watcher, so send_status falls through to its built-in no-op
	// guidance there instead of emitting into a dead writer.
	t.wireLiveCallbacks(sess)
	// Default workspace = per-user root. Tools that author scripts
	// via script_body ship them here at registration; dispatch finds
	// the same path. Tools that want explicit per-conversation
	// isolation call workspace(create) which switches sess.WorkspaceDir
	// to a managed workspace; otherwise everything operates at the
	// user root.
	//
	// History note: this was briefly auto-minting a managed workspace
	// per session. That broke existing persistent_temp_tools whose
	// script_body was shipped to the user root at authoring time —
	// the new managed workspace had no copy. Reverted; per-session
	// isolation is now opt-in via workspace(create).
	//
	// If a PRIOR step this turn switched to a managed workspace (via
	// workspace create/use), restore it so multi-step authoring shares
	// one workspace: a script written in step N is visible to a run in
	// step N+1. Falls through to the per-user root if the managed
	// workspace is gone (deleted / not ours).
	if t.activeWorkspaceID != "" {
		if w, ok := LoadManagedWorkspace(t.activeWorkspaceID); ok && w.Owner == t.user {
			if dir, derr := ManagedWorkspaceDir(w.Owner, w.ID); derr == nil {
				sess.WorkspaceDir = dir
				sess.WorkspaceID = w.ID
			}
		}
	}
	if sess.WorkspaceDir == "" {
		if ws, err := EnsureWorkspaceDir(t.user); err == nil {
			sess.WorkspaceDir = ws
		}
	}
	if sess.DB == nil || sess.Username == "" {
		return sess
	}
	// Persistent temp tools — gate logic depends on whether this is a
	// SEED agent (framework default, owner="system") or a USER-crafted
	// agent. Seed agents reflect "everything deployment-trusted is
	// available" — admin-approved persistent tools flow into them
	// automatically. User-crafted agents have deliberate AllowedTools
	// lists that should not be silently expanded by approvals
	// happening elsewhere; for those, persistent tools still follow
	// the explicit allow-list semantic.
	//
	// Both honor DisabledPersistentTools (per-agent deny list) as a
	// per-agent opt-out, and the no-tools sentinel (the "give me
	// absolutely no optional tools" mode) still suppresses everything.
	// Custom (temp) tools — persistent pool (this user) + the agent-scoped kit.
	// Shared with the channel/dispatch path via loadAgentTempTools so both
	// surfaces hydrate identically.
	t.loadAgentTempTools(sess, sess.Username, sess.DB)
	// Session-scoped tool drafts — the LLM authored these in THIS
	// conversation (e.g. for_agent-attached pipeline + bundled inline
	// tools from create_agent). Load them so the LLM can dispatch by
	// name to verify the tool works before relying on it. Persistence
	// to the agent record / approval queue already happened at author
	// time; this is purely for in-session testability.
	//
	// Drafts intentionally BYPASS the AllowedTools gate: the agent's
	// allowlist is the *committed* surface, but drafts are the
	// authoring scratchpad — the LLM needs to dispatch a draft to
	// verify it works *before* the user approves it into the agent.
	// Gating drafts on AllowedTools would make authoring impossible
	// (the tool can't be tested until it's allowed, but it isn't
	// allowed until the user has seen it work).
	if t.session != nil {
		drafts := LoadSessionTempTools(t.udb, t.session.ID)
		// Set of tool names already COMMITTED to any agent the user owns.
		// A draft whose canonical copy now lives on an agent record is
		// redundant and gets pruned. This is the Builder fix: Builder
		// authors tools for OTHER agents (committed to the TARGET's
		// record), but the session draft is keyed to BUILDER's session —
		// which never loads the target agent's Tools, so the name-conflict
		// prune below never caught them. They piled up turn after turn,
		// growing the "Your custom tools" prompt section (and busting the
		// prompt cache) for the whole build. Only built when drafts exist,
		// so the listAgents walk doesn't tax draft-free agents.
		var committed map[string]bool
		if len(drafts) > 0 {
			committed = map[string]bool{}
			for _, a := range listAgents(t.udb, t.user) {
				for _, tl := range a.Tools {
					committed[tl.Name] = true
				}
			}
		}
		var cleaned int
		for i := range drafts {
			tool := drafts[i]
			if t.privateMode && tool.Mode == TempToolModeAPI {
				continue
			}
			// Already committed to some agent → prune; don't load a stale
			// duplicate into this session's catalog. (Catches cross-agent
			// authoring, which the name-conflict path below misses because
			// the committed copy isn't in THIS session's tool set.)
			if committed[tool.Name] {
				RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
				cleaned++
				Debug("[orchestrate.tools] pruned committed session draft %q (now lives on an agent record)", tool.Name)
				continue
			}
			if err := sess.AppendTempTool(&tool); err != nil {
				// Name conflict with the persistent or agent-scoped pool — the
				// committed version wins (it's the canonical copy) and this
				// draft is stale duplication. Drop it so the runtime and the
				// Tools UI stop showing two of the same thing.
				RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
				cleaned++
				Debug("[orchestrate.tools] dropped redundant session draft %q (committed copy exists)", tool.Name)
				continue
			}
			// MIGRATION: session-scoped tools are retired — an authored tool now
			// commits to the agent that asked for it. A draft that reaches here
			// is committed nowhere (the prune above caught the rest), so it is
			// real work living only in this conversation. Give it a durable home
			// on this agent, marked Trial, and clear the draft.
			//
			// Done here rather than in a migration script because this is where
			// drafts are consumed: they drain as conversations resume, and a
			// conversation nobody reopens has nothing worth keeping.
			// A draft authored FOR another agent belongs to that agent, not
			// to the one running the turn — the same distinction
			// ToolSession.AuthoringAgentFn draws for fresh authoring. The
			// prune above catches drafts already committed somewhere, so what
			// lands here is uncommitted work; without the focus check every
			// such draft settles on the authoring agent (Builder) and it
			// carries another agent's tool schema in its prompt from then on.
			if target := migrationTargetAgent(t.agent.ID, t.authoringFocus()); AttachToolToAgent != nil && target != "" {
				migrated := tool
				migrated.Trial = true
				migrated.TrialSince = time.Now()
				if err := AttachToolToAgent(t.udb, t.user, target, migrated); err == nil {
					RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
					Log("[orchestrate.tools] migrated session draft %q onto agent %q as a trial tool", tool.Name, target)
				} else {
					Debug("[orchestrate.tools] could not migrate session draft %q: %v", tool.Name, err)
				}
			}
		}
		if ReapTrialTools != nil {
			// Cheap in the common case: no trial tools means one agent walk and
			// no writes. Keeps an agent in regular use tidy without requiring
			// anyone to visit a settings page.
			_ = ReapTrialTools(t.udb, t.user)
		}
		if n := len(drafts); n > 0 {
			Log("[orchestrate.tools] loaded %d session-draft tool(s) for session=%s (cleaned %d redundant)", n, t.session.ID, cleaned)
		}
	}
	return sess
}

// authoringFocus reports the agent this turn is authoring FOR, or "" when the
// turn is authoring for itself. Mirrors ToolSession.AuthoringAgentFn.
func (t *chatTurn) authoringFocus() string {
	if t == nil || t.session == nil {
		return ""
	}
	return strings.TrimSpace(t.session.AuthoringAgentID)
}

// migrationTargetAgent picks which agent an uncommitted session draft settles
// on: the authoring focus when the turn is building for someone else,
// otherwise the running agent. Returns "" when neither is known, which the
// caller treats as "leave the draft alone" — the failure mode here must be
// "left something behind", never "attached it to the wrong agent", since a
// misplaced tool both hides from its owner and inflates the prompt of an agent
// that never asked for it.
func migrationTargetAgent(runningID, authoringID string) string {
	if authoringID != "" {
		return authoringID
	}
	return runningID
}

// loadToolToolDef builds the load_tool meta-tool. Custom (temp) tools
// that take arguments are presented to the LLM by name + description
// only (a prompt section), keeping their schemas out of the catalog.
// load_tool fetches a named custom tool's full schema, marks it loaded
// (so the DynamicTools feed surfaces it next round), and returns the
// parameter spec so the LLM can call it correctly.
func (t *chatTurn) loadToolToolDef(sess *ToolSession) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "load_tool",
			Description: "Load one or more of your custom tools so you can call them. Custom tools are listed by name + description under \"Your custom tools\" but their parameters aren't loaded until you call this. Pass ALL the tools you expect to need for the task in ONE call (names[]) — batching loads them in a single round instead of one round per tool. Returns their parameters and makes them callable on your next step. Only needed for custom tools shown as needing a load — built-in tools are always ready.",
			Parameters: map[string]ToolParam{
				"names": {Type: "array", Items: &ToolParam{Type: "string"}, Description: "Exact names of the custom tools to load (from the \"Your custom tools\" list). Pass every tool you anticipate needing — one or many."},
			},
			Required: nil, // validated in the handler (also tolerates a singular `name`)
			Caps:     nil, // control/meta — no side effects of its own
		},
		Handler: func(args map[string]any) (string, error) {
			// Collect from names[] plus a tolerated singular `name` (LLMs
			// fall back to the old singular form out of habit), dedupe.
			raw := stringSliceFromArgs(args, "names")
			if single := strings.TrimSpace(stringArg(args, "name")); single != "" {
				raw = append(raw, single)
			}
			seen := make(map[string]bool, len(raw))
			var want []string
			for _, n := range raw {
				if n = strings.TrimSpace(n); n != "" && !seen[n] {
					seen[n] = true
					want = append(want, n)
				}
			}
			if len(want) == 0 {
				return "", errors.New("pass at least one tool name in names[]")
			}
			// Partial success: load every valid name, bucket the rest, so
			// one bad name doesn't sink the batch or make the LLM loop.
			var loaded, already, unknown []string
			schemas := make([]map[string]any, 0, len(want))
			for _, n := range want {
				td, ok := t.lazyCustomToolDefs[n]
				if !ok {
					if t.staticTempToolNames[n] {
						already = append(already, n)
						continue
					}
					// On-demand from the persistent pool. Builder skips loading
					// user-authored persistent tools at session setup ("authors
					// fresh"), so a tool the "approved but not loaded" list shows
					// (and that tool_def get can read) isn't in lazyCustomToolDefs
					// — without this branch load_tool would reject the very tool
					// that list told the model to load, an observed dead-end loop.
					if def, ok := t.loadPersistentToolOnDemand(sess, n); ok {
						t.lazyCustomToolDefs[n] = def
						t.lazyCustomToolNames[n] = true
						td = def
					} else {
						unknown = append(unknown, n)
						continue
					}
				}
				t.loadedCustomTools[n] = true
				loaded = append(loaded, n)
				schemas = append(schemas, map[string]any{
					"name":        td.Tool.Name,
					"description": td.Tool.Description,
					"parameters":  td.Tool.Parameters,
					"required":    td.Tool.Required,
				})
			}
			var b strings.Builder
			if len(loaded) > 0 {
				sb, _ := json.Marshal(schemas)
				fmt.Fprintf(&b, "Loaded %s — now callable. Schemas:\n%s\n", strings.Join(loaded, ", "), string(sb))
			}
			if len(already) > 0 {
				fmt.Fprintf(&b, "Already loaded (call directly): %s\n", strings.Join(already, ", "))
			}
			if len(unknown) > 0 {
				fmt.Fprintf(&b, "Unknown — check the \"Your custom tools\" list or call find_tools: %s\n", strings.Join(unknown, ", "))
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

// loadPersistentToolOnDemand pulls a tool from the user's persistent pool
// into the live session on request, so load_tool can resolve a tool that
// wasn't pre-loaded at session setup (the Builder case: it deliberately
// doesn't auto-load user tools, but MUST be able to load one explicitly to
// inspect or test it). Appends it to sess.TempTools (so dynamicNewTempTools
// surfaces the wrapped, callable version next round) and returns the
// activity-wrapped def for the same-round lazyToolFallback path. Returns
// false when no persistent tool of that name exists.
func (t *chatTurn) loadPersistentToolOnDemand(sess *ToolSession, name string) (AgentToolDef, bool) {
	if sess == nil || t.udb == nil || t.user == "" {
		return AgentToolDef{}, false
	}
	var found *TempTool
	for _, p := range LoadPersistentTempTools(t.udb, t.user) {
		if p.Tool.Name == name {
			tt := p.Tool
			found = &tt
			break
		}
	}
	if found == nil {
		return AgentToolDef{}, false
	}
	if !sess.HasTempTool(name) {
		if err := sess.AppendTempTool(found); err != nil {
			Log("[orchestrate.tools] load_tool on-demand append %q failed: %v", name, err)
			return AgentToolDef{}, false
		}
	}
	// Build + activity-wrap the single def for the immediate fallback path;
	// the next round's dynamicNewTempTools rebuilds it the same way.
	for _, td := range temptool.BuildAgentToolDefs(sess) {
		if td.Tool.Name == name {
			wrapped := t.wrapToolsForActivity(sess, []AgentToolDef{td})
			if len(wrapped) > 0 {
				return wrapped[0], true
			}
			return td, true
		}
	}
	return AgentToolDef{}, false
}

// lazyToolFallback resolves a direct call to a lazy custom tool that
// isn't in this round's catalog. The model learned the tool's schema on a
// prior turn (via load_tool) and is now calling it straight from context,
// so forcing a re-load would be pointless friction — its schema is lazy
// (kept out of the LLM tool array to save tokens), but its handler is
// still valid. Returns the (already activity-wrapped) handler and marks
// the tool loaded so its schema also rejoins the catalog next round.
// Wired as the agent loop's ToolFallbackResolver.
func (t *chatTurn) lazyToolFallback(name string) (ToolHandlerFunc, bool) {
	td, ok := t.lazyCustomToolDefs[name]
	if !ok {
		return nil, false
	}
	t.loadedCustomTools[name] = true
	return td.Handler, true
}

// dynamicTempTools returns a DynamicTools callback that exposes the
// session's loaded temp tools to the agent loop each round. Wraps
// each one through wrapToolsForActivity so they get the same inline
// tool_call / tool_result SSE emissions + activity-pane rendering +
// per-turn cache as the built-in tools — without this they fire
// invisibly and the user can't see the temp tool ran.
func (t *chatTurn) dynamicTempTools(sess *ToolSession) func() []AgentToolDef {
	return func() []AgentToolDef {
		defs := temptool.BuildAgentToolDefs(sess)
		t.wrapToolsForActivity(sess, defs)
		return defs
	}
}

// attachDeliveredSkillTools loads the bundled Tools of any skill the LLM has
// consulted this turn (t.deliveredSkills) into the session pool, so a skill's
// own shipped scripts become callable once it's active — and never before that.
// Idempotent: a tool already present (persistent pool, agent kit, or a prior
// round) is skipped. Surfaced through the normal BuildAgentToolDefs feed, so
// the tools inherit private-mode filtering + the lazy/static split for free.
func (t *chatTurn) attachDeliveredSkillTools(sess *ToolSession) {
	// Shared with phantom via core.AttachDeliveredSkillTools so both surfaces
	// behave identically. Orchestrate's dynamicNewTempTools surfaces sess temp
	// tools itself (brand-new mid-turn tools fall through to a direct surface),
	// so we don't need the returned names here.
	AttachDeliveredSkillTools(sess, t.udb, t.user, t.deliveredSkills, t.privateMode)
}

// dynamicNewTempTools returns the agent loop's per-round dynamic
// tool feed: ONLY temp tools created mid-turn that weren't in the
// static snapshot. Persistent temp tools live in the static catalog;
// this surfaces freshly-authored ones so the LLM can dispatch by
// name to verify a new tool works before the user approves it.
//
// Replaced the old dynamicToolsWithGroups now that group-expansion
// is retired (vector pre-selection picks tools by relevance; no
// expand_tool_group meta-tool, no synthetic group placeholders).
func (t *chatTurn) dynamicNewTempTools(sess *ToolSession) func() []AgentToolDef {
	base := t.dynamicTempTools(sess)
	return func() []AgentToolDef {
		// Bundled skill tools: once a skill is consulted this turn, load its
		// shipped scripts into the session pool so they become callable. Polled
		// each round (cheap, idempotent) so a skill consulted mid-turn surfaces
		// its tools the next round, exactly like a freshly-authored temp tool.
		t.attachDeliveredSkillTools(sess)
		raw := base()
		out := make([]AgentToolDef, 0, len(raw))
		for _, td := range raw {
			if t.staticTempToolNames[td.Tool.Name] {
				continue // zero-arg custom already in the static catalog
			}
			// Lazy (has-args) custom tools stay name+desc-only until the
			// LLM loads them via load_tool — surface the full def only
			// once loaded, so the schema enters the catalog exactly when
			// it's needed. Brand-new mid-turn tools (in neither set) fall
			// through and surface directly, preserving the verify flow.
			if t.lazyCustomToolNames[td.Tool.Name] && !t.loadedCustomTools[td.Tool.Name] {
				continue
			}
			// A lazy custom tool that HAS been loaded this session: surface it,
			// but mark it render-late so the split chat template puts its schema
			// at the BOTTOM of the prompt (via chat_template_kwargs.lazy_tool_names)
			// instead of the top-of-prompt tools block. That keeps loading a tool
			// from invalidating the cached prefix (the ~13s load_tool cold-prefill).
			// Zero-arg static temp tools and the agent's own kit stay at the top.
			if t.lazyCustomToolNames[td.Tool.Name] {
				td.Tool.RenderLate = true
			}
			out = append(out, td)
		}
		// Source-hook dispatcher: ONE query_source tool over all exposed
		// hooks (the agents pattern) instead of N per-hook tools — see
		// RenderAvailableSourcesBlock for the shown "Available sources"
		// menu that carries each source's "use when". Walked per-round so
		// admin add/remove/toggle takes effect without a restart; cheap
		// (reads the in-memory sourceHookRegistry). Skills still grant a
		// SPECIFIC hook as a focused per-hook tool via the skill path.
		if qs, ok := QuerySourceToolDef(t.app.DB); ok {
			out = append(out, qs)
		}
		// (No skill-granted tools here anymore. Experts run as dispatched
		// use_expert workers with their own catalog; behavior skills only
		// inject instructions. Neither puts tools in the MAIN catalog.)
		// Private-mode backstop. The dynamic feed re-introduces tools
		// that the static catalog filter already dropped:
		//   - mid-turn temp tools (LLM authored after runPlan started)
		//   - source-hook auto-tools (RSS / API readers — network by definition)
		// Without this pass, a Private turn gets network tools handed
		// back round-by-round even though the opening catalog was clean.
		// Mirrors the static-catalog filter in runPlan.
		if t.privateMode {
			filtered := out[:0]
			for _, td := range out {
				hasNet := false
				for _, c := range td.Tool.Caps {
					if c == CapNetwork {
						hasNet = true
						break
					}
				}
				if hasNet {
					continue
				}
				filtered = append(filtered, td)
			}
			out = filtered
		}
		return out
	}
}

// decodeUserImages turns the base64 strings the chat panel ships in
// the send body into raw bytes for the Message.Images field. Failures
// are logged and skipped — a corrupt image shouldn't kill the turn.
func decodeUserImages(b64s []string) [][]byte {
	if len(b64s) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(b64s))
	for i, s := range b64s {
		if s == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			Log("[orchestrate.images] decode failed for attachment %d: %v", i, err)
			continue
		}
		out = append(out, b)
	}
	return out
}

// setCurrentMsgID records which assistant bubble the wrap-emit
// helpers should target for tool_call/tool_result SSE events.
// Concurrent-safe so a tool call firing during a stream chunk
// can't race with the streamHandler that sets the id.
func (t *chatTurn) setCurrentMsgID(id string) {
	t.currentMu.Lock()
	t.currentMsgID = id
	t.currentMu.Unlock()
}

// getCurrentMsgID returns the active bubble id (may be empty).
func (t *chatTurn) getCurrentMsgID() string {
	t.currentMu.Lock()
	id := t.currentMsgID
	t.currentMu.Unlock()
	return id
}

// resolveWorkerTools builds the agent's effective tool surface for
// this turn: default pool (Read+Network non-blocked tools + unannotated
// orchestrate-internal tools) intersected with agent.AllowedTools when
// the agent set an explicit allowlist; Private mode then drops
// internet-flagged tools. Returns the resolved AgentToolDefs (handlers
// bound to sess) and the final name list for logging.
//
// Shared by runPlan (orchestrator's inline tool surface) and
// runWorkerStep (worker step execution) so the two pipelines agree on
// what's available without drift.
// resolveWorkerTools builds the per-turn tool catalog. The
// forOrchestrator flag distinguishes Builder's own chat-orchestrator
// turn (the planner — gets a LEAN authoring catalog, no workhorses)
// from a plan_set worker step (gets the FULL authoring catalog).
// Non-Builder agents are unaffected by the flag — the gate at
// isBuilderAgent only fires for the Builder seed.
func (t *chatTurn) resolveWorkerTools(sess *ToolSession, forOrchestrator bool) ([]AgentToolDef, []string, error) {
	defaultNames := availableWorkerToolNames()
	var toolNames []string
	switch {
	case isNoToolsSentinel(t.agent.AllowedTools):
		// Sentinel — admin explicitly unchecked every tool in the
		// Tools modal. Effective optional-tool list is empty.
		// Framework always-on tools (plan_set, respond_directly,
		// stay_silent, keep_going) get appended later in the catalog
		// build, so the agent still has the minimum it needs to
		// produce a response — just no read/network/write tools.
		toolNames = nil
	case len(t.agent.AllowedTools) > 0:
		allow := make(map[string]bool, len(t.agent.AllowedTools))
		for _, n := range t.agent.AllowedTools {
			allow[canonicalToolName(n)] = true
		}
		matched := 0
		for _, n := range defaultNames {
			if allow[n] {
				toolNames = append(toolNames, n)
				matched++
			}
		}
		// Credential-backed tools (fetch_url_<cred>) are synthesized per
		// session by Secure().BuildTools and never appear in defaultNames
		// (the registered worker pool), so the intersect above silently
		// dropped any credential tool the author explicitly allowlisted.
		// The agent then fell back to the generic fetch_url — which has no
		// injected auth and refuses private hosts — instead of the bound,
		// authed tool it was configured with. Re-add allowlisted names that
		// resolve to a live credential tool for this session. (The dispatch
		// path in agent_dispatch.go already resolves these via
		// GetAgentToolsWithSession's per-session fallback; this brings the
		// interactive / published-app surface to parity.) Only build the
		// per-session credential set when some allowlisted name went
		// unmatched above — the common all-registered case skips it.
		if matched < len(allow) {
			for _, td := range Secure().BuildTools(sess) {
				if n := td.Tool.Name; allow[n] && !slices.Contains(toolNames, n) {
					toolNames = append(toolNames, n)
				}
			}
		}
	default:
		// No explicit allow-list — the agent runs the full default pool.
		// defaultNames is the REGISTERED worker pool and never contains the
		// per-session credential tools (fetch_url_<cred>) synthesized by
		// Secure().BuildTools, so "allow everything" would paradoxically be
		// LESS capable than a hand-picked list that named a credential tool:
		// the model can't see fetch_url_<cred> and falls back to the generic,
		// unauthenticated fetch_url. Append every enabled credential tool so
		// the most permissive setting genuinely means everything. These carry
		// CapNetwork, so the Private-mode filter below still drops them per
		// turn when the agent is running network-restricted.
		toolNames = defaultNames
		for _, td := range Secure().BuildTools(sess) {
			if n := td.Tool.Name; !slices.Contains(toolNames, n) {
				toolNames = append(toolNames, n)
			}
		}
	}
	// Always include `workspace` regardless of cap filtering. Workspace
	// owns the delivery primitive (action="attach") that every producer
	// tool routes through — it's universal infrastructure, not a
	// per-agent capability choice. Without this, agents whose AllowedTools
	// is empty (default pool) miss workspace because its CapWrite +
	// CapExecute actions get it filtered out of the Read+Network default
	// pool. The LLM then sees producer tools telling it to call
	// workspace(attach) but has no workspace tool in its catalog —
	// silent never-attach failure.
	//
	// Skip the auto-include when the no-tools sentinel is set: the
	// admin explicitly wants NO optional tools, and workspace is an
	// optional tool. Without producers there's nothing to deliver
	// either, so workspace would be dead weight.
	if !isNoToolsSentinel(t.agent.AllowedTools) && !slices.Contains(toolNames, "workspace") {
		toolNames = append(toolNames, "workspace")
	}
	// Framework utility tools — pure CapRead helpers (calculate, date_math,
	// time_in_zone) kept always-on so an agent never fails basic math /
	// dates / timezones just because nobody allowlisted them. Hidden from
	// the curation UI (frameworkUtilityTools); force-included here like
	// workspace. Added BEFORE the Private-mode filter — they're non-network
	// so they survive it. Skip on the no-tools sentinel (admin wants none).
	if !isNoToolsSentinel(t.agent.AllowedTools) {
		for _, n := range frameworkUtilityTools {
			if !slices.Contains(toolNames, n) {
				toolNames = append(toolNames, n)
			}
		}
	}
	// Per-turn Private mode drops tools that contact the internet.
	// Applied AFTER the agent's allowlist so non-network tools the
	// agent allowed (list_agents, calculate, …) keep working.
	// Checks BOTH the legacy IsInternetTool interface AND the modern
	// Caps() declaration — a tool tagged CapNetwork via Caps() but
	// missing IsInternetTool would otherwise slip past this filter
	// (silently breaking Private mode's contract).
	if t.privateMode {
		filtered := toolNames[:0]
		for _, n := range toolNames {
			ct, ok := FindChatTool(n)
			if !ok {
				continue
			}
			if isNetworkTool(ct) {
				continue
			}
			filtered = append(filtered, n)
		}
		toolNames = filtered
	}
	// Strip client-bridge tool names (from_client.*) before the
	// global-registry lookup. They're never in the registered
	// ChatTools pool (they're per-user, injected at runtime by the
	// bridge), so GetAgentToolsWithSession would 404 on every one
	// and abort the whole turn. The runtime hook later in this
	// function reads them from the user's connected desktops and
	// appends as ChatToolToAgentToolDef so the LLM still sees them.
	if len(toolNames) > 0 {
		filtered := toolNames[:0]
		for _, n := range toolNames {
			if strings.HasPrefix(n, "from_client.") {
				continue
			}
			filtered = append(filtered, n)
		}
		toolNames = filtered
	}
	tools, err := GetAgentToolsWithSession(sess, toolNames...)
	if err != nil {
		return nil, nil, err
	}
	// Builder gets the unregistered authoring tools appended here —
	// they don't live in any global registry, so they reach the
	// catalog only via this explicit code path. Identity check
	// against the seed-builder ID prevents the appendage from
	// leaking to other agents.
	//
	// forOrchestrator chooses between two catalogs that reflect the
	// assembler-vs-research split:
	//   - true  → Builder's OWN chat-orchestrator turn. Builder is
	//             the assembler: it reads worker drafts and calls
	//             create_agent / tool_def / etc. itself, so it
	//             needs the FULL authoring catalog right here.
	//   - false → A plan_set worker step under Builder. Workers
	//             research / draft / smoke-test and report COMPONENTS
	//             back. They get the worker-research extras only
	//             (raw call_<credential> for API probing); authoring
	//             tools stay with the orchestrator.
	if isBuilderAgent(t.agent.ID) {
		var extra []AgentToolDef
		if forOrchestrator {
			extra = builderAuthoringTools(sess, t)
		} else {
			extra = builderWorkerResearchTools(sess, t)
		}
		tools = append(tools, extra...)
		for _, td := range extra {
			toolNames = append(toolNames, td.Tool.Name)
		}
	}
	// Fleet agents get the exclusive fleet-management catalog on their
	// conversational turn — delegate + create/list/run/pause standing
	// agents + read the run-ledger + event-monitor management + history
	// recall. Not globally registered; appended here only when Fleet is on,
	// same shape as Builder's authoring tools.
	// Owner-only: the Fleet toolset reaches owner-scoped management endpoints
	// (delegate, standing agents, run ledger, monitors), so it attaches ONLY
	// when the runtime user IS the agent's owner. A public-app visitor (or any
	// granted non-owner user) running a Fleet agent never sees these — which is
	// what lets publiclyExposable honor Publish on a Fleet agent without leaking
	// owner controls. Seeds load with Owner unset until shadowed; treat that as
	// the caller's own so the owner keeps Fleet on their own seed.
	ownerRun := t.agent.Owner == "" || t.agent.Owner == t.user
	if t.agent.Fleet && forOrchestrator && ownerRun {
		om := operatorManagementTools(sess, t.agent.ID)
		// History drill-in is its own pair (recall_history / expand_history) in
		// legacy mode; the unified `recall` tool already spans folded-away
		// history, so drop the pair to avoid two tools that search the past.
		if !unifiedMemoryEnabled() {
			om = append(om, operatorHistoryTools(sess, t.agent.ID)...)
		}
		tools = append(tools, om...)
		for _, td := range om {
			toolNames = append(toolNames, td.Tool.Name)
		}
		// Drop the generic interval scheduler — the Operator schedules through
		// the fleet (create_standing_agent), which does proper cron / start+
		// interval timing and surfaces in Enabled agents. Without this the LLM
		// reaches for "recurring" (interval-from-now → "not exactly 8am") and
		// bypasses the fleet.
		tools, toolNames = dropToolsByName(tools, toolNames, "recurring")
	}
	// request_build — the COMPLEMENT of Fleet's live Builder dispatch. A Fleet
	// agent hands authoring to Builder directly; a non-Fleet agent can't, so
	// without this it has no path to "create a sub-agent" and flails. This gives
	// it one: queue the build as an approval the user sees, and on approve Builder
	// authors it OwnedBy this agent. Owner-run conversational turn only, and not
	// Builder itself (Builder authors directly).
	if forOrchestrator && ownerRun && !t.agent.Fleet && !isBuilderAgent(t.agent.ID) {
		rb := requestBuildTool(t.user, t.agent.ID, t.agent.Name)
		tools = append(tools, rb)
		toolNames = append(toolNames, rb.Tool.Name)
	}
	// Channel-scoped chat tools — ANY agent that has channels gets list_chats /
	// read_chat over ITS channels (a whole-service binding widens to the global
	// view). Gated on having channels, independent of Fleet; conversational turn
	// only, like the Fleet block.
	if forOrchestrator {
		if chTools := channelChatTools(sess, t.user, t.agent.ID); len(chTools) > 0 {
			tools = append(tools, chTools...)
			for _, td := range chTools {
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
		// (cortex deliverables — file_deliverable / note_to_cortex — now come from
		// frameworkConversationalTools, the shared web+channel set.)
	}
	// Parent-tool inheritance — an owned sub-agent that opted in (InheritParentTools)
	// resolves its parent's NON-consequential catalog at runtime in addition to
	// its own allowlist: the parent's worker tools (no Fleet block) plus the
	// read-only phantom tools. Lets a Builder-authored summarizer read the chat
	// it summarizes without being a Fleet agent (so no texting / delegation).
	// Guarded to top-level parents so inheritance can't chain, and deduped so
	// shared names don't double-register.
	if t.agent.InheritParentTools && t.agent.OwnedBy != "" {
		if parent, ok := loadAgent(t.udb, t.agent.OwnedBy); ok && parent.OwnedBy == "" {
			inherited := t.inheritableParentTools(parent, sess)
			before := len(tools)
			tools = mergeToolsDedup(tools, inherited)
			for _, td := range tools[before:] {
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
	}
	// (Skill-granted tools are NOT resolved here anymore. Activation is
	// per-turn: t.skillsActive is empty when this static catalog is built
	// at turn start. A skill's tools are surfaced by the per-round
	// DynamicTools feed (dynamicNewTempTools → AppendSkillGrantedTools) the
	// round AFTER activate_skill fires, so they appear this same turn.)
	// Local tools from the user's gohort-desktop surface (from_client.*).
	// Exposed ONLY when this request came from the gohort-desktop viewer
	// itself (its proxy stamps the bridge key — see t.fromDesktopClient). A
	// remote browser / phone logged into the same account never sees them, so
	// the local machine's filesystem / screenshot / contacts can't be reached
	// remotely — not even with auto-approve on (the old "approval modal is the
	// enforcement point" model failed exactly there). When the desktop is
	// offline at call time the tool's wrapper still returns a clean "open your
	// desktop" error. Keyed off the chat user so seed agents (Chat) get the
	// surface for whoever is chatting at the desktop.
	have := map[string]bool{}
	for _, n := range toolNames {
		have[n] = true
	}
	// Only OPEN-POOL agents (empty AllowedTools — the general Chat/seed
	// assistants) receive the desktop's local surface. A CURATED agent with an
	// explicit allowlist (a Guide Author, a techwriter, any app agent) scoped
	// itself to a specific toolset and never opted into local-filesystem /
	// screenshot / contacts access — appending it silently let those ambient
	// tools SHADOW the agent's purpose-built ones (a Guide Author rummaging the
	// local disk instead of dispatching investigate_<system> to servitor). The
	// allowlist is the opt-in signal: no list ⇒ open pool ⇒ desktop surface; an
	// explicit list ⇒ exactly those tools, nothing ambient.
	var fromClient []AgentToolDef
	if t.fromDesktopClient && len(t.agent.AllowedTools) == 0 {
		for _, lt := range LocalToolsForUser(t.user) {
			if have[lt.Name()] {
				continue
			}
			fromClient = append(fromClient, ChatToolToAgentToolDef(lt))
			toolNames = append(toolNames, lt.Name())
		}
	}
	// NOTE: do NOT wrapToolsForActivity here. The client-bridge tools are
	// part of the returned slice, which both callers wrap as a whole
	// (resolveWorkerTools' result → wrapToolsForActivity at the orchestrator
	// and worker call sites). Wrapping here too double-wrapped ONLY the
	// from_client.* tools, so each fired two tool_call/tool_result SSE events
	// and two recordToolCall entries — the catalog showed (and the tool log
	// recorded) every desktop call twice. The single call-site wrap gives
	// them the same inline chips + cache as every other tool, once.
	tools = append(tools, fromClient...)
	return tools, toolNames, nil
}

// wrapToolsForActivity decorates each tool's handler with:
//   - per-turn cache short-circuit (skip the call if the same
//     (name, args) returned already)
//   - activity-pane cmd / output / error rows (for apps that show
//     the activity pane — orchestrate locks it off, servitor uses it)
//   - inline tool_call / tool_result SSE events attached to the
//     active conversation-pane bubble (chat-app style — the only
//     way users see tool use when the activity pane is hidden)
//   - per-turn tool log recording (for later step prompts)
//
// Lazy-materializes an "orch-…" bubble if a tool fires before any
// text has streamed in this round, so the call has somewhere to land
// agentCanAuthorAgents reports whether the active agent has access to
// the create_agent tool — i.e. whether it's an agent-authoring agent.
// True when AllowedTools is empty (= default pool, which includes
// create_agent) OR explicitly lists "create_agent". Used by
// create_pipeline_tool's handler to gate the user-wide-with-approval
// path: agents that can author other agents almost always mean to
// either bundle inline (case A in the prompt) or attach via for_agent
// (case B). The user-wide approval queue strands the LLM because the
// tool name doesn't resolve until admin review.
func (t *chatTurn) agentCanAuthorAgents() bool {
	if t == nil {
		return false
	}
	if len(t.agent.AllowedTools) == 0 {
		return true
	}
	for _, n := range t.agent.AllowedTools {
		if n == "create_agent" || n == "*" {
			return true
		}
	}
	return false
}

// filterToolAuthoringWithoutFocus prunes overlapping or unfireable
// authoring tools from the catalog. Two distinct surfaces survive:
//
//   - **add_tool** attaches a tool to the agent currently in
//     AuthoringAgentID focus. Refuses without focus, so hide it when
//     no focus is set (otherwise the LLM pattern-matches and burns
//     rounds calling a tool that always errors).
//   - **tool_def** is the standalone session/user-scoped grouped tool
//     builder. Stays visible in BOTH states — when there's no focus
//     it's the only authoring path; when there IS focus the LLM picks
//     between "attach to this agent" (add_tool) and "one-off session
//     tool" (tool_def) based on intent. The Chat prompt teaches the
//     distinction.
//
// The legacy unbundled trio (create_temp_tool / create_api_tool /
// create_pipeline_tool) is always dropped — tool_def is the grouped
// replacement and exposing both shapes guarantees the
// oscillation-between-authoring-tools loop we already saw.
//
// Returns filtered (tools, names) preserving original order.
func filterToolAuthoringWithoutFocus(tools []AgentToolDef, names []string, session *ChatSession) ([]AgentToolDef, []string) {
	hasFocus := session != nil && session.AuthoringAgentID != ""

	// Compute which tools to drop based on the active conditions.
	drop := map[string]bool{
		// Always dropped — superseded by tool_def's grouped action surface.
		"create_pipeline_tool": true,
		"create_temp_tool":     true,
		"create_api_tool":      true,
	}
	if !hasFocus {
		// add_tool refuses without focus — hide it so the LLM doesn't
		// burn a round calling something that's guaranteed to error.
		// tool_def stays visible: it's the standalone authoring path
		// (one-off session/user tool, no agent attachment).
		drop["add_tool"] = true
	}
	if len(drop) == 0 {
		return tools, names
	}

	filteredTools := tools[:0]
	for _, td := range tools {
		if drop[td.Tool.Name] {
			continue
		}
		filteredTools = append(filteredTools, td)
	}
	filteredNames := names[:0]
	for _, n := range names {
		if drop[n] {
			continue
		}
		filteredNames = append(filteredNames, n)
	}
	return filteredTools, filteredNames
}

// explicitOff returns whether the Explicit Memory layer (always-in-
// prompt facts via store_fact) is suppressed for this turn. Gated by
// DisableExplicit only — Explicit Memory is configuration-level,
// either the agent has a facts surface or it doesn't. Callers gate
// store_fact / list_facts / forget_fact registration + the "Saved
// facts" prompt-injection block on this helper.
func (t *chatTurn) explicitOff() bool {
	if t == nil {
		return true
	}
	return t.agent.DisableExplicit
}

// inferredOff returns whether the Reference Memory layer (vector-grown
// derived chunks via memory_save + synthesis auto-ingest) is
// suppressed for this turn. True when EITHER the agent has
// DisableInferred=true OR the per-turn Clean toggle is on. Callers
// gate memory_save / memory_search / memory_forget registration,
// synthesis auto-ingest, derived-chunk recall in auto-inject, and
// the skills classifier (skills emit derived chunks via self-training)
// on this helper.
func (t *chatTurn) inferredOff() bool {
	if t == nil {
		return true
	}
	return t.agent.DisableInferred || t.inferredDisabled
}

// gateAgentCRUDTools wraps the agent-CRUD tool handlers with a build-
// plan auto-advance hook so each successful authoring tool ticks the
// next pending step off the user-visible plan card even when the LLM
// forgets to call mark_step_done in the same response.
//
// The previous two-turn gate (propose-then-act with mandatory
// ask_user in between) was removed — the rhythm is now enforced via
// prompt + plan_set decomposition instead of a runtime block. The
// gate kept producing false-positive blocks on legitimate flows
// (e.g. plan_set worker steps where the confirm phase had already
// happened but the awaiting-confirm flag hadn't propagated by the
// time the worker fired). Trust the prompt's "Phase 3 — CONFIRM" +
// plan_set boundary instead.
func (t *chatTurn) gateAgentCRUDTools(tools []AgentToolDef) {
	autoAdvance := map[string]bool{
		"create_agent": true,
		"update_agent": true,
		"clone_agent":  true,
		"delete_agent": true,
		"add_tool":     true,
	}
	for i := range tools {
		name := tools[i].Tool.Name
		orig := tools[i].Handler
		if orig == nil || !autoAdvance[name] {
			continue
		}
		toolName := name // closure capture
		tools[i].Handler = func(args map[string]any) (string, error) {
			result, err := orig(args)
			if err == nil {
				// Auto-advance the next pending plan step. Summary is
				// the first line of the tool result (typically the
				// directive line like "AGENT_CREATED ok. id=…").
				summary := firstLineSnippet(result, 120)
				if n := t.autoAdvanceBuildPlan(summary); n > 0 {
					Debug("[orchestrate.build_plan] auto-advanced step %d after %s success", n, toolName)
				}
			}
			return result, err
		}
	}
}

// firstLineSnippet returns the first non-empty line of s clipped to
// max chars. Used to derive a one-line summary for auto-advanced
// build-plan steps when the tool result is multi-line.
func firstLineSnippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[:nl]
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// in the conversation pane. Mutates the slice in place. Returns it
// for chaining.
//
// Optional labelPrefix prepends to every cmd row emitted by the
// wrapped tools — used by sub-agent dispatches to visually nest
// the sub-agent's tool calls under the parent ("↳ [Pickleball
// Coach] knowledge_search(...)"). Empty prefix = no nesting marker
// (the default for top-level / parent wraps).
// untrustedContentFence prefixes the result of any network tool: its payload is
// EXTERNAL data (fetched pages, API/feed responses, search snippets) that can
// carry prompt-injection. The fence tells the model to treat the payload as data,
// not instructions — the front-line mitigation for an agent that ingests untrusted
// content. Kept to one line to bound the per-result token cost.
// untrustedContentFence is the package-local spelling of the framework banner.
// The text lives in core (UntrustedToolResultFence) because tools in other
// packages self-fence individual actions with the identical marker — a tool
// result that says "untrusted" in two different voices trains the model to
// treat the wording, rather than the meaning, as the signal.
const untrustedContentFence = UntrustedToolResultFence

// toolCarriesNetworkCap reports whether a tool's declared capabilities include
// network access — the signal that its result is external, untrusted content.
func toolCarriesNetworkCap(t Tool) bool {
	for _, c := range t.Caps {
		if c == CapNetwork {
			return true
		}
	}
	return false
}

func (t *chatTurn) wrapToolsForActivity(sess *ToolSession, tools []AgentToolDef, labelPrefix ...string) []AgentToolDef {
	prefix := ""
	if len(labelPrefix) > 0 {
		prefix = labelPrefix[0]
	}
	for i := range tools {
		name := tools[i].Tool.Name
		orig := tools[i].Handler
		if orig == nil {
			continue
		}
		// Fence network-tool results as untrusted external content. Wrap the RAW
		// handler so the fence rides through the cache + activity emission below
		// (cache stores fenced; the model always sees the marker). TrustedOutput
		// tools opt out: their result is framework-generated control/authoring
		// text (tool_def / add_tool confirmations), not fetched content — the
		// CapNetwork on their union comes from a verify/test sub-action, not
		// from their everyday output.
		if toolCarriesNetworkCap(tools[i].Tool) && !tools[i].Tool.TrustedOutput {
			inner := orig
			orig = func(args map[string]any) (string, error) {
				out, err := inner(args)
				if err == nil && strings.TrimSpace(out) != "" {
					out = untrustedContentFence + out
				}
				return out, err
			}
		}
		tools[i].Handler = func(args map[string]any) (string, error) {
			// Activity-pane cmd / inline tool_call go in parallel so
			// both views work: apps with activity visible (servitor)
			// see the cmd row; apps with it hidden (orchestrate)
			// see the inline chip.
			//
			// Tools listed in hiddenToolChips don't render either —
			// they have a better-than-chip rendering elsewhere (the
			// reply itself for respond_directly, the status line for
			// send_status) and the chip is redundant noise. We still
			// run the handler, snapshot attachments, and record the
			// call for the toolLogPromptSection — only the user-
			// facing emissions are skipped.
			hidden := hiddenToolChips[name]
			callLabel := prefix + formatToolCall(name, args)
			if cached, ok := t.lookupToolCache(name, args); ok {
				if !hidden {
					t.sse.Send(map[string]any{
						"kind": "activity",
						"type": "cmd",
						"id":   activityCheapID(),
						"text": "♻ " + callLabel + " (cached)",
					})
					if msgID := t.ensureBubbleForTool(); msgID != "" {
						callID := t.emitToolCall(msgID, name, args, " (cached)", prefix)
						t.emitToolResult(msgID, callID, name, cached, nil)
					}
				}
				// Persist cache-hit calls. The live UI shows them via
				// the cmd/inline chip emissions above; without this
				// record, the saved transcript + Copy session export
				// drop them silently — same call visible to the user
				// at the time, invisible to anyone reading the log
				// afterwards. Cached=true distinguishes from a fresh
				// dispatch so a downstream consumer can tell.
				t.recordToolCall(toolCallRecord{
					Name:   name,
					Args:   args,
					Result: cached,
					Cached: true,
				})
				return cached, nil
			}
			// Dispatch-cap path — refuse the Nth identical (name, args)
			// call regardless of cache hit/miss. The cache only stores
			// SUCCESSFUL results, so a tool that's been failing (timeout,
			// 503, blocked-by-bot) won't short-circuit via lookupToolCache
			// and the LLM can re-dispatch the same call indefinitely
			// until budget runs out. Applies only to cacheableTools
			// (pure-read tools where identical args genuinely give the
			// same answer); state-mutating calls fall through unchanged.
			//
			// Tool calls fire from a per-call goroutine in RunAgentLoop,
			// so dispatchCounts MUST be protected — toolMu is the existing
			// mutex on chatTurn that also guards toolCache and toolCalls.
			if cacheableTools[name] {
				key := toolCallKey(name, args)
				t.toolMu.Lock()
				if t.dispatchCounts == nil {
					t.dispatchCounts = map[string]int{}
				}
				prior := t.dispatchCounts[key]
				if prior >= dispatchCallCap {
					t.dispatchCounts[key] = prior + 1
					attempted := t.dispatchCounts[key] - 1
					t.toolMu.Unlock()
					msg := fmt.Sprintf("You've already dispatched %s with these exact args %d times this turn. The result is whatever it was — re-dispatching won't change it. Either USE the result from one of the prior calls, or call something DIFFERENT (different URL, different query, different tool). Don't retry the same call.",
						name, attempted)
					if !hidden {
						t.sse.Send(map[string]any{
							"kind": "activity",
							"type": "error",
							"id":   activityCheapID(),
							"text": "⛔ " + callLabel + " refused (dispatch cap)",
						})
					}
					// Persist refused dispatches too — they're real LLM
					// attempts that consumed budget. Without recording,
					// the saved transcript shows N calls, the export
					// reader is confused about where the "you've already
					// dispatched" feedback came from. Err carries the
					// refusal message so it renders as an error row.
					t.recordToolCall(toolCallRecord{
						Name: name,
						Args: args,
						Err:  msg,
					})
					return msg, nil
				}
				t.dispatchCounts[key] = prior + 1
				t.toolMu.Unlock()
			}
			var msgID, callID string
			if !hidden {
				t.sse.Send(map[string]any{
					"kind": "activity",
					"type": "cmd",
					"id":   activityCheapID(),
					"text": callLabel,
				})
				msgID = t.ensureBubbleForTool()
				if msgID != "" {
					callID = t.emitToolCall(msgID, name, args, "", prefix)
				}
			}
			imgN, vidN, fileN := sessAttachmentCounts(sess)
			out, err := orig(args)
			// Spill-to-workspace guard. When a tool result is larger
			// than the inline cap, write the full body to the session
			// workspace and replace `out` with a small stub that points
			// at it. The LLM then reads slices via workspace(head/tail/
			// grep/read_lines/stat). Prevents one fat result (a large
			// read_local_file, a verbose agents() dispatch, an unbounded
			// pipeline output) from blowing the context window.
			if err == nil {
				if spilled, ok := maybeSpillToolResult(sess, name, out); ok {
					out = spilled
				}
			}
			rec := toolCallRecord{Name: name, Args: args, Result: out}
			if err != nil {
				rec.Err = err.Error()
				if !hidden {
					t.sse.Send(map[string]any{
						"kind": "activity",
						"type": "error",
						"id":   activityCheapID(),
						"text": fmt.Sprintf("%s → %v", name, err),
					})
				}
			} else if trimmed := strings.TrimSpace(out); trimmed != "" && !hidden {
				t.sse.Send(map[string]any{
					"kind": "activity",
					"type": "output",
					"id":   activityCheapID(),
					"text": truncate(out, 4000),
				})
			}
			if msgID != "" {
				t.emitToolResult(msgID, callID, name, out, err)
				t.flushNewAttachments(sess, msgID, imgN, vidN, fileN)
			}
			if err == nil && agentMutationTools[name] {
				// Signal the chat page that the dropdown's option list
				// is stale. The page's listener re-fetches /api/agents
				// and rebuilds the select; UI stays in sync without
				// the user having to reload after a Chat-driven
				// create_agent / update_agent / delete_agent / clone.
				t.sse.Send(map[string]any{
					"kind": "event",
					"name": "orchestrate_agents_changed",
				})
			}
			t.recordToolCall(rec)
			return out, err
		}
	}
	return tools
}

// agentMutationTools is the set of agent-CRUD tools whose successful
// invocation invalidates the agent dropdown's option list. Kept here
// (next to the wrapper that reads it) rather than spread across the
// individual tool files because the wrapper is what owns the SSE.
var agentMutationTools = map[string]bool{
	"create_agent": true,
	"update_agent": true,
	"delete_agent": true,
	"clone_agent":  true,
	// add_tool mutates AgentRecord.Tools (the bundled-custom-tools
	// list). The Tools button label reads that count, so include the
	// signal here too — the page-side listener refreshes the count
	// without the user having to reopen the agent dropdown.
	"add_tool": true,
	// tool_def writes to session_temp_tools. The Tools button label
	// also reflects session tools, so its grouped surface gets the
	// same refresh signal. Fires on every tool_def action, including
	// list/help/delete — cheap to refresh, no harm in over-firing.
	"tool_def": true,
}

// hiddenToolChips lists tools whose invocation should NOT render a
// tool_call / tool_result chip in the conversation pane (and should
// not produce activity-pane rows either). These are tools whose
// effect is already visible to the user through a richer surface —
// respond_directly's "result" is the final reply text the user reads;
// send_status's status line shows up via the framework's status
// channel. A chip alongside the better surface is just noise.
//
// The wrapper still runs the handler, drains any attachments the
// tool produced, and records the call into the per-turn tool log so
// follow-up step prompts can reference it. Only the user-facing
// emissions are skipped.
var hiddenToolChips = map[string]bool{
	"respond_directly": true,
	"send_status":      true,
	"plan_set":         true,
	"ask_user_form":    true,
}

// sessAttachmentCounts snapshots the session's attachment slice lengths
// so wrapToolsForActivity can detect new entries the tool appended
// during its run. nil-safe — control tools may pass nil sess when they
// have no attachment surface. Reads are race-free because tools run
// synchronously within the agent loop goroutine (mirrors chat's
// flushNewImages assumption).
func sessAttachmentCounts(sess *ToolSession) (int, int, int) {
	if sess == nil {
		return 0, 0, 0
	}
	return len(sess.Images), len(sess.Videos), len(sess.Files)
}

// flushNewAttachments emits SSE events for any image / video / file
// entries the tool appended to sess past the snapshot lengths. Each
// payload carries msgID so the runtime renders them under the bubble
// the tool fired from. Mirrors chat's flushNewImages pattern but
// targets a specific assistant bubble instead of "the current one".
func (t *chatTurn) flushNewAttachments(sess *ToolSession, msgID string, imgN, vidN, fileN int) {
	if sess == nil {
		return
	}
	// Use the session's claim-API: each call atomically claims an
	// exclusive range of new attachments and advances the per-
	// session flushed marker. This prevents the parallel-goroutine
	// race where two tool-call goroutines each captured len=0
	// before either appended, then each flushed the FULL slice
	// (causing 2× delivery on 2 distinct attach calls, 3× on 3, etc).
	// imgN/vidN/fileN are kept in the signature for backward-compat
	// callers but ignored — the session-level markers are
	// authoritative.
	_ = imgN
	_ = vidN
	_ = fileN
	for _, b64 := range sess.ClaimUnflushedImages() {
		t.sse.Send(map[string]any{
			"kind":   "image",
			"msg_id": msgID,
			"data":   b64,
		})
	}
	for _, b64 := range sess.ClaimUnflushedVideos() {
		t.sse.Send(map[string]any{
			"kind":   "video",
			"msg_id": msgID,
			"data":   b64,
		})
	}
	for _, f := range sess.ClaimUnflushedFiles() {
		t.sse.Send(map[string]any{
			"kind":      "file",
			"msg_id":    msgID,
			"name":      f.Name,
			"mime_type": f.MimeType,
			"data":      f.Data,
			"size":      f.Size,
		})
	}
}

// ensureBubbleForTool returns the active orchestrator bubble id,
// materializing an empty assistant bubble if no text has streamed in
// this round yet. That way a tool-only round still gives the inline
// tool chip somewhere to attach.
func (t *chatTurn) ensureBubbleForTool() string {
	id := t.getCurrentMsgID()
	if id != "" {
		return id
	}
	id = fmt.Sprintf("orch-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   id,
		"text": "",
	})
	t.setCurrentMsgID(id)
	return id
}

// emitToolCall emits the inline tool-chip SSE event Agency renders
// in the conversation pane. namePrefix is optional — when set
// (variadic), it prepends to the chip's name field so sub-agent
// dispatches show as "↳ [Target Name] knowledge_search" instead
// of blending in with the parent's own tool calls. The canonical
// tool name passed to the LLM is unaffected — only the display
// chip changes.
func (t *chatTurn) emitToolCall(msgID, name string, args map[string]any, suffix string, namePrefix ...string) string {
	displayName := name
	if len(namePrefix) > 0 && namePrefix[0] != "" {
		displayName = namePrefix[0] + name
	}
	// Generate a per-dispatch call_id so emitToolResult can be paired
	// back to THIS exact tool_call regardless of arrival order. Without
	// this the client falls back to "last unmatched" positional pairing
	// (see core/ui/runtime.go tool_result case), which silently
	// mis-attributes when calls don't strictly settle in emission order
	// (async dispatch, parallel tool calls in one model response,
	// cached short-circuits emitted alongside live calls). The caller
	// passes the returned ID to emitToolResult; both events ride
	// together so the client can correlate by ID.
	callID := UUIDv4()
	// args is the COMPACT one-line summary shown in the inline chip
	// — values clipped at 60 chars so a row of tool calls stays
	// readable. args_full is the unclipped, structured view rendered
	// inside the expanded <details> body so the user can see the
	// actual command / script / arguments without re-running the
	// turn. Without args_full, debugging "what did the Builder
	// actually run?" required reading the server log.
	callArgs := args
	if callArgs == nil {
		callArgs = map[string]any{}
	}
	payload := map[string]any{
		"kind":      "tool_call",
		"msg_id":    msgID,
		"call_id":   callID,
		"name":      displayName,
		"args":      summarizeToolArgs(args) + suffix,
		"args_full": callArgs,
	}
	// Mark pipeline-mode temp tools so the client can decorate the
	// pill differently (today: an emoji prefix). Detected by walking
	// the session's TempTools list — cheap, the list is small.
	if kind := t.toolKindFor(name); kind != "" {
		payload["tool_kind"] = kind
	}
	t.sse.Send(payload)
	return callID
}

// toolKindFor returns "pipeline" when name belongs to a pipeline-mode
// TempTool in either the session-defined or persistent pool. Empty
// string for everything else (regular registered tools, shell/api
// temp tools).
func (t *chatTurn) toolKindFor(name string) string {
	if t.session == nil {
		return ""
	}
	// Walk session-resolved TempTools first (faster, hits the common
	// case). Persistent store is consulted only if not found inline.
	// Cheap either way at gohort scale.
	for _, attached := range LoadSessionTempTools(t.udb, t.session.ID) {
		if attached.Name == name && attached.Mode == TempToolModePipeline {
			return "pipeline"
		}
	}
	for _, p := range LoadPersistentTempTools(t.udb, t.user) {
		if p.Tool.Name == name && p.Tool.Mode == TempToolModePipeline {
			return "pipeline"
		}
	}
	return ""
}

func (t *chatTurn) emitToolResult(msgID, callID, name, out string, err error) {
	payload := map[string]any{
		"kind":    "tool_result",
		"msg_id":  msgID,
		"call_id": callID,
		"name":    name,
	}
	if err != nil {
		payload["result"] = "error: " + err.Error()
	} else {
		payload["result"] = truncate(out, 4000)
	}
	t.sse.Send(payload)
}

// summarizeToolArgs renders the args map into a single compact
// "key=val, key=val" string for the inline tool chip's summary row.
// Drops newlines, clips long values. Empty args produce empty string.
func summarizeToolArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch vv := args[k].(type) {
		case string:
			s = vv
		case []any:
			elems := make([]string, 0, len(vv))
			for _, e := range vv {
				elems = append(elems, fmt.Sprintf("%v", e))
			}
			s = "[" + strings.Join(elems, ", ") + "]"
		case map[string]any:
			s = "{…}"
		case nil:
			s = "null"
		default:
			s = fmt.Sprintf("%v", vv)
		}
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, s))
	}
	return strings.Join(parts, ", ")
}

// lastUserContent returns the .Content of the last user-role message
// in the LLM history, or "" when there isn't one. Used by the
// pre-plan knowledge search to query against the current user
// message before kicking off the orchestrator.
func lastUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// priorAssistantContext returns a continuity block for the worker —
// the user's immediately preceding turn (their question + the
// orchestrator's reply) so follow-ups like "tell me more" land with
// the right referent. Empty when this is the first turn or the
// session pointer isn't available.
//
// We pull from t.session.Messages (the in-memory mutable session
// the runner has been writing to), filter out the CURRENT user
// message (already the last entry), and surface the prior user +
// assistant exchange. Older history isn't included — the worker is
// step-focused, not conversation-aware; the orchestrator and the
// synthesis round handle deep history.
func (t *chatTurn) priorAssistantContext() string {
	if t == nil || t.session == nil {
		return ""
	}
	msgs := t.session.Messages
	// Last entry is the user's current message (handleSend appended
	// it). Look at what comes before that.
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs = msgs[:n-1]
	}
	if len(msgs) == 0 {
		return ""
	}
	var lastUser, lastAssistant string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && lastAssistant == "" {
			lastAssistant = strings.TrimSpace(msgs[i].Content)
			continue
		}
		if msgs[i].Role == "user" && lastUser == "" {
			lastUser = strings.TrimSpace(msgs[i].Content)
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}
	if lastAssistant == "" && lastUser == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Prior turn in this conversation\n")
	b.WriteString("Use this to resolve references like \"it\", \"that\", \"the X you mentioned\" in the user's new message.\n\n")
	if lastUser != "" {
		b.WriteString("**User previously asked:** ")
		b.WriteString(snippetOneLine(lastUser, 600))
		b.WriteString("\n\n")
	}
	if lastAssistant != "" {
		b.WriteString("**You replied:** ")
		b.WriteString(snippetOneLine(lastAssistant, 800))
		b.WriteString("\n\n")
	}
	return b.String()
}

// titleAfterFirstTurn fires a background goroutine that asks the
// worker LLM for a short, descriptive title given the first
// user/assistant exchange, then patches it onto the session. No-op
// when this isn't a fresh session or there's no LLM. Failures stay
// local — title generation is an optimization, not part of the
// reply contract.
//
// The goroutine takes its own context (background + timeout) since
// the parent request returns as soon as the reply is delivered;
// keeping the title work on the request's ctx would cancel it the
// moment handleSend returns.
func (t *chatTurn) titleAfterFirstTurn() {
	if t == nil || !t.isNewSession || t.app == nil || t.app.LLM == nil || t.session == nil {
		return
	}
	// Snapshot what the goroutine needs — the request's chatTurn /
	// session pointer don't outlive handleSend.
	llm := t.app.LLM
	udb := t.udb
	agentID := t.agent.ID
	sessID := t.session.ID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Log("[orchestrate.title] panicked for session=%s: %v", sessID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Re-load so the goroutine sees the persisted messages
		// (the parent already saved the assistant reply before
		// firing this).
		s, ok := loadChatSession(udb, agentID, sessID)
		if !ok {
			return
		}
		title := generateSessionTitle(ctx, llm, s)
		if title == "" || title == s.Title {
			return
		}
		s.Title = title
		_, _ = saveChatSession(udb, s)
		Log("[orchestrate.title] session=%s renamed: %q", sessID, title)
	}()
}

// emitStats pushes a per-message stats payload (tk/s, input/output/
// thinking tokens, elapsed) into the conversation pane so the user can
// see throughput per assistant turn. Mirrors the chat app's
// "12.3 tk/s · 1450 in · 230 out · 187 think · 18.7s" footer.
//
// Nil-safe: tool-only LLM rounds (no content output) get an empty
// payload and the framework skips rendering.
func (t *chatTurn) emitStats(msgID string, resp *Response, start time.Time) {
	if msgID == "" {
		return
	}
	elapsedMs := time.Since(start).Milliseconds()
	payload := map[string]any{
		"kind":       "stats",
		"id":         msgID,
		"elapsed_ms": elapsedMs,
	}
	usage := &ChatMessageUsage{ElapsedMs: elapsedMs}
	if resp != nil {
		payload["input_tokens"] = resp.InputTokens
		payload["output_tokens"] = resp.OutputTokens
		payload["reasoning_tokens"] = resp.ReasoningTokens
		usage.InputTokens = resp.InputTokens
		usage.OutputTokens = resp.OutputTokens
		usage.ReasoningTokens = resp.ReasoningTokens
		// Prefer the backend's per-phase throughput (llama.cpp) when
		// available — matches what the user sees in llama.cpp's own
		// UI. Fall back to a coarse output_tokens / elapsed otherwise.
		if resp.PredictedPerSecond > 0 {
			payload["tokens_per_sec"] = resp.PredictedPerSecond
			payload["prompt_per_sec"] = resp.PromptPerSecond
			usage.TokensPerSec = resp.PredictedPerSecond
			usage.PromptPerSec = resp.PromptPerSecond
		} else if resp.OutputTokens > 0 {
			elapsed := time.Since(start)
			if elapsed > 0 {
				rate := float64(resp.OutputTokens) / elapsed.Seconds()
				payload["tokens_per_sec"] = rate
				usage.TokensPerSec = rate
			}
		}
	}
	t.sse.Send(payload)
	t.lastUsageMu.Lock()
	t.lastUsage = usage
	t.lastUsageMu.Unlock()
}

// drainLastUsage returns the most-recent emitStats usage and clears
// the slot. Called from handleSend right before persisting an
// assistant ChatMessage so the saved record carries the stats footer.
// Returns nil when no stats event has fired (tool-only round, error
// before any LLM call) — callers omit Usage in that case.
func (t *chatTurn) drainLastUsage() *ChatMessageUsage {
	t.lastUsageMu.Lock()
	u := t.lastUsage
	t.lastUsage = nil
	t.lastUsageMu.Unlock()
	return u
}

// leadInMaxLen is the cutoff that separates a mid-round lead-in (one
// short sentence the model writes before a tool call — finalized as its
// own message bubble) from a full answer mis-emitted before a tool
// (cleared, so it doesn't double the answer the model then writes in its
// final reply). See the onStep !info.Done branch.
const leadInMaxLen = 600

// emitStatus pushes a phase-narration row into the activity pane.
// Mirrors servitor's status events ("Investigator: synthesizing…",
// "Verifying names…") — high-level signposts distinct from the plan
// block's per-step pips and the per-tool-call cmd/output rows.
//
// No bubble reset here. orchestrate locks the activity pane off, so
// the status text is invisible in this surface anyway; resetting
// currentMsgID would split consecutive tool calls across separate
// bubbles for an invisible event. Visible new cards (intent blocks
// at worker-step boundaries, message_done at round close) own the
// bubble reset explicitly.
func (t *chatTurn) emitStatus(text string) {
	t.sse.Send(map[string]any{
		"kind": "activity",
		"type": "status",
		"id":   activityCheapID(),
		"text": text,
	})
}

func init() {
	RegisterTunable(TunableSpec{Key: "tune_ack_timeout", Category: "Timeouts", Label: "Acknowledgment timeout", Help: "Bounds the fast \"On it…\" acknowledgment call.", Kind: KindSeconds, Default: 8, Min: 1, Max: 60})
}

// ackTimeout bounds the fast acknowledgment call. Short — the ack
// is only useful if it lands while the user is staring at dead air;
// a slow ack is worse than none.
func ackTimeout() time.Duration { return TuneDuration("tune_ack_timeout") }

// ackEnabled gates the concurrent "On it…" acknowledgment (see emitAck
// and its launch site). Off by default — on small llama.cpp slot pools
// the ack can't get a slot and is pure overhead; the "Thinking…" status
// already covers the dead air. Set true on a server with spare slots.
const ackEnabled = false

// emitAck fires a fast, no-think worker call that produces a short
// natural acknowledgment ("On it — checking that now.") and streams
// it as a status the moment it returns. Runs as a goroutine launched
// at turn start so it overlaps round-1 planning — the orchestrator's
// first round uses thinking mode, which delays its first visible
// output by seconds; this fills that gap with a contextual ack
// instead of a bare "Thinking…".
//
// The ack call itself decides whether an ack is warranted: greetings,
// thanks, and instantly-answerable questions get "NONE" back and emit
// nothing, so conversational turns don't get a needless "On it!".
func (t *chatTurn) emitAck(ctx context.Context, userMsg string) {
	if t == nil || t.app == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, ackTimeout())
	defer cancel()
	sys := "A user just messaged an assistant that may need tools, web search, or sub-agents to answer. In ONE short, natural sentence, acknowledge you're on it (e.g. \"On it — let me look that up.\" / \"Sure, checking now.\" / \"Give me a sec to dig into that.\"). Do NOT answer the request, do NOT ask questions, do NOT name tools or agents. If the message is a greeting, a thanks, or something you'd answer instantly with no lookup, reply with exactly NONE."
	resp, err := t.app.WorkerChat(cctx,
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sys), WithMaxTokens(30), WithThink(false),
		// Best-effort ack: never retry. The 8s ackTimeout is already
		// blown by the time it fails, so a retry just re-pays the wait
		// and spams "[retry] attempt failed" / "chat failed" for a call
		// whose result is optional. A failed ack is a silent no-op.
		WithMaxRetries(0),
	)
	if err != nil || resp == nil {
		return
	}
	ack := strings.TrimSpace(resp.Content)
	if ack == "" || strings.HasPrefix(strings.ToUpper(ack), "NONE") {
		return
	}
	// Strip wrapping quotes the model sometimes adds.
	ack = strings.Trim(ack, "\"'")
	t.emitStatus(ack)
}

// resolveTopic returns the turn's knowledge topic. With the per-turn
// classifier removed, this is just the value passed at chatTurn
// construction (sub-agent dispatches set it from the parent) or
// generalTopic. Kept as a method so callers don't reach into the
// field directly — leaves room to evolve scope without churning
// every call site.
func (t *chatTurn) resolveTopic() string {
	if t == nil || t.topic == "" {
		return generalTopic
	}
	return t.topic
}

// agentHasRetrievableContent reports whether the agent has any
// corpus the knowledge tools could surface this turn. Used to gate
// knowledge_search + fetch_knowledge_doc out of catalogs where they
// would only produce empty / not-found results — the LLM sees the
// tools, reaches for them, hallucinates doc_ids, and burns rounds.
// Considers:
//   - the agent's own AttachedCollections
//   - IngestAttachments (uploaded files land in the agent's per-(user,agent) bucket)
//   - active skills' AttachedCollections (active-path only)
//   - deployment-scope collections that auto-attach when the agent's
//     AttachedCollections list is empty (the open-pool default)
func (t *chatTurn) agentHasRetrievableContent() bool {
	if t == nil {
		return false
	}
	if len(t.agent.AttachedCollections) > 0 || t.agent.IngestAttachments {
		return true
	}
	for _, sk := range t.skillsActive {
		if len(sk.AttachedCollections) > 0 {
			return true
		}
	}
	// Open-pool default: when the agent has NO explicit collections,
	// deployment-scope collections auto-attach. If any exist, the agent
	// effectively has retrievable content.
	if len(t.agent.AttachedCollections) == 0 {
		for _, c := range ListCollections(nil, "") {
			if IsDeploymentScope(c) {
				return true
			}
		}
	}
	return false
}

// appendSearchOrderGuidance inserts a "knowledge before web" stub
// into the system prompt when the agent has both a knowledge corpus
// AND web tools enabled. Without this, most models default to
// web_search for any question they're unsure about, even when the
// agent's own corpus, skill-attached documents, or accumulated
// findings already have the answer. The stub reorders that default.
//
// Skipped when:
//   - the agent has no web tools in its catalog (no choice to reorder)
//   - the agent is Builder (its persona has its own search rhythm)
func (t *chatTurn) appendSearchOrderGuidance(sys string) string {
	return sys + searchOrderGuidanceBlock(t.agent)
}

// searchOrderGuidanceBlock is the chatTurn-free form so the shared capability
// assembler can render it for the channel/dispatch path too. Returns "" when
// the agent has no web tools (nothing to reorder) or is Builder.
func searchOrderGuidanceBlock(agent AgentRecord) string {
	if isBuilderAgent(agent.ID) {
		return ""
	}
	// Only fire when web tools are actually in the agent's effective
	// catalog — otherwise there's nothing to deprioritize. AllowedTools
	// empty = default pool (web tools likely present); otherwise check
	// the explicit list.
	hasWeb := len(agent.AllowedTools) == 0
	if !hasWeb {
		for _, name := range agent.AllowedTools {
			if name == "web_search" || name == "fetch_url" || name == "browse_page" {
				hasWeb = true
				break
			}
		}
	}
	if !hasWeb {
		return ""
	}
	return `

## Search order — knowledge first

Before reaching for web_search / fetch_url, ask: "Does this question require information that has CHANGED since my documents were written?" If no, call ` + "`knowledge_search`" + ` first — APIs don't change daily, runbooks haven't been edited, last year's policies haven't moved. Web is the exception. One cheap query beats an unnecessary web round.

If results from different documents disagree on a specific point, surface the conflict ("Doc A says X but Doc B says Y") rather than averaging or silently picking one.`
}

// facts loads the agent's structured key/value Explicit Memory entries
// (the always-in-prompt facts layer). Re-read every call — cheap and
// ensures store_fact mid-turn lands in subsequent worker steps.
// Gated on DisableExplicit; the framing of WHAT goes here is shaped
// by the agent's KnowledgeFraming.
func (t *chatTurn) facts() []MemoryFact {
	if t.agent.DisableExplicit {
		return nil
	}
	return ListMemoryFacts(t.udb, factsNamespace(t.agent.ID))
}

// gatedPersona returns the agent's persona prompt with any
// `<!-- @requires-tools: ... -->` sections stripped when the
// listed tools aren't in the effective tool set. Empty AllowedTools
// = no restriction (nil sentinel → strip nothing). When AllowedTools
// is explicit, the gate set is (framework always-on tools) +
// (agent's allowlist) so framework-provided things like plan_set
// and store_fact stay visible regardless of what the admin trimmed.
func (t *chatTurn) gatedPersona(prompt string) string {
	if len(t.agent.AllowedTools) == 0 {
		return StripPromptSectionsForTools(prompt, nil)
	}
	gate := append([]string{
		// Framework always-on tools — sections that depend on these
		// never get stripped regardless of admin allowlist.
		"plan_set",
		"ask_user", "ask_user_form",
		"knowledge_search", "fetch_knowledge_doc",
		"memory",
		"store_fact", "forget_fact", "list_facts",
	}, t.agent.AllowedTools...)
	return StripPromptSectionsForTools(prompt, gate)
}

// (The auto-classifier — activeSkillsForTurn + ActiveSkillsWithScores
// + trigger/embedding/gatekeeper layers — was removed. Skills now
// activate ONLY when the LLM calls activate_skill(name). The
// previous design auto-fired skills via a stateless classifier
// against the latest user message, which mid-conversation drift
// silently changed the agent's behavior with no LLM awareness.
// LLM-driven activation makes the choice visible in the activity
// log alongside every other tool call.)

// toolCallKey is the dedup key for the per-turn cache. JSON-marshal
// in sorted-key order via encoding/json's deterministic map output so
// semantically equal args collide regardless of how the LLM ordered
// the JSON keys. Argument-less calls still get a stable key.
func toolCallKey(name string, args map[string]any) string {
	if len(args) == 0 {
		return name + "()"
	}
	b, err := json.Marshal(args)
	if err != nil {
		// Hash failure shouldn't crash the call — just skip caching
		// by returning a unique-per-attempt key.
		return name + "?" + fmt.Sprintf("%p", &args)
	}
	return name + "|" + string(b)
}

// recordToolCall appends to the per-turn log and the dedup cache.
// Holds toolMu so concurrent tool calls (parallel workers in some
// agent loops) don't race.
func (t *chatTurn) recordToolCall(rec toolCallRecord) {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	t.toolCalls = append(t.toolCalls, rec)
	// Only cache successful results from tools on the cacheableTools
	// allowlist — symmetric with lookupToolCache. Skipping the write
	// for non-cacheable tools also keeps the per-turn cache map
	// small (the log itself still records every call).
	if rec.Err == "" && cacheableTools[rec.Name] {
		if t.toolCache == nil {
			t.toolCache = map[string]string{}
		}
		t.toolCache[toolCallKey(rec.Name, rec.Args)] = rec.Result
	}
}

// captureMidTurnBubble records one finalized assistant bubble's text
// so it survives session reload. Called from runPlan / runWorkerStep
// when a round closes with non-empty narration that the user saw live
// via SSE but would otherwise vanish (the saved transcript previously
// kept only the final synthesis / directReply / question). Empty
// strings are dropped — the live UI doesn't materialize a bubble for
// tool-only rounds, and we mirror that here.
func (t *chatTurn) captureMidTurnBubble(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	// Snapshot the tool calls that have fired SINCE the previous mid-
	// turn bubble was captured. This attributes each tool call to the
	// assistant message that triggered it, instead of dumping all of
	// them onto the final message — which made the export almost
	// useless for tuning because you couldn't tell which call followed
	// which piece of reasoning.
	//
	// Hold both mutexes briefly to take a consistent slice + index
	// advance. toolMu first to match the lock order used elsewhere.
	t.toolMu.Lock()
	calls := t.persistedToolCallsFromUnlocked(t.lastBubbleToolIdx)
	t.lastBubbleToolIdx = len(t.toolCalls)
	t.toolMu.Unlock()
	t.bubblesMu.Lock()
	t.midTurnBubbles = append(t.midTurnBubbles, ChatMessage{
		Role:      "assistant",
		Content:   trimmed,
		Created:   time.Now(),
		ToolCalls: calls,
	})
	t.bubblesMu.Unlock()
}

// persistedToolCallsFromUnlocked returns the slice of tool calls from
// index `from` onward, converted to the persisted shape. Caller MUST
// hold toolMu. Used by captureMidTurnBubble to attribute calls to the
// specific assistant message that triggered them, and by
// persistedToolCalls (the existing helper) to return calls since the
// LAST mid-turn snapshot for the FINAL assistant message.
func (t *chatTurn) persistedToolCallsFromUnlocked(from int) []PersistedToolCall {
	if from < 0 {
		from = 0
	}
	if from >= len(t.toolCalls) {
		return nil
	}
	out := make([]PersistedToolCall, 0, len(t.toolCalls)-from)
	for _, rec := range t.toolCalls[from:] {
		out = append(out, PersistedToolCall{
			Name:   rec.Name,
			Args:   rec.Args,
			Result: rec.Result,
			Err:    rec.Err,
			Cached: rec.Cached,
		})
	}
	return out
}

// drainMidTurnBubbles returns all bubbles captured so far and clears
// the buffer. Called by handleSend right before appending the final
// assistant message so they land in the saved transcript in order.
func (t *chatTurn) drainMidTurnBubbles() []ChatMessage {
	t.bubblesMu.Lock()
	defer t.bubblesMu.Unlock()
	if len(t.midTurnBubbles) == 0 {
		return nil
	}
	out := t.midTurnBubbles
	t.midTurnBubbles = nil
	return out
}

// appendMidTurnBubbles drops captured bubbles whose content is a near-
// duplicate of the final reply (the stream-then-respond_directly /
// stream-then-synthesize-same-bulk-different-conclusion case the live
// SSE path's exact-match dedup couldn't catch). Then appends every
// remaining bubble into the session's transcript in the order it was
// emitted.
//
// "Near-duplicate" here means substantial shared LEADING content — the
// orchestrator draft + synthesis "same analysis, revised conclusion"
// pattern. See isNearDuplicate for the threshold.
// Returns any ToolCalls from dropped near-duplicate bubbles so the
// caller can merge them into the final reply's ToolCalls — otherwise
// short turns (one round of tool calls, then a final reply with the
// same text) silently lose every tool record when the mid-turn
// bubble that carried them gets dedup'd against the final reply.
// "What time is it?" → time_in_zone called → assistant emits the
// answer once → final reply equals the bubble → bubble dropped →
// tool call invisible in the saved transcript was the symptom.
func appendMidTurnBubbles(sess *ChatSession, bubbles []ChatMessage, finalReply string) []PersistedToolCall {
	var orphanedCalls []PersistedToolCall
	if len(bubbles) == 0 {
		return nil
	}
	if final := strings.TrimSpace(finalReply); final != "" {
		kept := bubbles[:0]
		for _, b := range bubbles {
			if isNearDuplicate(b.Content, final) {
				if len(b.ToolCalls) > 0 {
					orphanedCalls = append(orphanedCalls, b.ToolCalls...)
				}
				continue
			}
			kept = append(kept, b)
		}
		bubbles = kept
	}
	if len(bubbles) == 0 {
		return orphanedCalls
	}
	sess.Messages = append(sess.Messages, bubbles...)
	return orphanedCalls
}

// persistIncompleteTurnTrace saves a fallback assistant record when a turn ends
// WITHOUT a final reply — a plan/synthesis error or timeout — but tools already
// fired this turn. Without it the handler returns having saved only the user
// message, so the thread reloads blank even though real side effects happened:
// the "schedule created in Cortex but wiped from history" report is exactly this
// — recurring(schedule) ran, then synthesis timed out on runaway reasoning and
// the confirmation was never persisted. No-op when nothing actually ran.
func persistIncompleteTurnTrace(sess *ChatSession, udb Database, turn *chatTurn, reason string) {
	bubbles := turn.drainMidTurnBubbles()
	orphanCalls := appendMidTurnBubbles(sess, bubbles, "")
	finalCalls := append(orphanCalls, turn.persistedToolCalls()...)
	// Bail only when the turn produced NOTHING — no mid-turn answers AND no
	// tool calls. Previously this returned whenever there were no tool calls,
	// which silently dropped the mid-turn answers the user saw live: the
	// bubbles were appended to sess.Messages above but the early return skipped
	// the save, so the reloaded thread / Copy-session export lost them. A turn
	// that only narrated (answered) before failing must still persist those
	// answers.
	if len(bubbles) == 0 && len(finalCalls) == 0 {
		return
	}
	// The synthetic "didn't finish" trailer is only meaningful when tool calls
	// ran OUTSIDE a captured bubble (their record would otherwise have no
	// owning message). A turn that produced only narration answers keeps the
	// bubbles alone — no trailer noise.
	if len(finalCalls) > 0 {
		sess.Messages = append(sess.Messages, ChatMessage{
			Role:      "assistant",
			Content:   "_(This reply didn't finish — " + reason + " — but the tool actions this turn did run and are recorded above.)_",
			Created:   time.Now(),
			Usage:     turn.drainLastUsage(),
			ToolCalls: finalCalls,
		})
	}
	_, _ = saveChatSession(udb, *sess)
}

// isNearDuplicate returns true when two strings share substantial
// LEADING content even if they diverge at the end. Used to dedup mid-
// turn bubbles against the final reply when the orchestrator emitted
// a draft that the synthesis pass then re-rendered with a changed
// conclusion. Exact-match dedup misses this; full substring / LCS
// dedup is too aggressive (drops legit shorter messages that happen
// to overlap). The longest-common-prefix ratio (vs the shorter string)
// is the narrowest catch: a 1500-char shared analysis with a 200-char
// diverging conclusion (LCP / short ≈ 0.88) drops; a short opener
// like "Looking into your question, …" shared between a brief
// narration bubble and a long final reply (LCP / short ≈ 0.2) doesn't.
//
// Threshold 0.6: tuned conservative — error toward keeping bubbles
// over dropping them, matching the deliberate posture of the live SSE
// dedup. Two identical strings register as 1.0 (still drop).
func isNearDuplicate(a, b string) bool {
	na := normalizeForDedup(a)
	nb := normalizeForDedup(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	minLen := len(na)
	if len(nb) < minLen {
		minLen = len(nb)
	}
	lcp := 0
	for lcp < minLen && na[lcp] == nb[lcp] {
		lcp++
	}
	if lcp == 0 {
		return false
	}
	return float64(lcp)/float64(minLen) >= 0.6
}

// normalizeForDedup lowercases, trims, and collapses runs of whitespace
// so cosmetic differences (extra newlines, casing, trailing space)
// don't defeat the dedup. Returns "" for empty / whitespace-only
// inputs so callers can use the empty check to bail.
func normalizeForDedup(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// persistedToolCalls converts the per-turn tool-call log into the
// shape persisted alongside the assistant message. Called at session
// save time so the export trace has every tool fired during the turn,
// not just the ones the live UI rendered. Returns nil for turns
// where no tools ran (the JSON tag is omitempty so the field
// vanishes in those cases).
func (t *chatTurn) persistedToolCalls() []PersistedToolCall {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	// Slice from the LAST mid-turn snapshot index so the final
	// assistant message only carries calls that fired AFTER the most
	// recent mid-turn bubble. captureMidTurnBubble took the earlier
	// calls and attributed them to their owning bubbles. Without this
	// the final message would double-list calls already counted on
	// mid-turn bubbles.
	return t.persistedToolCallsFromUnlocked(t.lastBubbleToolIdx)
}

// cacheableTools is the opt-in allowlist of tools whose results are
// safe to cache for the rest of a turn. The default is DON'T cache —
// blanket caching silently returns stale data for anything mutating,
// hides intentional retries (probe an endpoint, fix, retry), and
// confuses agents like Builder that iterate against the same args.
//
// Members must be:
//   - read-only (no side effects)
//   - idempotent (same args → same result within a turn's timescale)
//   - expensive enough that the cache hit is worth it
//
// Network-fetched content (web_search, fetch_url, browse_page) hits
// all three: external API call, deterministic for a given query in
// the moment, can be slow. Knowledge search is read-only against an
// embedding index. Add to this map deliberately — when in doubt,
// leave it out.
var cacheableTools = map[string]bool{
	"web_search":          true,
	"fetch_url":           true,
	"browse_page":         true,
	"knowledge_search":    true,
	"fetch_knowledge_doc": true,
}

// lookupToolCache returns a cached result for (name, args) if present.
// Errors are not cached — a previously-failing call gets a second shot.
// Tools not in cacheableTools bypass the lookup so probing/iterating
// flows always run fresh.
func (t *chatTurn) lookupToolCache(name string, args map[string]any) (string, bool) {
	if !cacheableTools[name] {
		return "", false
	}
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	if t.toolCache == nil {
		return "", false
	}
	v, ok := t.toolCache[toolCallKey(name, args)]
	return v, ok
}

// toolLogPromptSection formats the per-turn log for injection into a
// subsequent step's user prompt. Empty when nothing has run yet.
//
// Each entry is clipped so the prompt doesn't explode — the LLM
// sees a snippet ("X costs $20…"); if it wants the full result it
// can re-call deliberately (the cache will short-circuit).
func (t *chatTurn) toolLogPromptSection() string {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	if len(t.toolCalls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Tool calls already made this turn\n")
	b.WriteString("These calls ran in earlier steps. DO NOT repeat them — the cache short-circuits anyway, but a re-call wastes a round. Use what was already found:\n\n")
	for _, rec := range t.toolCalls {
		b.WriteString("- ")
		b.WriteString(formatToolCall(rec.Name, rec.Args))
		b.WriteString("\n  ")
		if rec.Err != "" {
			b.WriteString("ERROR: ")
			b.WriteString(rec.Err)
		} else {
			b.WriteString("→ ")
			b.WriteString(snippetOneLine(rec.Result, 500))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// snippetOneLine collapses to a single line + length-clip for the
// tool-log prompt section. Keeps the worker's context manageable.
func snippetOneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		cut := max
		if sp := strings.LastIndexByte(s[:max], ' '); sp > max/2 {
			cut = sp
		}
		s = s[:cut] + "…"
	}
	return s
}

// handleSend drives one user turn against an agent:
//
//  1. Append the user message to the session.
//  2. Orchestrator round 1: thinking LLM with the plan_set tool.
//     Returns a list of step titles.
//  3. Emit a plan block + one activity row per step.
//  4. For each step: call the worker LLM, stream its output to the
//     activity pane, flip the step status to done.
//  5. Orchestrator synthesis round: thinking LLM composes a final
//     user-facing reply given the original question + all worker
//     outputs. Stream as chunk events into the conversation pane.
//  6. Save the session (messages + plan snapshot) and emit done.
//
// Plans auto-confirm — no operator pause between steps in Phase 1.
func (T *OrchestrateApp) handleSend(w http.ResponseWriter, r *http.Request, udb Database, user string, agent AgentRecord) {
	T.handleSendWithAppTools(w, r, udb, user, agent, nil)
}

// handleSendWithAppTools is handleSend plus extra host-app tools injected into
// the agent's catalog for this run (see chatTurn.appTools). The plain handleSend
// passes nil.
func (T *OrchestrateApp) handleSendWithAppTools(w http.ResponseWriter, r *http.Request, udb Database, user string, agent AgentRecord, appTools []AgentToolDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message          string        `json:"message"`
		SessionID        string        `json:"session_id,omitempty"`
		History          []ChatMessage `json:"history,omitempty"`
		PrivateMode      bool          `json:"private_mode,omitempty"`
		InferredDisabled bool          `json:"inferred_disabled,omitempty"`
		// Incognito (clean-room session) — honored only on the FIRST turn, when
		// the session is created; afterwards the stored session flag governs.
		Incognito bool `json:"incognito,omitempty"`
		// Hidden marks the user message as already-shown-in-prior-bubble
		// (e.g. an ask_user card's submitted state). LLM still sees the
		// content in history; the chat panel just skips rendering a
		// duplicate user bubble.
		Hidden    bool     `json:"hidden,omitempty"`
		Images    []string `json:"images,omitempty"` // base64-encoded image data from the chat panel's paperclip
		Documents []struct {
			Name     string `json:"name"`
			MimeType string `json:"mime_type"`
			Data     string `json:"data"` // base64
		} `json:"documents,omitempty"` // pdf / docx / text attachments — server extracts and prepends to Message
		// IntakeValues, when set, marks this submission as the agent's
		// intake form. Persisted on the user message so re-edit on
		// replayed sessions can rehydrate the form with original values.
		IntakeValues map[string]string `json:"intake_values,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Extract attached documents (PDFs, DOCX, text files). Hold them
	// in extractedAttachments for now — the preamble build runs LATER,
	// after the chat session is resolved, so we have a workspace dir
	// available for spill-on-large.
	//
	// Why split phases: a 200-page PDF text-extracts to ~500KB–2MB of
	// plain text. Prepending that whole thing to req.Message blows the
	// model's context in one round, even before any tool calls. Large
	// attachments instead spill to <workspace>/.attachments/<name>.txt
	// and inject a stub preamble (filename + size + sample + path +
	// directive to use workspace stat/head/grep/read_lines) so the LLM
	// pulls only the slices it needs.
	type extractedAttachment struct {
		name     string
		mime     string
		text     string
		failNote string // non-empty when decode / extraction failed
	}
	const minIngestChars = 200
	var extractedAttachments []extractedAttachment
	type ingestPair struct{ name, text string }
	var ingestQueue []ingestPair
	if len(req.Documents) > 0 {
		for _, d := range req.Documents {
			raw, err := base64.StdEncoding.DecodeString(d.Data)
			if err != nil {
				extractedAttachments = append(extractedAttachments, extractedAttachment{
					name:     d.Name,
					failNote: fmt.Sprintf("[attached %s — decode failed: %v]\n\n", d.Name, err),
				})
				continue
			}
			text, err := ExtractDocument(r.Context(), DocumentAttachment{
				Name:     d.Name,
				MimeType: d.MimeType,
				Data:     raw,
			})
			if err != nil {
				extractedAttachments = append(extractedAttachments, extractedAttachment{
					name:     d.Name,
					failNote: fmt.Sprintf("[attached %s — extraction failed: %v]\n\n", d.Name, err),
				})
				continue
			}
			extractedAttachments = append(extractedAttachments, extractedAttachment{
				name: d.Name,
				mime: d.MimeType,
				text: text,
			})
			if agent.IngestAttachments && len(strings.TrimSpace(text)) >= minIngestChars {
				ingestQueue = append(ingestQueue, ingestPair{name: d.Name, text: text})
			}
		}
		// Provisionally satisfy the "message or attachment required"
		// check below: a non-empty marker stands in for the eventual
		// preamble we'll write after the session exists. Replaced
		// verbatim with the real preamble after session create.
		if strings.TrimSpace(req.Message) == "" {
			for _, a := range extractedAttachments {
				if a.failNote != "" || strings.TrimSpace(a.text) != "" {
					req.Message = "[attachments-being-processed]"
					break
				}
			}
		}
		// Background ingest — runs outside the reply path so the user
		// doesn't wait on embedding latency. Each attachment gets its
		// own chunk (filename as subject) under the agent's knowledge
		// store, topic="attachments". Later turns retrieve them via
		// knowledge_search like any other ingested content.
		if len(ingestQueue) > 0 {
			go func(items []ingestPair, user, agentID string) {
				// Recover — this runs off the reply path, so a panic in
				// the embedding backend or vector-store write would
				// otherwise crash the whole server. Match the title
				// goroutine's guard above.
				defer func() {
					if r := recover(); r != nil {
						Log("[orchestrate.attachments] ingest panicked for agent=%s: %v", agentID, r)
					}
				}()
				for _, it := range items {
					ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
					ingestAgentKnowledge(ctx, T.DB, user, agentID, "attachments", it.name, it.text)
					cancel()
					Log("[orchestrate.attachments] ingested %q (%d chars) into agent=%s knowledge[attachments]", it.name, len(it.text), agentID)
				}
			}(ingestQueue, user, agent.ID)
		}
	}
	// Validate AFTER extraction so attachment-only sends (e.g. "here's
	// a resume, analyze it" with only a PDF and no typed text) are
	// accepted as long as SOMETHING came in. The message can be empty
	// only when at least one image is attached (vision tier picks it
	// up via Message.Images) — pure documents already wrote their
	// preamble into req.Message above, so an empty message past this
	// point means nothing was attached at all.
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 {
		http.Error(w, "message or attachment required", http.StatusBadRequest)
		return
	}
	// Attachment-only turns get a synthetic message so the LLM has
	// something to react to. For image-only turns the vision LLM
	// sees the images directly; we just need a non-empty content
	// field for the message-role/Content contract.
	if strings.TrimSpace(req.Message) == "" {
		if len(req.Images) > 0 {
			req.Message = "[attached image — please analyze]"
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Channel agents no longer force every send onto one thread — the client
	// sends the channel session id (cortexSessionID) when the Channel row is
	// open, and ordinary session ids otherwise. The channel thread is created
	// under its requested id on first turn just like any session.
	// Resolve session (create on first turn, otherwise load).
	sess, _ := loadChatSession(udb, agent.ID, req.SessionID)
	isNewSession := sess.ID == ""
	if isNewSession {
		sess = ChatSession{
			// Honor the requested id so a pinned session (the Operator's
			// "operator-thread") is actually CREATED under that id on its
			// first turn. Empty req.SessionID (a normal new chat) falls
			// through to saveChatSession minting a fresh UUID.
			ID:        req.SessionID,
			AgentID:   agent.ID,
			Title:     titleFromFirstMessage(req.Message),
			Created:   time.Now(),
			Incognito: req.Incognito, // clean-room session, set at creation
		}
		var err error
		sess, err = saveChatSession(udb, sess)
		if err != nil {
			// Bare sseWriter for this one error; the run hasn't been
			// created yet so there's no buffer to tee into.
			tmp := newSSEWriter(w)
			tmp.Send(map[string]any{"kind": "error", "text": "save session: " + err.Error()})
			return
		}
	}

	// The user is actively in this session for the whole turn, so clear its
	// unread once the turn (and its final reply-save) is done. Registered
	// early so it runs LAST (LIFO), after every other save; reads the freshest
	// persisted LastAt. Background wakes use RunAgentSyncContinuing instead —
	// they never mark seen, which is what leaves a wake unread.
	defer func() { markSessionSeen(udb, agent.ID, sess.ID) }()

	// Build the attachment preamble now that the chat session (and a
	// resolvable workspace path) is available. Large extractions spill
	// to <workspace>/.attachments/<sanitized-name>.txt and inject a
	// stub; small ones inline as before via FormatAttachmentPreamble.
	//
	// Workspace dir: per-user root (same default the agent loop falls
	// back to in newToolSession). The agent loop's later ToolSession
	// resolves to the same directory unless a workspace(create/use)
	// switched to a managed one mid-turn — which doesn't happen before
	// the first user message anyway. So a spilled attachment is always
	// readable via workspace(action="head"/"grep"/...) at the path the
	// stub prints.
	if len(extractedAttachments) > 0 {
		attachSess := &ToolSession{
			Username:      user,
			ChatSessionID: sess.ID,
		}
		if ws, err := EnsureWorkspaceDir(user); err == nil {
			attachSess.WorkspaceDir = ws
		}
		var preamble strings.Builder
		for _, a := range extractedAttachments {
			if a.failNote != "" {
				preamble.WriteString(a.failNote)
				continue
			}
			preamble.WriteString(buildAttachmentPreamble(attachSess, a.name, a.mime, a.text))
		}
		// Replace the provisional marker if it was used, else prepend.
		if req.Message == "[attachments-being-processed]" {
			req.Message = strings.TrimRight(preamble.String(), "\n")
		} else if preamble.Len() > 0 {
			req.Message = preamble.String() + req.Message
		}
	}

	// Detach the agent loop's lifetime from the HTTP request. Earlier
	// the loop derived its ctx from r.Context(), so a client disconnect
	// (browser nav-away, network blip, desktop overlay opening) killed
	// every in-flight tool call mid-turn. Now ctx is rooted at
	// Background; cancellation is explicit — either /api/cancel from
	// the user, or the run-registry's auto-replace when a fresh turn
	// starts on the same session.
	//
	// The run tees every SSE frame into an in-memory buffer (see runs.go),
	// so a fresh /api/runs/<id>/stream subscriber after a reconnect
	// can replay the conversation from any sequence number.
	ctx, cancel := context.WithCancel(context.Background())
	run := T.runsRegistry().Create(user, agent.ID, sess.ID, cancel)
	sse := newTeeSSEWriter(w, run)

	// SSE response headers — handleSend streams frames inline off this response.
	// text/event-stream + X-Accel-Buffering:no so neither a reverse proxy nor the
	// browser holds frames back (matches handleRunsStream, which set these; this
	// handler historically relied on content sniffing + flush alone). Set before
	// the first frame is written.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Disconnect watchdog: when the original request's context fires
	// (client navigated away, network blip, the desktop app quit),
	// drop the live HTTP-response writer from sse. The loop runs to
	// completion thanks to the detached ctx above; this prevents
	// sse.Send from wedging the entire turn on a write to a dead
	// TCP connection (the symptom: the loop hangs after a tool exit
	// because the post-tool activity-event Send blocks holding the
	// sseWriter mutex). Run-buffer subscribers (a fresh
	// /api/runs/<id>/stream after the client reconnects) keep
	// receiving events fine.
	go func() {
		<-r.Context().Done()
		sse.detachLive()
	}()

	// inflightCancels stays populated for /api/cancel backward compat
	// (the cancel endpoint key is sessionID, same shape as before).
	inflightCancels.Store(sess.ID, cancel)
	defer func() {
		cancel()
		inflightCancels.Delete(sess.ID)
		// Mark the run done. Complete is idempotent; the panic
		// recovery defer below runs FIRST (LIFO) and gets to upgrade
		// to Failed if a panic occurred, so the unconditional
		// Completed here is the "no panic happened" path.
		run.Complete(RunStatusCompleted)
	}()

	// Always carry the run ID alongside session so the chat panel can
	// open /api/runs/<id>/stream after a reconnect. session event
	// keeps its prior shape for backward compat.
	sse.Send(map[string]any{"kind": "session", "id": sess.ID})
	sse.Send(map[string]any{"kind": "run", "id": run.ID})
	// Surface any panic in the turn pipeline through the SSE stream
	// AND the gohort log. The http server has its own panic recovery
	// that just tears down the connection; without this, a silent
	// panic in runPlan / runWorkerStep / synthesis looks like "no
	// reply" to the user and shows no clue in the log.
	//
	// Runs first in LIFO order so it gets to mark the run Failed
	// before the outer defer marks it Completed (Run.Complete is
	// idempotent — first call wins).
	defer func() {
		if rec := recover(); rec != nil {
			Log("[orchestrate /api/send] PANIC recovered: %v", rec)
			sse.Send(map[string]any{"kind": "error", "text": fmt.Sprintf("server panic: %v", rec)})
			run.Complete(RunStatusFailed)
		}
	}()

	// Append the user message to the persisted thread now; failures
	// downstream still leave the user's input visible on reload.
	userMsg := ChatMessage{Role: "user", Content: req.Message, Created: time.Now(), Hidden: req.Hidden}
	if len(req.IntakeValues) > 0 {
		userMsg.IntakeValues = req.IntakeValues
	}
	sess.Messages = append(sess.Messages, userMsg)
	if saved, err := saveChatSession(udb, sess); err == nil {
		sess = saved
	}
	// Ongoing-conversation compaction (channel home thread only): the RUN sees a
	// bounded view — a running summary + the ContextDepth tail — and STORAGE is
	// likewise bounded to the summary + a generous tail (trimStoredHistory), with
	// folded-out spans archived to the recall index rather than kept inline, so
	// this long-lived thread doesn't grow without limit. Folds aging turns into
	// the summary and evicts durable facts into memory. Gated to the channel
	// thread so a channel agent's ordinary ad-hoc sessions stay verbatim (they're
	// disposable; only the persistent home thread needs the running summary).
	//
	// CRITICAL: the bounded view is RUN-ONLY — it feeds runPlan below and
	// nothing else. It must NOT be written back to sess.Messages: the
	// CompactState.SummarizedThrough cursor indexes the FULL history, so
	// re-feeding an already-shortened list overshoots the cursor, clamps the
	// tail to empty, and silently drops the user's just-typed message from
	// BOTH the LLM payload and (via the end-of-turn save) storage — the model
	// then answers from the stale summary alone. Keep sess.Messages full; the
	// dispatch path (agent_dispatch.go) already uses this run-only pattern.
	planMsgs := sess.Messages
	// Bound any PERSISTENT thread — the Cortex home thread AND every channel
	// room. Both are keyed "channel:<id>" and both accumulate turn after turn
	// with no fresh-session reset, so both grow until they blow the context
	// window (observed: a channel room reached 299k tokens against a 262k
	// window, force-compact couldn't recover, and the turn died at round 0).
	// The gate used to require agent.Cortex, so a channel agent with the Cortex
	// FEATURE off ran this whole path unbounded — and context_depth read as
	// "default (12)" while never taking effect, because the code that applies
	// that default is exactly what the gate skipped. A normal chat session is
	// safe unbounded: it gets a fresh id per conversation and is short-lived.
	if strings.HasPrefix(sess.ID, "channel:") {
		planMsgs = T.compactOperatorHistory(udb, user, agent, sess.ID, sess.Messages)
		// Bound STORAGE too (not just the run-view): drop leading messages already
		// folded into the summary AND archived to the recall index, keeping the
		// summary + a generous verbatim tail. Caps the per-turn (and 3s cortex-poll)
		// load of this long-lived thread instead of growing it forever; older
		// content stays recoverable via recall_history. Cursor stays consistent.
		sess.Messages = T.trimStoredHistory(udb, agent, sess.ID, sess.Messages)
	}

	if T.LLM == nil {
		sse.Send(map[string]any{"kind": "error", "text": "worker LLM not configured"})
		return
	}

	// Register the per-session interjection queue so /api/inject can
	// find one. Released at the end of the turn so a subsequent
	// inject hits "session not interjectable" rather than landing
	// notes nobody will drain.
	//
	// Before release, drain any notes still queued — they arrived
	// after the last in-turn drain point (during synthesis, during
	// the directReply path, etc.) and would otherwise be destroyed.
	// Drained notes are appended as user messages on the session so
	// they survive into the NEXT turn as part of normal history.
	// Defers run LIFO, so this fires before release.
	queue := registerInjectionQueue(sess.ID, user, agent.ID)
	defer releaseInjectionQueue(sess.ID)
	defer func() {
		if queue == nil {
			return
		}
		leftover := queue.Drain()
		if len(leftover) == 0 {
			return
		}
		udb := UserDB(T.DB, user)
		if udb == nil {
			return
		}
		// Reload session so we don't clobber any other writes that
		// landed during the turn (the turn struct's snapshot may be
		// stale by this defer's firing time).
		latest, ok := loadChatSession(udb, agent.ID, sess.ID)
		if !ok {
			return
		}
		now := time.Now()
		for _, n := range leftover {
			latest.Messages = append(latest.Messages, ChatMessage{
				Role: "user", Content: n.Text, Created: now,
			})
		}
		_, _ = saveChatSession(udb, latest)
		Log("[orchestrate.inject] preserved %d leftover note(s) into session %s for next turn", len(leftover), sess.ID)
	}()

	// ForcePrivate on the agent record locks Private mode ON
	// regardless of what the user toggled. Composes with the
	// per-turn user toggle via OR — either source can force the
	// network connector to refuse.
	privateMode := req.PrivateMode || agent.ForcePrivate
	// Build ONE connector per turn and share it across ctx,
	// sess.Network, and the inflight registry. Sharing one
	// instance is what makes the mid-turn cutoff work: when the
	// privacy endpoint flips this connector via SetAllowed, every
	// in-flight tool re-checking Allowed() sees the new state.
	turnConnector := NewNetworkConnector(privateMode)
	ctx = WithNetworkConnector(ctx, turnConnector)
	inflightConnectors.Store(sess.ID, turnConnector)
	defer inflightConnectors.Delete(sess.ID)
	// Also lock ForcePrivate agents so the privacy endpoint can't
	// flip them OFF — the agent-level lock is the source of truth.
	if agent.ForcePrivate {
		// no-op marker; SetAllowed(true) from the endpoint will be
		// reverted by the next Allowed() check if we add an
		// auto-revert guard. Simpler for now: trust the endpoint
		// to refuse turning private OFF when ForcePrivate is set
		// (endpoint reads the agent record).
		_ = agent.ForcePrivate
	}

	turn := &chatTurn{
		app:         T,
		ctx:         ctx,
		sse:         sse,
		udb:         udb,
		user:        user,
		agent:       agent,
		queue:       queue,
		session:     &sess,
		privateMode: privateMode,
		network:     turnConnector,
		// Incognito (clean-room) sessions also suppress the inferred memory layer
		// (write side) — nothing accumulates back, matching "no baggage out".
		inferredDisabled: req.InferredDisabled || sess.Incognito,
		isNewSession:     isNewSession,
		userImages:       decodeUserImages(req.Images),
		// from_client.* tools are exposed only when the request came from the
		// gohort-desktop viewer (its proxy stamps the bridge key) — never a
		// remote browser/phone on the same account.
		fromDesktopClient: user != "" && DesktopClientUser(r) == user,
		// Flag the turn as having fresh external content if the user
		// attached any document. The consolidation loop-break gate
		// reads this to decide whether to ingest the synthesis.
		userDocsThisTurn: len(req.Documents) > 0,
		deliveredSkills:  map[string]bool{},
		appTools:         appTools,
	}
	for _, d := range req.Documents {
		if n := strings.TrimSpace(d.Name); n != "" {
			turn.docNames = append(turn.docNames, n)
		}
	}
	// (Skills are per-turn now: turn.skillsActive starts empty and is
	// populated only by activate_skill calls THIS turn. Nothing is
	// rehydrated from the session — activation is not sticky across turns.)
	// Reset the session's "awaiting user confirm" flag — the two-turn
	// gate that read this is removed. Kept here to clear stale state
	// from older sessions that may have set it; if THIS turn ends with
	// ask_user again the dispatch path re-sets it as needed.
	sess.AwaitingUserConfirm = false
	// Lift the authoring-in-progress slot from its side-table onto the
	// in-memory ChatSession so chatTurn-bound tool handlers (notably
	// create_pipeline_tool) can read it without their own DB lookup.
	// Side-table because the global create_agent handler can write
	// without knowing the per-agent session-storage shape.
	if a := loadAuthoringInProgress(udb, sess.ID); a != "" {
		sess.AuthoringAgentID = a
	}

	// Note: orchestrate does NOT auto-promote follow-ups into a prior
	// sub-agent. Dispatch (agents(run)) is synchronous and one-shot —
	// its reply is a tool result the orchestrator synthesizes from.
	// Continued conversation with a sub-agent happens the normal way:
	// the user keeps chatting (unlimited turns, full session history)
	// and the LLM re-calls agents(run) when it wants to delegate again
	// — which re-threads the same sub-session by its deterministic ID.
	// Auto-promotion was removed: it routed worker-mode dispatches as
	// conversational replies (producing empty bubbles) and was
	// redundant with just chatting. Injection/promotion remain a
	// phantom concern, where async dispatch makes them meaningful.

	// (Per-turn topic auto-classifier removed. The LLM picks the
	// topic slug when it calls memory_save / memory_search via the
	// topic= arg — the system prompt's "Known topics" block shows
	// existing slugs to nudge reuse. Without the classifier, this
	// turn starts unscoped; tools fall back to generalTopic when
	// the LLM doesn't pass one.)

	// Fire a fast acknowledgment concurrently so the user sees a
	// contextual "On it…" while round-1 planning (thinking mode)
	// produces its first real output. Skipped automatically for
	// greetings / trivial asks (the ack call returns NONE). Promotion
	// turns already emit their own "Routing follow-up…" status and
	// returned above, so this only runs on main-LLM turns.
	//
	// DISABLED by default: the ack is a 3rd concurrent LLM call that
	// competes with this turn's own lead + worker calls for the same
	// backend slots. On constrained servers (llama.cpp --parallel <= 2)
	// it never lands — it just queues, times out at ackTimeout, and adds
	// latency + log noise — while its value is purely cosmetic and the
	// "Thinking…" status below already fills the dead air. Flip
	// ackEnabled to true on a server with spare slots to re-enable.
	if ackEnabled {
		go turn.emitAck(ctx, req.Message)
	}

	// --- Orchestrator round 1: respond directly, plan, or ask ---
	turn.emitStatus("Thinking…")
	steps, question, directReply, planErr := turn.runPlan(planMsgs)
	if planErr != nil {
		persistIncompleteTurnTrace(&sess, udb, turn, planErr.Error())
		sse.Send(map[string]any{"kind": "error", "text": "plan: " + planErr.Error()})
		return
	}
	if directReply != "" {
		// runPlan already emitted the bubble live — either streamed inline during
		// the agent loop, or promoted post-loop via emitCapturedAsBubble (which
		// suppresses only a true near-duplicate of the last streamed bubble). Just
		// persist + run background consolidation + title generation.
		turn.emitStatus("Direct response.")
		orphanCalls := appendMidTurnBubbles(&sess, turn.drainMidTurnBubbles(), directReply)
		finalCalls := append(orphanCalls, turn.persistedToolCalls()...)
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: "assistant", Content: directReply,
			Created: time.Now(), Usage: turn.drainLastUsage(),
			// ToolCalls: the orchestrator's tool log this turn — same
			// data the plan_set path persists at the bottom of this
			// function. orphanCalls picks up any tool records from
			// mid-turn bubbles that got dedup'd as near-duplicates of
			// directReply — without that merge, short turns lose the
			// only carrier of their tool record.
			ToolCalls: finalCalls,
		})
		_, _ = saveChatSession(udb, sess)
		turn.titleAfterFirstTurn()
		sse.Send(map[string]any{"kind": "done"})
		return
	}
	if question != "" {
		// runPlan already emitted the question bubble. Just persist
		// + end the turn — the user's next message will re-enter the
		// plan round with the answer in context.
		turn.emitStatus("Orchestrator asked for clarification.")
		orphanCalls := appendMidTurnBubbles(&sess, turn.drainMidTurnBubbles(), question)
		finalCalls := append(orphanCalls, turn.persistedToolCalls()...)
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: "assistant", Content: question,
			Created: time.Now(), Usage: turn.drainLastUsage(),
			// ToolCalls: ask_user / ask_user_form are themselves tool
			// calls; record everything that fired this turn (including
			// the ask itself) so the export shows what led to the
			// question. orphanCalls preserves tool records from
			// dedup'd mid-turn bubbles — symmetric with directReply.
			ToolCalls: finalCalls,
		})
		_, _ = saveChatSession(udb, sess)
		turn.titleAfterFirstTurn()
		sse.Send(map[string]any{"kind": "done"})
		return
	}
	if len(steps) == 0 {
		// Degenerate: orchestrator chose neither to plan nor to ask.
		// Run a single pseudo-step "Respond" so the flow still
		// produces a worker output and a synthesis reply.
		turn.emitStatus("No plan needed — direct response.")
		steps = []PlanStep{{ID: 1, Title: "Respond directly", Status: StepPending}}
	} else {
		turn.emitStatus(fmt.Sprintf("Plan committed — %d step%s.", len(steps), plural(len(steps))))
	}

	// Plan and per-step status all render as a single in-chat block;
	// the runner re-emits the same block id on each transition and the
	// dispatcher routes the repeat into the renderer's onUpdate.
	roundIdx := len(sess.Plans)
	blockID := planBlockID(sess.ID, roundIdx)
	emitPlanBlock(sse, blockID, steps)

	// --- Per-step worker execution ---
	for i := range steps {
		select {
		case <-ctx.Done():
			sse.Send(map[string]any{"kind": "error", "text": "cancelled"})
			return
		default:
		}
		steps[i].Status = StepInProgress
		emitPlanBlock(sse, blockID, steps)
		// Servitor-style intent narration block lands in the
		// conversation pane before the worker fires so the user
		// sees "▸ Investigating: Step N: <title> — <intent>"
		// inline. Activity-pane status row stays too for the
		// right-pane audit trail.
		emitIntentBlock(sse, fmt.Sprintf("%s-intent-%d", blockID, steps[i].ID), steps[i])
		turn.emitStatus(fmt.Sprintf("▸ Step %d/%d: %s", steps[i].ID, len(steps), steps[i].Title))

		// Pull anything the user has queued since the prior step (or
		// since the plan was set). drainNotes persists them as user
		// ChatMessages and emits notes_consumed so client bubbles
		// re-style as "agent saw your note".
		stepNotes := turn.drainNotes()
		if n := len(stepNotes); n > 0 {
			turn.emitStatus(fmt.Sprintf("Picked up %d user note%s mid-flight.", n, plural(n)))
		}
		out, err := turn.runWorkerStep(steps[:i], steps[i], req.Message, stepNotes)
		if err != nil {
			steps[i].Status = StepBlocked
			steps[i].BlockedReason = err.Error()
			steps[i].Output = "error: " + err.Error()
			emitPlanBlock(sse, blockID, steps)
			turn.emitStatus(fmt.Sprintf("✗ Step %d/%d failed: %s", steps[i].ID, len(steps), truncate(err.Error(), 120)))
			continue
		}
		steps[i].Status = StepDone
		steps[i].Output = out
		steps[i].Findings = deriveFindings(out)
		emitPlanBlock(sse, blockID, steps)
		// Completion status — show what was actually accomplished
		// instead of leaving the user with the pre-step "▸ Step N:
		// <title>" line. Uses the same derived findings the plan
		// card shows; if findings are empty (worker returned
		// nothing meaningful), fall back to the title.
		summary := steps[i].Findings
		if summary == "" {
			summary = steps[i].Title
		}
		turn.emitStatus(fmt.Sprintf("✓ Step %d/%d: %s", steps[i].ID, len(steps), truncate(summary, 160)))
	}

	// --- Post-plan gap detection (opt-in) ---
	// Research-flavored agents set GapCheck=true so the runner scans
	// the worker outputs for structural failure modes (abstract
	// claims, evidence asymmetry, mechanism gaps) and runs targeted
	// fills before synthesis. Chat-flavored agents leave it off —
	// gap detection is a cost the conversational surface doesn't
	// need to pay every turn.
	if agent.GapCheck && len(steps) > 0 {
		turn.emitStatus("Checking for gaps…")
		gaps := turn.runGapCheck(req.Message, steps, len(steps)+1)
		if len(gaps) > 0 {
			turn.emitStatus(fmt.Sprintf("Found %d gap%s — filling…", len(gaps), plural(len(gaps))))
			// Append the new steps onto the plan and re-emit the
			// block so the user sees them join the existing card.
			steps = append(steps, gaps...)
			emitPlanBlock(sse, blockID, steps)
			for i := len(steps) - len(gaps); i < len(steps); i++ {
				select {
				case <-ctx.Done():
					sse.Send(map[string]any{"kind": "error", "text": "cancelled"})
					return
				default:
				}
				steps[i].Status = StepInProgress
				emitPlanBlock(sse, blockID, steps)
				emitIntentBlock(sse, fmt.Sprintf("%s-intent-%d", blockID, steps[i].ID), steps[i])
				turn.emitStatus(fmt.Sprintf("▸ Gap %d/%d: %s", i-(len(steps)-len(gaps))+1, len(gaps), steps[i].Title))
				stepNotes := turn.drainNotes()
				out, err := turn.runWorkerStep(steps[:i], steps[i], req.Message, stepNotes)
				gapPos := i - (len(steps) - len(gaps)) + 1
				if err != nil {
					steps[i].Status = StepBlocked
					steps[i].BlockedReason = err.Error()
					steps[i].Output = "error: " + err.Error()
					emitPlanBlock(sse, blockID, steps)
					turn.emitStatus(fmt.Sprintf("✗ Gap %d/%d failed: %s", gapPos, len(gaps), truncate(err.Error(), 120)))
					continue
				}
				steps[i].Status = StepDone
				steps[i].Output = out
				steps[i].Findings = deriveFindings(out)
				emitPlanBlock(sse, blockID, steps)
				summary := steps[i].Findings
				if summary == "" {
					summary = steps[i].Title
				}
				turn.emitStatus(fmt.Sprintf("✓ Gap %d/%d: %s", gapPos, len(gaps), truncate(summary, 160)))
			}
		} else {
			turn.emitStatus("No gaps detected.")
		}
	}

	// --- Orchestrator synthesis round ---
	synthNotes := turn.drainNotes()
	if n := len(synthNotes); n > 0 {
		turn.emitStatus(fmt.Sprintf("Picked up %d user note%s before synthesis.", n, plural(n)))
	}
	turn.emitStatus("Composing reply…")
	reply, synthErr := turn.runSynthesis(req.Message, steps, synthNotes)
	if synthErr != nil {
		persistIncompleteTurnTrace(&sess, udb, turn, synthErr.Error())
		sse.Send(map[string]any{"kind": "error", "text": "synthesis: " + synthErr.Error()})
		return
	}

	// Persist the orchestrator's final reply + the plan snapshot +
	// the full tool-call trace for this turn (orchestrator rounds +
	// worker steps). The trace makes session export useful for
	// debugging and dataset capture without needing live SSE.
	// orphanCalls picks up tool records carried by mid-turn bubbles
	// that got dedup'd as near-duplicates of the synthesis reply.
	turnToolCalls := turn.persistedToolCalls()
	orphanCalls := appendMidTurnBubbles(&sess, turn.drainMidTurnBubbles(), reply)
	finalCalls := append(orphanCalls, turnToolCalls...)
	// The model did the work (tools fired) but wrote no summary — a blank bubble
	// renders as nothing, so the turn looks like it vanished. Mark it so the tool
	// trace stays reachable in history.
	if strings.TrimSpace(reply) == "" && len(finalCalls) > 0 {
		reply = "_(No written reply this turn — see the tool actions above.)_"
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role: "assistant", Content: reply,
		Created: time.Now(), Usage: turn.drainLastUsage(),
		ToolCalls: finalCalls,
	})
	sess.Plans = append(sess.Plans, PlanSnapshot{
		RoundIndex: len(sess.Plans),
		Steps:      steps,
	})
	_, _ = saveChatSession(udb, sess)

	// Hallucinated-authoring detection. The pattern we keep seeing
	// on smaller models: the assistant replies "I've created the
	// agent with tools X, Y, Z…" without ever firing add_tool /
	// create_agent this turn. Catch it by cross-checking three
	// signals — (1) build plan exists with pending steps, (2) zero
	// authoring tools fired this turn, (3) the reply has non-empty
	// text. If all three: surface a visible warning AND inject a
	// corrective note into the session so the next turn's history
	// shows the LLM exactly what it claimed vs what actually fired.
	mismatchFired := injectAuthoringMismatchWarning(&sess, turnToolCalls, reply)
	if mismatchFired {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "Build plan still has pending steps but no authoring tool fired this turn — the reply may be describing work that didn't actually happen. Next turn will be re-prompted.",
		})
	}
	// The third quadrant: no build plan at all, nothing errored because nothing
	// fired, and the reply is a forward-looking PROMISE to author rather than a
	// claim it's done. Suppressed when the mismatch check already fired so a
	// planless promise gets exactly one notice.
	if !mismatchFired && injectPromisedAuthoringWarning(&sess, turnToolCalls, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "The reply promised to create or update a tool or agent but no authoring call fired this turn — nothing was saved. Next turn is re-prompted to actually make the call.",
		})
	}
	// The other half of hallucinated authoring: an authoring tool DID fire but
	// every call errored, yet the reply claims success (the live moltbook case —
	// no BuildPlan, so the check above misses it).
	if injectFailedAuthoringWarning(&sess, turnToolCalls, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "Every authoring call this turn failed, but the reply claims it's done — the tool/agent was NOT saved. Next turn is re-prompted to actually fix it.",
		})
	}
	// The build closed out without ever running the gap check, so nothing
	// surfaced blocked steps or unverified tools. Same post-hoc shape as the two
	// checks above rather than a mid-turn block: the reply has already streamed
	// to the client, and discarding it to force another round re-renders the
	// content (see the correction/re-render rule).
	if injectSkippedGapReportWarning(&sess, udb, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "The build plan was closed out without calling report_build_gaps — blocked steps and unverified tools went unchecked. Next turn is re-prompted to verify before claiming done.",
		})
	}

	turn.titleAfterFirstTurn()

	sse.Send(map[string]any{"kind": "done"})
}

// agentAuthoringToolNames is the set of tools whose successful
// invocation actually advances the build plan. Used by the
// hallucinated-authoring check below to decide whether a turn that
// closed without firing any of these "really" did the work it
// claimed in its text reply.
var agentAuthoringToolNames = map[string]bool{
	"create_agent": true,
	"update_agent": true,
	"clone_agent":  true,
	"delete_agent": true,
	"add_tool":     true,
	"tool_def":     true,
}

// injectAuthoringMismatchWarning detects the "claimed but didn't fire"
// pattern and, on a match, appends a synthetic user-role message to
// sess.Messages that the next turn's toLLMMessages will surface to
// the LLM as a corrective system note. Returns true when a warning
// was injected so the caller can flush the session save and emit a
// visible SSE warning.
func injectAuthoringMismatchWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || sess.BuildPlan == nil {
		return false
	}
	pending := 0
	for _, s := range sess.BuildPlan.Steps {
		if s.Status != "done" && s.Status != "blocked" {
			pending++
		}
	}
	if pending == 0 {
		return false
	}
	if strings.TrimSpace(reply) == "" {
		return false
	}
	for _, tc := range turnToolCalls {
		if agentAuthoringToolNames[tc.Name] {
			return false
		}
	}
	note := "FRAMEWORK NOTICE: your previous reply described authoring work but no add_tool / create_agent / update_agent call fired during that turn. The build plan still has " +
		fmt.Sprintf("%d pending step", pending)
	if pending != 1 {
		note += "s"
	}
	note += ". Do not describe work you haven't done. Re-do the next pending step by ACTUALLY calling the right authoring tool with concrete arguments. One tool call per turn — that is the only path that advances the plan."
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: note,
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// injectSkippedGapReportWarning is the enforcement half of report_build_gaps.
// The tool's contract — "call this BEFORE your final reply" — lived only in the
// prompt and the tool description: BuildPlanState.GapsReported was set and
// never read, so a model that simply never called it wrapped up unchecked, and
// every unverified tool it authored went unmentioned.
//
// Fires only when the plan LOOKS FINISHED (no pending or in-progress steps) and
// the turn produced a reply anyway without ever gap-checking. That precondition
// matters: the plan-approval turn and every mid-execution turn legitimately end
// with a reply and no gap report, and warning on those would train the model to
// ignore the notice. The complement of injectAuthoringMismatchWarning above,
// which covers the pending-steps-but-nothing-fired case.
//
// Names the unverified tools directly, since skipping the call is exactly how
// their standing stays invisible — the correction has to carry the payload the
// skipped tool would have delivered, or the next turn just re-runs blind.
func injectSkippedGapReportWarning(sess *ChatSession, udb Database, reply string) bool {
	if sess == nil || sess.BuildPlan == nil || len(sess.BuildPlan.Steps) == 0 {
		return false
	}
	if sess.BuildPlan.GapsReported || strings.TrimSpace(reply) == "" {
		return false
	}
	for _, s := range sess.BuildPlan.Steps {
		if s.Status != "done" && s.Status != "blocked" {
			return false // still executing — a reply here is legitimate
		}
	}
	note := "FRAMEWORK NOTICE: your previous reply closed out the build plan without calling report_build_gaps. That call is required before any reply that presents the build as finished — it is what surfaces blocked steps and tools that are not verified. Marking a step done is your OWN claim and is not evidence the tool works."
	if un := unverifiedTools(udb, sess.ID); len(un) > 0 {
		note += "\n\nTools you authored that do NOT currently stand verified:"
		for _, u := range un {
			note += fmt.Sprintf("\n  - %s — %s", u.Tool, u.Reason)
		}
		note += "\n\nYou may have told the user these are working. Verify each one now (add_tool with test_args, or tool_def(action=\"test\")), then say plainly what was actually confirmed and what was not."
	} else {
		note += " Call it now and address whatever it returns before restating that the work is done."
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: note,
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// injectFailedAuthoringWarning catches the OTHER half of the hallucinated-
// authoring pattern: an authoring tool DID fire this turn but EVERY such call
// errored, yet the reply reads as a success claim. Unlike the build-plan check
// above it needs no BuildPlan, so it also covers a regular agent (the live
// moltbook case: tool_def create/update errored repeatedly, then the agent said
// "Done! I've rebuilt the toolbox"). Injects a hidden corrective note so the
// next turn stops claiming success and actually fixes the call.
func injectFailedAuthoringWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || strings.TrimSpace(reply) == "" {
		return false
	}
	fired, errored, ok := 0, 0, 0
	for _, tc := range turnToolCalls {
		if !agentAuthoringToolNames[tc.Name] {
			continue
		}
		fired++
		if strings.TrimSpace(tc.Err) != "" {
			errored++
		} else {
			ok++
		}
	}
	// Only when authoring was attempted, ALL of it errored, and the reply claims
	// success without acknowledging the failure.
	if fired == 0 || ok > 0 || errored == 0 || !claimsSuccessWithoutAck(reply) {
		return false
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: "FRAMEWORK NOTICE: your reply says a tool or agent was created, updated, or fixed — but EVERY authoring call this turn returned an error, so nothing was saved. Do NOT tell the user it's done. Read the error text, correct the arguments, and make ONE fixed authoring call (prefer action=\"update\" over delete+recreate).",
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// injectPromisedAuthoringWarning catches the THIRD hallucinated-authoring
// quadrant, the one the other two miss by construction:
// injectAuthoringMismatchWarning needs a BuildPlan with pending steps, and
// injectFailedAuthoringWarning needs an authoring call that fired and errored.
// A turn that describes a toolbox in prose, calls nothing, and sets no plan
// satisfies neither — it closes clean and the work silently evaporates.
//
// The tell is tense. claimsSuccessWithoutAck looks for a past-tense claim
// ("Done! I've rebuilt it"); this looks for the forward-looking promise that
// precedes it ("Great! I will now create the vapi_calls toolbox") and is never
// followed by the call. Both observed shapes are lead-side, not a small-model
// artifact, so the check is deliberately tense-driven rather than model-gated.
//
// Conservative, in the same spirit as its siblings — a missed catch beats a
// false one, since a spurious notice trains the model to ignore all three:
//   - any authoring tool fired → not this pattern, stay quiet
//   - ask_user or plan_set fired → "I'll create X" is a legitimate promise
//     about a turn that hasn't happened yet (the approval and plan-card paths)
//   - the reply is a question → it's proposing, not promising
func injectPromisedAuthoringWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || strings.TrimSpace(reply) == "" {
		return false
	}
	for _, tc := range turnToolCalls {
		if agentAuthoringToolNames[tc.Name] {
			return false
		}
		switch tc.Name {
		case "ask_user", "plan_set":
			return false
		}
	}
	if strings.HasSuffix(strings.TrimSpace(reply), "?") {
		return false
	}
	if !promisesAuthoringWithoutAction(reply) {
		return false
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role: "user",
		Content: "FRAMEWORK NOTICE: your previous reply said you were about to create or update a tool or agent, but no authoring call fired during that turn — tool_def / add_tool / create_agent / update_agent never ran, so nothing was saved and the user is waiting on work that never started. Describing the change is not making it. Make ONE concrete authoring call now with real arguments. If you need the user to approve first, call ask_user — do not promise in prose and end the turn.",
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// promisesAuthoringWithoutAction reports whether reply contains a forward-
// looking promise to author something. Requires a promise marker, an authoring
// verb, and an authoring object in the SAME sentence — all three, or "I'll use
// the get_weather tool" (promise + object, ordinary dispatch) would trip it.
func promisesAuthoringWithoutAction(reply string) bool {
	promise := []string{
		"i will", "i'll", "i am going to", "i'm going to", "let me",
		"going to now", "next i", "now i", "proceeding to",
	}
	verbs := []string{
		"create", "creating", "build", "building", "author", "authoring",
		"add", "adding", "set up", "setting up", "define", "defining",
		"register", "registering", "make", "making", "update", "updating",
		"wire up", "wiring up",
	}
	objects := []string{
		"tool", "toolbox", "agent", "action", "pipeline", "skill",
	}
	for _, s := range sentencesOf(strings.ToLower(reply)) {
		if containsAnyOf(s, promise) && containsAnyOf(s, verbs) && containsAnyOf(s, objects) {
			return true
		}
	}
	return false
}

// sentencesOf splits text on sentence terminators and newlines. Crude by
// design — it exists so the three-signal test above can't match across
// unrelated clauses, not to parse prose correctly.
func sentencesOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n' || r == ';'
	})
}

func containsAnyOf(hay string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// claimsSuccessWithoutAck reports whether reply asserts authoring success while
// NOT acknowledging a failure — the heuristic separating a false "it's done"
// from an honest "it didn't work." Conservative: any failure-acknowledging word
// suppresses it, so it errs toward NOT firing (a missed catch over a false one).
func claimsSuccessWithoutAck(reply string) bool {
	r := strings.ToLower(reply)
	for _, neg := range []string{
		"error", "fail", "couldn't", "could not", "didn't", "did not", "wasn't able",
		"was not able", "unable", "not able", "still broken", "isn't working", "not working",
		"went wrong", "try again", "reject", "no luck", "hasn't worked", "won't",
	} {
		if strings.Contains(r, neg) {
			return false
		}
	}
	for _, pos := range []string{
		"done", "created", "rebuilt", "rebuild", "fixed", "i've ", "i have ", "is live",
		"now live", "ready to use", "all set", "up and running", "successfully",
		"is working now", "it's working", "built the", "set up",
	} {
		if strings.Contains(r, pos) {
			return true
		}
	}
	return false
}

// handleCancel aborts an in-flight runner by session ID. The runner
// goroutine cleans up on ctx.Done.
func (T *OrchestrateApp) handleCancel(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	// The Agency chat panel POSTs the session id as the ?id= query param (no
	// body); older callers send {session_id} in the JSON body. Accept BOTH —
	// reading only the body meant the Agency cancel button silently no-opped
	// (empty body → empty id → cancel() never fired, loop ran to completion).
	sid := strings.TrimSpace(r.URL.Query().Get("id"))
	if sid == "" {
		var body struct {
			SessionID string `json:"session_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sid = strings.TrimSpace(body.SessionID)
	}
	if sid != "" {
		if v, ok := inflightCancels.Load(sid); ok {
			if cancel, ok := v.(context.CancelFunc); ok {
				cancel()
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- LLM rounds -------------------------------------------------------------

// shouldUseLeadModel reports whether this agent's main reasoning should
// escalate to the lead model this turn. Honors the per-agent opt-in, but
// the privacy gate wins (gate 2): a ForcePrivate agent or a Private-toggled
// turn keeps reasoning on the local worker so the conversation never leaves
// for the remote lead model. Gate 1 (no lead configured) and gate 3 (admin
// route ceiling) are enforced downstream by LeadChat / the route stage.
func (t *chatTurn) shouldUseLeadModel() bool {
	return t.agent.LeadModel && !t.agent.ForcePrivate && !t.privateMode
}

// frameworkConversationalTools is the single source of truth for the always-on
// framework toolset every CONVERSATIONAL turn gets — IDENTICAL on the web
// (runPlan) and the channel/dispatch (buildDispatchTurnExtras) surfaces. Built
// once here so the two catalogs can't drift: the recurring "works in the web UI
// but not over a channel" class of bug (send_status, stay_silent, graph memory,
// fetch_knowledge_doc, …) all came from these being hand-maintained in two
// places. Genuinely path-specific tools stay in their callers: compact_context
// (web closure var), the Builder authoring set, the agents-grouped tool's
// per-path gating, recurring, Fleet, and attached pipelines (the web wraps those
// for its activity pane). Session-bound where the handler needs it: find_tools
// searches the session catalog, send_status reaches the StatusCallback,
// stay_silent sets Silenced.
func (t *chatTurn) frameworkConversationalTools(sess *ToolSession) []AgentToolDef {
	out := []AgentToolDef{t.introspectToolDef()} // self-awareness — always
	// Knowledge — only when the agent has a corpus, else it hallucinates
	// doc_ids the handler must refuse. Skipped under the unified surface:
	// recall fronts knowledge search, and recall(id="doc:…") the drill-down.
	if !unifiedMemoryEnabled() && t.agentHasRetrievableContent() {
		out = append(out, t.searchKnowledgeToolDef(), t.fetchKnowledgeDocToolDef())
	}
	for _, n := range []string{"find_tools", "send_status", "stay_silent", "keep_going"} {
		if ct, ok := LookupChatTool(n); ok {
			out = append(out, ChatToolToAgentToolDefWithSession(ct, sess))
		}
	}
	out = append(out, t.loadToolToolDef(sess)) // gateway for the agent's lazy custom tools
	out = append(out, t.skillToolDefs()...)    // read_skill / skill_knowledge_*; nil when skills off
	if unifiedMemoryEnabled() {
		// Collapsed surface: remember / recall / forget replace the six
		// memory + knowledge tools (knowledge_search + fetch were skipped near
		// the top of this function). Graph tools aren't part of the collapse
		// and stay gated by explicitOff.
		if t.hasAnyMemoryLayer() {
			out = append(out, t.unifiedMemoryTools()...)
		}
		if !t.explicitOff() {
			out = append(out, t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	} else {
		if !t.inferredOff() {
			out = append(out, t.memoryToolDef()) // Reference Memory (memory_save / search / forget)
		}
		if !t.explicitOff() {
			// Explicit (store_fact / forget_fact) + Graph (link_entities / recall_about).
			out = append(out, t.storeFactToolDef(), t.forgetFactToolDef(), t.searchFactsToolDef(), t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	}
	// Working notes (rewritable running-state block) — its own opt-in layer,
	// independent of the Explicit/Reference memory toggles.
	if t.agent.EnableNotes {
		out = append(out, t.updateNotesToolDef())
	}
	out = append(out, cortexDeliverableTools(t.udb, t.agent.ID)...) // file_deliverable + note_to_cortex; nil for non-cortex
	// (send_to_builder removed — agents reach Builder by DIRECT dispatch
	// (agents action="run", agent="builder") and iterate with it in-thread, rather
	// than handing the user a one-click link into a separate Builder session.)
	return out
}

// dispatchExtraTools assembles the framework + custom-tool catalog every
// SUB-AGENT surface shares: the channel/dispatch path (RunAgentSync /
// RunAgentSyncContinuingRich) and the inline agents(action="run") path. It
// operates on an ALREADY-BUILT subTurn (receiver t) so each caller keeps its own
// subTurn wiring — dispatchChain, network, topic — that this shared assembly must
// not clobber. Returns the framework conversational tools (+ the agents grouped
// tool, attached pipelines, and direct custom tools) plus the custom-tool prompt
// section the caller appends to the system prompt. The caller MUST also wire
// ToolFallbackResolver = t.lazyToolFallback and DynamicTools =
// t.dynamicNewTempTools(sess) on the agent loop, and supplies poolUser/poolDB =
// the AGENT OWNER (so a synthetic channel runtime user still draws the owner's
// custom-tool pool). channel-chat tools and parent-tool inheritance stay in the
// callers — their gating differs per surface.
func (t *chatTurn) dispatchExtraTools(sess *ToolSession, poolUser string, poolDB Database) (extraTools []AgentToolDef, customToolPrompt string) {
	extraTools = append(extraTools, t.frameworkConversationalTools(sess)...)
	// agents grouped tool — sub-agents (OwnedBy set) are LEAVES (no dispatch
	// surface → no depth cascades); top-level targets get the full surface;
	// Builder targets stay read-only on dispatch.
	if t.agent.OwnedBy == "" {
		extraTools = append(extraTools, t.agentsGroupedToolDef(!isBuilderAgent(t.agent.ID)))
	}
	extraTools = append(extraTools, t.buildAttachedPipelineToolDefs()...)
	// Custom (temp) tools — hydrate the session from the owner's pool + the
	// agent-scoped kit, then split direct/lazy. Without the hydrate the session
	// is empty (the ts3_client_status-over-a-channel bug).
	t.privateMode = t.agent.ForcePrivate
	t.loadAgentTempTools(sess, poolUser, poolDB)
	direct, ctp := t.setupCustomTools(sess)
	extraTools = append(extraTools, direct...)
	return extraTools, ctp
}

// setupCustomTools resolves the agent's custom (temp) tools the SAME way on the
// web (runPlan) and channel/dispatch surfaces — the third catalog beside the
// framework set. Zero-arg customs (and the agent's own deliberately-attached
// kit) come back as DIRECTLY callable tools; has-args customs are presented as a
// name+desc prompt section (returned) and loaded on demand via load_tool, with
// the turn's lazy-tool maps populated so load_tool + lazyToolFallback resolve
// them. Callers must: append the direct tools to the catalog, append the prompt
// section to the system prompt, and wire ToolFallbackResolver = lazyToolFallback
// + DynamicTools = dynamicNewTempTools(sess) on the agent loop. Without this on
// the dispatch path, a channel agent simply never sees its own custom tools
// (e.g. ts3_client_status works in the web chat but is absent over a channel).
func (t *chatTurn) setupCustomTools(sess *ToolSession) (direct []AgentToolDef, lazyPromptSection string) {
	allCustomTools := temptool.BuildAgentToolDefs(sess)
	// Credential scope enforcement: drop any tool whose backing credential this
	// agent may not use. credentialDenySet resolves the effective deny set — tier 1
	// (a credential's AllowedUsers vs the session user) ∪ tier 2 (the agent's own
	// DisabledCredentials opt-outs). The backing TempTool carries .Credential;
	// api/toolbox tools have one, shell tools don't (empty → never denied).
	if deny := credentialDenySet(t.agent, sess.Username); len(deny) > 0 {
		kept := allCustomTools[:0]
		var dropped []string
		for _, td := range allCustomTools {
			if lt := sess.LookupTempTool(td.Tool.Name); lt != nil && deny[lt.Credential] {
				dropped = append(dropped, td.Tool.Name)
				continue
			}
			kept = append(kept, td)
		}
		allCustomTools = kept
		if len(dropped) > 0 {
			Log("[orchestrate.scope] agent=%s credential-denied, dropped %d tool(s): %v", t.agent.ID, len(dropped), dropped)
		}
	}
	t.wrapToolsForActivity(sess, allCustomTools)
	t.staticTempToolNames = map[string]bool{}
	t.lazyCustomToolNames = map[string]bool{}
	t.loadedCustomTools = map[string]bool{}
	t.lazyCustomToolDefs = map[string]AgentToolDef{}
	agentKit := t.agentOwnTools // nil on the dispatch path → has-args customs go lazy
	var lazyCustomTools []AgentToolDef
	var trialDemoted int
	for _, td := range allCustomTools {
		// The agent-kit bypass exists so an agent's CURATED kit is callable
		// without a load_tool round-trip — worth the schema's prompt cost
		// because someone chose to put it there. A Trial tool is the opposite:
		// it landed on this agent because an authoring turn had to put it
		// somewhere, and nobody has vouched for it yet. Letting those through
		// the bypass means an authoring agent's prompt grows by a full JSON
		// schema for every tool it has ever drafted — the whole reason the
		// lazy split exists. Trial tools take the lazy path until confirmed.
		kit := agentKit[td.Tool.Name]
		if kit && isTrialTool(sess, td.Tool.Name) {
			kit = false
			trialDemoted++
		}
		if len(td.Tool.Parameters) == 0 || kit {
			direct = append(direct, td)
			t.staticTempToolNames[td.Tool.Name] = true
		} else {
			lazyCustomTools = append(lazyCustomTools, td)
			t.lazyCustomToolNames[td.Tool.Name] = true
			t.lazyCustomToolDefs[td.Tool.Name] = td
		}
	}
	if trialDemoted > 0 {
		// Not silent: a tool moving out of the always-loaded catalog changes
		// how the LLM must reach it (load_tool first), so leave a trail.
		Log("[orchestrate.tools] agent=%s: %d unconfirmed (trial) tool(s) kept out of the inline catalog — reachable via load_tool; Confirm them in My tools to pin their schemas", t.agent.ID, trialDemoted)
	}
	if len(lazyCustomTools) > 0 {
		var b strings.Builder
		b.WriteString("\n\n## Your custom tools (load before use)\n")
		b.WriteString("These tools exist but their full definitions aren't loaded. To use them, call `load_tool(names=[\"<name>\", ...])` first — pass ALL the ones you'll need in that one call; it returns their parameters and makes them callable. Then call them normally.\n\n")
		for _, td := range lazyCustomTools {
			desc := strings.TrimSpace(td.Tool.Description)
			if len(desc) > 200 {
				desc = desc[:200] + "…"
			}
			b.WriteString("- `" + td.Tool.Name + "` — " + desc + "\n")
		}
		lazyPromptSection = b.String()
	}
	return direct, lazyPromptSection
}

// isTrialTool reports whether the session's live record for name is an
// unconfirmed (Trial) tool — authored on some turn and attached to an agent
// because it needed a durable home, not because anyone chose to keep it.
// Absent record → false: the caller's fallback is the existing behavior.
func isTrialTool(sess *ToolSession, name string) bool {
	if sess == nil {
		return false
	}
	if lt := sess.LookupTempTool(name); lt != nil {
		return lt.Trial
	}
	return false
}

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
func (t *chatTurn) runPlan(msgs []ChatMessage) (steps []PlanStep, question, directReply string, err error) {
	// Telemetry — populated by onStepHandler (set up below), summarized
	// at function exit via deferred Log call. Captures rounds used,
	// tool call breakdown, dup-args fingerprinting, and exit reason so
	// budget-tuning and drift-pattern questions have data to look at.
	telem := newTurnTelemetry()
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
			telem.rounds, softCap,
			directReply != "",
			question != "",
			len(steps) > 0,
			t.ctx.Err() != nil,
		)
		label := "orchestrate.orch"
		Log("%s", telem.summary(label, softCap, hardCap, exitReason)+" agent="+t.agent.ID)
		if line := telem.toolCallSummary(label); line != "" {
			Log("%s", line)
		}
	}()

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
	// Incognito (clean-room) session: inherit NOTHING — no memory facts and no
	// cortex standing context. A one-off with no baggage. Connected sessions
	// (the default) get both.
	incognito := t.session != nil && t.session.Incognito
	facts := t.facts()
	if incognito {
		facts = nil
	}
	notes := t.operatingNotes()
	if incognito {
		notes = OperatingNotes{}
	}
	sys := prependAgentContext(persona, t.agent, facts, notes)
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
				sys += cortexContextBlock(odb, t.agent.ID)
			}
		case t.session.ID != cortexSessionID(t.agent.ID):
			sys += cortexContextBlock(t.udb, t.agent.ID)
		}
	}
	// Credential-first authoring guidance — capability-tied. Every non-Builder
	// agent is granted tool_def + the credential-draft tools later in this
	// function (same condition), so it gets the matching guidance here. Builder
	// carries the equivalent in its own persona, so it's excluded to avoid
	// doubling.
	if !isBuilderAgent(t.agent.ID) {
		sys += "\n\n" + credentialFirstGuidance
	}
	// Any skill the LLM activated mid-turn via activate_skill is
	// re-injected here as well — the orchestrator round sees the
	// skill via the tool result in conversation history naturally,
	// but a fresh prompt build (e.g. round-2 re-entry after a
	// catalog change) wouldn't carry that history forward without
	// this anchor.
	// Triggered skills: inject the instructions of any allowed skill whose
	// triggers match this turn (deterministic — framework decides by
	// trigger match, not the LLM). No-op when none match.
	triggerMsg := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			triggerMsg = msgs[i].Content
			break
		}
	}
	// Per-turn, MESSAGE-DEPENDENT content (triggered-skill instructions +
	// skill/agent trigger hints) is collected into turnContext and appended
	// to the LAST user message below — deliberately NOT spliced into the
	// system prompt. Message-varying text in the sys prefix changes the
	// prefix every turn, which forces a full KV-cache re-prefill on the
	// recurrent/hybrid worker model (the orchestrate turn-latency bug: every
	// turn re-prefilled ~16k instead of reusing the cached prefix). Keeping
	// sys byte-stable lets turns 2+ reuse the prefix; the hints also belong
	// next to the user message (highest salience) per their own design intent.
	turnContext := t.renderTriggeredSkills()             // full instructions for skills already consulted
	turnContext += t.renderSkillTriggerHints(triggerMsg) // soft nudge for skills whose triggers matched
	// ("Available skills" block moved into appendAgentCapabilityBlocks below, so
	// the channel/dispatch path renders the same set — do NOT re-append here or
	// it doubles.)
	// "Available agents" block — lists the user's OTHER agents so
	// the host LLM knows what specialists exist and can dispatch to
	// them via agents(action="run", agent=..., message=...). Without
	// this block the LLM has to call agents(action="list") to
	// discover them, which it almost never does speculatively.
	sys += t.renderAvailableAgentsBlock()
	// Per-turn dispatch nudge: when this turn matches an agent's triggers,
	// hint "dispatch to it FIRST" right after the catalog — the salient,
	// turn-specific signal the static block alone doesn't provide. Soft;
	// the model still decides. No-op when no agent's triggers match.
	turnContext += t.renderAgentTriggerHints(triggerMsg) // see turnContext note above — appended to user msg, not sys
	// Recall hints: a cheap scored-pointer nudge toward the agent's own
	// knowledge corpus (opt-in per agent), so it pulls relevant material with
	// knowledge_search instead of missing it. Pointers only, next to the message
	// — no chunk bodies injected. No-op when off or nothing scores high enough.
	turnContext += t.renderRecallHints(triggerMsg)
	// Active dispatch threads: remind the host of agents it already
	// delegated to THIS session so a follow-up re-dispatches instead of
	// being answered inline (the host's own history hides the delegation,
	// and a generic follow-up won't re-match the agent's triggers). Near
	// the user message for salience, like the trigger hint. No-op when no
	// dispatch thread is open this session.
	turnContext += t.renderActiveDispatchThreads()
	// "Available sources" block — the catalog for query_source: lists each
	// admin-exposed source hook (name — what it covers / when to use), so
	// the model picks a source and calls query_source(source, query). One
	// dispatcher + this menu instead of N per-hook tools (the agents
	// pattern). No-op when no hooks are exposed.
	if t.app != nil {
		sys += RenderAvailableSourcesBlock(t.app.DB)
	}
	// Builder-only: list the user's existing persistent custom tools
	// as READ-ONLY awareness. They're hidden from Builder's executable
	// catalog (see newToolSession's isBuilderAgent skip) — this block
	// is how Builder still knows what exists. No-op for every other
	// agent.
	sys += t.renderBuilderExistingToolsBlock()
	// "Known topics" block — lists the snake_case slugs this
	// (user, agent) has already used so the LLM reuses them when
	// calling memory_save / memory_search instead of minting near-
	// duplicates ("ssh_keys" vs "sshkey" vs "ssh-keys"). Replaces
	// the per-turn topic auto-classifier — now the LLM picks.
	sys += t.renderKnownTopicsBlock()
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
	sys = appendAgentCapabilityBlocks(sys, t.agent, t.udb, t.user, true)
	// (Authoring-in-progress banner removed. It referenced legacy
	// tool names (create_pipeline_tool / create_temp_tool /
	// create_api_tool) replaced by tool_def + add_tool, and the
	// underlying focus slot is meaningful only inside Builder's
	// dispatched sub-sessions where the persona's Phase 4 plan_set
	// rhythm already manages the focus implicitly. Non-Builder
	// agents can't author anyway. Dead state — kept the
	// AuthoringAgentID DB field for now in case a future surface
	// wants it; just don't surface it as prompt content.)
	maxSteps := resolveMaxPlanSteps(t.agent)
	stepBudget := fmt.Sprintf("up to %d step%s", maxSteps, plural(maxSteps))

	// Control tools — terminal. Each handler captures its args
	// onto local closure state and cancels the orchestrator's
	// child context to stop the agent loop after the current
	// round. The captured state is dispatched after the loop
	// returns.
	var (
		capturedSteps     []PlanStep
		capturedQuest     string
		capturedOptions   []string
		capturedMulti     bool
		capturedFormSteps []map[string]any
		capturedReply     string
	)
	// plan_set fixation guard: a Qwen failure mode is re-submitting a
	// rejected (single-step / vacuous) plan_set round after round,
	// ignoring the "use respond_directly" feedback. Count rejections;
	// once we hit planSetDropThreshold, RoundToolFilter drops plan_set
	// from the catalog for the rest of the turn so the model is FORCED
	// off it. forceNoThinkAfterReject makes the round right after a
	// rejection skip thinking — the model was burning 24k-token budgets
	// deliberating itself back into the same plan_set. Single-threaded
	// per turn (handler + round hooks run in the loop goroutine), so no
	// locking needed.
	var planSetRejects int
	var forceNoThinkAfterReject bool
	const planSetDropThreshold = 2
	// compactRequested: set by the compact_context tool when the LLM wants
	// to proactively shed verbose tool-result bodies it's done with (e.g.
	// a smoke-test report). Consumed by the RoundCompactNow hook, which
	// forces an aggressive history compaction on the next round.
	var compactRequested bool
	orchCtx, cancelOrch := context.WithCancel(t.ctx)
	defer cancelOrch()

	planTool := AgentToolDef{
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
		Handler: func(args map[string]any) (string, error) {
			steps := parsePlanSteps(args["steps"], maxSteps)
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
				planSetRejects++
				forceNoThinkAfterReject = true
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
				planSetRejects++
				forceNoThinkAfterReject = true
				return "", fmt.Errorf("plan_set rejected: %s. For a turn that needs no tool work, just write the answer as your reply text — there is no reply tool. plan_set is for genuinely multi-step research / decomposition", vacuous)
			}
			capturedSteps = steps
			cancelOrch()
			return fmt.Sprintf("Plan committed (%d step%s); the worker pipeline will now execute it.", len(capturedSteps), plural(len(capturedSteps))), nil
		},
	}
	askTool := AgentToolDef{
		Tool: Tool{
			Name:        "ask_user",
			Description: "Pause and ask the user a clarifying question. Use whenever GUESSING is the alternative — not when SEARCHING is. Call ask_user when: (a) a tool returned 2+ plausible matches and you'd be picking arbitrarily (\"there are 3 users named Sam — which one?\"), (b) the user must choose between meaningfully different approaches (\"PDF or HTML?\"), (c) they must supply personal info (which appliance, which file, their preference), or (d) the request is genuinely ambiguous beyond what a search could resolve. DON'T ask for things you could just look up — call the right tool instead. **DEFAULT TO `options`**: whenever the answer space is bounded — yes/no confirmations, picking a format / mode / category, choosing among matches you already found — pass the choices as `options=[\"…\",\"…\"]` so the user clicks one button instead of typing. Free-text fields are friction; click-to-choose is one tap. Only OMIT `options` when the user must type something open-ended you couldn't enumerate (a name, a description, content). If you find yourself writing \"(yes / no / maybe)\" or similar choices into the question TEXT, you should be passing them as `options` instead — text-only renders as a plain field with no buttons. For multi-step builds (Builder agent), pass `plan` to paint a visible checklist card above the question — each item flips to ✓ as later turns mark steps done.",
			Parameters: map[string]ToolParam{
				"question": {
					Type:        "string",
					Description: "Singular. The question to ask the user, in plain text. Field name is 'question' (NOT 'questions'). If you need to ask multiple things, you can include several questions in this one string, but multi-step clarifications work better via ask_user_form.",
				},
				"options": {
					Type:        "array",
					Description: "**STRONGLY PREFERRED — pass options whenever the answer has any natural choices.** Click-to-choose is one tap; a free-text field is friction. MUST be an array of STRINGS, each being one option label. Concrete examples: options=[\"yes\", \"edit\", \"no\"], options=[\"PDF\", \"HTML\", \"Markdown\"], options=[\"Sam Patel\", \"Sam Reyes\", \"Sam Chen\"]. NOT a count, NOT a number, NOT a JSON-encoded string. When provided, the UI renders radios (or checkboxes when multi=true) plus a free-text fallback. PUT THE CHOICES HERE, not in the question text — writing \"(yes / no / edit)\" into the question text gives a plain field with no buttons. Keep labels short (1-4 words each), 8 max. Only OMIT options when the user must type something genuinely open-ended you couldn't enumerate.",
					Items:       &ToolParam{Type: "string"},
				},
				"multi": {
					Type:        "boolean",
					Description: "When true (and options is non-empty), the user can select multiple options. When false, single-select (radios). Default false. Ignored when options is empty.",
				},
				"plan": {
					Type:        "array",
					Description: "Optional ordered list of build-plan steps rendered as a visible checklist above the question. MUST be an array of OBJECTS, each with a 'title' (one-line summary) and optional 'detail' (tool + args summary). NOT a count, NOT a list of numbers, NOT a string. Concrete example: plan=[{\"title\": \"Create agent shell\", \"detail\": \"create_agent\"}, {\"title\": \"Add search tool\", \"detail\": \"add_tool(mode=api)\"}, {\"title\": \"Verify by dispatch\", \"detail\": \"test call\"}, {\"title\": \"Report completion\"}]. When this is set, the framework paints an orchestrate_plan card; subsequent authoring-tool calls auto-update individual rows to ✓.",
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
		Handler: func(args map[string]any) (string, error) {
			capturedQuest = strings.TrimSpace(stringArg(args, "question"))
			// Defensive: smaller LLMs occasionally typo "questions"
			// (plural) for "question". Accept either so the call
			// doesn't lose its primary content.
			if capturedQuest == "" {
				capturedQuest = strings.TrimSpace(stringArg(args, "questions"))
			}
			capturedOptions = stringSliceFromArgs(args, "options")
			if v, ok := args["multi"].(bool); ok {
				capturedMulti = v
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
			cancelOrch()
			return "Question relayed to the user; the framework will wait for their reply.", nil
		},
	}
	formTool := AgentToolDef{
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
		Handler: func(args map[string]any) (string, error) {
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
						steps = append(steps, normalizeFormStep(m))
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
					steps = append(steps, normalizeFormStep(m))
				}
			default:
				Debug("[orchestrate.ask_form] steps arg in unusable shape %T: %.300v", raw, raw)
				t.turnDiag("form-steps-unusable", fmt.Sprintf("The agent's form arrived in an unusable shape (%T) and rendered without questions.", raw))
			}
			capturedFormSteps = steps
			cancelOrch()
			return fmt.Sprintf("Form relayed to the user (%d step%s); the framework will wait for their reply.", len(steps), plural(len(steps))), nil
		},
	}

	// Worker tools — same surface the worker step gets. Lets the
	// orchestrator handle single-tool turns inline (call web_search,
	// see result, reply) without spinning up plan_set + a worker
	// round + synthesis for what should be one round.
	Debug("[orchestrate.orch] runPlan: building tool session")
	sess := t.newToolSession()
	// Persist any managed-workspace switch this inline turn performs so
	// the next step/turn's session lands in the same workspace.
	defer t.captureActiveWorkspace(sess)
	Debug("[orchestrate.orch] runPlan: resolving worker tools")
	// forOrchestrator=true — this is Builder's own chat-orchestrator
	// round, NOT a worker step. The Builder branch in resolveWorkerTools
	// returns the LEAN authoring catalog so Builder must decompose via
	// plan_set instead of reaching for create_agent / add_tool / tool_def
	// inline.
	workerTools, workerNames, err := t.resolveWorkerTools(sess, true)
	if err != nil {
		Debug("[orchestrate.orch] runPlan: resolveWorkerTools error: %v", err)
		return nil, "", "", fmt.Errorf("resolve tools: %w", err)
	}
	Debug("[orchestrate.orch] runPlan: resolved %d worker tools", len(workerTools))
	// No per-turn classifier-trim. Every allowed tool on the agent
	// is shipped to the LLM verbatim — the model's own attention
	// disambiguates better than a cosine-similarity classifier ever
	// did, and a stateless trim broke follow-ups like "get me another"
	// (turn 1 used get_meme; turn 2's bare text scored a different tool
	// higher and the LLM never saw get_meme). find_tools is still
	// registered as the escape hatch for any future massive-catalog
	// case (a 200-tool MCP plug-in); today's agents fit fine
	// without trimming.
	workerTools, workerNames = filterToolAuthoringWithoutFocus(workerTools, workerNames, t.session)
	t.gateAgentCRUDTools(workerTools)
	t.wrapToolsForActivity(sess, workerTools)

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
	controlTools := []AgentToolDef{planTool, askTool, formTool}
	t.wrapToolsForActivity(nil, controlTools)
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
	var knowTools []AgentToolDef
	knowTools = append(knowTools, t.frameworkConversationalTools(sess)...)
	// Host-app tools (e.g. a workbench's co-author "add_section") — supplied by
	// the app that dispatched this turn, callable directly by the orchestrator.
	if len(t.appTools) > 0 {
		knowTools = append(knowTools, t.appTools...)
	}
	// compact_context — LLM-driven context management. Lets the model
	// proactively discard the bodies of EARLIER tool results it has
	// consumed and no longer needs (a smoke-test report, a big fetch, a
	// verbose listing), instead of carrying them until the automatic
	// budget compaction kicks in. Framework meta-tool: always available,
	// not classifier-trimmed (it's in knowTools).
	knowTools = append(knowTools, AgentToolDef{
		Tool: Tool{
			Name:        "compact_context",
			Description: "Free up context: discard the bodies of EARLIER tool results you've already read and no longer need — e.g. after judging a long smoke-test report, a big page fetch, or a verbose listing. Their bodies are replaced with a short marker (re-run the tool if you need the data again); the most recent result and the whole conversation stay intact. Call this at a natural breakpoint when you're carrying long tool outputs you're done with, to keep a long session from bloating its context. No arguments.",
		},
		Handler: func(args map[string]any) (string, error) {
			compactRequested = true
			return "Acknowledged — earlier verbose tool-result bodies you've consumed will be released on your next step. Continue with your next action.", nil
		},
	})
	// show_html — the viewer/previewer pane (preview_tool.go). Framework
	// display tool, always available like compact_context: emits a generic
	// html_artifact block the client renders in a slide-in side pane
	// (authored HTML sandboxed, or a same-origin page preview by url), and
	// upserts the artifact onto the session (UIBlocks) so it survives
	// reload. No Caps — display-only, so it also survives private mode.
	knowTools = append(knowTools, t.showHTMLToolDef())
	// show_link — the navigation counterpart (preview_tool.go): a clickable
	// link card in the transcript for "go here" moments — the app Builder
	// just created, a settings page, an external console. Display-only
	// like show_html, so it's ungated and always available.
	knowTools = append(knowTools, t.showLinkToolDef())
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
	if isBuilderAgent(t.agent.ID) {
		knowTools = append(knowTools,
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
			// app_def (create / update / list / get / delete) — author
			// data-driven gohort APPS (real in-dashboard surfaces served
			// by customapps at /custom/<slug>/). Builder-only, same
			// rationale as pipeline: composing an app is authoring work.
			// This is what lets Builder answer "build me an app" with an
			// actual gohort app instead of a standalone HTML file.
			t.appDefToolDef(),
		)
	}
	knowTools = append(knowTools,
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
		knowTools = append(knowTools, t.enterExplorerModeToolDef())
	}
	// Recurring per-session interval tasks — but NOT for Fleet agents. A Fleet
	// agent schedules recurring work through create_standing_agent (real cron
	// timing, and it surfaces in the Enabled-agents console where the user can
	// pause/cancel it); the generic per-session "recurring" scheduler bypasses
	// the fleet and stays invisible to the console, so we keep it off them.
	// (The earlier dropToolsByName in resolveWorkerTools was dead — recurring
	// is added HERE, after that assembly, so it was never in that list.)
	if !t.agent.Fleet {
		knowTools = append(knowTools, t.recurringToolDef())
	}
	// Session spin-off — web chat only (this assembly path is never used by
	// channel relays / dispatch / scheduled fires, and the handler's sse
	// guard backstops that): the agent can open a fresh titled session with
	// a seeded handoff note and offer the user a link to continue there.
	knowTools = append(knowTools, t.openSessionToolDef())
	// Tool authoring: any agent can author its OWN tools via tool_def (the way
	// phantom always could before it was centralized). Builder already has
	// tool_def via its authoring catalog, so don't double it. AGENT and
	// PIPELINE authoring still route to Builder; only tools are self-serve.
	if !isBuilderAgent(t.agent.ID) {
		knowTools = append(knowTools, ChatToolToAgentToolDefWithSession(temptool.BuildToolDef(), sess))
		// Credential-first authoring: an agent that can author API tools must be
		// able to create the credential it routes through (a DISABLED, secretless
		// draft the admin later completes), instead of soliciting secrets in
		// chat. Builder has these via its authoring catalog; every other
		// self-serve authoring agent gets them here. The matching
		// credentialFirstGuidance is injected into the system prompt above under
		// the same !isBuilderAgent condition.
		knowTools = append(knowTools, draftOAuthCredentialToolDef(t), draftAPICredentialToolDef(t), updateAPICredentialToolDef(t), storeCredentialSecretToolDef(), checkCredentialToolDef(t))
	}
	// create_pipeline_tool is NOT added to the catalog — add_tool with
	// mode="pipeline" covers the same use case via a unified surface.
	// Having both visible caused pattern-match loops (LLM oscillated
	// between the two). The handler stays in the codebase for
	// backward-compat with any future agent that explicitly opts in
	// via AllowedTools, but the closure-bound default registration is
	// removed.
	t.wrapToolsForActivity(sess, knowTools)
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
	directCustomTools, lazyCustomPrompt := t.setupCustomTools(sess)
	sys += lazyCustomPrompt
	allTools := append(controlTools, knowTools...)
	allTools = append(allTools, workerTools...)
	allTools = append(allTools, directCustomTools...)
	// Attached pipelines — one callable tool per pipeline bolted onto
	// this agent (AgentRecord.AttachedPipelines). Curated + tiny schema,
	// so direct (not lazy load_tool). Wrap for activity so a pipeline run
	// shows its tool_call / result in the convo + activity pane.
	if attachedPipes := t.buildAttachedPipelineToolDefs(); len(attachedPipes) > 0 {
		t.wrapToolsForActivity(sess, attachedPipes)
		allTools = append(allTools, attachedPipes...)
		Log("[orchestrate.tools] surfaced %d attached pipeline tool(s) for agent=%s", len(attachedPipes), t.agent.ID)
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
		filtered := allTools[:0]
		dropped := []string{}
		for _, td := range allTools {
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
		allTools = filtered
		if len(dropped) > 0 {
			Log("[orchestrate.orch] private mode dropped %d network-capable dynamic tool(s): %v", len(dropped), dropped)
		}
	}
	Debug("[orchestrate.orch] runPlan: rewriting catalog over %d tools", len(allTools))

	// (Runtime tool-group rewriting retired. The per-turn
	// classifier-trim that preceded this block is also gone — every
	// allowed tool ships to the LLM verbatim; the model's attention
	// disambiguates better than a cosine-similarity classifier did.
	// find_tools stays registered as the explicit-search fallback for
	// any future massive-catalog case. ToolGroup records still exist
	// as admin-side organizational metadata — they just don't gate
	// the runtime catalog anymore.)

	// Full-surface log every turn — confirms what the LLM actually
	// receives. If a follow-up turn lacks a tool the first turn had,
	// this line will show the regression. Includes both control and
	// worker tools so a bug that drops one but not the other is
	// visible immediately.
	allNames := make([]string, 0, len(allTools))
	for _, td := range allTools {
		allNames = append(allNames, td.Tool.Name)
	}
	sessID := ""
	if t.session != nil {
		sessID = t.session.ID
	}
	Log("[orchestrate.orch] session=%s msgs=%d tools_to_llm[%d]=%v (worker_subset=%v private=%v)",
		sessID, len(msgs), len(allTools), allNames, workerNames, t.privateMode)

	if len(workerTools) > 0 {
		sys += "\n\n" + buildToolUseDirective(workerTools)
	}
	// Append per-tool prompt fragments (opt-in via AgentToolDef.Prompt).
	// Lands between the framework directive and any subsequent
	// dynamic additions — close to the catalog so the model reads
	// per-tool usage notes alongside the tool list.
	if frag := RenderToolPromptFragments(allTools); frag != "" {
		sys += "\n\n" + frag
	}

	stopKeepalive := startKeepalive(t.sse)
	think := true
	if p := RouteThink(orchestratorRouteKey(t.agent.ID, t.shouldUseLeadModel())); p != nil {
		think = *p
	}
	// Per-agent override wins over the route default — the author may
	// have decided this agent always reasons (planners, synthesizers)
	// or never reasons (fast specialists). Empty Think means "auto":
	// keep the route default we just picked up.
	switch t.agent.Think {
	case "on":
		think = true
	case "off":
		think = false
	}

	// Per-round bubbles: each agent-loop round that streams text gets
	// its own assistant bubble, finalized at the round boundary by
	// the OnStep callback. Subsequent rounds create fresh bubbles —
	// the chat-app pattern where the user sees discrete narration
	// chunks rather than one accumulating wall of text.
	//
	// Tool-only rounds (no streamed content) don't materialize a
	// bubble. We track whether the most-recently-finalized round
	// actually had streamed content so the post-loop captured-text
	// dispatch can avoid emitting a duplicate when the LLM streamed
	// the same text it then passed to respond_directly/ask_user.
	var streamMsgID string
	var streamedBuf strings.Builder
	var lastFinalizedID string
	// lastFinalizedText is the text of the MOST RECENT finalized bubble.
	// emitCapturedAsBubble dedups against it: a respond_directly that just
	// repeats what the model streamed (stream answer → respond_directly
	// same text) is suppressed; anything else emits. Match is EXACT
	// (after trim), NOT substring-over-all-bubbles — the latter dropped a
	// short respond_directly reply whenever it happened to be a substring
	// of a larger earlier block. Dropping a reply is far worse than an
	// occasional double, so we err toward emitting: suppress only on an
	// exact repeat of the last shown bubble.
	var lastFinalizedText string
	streamHandler := func(chunk string) {
		if chunk == "" {
			return
		}
		// If a tool fired this round before any text streamed, it
		// already lazy-materialized a bubble via ensureBubbleForTool.
		// Adopt that bubble for the streamed text rather than
		// creating a second one.
		if streamMsgID == "" {
			if existing := t.getCurrentMsgID(); existing != "" {
				streamMsgID = existing
			} else {
				streamMsgID = fmt.Sprintf("orch-%d", time.Now().UnixNano())
				t.sse.Send(map[string]any{
					"kind": "message",
					"role": "assistant",
					"id":   streamMsgID,
					"text": "",
				})
				t.setCurrentMsgID(streamMsgID)
			}
		}
		streamedBuf.WriteString(chunk)
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   streamMsgID,
			"text": chunk,
		})
	}
	// Telemetry record fires at the top of the onStep callback; the
	// telem var is declared at function entry and summarized in the
	// deferred block above.
	onStepHandler := func(info StepInfo) {
		telem.record(info)
		// Tool-only round with no text and no lazy-bubble: nothing
		// to finalize, nothing visible. (Tool calls in that round
		// already created their own bubble via ensureBubbleForTool;
		// streamMsgID picks that up via getCurrentMsgID below.)
		id := streamMsgID
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
		raw := streamedBuf.String()
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
				streamedBuf.Reset()
				return
			}
			// Builder presents a PLAN (intentionally long) before it acts —
			// exempt it from the length clear so the user actually sees what it
			// intends to do. The clear exists for ORDINARY agents that mis-emit a
			// full answer before a tool (and then repeat it at the end); Builder's
			// pre-tool prose is the plan, not a doubled answer.
			if len(cleaned) > leadInMaxLen && !isBuilderAgent(t.agent.ID) {
				t.sse.Send(map[string]any{"kind": "chunk_replace", "id": id, "text": ""})
				streamedBuf.Reset()
				return
			}
			t.sse.Send(map[string]any{"kind": "message_done", "id": id})
			t.captureMidTurnBubble(cleaned)
			streamMsgID = ""
			t.setCurrentMsgID("")
			streamedBuf.Reset()
			return
		}
		// Final (tool-free) round — this round's text is the answer.
		// (Any text the model emitted in earlier tool rounds was already
		// settled as its own card above, so there's nothing to restore.)
		// Tool-only final round with no text: nothing to finalize — keep
		// the bubble open (subsequent tool pills fold in).
		if cleaned == "" {
			streamedBuf.Reset()
			return
		}
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		lastFinalizedID = id
		lastFinalizedText = cleaned
		// Persist the answer so a reloaded session replays the same
		// bubble the user saw live. handleSend drains the buffer right
		// before appending the final assistant message.
		t.captureMidTurnBubble(cleaned)
		streamMsgID = ""
		streamedBuf.Reset()
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
	settleRound := func() {
		id := streamMsgID
		if id == "" {
			id = t.getCurrentMsgID()
		}
		if id == "" {
			streamedBuf.Reset()
			return
		}
		raw := streamedBuf.String()
		cleaned := strings.TrimSpace(StripToolCallMarkup(raw))
		if cleaned == "" {
			streamedBuf.Reset()
			return
		}
		if cleaned != strings.TrimSpace(raw) {
			t.sse.Send(map[string]any{"kind": "chunk_replace", "id": id, "text": cleaned})
		}
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		lastFinalizedID = id
		lastFinalizedText = cleaned
		t.captureMidTurnBubble(cleaned)
		streamMsgID = ""
		streamedBuf.Reset()
		t.setCurrentMsgID("")
	}

	// Budget injection — surface the round counter to the LLM so it
	// can pace itself instead of calling tools until it hits the cap
	// blind. OnRoundStart fires AFTER history is appended but BEFORE
	// the LLM call, so each round sees the same brief note appended
	// to history.
	maxR := resolveMaxWorkerRounds(t.agent) // soft cap (the pace-against target)
	// Explorer wiring for the ORCHESTRATOR loop (mirrors runWorkerStep):
	// MaxRounds is the HARD cap, StopRound enforces the soft cap (maxR)
	// UNTIL the LLM flips explorerMode via enter_explorer_mode — then it
	// runs on to orchHardCap. Without this the orchestrator hard-stops at
	// maxR and enter_explorer_mode (which only flips a flag) is a no-op
	// inline, so a build that runs short can't self-extend. Non-explorer
	// agents keep orchHardCap == maxR, so StopRound is a plain cap.
	orchHardCap := maxR
	if t.agent.AllowExplorer {
		if ec := resolveExplorerHardCap(t.agent); ec > orchHardCap {
			orchHardCap = ec
		}
	}
	// absoluteCeiling is the loop's hard MaxRounds — set high enough that
	// StopRound (the dynamic governor: soft cap → explorer cap →
	// plan-scaled cap) always decides first. present_build_plan can lift
	// the budget above the explorer ceiling, so add the maximum plan grant
	// on top. Non-plan agents never reach it — StopRound stops earlier.
	absoluteCeiling := orchHardCap + buildPlanRoundsPerStep*resolveMaxPlanSteps(t.agent)
	var orchRoundsUsed int
	roundCounter := 0
	onRoundStartHandler := func() []Message {
		roundCounter++
		t.currentRound = roundCounter
		// Pace against the SOFT cap normally; once the LLM has flipped
		// explorer mode, pace against the hard cap (StopRound lets it run
		// there); once a build plan is presented, pace against the
		// plan-scaled cap. Explorer NUDGES only fire when NOT already
		// exploring.
		cap := maxR
		if t.explorerMode {
			cap = orchHardCap
		}
		if t.planBudgetCap > cap {
			cap = t.planBudgetCap
		}
		remaining := cap - roundCounter
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
						roundCounter, cap,
					),
				}}
			}
			return []Message{{
				Role: "user",
				Content: fmt.Sprintf(
					"[Round %d/%d — FINAL round. No more tool calls. Produce your final answer NOW from what you have so far.]",
					roundCounter, cap,
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
					roundCounter, cap, remaining, plural(remaining), orchHardCap,
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
				roundCounter, cap, remaining, plural(remaining),
			),
		}}
	}

	// Attach any image attachments to the most recent user message
	// so vision-capable LLMs see them. Images are ephemeral — only
	// passed for this turn; not persisted in session history (the
	// raw bytes would balloon the DB).
	llmMsgs := toLLMMessages(msgs)
	if len(t.userImages) > 0 {
		for i := len(llmMsgs) - 1; i >= 0; i-- {
			if llmMsgs[i].Role == "user" {
				llmMsgs[i].Images = t.userImages
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
	if tc := strings.TrimSpace(turnContext); tc != "" {
		for i := len(llmMsgs) - 1; i >= 0; i-- {
			if llmMsgs[i].Role == "user" {
				if strings.TrimSpace(llmMsgs[i].Content) == "" {
					llmMsgs[i].Content = tc
				} else {
					llmMsgs[i].Content += "\n\n" + tc
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
	allTools = dedupeToolDefsByName(allTools)

	orchStart := time.Now()
	Debug("[orchestrate.orch] entering RunAgentLoop (msgs=%d tools=%d sys_chars=%d)", len(llmMsgs), len(allTools), len(sys))
	resp, _, loopErr := t.app.RunAgentLoop(orchCtx, llmMsgs, AgentLoopConfig{
		SendGuardKey:         sendGuardKey,
		SystemPrompt:         sys,
		Tools:                allTools,
		StampLocation:        UserLocation(t.user), // stamp the turn in the interactive user's zone
		DynamicTools:         t.dynamicNewTempTools(sess),
		ToolFallbackResolver: t.lazyToolFallback,
		Stream:               streamHandler,
		OnStep:               onStepHandler,
		OnRoundStart:         onRoundStartHandler,
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
		SettleRound: settleRound,
		// Feed view_video's sampled frames to the model on the next round so it
		// actually sees the clip instead of describing it blind.
		DrainViewImages: sess.DrainViewImages,
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
			return !(name == "plan_set" && planSetRejects >= planSetDropThreshold)
		},
		// The round right after a plan_set rejection skips thinking — the
		// model was burning huge thinking budgets deliberating itself back
		// into plan_set. Consume the flag so it applies to exactly one round.
		RoundChatOptions: func() []ChatOption {
			if forceNoThinkAfterReject {
				forceNoThinkAfterReject = false
				return []ChatOption{WithThink(false)}
			}
			return nil
		},
		// Context window for history compaction — long multi-round
		// sessions (Builder's edit→re-fetch loop especially) were growing
		// past the window and triggering server-side context-shift. With
		// this set, the loop elides old tool-result bodies once history
		// nears the budget. LeadContextSize falls back to the worker
		// window; 0 (unconfigured) disables compaction.
		ContextSize: t.app.LeadContextSize(),
		// LLM-driven compaction: when the model calls compact_context (it
		// just consumed a long output it's done with), force an aggressive
		// shed on the next round.
		RoundCompactNow: func() bool {
			if compactRequested {
				compactRequested = false
				return true
			}
			return false
		},
		// Cap orchestrator iterations. MaxRounds is the HARD ceiling;
		// StopRound enforces the soft cap (maxR) until the LLM flips
		// explorer mode, then lets it run to orchHardCap. Most chat turns
		// need 1-3 rounds; deep research / large builds bump via the
		// agent's worker-rounds budget + enter_explorer_mode.
		MaxRounds:   absoluteCeiling,
		ThinkBudget: t.agent.ThinkBudget, // per-agent override; 0 = inherit route/global
		// StopRound is the dynamic governor (MaxRounds is just the safety
		// ceiling). Effective cap = soft cap, raised to the explorer cap
		// once explorer mode is on, raised again to the plan-scaled cap
		// once a build plan is presented. max() semantics — a later phase
		// never lowers the budget a prior one granted.
		StopRound: func() bool {
			orchRoundsUsed++
			cap := maxR
			if t.explorerMode {
				cap = orchHardCap
			}
			if t.planBudgetCap > cap {
				cap = t.planBudgetCap
			}
			return orchRoundsUsed > cap
		},
		// Escalation policy hook (confirm.go): calls through a
		// credential flagged "Require confirm" park on an in-chat
		// approval card; every other NeedsConfirm tool (delete_agent
		// etc.) auto-approves as before so nothing hangs on stdin.
		Confirm: t.confirmFuncFor(sess),
		// Control tools end the round immediately. If the LLM bundles
		// ask_user with create_agent in the same response, only ask_user
		// fires and the turn pauses for the user's actual answer.
		RoundAbortTools: []string{"ask_user", "ask_user_form", "respond_directly", "plan_set"},
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
			WithRouteKey(orchestratorRouteKey(t.agent.ID, t.shouldUseLeadModel())),
			WithThink(think),
		},
	})
	stopKeepalive()
	// Off-hot-path graph population: after a clean turn, best-effort extract the
	// entity relationships the user stated into the graph. Single-flight +
	// cooldown + own goroutine (never blocks the turn, self-throttles on the
	// shared GPU); gated off by default.
	if loopErr == nil {
		maybeExtractGraph(t.udb, factsNamespace(t.agent.ID), LatestUserContent(llmMsgs), t.app.WorkerChat)
	}
	{
		respLen := 0
		if resp != nil {
			respLen = len(strings.TrimSpace(resp.Content))
		}
		errStr := ""
		if loopErr != nil {
			errStr = loopErr.Error()
		}
		Debug("[orchestrate.orch] RunAgentLoop returned (elapsed=%s, resp.content=%dch, ctx.err=%v, orchCtx.err=%v, loopErr=%q, capturedSteps=%d, capturedReply=%dch, capturedQuest=%dch, capturedForm=%d)",
			time.Since(orchStart),
			respLen,
			t.ctx.Err(), orchCtx.Err(), errStr,
			len(capturedSteps),
			len(capturedReply),
			len(capturedQuest),
			len(capturedFormSteps),
		)
	}

	// Catch a final round whose OnStep didn't fire (rare — happens
	// when the loop terminates between content stream and the OnStep
	// dispatch). Finalize and treat it as the last finalized bubble.
	finalID := streamMsgID
	if finalID == "" {
		finalID = t.getCurrentMsgID()
	}
	if finalID != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": finalID})
		if streamedBuf.Len() > 0 {
			lastFinalizedText = strings.TrimSpace(StripToolCallMarkup(streamedBuf.String()))
		}
		lastFinalizedID = finalID
		// Persist the final round's narration too — same gap as the
		// per-round capture in onStepHandler, only on the rare path
		// where the loop terminates after streaming but before OnStep.
		t.captureMidTurnBubble(lastFinalizedText)
		streamMsgID = ""
		t.setCurrentMsgID("")
	}
	// Stats land on the last bubble we finalized; RunAgentLoop only
	// returns the last round's resp, so per-round stats aren't
	// available without backend changes.
	if lastFinalizedID != "" {
		t.emitStats(lastFinalizedID, resp, orchStart)
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
	emitCapturedAsBubble := func(text string) {
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
		if last := strings.TrimSpace(lastFinalizedText); last != "" && isNearDuplicate(trimmed, last) {
			Debug("[orchestrate.orch] captured reply (%d ch) is a near-duplicate of last shown bubble (%d ch) — suppressing", len(trimmed), len(last))
			return
		}
		Debug("[orchestrate.orch] emitting captured reply as bubble (%d ch, last bubble %d ch)", len(trimmed), len(strings.TrimSpace(lastFinalizedText)))
		id := fmt.Sprintf("orch-%d", time.Now().UnixNano())
		t.sse.Send(map[string]any{
			"kind": "message",
			"role": "assistant",
			"id":   id,
			"text": text,
		})
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.emitStats(id, resp, orchStart)
	}

	// Loop end conditions:
	//   1. A control tool fired → orchCtx is canceled (but t.ctx
	//      is still live). Captured state below drives dispatch.
	//   2. Loop hit MaxRounds → resp may carry content; treat as
	//      implicit respond_directly.
	//   3. Loop ended naturally (no tool call this round) → resp.Content
	//      is the implicit reply.
	//   4. Real error (network, LLM, etc.) → return it.
	if loopErr != nil && t.ctx.Err() == nil && orchCtx.Err() == nil {
		return nil, "", "", loopErr
	}

	if len(capturedFormSteps) > 0 {
		// Multi-step form — render as a single block with all the
		// steps; client walks the user through them one at a time
		// and submits a compiled answer at the end. Return a brief
		// placeholder as the captured question so the caller's
		// session-persistence logic has something to record.
		t.sse.Send(map[string]any{
			"kind":  "block",
			"type":  "orchestrate_ask_form",
			"id":    fmt.Sprintf("askform-%d", time.Now().UnixNano()),
			"steps": capturedFormSteps,
		})
		if t.session != nil {
			t.session.AwaitingUserConfirm = true
		}
		return nil, fmt.Sprintf("(form with %d question%s)", len(capturedFormSteps), plural(len(capturedFormSteps))), "", nil
	}
	if capturedQuest != "" {
		// ask_user ALWAYS renders as a form card — question + (optional
		// option group) + textarea + Submit. The renderer handles the
		// no-options case by just showing the textarea. Always-form
		// gives the user a consistent affordance to reply explicitly,
		// instead of having to click into the chat input on some
		// questions and an inline form on others.
		t.sse.Send(map[string]any{
			"kind":     "block",
			"type":     "orchestrate_ask",
			"id":       fmt.Sprintf("ask-%d", time.Now().UnixNano()),
			"question": capturedQuest,
			"options":  capturedOptions,
			"multi":    capturedMulti,
		})
		// Mark this session as awaiting user confirmation. Gated tools
		// (agent CRUD) will fire on the NEXT user turn after this
		// pause — without this flag, those tools refuse to run.
		if t.session != nil {
			t.session.AwaitingUserConfirm = true
		}
		return nil, capturedQuest, "", nil
	}
	if len(capturedSteps) > 0 {
		// Plan_set's pre-amble narration already streamed; the plan
		// card will render below. Nothing further to emit here.
		return capturedSteps, "", "", nil
	}
	if capturedReply != "" {
		emitCapturedAsBubble(capturedReply)
		return nil, "", capturedReply, nil
	}
	if resp != nil && strings.TrimSpace(resp.Content) != "" {
		// Implicit respond_directly path — the LLM streamed text in
		// its final round but didn't call any control tool. The
		// per-round finalizer already finalized that bubble; we
		// just need a clean copy for the persisted history.
		clean := strings.TrimSpace(StripToolCallMarkup(resp.Content))
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
		emitCapturedAsBubble(clean)
		return nil, "", clean, nil
	}
	// No content anywhere. If the loop ran out of its round budget (rather
	// than ending naturally or being deliberately silenced via stay_silent,
	// which returns from inside the loop without HitRoundCap), don't leave
	// the user staring at a blank turn — say so explicitly so they can narrow
	// the ask or retry instead of assuming the agent is broken. This is the
	// "ran out of turns, got nothing back" failure (common for retrieval-heavy
	// agents that exhaust rounds mid-investigation).
	if resp != nil && resp.HitRoundCap {
		t.turnDiag("round-cap", "This turn ran out of worker rounds before finishing — raise the round limit or narrow the ask.")
		msg := "I ran out of working rounds for this turn before I could finish, and didn't have a partial answer to show. Try narrowing the question, or ask me to continue and I'll pick up from here."
		// Same as the resp.Content path above: gate on the near-duplicate check,
		// not a coarse "already rendered something" boolean, so this shows even
		// after a mid-turn narration bubble.
		emitCapturedAsBubble(msg)
		return nil, "", msg, nil
	}
	return nil, "", "", nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// runWorkerStep dispatches one plan step to the worker (no-think) LLM.
// Prior step outputs are included as context so later steps can build
// on earlier ones. The worker's tool surface is the default pool (every
// non-blocked chat tool with Read or Network capability) intersected
// with agent.AllowedTools when the agent set an explicit allowlist;
// empty AllowedTools means "default pool".
//
// Each tool call the worker makes surfaces as an activity row in the
// AgentLoopPanel pane so the user can see what the agent is doing
// live, without waiting for the step to finish.
func (t *chatTurn) runWorkerStep(prior []PlanStep, cur PlanStep, userMsg string, notes []injectionNote) (stepOut string, stepErr error) {
	// Per-step telemetry — same instrumentation as the orchestrator's
	// runPlan. Each worker step gets its own round budget and its own
	// telem; the summary log fires when the step exits.
	telem := newTurnTelemetry()
	defer func() {
		softCap := resolveMaxWorkerRounds(t.agent)
		hardCap := softCap
		if t.agent.AllowExplorer {
			if ec := resolveExplorerHardCap(t.agent); ec > hardCap {
				hardCap = ec
			}
		}
		exitReason := classifyWorkerExit(stepErr, telem.rounds, softCap, len(stepOut))
		label := fmt.Sprintf("orchestrate.worker step=%d", cur.ID)
		Log("%s", telem.summary(label, softCap, hardCap, exitReason)+" agent="+t.agent.ID)
		if line := telem.toolCallSummary(label); line != "" {
			Log("%s", line)
		}
	}()

	// Reset the active-bubble id at each worker-step boundary so the
	// first tool call of THIS step materializes its own card under
	// the just-emitted intent block, instead of attaching to the
	// previous step's bubble. (currentMsgID lingers from the
	// orchestrator's last round / prior step otherwise.)
	t.setCurrentMsgID("")

	// Build the worker's per-call session — same helper the
	// orchestrator uses, so persistent temp tools land on both paths.
	sess := t.newToolSession()
	// Persist any managed-workspace switch this step performs so the
	// next step's fresh session shares the same workspace (a script
	// written here is then runnable from the following step).
	defer t.captureActiveWorkspace(sess)

	// forOrchestrator=false — this is the per-step worker running one
	// plan_set step. The Builder branch returns builderWorkerResearchTools
	// (raw call_<credential> for endpoint probing + the full authoring
	// catalog: tool_def / create_agent / add_tool / skill_def) on top of
	// the standard AllowedTools pool. Workers can either author directly
	// (call tool_def themselves and return confirmation) or return
	// components for Builder to assemble. The framework doesn't dictate;
	// Builder's brief decides which shape the worker takes.
	tools, toolNames, err := t.resolveWorkerTools(sess, false)
	if err != nil {
		return "", fmt.Errorf("resolve tools: %w", err)
	}
	// Orchestrator-curated per-step tool surface — when the plan
	// step's Tools field is set, intersect the agent's resolved pool
	// down to JUST those names. The worker sees exactly what the
	// orchestrator picked, no surplus, no classifier-trim noise.
	// Empty Tools falls back to the full resolved pool (the
	// orchestrator chose not to specify; degraded behavior).
	// Intersection (not raw substitution) keeps the agent's allowlist
	// + cap filters honored — the orchestrator can't grant a tool
	// the agent doesn't have.
	if len(cur.Tools) > 0 {
		want := make(map[string]bool, len(cur.Tools))
		for _, n := range cur.Tools {
			want[strings.TrimSpace(n)] = true
		}
		keptTools := tools[:0]
		keptNames := toolNames[:0]
		for i, t := range tools {
			if want[t.Tool.Name] {
				keptTools = append(keptTools, t)
				if i < len(toolNames) {
					keptNames = append(keptNames, toolNames[i])
				}
			}
		}
		Log("[orchestrate.worker] step %d (%q): orchestrator picked %d tools → %d resolved",
			cur.ID, cur.Title, len(cur.Tools), len(keptTools))
		tools = keptTools
		toolNames = keptNames
	}
	tools, toolNames = filterToolAuthoringWithoutFocus(tools, toolNames, t.session)
	t.gateAgentCRUDTools(tools)
	// Three layers, each gated by its own helper — same split as
	// the orchestrator catalog in runPlan. Workers are the ones
	// actually researching, so they get write access to the Inferred
	// Memory layer when it's enabled. Knowledge is always available.
	if !unifiedMemoryEnabled() {
		tools = append(tools, t.searchKnowledgeToolDef(), t.fetchKnowledgeDocToolDef())
		toolNames = append(toolNames, "knowledge_search", "fetch_knowledge_doc")
	}
	for _, td := range t.skillToolDefs() {
		tools = append(tools, td)
		toolNames = append(toolNames, td.Tool.Name)
	}
	// (dispatch_to_worker temporarily unmounted on the worker step
	// too — same reason as the orchestrator catalog: discoverability
	// problem, not a wiring problem.)
	if unifiedMemoryEnabled() {
		// Collapsed surface (see frameworkConversationalTools). recall fronts
		// knowledge search, so the legacy knowledge tools above are skipped too.
		if t.hasAnyMemoryLayer() {
			for _, td := range t.unifiedMemoryTools() {
				tools = append(tools, td)
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
		if !t.explicitOff() {
			tools = append(tools, t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	} else {
		if !t.inferredOff() {
			tools = append(tools, t.memoryToolDef())
			toolNames = append(toolNames, "memory")
		}
		if !t.explicitOff() {
			tools = append(tools, t.storeFactToolDef(), t.forgetFactToolDef(), t.searchFactsToolDef(),
				t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	}
	// Working notes (rewritable running-state block) — its own opt-in layer,
	// independent of the Explicit/Reference memory toggles.
	if t.agent.EnableNotes {
		tools = append(tools, t.updateNotesToolDef())
		toolNames = append(toolNames, "update_notes")
	}
	// create_pipeline_tool intentionally NOT added — add_tool with
	// mode="pipeline" is the single pipeline-authoring surface.
	// See the matching note in runPlan above.
	// Worker step catalog — always include agents(run). Builder's
	// orchestrator-round restriction (no run) is structural for the
	// chat-facing conductor turn, where Builder→Chat→Builder cycles
	// can form. Worker steps spawned via plan_set are different:
	// they smoke-test newly-created agents ("Step N (verify): worker
	// dispatches to the new agent with a representative input"), and
	// stripping run here breaks that pattern.
	tools = append(tools, t.agentsGroupedToolDef(true))
	if !t.agent.Fleet {
		// Fleet agents schedule through create_standing_agent, not the generic
		// per-session recurring scheduler — see runPlan's note.
		tools = append(tools, t.recurringToolDef())
	}
	tools = append(tools, t.openSessionToolDef())
	if t.agent.AllowExplorer {
		tools = append(tools, t.enterExplorerModeToolDef())
	}
	// Include persistent temp tools in the static set so the group
	// rewriter (called below) can collapse them when they're members
	// of an admin-curated group. Without this, temp tools come in
	// via DynamicTools after the rewriter has already run and stay
	// visible at the top level even when grouped — the LLM then sees
	// both the toolbox AND the individual temp tools and picks
	// non-deterministically. Matches the runPlan fix.
	staticTempTools := temptool.BuildAgentToolDefs(sess)
	tools = append(tools, staticTempTools...)
	if t.staticTempToolNames == nil {
		t.staticTempToolNames = map[string]bool{}
	}
	for _, td := range staticTempTools {
		toolNames = append(toolNames, td.Tool.Name)
		t.staticTempToolNames[td.Tool.Name] = true
	}
	// Private-mode backstop for worker-step catalog — same pattern as
	// runPlan. Drops any AgentToolDef that declares CapNetwork in its
	// Tool.Caps (the `agents` grouped tool, any temp/source-hook tool
	// appended above with a network cap). Without this, a Private
	// orchestrator turn could plan a step, then the worker step gets
	// `agents` + dispatch its way to a network-capable sub-agent.
	if t.privateMode {
		filtered := tools[:0]
		filteredNames := toolNames[:0]
		nameSet := map[string]bool{}
		dropped := []string{}
		for _, td := range tools {
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
			nameSet[td.Tool.Name] = true
		}
		for _, n := range toolNames {
			if nameSet[n] {
				filteredNames = append(filteredNames, n)
			}
		}
		tools = filtered
		toolNames = filteredNames
		if len(dropped) > 0 {
			Log("[orchestrate.tools] step %d private mode dropped %d network-capable tool(s): %v",
				cur.ID, len(dropped), dropped)
		}
	}
	Log("[orchestrate.tools] step %d resolved %d tools: %v",
		cur.ID, len(tools), toolNames)

	var ctxBlock strings.Builder
	if priorTurn := t.priorAssistantContext(); priorTurn != "" {
		ctxBlock.WriteString(priorTurn)
	}
	if len(prior) > 0 {
		ctxBlock.WriteString("## Earlier steps in this plan\n\n")
		for _, p := range prior {
			fmt.Fprintf(&ctxBlock, "### Step %d: %s\n%s\n\n", p.ID, p.Title, strings.TrimSpace(p.Output))
		}
	}
	stepUser := fmt.Sprintf(
		"## Original user request\n%s\n\n%s%s%s## Your task (step %d)\n%s",
		userMsg,
		notesContextBlock(notes),
		t.toolLogPromptSection(),
		ctxBlock.String(),
		cur.ID,
		cur.Title,
	)

	// Stream the worker's content into nothing visible — the runner
	// emits the final step output via the plan block in the chat
	// pane. Capturing chunks here gives a non-empty fallback when
	// the LLM responds in reasoning-only.
	var fullOut strings.Builder
	stream := func(chunk string) { fullOut.WriteString(chunk) }

	// Tag every tool call this worker makes with a "↳ [worker: <Title>]"
	// nesting prefix so the user can tell at a glance which calls are
	// the orchestrator's vs the worker's. Without this, plan_set's
	// inner tool_def / create_agent / etc. chips blend visually with
	// Builder's own chips and the conversation reads as if Builder
	// authored them inline — defeating the whole conductor metaphor.
	// Mirrors the agents(run) sub-dispatch nesting ("↳ [TargetName] …").
	workerLabel := strings.TrimSpace(cur.Title)
	if workerLabel == "" {
		workerLabel = "step"
	}
	if len(workerLabel) > 32 {
		workerLabel = workerLabel[:32] + "…"
	}
	t.wrapToolsForActivity(sess, tools, "↳ [worker: "+workerLabel+"] ")

	// Collapse admin-curated tool groups. Workers may have a totally
	// different tool surface than the orchestrator (each step
	// resolves its own AllowedTools), so each step needs its own
	// (Runtime tool-group rewriting retired — see the matching note
	// in runPlan. Vector pre-selection on the orchestrator's side
	// handles catalog-saturation; worker steps run on whatever
	// surface the orchestrator picked. find_tools is still the
	// LLM's explicit-search fallback.)

	// Compose the worker's system prompt:
	//   1. The orchestrator's per-step brief (cur.WorkerBrief), or a
	//      minimal fallback derived from title + intent when the
	//      orchestrator didn't author one.
	//   2. Rules + memory prepended via the standard agent context
	//      helper (so the worker honors the agent's policy + prior
	//      learning even though it has no user-authored persona).
	//   3. The framework-side tool-use directive when tools are
	//      present, so the worker prefers tool calls over fabricating
	//      from training for time-sensitive or specific information.
	brief := strings.TrimSpace(cur.WorkerBrief)
	if brief == "" {
		brief = fmt.Sprintf(
			"Execute this step: %s. %s\n\nBe direct, factual, no preamble.",
			cur.Title,
			strings.TrimSpace(cur.Intent),
		)
	}
	sysPrompt := prependAgentContext(t.gatedPersona(brief), t.agent, t.facts(), t.operatingNotes())
	if len(tools) > 0 {
		sysPrompt += "\n\n" + buildToolUseDirective(tools)
	}
	if frag := RenderToolPromptFragments(tools); frag != "" {
		sysPrompt += "\n\n" + frag
	}
	// Universal authoring directives for Builder-spawned workers.
	// Workers don't see Builder's OrchestratorPrompt (only the brief
	// + per-tool fragments), so the cross-cutting rules that apply
	// to any script the worker writes — URL encoding, no pip install,
	// hook usage, script-body patterns, etc. — never reach them
	// otherwise. Injected here for any worker whose parent agent is
	// Builder. Kept short on purpose: the rules that catch the most
	// common authoring mistakes, no narrative.
	if isBuilderAgent(t.agent.ID) {
		// builderWorkerDirectives is appended after prependAgentContext and this
		// worker path skips appendAgentCapabilityBlocks, so run the mode-aware
		// rewrite here too (the directives name store_fact in prose).
		sysPrompt += "\n\n" + rewriteMemoryToolNames(builderWorkerDirectives)
		sysPrompt += sandboxPythonNoteSection()
	}

	stopKeepalive := startKeepalive(t.sse)
	f := false
	// Reset explorer state per worker step so a step starts at the
	// soft cap; the LLM must opt back into explorer mode if it
	// still needs more rounds.
	t.explorerMode = false
	t.explorerReason = ""
	softCap := resolveMaxWorkerRounds(t.agent)
	hardCap := softCap
	if t.agent.AllowExplorer && softCap < explorerHardCap {
		hardCap = explorerHardCap
	}
	roundsUsed := 0
	resp, _, err := t.app.RunAgentLoop(t.ctx, []Message{{Role: "user", Content: stepUser}}, AgentLoopConfig{
		SendGuardKey:         sendGuardKey,
		SystemPrompt:         sysPrompt,
		Tools:                tools,
		DynamicTools:         t.dynamicNewTempTools(sess),
		ToolFallbackResolver: t.lazyToolFallback,
		MaxRounds:            hardCap,
		ThinkBudget:          t.agent.ThinkBudget, // per-agent override; 0 = inherit route/global
		Stream:               stream,
		// Worker-step corrections breadcrumb into the same session trail as
		// the orchestrator loop — a silent re-prompt during a plan step is
		// still a framework decision the user should be able to see.
		OnDiag: t.turnDiag,
		// One-shot advice for the failure-shape guard: three hits on one wall
		// means the arguments aren't what's wrong, so ask a model that can
		// ANSWER instead of nudging the one that's stuck. See consult.go.
		Consult: t.consult,
		// OnStep feeds telemetry — rounds, tool calls, dup-args
		// fingerprints. Summary log fires from the deferred block at
		// the top of runWorkerStep.
		OnStep: func(info StepInfo) { telem.record(info) },
		// Soft-cap enforcement for explorer-mode agents: pass hardCap
		// as MaxRounds upfront, then stop early at softCap UNLESS the
		// LLM has flipped explorerMode via enter_explorer_mode. For
		// non-explorer agents softCap == hardCap so StopRound never
		// fires until both are exhausted.
		StopRound: func() bool {
			roundsUsed++
			if t.explorerMode {
				return false
			}
			return roundsUsed > softCap
		},
		// Escalation policy hook (confirm.go) — same policy as the
		// orchestrator loop: flagged-credential calls park on the
		// in-chat approval card; everything else auto-approves (no
		// stdin fallback — gohort runs as a service).
		Confirm: t.confirmFuncFor(sess),
		// (No SingleFireGroups for image/video producers — same
		// rationale as the orchestrator round above. Multi-fire is
		// intentional under the write-to-workspace + workspace(attach)
		// architecture.)
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	stopKeepalive()
	if err != nil {
		return "", err
	}
	out := fullOut.String()
	if out == "" && resp != nil {
		out = resp.Content
	}
	// Defensive markup strip — models sometimes emit prompt-style
	// <tool_call><function=...>...</function></tool_call> markup IN
	// their text content (especially when emitting multiple calls in
	// one response). The agent loop only dispatches the first parsed
	// call and strips its markup from history, but a parse failure
	// or extra unparsed blocks leak markup into the step output.
	// Always sweep before returning so the user never sees raw XML.
	out = StripToolCallMarkup(out)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(no output)"
	}
	// Finalize the per-step bubble (if a tool created one via
	// ensureBubbleForTool) so it sheds the streaming class and any
	// app-side decorators fire. Idempotent — no-op when no bubble
	// was created this step.
	if id := t.getCurrentMsgID(); id != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.setCurrentMsgID("")
	}
	return out, nil
}

// runSynthesis has the orchestrator compose the final user-facing
// reply given the original message + every worker step output. Streamed
// to the user via SSE chunk events as it generates.
func (t *chatTurn) runSynthesis(userMsg string, steps []PlanStep, notes []injectionNote) (string, error) {
	// Build the LLM message array as full conversation history with
	// the synthesis-flavored content REPLACING the latest user turn
	// (handleSend already appended the user's current message; we
	// strip it and re-add a beefed-up version that folds in worker
	// findings + mid-flight notes). This gives the synthesizer the
	// same multi-turn continuity the orchestrator gets, so follow-
	// ups like "tell me more about that" land with the right
	// referent instead of being synthesized in isolation.
	var msgs []Message
	if t.session != nil {
		hist := t.session.Messages
		if n := len(hist); n > 0 && hist[n-1].Role == "user" {
			hist = hist[:n-1]
		}
		msgs = toLLMMessages(hist)
	}

	var body strings.Builder
	body.WriteString(userMsg)
	body.WriteString("\n\n")
	if nb := notesContextBlock(notes); nb != "" {
		body.WriteString(nb)
	}
	body.WriteString("## Worker findings for this turn (internal context — the user can't see this)\n\n")
	for _, s := range steps {
		fmt.Fprintf(&body, "### Step %d: %s\n%s\n\n", s.ID, s.Title, strings.TrimSpace(s.Output))
	}
	body.WriteString("\n## Your task\nCompose the final reply to the user's message above. Use the worker findings as your source material; integrate them naturally with the prior conversation context. Don't restate the plan unless it helps explain the result, and don't repeat content already established earlier in the conversation.")
	msgs = append(msgs, Message{Role: "user", Content: body.String()})

	// Allocate a stable message id so SSE chunk events know which
	// bubble in the conversation pane to stream into.
	msgID := fmt.Sprintf("synth-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   msgID,
		"text": "",
	})

	var full strings.Builder
	handler := func(chunk string) {
		full.WriteString(chunk)
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   msgID,
			"text": chunk,
		})
	}
	stopKeepalive := startKeepalive(t.sse)
	// Worker tier (Private: true route stage) — same routing as the
	// plan round; honors the "worker (thinking)" preference.
	think := true
	if p := RouteThink(orchestratorRouteKey(t.agent.ID, t.shouldUseLeadModel())); p != nil {
		think = *p
	}
	// Per-agent override wins over the route default (see plan round
	// for rationale).
	switch t.agent.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	synthStart := time.Now()
	// Same skill injection the orchestrator round saw — synthesis
	// IS the user-facing voice, so any skill the LLM activated this
	// turn must shape the final reply as well. The orchestrator
	// round sees the skill via the tool result in conversation
	// history; synthesis builds a fresh system prompt and re-injects
	// here so a skill that prescribed a tone or reference convention
	// governs both planning and reply. No-op when no skills active.
	synthSys := prependAgentContext(t.gatedPersona(t.agent.OrchestratorPrompt), t.agent, t.facts(), t.operatingNotes())
	// Re-inject any trigger-matched skill instructions so a skill that
	// prescribed a tone/convention governs the synthesis reply too.
	// Re-inject the full instructions of any skill the LLM consulted this
	// turn so its lens governs the synthesis reply. No trigger hints here —
	// synthesis has no tools, so a "go consult it" nudge would be useless.
	synthSys += t.renderTriggeredSkills()
	resp, err := t.app.LLM.ChatStream(t.ctx,
		msgs,
		handler,
		WithSystemPrompt(synthSys),
		WithRouteKey(orchestratorRouteKey(t.agent.ID, t.shouldUseLeadModel())),
		WithThink(think),
	)
	stopKeepalive()
	if err != nil {
		return "", err
	}
	reply := full.String()
	// Thinking models sometimes emit their entire response in the
	// reasoning channel — the content stream stays silent and resp.Content
	// is only populated after agent_loop promotes reasoning. Catch that
	// case and flush as a single chunk so the bubble isn't empty.
	if reply == "" && resp != nil && strings.TrimSpace(resp.Content) != "" {
		reply = resp.Content
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   msgID,
			"text": reply,
		})
	}
	t.sse.Send(map[string]any{"kind": "message_done", "id": msgID})
	t.emitStats(msgID, resp, synthStart)
	// Scrub framework-internal markers AND enforce the no-em-dash house style on
	// the saved/exported copy (the client also strips both on render — see
	// uiRenderMarkdown). Cheap no-op when neither is present.
	return strings.TrimSpace(StripEmDashes(StripMetaTags(reply))), nil
}

// --- helpers ---------------------------------------------------------------

// toLLMMessages converts the on-disk ChatMessage slice to the LLM
// Message slice the Chat call expects. Identity-shaped — the framework's
// Message type uses Role + Content.
func toLLMMessages(msgs []ChatMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for mi, m := range msgs {
		base := Message{Role: m.Role, Content: m.Content}
		// Automated reports store a clean body (the UI shows the producer in a
		// card header); re-attach an origin marker for the LLM so it reads as an
		// automated report, not something it said itself. Wrapped in
		// <gohort-meta> so that if the model echoes it into a reply it's scrubbed
		// (a bare [standing agent …] would leak); the model still reads it as
		// input (StripMetaTags only touches output).
		if m.ReportFrom != "" {
			base.Content = fmt.Sprintf("<gohort-meta>automated report from %q — context, not user input</gohort-meta>\n%s", m.ReportFrom, m.Content)
		}
		// Preserve tool calls + results across turns. ChatMessage.ToolCalls
		// (set on the assistant turn that fired them) gets expanded into
		// the LLM-protocol shape: the assistant message carries ToolCalls,
		// followed by a synthetic user message carrying ToolResults.
		// Without this, the LLM only sees the bare text content from
		// prior turns and has to re-derive what actually happened —
		// which manifests as "looping on what we're even talking about"
		// and re-asking questions whose answers came from tool returns.
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			calls := make([]ToolCall, 0, len(m.ToolCalls))
			results := make([]ToolResult, 0, len(m.ToolCalls))
			for ti, tc := range m.ToolCalls {
				// Stable per-(message, tool-index) IDs so the
				// assistant's ToolCall.ID matches the corresponding
				// ToolResult.ID within this conversion.
				id := fmt.Sprintf("hist-%d-%d", mi, ti)
				calls = append(calls, ToolCall{
					ID:   id,
					Name: tc.Name,
					Args: tc.Args,
				})
				content := tc.Result
				isErr := false
				if tc.Err != "" {
					content = "Error: " + tc.Err
					isErr = true
				}
				results = append(results, ToolResult{
					ID:      id,
					Content: content,
					IsError: isErr,
				})
			}
			base.ToolCalls = calls
			out = append(out, base)
			out = append(out, Message{
				Role:        "user",
				ToolResults: results,
			})
			continue
		}
		out = append(out, base)
	}
	return out
}

// parsePlanSteps coerces the orchestrator's plan_set "steps" arg into
// PlanStep records. Accepts multiple shapes (LLMs occasionally regress
// to older ones even after the schema upgrade):
//
//   - []any of map[string]any with title + optional intent + optional worker_brief
//   - []any of strings (legacy: title-only)
//
// Empty / whitespace titles are dropped silently. Length is clipped
// to maxSteps with a log when the orchestrator overshoots its budget.
// normalizeFormStep coerces one entry from the ask_user_form tool's
// `steps` array into the shape the client renderer expects:
// {question, options:[...], multi:bool, type, placeholder}. Defensive
// against the LLM varying types (string options vs []any, missing fields).
// A non-empty `type` marks the step as a typed entry FIELD (text / number /
// textarea / select / password); the renderer switches to an all-at-once
// form when any step carries one.
func normalizeFormStep(m map[string]any) map[string]any {
	out := map[string]any{
		"question": strings.TrimSpace(stringArg(m, "question")),
	}
	if opts := stringSliceFromArgs(m, "options"); len(opts) > 0 {
		out["options"] = opts
	}
	if v, ok := m["multi"].(bool); ok && v {
		out["multi"] = true
	}
	// Entry-field metadata. Only accept known types so a stray value can't
	// produce a broken input; anything else falls back to a choice/open step.
	switch strings.ToLower(strings.TrimSpace(stringArg(m, "type"))) {
	case "text", "number", "textarea", "select", "password":
		out["type"] = strings.ToLower(strings.TrimSpace(stringArg(m, "type")))
	}
	if ph := strings.TrimSpace(stringArg(m, "placeholder")); ph != "" {
		out["placeholder"] = ph
	}
	return out
}

// looksLikeVacuousPlan returns a non-empty reason string when the plan
// consists entirely of no-op / "just respond" steps. Returns "" when
// the plan looks like real decomposition. Matches case-insensitively
// against title+intent+worker_brief for known empty-step phrasings.
//
// Triggers seen in real traces:
//   - title: "Respond to user", intent: "respond directly", brief: "..."
//   - "Acknowledge the message", "Reply to the user", "Compose the answer"
//   - briefs that name respond_directly as the only tool to use
//
// We reject when EVERY step matches at least one of these patterns —
// a single ack step at the end of a real plan is fine.
func looksLikeVacuousPlan(steps []PlanStep) string {
	if len(steps) == 0 {
		return ""
	}
	emptyPatterns := []string{
		"respond_directly",
		"respond to the user",
		"respond to user",
		"reply to the user",
		"reply to user",
		"compose the reply",
		"compose the response",
		"compose the answer",
		"acknowledge the message",
		"acknowledge the user",
		"answer directly",
		"just respond",
		"summarize and reply",
		"summarize and respond",
		"final reply",
		"final response",
	}
	matchesEmpty := func(s string) bool {
		low := strings.ToLower(s)
		for _, p := range emptyPatterns {
			if strings.Contains(low, p) {
				return true
			}
		}
		return false
	}
	for _, st := range steps {
		blob := st.Title + " || " + st.Intent + " || " + st.WorkerBrief
		if !matchesEmpty(blob) {
			return ""
		}
	}
	return "every step is a 'just respond' / no-op step"
}

// sanitizePlanText cleans a plan-step title/intent a model handed over
// JSON-ENCODED instead of as plain text — the "messed-up plan" symptom: literal
// \n / \t / \" escape sequences plus a stray closing quote+comma leaked from an
// adjacent JSON field (e.g. a malformed ask_user payload dropped into a step).
// The framework stores tool args verbatim, so it decodes them here at the parse
// boundary — the one place every plan-card surface (live + replayed) funnels
// through. Conservative: only unescapes when literal escapes are present, only
// strips the exact quote / quote-comma leak signature.
func sanitizePlanText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimSuffix(s, `",`))
	s = strings.TrimSpace(strings.TrimSuffix(s, `"`))
	s = strings.TrimPrefix(s, `"`)
	if strings.Contains(s, `\n`) || strings.Contains(s, `\t`) || strings.Contains(s, `\"`) {
		s = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r", `\"`, `"`, `\\`, `\`).Replace(s)
	}
	return strings.TrimSpace(s)
}

func parsePlanSteps(raw any, maxSteps int) []PlanStep {
	var out []PlanStep
	add := func(title, intent, brief string, tools []string) {
		title = sanitizePlanText(title)
		if title == "" {
			return
		}
		out = append(out, PlanStep{
			ID:          len(out) + 1,
			Title:       title,
			Intent:      sanitizePlanText(intent),
			WorkerBrief: strings.TrimSpace(brief),
			Tools:       tools,
			Status:      StepPending,
		})
	}
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			switch s := x.(type) {
			case string:
				add(s, "", "", nil)
			case map[string]any:
				add(stringArg(s, "title"), stringArg(s, "intent"), stringArg(s, "worker_brief"), stringSliceFromArgs(s, "tools"))
			}
		}
	case []string:
		for _, s := range v {
			add(s, "", "", nil)
		}
	case []map[string]any:
		for _, m := range v {
			add(stringArg(m, "title"), stringArg(m, "intent"), stringArg(m, "worker_brief"), stringSliceFromArgs(m, "tools"))
		}
	}
	if len(out) > maxSteps {
		Log("[orchestrate.plan] LLM returned %d steps; clipping to MaxPlanSteps=%d",
			len(out), maxSteps)
		out = out[:maxSteps]
	}
	return out
}

// activityCheapID returns a monotonic short id for an activity row.
// Each activity event needs a unique id so future updates (truncation
// hints, status flips) can target it; we don't have one from the
// agent loop, so we generate here. Atomic counter to keep concurrent
// wrapped-handler calls from racing.
var activityIDCounter uint64

func activityCheapID() string {
	n := atomic.AddUint64(&activityIDCounter, 1)
	return fmt.Sprintf("a-%d-%d", time.Now().UnixNano(), n)
}

// formatToolCall renders a tool invocation as a single-line label
// for the activity pane. Keys sorted for stable output across runs.
// Values are stringified and clipped to keep the row scannable —
// long args (page bodies, base64 blobs) ruin the at-a-glance read.
func formatToolCall(name string, args map[string]any) string {
	if len(args) == 0 {
		return "🔧 " + name + "()"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch vv := args[k].(type) {
		case string:
			s = vv
		case []any:
			elems := make([]string, 0, len(vv))
			for _, e := range vv {
				elems = append(elems, fmt.Sprintf("%v", e))
			}
			s = "[" + strings.Join(elems, ", ") + "]"
		case map[string]any:
			s = "{…}"
		case nil:
			s = "null"
		default:
			s = fmt.Sprintf("%v", vv)
		}
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 80 {
			s = s[:80] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, s))
	}
	return "🔧 " + name + "(" + strings.Join(parts, ", ") + ")"
}

// deriveFindings extracts a short summary from worker output for the
// plan card's always-visible "findings" preview. Takes the first
// non-empty paragraph (split on blank line), strips markdown noise
// that reads badly without rendering, and clips to a length that
// fits a few lines in the plan card. The full output still lives in
// the collapsible — this is just the headline.
func deriveFindings(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(no output)" {
		return ""
	}
	// First paragraph = everything up to the first blank line.
	if i := strings.Index(s, "\n\n"); i > 0 {
		s = s[:i]
	}
	// Collapse internal newlines so the snippet renders as one
	// flowing line; the user can expand the full output if they
	// want the structure.
	s = strings.ReplaceAll(s, "\n", " ")
	// Strip a leading markdown heading marker — headings in the
	// findings slot land as bare text and look broken.
	s = strings.TrimLeft(s, "# ")
	const cap = 280
	if len(s) > cap {
		// Cut at the last space before cap to avoid mid-word slices.
		cut := cap
		if sp := strings.LastIndexByte(s[:cap], ' '); sp > cap/2 {
			cut = sp
		}
		s = s[:cut] + "…"
	}
	return strings.TrimSpace(s)
}

// truncate clips long tool-output text for activity rendering. The
// worker's full output still rides through the agent loop into the
// step's persisted output, so nothing is lost — only the live
// activity row gets cut down.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[truncated]"
}

// planBlockID is the stable id for one plan's in-chat block. Each
// chat round (i.e. each user turn that produces a new plan) gets its
// own block via the round-index suffix so prior plans in the same
// session stay visible — re-emitting the same id triggers onUpdate on
// the existing block instead of creating a new one.
func planBlockID(sessionID string, roundIdx int) string {
	return fmt.Sprintf("plan-%s-%d", sessionID, roundIdx)
}

// emitPlanBlock sends the plan as a single `block` SSE event into the
// conversation pane. Called once when the plan is first set and again
// on every step status / output transition; the framework's block
// dispatcher routes the repeat into the renderer's onUpdate so the
// same DOM node refreshes in place.
//
// Shape matches servitor_plan's payload: title + what_to_find +
// findings + blocked_reason. Intent maps to what_to_find (same
// semantic — "what this step is looking for"). Full per-step output
// is NOT shipped to the renderer — findings is the visible result;
// drill-down lives in the activity pane.
func emitPlanBlock(sse *sseWriter, blockID string, steps []PlanStep) {
	items := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		item := map[string]any{
			"id":     s.ID,
			"title":  s.Title,
			"status": string(s.Status),
		}
		if strings.TrimSpace(s.Intent) != "" {
			item["what_to_find"] = s.Intent
		}
		if strings.TrimSpace(s.Findings) != "" {
			item["findings"] = s.Findings
		}
		if strings.TrimSpace(s.BlockedReason) != "" {
			item["blocked_reason"] = s.BlockedReason
		}
		items = append(items, item)
	}
	sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_plan",
		"id":   blockID,
		"plan": items,
	})
}

// emitIntentBlock emits a servitor_intent-style block before each
// worker step starts. The conversation pane renders an accent-
// bordered card with "▸ Investigating" + the step's title + intent
// as italic reason, so the user sees what the agent is about to do
// in real time. One block per step; unique id so they accumulate
// chronologically rather than overwriting.
func emitIntentBlock(sse *sseWriter, blockID string, step PlanStep) {
	payload := map[string]any{
		"kind": "block",
		"type": "orchestrate_intent",
		"id":   blockID,
		"text": fmt.Sprintf("Step %d: %s", step.ID, step.Title),
	}
	if r := strings.TrimSpace(step.Intent); r != "" {
		payload["reason"] = r
	}
	sse.Send(payload)
}

// buildToolUseDirective produces a framework-side append to the worker's
// system prompt that nudges the model toward calling the supplied tools
// rather than answering from training. Without this push, the worker
// routinely fabricates answers to questions like "what's the weather in
// SF?" or "latest news on X" — both well-served by web_search/fetch_url
// but invisible to the model unless it's reminded.
//
// Lists each tool name + first-line description so the model knows what
// it has. The "prefer tools over guessing" language is the load-bearing
// nudge — models are good at picking the right tool when told to look
// before answering.
func buildToolUseDirective(tools []AgentToolDef) string {
	var b strings.Builder
	b.WriteString("## Tools available\n\n")
	for _, t := range tools {
		desc := strings.SplitN(t.Tool.Description, "\n", 2)[0]
		if len(desc) > 200 {
			desc = desc[:200] + "…"
		}
		fmt.Fprintf(&b, "- **%s** — %s\n", t.Tool.Name, desc)
	}
	return b.String()
}
