package core

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmcoffee/snugforge/nfo"

	"github.com/cmcoffee/gohort/core/prompts"
)

// ToolHandlerFunc is a function that executes a tool call and returns its output.
type ToolHandlerFunc func(args map[string]any) (string, error)

// safeInvoke runs a tool handler, converting a panic into an ordinary error.
// A tool handler is arbitrary app code; without this a panic (a) crashes the
// whole process in the parallel-tool branch, where the handler runs in a bare
// goroutine and an unrecovered panic is fatal, and (b) drops the turn with
// nothing the model can react to. Recovering turns the panic into a normal tool
// error the loop surfaces as an IsError result, so the model sees "tool
// panicked: …" and can adjust. The full stack goes to the debug log, never into
// the model's context (stacks are large and not useful to the LLM).
func safeInvoke(name string, handler ToolHandlerFunc, args map[string]any) (output string, err error) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 8192)
			buf = buf[:runtime.Stack(buf, false)]
			Debug("[agent_loop] tool %q PANICKED: %v\n%s", name, r, buf)
			output = ""
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()
	output, err = handler(args)
	// Strip the framework mark unconditionally: whether or not an app wrapper
	// read it, it must never reach the model. This is the one place every tool
	// call passes through.
	output, _ = TakeFrameworkResultMark(output)
	return output, err
}

// ErrToolDenied is returned when the user denies a tool call.
var ErrToolDenied = fmt.Errorf("tool call denied by user")

// LeadTurnTokenBudget caps what ONE agent turn may spend on the lead tier
// (input + output, summed across the turn's lead-served rounds). Past it the
// remaining rounds run on the worker — the turn still finishes, it just stops
// billing frontier rates for a loop that isn't converging. Sized to clear a
// legitimately large design turn (roughly eight full-history rounds) and to
// bite only on a genuine flail. Set to 0 to disable the cap entirely.
var LeadTurnTokenBudget = 500_000

// defaultConfirm prompts the user in the terminal with a Claude Code-style
// confirmation showing the tool name and arguments.
func defaultConfirm(toolName string, argsSummary string) bool {
	PleaseWait.Hide()
	fmt.Fprintf(os.Stderr, "\n\033[1;33m  ╭─ Tool Call ─────────────────────────\033[0m\n")
	fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m \033[1m%s\033[0m\n", toolName)
	if argsSummary != "" {
		for _, line := range strings.Split(argsSummary, "\n") {
			fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m   %s\n", line)
		}
	}
	fmt.Fprintf(os.Stderr, "\033[1;33m  ╰──────────────────────────────────────\033[0m\n")
	result := nfo.GetConfirm("  Allow this tool call?")
	PleaseWait.Show()
	return result
}

// toolCallLabel names a call the way a log reader needs to see it: the tool,
// plus the ACTION when the tool is a grouped one.
//
// A grouped tool is one name over many operations — the moltbook tool both
// lists your messages and publishes a post — so logging the bare name makes
// those identical on the page. "tool call: moltbook (args=46 bytes)" cannot
// answer the only question worth asking of a scheduled fire's log: did it
// actually post, or did it just read? The action is the answer and it was
// being dropped.
//
// Only `action` is lifted, and only when it is a plain scalar. Everything else
// stays in the Trace line: the point is to make the log scannable, not to leak
// argument content into DEBUG, which MaskDebugOutput exists to prevent.
func toolCallLabel(tc ToolCall) string {
	if tc.Args == nil {
		return tc.Name
	}
	switch v := tc.Args["action"].(type) {
	case string:
		if a := strings.TrimSpace(v); a != "" {
			return tc.Name + "/" + a
		}
	}
	return tc.Name
}

// formatArgs formats tool call arguments as a human-readable summary.
func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	// Sort keys so the output is DETERMINISTIC. Go randomizes map
	// iteration order, and formatArgs feeds the loop-guard signature
	// (sig := name + formatArgs(args); repeatFail keys on it). With an
	// unsorted order the SAME logical call hashes to different signatures
	// depending on iteration order, so identical failing calls split
	// across those variants and never reach repeatFailLimit — the guard
	// silently never fires for any tool called with 2+ args. Sorting also
	// stabilizes the confirm-dialog display, which shares this helper.
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		display := stringify(args[k])
		if len(display) > 200 {
			display = display[:200] + "..."
		}
		lines = append(lines, fmt.Sprintf("%s: %s", k, display))
	}
	return strings.Join(lines, "\n")
}

// countBatchDupes reports how many calls in a batch were deduped onto the
// canonical call at results index canon. Used only to tell the model how many
// copies it actually emitted — "you issued this 3 times" lands where "this was
// a duplicate" gets skimmed past.
func countBatchDupes(pairs [][2]int, canon int) int {
	n := 0
	for _, p := range pairs {
		if p[1] == canon {
			n++
		}
	}
	return n
}

// Run is a convenience method that resolves tools from SetTools(), uses the
// stored system prompt, and applies MaxRounds, then calls RunAgentLoop.
// Additional ChatOptions can be passed for per-call settings like WithMaxTokens.
func (T *AppCore) Run(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, []Message, error) {
	if err := T.RequireLLM(); err != nil {
		return nil, messages, err
	}

	var tools []AgentToolDef
	if len(T.tools) > 0 {
		var err error
		tools, err = GetAgentTools(T.tools...)
		if err != nil {
			return nil, messages, err
		}
	}

	return T.RunAgentLoop(ctx, messages, AgentLoopConfig{
		SystemPrompt: T.systemPrompt,
		Tools:        tools,
		MaxRounds:    T.MaxRounds,
		PromptTools:  T.promptToolsMode(),
		ChatOptions:  opts,
		OnStep: func(step StepInfo) {
			if step.Done {
				return
			}
			for _, tc := range step.ToolCalls {
				Debug("[agent] round %d: called tool '%s'", step.Round, tc.Name)
				if step.ToolErrors > 0 {
					Debug("[agent] round %d: %d tool error(s)", step.Round, step.ToolErrors)
				}
			}
		},
	})
}

// RunAgentLoop runs an autonomous agent loop: the LLM receives the initial
// messages, can call tools, observe results, and continue reasoning until it
// produces a final text response or hits MaxRounds.
//
// The returned Response is from the final LLM call. The returned []Message
// contains the full conversation history including all tool interactions.
func (T *AppCore) RunAgentLoop(ctx context.Context, messages []Message, cfg AgentLoopConfig) (*Response, []Message, error) {
	// Pre-empted: an app-layer check already refused this request, so there is no
	// turn to run. Handled at the OUTER boundary, before runAgentLoopInner, because
	// nothing inside the loop should have to know about a turn that never happens.
	if reply := strings.TrimSpace(cfg.PreEmptedReply); reply != "" {
		resp, history := T.deliverPreEmptedReply(messages, reply, cfg)
		return resp, history, nil
	}
	// Compaction is ON unless a caller has explicitly said otherwise.
	//
	// The window is not the caller's business — it belongs to whichever model
	// tier the turn ends up on, which the loop knows and the caller often does
	// not. Asking every construction site to remember produced exactly what
	// asking always produces: one site remembered. The nineteen that did not
	// included every scheduled fire, and a scheduled agent carrying its prior
	// conversation is the turn most likely to grow past the window — observed
	// at a 600KB request whose reply then wrote out three posts in prose and
	// called no tool at all, because the system prompt telling it to had been
	// dropped server-side.
	if cfg.ContextSize == 0 {
		cfg.ContextSize = T.loopContextSize()
	}
	resp, history, err := T.runAgentLoopInner(ctx, messages, cfg)
	// Think-tag leak backstop at THE loop boundary, so every surface (web
	// chat, dispatch, scheduled fires) is covered by one seam — the channel
	// path additionally strips at phantom outbound, but web chat had no strip
	// at all and a leak reached the bubble + stored transcript verbatim
	// ("blocks\n6. …\n</think>\nQwen3.6-27B works…"). Only fires when a
	// delimiter appears OUTSIDE inline/fenced code: an answer that merely
	// MENTIONS `<think>` in backticks (any conversation about thinking modes)
	// is legit prose and must not be clipped by the stripper's
	// keep-one-side-of-the-closer heuristics.
	if resp != nil && strings.TrimSpace(resp.Content) != "" {
		if thinkTagOutsideCode(resp.Content) {
			cleaned, leaked := StripThinkTags(resp.Content)
			if leaked {
				Log("[agent_loop] think-tag leak stripped from final content (%d -> %d chars) — upstream reasoning/content separation failed", len(resp.Content), len(cleaned))
				resp.Content = cleaned
			}
		}
	}
	return resp, history, err
}

// runAgentLoopInner drives the rounds. Everything a round needs lives on
// loopRun (the turn) and loopRun.rs (the current round, zeroed at the top of
// each one); every phase of the turn is a method, and a round phase answers
// with what the driver should do next. This function is the order they run
// in, the defers that bracket them, and the two early exits that must
// happen before any state is committed.
func (T *AppCore) runAgentLoopInner(ctx context.Context, messages []Message, cfg AgentLoopConfig) (*Response, []Message, error) {
	if T.LLM == nil {
		return nil, messages, fmt.Errorf("LLM is not configured")
	}
	lr := &loopRun{T: T, ctx: ctx, messages: messages, cfg: cfg}
	lr.setupTools()
	// Turn-time split. Everything the loop spends that is NOT waiting on a
	// provider is gohort's own overhead — prompt assembly, knowledge
	// injection, tool resolution, guardrails, and the tool calls themselves.
	// Until now that number could only be inferred by subtracting a separate
	// direct-to-provider probe from a wall-clock stopwatch, so "is the
	// framework adding latency?" was never answerable from a running
	// deployment. The provider span is measured between the two existing
	// LLM breadcrumbs; a round that errors out mid-call leaves its span
	// uncounted, which biases the report toward OVER-reporting gohort's
	// share on a failed turn — the safe direction for a diagnostic.
	defer func() {
		total := time.Since(lr.turnStarted)
		own := total - lr.llmWall
		if own < 0 {
			own = 0
		}
		pct := 0
		if total > 0 {
			pct = int(own * 100 / total)
		}
		Log("[agent_loop] turn time: %s total = %s in %d LLM call(s) + %s gohort (%d%%)",
			total.Round(time.Millisecond), lr.llmWall.Round(time.Millisecond), lr.llmCalls,
			own.Round(time.Millisecond), pct)
	}()
	lr.setupHistory()
	lr.setupPrompt()
	lr.setupState()
	// Over budget before the first call: refuse the turn rather than start
	// work that will de-escalate on its first round anyway. Checked before
	// the failure-memory save is armed, so a refused turn writes nothing.
	if over, spent := overDailySpend(cfg); over {
		Log("[agent_loop] daily spend cap reached for %q ($%.2f of $%.2f) — turn refused", cfg.BudgetKey, spent, cfg.DailySpendUSD)
		lr.emitDiag("spend-cap", fmt.Sprintf("This agent has spent $%.2f of its $%.2f daily allowance; the turn was not run.", spent, cfg.DailySpendUSD))
		return &Response{Content: fmt.Sprintf(
			"I've reached my spending limit for now — $%.2f of the $%.2f allowed in a 24-hour window — so I didn't run this. It frees up as earlier work ages out, or the owner can raise the limit.",
			spent, cfg.DailySpendUSD)}, messages, nil
	}
	defer func() { saveFailureMemory(cfg.FailureMemoryKey, lr.repeatFail) }()

rounds:
	for lr.round = 1; lr.round <= lr.maxRounds+lr.graceRounds; lr.round++ {
		lr.rs = roundState{}
		switch lr.runRound() {
		case actContinue:
			continue
		case actBreak:
			break rounds
		case actReturn:
			return lr.ret.resp, lr.ret.history, lr.ret.err
		}
	}
	return lr.finish()
}

// runRound is one round of the loop, phase by phase. The first phase that
// wants the round to end says so and the rest do not run — the same shape
// the continue/break/return statements had when this was one body.
func (lr *loopRun) runRound() loopAction {
	for _, phase := range []func() loopAction{
		lr.roundHead,
		lr.prepareCall,
		lr.callModel,
		lr.recordResponse,
		lr.noToolCallRound,
		lr.interimGuardrail,
		lr.planToolWork,
		lr.dispatchTools,
		lr.settleToolRound,
	} {
		if act := phase(); act != actNone {
			return act
		}
	}
	return actNone
}

// Tool-round discipline — applies to EVERY tool-using agent loop
// (native or prompt-based tool calling), not just one app. The
// failure mode is the model writing a COMPLETE answer in a step
// that also calls a tool, then a SECOND complete answer in the
// final step — the user sees two full replies with a tool run
// wedged between. In-progress narration ("checking that now…",
// status notes) is fine and kept; what's forbidden is delivering
// the full, final answer more than once. The complete answer lands
// exactly once, in the final tool-free step.
// emitDisciplinePrompt gates the "no two answers in one turn" prompt
// block. RE-ENABLED: the deterministic runner-side drop of long text in
// tool rounds was removed because it dropped legitimate content the
// model emitted alongside a tool call but never repeated (false
// positives). Preventing the double at the SOURCE — telling the model to
// hold its full answer until the final tool-free round — is the right
// trade: in-progress narration is still allowed, only the full answer
// must wait, so nothing legitimate gets dropped.
const emitDisciplinePrompt = true

const failureStreakThreshold = 3

const repeatFailLimit = 3

// How much content makes a clean "stop" finish read as an ANSWER rather
// than a lead-in to a narrated tool call, for the prose-scan gate below.
// Under it the scan still runs, so a model that only ever describes its
// calls keeps working; over it we trust the model that said it was done.
// The case this comes from was 8366 chars; narrated intents run a few
// hundred. Wide margin on both sides on purpose.
const cleanFinishProseFloor = 2000

const repeatSameLimit = 4

const errShapeNudgeAt = 3

const errShapeDeescalateAt = 6

const guardBlockedBreakLimit = 2

const keepGoingSpinLimit = 3

// If the loop exhausted maxRounds and the last response has no content,
// scan backwards through the most recent few history entries for an
// assistant message that had content but no tool calls (a synthesis
// round). This handles models (e.g. Llama via Ollama) that occasionally
// return an empty final response after completing their tool-call
// sequence.
//
// CAP THE LOOKBACK. The rescue is meant to recover the model's
// IMMEDIATELY-PRIOR clean turn — e.g. it produced a synthesis on
// round N-1, then round N tool-called and returned empty. Walking
// arbitrarily far back can dredge up an answer to a much earlier
// user message and emit it as the reply to the current one, which
// reads to the user as the agent ignoring their last message and
// repeating itself. Limit to the last rescueLookback entries; if
// nothing useful is in that window, surface the empty response and
// let the caller decide (e.g. "I ran out of rounds, please retry").
const rescueLookback = 4

// First pass: resolve handlers and handle confirmations serially.
type toolWork struct {
	index   int
	tc      ToolCall
	handler ToolHandlerFunc
	sig     string
	sendKey string
}

// Round batch cap. A model can emit an arbitrarily large tool batch in
// ONE round (observed: ~120 agent dispatches in a single response) —
// per-tool guards then block each call individually, but all of them
// still execute-or-STOP and the round takes the full hit. Cap the
// batch: calls past the cap get an error result (the API still needs a
// result per call id) and never reach a handler. Counts as a guard
// block so repeated capped rounds feed the wedge break-out below.
const maxToolCallsPerRound = 24

type loopRun struct {
	T           *AppCore
	ctx         context.Context
	messages    []Message
	cfg         AgentLoopConfig
	turnStarted time.Time
	llmWall     time.Duration
	llmCalls    int
	maxRounds   int
	confirmFn   ConfirmFunc
	// Capability allow-set, computed once. Static tools and dynamic ones
	// (from cfg.DynamicTools) both pass through the same filter — runtime
	// tool registration can't elevate beyond the session's tier.
	allowedSet map[Capability]bool
	tools      []AgentToolDef
	// Tool dispatch maps. When DynamicTools is set these get rebuilt at
	// the top of each round so newly-defined temp tools become visible
	// to the LLM on the next call. When unset, the static slice is used
	// directly and these maps are computed once.
	toolDefs        []Tool
	handlers        map[string]ToolHandlerFunc
	premise         premiseGate
	needsConfirm    map[string]bool
	writeTools      map[string]bool
	singleFireTools map[string]bool
	serialFireTools map[string]bool
	batchLaneFns    map[string]func(map[string]any) string
	history         []Message
	systemPrompt    string
	// Every framework clause below arrives through the prompts registry, which
	// hands back "" for a block an operator has switched off. One conditional
	// append here beats fifteen at the call sites.
	//
	// The key rides along because the digest's question is "which rules were
	// live on THIS turn?", and a gate ("only when the agent has tools") answers
	// that in general while a per-turn list answers it for the turn that
	// actually went wrong.
	clauseKeys []string
	lastResp   *Response
	// Per-turn prompt digest: built on the first round, emitted once the first
	// response carries the provider's own token count.
	digest                     PromptDigest
	digestBuilt                bool
	digestSent                 bool
	prevHadToolCalls           bool
	corrections                *correctionBudget
	guardrailOutputCorrections int
	judgedNarration            map[string]bool
	skippedInterimGuard        bool
	toolFiredThisTurn          bool
	wrapUpWarningFired         bool
	midpointNudgeFired         bool
	baseRound                  int
	wrapUpThreshold            int
	midpointThreshold          int
	failureStreak              int
	failureStreakWarned        bool
	cumulativeToolErrors       int
	lastToolError              string
	turnToolCalls              []string
	repeatFail                 map[string]int
	sentThisTurn               map[string]bool
	shakeoutNextRound          bool
	guardrailQuietNextRound    bool
	repeatSame                 map[string]int
	lastToolContent            map[string]string
	errShapeCount              map[string]int
	errShapeNudged             map[string]bool
	toolFailShapes             map[string]map[string]bool
	graceRounds                int
	hardStop                   int
	// truncatedLead holds the text of every reply that was cut off and then
	// continued this turn, in order. See joinContinuation for who needs it.
	truncatedLead       strings.Builder
	guardBlockedStreak  int
	forceFinal          bool
	synthesizedFrom     string
	leadTokens          int
	deescalated         string
	keepGoingStreak     int
	lastRoundToolCalled bool
	round               int

	// rs is the current round's state, zeroed by the driver at the top of
	// every round exactly as the declarations it replaces were.
	rs roundState
	// ret carries an early return out of a round method (see exit).
	ret loopResult
}

type roundState struct {
	stop         bool
	forceCompact bool
	window       int
	budget       int
	// Route think is the default; ChatOptions override it. Build route
	// defaults first so per-call WithThink(true/false) takes precedence.
	opts                  []ChatOption
	roundOpts             []ChatOption
	offerTools            bool
	resp                  *Response
	err                   error
	histChars             int
	llmStarted            time.Time
	streamHandler         StreamHandler
	callOpts              []ChatOption
	roundUsedLead         bool
	results               []ToolResult
	toolErrors            int
	guardBlockedThisRound bool
	guardrailHalt         string
	silentCount           int
	realCount             int
	dropAllSilent         bool
	dedupeSilent          bool
	silentSeen            bool
	work                  []toolWork
	batchSend             map[string]bool
	batchSig              map[string]int
	batchDup              [][2]int
	knownIDs              map[string]bool
	abortSet              map[string]bool
	roundAborted          bool
	effectiveGroups       [][]string
	// Corrections raised while inspecting this round's results. They MUST
	// land after the tool-results message, never before it: providers
	// enforce that a tool result directly follows an assistant-or-tool
	// message, and a plain user turn wedged into that gap is a hard 400
	// from the chat template, not a soft degradation.
	pendingCorrections []Message
	allFailed          bool
	keepGoingOnly      bool
}

// loopAction is what a round method tells the driver to do next.
type loopAction int

const (
	actNone     loopAction = iota // carry on with the next phase of this round
	actContinue                   // next round
	actBreak                      // leave the loop and finish
	actReturn                     // return lr.ret from the function
)

// loopResult is the function's return, parked by exit until the driver returns it.
type loopResult struct {
	resp    *Response
	history []Message
	err     error
}

// exit records an early return and tells the driver to take it.
func (lr *loopRun) exit(resp *Response, history []Message, err error) loopAction {
	lr.ret = loopResult{resp, history, err}
	return actReturn
}

func (lr *loopRun) filterCaps(in []AgentToolDef) []AgentToolDef {
	if lr.allowedSet == nil {
		return in
	}
	out := make([]AgentToolDef, 0, len(in))
	for _, td := range in {
		if !capsAllowed(td.Tool.Caps, lr.allowedSet) {
			Debug("[agent_loop] tool '%s' filtered out by AllowedCaps (declares %v, allowed %v)", td.Tool.Name, td.Tool.Caps, lr.cfg.AllowedCaps)
			continue
		}
		out = append(out, td)
	}
	return out
}

func (lr *loopRun) rebuildToolMaps(active []AgentToolDef) {
	lr.toolDefs = lr.toolDefs[:0]
	for k := range lr.handlers {
		delete(lr.handlers, k)
	}
	for k := range lr.needsConfirm {
		delete(lr.needsConfirm, k)
	}
	for k := range lr.singleFireTools {
		delete(lr.singleFireTools, k)
	}
	for k := range lr.serialFireTools {
		delete(lr.serialFireTools, k)
	}
	for k := range lr.batchLaneFns {
		delete(lr.batchLaneFns, k)
	}
	for _, td := range active {
		// Name collision. Two defs can reach here under one name without any
		// earlier check firing — an EXPANDED toolbox synthesizes
		// "<toolbox>_<action>" at catalog-build time, long after the
		// registration-time uniqueness check compared record names. Before
		// this, the later def silently overwrote the handler while BOTH
		// schemas were shown to the model: it saw one name described two
		// ways and had no way to tell which it would actually get.
		//
		// FIRST registration wins, and the duplicate is dropped from the
		// catalog so the model sees exactly the def it will dispatch to.
		// First rather than last because the leading entry is the one
		// already described to the model, and because static built-ins are
		// prepended — the same direction IsReservedToolName enforces.
		// Loud, not silent: a shadowed tool is invisible by nature, so the
		// only way it gets noticed is a line naming it.
		if _, dup := lr.handlers[td.Tool.Name]; dup {
			Log("[agent_loop] tool name collision: %q is registered twice — keeping the first definition, ignoring the later one (an expanded toolbox action and a standalone tool can mint the same name)", td.Tool.Name)
			continue
		}
		lr.toolDefs = append(lr.toolDefs, td.Tool)
		lr.handlers[td.Tool.Name] = td.Handler
		// A declared ConfirmPrompt implies NeedsConfirm. Without this the
		// two flags are a trap: a tool author writes the sentence the user
		// is meant to read, forgets the boolean, and the loop never calls
		// Confirm at all — so the tool runs unasked and the only evidence
		// is a prompt string nothing ever renders.
		if td.NeedsConfirm || td.Confirmation.asks() {
			lr.needsConfirm[td.Tool.Name] = true
		}
		for _, c := range td.Tool.Caps {
			if c == CapWrite || c == CapExecute {
				lr.writeTools[td.Tool.Name] = true
				break
			}
		}
		// Serial-fire takes precedence: a serial tool must NOT be added to
		// the single-fire set, or the enforcement pass would drop its
		// excess calls before the executor gets to run them in order. A
		// lane function supersedes both for the same reason — its calls
		// all run, and the lanes decide which of them run together.
		if td.BatchLane != nil {
			lr.batchLaneFns[td.Tool.Name] = td.BatchLane
		} else if td.SerialFirePerBatch {
			lr.serialFireTools[td.Tool.Name] = true
		} else if td.SingleFirePerBatch {
			lr.singleFireTools[td.Tool.Name] = true
		}
	}
}

func (lr *loopRun) setupTools() {
	// Turn-time split. Everything the loop spends that is NOT waiting on a
	// provider is gohort's own overhead — prompt assembly, knowledge
	// injection, tool resolution, guardrails, and the tool calls themselves.
	// Until now that number could only be inferred by subtracting a separate
	// direct-to-provider probe from a wall-clock stopwatch, so "is the
	// framework adding latency?" was never answerable from a running
	// deployment. The provider span is measured between the two existing
	// LLM breadcrumbs; a round that errors out mid-call leaves its span
	// uncounted, which biases the report toward OVER-reporting gohort's
	// share on a failed turn — the safe direction for a diagnostic.
	lr.turnStarted = time.Now()
	lr.maxRounds = lr.cfg.MaxRounds
	if lr.maxRounds <= 0 {
		lr.maxRounds = 10
	}

	lr.confirmFn = lr.cfg.Confirm
	if lr.confirmFn == nil {
		lr.confirmFn = defaultConfirm
	}

	if len(lr.cfg.AllowedCaps) > 0 {
		lr.allowedSet = make(map[Capability]bool, len(lr.cfg.AllowedCaps))
		for _, c := range lr.cfg.AllowedCaps {
			lr.allowedSet[c] = true
		}
	}
	// Static (per-session) tools — survive across rounds. Dynamic tools
	// (cfg.DynamicTools) are pulled fresh per round and merged in below.
	lr.tools = lr.filterCaps(lr.cfg.Tools)

	lr.handlers = make(map[string]ToolHandlerFunc)
	// One hold per turn, for a turn driven by somebody who is not the principal.
	lr.premise = newPremiseGate(lr.cfg.LiveClaimSpeaker, LatestUserContent(lr.messages), lr.cfg.LiveClaimTrusted)
	lr.needsConfirm = make(map[string]bool)
	// Which tools DO something, for the unverified-premise gate below. Caps are
	// the framework's own annotation, not a name list, so a tool added later is
	// covered by declaring what it is.
	lr.writeTools = make(map[string]bool)
	lr.singleFireTools = make(map[string]bool)
	lr.serialFireTools = make(map[string]bool)
	lr.batchLaneFns = make(map[string]func(map[string]any) string)
	lr.rebuildToolMaps(lr.tools)
}

func (lr *loopRun) setupHistory() {
	lr.history = make([]Message, len(lr.messages))
	copy(lr.history, lr.messages)
	// Damp prior-turn failure storms before the model re-reads them: rebuilt
	// tool rounds carry every old error verbatim, and a wall of identical
	// failures is in-context training data for producing more of them. Keeps
	// the first and newest copy of each repeated shape, collapses the middle.
	if n := collapseIncomingFailureStreaks(lr.history); n > 0 {
		Debug("[agent_loop] failure-streak collapse: %d repeated failure result(s) in incoming history collapsed", n)
	}

	// Turn-scoped notes from the app, appended to the newest user turn BEFORE
	// the stamp goes on the front — so the app is handed the user's own words,
	// not a message that opens with a timestamp it has to look past.
	applyTurnNotes(lr.cfg, lr.history)

	// Stamp the current date+time onto the latest user turn (the human message
	// that opened this turn — tool-result user messages get appended below, so at
	// this point the last message IS the human turn). This is the cache-safe home
	// for the wall-clock: the newest user message is the volatile tail that never
	// hits cache anyway, so the stamp costs nothing, while the system prompt stays
	// date-free and cacheable across days. The stamp freezes here and rides into
	// the returned history, so on later turns it stays put (a stable, cached prefix
	// element) while only the next new turn re-stamps. Paired with WithoutAutoDate()
	// on the LLM calls below so the date isn't ALSO injected into the system prompt.
	if n := len(lr.history); n > 0 && lr.history[n-1].Role == "user" &&
		!strings.HasPrefix(lr.history[n-1].Content, "[Current date & time:") {
		lr.history[n-1].Content = CurrentContextStampIn(lr.cfg.StampLocation) + "\n\n" + lr.history[n-1].Content
	}
}

func (lr *loopRun) addClause(key, clause string) {
	if clause != "" {
		lr.systemPrompt += "\n\n" + clause
		lr.clauseKeys = append(lr.clauseKeys, key)
	}
}

func (lr *loopRun) setupPrompt() {
	// In PromptTools mode, inject tool descriptions into the system
	// prompt instead of using native function calling. Everything stays
	// as plain text — tool calls are parsed from <tool_call> tags and
	// results are sent back as regular user messages.
	lr.systemPrompt = lr.cfg.SystemPrompt
	if lr.cfg.PromptTools && len(lr.tools) > 0 {
		lr.systemPrompt += BuildToolPrompt(lr.tools)
		lr.clauseKeys = append(lr.clauseKeys, "framework.tools_directive")
	}
	if emitDisciplinePrompt && len(lr.tools) > 0 {
		lr.addClause(prompts.AnsweringRoundsKey, prompts.AnsweringRoundsClause())
	}
	// Grounding discipline — every tool-using loop. The failure mode: the
	// model retrieves real sources, then embellishes with specifics pulled
	// from memory — a wrong statute/instruction number, a plausible-but-
	// fabricated case cite, an invented figure, date, or quote. For a
	// research / legal / medical assistant that's the dangerous one: a
	// fabricated citation that reads as authoritative. Pin specifics to
	// what was actually retrieved or provided.
	if len(lr.tools) > 0 {
		// Framing header — names the blocks below as ONE grounding contract seen
		// from several angles, so they read as a coherent message rather than a
		// pile of rules. Kept to a single short line on purpose: the per-block
		// salience the 27B needs comes from the blunt standalone blocks, NOT from
		// a long preamble, so the frame stays out of their way.
		lr.addClause(prompts.GroundingContractKey, prompts.GroundingContractClause())
		// Capability-first — tool SELECTION, distinct from Grounding (which
		// governs specifics once you have results). The failure mode: the
		// model answers a recency-sensitive or job-specific question straight
		// from training when a tool or agent for it is sitting in the catalog
		// (e.g. reciting "the news" from priors instead of searching). The
		// tool-vs-agent choice is a SIZING decision (how big is the job), kept
		// separate from the trust decision (where the answer comes from) so
		// this doesn't push the model away from delegating real multi-step
		// work. Written without em-dashes so it doesn't model the tic.
		lr.addClause(prompts.CapabilityFirstKey, prompts.CapabilityFirstClause())
		lr.addClause(prompts.GroundingKey, prompts.GroundingClause())
		// Action grounding — the sibling of Grounding aimed at ACTIONS rather than
		// facts. The failure mode (observed live: an agent in a group chat said "I
		// sent a meme" with zero tool calls): the model narrates a completed action
		// it never performed, because its reply text feels like doing the thing.
		// Written without em-dashes (house style).
		lr.addClause(prompts.ActionsKey, prompts.ActionsClause())
		// Stated where [Actions:] is stated, because they are the same mistake
		// from opposite ends: that one stops an agent claiming work it never did,
		// this one stops it withholding work it actually did.
		lr.addClause(prompts.ToolVisibilityKey, prompts.ToolVisibilityClause())
		// Contradiction discipline — the sibling of Grounding aimed the OPPOSITE
		// direction: not the model's own volunteered specifics, but the model
		// DISPUTING a fact the user stated or assumed. The failure mode is a
		// confident "well, actually that's wrong" sourced from stale training,
		// which is worse than the user's claim when the priors are out of date.
		// Scoped to CONTRADICTING the user (not to general answers) so it does
		// not add hedging to the decisive-language posture elsewhere. Written
		// without em-dashes (house style).
		lr.addClause(prompts.DisagreeingKey, prompts.DisagreeingClause())
		lr.addClause(prompts.NumbersKey, prompts.NumbersClause())
		// False-precision prevention — the behavioral half of the same concern
		// the [Numbers] / [Grounding] blocks address: stop the model inventing a
		// percentage / fraction / dollar figure for rhetorical weight. This is
		// prompt-only by design; the mechanical re-prompt gate that used to back
		// it was removed because its verbatim-corpus match couldn't tell a
		// correctly COMPUTED figure ("$120 over MSRP") from a fabricated one and
		// false-flagged the model's own arithmetic.
		lr.addClause(prompts.NoFalsePrecisionKey, prompts.NoFalsePrecisionClause())
		// Volatile facts — a blunt, standalone restatement of the Grounding rule
		// aimed at the specifics the worker keeps fabricating. Already covered
		// inside Grounding + Capability-first + Numbers, but buried in long
		// paragraphs those clauses don't land on a 27B: a price (or a "current
		// version", a "current CEO") reads to the model like a stable fact it
		// "knows", so the recency reflex that makes it search for "news" never
		// fires. A short categorical block ("you do NOT know it") is what
		// actually moves the worker to call the tool, same pattern as the
		// Actions block. Lead with PRICES (the confirmed offender), name a tight
		// cluster of the other things it misclassifies as stable, then anchor on
		// the underlying test so it generalizes past the list rather than
		// treating "not listed" as safe to recall. Kept short on purpose: a long
		// enumeration re-buries the rule and loses the salience that makes it
		// work. Written without em-dashes. The lookup clause names the web tools
		// only when the catalog actually has one; a private/offline agent gets
		// the say-you-can't-confirm branch instead of being told to call a tool
		// another layer stripped.
		hasWebTool := false
		for _, td := range lr.tools {
			if n := td.Tool.Name; n == "web_search" || n == "fetch_url" || n == "browse_page" {
				hasWebTool = true
				break
			}
		}
		lr.addClause(prompts.VolatileFactsKey, prompts.VolatileFactsClause(hasWebTool))
	}
	// Global rules — the deployment's own, ahead of everything else this
	// section adds. Injected here and ONLY here: the per-namespace rules panels
	// display them so a person can see the whole set on one screen, and a
	// display is not a second injection.
	lr.addClause(prompts.GlobalRulesKey, prompts.GlobalRulesClause())
	// Output style — universal (every reply, with or without tools).
	// Suppresses persistent LLM lexical/punctuation tics the user flagged.
	//
	// Assembled from the style-rule LIST rather than written here, because a
	// style rule is a sentence an operator adds or drops when they notice a tic,
	// not a paragraph encoding an incident. The two shipped rules also carry
	// transforms (StripFillerClassic, StripEmDashes) bound to the same keys, so
	// turning one off on the Prompts page stops the sentence AND the transform
	// together rather than leaving the prompt asking for something the code no
	// longer does. Empty when every rule is off, and then nothing is appended.
	lr.addClause(prompts.StyleKey, prompts.StyleClause())
	// Secret handling — universal. Stops any agent from soliciting API
	// credentials in chat (the OPNsense-controller failure mode); auth is
	// injected server-side via Admin > APIs credentials, so the secret never
	// belongs in the conversation or the tool-call logs.
	lr.addClause(prompts.SecretsKey, prompts.SecretsClause())

	// Internal-marker convention: gives the model a sanctioned, always-scrubbed
	// wrapper for internal-only notes AND tells it not to type bare delivery
	// markers into user-facing text (the textutil.StripMetaTags safety net catches both).
	lr.addClause(prompts.InternalMarkersKey, prompts.InternalMarkersClause())
	// Round-budget awareness — let the LLM know how many rounds it has
	// for the whole turn so it can pace itself (vs. exploring as if
	// budget were infinite, then getting truncated). Only emit for
	// sessions with meaningful budgets; short fixed loops (judges,
	// classifiers) don't need the noise.
	if lr.maxRounds >= 10 {
		lr.addClause(prompts.RoundBudgetKey, prompts.RoundBudgetClause(lr.maxRounds))
	}
}

// emitDiag breadcrumbs a silent correction into the app's session-diag
// trail (nil-safe). Kept here so every correction guard below records the
// framework decision it just made — see AgentLoopConfig.OnDiag.
func (lr *loopRun) emitDiag(kind, detail string) {
	if lr.cfg.OnDiag != nil {
		lr.cfg.OnDiag(kind, detail)
	}
}

// noteUncorrected breadcrumbs a guard that spotted its problem and has run
// out of budget to do anything about it. Once per kind, and a no-op until
// then. Without it an exhausted guard is indistinguishable from one that
// never fired: the turn ships the flaw and the trail says nothing happened.
func (lr *loopRun) noteUncorrected(kind, detail string) {
	if lr.corrections.exhausted(kind) {
		Debug("[agent_loop] %s detected again but its correction budget is spent — letting it stand", kind)
		lr.emitDiag(kind+"-uncorrected", detail)
	}
}

// settleRound finalizes the just-rejected round's streamed text before a
// correction re-prompts (nil-safe), so the retry starts in a fresh bubble
// instead of concatenating into an orphaned one — see
// AgentLoopConfig.SettleRound. Every correction guard that `continue`s on a
// round that may have streamed content calls this first.
func (lr *loopRun) settleRound() {
	if lr.cfg.SettleRound != nil {
		lr.cfg.SettleRound()
	}
}

// retractRound discards the current round's streamed bubble on a guardrail
// block (never persisted/delivered). Falls back to settleRound when the app
// wired no retract — that's concatenation-safe but still persists the bubble,
// so only paths that set RetractRound get the leak-proof behavior.
func (lr *loopRun) retractRound() {
	if lr.cfg.RetractRound != nil {
		lr.cfg.RetractRound()
		return
	}
	lr.settleRound()
}

// replaceBlockedDraft overwrites the most-recent assistant turn's content
// after a guardrail blocks it. The draft is appended to history the moment
// the model produces it (see "Record assistant response" below), BEFORE the
// pre_output/periodic gate runs — so a blocked reply is already in the slice
// the caller will persist to the session and may deliver to the user. Left
// intact it leaks the very content the guardrail protects (observed: a salary
// figure blocked by pre_output was still recorded and delivered because the
// block only re-prompts, it doesn't retract). Overwrite in place rather than
// popping the turn, so user/assistant alternation survives. `with` is the
// redaction placeholder on a re-prompt, or the safe fallback reply when the
// budget is spent and we return a substitute.
func (lr *loopRun) replaceBlockedDraft(with string) {
	for i := len(lr.history) - 1; i >= 0; i-- {
		if lr.history[i].Role == "assistant" {
			lr.history[i].Content = with
			lr.history[i].Reasoning = ""
			lr.history[i].ToolCalls = nil
			return
		}
	}
}

func (lr *loopRun) recomputeThresholds() {
	remaining := lr.maxRounds - lr.baseRound
	if remaining >= 5 {
		lr.wrapUpThreshold = lr.baseRound + (remaining*4)/5
	} else {
		lr.wrapUpThreshold = 0
	}
	if remaining >= 10 {
		lr.midpointThreshold = lr.baseRound + remaining/2
	} else {
		lr.midpointThreshold = 0
	}
}

func (lr *loopRun) setupState() {
	lr.digestBuilt, lr.digestSent = false, false
	lr.prevHadToolCalls = false
	// Rations the loop's silent re-prompts. Per KIND, not one pot — see
	// correctionBudget for why that mattered.
	lr.corrections = newCorrectionBudget()
	lr.guardrailOutputCorrections = 0 // pre_output revise passes used this turn
	// judgedNarration records the interim prose the periodic guard has already
	// ruled on this turn, so a model that repeats a lead-in verbatim doesn't pay
	// for a second identical warden call. Keyed by the prose itself: the check is
	// a pure function of (rules, text), so the same text cannot get a different
	// answer. This is the only concession to cost — the guard no longer skips
	// rounds, so dedup is what keeps a repetitive turn from paying twice.
	//
	// It caches a pass, including a pass the app returned because its warden call
	// FAILED (the app's policy is to fail open, loudly, and it owns its own retry).
	// So identical text is not re-attempted later in the turn. That follows the
	// app's decision rather than second-guessing it; core cannot tell an allow
	// from an unavailable check through this interface, and inventing a retry here
	// would duplicate the one the app already does.
	lr.judgedNarration = map[string]bool{}
	lr.skippedInterimGuard = false // logged once per turn, not once per round

	// toolFiredThisTurn tracks whether ANY tool dispatched at any
	// point in this turn (any round). The action-promise correction
	// keys off the CURRENT round's tool calls and would otherwise
	// fire on legitimate "On it." / "Standby." acknowledgments
	// emitted in a follow-up round AFTER a dispatch tool already
	// fired the actual work — common with async dispatch flows
	// (e.g. phantom's dispatch_agent: round 1 calls the tool,
	// round 2 just says "On it." to the user). Once a tool has
	// fired this turn the action promise has already been satisfied;
	// don't re-prompt the model into doing it again.
	lr.toolFiredThisTurn = false

	// Soft-pacing checkpoints — midpoint nudge (50% of remaining
	// budget) and wrap-up warning (80% of remaining). Both compute
	// off a rebase point that defaults to 0 (= "remaining" is
	// MaxRounds) and shifts whenever OnRoundReset returns true. That
	// gives apps a way to say "the LLM just advanced to a new logical
	// phase — give it a fresh pacing window from here." Hard MaxRounds
	// cap is unaffected.
	lr.wrapUpWarningFired = false
	lr.midpointNudgeFired = false
	lr.baseRound = 0
	lr.recomputeThresholds()

	// Failure-streak pivot. Tracks consecutive rounds where EVERY tool
	// call this round returned IsError=true (the framework-level
	// signal). When the streak hits failureStreakThreshold, inject a
	// one-shot pivot nudge telling the model to stop iterating on the
	// failing approach and try something fundamentally different (or
	// stop and report what didn't work). Streak resets the moment ANY
	// tool call in a round succeeds. Catches the "20 variants of the
	// same broken command" failure mode without baking in app-specific
	// markers (apps that want richer detection — e.g. servitor's
	// non-zero-exit-as-soft-failure — layer their own counter on top).
	lr.failureStreak = 0
	lr.failureStreakWarned = false
	// cumulativeToolErrors tracks tool errors across the WHOLE loop, not
	// just the current round. Used to catch the "give up with errors
	// pending" pattern: model emits empty content + finish=stop after a
	// run with unresolved tool errors, even though budget remains.
	// Increments after each round's native-tool batch (toolErrors local
	// var), and the no-tool-call exit path checks it before letting the
	// loop terminate — injecting a "fix the errors, don't summarize"
	// nudge instead of letting the rescue path paper over the bailout.
	lr.cumulativeToolErrors = 0
	lr.lastToolError = ""         // most recent failure text, for the turn judge
	lr.turnToolCalls = []string{} // every tool this turn ran, in order, duplicates kept

	lr.repeatFail = map[string]int{}
	// Carry in what this standing work already learned, before history is
	// consulted: a scheduled fire's history has no tool results to learn from.
	loadFailureMemory(lr.cfg.FailureMemoryKey, lr.repeatFail, repeatFailLimit-1)
	// Identical-repeat guard. repeatFail above only counts ERRORS (it resets
	// on success), so a model that re-issues the same call and keeps getting a
	// valid-but-useless SAME result never trips it (observed live: inspect_run
	// polled on one run id ~30x in a single turn, every call succeeding, zero
	// progress, until the round cap). repeatSame counts consecutive BYTE-
	// IDENTICAL results per signature regardless of error status; a changed
	// result (genuine polling) resets it, so real polling is never penalized.
	// sentThisTurn keys the recipients a side-effecting send has already reached
	// this turn (see SendGuardKey). A second send to the same recipient is held,
	// not delivered.
	lr.sentThisTurn = map[string]bool{}
	// shakeoutNextRound: a repeat guard fired last round, so the model is
	// provably stuck re-emitting the same call. The NEXT LLM call gets a
	// one-shot temperature bump (shakeoutTemperature) to jolt it off the
	// fixed point — near-greedy sampling on near-identical context is what
	// makes these orbits stable. One round only, then back to route defaults;
	// zero cost in the steady state (unlike a global penalty sampler, which
	// measured ~23% tok/s on this rig).
	lr.shakeoutNextRound = false
	// guardrailQuietNextRound: a guardrail blocked last round, so the NEXT LLM
	// call runs with thinking OFF.
	//
	// A block is the one round where deliberation reliably goes wrong. The model
	// is handed a refusal it did not expect, about a mechanism it is told not to
	// name, and asked to carry on — and it reasons at length about the system it
	// is inside instead of answering. That is the failure COLLAPSE-DIAG was
	// written to detect: a huge reasoning block, almost no output, no tool call.
	// Trimming the block messages from eight imperatives to three helped and did
	// not fix it, because the deliberation is caused by the SITUATION, not only
	// by the wording.
	//
	// There is nothing to think about anyway. The block message says what
	// happened, that the call did not run, and not to reach the same end another
	// way. Acting on it is a short step; the reasoning budget buys nothing and
	// costs the user the wait. Same lever the shake-out uses, same one-shot
	// scope — one round, then back to route defaults.
	lr.guardrailQuietNextRound = false
	lr.repeatSame = map[string]int{}
	lr.lastToolContent = map[string]string{}
	// Failure-SHAPE guard. Both guards above key on the call SIGNATURE
	// (tool name + exact args), which a model defeats without meaning to
	// by varying an argument slightly between attempts. Observed live: a
	// tool returning "Failed to create calendar" eight times across four
	// signatures (two date ranges × two tools) while a second wall,
	// "agent X has no attached tools to run", came back nine more times —
	// none of it consecutive-identical, so nothing tripped and the turn
	// ground on. The all-tools-failed streak below missed it too: the
	// rounds were MIXED (a recall or a probe succeeded alongside the
	// failures), which resets that counter every time.
	//
	// What never changed was the failure TEXT. Counting normalized error
	// shapes across the turn, regardless of which call produced them,
	// catches the wall the other three guards walk past.
	lr.errShapeCount = map[string]int{}
	lr.errShapeNudged = map[string]bool{}
	// toolFailShapes records which failure shapes each TOOL produced this
	// turn, so a later success of that tool can retire its stale failure
	// residue from the context (retireResolvedFailureResults).
	lr.toolFailShapes = map[string]map[string]bool{}
	// Seed the guard from prior-turn history so a fixation that spans
	// SEPARATE user turns is caught. repeatFail is otherwise turn-local, so
	// a model that re-issues the SAME wrong+erroring call every turn resets
	// each turn and never trips (observed live: an agent called an unrelated
	// tool with identical args, erroring, across five user turns). The
	// incoming `messages` already carry prior tool calls + their error
	// results (toLLMMessages reconstructs them across turns), so replaying
	// that record through the SAME increment-on-error / reset-on-success
	// rule pre-arms the guard.
	seedRepeatFailFromHistory(lr.messages, lr.repeatFail)

	// Wrap-up grace runway. Rather than hard-stopping at the cap (which
	// strips tools and makes some models emit their intended tool call as
	// TEXT to compensate — a garbled final round), we keep tools available
	// and give the model a bounded runway to land the turn. hardStop is the
	// last allowed round once wrap-up begins (-1 until the cap is hit).
	lr.graceRounds = lr.cfg.GraceRounds
	if lr.graceRounds == 0 && lr.maxRounds >= 10 {
		lr.graceRounds = 5 // default runway for real agent turns; short fixed loops opt out
	}
	if lr.graceRounds < 0 {
		lr.graceRounds = 0
	}
	lr.hardStop = -1
	// Wedge break-out. The loop-guard above blocks a repeated failing call, but a
	// small model may keep RE-EMITTING that identical blocked call every round
	// (often as a TEXT/reasoning tool call), ignoring the STOP directive and
	// making zero progress. Counting rounds that were nothing-but-a-blocked-call,
	// once past this small limit we stop looping and force a clean final answer
	// rather than grinding to the round cap. forceFinal drives the post-loop rescue.
	lr.guardBlockedStreak = 0
	lr.forceFinal = false

	// synthesizedFrom holds the model's own text for a round whose tool
	// call was READ OUT OF that text rather than emitted structurally.
	// If the loop-guard then blocks the synthesized call, the wedge would
	// regenerate an answer we already have — so it returns this instead.
	// Reset each round and cleared the moment a real tool executes, so it
	// can only ever short-circuit a round that did no actual work.
	lr.synthesizedFrom = ""

	// Lead-tier spend guard. Escalation to lead is decided per ROUTE (an
	// agent's stage resolves to lead and every round of its turn goes
	// there), so a turn that goes badly spends frontier tokens on every
	// round of the flail — and because the whole history is resent each
	// round, the rounds get MORE expensive as they get less productive.
	// Observed live: a 93.6k-token round on lead that produced 141 tokens
	// of "let me try that again."
	//
	// leadTokens accumulates what the lead tier actually served this turn
	// (resp.Tier is authoritative on both the streaming and non-streaming
	// paths). Past the budget the turn finishes on the worker: the work
	// still completes, it just stops costing frontier rates. Same response
	// to a no-progress failure-shape streak — a turn that keeps hitting an
	// identical wall has stopped being worth the better model.
	lr.leadTokens = 0
	lr.deescalated = ""

	// keep_going spin guard. keep_going is a pure "run another round" signal
	// with no side effect — meant for "I'm about to act, give me one more
	// round." Because it IS a tool call, it sets toolFiredThisTurn and thereby
	// SUPPRESSES the action-promise correction below, so a model can promise
	// "I'll call the real tool next" every round and never act. Counting
	// consecutive rounds whose ONLY tool call(s) were keep_going, we escalate
	// the nudge and then force a clean final answer rather than let it spin.
	// (Observed live: 8+ keep_going calls across two turns, ~2.5 min, the
	// actual tool never called.)
	lr.keepGoingStreak = 0
}

func (lr *loopRun) finish() (*Response, []Message, error) {
	if lr.lastResp != nil && strings.TrimSpace(lr.lastResp.Content) == "" {
		floor := len(lr.history) - rescueLookback
		if floor < 0 {
			floor = 0
		}
		for i := len(lr.history) - 1; i >= floor; i-- {
			m := lr.history[i]
			if m.Role == "assistant" && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
				Debug("[agent_loop] rescued empty final response; using last non-empty assistant turn (history[%d])", i)
				lr.lastResp = &Response{Content: m.Content}
				break
			}
		}
	}

	// Last-ditch rescue: if we still have empty content after the
	// lookback scan, do ONE bonus LLM call instructing the model to
	// produce a final answer NOW from what's already in history. No
	// tools available on this call — content-only forced. Catches the
	// "stuck in tool-call thrashing, hit MaxRounds with nothing to
	// show the user" failure that the lookback rescue can't help
	// with (when there's no clean assistant content anywhere recent).
	//
	// ALSO fire when the last completed round CALLED TOOLS (lastRoundToolCalled),
	// even though its content is non-empty. Reaching this post-loop point always
	// means an abnormal exit (the natural "no more tool calls, here's my answer"
	// completion returns from INSIDE the loop) — so if the budget ran out while
	// the model was still tool-calling, whatever text it emitted that round is
	// narration alongside the call ("Let me get the full details to give you the
	// steps."), not a synthesis. Without this, that intent-stub is promoted to the
	// final answer and the turn ends looking done while the actual answer was never
	// written — even though the tool results it needs are already in history
	// (dispatch happens before the next round's top-of-loop break). Structural, not
	// phrase-matched: the tell is "last round was still calling tools", not any
	// wording. The forced call below has the retrieved data on hand and synthesizes
	// the real answer from it.
	lr.lastRoundToolCalled = lr.lastResp != nil && len(lr.lastResp.ToolCalls) > 0
	if lr.lastResp != nil && (lr.forceFinal || lr.lastRoundToolCalled || strings.TrimSpace(lr.lastResp.Content) == "") && lr.T.LLM != nil {
		if lr.forceFinal {
			Debug("[agent_loop] wedge break — issuing a forced-final-answer call with no tools")
		} else if lr.lastRoundToolCalled {
			Debug("[agent_loop] budget exhausted mid-tool-call (last content is narration, not a synthesis) — issuing a forced-final-answer call with no tools")
		} else {
			Debug("[agent_loop] empty after lookback rescue — issuing a forced-final-answer call with no tools")
		}
		wrapHistory := append([]Message{}, lr.history...)
		wrapHistory = append(wrapHistory, Message{
			Role:    "user",
			Content: "Stop calling tools now and produce your final answer for the user from whatever you've gathered so far — even if incomplete, summarize what you found and what you tried, and if something didn't work, say so plainly. Just text, no tool calls.",
		})
		// No-tools, no-think final call so the model has nothing to
		// chase — must produce text. Inherit RouteKey for telemetry.
		var wrapOpts []ChatOption
		wrapOpts = append(wrapOpts, WithSystemPrompt(lr.systemPrompt))
		wrapOpts = append(wrapOpts, WithoutAutoDate()) // date is on the user turn, not the system prompt
		f := false
		wrapOpts = append(wrapOpts, WithThink(f))
		if lr.cfg.RouteKey != "" {
			wrapOpts = append(wrapOpts, WithRouteKey(lr.cfg.RouteKey))
		}
		if forced, err := lr.T.LLM.Chat(lr.ctx, wrapHistory, wrapOpts...); err == nil && forced != nil {
			// Thinking workers often answer entirely in the reasoning
			// channel with empty content — promote it rather than discard
			// it, same as the in-loop reasoning→content promotion. Without
			// this the rescue "succeeds" but hands back empty, and the
			// caller shows the user nothing.
			if strings.TrimSpace(forced.Content) == "" && strings.TrimSpace(forced.Reasoning) != "" {
				forced.Content = forced.Reasoning
			}
			if strings.TrimSpace(forced.Content) != "" {
				// The worker sometimes ignores "just text" and emits a tool call as
				// PROSE. With no tools attached it can't run, and the raw <tool_call>
				// XML would surface as the answer (observed: a scheduled agent's
				// "card" was the send_message XML — the send never executed). Detect
				// it, name what it was about to do, and replace with a clear
				// "ran out of steps" note so the result reads as incomplete, not
				// gibberish.
				// First-person, user-safe wording: this string can surface as a
				// LIVE CHAT reply, not just a scheduled report card, and telling a
				// chat user to "raise this agent's worker-round limit" is operator
				// language in the wrong mouth (observed: Barebones answered a
				// simple question with exactly that). Operators still get the
				// signal via HitRoundCap + the rounds_used exit log.
				if strings.Contains(forced.Content, "<function=") || strings.Contains(forced.Content, "<tool_call>") {
					if name, _ := parseFunctionTagToolCall(forced.Content); strings.TrimSpace(name) != "" {
						forced.Content = "I ran out of steps before finishing — I was about to call \"" + name + "\" but it did NOT run. Say \"continue\" and I'll pick up where I left off, or narrow the request."
					} else {
						forced.Content = "I ran out of steps before finishing — an action I was about to take did NOT run. Say \"continue\" and I'll pick up where I left off, or narrow the request."
					}
				}
				lr.lastResp = forced
				Debug("[agent_loop] forced-final-answer rescue produced %d chars", len(forced.Content))
			} else {
				Debug("[agent_loop] forced-final-answer rescue produced no usable content")
			}
		} else {
			Debug("[agent_loop] forced-final-answer rescue produced no usable content (err=%v)", err)
		}
	}

	// Reaching here means the for-loop ran to exhaustion — the round budget
	// (MaxRounds + grace) was spent without a natural final answer. Flag it so
	// callers can distinguish "done" from "out of rounds" and continue if the
	// work is genuinely unfinished. (Natural completions return from inside
	// the loop above and never reach this point.)
	if lr.lastResp != nil {
		lr.lastResp.HitRoundCap = true
	}
	return lr.lastResp, lr.history, nil
}

func (lr *loopRun) roundHead() loopAction {
	// Bail immediately on cancellation so the loop doesn't burn another
	// LLM call (or tool execution) after the session was aborted. Tool
	// handlers that don't check ctx themselves can otherwise hold the
	// loop open for a tick after cancel().
	if err := lr.ctx.Err(); err != nil {
		return lr.exit(lr.lastResp, lr.history, err)
	}
	// Round-start breadcrumb — pair with the existing "round N:
	// content=..." post-LLM log to bracket each round. When a
	// hang lands between rounds (after a tool returned, before
	// the next LLM call), we see "starting" without a matching
	// "calling LLM" → narrows the wedge to compaction / option
	// assembly / injection drain. Cheap, fires once per iteration.
	Debug("[agent_loop] round %d: starting (history=%d msgs)", lr.round, len(lr.history))
	if lr.round == 1 {
		logPromptFloor(lr.cfg, lr.systemPrompt, lr.history)
	}
	// BREADCRUMB: round-top reached. Mirrors the Debug above at
	// Log level — paired with the "dispatch complete" breadcrumb
	// after tools, the gap between the two pinpoints whether
	// the hang is in iteration restart (Debug fires) or pre-
	// iteration bookkeeping (Debug does NOT fire).
	Log("[agent_loop] round %d: top of iteration", lr.round)
	// Per-round: only a call synthesized THIS round may short-circuit
	// the wedge. A stale value from an earlier round would let a real
	// blocked call return someone else's text.
	lr.synthesizedFrom = ""
	// Soft cap hook — apps that want a budget cap depending on
	// runtime state (e.g. orchestrate's explorer-mode flag) wire
	// StopRound. Called EXACTLY ONCE per round (it has side effects,
	// e.g. orchestrate increments its round counter here).
	lr.rs.stop = lr.cfg.StopRound != nil && lr.cfg.StopRound()
	if lr.graceRounds <= 0 {
		// No runway (short fixed loops / opted out): original behavior —
		// a true StopRound terminates immediately.
		if lr.rs.stop {
			return actBreak
		}
	} else {
		// Begin the wrap-up runway the first time the cap is reached,
		// then hard-stop once it's spent. Tools stay available the whole
		// time (see the tool-offer gate below), so the model lands the
		// turn instead of getting them stripped mid-intent. The escalating
		// directive a few lines down is what bounds the runway; the forced
		// no-tools rescue after the loop is the final backstop.
		if lr.rs.stop || lr.round >= lr.maxRounds {
			if lr.hardStop < 0 {
				lr.hardStop = lr.round + lr.graceRounds
				if lr.hardStop > lr.maxRounds+lr.graceRounds {
					lr.hardStop = lr.maxRounds + lr.graceRounds
				}
				Debug("[agent_loop] round %d: cap reached — entering wrap-up grace, hard stop at round %d", lr.round, lr.hardStop)
			}
		} else if lr.hardStop >= 0 {
			// The cap lifted again (e.g. the model flipped orchestrate's
			// explorer mode mid-grace, so StopRound now returns false).
			// Cancel wrap-up and resume normal running until the next cap.
			Debug("[agent_loop] round %d: cap lifted — cancelling wrap-up grace", lr.round)
			lr.hardStop = -1
		}
		if lr.hardStop >= 0 && lr.round > lr.hardStop {
			return actBreak
		}
	}
	// Phase-reset hook — when OnRoundReset returns true, rebase
	// the soft-pacing thresholds so the LLM gets a fresh
	// midpoint + wrap-up window from the remaining budget. App
	// signals "the LLM just crossed a logical phase boundary"
	// (e.g. servitor advancing to a new plan step). Hard
	// MaxRounds cap stays in place — only the soft pacing
	// resets. Also injects a brief status so the model sees the
	// fresh-budget framing rather than silently getting new
	// thresholds.
	if lr.cfg.OnRoundReset != nil && lr.cfg.OnRoundReset() {
		lr.baseRound = lr.round - 1 // remaining counts from this round forward
		remaining := lr.maxRounds - lr.baseRound
		Debug("[agent_loop] round reset at round %d/%d — %d rounds remain", lr.round, lr.maxRounds, remaining)
		lr.wrapUpWarningFired = false
		lr.midpointNudgeFired = false
		lr.failureStreak = 0
		lr.failureStreakWarned = false
		lr.recomputeThresholds()
		if remaining >= 5 {
			lr.history = append(lr.history, Message{
				Role: "user",
				Content: fmt.Sprintf(
					frameworkNoticeTag+"Fresh budget window: you have %d rounds for this phase. The framework will nudge you at the halfway mark and again near the cap — pace this phase as if starting clean. (Hard MaxRounds cap is still %d total for the turn.)",
					remaining, lr.maxRounds),
			})
		}
	}
	// Per-round content (budget/pacing notes, status reminders).
	// May return content every round; that's fine here — it's only
	// called at round start, never in the pre-finalize re-check.
	if lr.cfg.OnRoundStart != nil {
		if injected := lr.cfg.OnRoundStart(); len(injected) > 0 {
			lr.history = append(lr.history, injected...)
		}
	}
	// Drain any mid-flight injections (user notes interjected into a
	// running orchestrator). Separate from OnRoundStart because the
	// loop re-calls THIS one before finalizing — so it must empty its
	// queue and return nil when nothing's pending.
	if lr.cfg.InjectionDrain != nil {
		if injected := lr.cfg.InjectionDrain(); len(injected) > 0 {
			lr.history = append(lr.history, injected...)
		}
	}
	// Midpoint nudge — at 50% of MaxRounds, drop a status reminder
	// so the model can recalibrate before the wrap-up pressure
	// kicks in. Fires once per session (midpointNudgeFired flag).
	if !lr.midpointNudgeFired && lr.midpointThreshold > 0 && lr.round >= lr.midpointThreshold {
		Debug("[agent_loop] midpoint nudge at round %d/%d (base=%d)", lr.round, lr.maxRounds, lr.baseRound)
		phaseRound := lr.round - lr.baseRound
		phaseTotal := lr.maxRounds - lr.baseRound
		lr.history = append(lr.history, Message{
			Role: "user",
			Content: fmt.Sprintf(
				frameworkNoticeTag+"Halfway checkpoint: you're at round %d of %d for this phase. Taking stock is worth a moment — if you're making real progress, keep going; if not, consider switching tools, trying a different angle, or asking the user for clarification before the remaining budget gets spent.",
				phaseRound, phaseTotal),
		})
		lr.midpointNudgeFired = true
	}
	// Wrap-up warning — when the loop crosses 80% of MaxRounds,
	// inject a one-shot user message telling the model it's near
	// the budget cap and to produce a final answer NOW with what
	// it has, rather than continuing to explore. Without this,
	// long-running flows that overrun the cap return whatever was
	// last accumulated, which often looks "incomplete" to users.
	// Fires once per session (wrapUpWarningFired flag).
	//
	// When PendingWorkFn is wired and reports remaining authorized
	// items (e.g. plan steps not yet done), swap the message to one
	// that distinguishes "wind down THIS item" from "wind down the
	// whole task." Without that, a plan-driven worker reads "Do NOT
	// start new investigations" as license to skip pending plan
	// steps and write a summary instead.
	if !lr.wrapUpWarningFired && lr.wrapUpThreshold > 0 && lr.round >= lr.wrapUpThreshold {
		remaining := lr.maxRounds - lr.round + 1
		Debug("[agent_loop] wrap-up warning at round %d/%d (%d remaining)", lr.round, lr.maxRounds, remaining)
		pending := 0
		if lr.cfg.PendingWorkFn != nil {
			pending = lr.cfg.PendingWorkFn()
		}
		var wrapUpMsg string
		if pending > 0 {
			wrapUpMsg = fmt.Sprintf(
				frameworkNoticeTag+"Budget checkpoint: %d rounds left of a %d-round budget, and %d authorized work item(s) still remain on your list. Finish the current item cleanly with a real result, then move to the next one — do NOT skip the remaining items and do NOT start new exploration outside the list. If you genuinely can't complete an item with the rounds remaining, mark it as such and continue.",
				remaining, lr.maxRounds, pending)
		} else {
			wrapUpMsg = fmt.Sprintf(
				frameworkNoticeTag+"You have %d rounds left of a %d-round budget. Stop exploring and produce a final answer NOW with what you've gathered. If the task isn't complete, summarize what you found, what you tried, and what's still open. Do NOT start new investigations — wind down cleanly.",
				remaining, lr.maxRounds)
		}
		lr.history = append(lr.history, Message{Role: "user", Content: wrapUpMsg})
		lr.wrapUpWarningFired = true
	}
	// Wrap-up grace directive — once on the runway, tell the model
	// (escalating) to finish and answer. Tools stay available, so this
	// message is what makes it land rather than thrash; it's the most
	// recent thing the model sees before the call.
	if lr.hardStop >= 0 {
		left := lr.hardStop - lr.round + 1
		var msg string
		if left <= 1 {
			msg = frameworkNoticeTag + "[ROUND LIMIT — HARD STOP after this round. Produce your final answer NOW from what you already have. Start no new work; make a tool call only if it is the single step needed to finish, then answer.]"
		} else {
			msg = fmt.Sprintf(frameworkNoticeTag+"[Round limit reached — wrap up and give your final answer. %d round(s) left before a hard stop. Finish in-flight work only; start nothing new.]", left)
		}
		lr.history = append(lr.history, Message{Role: "user", Content: msg})
	}
	// Pull dynamic tools (e.g. temp tools defined by the LLM via
	// create_temp_tool earlier this loop) and merge into the catalog
	// for this round. Filtered through the same caps gate as static
	// tools so the LLM can't elevate via runtime registration.
	if lr.cfg.DynamicTools != nil || lr.cfg.RoundToolFilter != nil {
		active := make([]AgentToolDef, 0, len(lr.tools)+4)
		active = append(active, lr.tools...)
		if lr.cfg.DynamicTools != nil {
			active = append(active, lr.filterCaps(lr.cfg.DynamicTools())...)
		}
		// Per-round suppression: drop any tool RoundToolFilter rejects
		// (e.g. plan_set after it loops). Filtered in place — active is
		// freshly allocated this round, so reusing its backing array is
		// safe and the dispatch maps below see only the kept set.
		if lr.cfg.RoundToolFilter != nil {
			kept := active[:0]
			for _, td := range active {
				if lr.cfg.RoundToolFilter(td.Tool.Name) {
					kept = append(kept, td)
				}
			}
			active = kept
		}
		lr.rebuildToolMaps(active)
	}
	return actNone
}

func (lr *loopRun) prepareCall() loopAction {
	// Compact history if it's about to push the round past the window
	// (budget-based), OR if the LLM asked for it via RoundCompactNow
	// (forced, aggressive). Elides old tool-result bodies in place so
	// this and later rounds stay under the window. Runs after
	// round-start injections so it sees the full assembled history.
	lr.rs.forceCompact = lr.cfg.RoundCompactNow != nil && lr.cfg.RoundCompactNow()
	// effectiveContextSize, not cfg.ContextSize: if this window has recently
	// refused a prompt, the next one has to be built against what the
	// worker actually granted. Otherwise every turn rebuilds to the claim
	// and rediscovers the wall a round later.
	lr.rs.window = effectiveContextSize(lr.cfg.ContextSize)
	lr.rs.budget = compactHistory(lr.history, lr.systemPrompt, lr.rs.window, lr.rs.forceCompact)
	// One digest per TURN, measured on the prompt of the first round after
	// compaction has run — the prompt that is actually sent. Later rounds
	// grow history with results the model asked for; round 1 is the part a
	// person can act on (a system prompt, a tool catalog, and a thread they
	// can trim). Provider numbers are filled in below, once the response
	// makes them real.
	if !lr.digestBuilt {
		lr.digest = buildPromptDigest(lr.systemPrompt, lr.clauseKeys, lr.tools, lr.history, lr.rs.window, lr.rs.budget)
		lr.digestBuilt = true
	}
	if lr.cfg.RouteKey != "" {
		if think := RouteThink(lr.cfg.RouteKey); think != nil {
			lr.rs.opts = append(lr.rs.opts, WithThink(*think))
		}
	}
	// Thinking budget (no input-scaling). Resolution, highest priority
	// first:
	//   1. explicit per-loop override (cfg.ThinkBudget, e.g. a per-agent
	//      configured budget)
	//   2. per-route configured budget (admin routing UI) via RouteKey —
	//      symmetric with the RouteThink flag applied just above; without
	//      this the admin's per-route budget is silently ignored in agent
	//      loops (the old input-scaling formula used to mask the gap)
	//   3. the operator-configured global default (client llamacppBudget,
	//      default 4096, also a hard ceiling), applied inside the client
	// See ThinkBudget's doc on AgentLoopConfig.
	if lr.cfg.ThinkBudget > 0 {
		lr.rs.opts = append(lr.rs.opts, WithThinkBudget(lr.cfg.ThinkBudget))
	} else if lr.cfg.RouteKey != "" {
		if rb := RouteThinkBudget(lr.cfg.RouteKey); rb != nil && *rb > 0 {
			lr.rs.opts = append(lr.rs.opts, WithThinkBudget(*rb))
		}
	}
	// If the previous round produced tool calls and ToolRoundOptions are
	// configured, use them instead of ChatOptions for this round.
	lr.rs.roundOpts = lr.cfg.ChatOptions
	if lr.prevHadToolCalls && len(lr.cfg.ToolRoundOptions) > 0 {
		lr.rs.roundOpts = lr.cfg.ToolRoundOptions
	}
	lr.rs.opts = append(lr.rs.opts, lr.rs.roundOpts...)
	// Per-round dynamic override, appended after the static option
	// slices so it wins (e.g. WithThink(false) on the round after a
	// control-tool rejection).
	if lr.cfg.RoundChatOptions != nil {
		lr.rs.opts = append(lr.rs.opts, lr.cfg.RoundChatOptions()...)
	}
	// One-shot sampling shake-out after a repeat-guard trip (see the
	// shakeoutNextRound decl). Appended last so it wins over static opts.
	if lr.shakeoutNextRound {
		lr.shakeoutNextRound = false
		lr.rs.opts = append(lr.rs.opts, WithTemperature(shakeoutTemperature))
		Debug("[agent_loop] shake-out round: one-shot temperature %.2f after a repeat-guard trip", shakeoutTemperature)
	}
	// One-shot thinking-off after a guardrail block (see the
	// guardrailQuietNextRound decl). Appended last so it beats the static
	// option slices and any per-agent think budget.
	if lr.guardrailQuietNextRound {
		lr.guardrailQuietNextRound = false
		lr.rs.opts = append(lr.rs.opts, WithThink(false))
		Debug("[agent_loop] guardrail round: thinking OFF for one round after a block")
	}
	if lr.systemPrompt != "" {
		lr.rs.opts = append(lr.rs.opts, WithSystemPrompt(lr.systemPrompt))
	}
	// Date lives on the latest user turn (stamped above), not the system
	// prompt — keep applyOpts from re-injecting it and poisoning the cache.
	lr.rs.opts = append(lr.rs.opts, WithoutAutoDate())
	if lr.cfg.MaskDebugOutput {
		lr.rs.opts = append(lr.rs.opts, WithMaskDebug())
	}
	// Offer native tools when NOT in PromptTools mode. Grace-enabled
	// loops keep tools available through the wrap-up runway (the
	// escalating directive + post-loop rescue handle the landing, so we
	// never strip — that's what caused models to emit tool-calls as
	// text). Grace-disabled short loops keep the original behavior:
	// no tools on the forced final round.
	lr.rs.offerTools = lr.graceRounds > 0 || lr.round < lr.maxRounds
	if !lr.cfg.PromptTools && len(lr.toolDefs) > 0 && lr.rs.offerTools {
		lr.rs.opts = append(lr.rs.opts, WithTools(lr.toolDefs))
	}
	// Surface reasoning chunks to the caller-supplied handler when set.
	// Fires only on the streaming path; the non-streaming Chat() call
	// returns reasoning only as a single block on Response.Reasoning.
	if lr.cfg.ReasoningStream != nil {
		lr.rs.opts = append(lr.rs.opts, WithReasoningStream(lr.cfg.ReasoningStream))
	}
	return actNone
}

func (lr *loopRun) callModel() loopAction {
	// Pre-call breadcrumb: when an LLM round hangs, we want to know
	// whether the hang is upstream of the LLM call (compaction,
	// injection drain, option assembly) or inside it (waiting on
	// llama.cpp's response). Pairs with the existing "stream
	// completed" log after the call returns: enter-without-exit =
	// LLM-side hang; no-enter = something earlier in the loop wedged.
	// Cheap, fires once per round.
	lr.rs.histChars = 0
	for _, m := range lr.history {
		lr.rs.histChars += len(m.Content)
		for _, tr := range m.ToolResults {
			lr.rs.histChars += len(tr.Content)
		}
	}
	Debug("[agent_loop] round %d: calling LLM (history=%d msgs, ~%d chars)", lr.round, len(lr.history), lr.rs.histChars)
	// BREADCRUMB: about to make the LLM HTTP call. If we see this
	// but no matching "LLM returned" below, the call is hung at
	// the provider — needs a per-call hard timeout or the
	// provider's endpoint is wedged.
	Log("[agent_loop] round %d: → LLM call (history=%d msgs)", lr.round, len(lr.history))
	lr.rs.llmStarted = time.Now()
	// Assert the tool-result adjacency invariant before we hand the history
	// to a provider. Violating it is a hard 400 from the chat template
	// tens of seconds later, with an error that names a line in a Jinja
	// file rather than the message we mis-ordered. Log, don't block: the
	// request may still succeed on a provider with a laxer template, and a
	// guard that kills the turn to prevent a bad turn is the mistake this
	// invariant already caused once.
	if bad := FirstToolOrderViolation(lr.history); bad >= 0 {
		prev := "(start of history)"
		if bad > 0 {
			prev = strconv.Quote(lr.history[bad-1].Role)
		}
		Log("[agent_loop] round %d: WARNING history[%d] carries tool results but follows %s, which has no tool calls — providers reject this ordering (a mid-round correction injected before the tool-results message is the usual cause)",
			lr.round, bad, prev)
	}
	// If the caller wants reasoning streamed but didn't set a content
	// stream handler, take the streaming path with a no-op content
	// callback so the reasoning callback can fire. The reasoning
	// channel only flows on the streaming path; ChatStreamWithReport
	// is the only LLM dispatch that pumps it.
	lr.rs.streamHandler = lr.cfg.Stream
	if lr.rs.streamHandler == nil && lr.cfg.ReasoningStream != nil {
		lr.rs.streamHandler = func(string) {}
	}
	// Spend guard: once this turn has burned its lead budget, or kept
	// hitting one identical failure, the rest of the rounds run on the
	// worker. Clearing the route key is what carries the decision to
	// ChatStreamWithReport, which resolves the tier from that key alone;
	// the non-streaming branch below reads deescalated directly.
	lr.rs.callOpts = lr.rs.opts
	if lr.deescalated != "" {
		lr.rs.callOpts = append(append([]ChatOption{}, lr.rs.opts...), WithRouteKey(""))
	} else if lr.cfg.TierOverride != TierUnset {
		// Carry the per-run pin onto the call itself. The non-streaming
		// branch below reads cfg directly, but ChatStreamWithReport gets
		// only these options — so without this the pin reached every path
		// EXCEPT the streaming one, and the streaming one is every
		// interactive turn. Copied rather than appended in place: opts is
		// the caller's slice and is reused every round.
		//
		// Skipped while de-escalated, where the cleared route key is the
		// decision and a pin must not undo it.
		lr.rs.callOpts = append(append([]ChatOption{}, lr.rs.opts...), WithTierOverride(lr.cfg.TierOverride))
	}
	// Whether THIS round went to the lead — the fallback below only fires
	// for rounds the lead actually served, so a worker failure isn't
	// pointlessly retried on the worker.
	lr.rs.roundUsedLead = false
	if lr.deescalated == "" && !lr.T.LeadDenied() {
		lr.rs.roundUsedLead = lr.cfg.wantsLead()
	}
	if lr.rs.streamHandler != nil {
		lr.rs.resp, lr.rs.err = lr.T.ChatStreamWithReport(lr.ctx, lr.history, lr.rs.streamHandler, lr.rs.callOpts...)
	} else {
		// A binding private pin redirects all routing to worker — no escalation.
		useLead := !lr.T.LeadDenied() && lr.cfg.wantsLead()
		if lr.deescalated != "" {
			useLead = false
		}
		callFn := lr.T.WorkerChat
		if useLead {
			callFn = lr.T.LeadChat
			// The tier is settled HERE, from wantsLead — which already
			// consulted the route stage and any per-run override. Without
			// saying so, LeadChat re-derives it from the same RouteKey,
			// finds the stage says worker, and transparently delegates
			// back: the override reaches the call and is undone one frame
			// later. RouteKey stays on the options because it still
			// carries the stage's thinking preference.
			lr.rs.callOpts = append(lr.rs.callOpts, WithTierResolved())
			// An EXPLICIT pin also refuses the quiet degrade. A routing
			// preference should keep the session alive on the worker when
			// the lead is unavailable; somebody who pinned one system to the
			// lead said which model they wanted, and answering from the
			// other one behind a debug line is the substitution the pin
			// exists to prevent.
			if lr.cfg.TierOverride == LEAD {
				lr.rs.callOpts = append(lr.rs.callOpts, WithNoTierFallback())
			}
		}
		// Empty/timeout/empty-error retry happens inside retryLLM
		// (core/llm.go) — every caller gets it for free, including
		// direct WorkerChat/LeadChat and chat-handler ChatStream.
		lr.rs.resp, lr.rs.err = callFn(lr.ctx, lr.history, lr.rs.callOpts...)
	}
	// Context-exceeded recovery: provider rejected the prompt as
	// too large. Naive retries don't help (same prompt → same
	// error), but aggressive compaction (force=true drops all but
	// the newest tool-result body) may free enough room. Retry
	// once after compacting; if the second call still says context-
	// exceeded, surface a clean caller-friendly error instead of
	// the raw provider message.
	if lr.rs.err != nil && IsContextExceededError(lr.rs.err) {
		// The window to recover INTO. cfg.ContextSize is the configured
		// answer and is routinely zero — it comes from an optional
		// ContextSizer the provider may not implement, and every
		// size-dependent path is documented as disabling itself when it
		// is. That default is defensible for a routine budget check and
		// indefensible here: we are in this branch because the provider
		// has JUST said the prompt is too large, so "we do not know the
		// window" must not mean "do nothing". The provider's own refusal
		// usually names the number; failing that, assume a small window
		// and recover harder than strictly needed.
		refused := estimatePromptTokens(lr.history, lr.systemPrompt)
		noteContextRefusal(lr.cfg.ContextSize, refused)
		window := recoveryWindow(lr.cfg.ContextSize, lr.rs.err, refused)
		Debug("[agent_loop] round %d: context exceeded — recovering into a %d-token window (refused prompt ~%d tokens)", lr.round, window, refused)

		compactHistory(lr.history, lr.systemPrompt, window, true)
		// Compaction only cuts bodies. If the bulk is ordinary
		// conversation it is still there, so SUMMARIZE the older span
		// before considering throwing any of it away — the recovery
		// ladder is cheap-and-lossless, then costly-and-faithful, then
		// cheap-and-lossy, in that order.
		if stillTooBig(lr.history, lr.systemPrompt, window) {
			if folded, ok := lr.T.summarizeOldHistory(lr.ctx, lr.history, window, contextRecoveryKeepWhole); ok {
				lr.history = folded
			}
		}
		// Last resort. A summarizer that is unavailable or failing must
		// not leave the turn dead when dropping old text would let it run.
		if stillTooBig(lr.history, lr.systemPrompt, window) {
			budget := window - EstimateTokens(lr.systemPrompt) - 34000
			if n := elideOldMessageText(lr.history, budget, contextRecoveryKeepWhole); n > 0 {
				Log("[agent_loop] context recovery: summarization unavailable — elided ~%d tokens of older message text", n)
			}
		}
		if lr.rs.streamHandler != nil {
			lr.rs.resp, lr.rs.err = lr.T.ChatStreamWithReport(lr.ctx, lr.history, lr.rs.streamHandler, lr.rs.callOpts...)
		} else {
			useLead := !lr.T.LeadDenied() && lr.cfg.wantsLead()
			if lr.deescalated != "" {
				useLead = false
			}
			callFn := lr.T.WorkerChat
			if useLead {
				callFn = lr.T.LeadChat
				lr.rs.callOpts = append(lr.rs.callOpts, WithTierResolved())
				if lr.cfg.TierOverride == LEAD {
					lr.rs.callOpts = append(lr.rs.callOpts, WithNoTierFallback())
				}
			}
			lr.rs.resp, lr.rs.err = callFn(lr.ctx, lr.history, lr.rs.callOpts...)
		}
		if lr.rs.err != nil && IsContextExceededError(lr.rs.err) {
			// Log, not Debug. This is the moment somebody needs the
			// breakdown, and requiring --debug to learn where two million
			// tokens went means the answer is missing exactly when it is
			// being asked for.
			Log("[agent_loop] round %d: context exceeded after force-compact — %s", lr.round, promptSizeReport(lr.cfg, lr.systemPrompt, lr.history))
			return lr.exit(lr.rs.resp, lr.history, fmt.Errorf("context exhausted: %s. Compaction only trims conversation history, so if the bulk is elsewhere a new session will not help (%w)",
				promptSizeHeadline(lr.cfg, lr.systemPrompt, lr.history), lr.rs.err))
		}
		if lr.rs.err == nil {
			Debug("[agent_loop] round %d: context-exceeded recovered after force-compact", lr.round)
		}
	}
	// LEAD FAILED OR REFUSED — hand this round to the worker instead of
	// failing the turn.
	//
	// Escalating to a lead is an optimization, so losing it should cost
	// quality, not the work. Two ways it goes wrong and both used to end
	// the turn outright:
	//
	//   - the call errors (provider 4xx/5xx, network, a malformed tool
	//     schema the lead rejects but the worker accepts)
	//   - the provider REFUSES on its own policy (safety / recitation /
	//     blocklist), which is not the local deployment's policy at all
	//
	// The local worker has neither the remote provider's outage nor its
	// content rules, so it can usually just do the work. This reuses the
	// existing de-escalation path (the same one the lead-budget guard
	// uses), so the rest of the turn stays on the worker rather than
	// bouncing back and failing again next round.
	if lr.rs.err != nil || providerRefused(lr.rs.resp) {
		if lr.rs.roundUsedLead && lr.deescalated == "" && !lr.T.LeadDenied() {
			why := "the lead model call failed"
			diag := "The lead model could not complete this round"
			if lr.rs.err == nil {
				why = "the lead model refused this round on its own content policy"
				diag = "The lead model refused this round on its provider's content policy"
			}
			Log("[agent_loop] round %d: %s — retrying on the worker model", lr.round, why)
			lr.deescalated = "lead-unavailable"
			if lr.cfg.OnDiag != nil {
				lr.cfg.OnDiag("tier_deescalated", diag+" — this turn continued on the local worker model instead of stopping.")
			}
			workerOpts := append(append([]ChatOption{}, lr.rs.opts...), WithRouteKey(""))
			if lr.rs.streamHandler != nil {
				lr.rs.resp, lr.rs.err = lr.T.ChatStreamWithReport(lr.ctx, lr.history, lr.rs.streamHandler, workerOpts...)
			} else {
				lr.rs.resp, lr.rs.err = lr.T.WorkerChat(lr.ctx, lr.history, workerOpts...)
			}
		}
	}
	if lr.rs.err != nil {
		return lr.exit(lr.rs.resp, lr.history, lr.rs.err)
	}
	lr.lastResp = lr.rs.resp

	// Lead-spend accounting. resp.Tier reflects the tier that actually
	// SERVED the round — a lead call that fell back to the worker is
	// tagged WORKER and correctly doesn't count against the budget.
	if lr.rs.resp != nil && lr.rs.resp.Tier == LEAD {
		lr.leadTokens += lr.rs.resp.InputTokens + lr.rs.resp.OutputTokens
		if lr.deescalated == "" && LeadTurnTokenBudget > 0 && lr.leadTokens >= LeadTurnTokenBudget {
			lr.deescalated = "budget"
			Log("[agent_loop] lead budget spent (%d tokens ≥ %d) — remaining rounds run on the worker tier", lr.leadTokens, LeadTurnTokenBudget)
			if lr.cfg.OnDiag != nil {
				lr.cfg.OnDiag("tier_deescalated", fmt.Sprintf("This turn spent its lead-model budget (%d tokens) — the remaining rounds ran on the worker model.", lr.leadTokens))
			}
		}
	}

	// Charge this round, then check the line. A turn under way finishes —
	// on the worker tier once it crosses — because stranding half-done
	// work costs the owner more than the round would have.
	if spent, crossed := chargeDailySpend(lr.cfg, lr.rs.resp); crossed && lr.deescalated == "" && !lr.T.LeadDenied() {
		lr.deescalated = "spend-cap"
		Log("[agent_loop] daily spend cap reached mid-turn for %q ($%.2f of $%.2f) — remaining rounds run on the worker tier", lr.cfg.BudgetKey, spent, lr.cfg.DailySpendUSD)
		lr.emitDiag("spend-cap", fmt.Sprintf("This agent crossed its $%.2f daily allowance mid-turn; the rest of the turn ran on the local worker model.", lr.cfg.DailySpendUSD))
	}

	Debug("[agent_loop] round %d: content=%d chars, reasoning=%d chars, tool_calls=%d", lr.round, len(lr.rs.resp.Content), len(lr.rs.resp.Reasoning), len(lr.rs.resp.ToolCalls))
	// BREADCRUMB: LLM returned. Pair with the "→ LLM call"
	// breadcrumb above to detect a wedged provider call.
	// The tier is on this line and not the "→ LLM call" one because it is
	// the tier that actually SERVED the round, taken off the response — a
	// fallback or a de-escalation is recorded here as what happened, where
	// anything logged before the call would only be what was intended. It
	// is the direct answer to "did this really run on the lead", which no
	// amount of reading the routing config can settle.
	lr.llmWall += time.Since(lr.rs.llmStarted)
	lr.llmCalls++
	Log("[agent_loop] round %d: ← LLM returned in %s (tier=%v, content=%d, tools=%d%s)", lr.round, time.Since(lr.rs.llmStarted).Round(time.Millisecond), lr.rs.resp.Tier, len(lr.rs.resp.Content), len(lr.rs.resp.ToolCalls), promptReuseNote(lr.rs.resp))

	// The turn's prompt digest, completed with what the provider charged
	// and handed to whatever outlives the turn. Emitted here, after the
	// first response, because InputTokens is the one number in it that is
	// measured rather than estimated — and when it disagrees badly with
	// the estimate, the estimator is the thing that is wrong.
	if lr.digestBuilt && !lr.digestSent {
		lr.digest.InputTokens = lr.rs.resp.InputTokens
		lr.digest.Prefilled = lr.rs.resp.PromptTokensPrefilled
		lr.digest.PrefillMS = lr.rs.resp.PrefillMS
		lr.digestSent = true
		emitPromptDigest(lr.ctx, lr.cfg, lr.digest)
	}
	return actNone
}

func (lr *loopRun) recordResponse() loopAction {
	// DIAGNOSTIC: collapse-ish round — the model wrote a large reasoning
	// block but little visible content and called no tool. The existing
	// reasoning-collapse re-prompt below only triggers on ~EMPTY content
	// (<3 chars — a short-but-complete reply is normal and has already
	// streamed), so anything from a stub sentence up to 200 chars over 4k
	// tokens of reasoning is returned as the reply with the reasoning
	// silently dropped. Dump the reasoning here so we can confirm whether
	// the actual answer was buried in the thinking channel. Thinking
	// models put the conclusion at the END, so log the tail in full
	// rather than truncating it off.
	if len(lr.rs.resp.ToolCalls) == 0 && len(strings.TrimSpace(lr.rs.resp.Content)) < 200 && len(lr.rs.resp.Reasoning) > 2000 {
		tail := lr.rs.resp.Reasoning
		if len(tail) > 4000 {
			tail = "…" + tail[len(tail)-4000:]
		}
		Debug("[agent_loop] COLLAPSE-DIAG round %d: content=%q | reasoning_tail(%d total)=%q",
			lr.round, strings.TrimSpace(lr.rs.resp.Content), len(lr.rs.resp.Reasoning), tail)
	}

	// Thinking models may place their response entirely in the
	// reasoning field. Promote reasoning to content when there is
	// no content or tool calls so text-based tool parsing can work.
	if lr.rs.resp.Content == "" && len(lr.rs.resp.ToolCalls) == 0 && lr.rs.resp.Reasoning != "" {
		Debug("[agent_loop] promoting reasoning to content (%d chars)", len(lr.rs.resp.Reasoning))
		lr.rs.resp.Content = lr.rs.resp.Reasoning
	}

	// PromptTools path: parse <tool_call> tags from the text response.
	// Everything is plain text — no native ToolCall/ToolResult objects.
	if lr.cfg.PromptTools {
		tc, preamble := ParsePromptToolCall(lr.rs.resp.Content, lr.handlers)
		if tc == nil {
			// No tool call — LLM is done. But first re-drain any
			// mid-flight injection that landed during this final
			// round (see the native-path finalize for the full
			// rationale); continue instead of finishing if there's
			// pending input. InjectionDrain, not OnRoundStart.
			if lr.cfg.InjectionDrain != nil && lr.round < lr.maxRounds {
				if injected := lr.cfg.InjectionDrain(); len(injected) > 0 {
					Debug("[agent_loop] pre-finalize injection (prompt-tools): %d note(s) — continuing", len(injected))
					lr.history = append(lr.history, Message{Role: "assistant", Content: lr.rs.resp.Content, Reasoning: lr.rs.resp.Reasoning})
					lr.history = append(lr.history, injected...)
					return actContinue
				}
			}
			// Record and return.
			lr.history = append(lr.history, Message{Role: "assistant", Content: lr.rs.resp.Content, Reasoning: lr.rs.resp.Reasoning})
			if lr.cfg.OnStep != nil {
				lr.cfg.OnStep(StepInfo{Round: lr.round, Content: lr.rs.resp.Content, Done: true})
			}
			return lr.exit(lr.rs.resp, lr.history, nil)
		}

		if lr.cfg.MaskDebugOutput {
			Debug("[agent_loop] prompt-tool call: %s([masked: %d bytes])", toolCallLabel(*tc), len(formatArgs(tc.Args)))
		} else {
			Debug("[agent_loop] prompt-tool call: %s (args=%d bytes)", toolCallLabel(*tc), len(formatArgs(tc.Args)))
			Trace("[agent_loop] prompt-tool call: %s(%s)", tc.Name, formatArgs(tc.Args))
		}

		// Record the assistant's message (preamble only, strip the tag).
		if preamble != "" {
			lr.history = append(lr.history, Message{Role: "assistant", Content: preamble})
		}

		// Confirmation check.
		if lr.needsConfirm[tc.Name] {
			if !lr.confirmFn(tc.Name, formatArgs(tc.Args)) {
				Debug("[agent_loop] prompt-tool denied: %s", tc.Name)
				lr.history = append(lr.history, Message{
					Role:    "user",
					Content: fmt.Sprintf("Tool call to %s was denied.", tc.Name),
				})
				if lr.cfg.OnStep != nil {
					lr.cfg.OnStep(StepInfo{Round: lr.round, ToolCalls: []ToolCall{*tc}, ToolErrors: 1})
				}
				return actContinue
			}
		}

		// Unverified premise: this turn is answering someone who is not the
		// principal, and the thing about to happen rests on what they said.
		// Deflected ONCE, then allowed — see premiseGate.
		if note, held := lr.premise.hold(tc.Name, lr.writeTools[tc.Name]); held {
			Debug("[agent_loop] premise gate: held %s — turn rests on %s's unverified claim", tc.Name, lr.cfg.LiveClaimSpeaker)
			lr.emitDiag("unverified-premise-held", fmt.Sprintf("Held %s: this turn acts on %s's unverified claim. Asked to check it first.", tc.Name, lr.cfg.LiveClaimSpeaker))
			lr.history = append(lr.history, Message{Role: "user", Content: frameworkNoticeTag + note})
			if lr.cfg.OnStep != nil {
				lr.cfg.OnStep(StepInfo{Round: lr.round, ToolCalls: []ToolCall{*tc}})
			}
			return actContinue
		}

		// Execute the tool.
		output, toolErr := safeInvoke(tc.Name, lr.handlers[tc.Name], tc.Args)
		lr.toolFiredThisTurn = true
		toolErrors := 0
		var resultText string
		if toolErr != nil {
			resultText = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, toolErr)
			toolErrors = 1
			lr.cumulativeToolErrors++
		} else {
			resultText = fmt.Sprintf("Tool result from %s:\n%s", tc.Name, output)
		}
		if lr.cfg.MaskDebugOutput {
			Debug("[agent_loop] prompt-tool result: %s: [masked: %d bytes]", tc.Name, len(resultText))
		} else {
			Debug("[agent_loop] prompt-tool result: %s (%d bytes)", tc.Name, len(resultText))
			Trace("[agent_loop] prompt-tool result: %s", resultText)
		}

		// Send result back as a plain user message.
		lr.history = append(lr.history, Message{Role: "user", Content: resultText})
		lr.prevHadToolCalls = true

		if lr.cfg.OnStep != nil {
			lr.cfg.OnStep(StepInfo{Round: lr.round, ToolCalls: []ToolCall{*tc}, ToolErrors: toolErrors})
		}
		return actContinue
	}

	// Native tool path (existing behavior).

	// Strip echoed tool-call markup from content. Some models (Qwen 3
	// in particular) emit a structured ToolCall AND simultaneously
	// echo the same call as `<tool_call>...</tool_call>` text in
	// content. The native dispatch happens via resp.ToolCalls; the
	// XML echo is just noise and would leak to the user if the loop
	// exits on this round (MaxRounds, error, rescue path) before the
	// tool result and a clean follow-up reply come back. Strip
	// unconditionally — when there's no markup it's a no-op.
	if len(lr.rs.resp.ToolCalls) > 0 && (strings.Contains(lr.rs.resp.Content, "<tool_call>") || strings.Contains(lr.rs.resp.Content, "<function=")) {
		Debug("[agent_loop] stripping echoed tool-call markup from content alongside native ToolCalls")
		lr.rs.resp.Content = StripToolCallMarkup(lr.rs.resp.Content)
	}

	// Record assistant response.
	lr.history = append(lr.history, Message{
		Role:      "assistant",
		Content:   lr.rs.resp.Content,
		Reasoning: lr.rs.resp.Reasoning,
		ToolCalls: lr.rs.resp.ToolCalls,
	})
	return actNone
}

func (lr *loopRun) noToolCallRound() loopAction {
	// If no tool calls, check if the model emitted a tool call as
	// text (common with models that don't support function calling).
	// Preserve resp.Content alongside the synthesized tool call —
	// the LLM produced text reasoning AND happened to mention a
	// tool; that text may be the actual answer-in-progress and we
	// shouldn't drop it. The history entry keeps both so subsequent
	// rounds (and the rescue path on MaxRounds exit) see what the
	// model said.
	//
	// Qwen3 in particular sometimes emits the XML-style tool-call
	// markup in resp.Reasoning rather than resp.Content (the
	// "thinking" channel) when it's mid-reasoning about which tool
	// to invoke. Try Content first, then fall back to Reasoning so
	// those calls don't slip through and render as visible text.
	if len(lr.rs.resp.ToolCalls) == 0 {
		// Clean-finish gate on the PROSE scan only. A model that
		// reports "stop" with a substantial body has answered; reading
		// a tool call out of that body turns a finished turn into a
		// tool round, and if the loop-guard then blocks the phantom
		// call the real answer is replaced by a regenerated one.
		// Observed: an 8366-char final answer re-read as a moltbook
		// call, blocked as a repeat, then regenerated over 39s.
		//
		// Length matters as well as the stop reason: a model that
		// only ever NARRATES its calls emits a short lead-in ("I'll
		// call X with…") and still finishes with "stop", so gating on
		// the stop reason alone would silently stop doing its work.
		// Short + stop stays extractable; long + stop does not.
		allowProse := true
		if lr.rs.resp.StopReason == "stop" && len(lr.rs.resp.Content) >= cleanFinishProseFloor {
			allowProse = false
			Debug("[agent_loop] prose tool-call scan skipped — model finished cleanly with %d chars (stop_reason=%q)", len(lr.rs.resp.Content), lr.rs.resp.StopReason)
		}
		parsed := ParseTextToolCall(lr.rs.resp.Content, lr.handlers, lr.toolDefs, allowProse)
		if parsed == nil && lr.rs.resp.Reasoning != "" && strings.Contains(lr.rs.resp.Reasoning, "<function=") {
			// Reasoning-channel markup only — never the prose scan.
			if reasoningCall := ParseTextToolCall(lr.rs.resp.Reasoning, lr.handlers, lr.toolDefs, false); reasoningCall != nil {
				Debug("[agent_loop] parsed tool call out of reasoning channel: %s", reasoningCall.Name)
				parsed = reasoningCall
			}
		}
		if parsed != nil {
			// Keep the answer the model actually produced. If this
			// synthesized call turns out to be a phantom (the loop-guard
			// blocks it), the turn ends with this instead of paying to
			// regenerate something already in hand. Cleared as soon as a
			// tool really runs.
			if txt := strings.TrimSpace(StripToolCallMarkup(lr.rs.resp.Content)); txt != "" {
				lr.synthesizedFrom = txt
			}
			Debug("[agent_loop] parsed text-based tool call: %s", parsed.Name)
			lr.rs.resp.ToolCalls = []ToolCall{*parsed}
			// Strip the synthesized tool-call markup (XML <tool_call>
			// or bare <function=...>...</function>) from resp.Content
			// so subsequent rounds and the rescue path don't expose
			// the markup OR any preceding narration to the user. The
			// real action lives in the dispatched tool now; the text
			// shouldn't trail along.
			lr.rs.resp.Content = StripToolCallMarkup(lr.rs.resp.Content)
			lr.history[len(lr.history)-1] = Message{
				Role:      "assistant",
				Content:   lr.rs.resp.Content,
				Reasoning: lr.rs.resp.Reasoning,
				ToolCalls: lr.rs.resp.ToolCalls,
			}
		} else if strings.Contains(lr.rs.resp.Content, "<function=") || strings.Contains(lr.rs.resp.Content, "<tool_call>") {
			// Orphaned XML — the model emitted a tool-call attempt
			// but the name didn't resolve (typo, hallucinated tool
			// name like "run_shell_command" instead of "run_local").
			// Strip the markup so the user doesn't see XML, and
			// inject a corrective so the model gets a chance to
			// retry with the right name.
			attemptedName, _ := parseFunctionTagToolCall(lr.rs.resp.Content)
			lr.rs.resp.Content = StripToolCallMarkup(lr.rs.resp.Content)
			lr.history[len(lr.history)-1] = Message{
				Role:      "assistant",
				Content:   lr.rs.resp.Content,
				Reasoning: lr.rs.resp.Reasoning,
			}
			lr.noteUncorrected(correctionOrphanedXML, "The reply again wrote tool-call XML for an unknown tool; the markup was stripped but no further re-prompt was left to spend.")
			if lr.corrections.available(correctionOrphanedXML) && lr.round < lr.maxRounds {
				hint := ""
				if attemptedName != "" {
					hint = fmt.Sprintf(" You attempted to call %q which is not a registered tool.", attemptedName)
					if suggestion := nearestToolName(attemptedName, lr.handlers); suggestion != "" {
						hint += fmt.Sprintf(" Did you mean %q?", suggestion)
					}
				}
				Debug("[agent_loop] orphaned XML tool-call detected (name=%q), re-prompting: correction %d/%d", attemptedName, lr.corrections.spend(correctionOrphanedXML), maxCorrectionsPerKind)
				lr.emitDiag("tool-markup-corrected", fmt.Sprintf("The reply wrote tool-call XML for an unknown tool (%q); markup stripped and re-prompted for a real call.", attemptedName))
				lr.settleRound() // finalize the stripped prose so the retry doesn't concatenate into it
				lr.history = append(lr.history, Message{
					Role:    "user",
					Content: frameworkNoticeTag + "Your previous response contained tool-call XML markup with a name that doesn't match any available tool." + hint + " Look at your tool catalog for the exact tool name. Use the native function-calling format, not text markup. Try again now.",
				})
				return actContinue
			}
		} else if refs := phantomDeliveryRefs(lr.cfg, lr.rs.resp.Content); len(refs) > 0 {
			// The reply promises a file that does not exist and the turn
			// produced nothing to deliver. Same class as a fake tool call —
			// an action claimed but never taken — and it gets the same
			// remedy: strip the claim, say what was wrong, let the model
			// either do the work or admit it can't. Left alone, this leaves
			// the reply empty after stripping and the person on the other
			// end gets a generic apology about their phrasing.
			lr.rs.resp.Content = StripDeliveryMarkers(lr.rs.resp.Content)
			lr.history[len(lr.history)-1] = Message{
				Role:      "assistant",
				Content:   lr.rs.resp.Content,
				Reasoning: lr.rs.resp.Reasoning,
			}
			if lr.corrections.available(correctionPhantomDelivery) && lr.round < lr.maxRounds {
				// Joined, not %v: a ref is a filename when the reply named
				// one and a plain noun phrase ("the image") when it didn't,
				// and "[the image]" reads as a placeholder the model is
				// meant to fill in rather than the thing it just claimed.
				named := strings.Join(refs, ", ")
				Debug("[agent_loop] phantom delivery detected (%s), re-prompting: correction %d/%d", named, lr.corrections.spend(correctionPhantomDelivery), maxCorrectionsPerKind)
				lr.emitDiag("phantom-delivery-corrected", fmt.Sprintf("The reply presented %s as delivered, but nothing was attached and nothing exists to attach. The claim was removed and the model re-prompted.", named))
				// Retract, not settle. On a streaming surface the false
				// claim has already been painted, and settling would leave
				// it standing above the correction — the user reads "Here's
				// your picture" and then, underneath, that there is no
				// picture. Same class as a blocked guardrail draft: a
				// statement the framework has decided must not stand.
				// It stays in `history` either way, which is what the model
				// needs to see to understand what it is being corrected on.
				// Falls back to settleRound on hosts with no retract wired.
				lr.retractRound()
				lr.history = append(lr.history, Message{
					Role: "user",
					Content: frameworkNoticeTag + fmt.Sprintf(
						"You wrote your reply as though you were handing over %s. Nothing was attached and nothing exists to attach — it was never created, fetched, or it failed. The user received your words and no file. Either call the tool that actually produces it now, or tell them plainly that you do not have it. Do NOT present a file you have not made, and do not write a delivery marker for one.", named),
				})
				return actContinue
			}
			// Out of corrections, and the claim is still false. Everything
			// above assumed the model could be talked into fixing it; twice
			// now it has rewritten the same claim, and the old code simply
			// let the third one through — the guard that ruled it false being
			// the only thing that ever noticed.
			//
			// Delivering a promise about a file that does not exist is worse
			// than delivering nothing, so the claim is replaced with something
			// true. Same principle as a substituted guardrail decline: the
			// framework writes in the agent's voice only when the alternative
			// is letting a false statement stand.
			if lr.corrections.exhausted(correctionPhantomDelivery) {
				named := strings.Join(refs, ", ")
				Debug("[agent_loop] phantom delivery still uncorrected after %d attempts (%s) — substituting a truthful reply", maxCorrectionsPerKind, named)
				lr.emitDiag("phantom-delivery-uncorrected", fmt.Sprintf("The reply claimed %s again after two corrections, and no such file exists. The claim was replaced rather than delivered.", named))
				lr.retractRound()
				lr.rs.resp.Content = UnfulfilledDeliveryReply(refs)
				lr.history[len(lr.history)-1] = Message{
					Role:      "assistant",
					Content:   lr.rs.resp.Content,
					Reasoning: lr.rs.resp.Reasoning,
				}
			}
		} else if containsFakeToolCodeBlock(lr.rs.resp.Content) {
			// Training-data artifact: the model writes its tool call
			// as plain text in a <tool_code> block (Gemini format) or
			// with ::name(...):: cascade syntax (gohort-shaped fake).
			// This happens most often near the round cap when the
			// wrap-up nudge fires and the model interprets "respond
			// directly now" as "polish a final message" — so it
			// describes the call in narrative form ("Creating the
			// updated tool now…") and appends the fake invocation.
			// The actual tool_calls field is empty, so the loop
			// would otherwise terminate with nothing executed.
			//
			// Recovery: strip the fake markup from the visible
			// content and inject a corrective re-prompt so the
			// model issues the real structured call next round.
			attemptedName := extractFakeToolCodeName(lr.rs.resp.Content)
			lr.rs.resp.Content = stripFakeToolCodeBlocks(lr.rs.resp.Content)
			lr.history[len(lr.history)-1] = Message{
				Role:      "assistant",
				Content:   lr.rs.resp.Content,
				Reasoning: lr.rs.resp.Reasoning,
			}
			lr.noteUncorrected(correctionFakeToolCode, "The reply again wrote a tool call as a text block instead of calling it; the markup was stripped but no further re-prompt was left to spend.")
			if lr.corrections.available(correctionFakeToolCode) && lr.round < lr.maxRounds {
				hint := ""
				if attemptedName != "" {
					hint = fmt.Sprintf(" You appeared to invoke %q.", attemptedName)
				}
				Debug("[agent_loop] fake <tool_code>/::name():: block detected (name=%q), re-prompting: correction %d/%d", attemptedName, lr.corrections.spend(correctionFakeToolCode), maxCorrectionsPerKind)
				lr.emitDiag("tool-markup-corrected", fmt.Sprintf("The reply wrote a tool call as plain text (%q) instead of a real call; markup stripped and re-prompted.", attemptedName))
				lr.settleRound() // finalize the stripped prose so the retry doesn't concatenate into it
				lr.history = append(lr.history, Message{
					Role:    "user",
					Content: frameworkNoticeTag + "Your previous response wrote a tool invocation as plain TEXT (in a <tool_code> block or ::name(...):: form)." + hint + " That format does NOT execute — only structured tool_calls do. Re-issue the call NOW using the framework's native tool-calling mechanism. Do not wrap it in <tool_code>, do not use ::name():: syntax, do not narrate 'Creating the tool now…' — just emit the structured call.",
				})
				return actContinue
			}
		}
	}

	// If still no tool calls, the LLM is done reasoning — UNLESS
	// the content text is a promise of action without a tool call.
	// "Let me try X." / "One moment, pulling that up." / "I'll
	// figure this out properly." with no actual tool fired is the
	// canonical Qwen-style failure mode where the user sees only
	// stated intent and nothing happens. When detected, inject a
	// corrective user message and re-loop instead of returning,
	// up to maxCorrectionsPerKind times per turn.
	if len(lr.rs.resp.ToolCalls) == 0 {
		// Cut off, not finished. The provider says so outright, and until
		// this the loop read "no tool calls + some content" as a completed
		// turn and exited respond_directly with rounds to spare.
		//
		// What that looked like: a lead spent its whole output allowance
		// thinking, emitted 133 characters and no tool call, and the turn
		// ended on "Doing it now." Twice in a row, three minutes and a
		// couple of dollars each, with 29 of 30 rounds unused. stop_reason
		// said max_tokens both times and nothing was listening — the only
		// consumers were providerRefused (safety reasons only) and a Warn.
		//
		// Checked BEFORE the behavioural corrections below because this is
		// a mechanical fact rather than an inference about intent: the
		// reply ISN'T an unkept promise or an announced call, it is an
		// unfinished one, and the corrections that pattern-match prose
		// would either miss it or scold the model for being interrupted.
		if responseWasTruncated(lr.rs.resp) {
			if lr.corrections.available(correctionTruncated) {
				Debug("[agent_loop] round %d: output truncated (stop_reason=%q, %d chars) — continuing (correction %d/%d)",
					lr.round, lr.rs.resp.StopReason, len(lr.rs.resp.Content), lr.corrections.spend(correctionTruncated), maxCorrectionsPerKind)
				lr.emitDiag("output-truncated", truncationDiag(lr.rs.resp))
				lr.settleRound() // finalize the partial so the continuation doesn't concatenate into it
				// The continuation needs a round of its own. This used to
				// be gated on round < maxRounds, which left a one-round
				// call — a synthesis pass, a summary — with no way to
				// finish: its only round was the cut one, and the fragment
				// shipped as the report. Extend the runway by the round the
				// continuation takes, keeping an active wrap-up hard stop
				// in step; the correction budget above bounds how often.
				lr.graceRounds++
				if lr.hardStop >= 0 {
					lr.hardStop++
				}
				lr.truncatedLead.WriteString(lr.rs.resp.Content)
				lr.history = append(lr.history, Message{
					Role:    "user",
					Content: frameworkNoticeTag + "Your previous reply was CUT OFF before you finished it — you did not choose to stop. Continue from where you left off without repeating what you already said. If you were about to call a tool, emit the real structured tool call now; keep any preamble short so the call itself fits.",
				})
				return actContinue
			}
			lr.noteUncorrected(correctionTruncated, "The reply was cut off at the output limit again and no further continuation was left to spend, so the partial answer was delivered as written.")
		}

		// Cut off by the provider, not by the model. Anthropic's streaming
		// classifier can stop a reply partway with stop_reason=refusal,
		// and the fragment arrives exactly like a finished answer: content,
		// no tool calls. Nothing else here looks at it — providerRefused
		// wants EMPTY content, the truncation check wants max_tokens — so
		// the fragment was delivered as the answer with a Warn in the log
		// and nothing in the turn's diagnostics. No retry: a continuation
		// on the same model meets the same classifier, and a regenerate
		// re-bills the whole prompt. The user gets told what they are
		// looking at and decides.
		if providerCutReply(lr.rs.resp) {
			Log("[agent_loop] round %d: provider stopped the reply partway (stop_reason=%q, %d chars) — delivering the fragment with a diagnostic",
				lr.round, lr.rs.resp.StopReason, len(lr.rs.resp.Content))
			lr.emitDiag("provider-refusal", "The provider's content classifier stopped this reply partway (stop_reason=refusal); what you see is the fragment produced before the stop, not a finished answer. Rephrasing the request or retrying may get a complete one.")
		}

		// Action-promise correction DISABLED for now — it false-positived
		// on ordinary conversational replies ("I'll try to nail the house
		// next time."), burning rounds re-prompting for an action the model
		// never intended. Flip to true to re-enable; the reasoning-collapse
		// correction below is unaffected either way.
		const actionPromiseCorrection = false
		if actionPromiseCorrection && lr.corrections.available(correctionActionPromise) && lr.round < lr.maxRounds && !lr.toolFiredThisTurn && containsActionPromise(lr.rs.resp.Content) {
			Debug("[agent_loop] action-promise without tool call detected, re-prompting (correction %d/%d): %q", lr.corrections.spend(correctionActionPromise), maxCorrectionsPerKind, truncForLog(lr.rs.resp.Content, 80))
			lr.history = append(lr.history, Message{
				Role:    "user",
				Content: frameworkNoticeTag + "You stated an intention to take an action (e.g. 'let me try', 'one moment') but called no tool. Either call the tool now to actually do what you said, or reply plainly that you can't proceed and explain what you tried. Do NOT promise further action without taking it.",
			})
			return actContinue
		}

		// Announced-call correction: the reply ENDS on a colon
		// introducing a call that never came — "Here's the
		// `update_agent` call to implement these changes:" and the turn
		// stops (observed: Builder settled a turn exactly there and the
		// user watched nothing happen). Unlike the disabled
		// actionPromiseCorrection above, the trailing-colon +
		// call-announcement shape doesn't occur in complete replies, so
		// it's safe to re-prompt on. No !toolFiredThisTurn gate:
		// announcing a follow-up call and stopping is just as broken
		// after earlier tools succeeded. Budget-shared with the other
		// promise corrections so it can't loop.
		if endsWithCallAnnouncement(lr.rs.resp.Content) {
			lr.noteUncorrected(correctionAnnouncedCall, "The reply again ended announcing a call it never made; no further re-prompt was left to spend, so it was delivered as written.")
		}
		if lr.corrections.available(correctionAnnouncedCall) && lr.round < lr.maxRounds && endsWithCallAnnouncement(lr.rs.resp.Content) {
			Debug("[agent_loop] reply ends announcing a call that never followed, re-prompting: correction %d/%d: %q", lr.corrections.spend(correctionAnnouncedCall), maxCorrectionsPerKind, truncForLog(lr.rs.resp.Content, 80))
			lr.emitDiag("announced-call-corrected", "The reply ended by announcing a tool call it never made; re-prompted to actually make the call or finish the reply.")
			lr.settleRound() // finalize the announcement so the retry doesn't concatenate into it
			lr.history = append(lr.history, Message{
				Role:    "user",
				Content: frameworkNoticeTag + "Your previous reply ended by announcing a call or content that never followed (it ends with a colon). If you meant to run a tool, emit the REAL structured tool call NOW — never write it out as text or stop after describing it. If no tool exists for what you described, say so plainly and finish the reply instead.",
			})
			return actContinue
		}

		// Tool-mention correction: the model named a KNOWN tool in its
		// reply (e.g. "let me get_joke", or "I can't reach
		// read_support_bundles") but emitted no structured call.
		// parseNaturalToolCall rescues a narration only when arguments
		// are readable off the prose, so a no-arg tool (nothing to
		// extract) and a tool merely TALKED about both fall through it
		// — the tool silently never runs, and the model either narrates
		// a result it never got or reports an inability that isn't one.
		// Nudge once to either issue the real call or answer plainly.
		// FAR narrower than the disabled actionPromiseCorrection above: it
		// fires ONLY on an exact, token-bounded, snake_case tool NAME (those
		// don't occur in ordinary prose), only when NO tool fired this turn,
		// only on a reply short enough to be a lead-in, is capped by its own
		// correction budget, and the nudge gives an explicit "if you didn't
		// mean to, answer directly" out. Flip the const to disable if it
		// ever proves noisy.
		const toolMentionCorrection = true
		// Full-reply gate (double-emit prevention). This correction fires by
		// re-prompting, and re-prompting a round whose content ALREADY
		// streamed to the client makes the retry stream a SECOND time — the
		// "…What API?" + "Fair point, I was just describing…" double. That's
		// only worth the risk when the round is a genuine PREAMBLE ("Let me
		// get_joke") that plausibly meant to fire the tool. When the content
		// is a full reply that merely MENTIONS a tool in passing, it's
		// exposition, not a missed call — a complete answer never needed a
		// tool to exist, so re-prompting can only produce restated noise.
		// Gate on the same lead-in/full-answer cutoff the runner uses for
		// the analogous mis-emit case (leadInMaxLen, 600): only nudge when
		// the visible reply is short enough to be a lead-in. Source-side and
		// lossless — a skipped correction leaves the full answer standing.
		const noArgCorrectionMaxContentLen = 600
		contentIsPreamble := len(strings.TrimSpace(lr.rs.resp.Content)) <= noArgCorrectionMaxContentLen
		if toolMentionCorrection && contentIsPreamble && !lr.cfg.DisableToolMentionCorrection && !lr.toolFiredThisTurn {
			name, needsArgs := mentionedUncalledTool(lr.rs.resp.Content, lr.handlers, lr.toolDefs)
			if name != "" && !(lr.corrections.available(correctionToolMention) && lr.round < lr.maxRounds) {
				lr.noteUncorrected(correctionToolMention, "The reply again named a tool in prose without calling it; no further re-prompt was left to spend.")
			} else if name != "" {
				Debug("[agent_loop] tool %q named in prose without a call (needs_args=%v), re-prompting: correction %d/%d", name, needsArgs, lr.corrections.spend(correctionToolMention), maxCorrectionsPerKind)
				lr.emitDiag("tool-mention-corrected", fmt.Sprintf("The reply named the %q tool without calling it; re-prompted to either run it or answer plainly.", name))
				lr.settleRound() // finalize the preamble so the retry doesn't concatenate into it
				// Two different reasons nothing ran, and the model can only
				// fix the one it is told about. The parameterized wording
				// also has to say the tool IS available: the reply that
				// triggers this is often a refusal ("I don't have access to
				// those files"), and repeating the nudge without correcting
				// the premise just gets the refusal restated.
				why := "it takes no arguments, so there was nothing to run"
				if needsArgs {
					why = "naming a tool in text does not run it — the arguments have to travel in a real structured call"
				}
				lr.history = append(lr.history, Message{
					Role:    "user",
					Content: fmt.Sprintf(frameworkNoticeTag+"Your previous response referred to the %q tool but did not actually call it (%s). That tool IS available to you on this turn — do not say you lack access to what it reaches. If you intend to use it, emit the real structured tool call NOW. If you did NOT mean to use it, answer the user directly and do not claim you used it.", name, why),
				})
				return actContinue
			}
		}

		// Reasoning-collapse correction: Qwen-style models with
		// thinking enabled sometimes burn the entire budget on
		// reasoning and emit ~no visible content, while reporting
		// finish=stop. From the user's view: black hole — sent a
		// message, got nothing back. Detect: substantial reasoning
		// (>200 chars), EMPTY content (a bare stub like ""/"."/"…"
		// after trim), and no tool calls. Inject a corrective and
		// retry so the next round either produces text or calls a
		// tool. Budget-gated per kind (correctionBudget) so it
		// can't loop.
		//
		// The threshold is deliberately near-zero, NOT "short": a
		// complete short reply ("Yes.", "7:57 AM PDT.") is normal
		// for a thinking model, and this round's content has
		// ALREADY streamed to the client — re-prompting makes the
		// model repeat it, so the user watches the same sentence
		// render once per correction (and each retry re-bills the
		// full prompt). Only a round that showed nothing may retry.
		trimmedContent := strings.TrimSpace(lr.rs.resp.Content)
		collapsed := len(trimmedContent) < 3 && len(lr.rs.resp.Reasoning) > 200
		if collapsed {
			lr.noteUncorrected(correctionCollapse, "The round again produced no visible reply and called no tool; no further re-prompt was left to spend.")
		}
		if collapsed && lr.corrections.available(correctionCollapse) && lr.round < lr.maxRounds {
			Debug("[agent_loop] reasoning-collapse detected (reasoning=%d chars, content=%d chars), re-prompting: correction %d/%d", len(lr.rs.resp.Reasoning), len(trimmedContent), lr.corrections.spend(correctionCollapse), maxCorrectionsPerKind)
			lr.emitDiag("empty-round-retried", "A round produced reasoning but no visible reply and no tool call; re-prompted for concrete output.")
			lr.settleRound() // no-op when nothing streamed; keeps the discipline uniform across guards
			lr.history = append(lr.history, Message{
				Role:    "user",
				Content: frameworkNoticeTag + "Your previous round produced no visible reply (you reasoned but wrote nothing the user can see) and called no tool. Don't end a turn empty-handed: either produce concrete text now, or call a relevant tool. If the user's question is too vague to act on, ask a clarifying question.",
			})
			return actContinue
		}

		// Give-up-with-errors-pending catch. Model emitted no tool
		// calls and ~empty content while tool errors accumulated
		// earlier in this turn AND budget remains — the "I tried,
		// give up" pattern. The forced-final-answer rescue path
		// after the loop would otherwise paper over this with a
		// polite "here's what I did" summary instead of fixing the
		// underlying problem. Push back: inject a continuation
		// nudge that names the error count and the rounds remaining,
		// and re-loop. Budget-gated per kind (correctionBudget) so
		// pathological cases can't infinitely re-prompt.
		//
		// Triggers:
		//   - no tool calls THIS round
		//   - empty content (or nearly so — <30 chars after trim), OR a
		//     reply that only PROMISES the work (see replyStalledOnAPromise)
		//   - cumulative tool errors > 0
		//   - more than 5 rounds remain (don't push at the cap;
		//     the existing wrap-up message owns that case)
		//   - haven't already burned the correction budget
		// trimmedContent reuses the variable declared in the
		// reasoning-collapse check above — same scope, already trimmed.
		roundsLeft := lr.maxRounds - lr.round
		// A promise is the same give-up wearing a nicer hat, and it is the
		// worse of the two: an empty round shows the user nothing, while
		// "let me create this" reads as progress and ends the turn anyway.
		// Observed: two image backends errored, and the turn closed on "Got
		// it — let me create this. I'll blend Alex onto the picture of me
		// wasting away in the garage." Nothing followed. The user's next
		// message was "you forgot to attach the image."
		promised := replyStalledOnAPromise(trimmedContent)
		// Two ways a turn ends without doing the work, and only the first
		// was covered. The second arrived as a transcript: "On it — let me
		// grab some reference photos and composite them into that scene",
		// no tool call, turn over, nothing errored — so a guard keyed on
		// pending errors never looked. The errors were incidental to the
		// original sighting, not the thing that made it a stall.
		//
		// The carve-out that makes the second safe is !toolFiredThisTurn.
		// "I'll let you know when it's done" is the reply the detached-task
		// notice explicitly ASKS for, and a detached call is a tool call —
		// so a promise backed by work that actually started is left alone,
		// and only a promise backed by nothing gets pushed.
		stalledOnErrors := lr.cumulativeToolErrors > 0 && (len(trimmedContent) < 30 || promised)
		stalledOnNothing := promised && !lr.toolFiredThisTurn
		gaveUp := roundsLeft >= 5 && (stalledOnErrors || stalledOnNothing)
		if gaveUp {
			lr.noteUncorrected(correctionGiveUp, "The turn again stopped with tool errors unaddressed and rounds to spare; no further re-prompt was left to spend.")
		}
		if gaveUp && lr.corrections.available(correctionGiveUp) {
			Debug("[agent_loop] give-up-with-errors-pending detected (errors=%d, rounds_left=%d, content=%dch, promised=%v), re-prompting: correction %d/%d",
				lr.cumulativeToolErrors, roundsLeft, len(trimmedContent), promised, lr.corrections.spend(correctionGiveUp), maxCorrectionsPerKind)
			// Two failures, two messages. Telling a model to "re-read the
			// error messages" when nothing errored sends it hunting for a
			// problem that isn't there, and it will invent one.
			var diag, nudge string
			roundPlural := ""
			if roundsLeft != 1 {
				roundPlural = "s"
			}
			if stalledOnErrors {
				errPlural := ""
				if lr.cumulativeToolErrors != 1 {
					errPlural = "s"
				}
				stopped := "stopped without producing a reply and without calling any tool"
				diag = fmt.Sprintf("The turn stopped with %d unaddressed tool error(s) and rounds to spare; re-prompted to adjust and retry rather than give up.", lr.cumulativeToolErrors)
				if promised && len(trimmedContent) >= 30 {
					stopped = "ended your turn by saying you were ABOUT to do the work, and then called no tool"
					diag = fmt.Sprintf("The reply promised work it never did — it announced the next step, called no tool, and left %d tool error(s) unaddressed with rounds to spare. Re-prompted to actually do it.", lr.cumulativeToolErrors)
				}
				nudge = fmt.Sprintf(
					frameworkNoticeTag+"You %s, but %d tool call%s errored earlier this turn that you didn't follow up on, and you have %d round%s remaining. Saying what you are about to do is not doing it — the user sees the sentence and nothing else, and nothing runs after your turn ends. DON'T end here with a polite summary of what you tried — that's giving up. Re-read the most recent error message(s) carefully, ADJUST your approach (different args, different tool, different sequence), and TRY AGAIN with a real tool call. If you genuinely have no other avenues, say so explicitly — but only after you've actually tried adjusting at least once.",
					stopped, lr.cumulativeToolErrors, errPlural, roundsLeft, roundPlural)
			} else {
				diag = "The reply said the work was about to happen and then ended the turn without calling a single tool. Re-prompted to do it now or say plainly what is stopping it."
				nudge = fmt.Sprintf(
					frameworkNoticeTag+"You ended your turn saying you were about to do something, and then called no tool at all — so nothing happened. Nothing runs after your turn ends; the user is left holding a sentence. You have %d round%s remaining. Do it NOW with a real tool call, or say plainly what is stopping you. Do not repeat the promise, and do not apologize for it: do the work or explain why you can't.",
					roundsLeft, roundPlural)
			}
			lr.emitDiag("giveup-retried", diag)
			lr.settleRound() // no-op when nothing streamed; keeps the discipline uniform across guards
			lr.history = append(lr.history, Message{Role: "user", Content: nudge})
			return actContinue
		}

		// Pre-finalize injection drain. Mid-flight user notes are
		// normally picked up at round start, but a note that lands
		// DURING this final round would otherwise be lost — the loop
		// is about to return. Re-drain here: if anything is pending,
		// append it and do another round instead of finishing.
		// Uses InjectionDrain (NOT OnRoundStart) — InjectionDrain
		// empties its queue and returns nil when nothing's pending,
		// so this re-call terminates. OnRoundStart may return content
		// every call (budget pacer) and would loop forever here.
		if lr.cfg.InjectionDrain != nil && lr.round < lr.maxRounds {
			if injected := lr.cfg.InjectionDrain(); len(injected) > 0 {
				Debug("[agent_loop] pre-finalize injection: %d note(s) arrived during the final round — continuing instead of finishing", len(injected))
				lr.history = append(lr.history, injected...)
				return actContinue
			}
		}

		// A turn that called NOTHING leaves no trail but its own words, and
		// without them a failure cannot be diagnosed at all. Observed: "Wiwee,
		// try again" answered in 66 characters with zero tool calls — the
		// framework recorded the length and nothing else, so whether the reply
		// was an honest refusal or a fresh empty promise was unknowable after
		// the fact. Every OTHER shape of turn is reconstructable from its tool
		// calls; this one is not.
		//
		// Logged for the whole turn, not the round, so it fires once on the
		// reply that actually goes out. Masked sessions get lengths only —
		// MaskDebugOutput exists because some sessions carry credentials and
		// private files, and a diagnostic is not worth leaking them.
		if len(lr.turnToolCalls) == 0 {
			Debug("%s", noToolDiagLine(lr.round, LatestUserContent(lr.messages), lr.rs.resp.Content, lr.cfg.MaskDebugOutput))
		}

		// Turn judge: the reply is about to go out, so this is the last moment
		// anything can ask whether it is TRUE about what the turn did. Runs
		// after the phrase-list guards above have had their say and only when
		// the evidence warrants a model call — see turn_judge.go for the
		// pre-filter and why it is deliberately looser than the guards.
		//
		// Placed before the guardrail gate on purpose: a reply that claims work
		// it never did should be fixed before a warden spends a call judging
		// its content, and the correction below re-prompts anyway.
		if verdict, convicted := judgeTurnClaim(lr.cfg, TurnClaimEvidence{
			Request:       LatestUserContent(lr.messages),
			Reply:         lr.rs.resp.Content,
			ToolCalls:     lr.turnToolCalls,
			PriorWork:     lr.cfg.priorWork(),
			PriorReports:  lr.cfg.priorReports(),
			ToolErrors:    lr.cumulativeToolErrors,
			LastToolError: lr.lastToolError,
			Delivered:     lr.cfg.deliveredCount(),
			Backgrounded:  lr.cfg.backgrounded(),
			GivenEstimate: lr.cfg.backgroundEstimate(),
			Unattended:    lr.cfg.Unattended,
		}); convicted {
			// Two independent findings share one verdict, so each branch checks
			// its own. A machinery-only conviction reaching the claim branch
			// would tell the model its reply "did not happen" about a sentence
			// that was true.
			if verdict.Unkept && lr.corrections.available(correctionUnkeptClaim) && lr.round < lr.maxRounds {
				Debug("[agent_loop] turn judge: reply claims work the turn did not do (%q) — %s; re-prompting: correction %d/%d",
					truncForLog(verdict.Claim, 80), verdict.Why, lr.corrections.spend(correctionUnkeptClaim), maxCorrectionsPerKind)
				lr.emitDiag("unkept-claim-corrected", fmt.Sprintf("The reply said %q, which did not happen: %s. Re-prompted to do it or say so.", truncForLog(verdict.Claim, 120), verdict.Why))
				// Retract rather than settle: the claim is false and, on a
				// streaming surface, already painted. Same call as the phantom
				// guard makes about the same class of statement.
				lr.retractRound()
				lr.history[len(lr.history)-1] = Message{Role: "assistant", Content: lr.rs.resp.Content, Reasoning: lr.rs.resp.Reasoning}
				lr.history = append(lr.history, Message{
					Role: "user",
					Content: frameworkNoticeTag + fmt.Sprintf(
						"Your reply says: %q. That did not happen — %s. The user reads your words and gets nothing else; nothing runs after your turn ends. Either do it NOW with a real tool call, or rewrite the reply to say plainly what actually happened and what you could not do. Do not apologize, do not restate the claim, and do not promise it for later.",
						verdict.Claim, verdict.Why),
				})
				return actContinue
			}
			if verdict.Unkept && lr.corrections.exhausted(correctionUnkeptClaim) {
				lr.emitDiag("unkept-claim-uncorrected", fmt.Sprintf("The reply still says %q after correction, and it did not happen: %s. Delivered as written.", truncForLog(verdict.Claim, 120), verdict.Why))
			}
			// Machinery is a separate finding with a separate budget, because it
			// is a separate failure: the reply is usually TRUE and merely says
			// things nobody asked to hear. A rewrite fixes it, where a false
			// claim needs the work done or admitted — so it must not spend the
			// allowance the serious one might need in the same turn.
			if leak := strings.TrimSpace(verdict.Machinery); leak != "" && !verdict.Unkept {
				if lr.corrections.available(correctionMachinery) && lr.round < lr.maxRounds {
					Debug("[agent_loop] turn judge: reply explains machinery (%q); re-prompting: correction %d/%d",
						truncForLog(leak, 80), lr.corrections.spend(correctionMachinery), maxCorrectionsPerKind)
					lr.emitDiag("machinery-corrected", fmt.Sprintf("The reply explained how the work is being run (%q), which nobody asked about. Re-prompted for the same message without it.", truncForLog(leak, 120)))
					lr.retractRound()
					lr.history[len(lr.history)-1] = Message{Role: "assistant", Content: lr.rs.resp.Content, Reasoning: lr.rs.resp.Reasoning}
					lr.history = append(lr.history, Message{
						Role: "user",
						Content: frameworkNoticeTag + fmt.Sprintf(
							"Your reply says: %q. That is plumbing — how the work is being carried out — and they did not ask about it. Nothing else is wrong with the reply. Send the SAME message with that part removed: what you are doing for them, in one line, the way a person would. No ids, no mention of how or where anything runs, no invitation to check back, no time estimate you were not given.",
							leak),
					})
					return actContinue
				}
				if lr.corrections.exhausted(correctionMachinery) {
					lr.emitDiag("machinery-uncorrected", fmt.Sprintf("The reply still explains how the work is run (%q) after correction. Delivered as written.", truncForLog(leak, 120)))
				}
			}
		}

		// Grounding: the claim judge asked whether the turn DID what the reply
		// describes; this asks whether it KNOWS what the reply asserts. Only
		// notes the memory block marked unchecked are in scope, so a turn
		// carrying none never reaches a model call.
		//
		// The live note is built here rather than inline so the correction
		// below can recognise it: where the claim came FROM changes what the
		// rewrite should say, and calling a meme somebody posted seconds ago
		// a "stored note" is what produced a reply apologising for not having
		// checked a joke.
		liveNote := liveClaimNote(lr.cfg.LiveClaimSpeaker, LatestUserContent(lr.messages))
		if gv, convicted := judgeTurnGrounding(lr.cfg, TurnGroundingEvidence{
			Reply: lr.rs.resp.Content,
			// Stored notes plus, on a channel, whatever the person just
			// said. Composed here rather than by the host so the live entry
			// is worded the same way everywhere it is judged.
			Unchecked: withLiveClaim(lr.cfg.UncheckedClaims, lr.cfg.LiveClaimSpeaker, LatestUserContent(lr.messages)),
			ToolCalls: lr.turnToolCalls,
		}); convicted {
			if lr.corrections.available(correctionUngrounded) && lr.round < lr.maxRounds {
				Debug("[agent_loop] grounding judge: reply asserts an unchecked claim (%q) — re-prompting: correction %d/%d",
					truncForLog(gv.Claim, 80), lr.corrections.spend(correctionUngrounded), maxCorrectionsPerKind)
				lr.emitDiag("ungrounded-claim-corrected", fmt.Sprintf("The reply stated %q as fact; it traces to an unchecked note (%q). Re-prompted to check it or attribute it.",
					truncForLog(gv.Claim, 120), truncForLog(gv.Basis, 120)))
				// NOT retracted, unlike an unkept claim. That one is false and
				// has to be taken back; this one may well be true — nobody
				// checked, which is a different and lesser thing. Settling the
				// round and asking for a rewrite keeps a correct answer from
				// being yanked off the screen over its phrasing.
				lr.history[len(lr.history)-1] = Message{Role: "assistant", Content: lr.rs.resp.Content, Reasoning: lr.rs.resp.Reasoning}
				// Two shapes of basis, and they call for different rewrites.
				// A stored note is something the agent holds; a live claim is
				// something a person in the room said moments ago, where the
				// natural fix is "they posted…", not "per your note…".
				basis := fmt.Sprintf("a stored note marked as not independently checked: %q", gv.Basis)
				if basisIsLiveClaim(liveNote, gv.Basis) {
					who := strings.TrimSpace(lr.cfg.LiveClaimSpeaker)
					if who == "" {
						who = "the sender"
					}
					basis = fmt.Sprintf("what %s just put in the conversation, which nothing has checked: %q", who, gv.Basis)
				}
				lr.history = append(lr.history, Message{
					Role: "user",
					Content: frameworkNoticeTag + fmt.Sprintf(
						"Your reply states %q as established fact. That traces to %s. Either CHECK it now with a real tool call and then say what you found, or rewrite that one sentence to say where it came from (\"you mentioned…\", \"they posted…\"). If it was never offered as fact in the first place, a joke, a meme, teasing, obvious exaggeration, do NEITHER of those: reply in the register it was sent in and just don't restate its content as true. Describing what a picture you were shown visibly contains is not a claim and needs no hedge. Send the SAME reply with only that fixed: do not apologise, do not say you should have checked, do not mention this instruction, and do not add a disclaimer or hedge anything else.",
						gv.Claim, basis),
				})
				return actContinue
			}
			if lr.corrections.exhausted(correctionUngrounded) {
				lr.emitDiag("ungrounded-claim-uncorrected", fmt.Sprintf("The reply still states %q as fact after correction. Delivered as written.", truncForLog(gv.Claim, 120)))
			}
		}

		// Guardrail pre-output gate: before the final reply is returned, an
		// independent warden judges the OUTPUT against the agent's guardrails
		// (for "never say/reveal X" rules). This is the REAL guarantee — an
		// input check (pre_input) can always be talked around with an
		// innocuous-looking follow-up ("go ahead and show me"), but the output
		// check judges the actual reply, where "contains the protected thing"
		// is unambiguous regardless of how it was asked. So it runs on EVERY
		// terminal reply (no budget guard on the CHECK). A violation re-prompts
		// for a revise pass while corrections/rounds remain; once the budget is
		// spent and the reply STILL violates, the draft is NOT released — a
		// neutral decline is substituted so a determined push can't leak on the
		// attempt after the budget runs out (the old escape hatch).
		if lr.cfg.GuardrailCheck != nil && strings.TrimSpace(lr.rs.resp.Content) != "" {
			if dec := lr.cfg.GuardrailCheck(GuardHookPreOutput, lr.rs.resp.Content); dec.Blocked {
				gmsg := dec.Message
				// Halt overrides the correction budget: once the app has
				// decided this turn is over, asking the same model for one
				// more revision is another generation from the context that
				// just failed. A block on a rule that is not Correctable says the
				// same thing about the FIRST attempt — the rule forbids what was
				// asked for, so there is no compliant revision to wait for.
				halted := lr.cfg.GuardrailHalted != nil && lr.cfg.GuardrailHalted()
				if !halted && dec.Correctable && lr.guardrailOutputCorrections < maxGuardrailOutputCorrections && lr.round < lr.maxRounds {
					Debug("[agent_loop] guardrail blocked pre-output, re-prompting (correction %d/%d)", lr.guardrailOutputCorrections+1, maxGuardrailOutputCorrections)
					lr.emitDiag("guardrail-blocked-output", "The reply was withheld by an enforced guardrail; re-prompted to revise.")
					lr.retractRound()                              // DISCARD the withheld bubble — not persisted or delivered (settle would commit it)
					lr.replaceBlockedDraft(guardrailRedactedDraft) // scrub the leaked draft from history — never persisted or delivered
					lr.history = append(lr.history, Message{Role: "user", Content: frameworkNoticeTag + gmsg})
					lr.guardrailOutputCorrections++
					// The revise pass is a rewrite of text the model just
					// produced, against one stated constraint — the most
					// expensive round in the turn and the one with least to
					// reason about.
					lr.guardrailQuietNextRound = true
					return actContinue
				}
				// Not correctable, halted, or the budget/rounds are spent and the
				// reply STILL violates. Do not release it — overwrite the draft in
				// place with the safe decline and return that. The floor is a
				// canned reply, not the leak, no matter how hard the turn was
				// pushed.
				Debug("[agent_loop] guardrail pre-output final (correctable=%v halted=%v) — handing the reply to the rejection model", dec.Correctable, halted)
				lr.emitDiag("guardrail-output-substituted", "A reply kept violating an enforced guardrail; a neutral decline was substituted so nothing protected was released.")
				lr.retractRound() // DISCARD the leaking draft bubble; the safe reply below is what gets delivered
				fallback := guardrailRejectionReply(lr.cfg, "pre_output", lr.history)
				lr.replaceBlockedDraft(fallback)
				// The decline alone leaves the request looking OPEN. A model
				// reading back "user asked X / assistant brushed it off"
				// treats X as unfinished business and answers it at the next
				// opportunity — observed live: an agent asked the date
				// answered the refused question instead. The refusal has to
				// be recorded as a CLOSED outcome, not an evasion.
				//
				// Carried in a meta note because it is for the model and not
				// for the reader: StripMetaTags runs at every delivery
				// boundary (channel, web, phantom) while the persisted copy
				// keeps it, so the next turn sees it and the contact never
				// does.
				lr.rs.resp.Content = fallback + guardrailClosedNote
				lr.rs.resp.Reasoning = ""
				lr.rs.resp.ToolCalls = nil
				return lr.exit(lr.rs.resp, lr.history, nil)
			}
		}

		if lr.truncatedLead.Len() > 0 && lr.cfg.SettleRound == nil {
			// A caller with no SettleRound never displayed the cut-off
			// part — the loop is the only thing that saw it — so the reply
			// it gets has to carry it. (A caller WITH one already showed
			// the partial as its own bubble; handing it back joined would
			// render it twice.) Before this, every headless consumer of a
			// continued reply got the continuation alone: a report that
			// began mid-sentence, or, when the continuation was itself the
			// cut one, a report that simply ended short.
			lr.rs.resp.Content = joinContinuation(lr.truncatedLead.String(), lr.rs.resp.Content)
			Debug("[agent_loop] round %d: reply joined with %d chars cut off earlier this turn (caller never saw them)", lr.round, lr.truncatedLead.Len())
		}
		if lr.cfg.OnStep != nil {
			lr.cfg.OnStep(StepInfo{
				Round:   lr.round,
				Content: lr.rs.resp.Content,
				Done:    true,
			})
		}
		return lr.exit(lr.rs.resp, lr.history, nil)
	}
	return actNone
}

func (lr *loopRun) interimGuardrail() loopAction {
	// Guardrail periodic gate: judge this round's narration — the model's own
	// words as it works — against the guardrails. Catches what neither the
	// per-action nor the final-output check sees, because a round that carries
	// tool calls is never a "terminal reply" and pre_output never looks at it.
	//
	// EVERY round with narration, not every Nth. This used to sample on an
	// interval, and the rounds it skipped were a straight leak: the prose is
	// appended to history above and DELIVERED further down (the OnStep with
	// Done:false, which is what paints a mid-turn bubble), so an unsampled
	// round reached both the transcript and the user having never been judged.
	// An interval is defensible for a drift detector and indefensible for
	// containment, and a rule left blocking rather than correctable is an owner
	// plainly asking for containment.
	//
	// Per-round checking also fixes the scrubbing scope for free.
	// replaceBlockedDraft only rewrites the most recent assistant turn, which
	// was the right thing to reach for and the wrong thing to rely on while
	// earlier rounds went unchecked: a figure narrated two rounds before the
	// sample survived the scrub. Now a violation is always caught in the round
	// that produced it, so "the last assistant turn" IS the offending one.
	//
	// No budget guard on the CHECK. It used to stop running once the
	// correction budget was spent, the same escape hatch pre_output had to
	// have removed: the attempt after the budget ran out sailed through
	// unjudged. The budget now governs only whether a block may REDIRECT; when
	// it cannot, the turn hands over rather than releasing the prose.
	//
	// Skipped entirely where interim prose goes nowhere (see
	// InterimContentHidden): judging text that is discarded before anyone sees
	// it buys no containment and costs a model call per round. pre_output still
	// judges the reply that ships, which on such a path is the only text there
	// is.
	if lr.cfg.GuardrailCheck != nil && lr.cfg.InterimContentHidden && strings.TrimSpace(lr.rs.resp.Content) != "" && !lr.skippedInterimGuard {
		lr.skippedInterimGuard = true // once per turn; this is a property of the host, not the round
		Debug("[agent_loop] periodic guardrail skipped: this host does not show or store interim round prose (pre_output still judges the final reply)")
	}
	if lr.cfg.GuardrailCheck != nil && !lr.cfg.InterimContentHidden && strings.TrimSpace(lr.rs.resp.Content) != "" &&
		!lr.judgedNarration[lr.rs.resp.Content] {
		lr.judgedNarration[lr.rs.resp.Content] = true
		if dec := lr.cfg.GuardrailCheck(GuardHookPeriodic, lr.rs.resp.Content); dec.Blocked {
			gmsg := dec.Message
			lr.retractRound()
			lr.replaceBlockedDraft(guardrailRedactedDraft) // scrub the leaked narration (and its unexecuted tool calls) from history
			// A redirect asks the same model to carry on differently. It is
			// only available while there is budget and rounds left to carry on
			// INTO, the app hasn't halted the turn, and the rule that fired was
			// one a different course could satisfy. Otherwise the turn ends
			// here — the one thing that must never happen is releasing the
			// narration because there was no correction left to spend.
			canRedirect := dec.Correctable &&
				!(lr.cfg.GuardrailHalted != nil && lr.cfg.GuardrailHalted()) &&
				lr.guardrailOutputCorrections < maxGuardrailOutputCorrections &&
				lr.round < lr.maxRounds
			if !canRedirect {
				Debug("[agent_loop] guardrail periodic final at round %d (correctable=%v) — handing over to the rejection model", lr.round, dec.Correctable)
				lr.emitDiag("guardrail-halted", "An enforced guardrail stopped this turn; the reply was written by a separate check, not by the agent.")
				reply := guardrailRejectionReply(lr.cfg, GuardHookPeriodic, lr.history)
				lr.replaceBlockedDraft(reply)
				lr.rs.resp.Content = reply
				lr.rs.resp.Reasoning = ""
				lr.rs.resp.ToolCalls = nil
				return lr.exit(lr.rs.resp, lr.history, nil)
			}
			Debug("[agent_loop] guardrail periodic block at round %d, redirecting", lr.round)
			lr.emitDiag("guardrail-periodic-block", "An enforced guardrail flagged the turn mid-flight; redirected before running this round's tools.")
			lr.history = append(lr.history, Message{Role: "user", Content: frameworkNoticeTag + gmsg})
			lr.guardrailOutputCorrections++
			return actContinue
		}
	}
	return actNone
}

func (lr *loopRun) planToolWork() loopAction {
	// Execute tool calls and collect results.
	// Independent calls run in parallel; confirmable tools are
	// checked serially first to avoid concurrent prompts.
	//
	// Anything the round's ARGUMENTS are read against is pinned first: the
	// calls are settled, none has run, and no sibling has yet changed the
	// state a later one refers to.
	if lr.cfg.BeforeToolRound != nil {
		lr.cfg.BeforeToolRound()
	}
	lr.rs.results = make([]ToolResult, len(lr.rs.resp.ToolCalls))
	lr.rs.toolErrors = 0
	lr.rs.guardBlockedThisRound = false // set when the loop-guard blocks a repeat this round
	lr.rs.guardrailHalt = ""            // non-empty ⇒ the app halted the turn at this hook; end it after the round settles

	// stay_silent normalization. Two failure modes from real models
	// (Qwen 3 in particular):
	//   1. stay_silent bundled with a real tool — model treats it
	//      as a "no-reply" flag rather than a turn-closer.
	//   2. stay_silent called multiple times in one batch — model
	//      double-emits the closer.
	// Policy:
	//   - If the batch contains ONLY stay_silent calls (≥1), keep the
	//     first one and skip the rest. Silence the turn as intended.
	//   - If the batch mixes stay_silent with other tools, drop ALL
	//     stay_silent calls (with an instructive error) and run the
	//     real tools. The model can re-emit stay_silent alone next
	//     turn after seeing results.
	lr.rs.silentCount = 0
	lr.rs.realCount = 0
	for _, tc := range lr.rs.resp.ToolCalls {
		if tc.Name == "stay_silent" {
			lr.rs.silentCount++
		} else {
			lr.rs.realCount++
		}
	}
	lr.rs.dropAllSilent = lr.rs.silentCount > 0 && lr.rs.realCount > 0
	lr.rs.dedupeSilent = lr.rs.silentCount > 1 && !lr.rs.dropAllSilent
	lr.rs.silentSeen = false

	// batchSend claims recipients WITHIN this round — the sends a model
	// batches into one response (the "fired all 4 jokes at once" case) share
	// a round, so sentThisTurn (marked only post-execution) wouldn't yet see
	// the first when the second is checked. Claimed pre-execution here so the
	// second in the same batch is held too.
	lr.rs.batchSend = map[string]bool{}

	// In-batch identical-call dedup. batchSig maps a call signature to the
	// results index of the ONE call that will actually run; batchDup lists
	// {duplicate index, canonical index} pairs to fill in afterward.
	//
	// The repeatFail/repeatSame guards can't cover this: both counters are
	// updated AFTER the round, so every sibling in a batch reads the same
	// stale value and all of them pass. Observed live: a model emitted the
	// same tool_def(action="test") three times in one response and the
	// verifier — a genuine dispatch that really runs the tool — hit the
	// live network three times for one question.
	//
	// Identical name AND identical args in the SAME response is a
	// generation artifact, not intent: deliberate repetition varies
	// something (recipient, path, offset). The native Ollama transport
	// already collapses these at the wire (llm_openai.go), so this closes
	// the same hole for every other provider.
	lr.rs.batchSig = map[string]int{}
	if len(lr.rs.resp.ToolCalls) > maxToolCallsPerRound {
		lr.emitDiag("round-batch-capped", fmt.Sprintf("The model emitted %d tool calls in one round; only the first %d ran.", len(lr.rs.resp.ToolCalls), maxToolCallsPerRound))
		lr.rs.guardBlockedThisRound = true
	}
	// Identifiers this conversation has actually produced: everything the
	// user wrote, every tool result, and the system prompt (which carries
	// the appliance / agent / memory ids a turn is entitled to name). The
	// model's own prose is deliberately absent — that is where an invented
	// id is written, and treating it as a source would launder one.
	lr.rs.knownIDs = collectKnownIDs(lr.systemPrompt, lr.history)
	for i, tc := range lr.rs.resp.ToolCalls {
		if i >= maxToolCallsPerRound {
			lr.rs.results[i] = ToolResult{ID: tc.ID, Content: fmt.Sprintf("Error: round batch cap — a single round may fire at most %d tool calls; this call (#%d) was dropped. Use the results you already have, or continue next round with a SMALLER, deliberate batch.", maxToolCallsPerRound, i+1), IsError: true}
			lr.rs.toolErrors++
			continue
		}
		if tc.Name == "stay_silent" {
			if lr.rs.dropAllSilent {
				Debug("[agent_loop] stay_silent dropped — bundled with %d real tool call(s)", lr.rs.realCount)
				lr.rs.results[i] = ToolResult{
					ID:      tc.ID,
					Content: "Error: stay_silent was ignored because it was bundled with other tool calls. stay_silent closes the turn and must be the ONLY tool call in your response. Complete your other tool work first, observe the results, then call stay_silent alone in a later turn.",
					IsError: true,
				}
				lr.rs.toolErrors++
				continue
			}
			if lr.rs.dedupeSilent {
				if lr.rs.silentSeen {
					Debug("[agent_loop] duplicate stay_silent dropped (already silenced)")
					lr.rs.results[i] = ToolResult{
						ID:      tc.ID,
						Content: "Acknowledged (duplicate). The turn is already closing silently — only one stay_silent call is needed per turn.",
					}
					continue
				}
				lr.rs.silentSeen = true
			}
		}
		if lr.cfg.MaskDebugOutput {
			Debug("[agent_loop] tool call: %s([masked: %d bytes])", toolCallLabel(tc), len(formatArgs(tc.Args)))
		} else {
			Debug("[agent_loop] tool call: %s (args=%d bytes)", toolCallLabel(tc), len(formatArgs(tc.Args)))
			Trace("[agent_loop] tool call: %s(%s)", tc.Name, formatArgs(tc.Args))
		}

		handler, ok := lr.handlers[tc.Name]
		if !ok && lr.cfg.ToolFallbackResolver != nil {
			// The name isn't in this round's catalog, but it may be a
			// lazy tool whose handler is still valid (model knows the
			// schema from context and called it directly — no re-load).
			if fb, found := lr.cfg.ToolFallbackResolver(tc.Name); found {
				Debug("[agent_loop] tool %q resolved via fallback (lazy/known tool called directly)", tc.Name)
				handler, ok = fb, true
			}
		}
		if !ok {
			errMsg := fmt.Sprintf("Error: unknown tool '%s'", tc.Name)
			Debug("[agent_loop] %s", errMsg)
			lr.rs.results[i] = ToolResult{ID: tc.ID, Content: errMsg, IsError: true}
			lr.rs.toolErrors++
			continue
		}

		// Guardrail pre-action gate: before a CONSEQUENTIAL tool call
		// (the NeedsConfirm set — sends, posts, deletes, spends) runs, an
		// independent warden judges it against the agent's guardrails. A
		// violation blocks the call and hands back the app's trusted
		// message (never fenced). guardBlockedThisRound feeds the wedge
		// machinery so repeated blocks settle the turn.
		judgeThisCall := lr.needsConfirm[tc.Name]
		if !judgeThisCall && lr.cfg.GuardrailActionGate != nil {
			judgeThisCall = lr.cfg.GuardrailActionGate(tc.Name)
		}
		if lr.cfg.GuardrailCheck != nil && judgeThisCall {
			// Decision.Correctable is deliberately ignored here: a blocked call
			// still leaves the agent a compliant way to finish the task, so
			// this stays block-and-continue no matter which rule fired.
			// Ending the turn is the escalation counter's job.
			if dec := lr.cfg.GuardrailCheck(GuardHookPreAction, tc.Name+" "+formatArgsForGuardrail(tc.Args)); dec.Blocked {
				Debug("[agent_loop] guardrail blocked pre-action: %s", tc.Name)
				lr.rs.guardBlockedThisRound = true
				// The next round reads a refusal it did not expect and is
				// told not to name the mechanism. Deliberating on that is
				// how a blocked turn burns thousands of reasoning tokens
				// and emits nothing. Acting on the message is a short step.
				lr.guardrailQuietNextRound = true
				lr.rs.results[i] = ToolResult{ID: tc.ID, Content: dec.Message, IsError: true}
				lr.rs.toolErrors++
				// Blocking one call is not enforcement when the model can
				// simply pick a different route to the same end. If the app
				// says this turn is over, stop the whole turn — recorded here
				// and acted on once the round's remaining results settle, so
				// no half-executed batch is left behind.
				if lr.cfg.GuardrailHalted != nil && lr.cfg.GuardrailHalted() {
					lr.rs.guardrailHalt = "pre_action"
				}
				continue
			}
		}

		if lr.needsConfirm[tc.Name] {
			if !lr.confirmFn(tc.Name, formatArgs(tc.Args)) {
				Debug("[agent_loop] tool call denied by user: %s", tc.Name)
				// The bare "denied by user" line TAUGHT a workaround:
				// agents whose governed tool was denied on scheduled
				// fires learned to reach the same service through
				// ungoverned tools instead (hand-rolled fetch_url
				// against a guessed API — a habit that outlived the
				// gate). A denial denies the OPERATION, not one route
				// to it — say so.
				lr.rs.results[i] = ToolResult{ID: tc.ID, Content: "Error: tool call denied by user — this operation was not authorized to run. Do NOT work around the denial by attempting the same operation through a different tool (raw fetch_url, shell, or a dispatch); proceed without it, or report that it needs the owner's authorization.", IsError: true}
				lr.rs.toolErrors++
				continue
			}
		}

		// Invented-identifier gate: this call references a record by an id
		// nobody ever gave the model. Checked BEFORE the call runs, because
		// the service's own answer to a fabricated id is a 404 — which reads
		// as "that record is missing" or "the endpoint is broken", and is
		// acted on as either. Observed live: an agent invented a post id by
		// splicing the front of one real id onto the tail of another, got
		// 404 twice, and reported a routing bug in the API.
		if !lr.cfg.DisableIDProvenanceGate {
			if refusal := idProvenanceRefusal(tc.Name, tc.Args, lr.rs.knownIDs); refusal != "" {
				Debug("[agent_loop] id-provenance: %s blocked (argument id was never issued this session)", tc.Name)
				lr.rs.guardBlockedThisRound = true
				lr.emitDiag("invented-id", fmt.Sprintf("A call to '%s' referenced an id that nothing in this conversation produced; it was refused before it ran.", tc.Name))
				lr.rs.results[i] = ToolResult{ID: tc.ID, Content: refusal, IsError: true}
				lr.rs.toolErrors++
				continue
			}
		}

		// Action quota: this action has already run its allowance in the
		// last 24 hours. Refused before it runs, and counted below only
		// when it SUCCEEDS — a failed call consumed nothing anyone cares
		// about, and charging for it would end a day's budget on an
		// outage.
		if refusal, action := actionQuotaRefusal(lr.cfg, tc.Name, tc.Args); refusal != "" {
			Debug("[agent_loop] quota: %s blocked (%s is at its 24h allowance)", tc.Name, action)
			lr.rs.guardBlockedThisRound = true
			lr.emitDiag("action-quota", fmt.Sprintf("'%s' has used its allowance of %d per 24 hours; further calls were refused this turn.", action, lr.cfg.ActionQuotas[action]))
			lr.rs.results[i] = ToolResult{ID: tc.ID, Content: refusal, IsError: true}
			lr.rs.toolErrors++
			continue
		}

		// Repeated-failure loop-guard: this exact call (name+args) has
		// already errored repeatFailLimit times this turn — don't run it
		// again. Hand back a hard STOP so the model breaks the loop instead
		// of hammering the same dead end until the round budget is gone.
		sig := tc.Name + "\x00" + formatArgs(tc.Args)
		if lr.repeatFail[sig] >= repeatFailLimit {
			Debug("[agent_loop] loop-guard: %s blocked (%d prior identical failures this turn)", tc.Name, lr.repeatFail[sig])
			lr.rs.guardBlockedThisRound = true
			lr.rs.results[i] = ToolResult{
				ID:      tc.ID,
				Content: fmt.Sprintf("STOP — you have already called '%s' with these exact arguments %d times this turn and it failed the same way each time. Calling it again will NOT change the result. Do something different: try another approach or different arguments, or tell the user plainly that this isn't working and what you tried. Do not repeat this call.", tc.Name, lr.repeatFail[sig]),
				IsError: true,
			}
			lr.rs.toolErrors++
			continue
		}

		// Identical no-progress guard: this exact call keeps returning the
		// SAME result. Unlike the error guard above this fires on SUCCESS too,
		// catching a valid-but-pointless polling loop the error counter misses.
		if lr.repeatSame[sig] >= repeatSameLimit {
			Debug("[agent_loop] loop-guard: %s blocked (%d identical no-progress repeats this turn)", tc.Name, lr.repeatSame[sig])
			lr.rs.guardBlockedThisRound = true
			lr.rs.results[i] = ToolResult{
				ID:      tc.ID,
				Content: fmt.Sprintf("STOP — you have already called '%s' with these exact arguments %d times this turn and it returned the SAME result every time. It is giving you no new information and making no progress. Do NOT call it again. Answer the user with what you already have, use a DIFFERENT tool, or tell them plainly you cannot get what they asked for.", tc.Name, lr.repeatSame[sig]),
				IsError: true,
			}
			lr.rs.toolErrors++
			continue
		}

		// Duplicate-send guard: a second delivery to the same recipient this
		// turn is HELD, not sent. Catches the "drafted several messages and
		// fired them all" mistake that the identical-args guards miss (the
		// drafts differ). Keyed on recipient, not text.
		sendKey := ""
		if lr.cfg.SendGuardKey != nil {
			sendKey = lr.cfg.SendGuardKey(tc.Name, tc.Args)
		}
		if sendKey != "" && (lr.sentThisTurn[sendKey] || lr.rs.batchSend[sendKey]) {
			Debug("[agent_loop] send-guard: %s held (already sent to this recipient this turn)", tc.Name)
			lr.rs.guardBlockedThisRound = true
			lr.rs.results[i] = ToolResult{
				ID:      tc.ID,
				Content: fmt.Sprintf("HELD — you already sent a message to this recipient this turn via '%s', so this additional send was NOT delivered (it would double-message them). If you drafted several variations, that's expected: pick the ONE you want and send it on your NEXT turn. If you genuinely need to send a distinct follow-up, do it next turn, not batched with the first.", tc.Name),
				IsError: true,
			}
			lr.rs.toolErrors++
			continue
		}

		// Identical sibling already approved this batch — run it once and
		// copy the result. Claimed here, at the append, so a call that got
		// held by a guard above never becomes the canonical for a sibling
		// that would otherwise have run.
		if canon, dup := lr.rs.batchSig[sig]; dup {
			Debug("[agent_loop] batch-dedup: %s call #%d is identical to #%d — running once", tc.Name, i+1, canon+1)
			lr.rs.batchDup = append(lr.rs.batchDup, [2]int{i, canon})
			continue
		}
		lr.rs.batchSig[sig] = i

		if sendKey != "" {
			lr.rs.batchSend[sendKey] = true // claim so a same-batch duplicate is held
		}
		lr.rs.work = append(lr.rs.work, toolWork{index: i, tc: tc, handler: handler, sig: sig, sendKey: sendKey})
	}

	// RoundAbortTools: when a control tool (ask_user, respond_directly,
	// plan_set, …) is present in the batch, keep only the FIRST such
	// tool and drop everything else with a SKIPPED notice. The loop
	// will break after this round (handled below). This prevents the
	// LLM from bundling "ask the user a question" with "do the thing
	// anyway" in the same response.
	lr.rs.abortSet = map[string]bool{}
	for _, n := range lr.cfg.RoundAbortTools {
		lr.rs.abortSet[n] = true
	}
	lr.rs.roundAborted = false
	if len(lr.rs.abortSet) > 0 {
		abortIdx := -1
		for i, w := range lr.rs.work {
			if lr.rs.abortSet[w.tc.Name] {
				abortIdx = i
				break
			}
		}
		if abortIdx >= 0 {
			lr.rs.roundAborted = true
			abortName := lr.rs.work[abortIdx].tc.Name
			for i, w := range lr.rs.work {
				if i == abortIdx {
					continue
				}
				lr.rs.results[w.index] = ToolResult{
					ID:      w.tc.ID,
					Content: fmt.Sprintf("[SKIPPED] Tool '%s' was dropped because '%s' was called in the same response. Control tools (ask_user, respond_directly, plan_set, …) end the round — they must be the ONLY tool call. If you need to do other work first, do it in an earlier round.", w.tc.Name, abortName),
					IsError: true,
				}
				lr.rs.toolErrors++
			}
			lr.rs.work = []toolWork{lr.rs.work[abortIdx]}
			Debug("[agent_loop] round aborted by control tool %q — dropped %d other call(s)", abortName, len(lr.rs.resp.ToolCalls)-1)
		}
	}

	// Single-fire enforcement. Two sources, processed uniformly:
	//   1. cfg.SingleFireGroups — explicit cross-tool groups
	//      (e.g. {find_image, fetch_image, generate_image} all
	//      attach images; only one across the group fires).
	//   2. singleFireTools — per-tool flag set by tools that
	//      implement SingleFireTool. Each becomes an implicit
	//      one-element group.
	// Within each group, the first call in the batch runs; the
	// rest get a SKIPPED notice. Round CONTINUES (unlike
	// RoundAbortTools); the LLM can still produce a text reply.
	lr.rs.effectiveGroups = make([][]string, 0, len(lr.cfg.SingleFireGroups)+len(lr.singleFireTools))
	lr.rs.effectiveGroups = append(lr.rs.effectiveGroups, lr.cfg.SingleFireGroups...)
	for name := range lr.singleFireTools {
		lr.rs.effectiveGroups = append(lr.rs.effectiveGroups, []string{name})
	}
	for _, group := range lr.rs.effectiveGroups {
		if len(group) < 1 {
			continue
		}
		groupSet := map[string]bool{}
		for _, n := range group {
			groupSet[n] = true
		}
		firstIdx := -1
		var filtered []toolWork
		for _, w := range lr.rs.work {
			if !groupSet[w.tc.Name] {
				filtered = append(filtered, w)
				continue
			}
			if firstIdx < 0 {
				firstIdx = w.index
				filtered = append(filtered, w)
				continue
			}
			// Excess call from the same group — skip.
			skipMsg := fmt.Sprintf(
				"[SKIPPED] Tool '%s' was dropped because it had already been called in this batch (single-fire-per-batch). Only one call per batch is allowed for this tool. The first call's result stands; if the user needs another invocation, do it on a future turn.",
				w.tc.Name,
			)
			if len(group) > 1 {
				skipMsg = fmt.Sprintf(
					"[SKIPPED] Tool '%s' was dropped because another tool from its single-fire group already ran in this batch. Group: %v. Only one call across the group is allowed per batch. The first call's result stands; if more is needed, do it on a future turn.",
					w.tc.Name, group,
				)
			}
			lr.rs.results[w.index] = ToolResult{
				ID:      w.tc.ID,
				Content: skipMsg,
				IsError: true,
			}
			lr.rs.toolErrors++
		}
		if firstIdx >= 0 && len(filtered) < len(lr.rs.work) {
			Debug("[agent_loop] single-fire %v — dropped %d excess call(s)", group, len(lr.rs.work)-len(filtered))
			lr.rs.work = filtered
		}
	}

	// SerialTools: discard all but the first approved call so the LLM
	// must observe each result before deciding what to run next.
	if lr.cfg.SerialTools && len(lr.rs.work) > 1 {
		for _, w := range lr.rs.work[1:] {
			lr.rs.results[w.index] = ToolResult{
				ID:      w.tc.ID,
				Content: fmt.Sprintf("[SKIPPED] Submit one tool call at a time. Resubmit '%s' after reviewing the result above.", w.tc.Name),
			}
		}
		lr.rs.work = lr.rs.work[:1]
	}
	return actNone
}

// Second pass: execute approved tool calls in parallel.
func (lr *loopRun) debugResult(name, output string) {
	if lr.cfg.MaskDebugOutput {
		Debug("[agent_loop] tool result: %s: [masked: %d bytes]", name, len(output))
	} else {
		Debug("[agent_loop] tool result: %s (%d bytes)", name, len(output))
		Trace("[agent_loop] tool result: %s: %s", name, output)
	}
}

func (lr *loopRun) debugToolErr(name string, err error) {
	if lr.cfg.MaskDebugOutput {
		Debug("[agent_loop] tool error: %s: [masked]", name)
	} else {
		Debug("[agent_loop] tool error: %s: %s", name, err)
	}
}

func (lr *loopRun) dispatchTools() loopAction {
	if len(lr.rs.work) > 0 {
		lr.toolFiredThisTurn = true
	}
	if len(lr.rs.work) == 1 {
		// Single call — no goroutine overhead.
		w := lr.rs.work[0]
		output, err := safeInvoke(w.tc.Name, w.handler, w.tc.Args)
		if err != nil {
			lr.debugToolErr(toolCallLabel(w.tc), err)
			lr.rs.results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
			lr.rs.toolErrors++
		} else {
			lr.debugResult(toolCallLabel(w.tc), output)
			lr.rs.results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
			chargeActionQuota(lr.cfg, w.tc.Name, w.tc.Args)
		}
	} else if len(lr.rs.work) > 1 {
		var wg sync.WaitGroup
		var errCount int32
		invokeStore := func(w toolWork) {
			output, err := safeInvoke(w.tc.Name, w.handler, w.tc.Args)
			if err != nil {
				lr.debugToolErr(toolCallLabel(w.tc), err)
				lr.rs.results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
				atomic.AddInt32(&errCount, 1)
			} else {
				lr.debugResult(toolCallLabel(w.tc), output)
				lr.rs.results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
				chargeActionQuota(lr.cfg, w.tc.Name, w.tc.Args)
			}
		}
		// Partition into LANES. Calls sharing a lane run SEQUENTIALLY in
		// submission order, so a stateful authoring batch like
		// tool_def[delete X, create Y] applies in the order the LLM
		// intended and can't race on the same record. Everything else
		// still runs in parallel. work is already in submission order, so
		// one ordered goroutine per lane preserves it while the unlaned
		// calls fan out.
		//
		// Plain serial-fire tools all share the one unnamed lane, which is
		// what they did when that lane was the only one: two serial tools
		// in a batch stay ordered against each other, not just against
		// themselves. A BatchLane function opts a tool into finer
		// partitioning — same guarantee within a lane, concurrency across
		// lanes — and its keys are namespaced by tool name so two tools'
		// lane functions cannot collide on a shared string.
		lanes := map[string][]toolWork{}
		var laneOrder []string
		for _, w := range lr.rs.work {
			lane, laned := "", false
			if fn := lr.batchLaneFns[w.tc.Name]; fn != nil {
				laned = true
				if key := fn(w.tc.Args); key != "" {
					lane = w.tc.Name + "\x00" + key
				}
			} else if lr.serialFireTools[w.tc.Name] {
				laned = true
			}
			if !laned {
				wg.Add(1)
				go func(w toolWork) {
					defer wg.Done()
					invokeStore(w)
				}(w)
				continue
			}
			if _, seen := lanes[lane]; !seen {
				laneOrder = append(laneOrder, lane)
			}
			lanes[lane] = append(lanes[lane], w)
		}
		for _, key := range laneOrder {
			wg.Add(1)
			go func(items []toolWork) {
				defer wg.Done()
				for _, w := range items {
					invokeStore(w)
				}
			}(lanes[key])
		}
		wg.Wait()
		lr.rs.toolErrors += int(atomic.LoadInt32(&errCount))
	}

	// Satisfy the deduped siblings from the canonical call's result. The API
	// needs a result per tool_call id, so these can't just be omitted. They
	// carry the SAME content (the model gets consistent data, not an error
	// it has to reconcile) behind a one-line note, so a model that meant to
	// vary the args can see that it didn't. IsError is copied but the error
	// is NOT re-counted — one call ran, so one outcome is the honest count.
	for _, d := range lr.rs.batchDup {
		dup, canon := d[0], d[1]
		src := lr.rs.results[canon]
		lr.rs.results[dup] = ToolResult{
			ID:      lr.rs.resp.ToolCalls[dup].ID,
			Content: fmt.Sprintf("[DUPLICATE CALL — you issued this exact call %d times in one response; it ran ONCE and every copy returns the same result below. To get something different, change the arguments.]\n\n%s", countBatchDupes(lr.rs.batchDup, canon)+1, src.Content),
			IsError: src.IsError,
		}
	}

	// Update the repeated-failure loop-guard from this round's outcomes:
	// bump the per-signature error count on failure, reset it on success
	// (so legitimate polling that finally changes isn't penalized).
	for _, w := range lr.rs.work {
		if w.sig == "" {
			continue
		}
		if lr.rs.results[w.index].IsError {
			lr.repeatFail[w.sig]++
		} else {
			delete(lr.repeatFail, w.sig)
			// Success half of the failure-streak damper: this tool worked,
			// so its earlier failure results are stale — rewrite them to
			// resolved markers before the model has to arbitrate between
			// "it's broken" (repeated) and "it works" (said once).
			if shapes := lr.toolFailShapes[w.tc.Name]; len(shapes) > 0 {
				if n := retireResolvedFailureResults(lr.history, shapes, w.tc.Name); n > 0 {
					Debug("[agent_loop] failure-streak collapse: %s succeeded — %d earlier failure result(s) marked resolved", w.tc.Name, n)
				}
				delete(lr.toolFailShapes, w.tc.Name)
			}
		}
		// Mark the recipient reached only on a SUCCESSFUL send — a failed
		// delivery shouldn't block a legitimate retry to the same recipient.
		if w.sendKey != "" && !lr.rs.results[w.index].IsError {
			lr.sentThisTurn[w.sendKey] = true
		}
		// Identical-repeat tracking (see repeatSame decl). Count byte-identical
		// results per signature on success OR error; a changed result resets.
		if prev, seen := lr.lastToolContent[w.sig]; seen && prev == lr.rs.results[w.index].Content {
			lr.repeatSame[w.sig]++
		} else {
			lr.repeatSame[w.sig] = 0
		}
		lr.lastToolContent[w.sig] = lr.rs.results[w.index].Content
	}

	// Failure-SHAPE bookkeeping (see errShapeCount decl). Counts how many
	// times one normalized failure text has come back this turn, across
	// ANY call that produced it — the signal the signature-keyed guards
	// above miss when the model varies its arguments between attempts.
	for _, w := range lr.rs.work {
		if !lr.rs.results[w.index].IsError {
			continue
		}
		shape := normalizeFailureShape(lr.rs.results[w.index].Content)
		if shape == "" {
			continue
		}
		lr.errShapeCount[shape]++
		n := lr.errShapeCount[shape]
		if m := lr.toolFailShapes[w.tc.Name]; m == nil {
			lr.toolFailShapes[w.tc.Name] = map[string]bool{shape: true}
		} else {
			m[shape] = true
		}
		// Streak damper: from the errShapeCollapseAt-th recurrence on,
		// collapse the earlier duplicates in the accumulated history.
		// The first occurrence stays full; this round's copy is appended
		// after this loop, so the model always sees first + latest.
		if n >= errShapeCollapseAt {
			if c := collapseRepeatedFailureResults(lr.history, shape, false); c > 0 {
				Debug("[agent_loop] failure-streak collapse: %q — %d earlier duplicate result(s) collapsed", oneLineShape(shape), c)
			}
		}
		// Say it plainly, once. The model can see each failure but not
		// that it has now hit the SAME one from several directions —
		// which is the fact that should change its approach.
		if n >= errShapeNudgeAt && !lr.errShapeNudged[shape] {
			lr.errShapeNudged[shape] = true
			Debug("[agent_loop] failure-shape guard: %q seen %d times this turn — nudging", oneLineShape(shape), n)
			msg, consulted := failureShapeCorrection(n, oneLineShape(shape), lr.rs.results[w.index].Content, lr.cfg.Consult)
			// DEFERRED, not appended here. We are between the assistant
			// message that carried the tool calls and the tool-results
			// message appended below, and a tool result must directly
			// follow an assistant-or-tool message. Slipping a plain user
			// turn into that gap makes the provider's chat template reject
			// the whole request — llama.cpp returns a hard 400 ("A tool
			// message must follow an assistant or tool message") and the
			// turn dies with loop_error, so the guard meant to rescue a
			// struggling turn killed it instead. Queue it and let it land
			// after the results.
			lr.rs.pendingCorrections = append(lr.rs.pendingCorrections, Message{Role: "user", Content: frameworkNoticeTag + msg})
			if consulted {
				Log("[agent_loop] failure-shape guard: consulted on %q after %d hits", oneLineShape(shape), n)
				if lr.cfg.OnDiag != nil {
					lr.cfg.OnDiag("consulted", fmt.Sprintf("Hit the same failure %d times (%q) — a stronger model was consulted and its advice was given to the agent.", n, oneLineShape(shape)))
				}
			}
		}
		// Still hitting it. The turn has stopped being worth frontier
		// tokens — finish it on the worker. De-escalating rather than
		// terminating keeps the failure mode safe: worst case on a false
		// positive is a cheaper model, not a truncated turn.
		if n >= errShapeDeescalateAt && lr.deescalated == "" {
			lr.deescalated = "no-progress"
			Log("[agent_loop] failure-shape guard: %q hit %d times with no progress — remaining rounds run on the worker tier", oneLineShape(shape), n)
			if lr.cfg.OnDiag != nil {
				lr.cfg.OnDiag("tier_deescalated", fmt.Sprintf("Hit the same failure %d times with no progress (%q) — the rest of this turn ran on the worker model instead of the lead model.", n, oneLineShape(shape)))
			}
		}
	}

	// BREADCRUMB: tool dispatch complete. If we see this line but
	// no subsequent "round N+1: starting", the hang is in the
	// bookkeeping/OnStep/iteration-restart path. Log-level (not
	// Debug) so it surfaces regardless of debug flags.
	Log("[agent_loop] round %d: tool dispatch complete (%d tools, %d errors) — appending results to history", lr.round, len(lr.rs.work), lr.rs.toolErrors)
	// Add tool results to history for the next LLM round.
	lr.history = append(lr.history, Message{
		Role:        "user",
		ToolResults: lr.rs.results,
	})
	// Now the deferred corrections — after the results, where a plain user
	// turn is legal and the model reads them as commentary on what it just
	// saw rather than as an interruption of the tool exchange.
	lr.history = append(lr.history, lr.rs.pendingCorrections...)
	return actNone
}

func (lr *loopRun) settleToolRound() loopAction {
	// If a tool queued images for the model to look at, inject them as a
	// vision message NOW so the next round actually sees them. Producers:
	// view_video (samples frames from a clip) and generate_image (shows the
	// model its own output so it can verify the result matches the request).
	// Without this the bytes were extracted and dropped, and the model
	// hallucinated a description of something it never saw. Goes right after
	// the tool results — the order is assistant-tool_calls -> tool_results ->
	// the images it asked to see — and the wording is producer-agnostic: the
	// preceding tool result says what the images are.
	if lr.cfg.DrainViewImages != nil {
		if imgs := lr.cfg.DrainViewImages(); len(imgs) > 0 {
			lr.history = append(lr.history, Message{
				Role:    "user",
				Content: viewImageNote(imgs),
				Images:  viewImageBytes(imgs),
			})
		}
	}
	lr.prevHadToolCalls = true

	// Failure-streak bookkeeping. A round counts as a "failure"
	// when EVERY tool result this round has IsError=true. Any
	// successful result resets the streak. After N consecutive
	// failure rounds, inject the pivot nudge once per streak.
	// A pre_action halt ends the turn here — after the round's results are
	// assembled (so nothing is left half-executed) and before the model is
	// asked for another word. The blocked round is retracted and the reply
	// comes from the rejection model, never from the context that just
	// tripped the rule.
	if lr.rs.guardrailHalt != "" {
		Debug("[agent_loop] guardrail halt at %s — ending the turn, handing over to the rejection model", lr.rs.guardrailHalt)
		lr.emitDiag("guardrail-halted", "An enforced guardrail stopped this turn; the reply was written by a separate check, not by the agent.")
		lr.retractRound()
		reply := guardrailRejectionReply(lr.cfg, lr.rs.guardrailHalt, lr.history)
		lr.replaceBlockedDraft(reply)
		lr.rs.resp.Content = reply
		lr.rs.resp.Reasoning = ""
		lr.rs.resp.ToolCalls = nil
		return lr.exit(lr.rs.resp, lr.history, nil)
	}

	lr.rs.allFailed = len(lr.rs.results) > 0
	for i := range lr.rs.results {
		if !lr.rs.results[i].IsError && !isGuardStopResult(lr.rs.results[i].Content) {
			lr.rs.allFailed = false
			break
		}
		if isGuardStopResult(lr.rs.results[i].Content) {
			// Inner guards (the agents tool's dispatch ceiling, the
			// identical-dispatch check) return their STOP verdict as a
			// SUCCESSFUL result string — without this, a wall of STOPs
			// read as progress, the wedge streak reset every round, and
			// the model could burn hundreds of rounds re-dispatching
			// into the same ceiling (observed with 120+ blocked
			// Comedian dispatches).
			lr.rs.guardBlockedThisRound = true
		}
	}
	if lr.rs.allFailed {
		lr.failureStreak++
		if !lr.failureStreakWarned && lr.failureStreak >= failureStreakThreshold {
			Debug("[agent_loop] failure streak hit %d — injecting pivot nudge", lr.failureStreak)
			lr.history = append(lr.history, Message{
				Role: "user",
				Content: fmt.Sprintf(
					frameworkNoticeTag+"You've hit %d rounds in a row where every tool call failed. Recommending checking other vectors first before resuming this approach — a different tool, a different angle, or asking the user for clarification is often faster than continuing to iterate here.",
					lr.failureStreak),
			})
			lr.failureStreakWarned = true
		}
	} else {
		if lr.failureStreak > 0 {
			Debug("[agent_loop] failure streak reset (was %d) after successful tool call", lr.failureStreak)
		}
		lr.failureStreak = 0
		lr.failureStreakWarned = false
	}

	// Wedge break-out: a round whose only tool activity was a loop-guard-BLOCKED
	// call (blocked this round AND every result errored) is pure spinning — the
	// model is re-issuing the dead call and ignoring the STOP directive. After a
	// couple of these in a row, stop looping and force a clean final answer
	// instead of burning the rest of the budget. (Any successful tool call resets
	// the streak via the else branch.)
	if lr.rs.guardBlockedThisRound {
		lr.shakeoutNextRound = true
	}
	if lr.rs.guardBlockedThisRound && lr.rs.allFailed {
		// The blocked call was read out of the model's own prose and
		// the answer that prose came from is still in hand — so the
		// wedge would spend a whole extra generation rebuilding
		// something we already have. Return it and end the turn.
		// (Observed: 39s and 3162 output tokens to regenerate a
		// finished 8366-char answer.) Only fires when the round's
		// ONLY calls were synthesized and every one of them failed,
		// so a real tool call is never short-circuited.
		if lr.synthesizedFrom != "" {
			Debug("[agent_loop] loop-guard: blocked call was synthesized from prose — returning the model's own answer (%d chars) instead of regenerating", len(lr.synthesizedFrom))
			lr.rs.resp.Content = lr.synthesizedFrom
			lr.rs.resp.ToolCalls = nil
			return lr.exit(lr.rs.resp, lr.history, nil)
		}
		lr.guardBlockedStreak++
		if lr.guardBlockedStreak >= guardBlockedBreakLimit {
			Debug("[agent_loop] loop-guard wedge: %d blocked-with-no-progress rounds — forcing final answer", lr.guardBlockedStreak)
			lr.forceFinal = true
			return actBreak
		}
	} else {
		lr.guardBlockedStreak = 0
	}

	// keep_going spin guard (declared above). A round whose ONLY tool
	// call(s) were keep_going is a promise-to-act with no action. First
	// repeat gets a firm corrective injected; a further repeat forces the
	// final answer so the model can't burn the budget re-promising.
	lr.rs.keepGoingOnly = len(lr.rs.resp.ToolCalls) > 0
	for _, tc := range lr.rs.resp.ToolCalls {
		if tc.Name != "keep_going" {
			lr.rs.keepGoingOnly = false
			break
		}
	}
	if lr.rs.keepGoingOnly && lr.cfg.backgrounded() {
		// Nothing to keep going TO. A detached job delivers on its own, in its
		// own message, minutes from now — so a turn whose only remaining move
		// is "give me another round" has already done everything it can, and
		// every further round is dead time the user spends watching a spinner.
		//
		// Ended immediately rather than counted, because the streak guard below
		// cannot reach this shape: it resets on ANY tool call, and a model
		// waiting on a render fills the gaps with workspace(ls). Observed as
		// keep_going, keep_going, ls, ls, keep_going, keep_going, keep_going —
		// seven rounds and twenty seconds to arrive exactly where round one
		// already was.
		Debug("[agent_loop] keep_going while a background job is outstanding — nothing to continue to, finalizing")
		lr.emitDiag("keep-going-while-detached", "The turn asked for another round while a background job was still running. There is nothing to wait for in-turn — the result arrives on its own — so the turn was finalized instead of spinning.")
		lr.forceFinal = true
		return actBreak
	}
	if lr.rs.keepGoingOnly {
		lr.keepGoingStreak++
		if lr.keepGoingStreak >= keepGoingSpinLimit {
			Debug("[agent_loop] keep_going spin: %d consecutive keep_going-only rounds — forcing final answer", lr.keepGoingStreak)
			lr.forceFinal = true
			return actBreak
		}
		// One firm nudge before the force-final: keep_going fired but no
		// real tool, so the promise-correction path never ran.
		lr.history = append(lr.history, Message{
			Role:    "user",
			Content: frameworkNoticeTag + "You have signalled continue without taking any action. Do NOT call keep_going again. This round, either emit the ACTUAL tool call you intend (the tool is already loaded — call it directly), or, if you cannot, give your final answer to the user now.",
		})
	} else {
		lr.keepGoingStreak = 0
	}

	if lr.cfg.OnStep != nil {
		lr.cfg.OnStep(StepInfo{
			Round:      lr.round,
			Content:    lr.rs.resp.Content,
			ToolCalls:  lr.rs.resp.ToolCalls,
			ToolErrors: lr.rs.toolErrors,
			Done:       false,
		})
	}
	lr.cumulativeToolErrors += lr.rs.toolErrors
	// Evidence for the turn judge: what ran, and what the last failure said.
	// Duplicates are kept on purpose — three image calls are three attempts,
	// and a judge that sees one of them is reading a different turn.
	for _, w := range lr.rs.work {
		// The LABEL, not the bare name: a grouped tool's read and its write
		// share a name, and "moltbook ran nine times" is consistent with a
		// reply claiming three posts. "moltbook/get_feed" is not.
		lr.turnToolCalls = append(lr.turnToolCalls, toolCallLabel(w.tc))
		if w.index < len(lr.rs.results) && lr.rs.results[w.index].IsError {
			lr.lastToolError = lr.rs.results[w.index].Content
		}
	}

	// stay_silent closes the turn. The "do not call any more tools"
	// instruction in the tool result is unreliable — Qwen 3 in
	// particular keeps emitting stay_silent over and over. Once the
	// model has called stay_silent successfully, break the agent
	// loop server-side so no further LLM rounds happen.
	for _, w := range lr.rs.work {
		if w.tc.Name == "stay_silent" && !lr.rs.results[w.index].IsError {
			Debug("[agent_loop] stay_silent fired — closing turn")
			// Honor the suppression — stay_silent's whole purpose. Blank the
			// reply text so every caller (web reply, channel outbound,
			// dispatch result) emits NOTHING; attachments gathered this turn
			// still flow via their own path. Without this the Silenced flag
			// was set but never consumed, so stay_silent closed the turn yet
			// the model's text still showed ("stay_silent doesn't work").
			if lr.rs.resp != nil {
				lr.rs.resp.Content = ""
			}
			return lr.exit(lr.rs.resp, lr.history, nil)
		}
	}

	// RoundAbortTools: if a control tool fired successfully, close the
	// loop server-side. The orchestrate flow uses cancelOrch() in the
	// handler too, but that races against the in-flight tool batch; this
	// is the deterministic stop.
	if lr.rs.roundAborted {
		for _, w := range lr.rs.work {
			if lr.rs.abortSet[w.tc.Name] && !lr.rs.results[w.index].IsError {
				Debug("[agent_loop] control tool %q fired — closing turn", w.tc.Name)
				return lr.exit(lr.rs.resp, lr.history, nil)
			}
		}
	}
	return actNone
}
