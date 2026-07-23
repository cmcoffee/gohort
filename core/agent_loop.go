package core

import (
	"context"
	"encoding/json"
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
	return handler(args)
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

// failureShapeCorrection builds the message injected when a turn has hit the
// same failure shape repeatedly. Two forms:
//
//   - With a working consult: the wall's raw text goes to a model that can
//     ANSWER, and its reply rides back as explicitly-labelled advice. Telling a
//     stuck model "stop retrying variations" only names the problem; advice can
//     supply the fix.
//   - Without one (nil, error, or an empty reply): the generic directive, which
//     is still correct on its own. A consult that fails must never cost the
//     turn its correction.
//
// Advice is labelled advice in both the prose and the ADVICE prefix. Both tiers
// have been confidently wrong about the same API inside one session, so the
// model is told to verify before reporting anything as working.
//
// Returns the message and whether a consult actually supplied it.
func failureShapeCorrection(n int, shape, evidence string, consult func(question, evidence string) (string, error)) (string, bool) {
	if consult != nil {
		q := fmt.Sprintf("An agent has hit this same failure %d times from different calls and different arguments, so its arguments are not what is wrong. Diagnose the failure itself and give the concrete fix — exact field names and nesting if this is a request-shape problem. If the evidence does not settle it, say so and name what would.", n)
		if advice, err := consult(q, evidence); err == nil && strings.TrimSpace(advice) != "" {
			return fmt.Sprintf(
				"You have hit this SAME failure %d times this turn: %q. The arguments are not what's wrong, so a stronger model was consulted with the failure text. Its ADVICE follows — it is advice, not fact: apply it and VERIFY with a real call before reporting anything as working.\n\n%s",
				n, shape, strings.TrimSpace(advice)), true
		} else if err != nil {
			Debug("[agent_loop] failure-shape guard: consult failed, falling back to directive: %v", err)
		}
	}
	return fmt.Sprintf(
		"You have now hit this SAME failure %d times this turn, from different calls and different arguments: %q. The arguments are not what's wrong. Stop retrying variations of it — diagnose the failure itself, take a different approach, or tell the user plainly what is blocked and what you tried.",
		n, shape), false
}

// normalizeFailureShape reduces a failed tool result to a comparable
// fingerprint of WHAT went wrong, so the same wall is recognized across
// different calls and different arguments. Case and whitespace are flattened,
// a leading "error:" is dropped, and long digit runs (ids, timestamps, ports)
// collapse to "#" so one failure with a rotating id is still one shape. Only
// the head of the message is kept — the first line or two carries the failure;
// the tail is usually a stack or a body echo that varies harmlessly.
//
// Returns "" for a result too short to fingerprint, which the caller skips.
func normalizeFailureShape(content string) string {
	s := strings.ToLower(strings.TrimSpace(content))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "error: ")
	s = strings.TrimPrefix(s, "error:")
	var b strings.Builder
	var digits []rune
	// Long runs collapse to "#" (a rotating id shouldn't split one wall into
	// many); short ones are written back VERBATIM — "exit status 1" and "exit
	// status 2" are different failures, and the shape is quoted back to the
	// model in the nudge, so it must not misreport what it saw.
	flushDigits := func() {
		if len(digits) >= 4 {
			b.WriteByte('#')
		} else {
			for _, d := range digits {
				b.WriteRune(d)
			}
		}
		digits = digits[:0]
	}
	lastSpace := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, r)
			lastSpace = false
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flushDigits()
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			flushDigits()
			b.WriteRune(r)
			lastSpace = false
		}
	}
	flushDigits()
	out := strings.TrimSpace(b.String())
	if len(out) < 12 { // too generic to be a meaningful fingerprint
		return ""
	}
	if len(out) > 160 {
		out = out[:160]
	}
	return out
}

// oneLineShape renders a failure shape for a log line or a directive.
func oneLineShape(shape string) string {
	if len(shape) > 120 {
		return shape[:120] + "…"
	}
	return shape
}

// AgentToolDef combines a tool definition with its handler.
type AgentToolDef struct {
	Tool    Tool
	Handler ToolHandlerFunc

	// NeedsConfirm indicates that this tool requires user approval before
	// execution. When true, the agent loop will display the tool name and
	// arguments and prompt the user to allow or deny the call.
	NeedsConfirm bool

	// SingleFirePerBatch indicates that only ONE call to this tool may run
	// per batch. When the LLM emits multiple parallel calls in one
	// response, only the first runs; the rest get a SKIPPED notice. The
	// round CONTINUES — this isn't a round-abort. Use for tools where
	// multi-fire-per-batch is structurally wrong (authoring actions,
	// outbound communication, resource creation).
	SingleFirePerBatch bool

	// SerialFirePerBatch indicates that batched calls to this tool are a
	// SEQUENCE: they all run, but sequentially in submission order (each
	// observing the prior's mutations), instead of the single-fire skip.
	// Other tools in the same batch still run in parallel. Use for stateful
	// authoring tools where [delete X, create Y] is a legit two-step edit
	// (tool_def). Takes precedence over SingleFirePerBatch if both are set.
	SerialFirePerBatch bool
}

// ConfirmFunc is called to ask the user whether a tool call should proceed.
// It receives the tool name and a human-readable summary of the arguments.
// Return true to allow, false to deny.
type ConfirmFunc func(toolName string, argsSummary string) bool

// StepInfo provides observability into each round of the agent loop.
type StepInfo struct {
	Round      int        // Current round number (1-based).
	Content    string     // Text content from the LLM this round.
	ToolCalls  []ToolCall // Tool calls the LLM requested this round.
	ToolErrors int        // Number of tool calls that returned errors.
	Done       bool       // True if this is the final round (no more tool calls).
}

// StepCallback is called after each round of the agent loop for observability.
type StepCallback func(step StepInfo)

// AgentLoopConfig configures a RunAgentLoop invocation.
type AgentLoopConfig struct {
	// SystemPrompt sets the system prompt for the LLM.
	SystemPrompt string

	// Tools defines the tools available to the LLM and their handlers.
	Tools []AgentToolDef

	// MaxRounds limits how many LLM call rounds before stopping. Default 10.
	MaxRounds int

	// OnStep is called after each LLM round for logging/observability. Optional.
	OnStep StepCallback

	// OnDiag is called when the loop makes a silent framework decision on the
	// user's behalf mid-turn — chiefly the correction guards below that inject
	// a hidden corrective message and re-prompt (no-arg tool named in prose,
	// tool-call written as text markup, empty reasoning-collapse round, giving
	// up with tool errors pending). These otherwise vanish into Debug logs; a
	// silent guard that alters the turn but leaves no trace is exactly what the
	// per-session ⚠ diagnostics trail exists to prevent. Wire it to the app's
	// session-diag sink (orchestrate: chatTurn.turnDiag). kind is a short
	// stable slug; detail is one human-readable sentence. Optional; nil means
	// the loop still corrects, just without a breadcrumb.
	OnDiag func(kind, detail string)

	// Consult asks a stronger model ONE self-contained question on the loop's
	// behalf and returns its answer, which the caller injects into history as
	// advice. Wired by the app (orchestrate routes it through
	// app.orchestrate.consult); core stays ignorant of tiers and routes, the
	// same way OnDiag keeps it ignorant of session trails.
	//
	// Used by the failure-shape guard below: three hits on one wall means the
	// arguments aren't what's wrong, and a model that can ANSWER — given just
	// the failure text, with no tool catalog or history to lose track of — is
	// worth more there than another variation from the model that's stuck.
	// Note the symmetry: the same fingerprint DE-escalates a lead-driven turn
	// to the worker at six hits, and ESCALATES a worker-driven turn to a
	// consult at three. One signal, direction depending on who is driving.
	//
	// Optional; nil means the guard falls back to its generic directive. An
	// error or empty answer falls back the same way — a consult that fails
	// must never cost the turn its correction.
	Consult func(question, evidence string) (string, error)

	// SettleRound is called by a correction guard right before it re-prompts
	// and continues, so the app can FINALIZE whatever the just-rejected round
	// already streamed into its own bubble and open a fresh one for the retry.
	// Without it, a correction that `continue`s before the normal end-of-round
	// finalize orphans the streamed text: the retry round's text concatenates
	// into the still-open bubble ("…What API?Fair point…") and the post-loop
	// path can re-emit it as a second bubble. The app MUST settle by finalizing
	// (never length-clearing) — a correction retry is not guaranteed to repeat
	// the earlier text, so clearing it would lose real content; finalizing is
	// lossless and lets the post-loop near-duplicate check suppress any echo.
	// Wire it to the orchestrate streamHandler's finalize path. Optional; nil
	// means the pre-fix behavior (orphaned bubble). Idempotent / no-op when
	// nothing streamed this round.
	SettleRound func()

	// DrainViewImages, when set, is called after each tool-execution round to
	// pull any frames a tool queued for the model to look at (e.g. view_video's
	// sampled video frames, held on sess.PendingViewImages). Returned images are
	// injected as a vision user-message before the next LLM call, so the model
	// actually SEES them. Wire it to sess.DrainViewImages. Optional; nil means
	// the loop never injects view images (the pre-fix behavior, where queued
	// frames were silently dropped and the model "described" a video it never saw).
	DrainViewImages func() [][]byte

	// Stream enables streaming mode. When set, LLM responses are streamed
	// through this handler as they arrive. Optional.
	Stream StreamHandler

	// ReasoningStream, when set, receives reasoning_content chunks (the
	// model's <think>...</think> stream) as they arrive. Fires only when
	// the LLM call uses ChatStream (i.e. when Stream is also set), and
	// only for backends that surface reasoning incrementally (llama.cpp,
	// Ollama). Use to surface what the model is reasoning about during
	// long agentic loops — e.g. servitor's investigator panel showing
	// the orchestrator's thought process as it streams. Optional.
	ReasoningStream func(chunk string)

	// Confirm is called when a tool with NeedsConfirm is about to execute.
	// If nil, a default terminal prompt is used (y/n).
	Confirm ConfirmFunc

	// ChatOptions are additional options passed to every LLM call.
	ChatOptions []ChatOption

	// ToolRoundOptions are options applied to rounds that follow a tool-call
	// round (i.e. rounds where the model is processing tool results). When set,
	// these replace ChatOptions for those rounds. Use to enable thinking only
	// for tool-execution rounds while keeping the initial conversational round
	// lean — e.g. ChatOptions: [WithThink(false)], ToolRoundOptions: [WithThink(true)].
	ToolRoundOptions []ChatOption

	// PromptTools describes tools in the system prompt as text instead of
	// using native function calling. The LLM responds with plain text
	// containing tool calls in a defined format, which the loop parses and
	// executes. Results are sent back as regular user messages, giving the
	// caller full control over context. This works reliably with models
	// that have poor or no native tool support (e.g. Gemma via Ollama).
	PromptTools bool

	// DisableToolMentionCorrection turns off the no-arg-tool-mention nudge
	// (the loop otherwise re-prompts when the model names a known no-arg tool
	// in prose but emits no call). Set this when the model is EXPECTED to name
	// its own tools legitimately — chiefly a code-analysis session whose
	// SUBJECT is a codebase that defines those same tools (e.g. servitor
	// pointed at an agent framework), where "the code defines store_fact" is
	// description, not an intended call. Leaving it on there makes the model
	// waste a round explaining "I didn't mean to call any tool", and that
	// meta-explanation leaks into the answer. Optional; default false (on).
	DisableToolMentionCorrection bool

	// Tier selects which LLM tier runs the loop. Defaults to WORKER.
	// Set to LEAD to route all rounds through the lead LLM.
	// Ignored when RouteKey is set.
	Tier LLMTier

	// RouteKey is a registered route stage key (see RegisterRouteStage).
	// When set, the tier is resolved from the admin routing config via
	// RouteToLead(key) instead of the Tier field. This lets admins
	// configure per-agent LLM routing from the admin panel.
	RouteKey string

	// MaskDebugOutput suppresses tool argument and result content from debug
	// logs. Use this for sessions that handle sensitive data (SSH credentials,
	// system facts, private files) to prevent data leaking into log files.
	// Tool names are still logged; content is replaced with byte counts.
	MaskDebugOutput bool

	// ThinkBudget sets the thinking_budget_tokens for every round of this
	// loop (e.g. a per-agent configured budget). 0 = inherit the
	// operator-configured global default (admin "Thinking Budget" →
	// llamacppBudget, default 4096). We no longer scale the budget by
	// prior input-token count: prompt size is a poor proxy for task
	// difficulty (a trivial tool call in a long history doesn't need more
	// thinking), and Qwen's own best-practice guidance is a flat budget,
	// not an input-scaled one. Callers needing a specific size set this;
	// the resolution order is per-call WithThinkBudget > this > global.
	ThinkBudget int

	// SerialTools limits execution to one tool call per round. When the LLM
	// returns multiple tool calls in a single response, only the first is
	// executed; the rest receive a SKIPPED notice so the LLM is forced to
	// proceed one step at a time and see each result before deciding what to
	// do next. Recommended for investigative agents where failure feedback
	// must be seen before the next attempt.
	SerialTools bool

	// RoundAbortTools names tools that, when called, must close the round
	// immediately — any other tool calls in the same LLM response are
	// dropped with a SKIPPED notice, and the loop breaks after this round.
	// Use for control tools like ask_user / respond_directly / plan_set
	// that route the turn to a different flow: bundling them with a real
	// tool (e.g. ask_user + create_agent) lets the LLM "ask then act
	// anyway" which defeats the pause. With this set, only the abort tool
	// fires and the LLM has no chance to chain through.
	RoundAbortTools []string

	// SingleFireGroups names sets of tools where AT MOST ONE call from
	// each set may run per batch. When the LLM emits multiple calls from
	// the same group in one response, only the FIRST runs; the rest get
	// a SKIPPED notice. Unlike RoundAbortTools, the round itself
	// CONTINUES — the LLM can still produce its text reply. Designed
	// for attachment-emitting tool families (find_image + fetch_image
	// + generate_image) where parallel-dispatch in one batch produces
	// multiple attachments when the user wanted one.
	//
	// Each inner slice is one group; calls within a group cross-block
	// each other, calls across groups don't.
	SingleFireGroups [][]string

	// OnRoundStart, when set, is called at the top of each round AFTER the
	// ctx-cancellation check and BEFORE the LLM call. Any messages it returns
	// are appended to history before the call. Use for per-round content the
	// model should see every round — budget/pacing notes, status reminders.
	// MAY return content on every call (e.g. orchestrate's round-counter
	// pacer always returns a note); do NOT use it for the pre-finalize
	// injection drain — that's InjectionDrain's job.
	OnRoundStart func() []Message

	// InjectionDrain, when set, returns any pending mid-flight user
	// notes (from an injection queue) and REMOVES them from the queue.
	// Distinct from OnRoundStart in one critical way: it must return
	// EMPTY when there's nothing pending. The loop calls it both at
	// round start AND once more right before finalizing — so a note
	// that lands during the final round still gets picked up and the
	// agent does another round instead of finishing with the note
	// unread. Because it empties its queue, the pre-finalize re-call
	// terminates (returns empty once drained) rather than looping.
	//
	// Wire an injection queue's Drain here, NOT OnRoundStart — a hook
	// that always returns content (like a budget pacer) would make the
	// pre-finalize check loop forever.
	InjectionDrain func() []Message

	// StopRound, when set, is called at the top of each round AFTER the
	// ctx-cancellation check. Returning true breaks the loop cleanly,
	// same effect as hitting MaxRounds. Use for soft-cap policies where
	// the cap depends on runtime state (e.g. orchestrate's explorer-mode
	// flag — the per-agent budget is enforced via StopRound; explorer
	// mode keeps it lifted to the absolute MaxRounds).
	StopRound func() bool

	// GraceRounds is the wrap-up runway. When the cap is reached (MaxRounds
	// or StopRound), instead of hard-stopping — which strips tools and makes
	// some models emit their intended call as TEXT to compensate — the loop
	// keeps tools available and gives the model this many extra rounds to
	// land the turn, escalating a "wrap up now" directive each round before
	// a hard stop (the forced no-tools rescue is the final backstop). 0 lets
	// the loop default it: 5 for real agent turns (MaxRounds >= 10), 0 for
	// short fixed loops (classifiers, judges) which must stop exactly on cap.
	// Set explicitly to override either default.
	GraceRounds int

	// OnRoundReset, when set, is called once per round. Returning true
	// rebases the soft-pacing thresholds (midpoint nudge, wrap-up
	// warning, failure-streak counter) as if the loop just started —
	// "remaining budget" is recomputed from the current round onward.
	// Hard MaxRounds cap stays in place; this only resets the LLM-
	// facing pacing signals.
	//
	// Use when the app's notion of "logical phase" changes mid-loop and
	// the LLM should get a fresh pacing window for the new phase (e.g.
	// servitor advancing to a new plan step — burning rounds on step 1
	// shouldn't trigger the wrap-up warning on step 4). One-shot per
	// transition: app's closure tracks "have I reset since the last
	// phase change?" and returns true exactly once per phase boundary.
	OnRoundReset func() bool

	// PendingWorkFn, when set, reports how many authorized work items
	// (e.g. unfinished plan steps) still remain. The agent-loop's
	// wrap-up warning uses this to distinguish "stop exploring" (the
	// default) from "you still have N authorized items to finish — wind
	// down this item cleanly and continue the list." Without this hook,
	// the wrap-up nudge tells the model not to start new investigations,
	// which a plan-driven worker can read as "abort the remaining plan
	// steps and write a summary" — leading to clean wrap-ups that
	// silently skip pending steps.
	//
	// Return the count of remaining items (pending + in-progress is
	// usually right). 0 means "no more authorized work, exploration is
	// up to you" and the default wrap-up text fires.
	PendingWorkFn func() int

	// DynamicTools, when set, is called at the top of each round to fetch
	// runtime-defined tools to merge into the catalog. Used by apps that
	// support session-scoped tools the LLM creates mid-conversation
	// (e.g. via create_temp_tool). The returned tools go through the
	// same AllowedCaps filter as static tools — runtime registration
	// can't escape capability gating. Returning nil/empty is fine and
	// just means "no extras this round."
	DynamicTools func() []AgentToolDef

	// ToolFallbackResolver, when set, is consulted when the model calls a
	// tool name that ISN'T in the round's catalog. It lets an app route a
	// call to a tool whose SCHEMA is intentionally lazy (kept out of the
	// LLM tool array to save tokens) but whose HANDLER is still valid —
	// e.g. a custom tool the model already learned via load_tool on a
	// prior turn and now calls directly from context. Return (handler,
	// true) to run it; (nil, false) to fall through to the normal
	// "unknown tool" error. The resolver may also mark the tool loaded so
	// its schema rejoins the catalog next round.
	ToolFallbackResolver func(name string) (ToolHandlerFunc, bool)

	// RoundToolFilter, when set, is called at the top of each round for
	// every candidate tool name; returning false drops that tool from the
	// round's catalog. Use to SUPPRESS a tool mid-turn — e.g. after it has
	// looped/errored repeatedly — so the model is forced off it. A fixated
	// model ignores error feedback but physically can't call a tool that
	// isn't in the catalog. Nil = no filtering (all tools offered).
	RoundToolFilter func(name string) bool

	// RoundChatOptions, when set, is called at the top of each round; its
	// options are appended LAST (after route/budget defaults and
	// ChatOptions/ToolRoundOptions) so they OVERRIDE for that round.
	// Use for per-round dynamic overrides the static option slices can't
	// express — e.g. forcing WithThink(false) on the round right after a
	// control-tool rejection so the model doesn't deliberate itself back
	// into the same dead end. Nil = no per-round override.
	RoundChatOptions func() []ChatOption

	// ContextSize is the model's context window (tokens). When > 0, the
	// loop compacts history before each round once it crosses ~70% of the
	// window — eliding the bodies of OLD tool results (keeping recent ones
	// + all conversational text) so a long multi-round session can't grow
	// past the window and trigger server-side context-shift (which drops
	// the system prompt and degrades the model). 0 = no compaction. Set it
	// from the caller's WorkerContextSize()/LeadContextSize().
	ContextSize int

	// RoundCompactNow, when set, is checked at the top of each round; a
	// true return forces an AGGRESSIVE compaction this round (shed all but
	// the newest tool-result body, regardless of budget). This is the
	// LLM-driven path: a compact_context tool sets it so the model can
	// proactively drop a long tool output (e.g. a smoke-test report) the
	// moment it's done with it, instead of waiting for the budget floor.
	// Works even when ContextSize is 0. Nil = budget-only compaction.
	RoundCompactNow func() bool

	// StampLocation sets the timezone of the "[Current date & time: …]"
	// marker prefixed onto the newest user turn. Nil = the deployment/host
	// zone (time.Local). Set it to the acting user's location (UserLocation)
	// so the model sees the wall-clock in the user's own zone rather than the
	// server's. Only the stamp is affected; nothing else in the loop reads it.
	StampLocation *time.Location

	// AllowedCaps gates which tools the LLM is offered, by capability tier
	// (CapRead, CapNetwork, CapWrite, CapExecute). Tools whose declared Caps
	// aren't all in this set are filtered out before the LLM ever sees the
	// catalog. Empty/nil means "no restriction" (legacy behavior — every
	// tool the caller passed is offered). Use to enforce least-privilege:
	// e.g. a chat agent permits read+network but not write+execute, so even
	// if a write/execute tool ends up in the registry it can't be invoked
	// from chat. Tools with empty Caps (unannotated) pass through unfiltered
	// during the migration period.
	AllowedCaps []Capability
}

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

// repeatFailHistoryWindow bounds how far back seedRepeatFailFromHistory
// replays: only the recent tail counts, so an ancient failure doesn't
// ban a call forever (a fixation worth stopping shows up within a handful
// of recent turns; a success anywhere in the window clears it).
const repeatFailHistoryWindow = 40

// seedRepeatFailFromHistory pre-arms the loop-guard from the conversation
// tail so a fixation spanning separate turns is caught. It walks the
// recent messages in order, mapping each tool call's ID to its signature,
// then applies each tool result under the SAME rule the live loop uses:
// bump the signature on an errored result, clear it on a successful one.
// The result is repeatFail reflecting the current per-signature failure
// streak as of the end of history — so a call that already failed
// identically repeatFailLimit times is blocked on its next attempt.
func seedRepeatFailFromHistory(messages []Message, repeatFail map[string]int) {
	start := 0
	if len(messages) > repeatFailHistoryWindow {
		start = len(messages) - repeatFailHistoryWindow
	}
	idToSig := map[string]string{}
	for _, m := range messages[start:] {
		for _, tc := range m.ToolCalls {
			idToSig[tc.ID] = tc.Name + "\x00" + formatArgs(tc.Args)
		}
		for _, tr := range m.ToolResults {
			sig, ok := idToSig[tr.ID]
			if !ok {
				continue
			}
			if tr.IsError {
				repeatFail[sig]++
			} else {
				delete(repeatFail, sig)
			}
		}
	}
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
		PromptTools:  T.PromptTools,
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
	if T.LLM == nil {
		return nil, messages, fmt.Errorf("LLM is not configured")
	}

	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 10
	}

	confirmFn := cfg.Confirm
	if confirmFn == nil {
		confirmFn = defaultConfirm
	}

	// Capability allow-set, computed once. Static tools and dynamic ones
	// (from cfg.DynamicTools) both pass through the same filter — runtime
	// tool registration can't elevate beyond the session's tier.
	var allowedSet map[Capability]bool
	if len(cfg.AllowedCaps) > 0 {
		allowedSet = make(map[Capability]bool, len(cfg.AllowedCaps))
		for _, c := range cfg.AllowedCaps {
			allowedSet[c] = true
		}
	}
	filterCaps := func(in []AgentToolDef) []AgentToolDef {
		if allowedSet == nil {
			return in
		}
		out := make([]AgentToolDef, 0, len(in))
		for _, td := range in {
			if !capsAllowed(td.Tool.Caps, allowedSet) {
				Debug("[agent_loop] tool '%s' filtered out by AllowedCaps (declares %v, allowed %v)", td.Tool.Name, td.Tool.Caps, cfg.AllowedCaps)
				continue
			}
			out = append(out, td)
		}
		return out
	}

	// Static (per-session) tools — survive across rounds. Dynamic tools
	// (cfg.DynamicTools) are pulled fresh per round and merged in below.
	tools := filterCaps(cfg.Tools)

	// Tool dispatch maps. When DynamicTools is set these get rebuilt at
	// the top of each round so newly-defined temp tools become visible
	// to the LLM on the next call. When unset, the static slice is used
	// directly and these maps are computed once.
	var toolDefs []Tool
	handlers := make(map[string]ToolHandlerFunc)
	needsConfirm := make(map[string]bool)
	singleFireTools := make(map[string]bool)
	serialFireTools := make(map[string]bool)
	rebuildToolMaps := func(active []AgentToolDef) {
		toolDefs = toolDefs[:0]
		for k := range handlers {
			delete(handlers, k)
		}
		for k := range needsConfirm {
			delete(needsConfirm, k)
		}
		for k := range singleFireTools {
			delete(singleFireTools, k)
		}
		for k := range serialFireTools {
			delete(serialFireTools, k)
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
			if _, dup := handlers[td.Tool.Name]; dup {
				Log("[agent_loop] tool name collision: %q is registered twice — keeping the first definition, ignoring the later one (an expanded toolbox action and a standalone tool can mint the same name)", td.Tool.Name)
				continue
			}
			toolDefs = append(toolDefs, td.Tool)
			handlers[td.Tool.Name] = td.Handler
			if td.NeedsConfirm {
				needsConfirm[td.Tool.Name] = true
			}
			// Serial-fire takes precedence: a serial tool must NOT be added to
			// the single-fire set, or the enforcement pass would drop its
			// excess calls before the executor gets to run them in order.
			if td.SerialFirePerBatch {
				serialFireTools[td.Tool.Name] = true
			} else if td.SingleFirePerBatch {
				singleFireTools[td.Tool.Name] = true
			}
		}
	}
	rebuildToolMaps(tools)

	history := make([]Message, len(messages))
	copy(history, messages)

	// Stamp the current date+time onto the latest user turn (the human message
	// that opened this turn — tool-result user messages get appended below, so at
	// this point the last message IS the human turn). This is the cache-safe home
	// for the wall-clock: the newest user message is the volatile tail that never
	// hits cache anyway, so the stamp costs nothing, while the system prompt stays
	// date-free and cacheable across days. The stamp freezes here and rides into
	// the returned history, so on later turns it stays put (a stable, cached prefix
	// element) while only the next new turn re-stamps. Paired with WithoutAutoDate()
	// on the LLM calls below so the date isn't ALSO injected into the system prompt.
	if n := len(history); n > 0 && history[n-1].Role == "user" &&
		!strings.HasPrefix(history[n-1].Content, "[Current date & time:") {
		history[n-1].Content = CurrentContextStampIn(cfg.StampLocation) + "\n\n" + history[n-1].Content
	}

	// In PromptTools mode, inject tool descriptions into the system
	// prompt instead of using native function calling. Everything stays
	// as plain text — tool calls are parsed from <tool_call> tags and
	// results are sent back as regular user messages.
	systemPrompt := cfg.SystemPrompt
	if cfg.PromptTools && len(tools) > 0 {
		systemPrompt += BuildToolPrompt(tools)
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
	if emitDisciplinePrompt && len(tools) > 0 {
		systemPrompt += "\n\n[Answering across tool rounds: NEVER emit answer text in the same step as a tool call. Call your tools FIRST, wait for the results, THEN write your answer from those results — in a final step that has no tool call. Do NOT pre-write an answer from training/memory and then also fetch tools: an answer composed before the results is exactly the double-emit to avoid. Answer after tools, not before. A brief progress note as you work (\"checking that now…\") is fine; your actual answer waits for the end and appears exactly once. Never two answers in one turn.]"
	}
	// Grounding discipline — every tool-using loop. The failure mode: the
	// model retrieves real sources, then embellishes with specifics pulled
	// from memory — a wrong statute/instruction number, a plausible-but-
	// fabricated case cite, an invented figure, date, or quote. For a
	// research / legal / medical assistant that's the dangerous one: a
	// fabricated citation that reads as authoritative. Pin specifics to
	// what was actually retrieved or provided.
	if len(tools) > 0 {
		// Framing header — names the blocks below as ONE grounding contract seen
		// from several angles, so they read as a coherent message rather than a
		// pile of rules. Kept to a single short line on purpose: the per-block
		// salience the 27B needs comes from the blunt standalone blocks, NOT from
		// a long preamble, so the frame stays out of their way.
		systemPrompt += "\n\n[Grounding contract: the blocks below are one rule seen from several sides. You earn the right to state a fact by pointing to where it came from THIS turn; when you can't, say so instead of guessing.]"
		// Capability-first — tool SELECTION, distinct from Grounding (which
		// governs specifics once you have results). The failure mode: the
		// model answers a recency-sensitive or job-specific question straight
		// from training when a tool or agent for it is sitting in the catalog
		// (e.g. reciting "the news" from priors instead of searching). The
		// tool-vs-agent choice is a SIZING decision (how big is the job), kept
		// separate from the trust decision (where the answer comes from) so
		// this doesn't push the model away from delegating real multi-step
		// work. Written without em-dashes so it doesn't model the tic.
		systemPrompt += "\n\n[Capability-first: when a tool or agent can do the job or get fresher information than you hold (news, prices, status, anything that may have changed since training), use it instead of answering from memory. Size it to the job: a direct tool for one lookup or action, an agent for multi-step or specialized work. Prior knowledge is a fallback for gaps no capability can fill, never a substitute for one that exists; if you fall back, say so and offer to verify. To answer what tools you have, read your live catalog (including any 'custom tools (load before use)' section), never recite from memory, and never claim you already built a tool without checking.]"
		systemPrompt += "\n\n[Grounding: state a precise specific (number, name, citation, statute/case/version/ID, date, dosage, direct quote) ONLY when it appears in a tool result or material the user gave you THIS turn, never from memory however confident — a specific you can't point to is worse than none. This holds in casual talk too: give the general shape, but don't attach a specific you can't source, say you're not sure instead of guessing. The [Current date & time: …] stamp on the latest user message IS a this-turn source: read the date and time off it and state them plainly (no tool call needed), but do NOT add a holiday, season, or event association unless a source gave it this turn (a rule-based holiday like \"the last Monday in May\" is exactly the specific you must not assert from memory; \"it's a regular Sunday, what's up?\" beats a confident wrong holiday). If a tool you relied on fails, errors, times out, or returns empty, treat the data as missing, never backfill from memory. Same for MEDIA you couldn't open, download, or view: don't describe it or infer its content from the URL, caption, sender, or nearby messages, and don't reuse one item's description for another; if a past turn's \"[N image(s) attached …]\" note records what an image showed, rely only on that note, don't re-describe or invent its subject. If the user corrects a specific, don't swap in another guess or invent a reason for the error, admit you're unsure and offer to look it up.]"
		// Action grounding — the sibling of Grounding aimed at ACTIONS rather than
		// facts. The failure mode (observed live: an agent in a group chat said "I
		// sent a meme" with zero tool calls): the model narrates a completed action
		// it never performed, because its reply text feels like doing the thing.
		// Written without em-dashes (house style).
		systemPrompt += "\n\n[Actions: never claim you DID something (sent a message/meme/image/file, posted, scheduled, created, saved, ran a command) unless you called the tool that does it THIS turn and its result confirms success. Your reply text is NOT an action: writing 'I sent it', 'attached', 'posted to the group', or 'done' does nothing by itself. If the thing needs a tool, call it and report what its result says; if you didn't call it, don't say you did. If you couldn't or chose not to, say so plainly. When an action tool errors, times out, or returns empty, treat the action as NOT done and tell the user.]"
		// Contradiction discipline — the sibling of Grounding aimed the OPPOSITE
		// direction: not the model's own volunteered specifics, but the model
		// DISPUTING a fact the user stated or assumed. The failure mode is a
		// confident "well, actually that's wrong" sourced from stale training,
		// which is worse than the user's claim when the priors are out of date.
		// Scoped to CONTRADICTING the user (not to general answers) so it does
		// not add hedging to the decisive-language posture elsewhere. Written
		// without em-dashes (house style).
		systemPrompt += "\n\n[Disagreeing with the user: your training is not enough to tell the user they are wrong. When they state a fact you think is mistaken, treat your priors as possibly stale. If a tool can check it, verify FIRST, then correct with the source in hand. If nothing can verify it, do NOT assert they are wrong from memory: say you are not certain and offer to check, or ask. This is about EMPIRICAL claims (dates, numbers, who/what/when, current state, how something works). Reasoning, math you can show step by step, and the user's own preferences you can still engage directly and decisively.]"
		systemPrompt += "\n\n[Numbers: reproduce a figure exactly as the source writes it, keep its unit or currency attached, and keep it bound to the thing it describes (which item, date, place) so you never swap two values from the same source. Do not do multi-step arithmetic, percentages, or unit/currency conversion in your head and present the result as fact: show the steps so it can be checked, or use a tool. If two sources disagree, say so rather than silently picking one. (Prices and other time-sensitive figures are governed by [Volatile facts].)]"
		// False-precision prevention — the behavioral half of the same concern
		// the [Numbers] / [Grounding] blocks address: stop the model inventing a
		// percentage / fraction / dollar figure for rhetorical weight. This is
		// prompt-only by design; the mechanical re-prompt gate that used to back
		// it was removed because its verbatim-corpus match couldn't tell a
		// correctly COMPUTED figure ("$120 over MSRP") from a fabricated one and
		// false-flagged the model's own arithmetic.
		systemPrompt += "\n\n[No false precision: do NOT manufacture a number for emphasis or authority. Without a real sourced figure, don't invent a percentage, fraction, or dollar amount: say \"most\", \"roughly half\", \"a few thousand\", or describe the size in words. An invented \"80%\" or \"$5,000\" reads as precise and is worse than an honest \"most\". A genuinely sourced number stated exactly is right, as is arithmetic you actually did on sourced numbers (show it), and hedged estimates (\"about half\") are fine.]"
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
		lookupClause := "look it up FIRST with whatever search or fetch tool you have and quote what the result returns (with what it applies to and when observed); if you have no way to look it up right now, say plainly you don't have a current figure and offer to check"
		for _, td := range tools {
			if n := td.Tool.Name; n == "web_search" || n == "fetch_url" || n == "browse_page" {
				lookupClause = "call web_search or fetch_url FIRST and quote what the result returns (with what it applies to and when observed), or, if you cannot look it up right now, say plainly you don't have a current figure and offer to check"
				break
			}
		}
		systemPrompt += "\n\n[Volatile facts: some facts change over time, and you do NOT know their current value no matter how confident it feels. PRICES are the clearest case: any price, rate, fee, cost, or money figure is volatile, so NEVER state one from memory, not even a rough number or a range; a remembered price is always a guess. The same rule covers stock and availability, the CURRENT holder of a changing role or record (who runs a company now, the latest version of something, the current champion or office-holder), and live status, scores, or counts. The test for any specific: could this have changed since your training, and does the user expect today's value? If yes, it is volatile: " + lookupClause + ". Do not fill the gap with a plausible-sounding value. This is not a closed list: any fact that fails the test is volatile even if it is not named here.]"
	}
	// Output style — universal (every reply, with or without tools).
	// Suppresses persistent LLM lexical/punctuation tics the user flagged.
	// The rule itself is written WITHOUT em-dashes so it doesn't model the
	// behavior it forbids.
	systemPrompt += "\n\n[Style: (1) Stop reaching for the word \"classic\"; you lean on it as filler. Drop it unless it's literally accurate (a \"classic car\", a named \"classic\" edition), never as a generic intensifier for something ordinary. (2) Do NOT use em-dashes (the \"—\" character, U+2014) at all. Where you'd reach for one, use a comma, parentheses, a colon, or two sentences instead.]"
	// Secret handling — universal. Stops any agent from soliciting API
	// credentials in chat (the OPNsense-controller failure mode); auth is
	// injected server-side via Admin > APIs credentials, so the secret never
	// belongs in the conversation or the tool-call logs.
	systemPrompt += "\n\n[Secrets: never ask the user to paste an API key, secret, token, or password into the conversation, and do not accept one if offered. Authenticated APIs are wired through gohort credentials set up in Admin > APIs; auth is injected server-side, so you never see or need the secret. If a tool's credential is not configured yet, tell the user it needs to be set up in Admin > APIs (name the credential) and stop there, do not collect login details in chat. A secret typed into a chat leaks into the session history and the tool-call logs, which is what the credential system exists to prevent.]"

	// Internal-marker convention: gives the model a sanctioned, always-scrubbed
	// wrapper for internal-only notes AND tells it not to type bare delivery
	// markers into user-facing text (the textutil.StripMetaTags safety net catches both).
	systemPrompt += "\n\n[Internal markers: anything wrapped in <gohort-meta>...</gohort-meta> is stripped before the user sees it — use it for internal-only notes and NEVER put anything the user should read inside it. Do not type bare delivery markers like [ATTACH: file] into your reply; attachments ride along through their tool, and any stray marker is scrubbed from your reply anyway.]"
	// Round-budget awareness — let the LLM know how many rounds it has
	// for the whole turn so it can pace itself (vs. exploring as if
	// budget were infinite, then getting truncated). Only emit for
	// sessions with meaningful budgets; short fixed loops (judges,
	// classifiers) don't need the noise.
	if maxRounds >= 10 {
		systemPrompt += fmt.Sprintf("\n\n[Round budget: this turn has up to %d tool-execution rounds. The framework will nudge you at the halfway mark and again near the cap; plan your investigation so you finish with a real answer rather than hitting the limit mid-exploration.]", maxRounds)
	}

	var lastResp *Response
	prevHadToolCalls := false
	// promiseCorrectionsTotal caps how many times we'll re-prompt the
	// model for promising action without taking it. Two attempts is
	// enough to nudge a stuck Qwen turn; further attempts would burn
	// rounds without progress.
	promiseCorrectionsTotal := 0
	const maxPromiseCorrections = 2

	// emitDiag breadcrumbs a silent correction into the app's session-diag
	// trail (nil-safe). Kept here so every correction guard below records the
	// framework decision it just made — see AgentLoopConfig.OnDiag.
	emitDiag := func(kind, detail string) {
		if cfg.OnDiag != nil {
			cfg.OnDiag(kind, detail)
		}
	}

	// settleRound finalizes the just-rejected round's streamed text before a
	// correction re-prompts (nil-safe), so the retry starts in a fresh bubble
	// instead of concatenating into an orphaned one — see
	// AgentLoopConfig.SettleRound. Every correction guard that `continue`s on a
	// round that may have streamed content calls this first.
	settleRound := func() {
		if cfg.SettleRound != nil {
			cfg.SettleRound()
		}
	}

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
	toolFiredThisTurn := false

	// Soft-pacing checkpoints — midpoint nudge (50% of remaining
	// budget) and wrap-up warning (80% of remaining). Both compute
	// off a rebase point that defaults to 0 (= "remaining" is
	// MaxRounds) and shifts whenever OnRoundReset returns true. That
	// gives apps a way to say "the LLM just advanced to a new logical
	// phase — give it a fresh pacing window from here." Hard MaxRounds
	// cap is unaffected.
	wrapUpWarningFired := false
	midpointNudgeFired := false
	baseRound := 0
	var wrapUpThreshold, midpointThreshold int
	recomputeThresholds := func() {
		remaining := maxRounds - baseRound
		if remaining >= 5 {
			wrapUpThreshold = baseRound + (remaining*4)/5
		} else {
			wrapUpThreshold = 0
		}
		if remaining >= 10 {
			midpointThreshold = baseRound + remaining/2
		} else {
			midpointThreshold = 0
		}
	}
	recomputeThresholds()

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
	failureStreak := 0
	failureStreakWarned := false
	const failureStreakThreshold = 3

	// cumulativeToolErrors tracks tool errors across the WHOLE loop, not
	// just the current round. Used to catch the "give up with errors
	// pending" pattern: model emits empty content + finish=stop after a
	// run with unresolved tool errors, even though budget remains.
	// Increments after each round's native-tool batch (toolErrors local
	// var), and the no-tool-call exit path checks it before letting the
	// loop terminate — injecting a "fix the errors, don't summarize"
	// nudge instead of letting the rescue path paper over the bailout.
	cumulativeToolErrors := 0

	// Repeated-failure loop-guard. Small models fixate: they re-issue the
	// SAME tool call with the SAME args, get the SAME error, and never adapt
	// (e.g. polling inspect_run with an approval id that has no run — 25× until the
	// round budget burns). repeatFail counts consecutive errors per call
	// signature (tool name + args); once a signature crosses repeatFailLimit we
	// stop executing it and feed back a hard STOP directive instead. Signature-
	// scoped (not tool-scoped) so the SAME tool with DIFFERENT args is fine, and
	// a success resets the counter so legitimate polling isn't penalized.
	repeatFail := map[string]int{}
	const repeatFailLimit = 3
	// Identical-repeat guard. repeatFail above only counts ERRORS (it resets
	// on success), so a model that re-issues the same call and keeps getting a
	// valid-but-useless SAME result never trips it (observed live: inspect_run
	// polled on one run id ~30x in a single turn, every call succeeding, zero
	// progress, until the round cap). repeatSame counts consecutive BYTE-
	// IDENTICAL results per signature regardless of error status; a changed
	// result (genuine polling) resets it, so real polling is never penalized.
	repeatSame := map[string]int{}
	lastToolContent := map[string]string{}
	const repeatSameLimit = 4
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
	errShapeCount := map[string]int{}
	errShapeNudged := map[string]bool{}
	const errShapeNudgeAt = 3    // say it plainly, once per shape
	const errShapeDeescalateAt = 6 // stop paying lead rates to keep hitting it
	// Seed the guard from prior-turn history so a fixation that spans
	// SEPARATE user turns is caught. repeatFail is otherwise turn-local, so
	// a model that re-issues the SAME wrong+erroring call every turn resets
	// each turn and never trips (observed live: an agent called an unrelated
	// tool with identical args, erroring, across five user turns). The
	// incoming `messages` already carry prior tool calls + their error
	// results (toLLMMessages reconstructs them across turns), so replaying
	// that record through the SAME increment-on-error / reset-on-success
	// rule pre-arms the guard.
	seedRepeatFailFromHistory(messages, repeatFail)

	// Wrap-up grace runway. Rather than hard-stopping at the cap (which
	// strips tools and makes some models emit their intended tool call as
	// TEXT to compensate — a garbled final round), we keep tools available
	// and give the model a bounded runway to land the turn. hardStop is the
	// last allowed round once wrap-up begins (-1 until the cap is hit).
	graceRounds := cfg.GraceRounds
	if graceRounds == 0 && maxRounds >= 10 {
		graceRounds = 5 // default runway for real agent turns; short fixed loops opt out
	}
	if graceRounds < 0 {
		graceRounds = 0
	}
	hardStop := -1

	// Wedge break-out. The loop-guard above blocks a repeated failing call, but a
	// small model may keep RE-EMITTING that identical blocked call every round
	// (often as a TEXT/reasoning tool call), ignoring the STOP directive and
	// making zero progress. Counting rounds that were nothing-but-a-blocked-call,
	// once past this small limit we stop looping and force a clean final answer
	// rather than grinding to the round cap. forceFinal drives the post-loop rescue.
	guardBlockedStreak := 0
	const guardBlockedBreakLimit = 2
	forceFinal := false

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
	leadTokens := 0
	deescalated := ""

	// keep_going spin guard. keep_going is a pure "run another round" signal
	// with no side effect — meant for "I'm about to act, give me one more
	// round." Because it IS a tool call, it sets toolFiredThisTurn and thereby
	// SUPPRESSES the action-promise correction below, so a model can promise
	// "I'll call the real tool next" every round and never act. Counting
	// consecutive rounds whose ONLY tool call(s) were keep_going, we escalate
	// the nudge and then force a clean final answer rather than let it spin.
	// (Observed live: 8+ keep_going calls across two turns, ~2.5 min, the
	// actual tool never called.)
	keepGoingStreak := 0
	const keepGoingSpinLimit = 3

	for round := 1; round <= maxRounds+graceRounds; round++ {
		// Bail immediately on cancellation so the loop doesn't burn another
		// LLM call (or tool execution) after the session was aborted. Tool
		// handlers that don't check ctx themselves can otherwise hold the
		// loop open for a tick after cancel().
		if err := ctx.Err(); err != nil {
			return lastResp, history, err
		}
		// Round-start breadcrumb — pair with the existing "round N:
		// content=..." post-LLM log to bracket each round. When a
		// hang lands between rounds (after a tool returned, before
		// the next LLM call), we see "starting" without a matching
		// "calling LLM" → narrows the wedge to compaction / option
		// assembly / injection drain. Cheap, fires once per iteration.
		Debug("[agent_loop] round %d: starting (history=%d msgs)", round, len(history))
		// BREADCRUMB: round-top reached. Mirrors the Debug above at
		// Log level — paired with the "dispatch complete" breadcrumb
		// after tools, the gap between the two pinpoints whether
		// the hang is in iteration restart (Debug fires) or pre-
		// iteration bookkeeping (Debug does NOT fire).
		Log("[agent_loop] round %d: top of iteration", round)
		// Soft cap hook — apps that want a budget cap depending on
		// runtime state (e.g. orchestrate's explorer-mode flag) wire
		// StopRound. Called EXACTLY ONCE per round (it has side effects,
		// e.g. orchestrate increments its round counter here).
		stop := cfg.StopRound != nil && cfg.StopRound()
		if graceRounds <= 0 {
			// No runway (short fixed loops / opted out): original behavior —
			// a true StopRound terminates immediately.
			if stop {
				break
			}
		} else {
			// Begin the wrap-up runway the first time the cap is reached,
			// then hard-stop once it's spent. Tools stay available the whole
			// time (see the tool-offer gate below), so the model lands the
			// turn instead of getting them stripped mid-intent. The escalating
			// directive a few lines down is what bounds the runway; the forced
			// no-tools rescue after the loop is the final backstop.
			if stop || round >= maxRounds {
				if hardStop < 0 {
					hardStop = round + graceRounds
					if hardStop > maxRounds+graceRounds {
						hardStop = maxRounds + graceRounds
					}
					Debug("[agent_loop] round %d: cap reached — entering wrap-up grace, hard stop at round %d", round, hardStop)
				}
			} else if hardStop >= 0 {
				// The cap lifted again (e.g. the model flipped orchestrate's
				// explorer mode mid-grace, so StopRound now returns false).
				// Cancel wrap-up and resume normal running until the next cap.
				Debug("[agent_loop] round %d: cap lifted — cancelling wrap-up grace", round)
				hardStop = -1
			}
			if hardStop >= 0 && round > hardStop {
				break
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
		if cfg.OnRoundReset != nil && cfg.OnRoundReset() {
			baseRound = round - 1 // remaining counts from this round forward
			remaining := maxRounds - baseRound
			Debug("[agent_loop] round reset at round %d/%d — %d rounds remain", round, maxRounds, remaining)
			wrapUpWarningFired = false
			midpointNudgeFired = false
			failureStreak = 0
			failureStreakWarned = false
			recomputeThresholds()
			if remaining >= 5 {
				history = append(history, Message{
					Role: "user",
					Content: fmt.Sprintf(
						"Fresh budget window: you have %d rounds for this phase. The framework will nudge you at the halfway mark and again near the cap — pace this phase as if starting clean. (Hard MaxRounds cap is still %d total for the turn.)",
						remaining, maxRounds),
				})
			}
		}
		// Per-round content (budget/pacing notes, status reminders).
		// May return content every round; that's fine here — it's only
		// called at round start, never in the pre-finalize re-check.
		if cfg.OnRoundStart != nil {
			if injected := cfg.OnRoundStart(); len(injected) > 0 {
				history = append(history, injected...)
			}
		}
		// Drain any mid-flight injections (user notes interjected into a
		// running orchestrator). Separate from OnRoundStart because the
		// loop re-calls THIS one before finalizing — so it must empty its
		// queue and return nil when nothing's pending.
		if cfg.InjectionDrain != nil {
			if injected := cfg.InjectionDrain(); len(injected) > 0 {
				history = append(history, injected...)
			}
		}
		// Midpoint nudge — at 50% of MaxRounds, drop a status reminder
		// so the model can recalibrate before the wrap-up pressure
		// kicks in. Fires once per session (midpointNudgeFired flag).
		if !midpointNudgeFired && midpointThreshold > 0 && round >= midpointThreshold {
			Debug("[agent_loop] midpoint nudge at round %d/%d (base=%d)", round, maxRounds, baseRound)
			phaseRound := round - baseRound
			phaseTotal := maxRounds - baseRound
			history = append(history, Message{
				Role: "user",
				Content: fmt.Sprintf(
					"Halfway checkpoint: you're at round %d of %d for this phase. Taking stock is worth a moment — if you're making real progress, keep going; if not, consider switching tools, trying a different angle, or asking the user for clarification before the remaining budget gets spent.",
					phaseRound, phaseTotal),
			})
			midpointNudgeFired = true
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
		if !wrapUpWarningFired && wrapUpThreshold > 0 && round >= wrapUpThreshold {
			remaining := maxRounds - round + 1
			Debug("[agent_loop] wrap-up warning at round %d/%d (%d remaining)", round, maxRounds, remaining)
			pending := 0
			if cfg.PendingWorkFn != nil {
				pending = cfg.PendingWorkFn()
			}
			var wrapUpMsg string
			if pending > 0 {
				wrapUpMsg = fmt.Sprintf(
					"Budget checkpoint: %d rounds left of a %d-round budget, and %d authorized work item(s) still remain on your list. Finish the current item cleanly with a real result, then move to the next one — do NOT skip the remaining items and do NOT start new exploration outside the list. If you genuinely can't complete an item with the rounds remaining, mark it as such and continue.",
					remaining, maxRounds, pending)
			} else {
				wrapUpMsg = fmt.Sprintf(
					"You have %d rounds left of a %d-round budget. Stop exploring and produce a final answer NOW with what you've gathered. If the task isn't complete, summarize what you found, what you tried, and what's still open. Do NOT start new investigations — wind down cleanly.",
					remaining, maxRounds)
			}
			history = append(history, Message{Role: "user", Content: wrapUpMsg})
			wrapUpWarningFired = true
		}
		// Wrap-up grace directive — once on the runway, tell the model
		// (escalating) to finish and answer. Tools stay available, so this
		// message is what makes it land rather than thrash; it's the most
		// recent thing the model sees before the call.
		if hardStop >= 0 {
			left := hardStop - round + 1
			var msg string
			if left <= 1 {
				msg = "[ROUND LIMIT — HARD STOP after this round. Produce your final answer NOW from what you already have. Start no new work; make a tool call only if it is the single step needed to finish, then answer.]"
			} else {
				msg = fmt.Sprintf("[Round limit reached — wrap up and give your final answer. %d round(s) left before a hard stop. Finish in-flight work only; start nothing new.]", left)
			}
			history = append(history, Message{Role: "user", Content: msg})
		}
		// Pull dynamic tools (e.g. temp tools defined by the LLM via
		// create_temp_tool earlier this loop) and merge into the catalog
		// for this round. Filtered through the same caps gate as static
		// tools so the LLM can't elevate via runtime registration.
		if cfg.DynamicTools != nil || cfg.RoundToolFilter != nil {
			active := make([]AgentToolDef, 0, len(tools)+4)
			active = append(active, tools...)
			if cfg.DynamicTools != nil {
				active = append(active, filterCaps(cfg.DynamicTools())...)
			}
			// Per-round suppression: drop any tool RoundToolFilter rejects
			// (e.g. plan_set after it loops). Filtered in place — active is
			// freshly allocated this round, so reusing its backing array is
			// safe and the dispatch maps below see only the kept set.
			if cfg.RoundToolFilter != nil {
				kept := active[:0]
				for _, td := range active {
					if cfg.RoundToolFilter(td.Tool.Name) {
						kept = append(kept, td)
					}
				}
				active = kept
			}
			rebuildToolMaps(active)
		}
		// Compact history if it's about to push the round past the window
		// (budget-based), OR if the LLM asked for it via RoundCompactNow
		// (forced, aggressive). Elides old tool-result bodies in place so
		// this and later rounds stay under the window. Runs after
		// round-start injections so it sees the full assembled history.
		forceCompact := cfg.RoundCompactNow != nil && cfg.RoundCompactNow()
		compactHistory(history, systemPrompt, cfg.ContextSize, forceCompact)
		// Route think is the default; ChatOptions override it. Build route
		// defaults first so per-call WithThink(true/false) takes precedence.
		var opts []ChatOption
		if cfg.RouteKey != "" {
			if think := RouteThink(cfg.RouteKey); think != nil {
				opts = append(opts, WithThink(*think))
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
		if cfg.ThinkBudget > 0 {
			opts = append(opts, WithThinkBudget(cfg.ThinkBudget))
		} else if cfg.RouteKey != "" {
			if rb := RouteThinkBudget(cfg.RouteKey); rb != nil && *rb > 0 {
				opts = append(opts, WithThinkBudget(*rb))
			}
		}
		// If the previous round produced tool calls and ToolRoundOptions are
		// configured, use them instead of ChatOptions for this round.
		roundOpts := cfg.ChatOptions
		if prevHadToolCalls && len(cfg.ToolRoundOptions) > 0 {
			roundOpts = cfg.ToolRoundOptions
		}
		opts = append(opts, roundOpts...)
		// Per-round dynamic override, appended after the static option
		// slices so it wins (e.g. WithThink(false) on the round after a
		// control-tool rejection).
		if cfg.RoundChatOptions != nil {
			opts = append(opts, cfg.RoundChatOptions()...)
		}
		if systemPrompt != "" {
			opts = append(opts, WithSystemPrompt(systemPrompt))
		}
		// Date lives on the latest user turn (stamped above), not the system
		// prompt — keep applyOpts from re-injecting it and poisoning the cache.
		opts = append(opts, WithoutAutoDate())
		if cfg.MaskDebugOutput {
			opts = append(opts, WithMaskDebug())
		}
		// Offer native tools when NOT in PromptTools mode. Grace-enabled
		// loops keep tools available through the wrap-up runway (the
		// escalating directive + post-loop rescue handle the landing, so we
		// never strip — that's what caused models to emit tool-calls as
		// text). Grace-disabled short loops keep the original behavior:
		// no tools on the forced final round.
		offerTools := graceRounds > 0 || round < maxRounds
		if !cfg.PromptTools && len(toolDefs) > 0 && offerTools {
			opts = append(opts, WithTools(toolDefs))
		}
		// Surface reasoning chunks to the caller-supplied handler when set.
		// Fires only on the streaming path; the non-streaming Chat() call
		// returns reasoning only as a single block on Response.Reasoning.
		if cfg.ReasoningStream != nil {
			opts = append(opts, WithReasoningStream(cfg.ReasoningStream))
		}

		var resp *Response
		var err error
		// Pre-call breadcrumb: when an LLM round hangs, we want to know
		// whether the hang is upstream of the LLM call (compaction,
		// injection drain, option assembly) or inside it (waiting on
		// llama.cpp's response). Pairs with the existing "stream
		// completed" log after the call returns: enter-without-exit =
		// LLM-side hang; no-enter = something earlier in the loop wedged.
		// Cheap, fires once per round.
		histChars := 0
		for _, m := range history {
			histChars += len(m.Content)
			for _, tr := range m.ToolResults {
				histChars += len(tr.Content)
			}
		}
		Debug("[agent_loop] round %d: calling LLM (history=%d msgs, ~%d chars)", round, len(history), histChars)
		// BREADCRUMB: about to make the LLM HTTP call. If we see this
		// but no matching "LLM returned" below, the call is hung at
		// the provider — needs a per-call hard timeout or the
		// provider's endpoint is wedged.
		Log("[agent_loop] round %d: → LLM call (history=%d msgs)", round, len(history))
		// If the caller wants reasoning streamed but didn't set a content
		// stream handler, take the streaming path with a no-op content
		// callback so the reasoning callback can fire. The reasoning
		// channel only flows on the streaming path; ChatStreamWithReport
		// is the only LLM dispatch that pumps it.
		streamHandler := cfg.Stream
		if streamHandler == nil && cfg.ReasoningStream != nil {
			streamHandler = func(string) {}
		}
		// Spend guard: once this turn has burned its lead budget, or kept
		// hitting one identical failure, the rest of the rounds run on the
		// worker. Clearing the route key is what carries the decision to
		// ChatStreamWithReport, which resolves the tier from that key alone;
		// the non-streaming branch below reads deescalated directly.
		callOpts := opts
		if deescalated != "" {
			callOpts = append(append([]ChatOption{}, opts...), WithRouteKey(""))
		}
		if streamHandler != nil {
			resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, callOpts...)
		} else {
			// NoLead redirects all routing to worker — no escalation.
			useLead := cfg.Tier == LEAD && !T.NoLead
			if cfg.RouteKey != "" && !T.NoLead {
				useLead = RouteToLead(cfg.RouteKey)
			}
			if deescalated != "" {
				useLead = false
			}
			callFn := T.WorkerChat
			if useLead {
				callFn = T.LeadChat
			}
			// Empty/timeout/empty-error retry happens inside retryLLM
			// (core/llm.go) — every caller gets it for free, including
			// direct WorkerChat/LeadChat and chat-handler ChatStream.
			resp, err = callFn(ctx, history, callOpts...)
		}
		// Context-exceeded recovery: provider rejected the prompt as
		// too large. Naive retries don't help (same prompt → same
		// error), but aggressive compaction (force=true drops all but
		// the newest tool-result body) may free enough room. Retry
		// once after compacting; if the second call still says context-
		// exceeded, surface a clean caller-friendly error instead of
		// the raw provider message.
		if err != nil && IsContextExceededError(err) {
			Debug("[agent_loop] round %d: context exceeded — force-compacting history and retrying once", round)
			compactHistory(history, systemPrompt, cfg.ContextSize, true)
			if streamHandler != nil {
				resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, callOpts...)
			} else {
				useLead := cfg.Tier == LEAD && !T.NoLead
				if cfg.RouteKey != "" && !T.NoLead {
					useLead = RouteToLead(cfg.RouteKey)
				}
				if deescalated != "" {
					useLead = false
				}
				callFn := T.WorkerChat
				if useLead {
					callFn = T.LeadChat
				}
				resp, err = callFn(ctx, history, callOpts...)
			}
			if err != nil && IsContextExceededError(err) {
				Debug("[agent_loop] round %d: context exceeded after force-compact — giving up", round)
				return resp, history, fmt.Errorf("context exhausted: even after aggressive history compaction, the round prompt remains too large for the model's context window — start a new session or split this turn into smaller steps (%w)", err)
			}
			if err == nil {
				Debug("[agent_loop] round %d: context-exceeded recovered after force-compact", round)
			}
		}
		if err != nil {
			return resp, history, err
		}
		lastResp = resp

		// Lead-spend accounting. resp.Tier reflects the tier that actually
		// SERVED the round — a lead call that fell back to the worker is
		// tagged WORKER and correctly doesn't count against the budget.
		if resp != nil && resp.Tier == LEAD {
			leadTokens += resp.InputTokens + resp.OutputTokens
			if deescalated == "" && LeadTurnTokenBudget > 0 && leadTokens >= LeadTurnTokenBudget {
				deescalated = "budget"
				Log("[agent_loop] lead budget spent (%d tokens ≥ %d) — remaining rounds run on the worker tier", leadTokens, LeadTurnTokenBudget)
				if cfg.OnDiag != nil {
					cfg.OnDiag("tier_deescalated", fmt.Sprintf("This turn spent its lead-model budget (%d tokens) — the remaining rounds ran on the worker model.", leadTokens))
				}
			}
		}

		Debug("[agent_loop] round %d: content=%d chars, reasoning=%d chars, tool_calls=%d", round, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))
		// BREADCRUMB: LLM returned. Pair with the "→ LLM call"
		// breadcrumb above to detect a wedged provider call.
		Log("[agent_loop] round %d: ← LLM returned (content=%d, tools=%d)", round, len(resp.Content), len(resp.ToolCalls))

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
		if len(resp.ToolCalls) == 0 && len(strings.TrimSpace(resp.Content)) < 200 && len(resp.Reasoning) > 2000 {
			tail := resp.Reasoning
			if len(tail) > 4000 {
				tail = "…" + tail[len(tail)-4000:]
			}
			Debug("[agent_loop] COLLAPSE-DIAG round %d: content=%q | reasoning_tail(%d total)=%q",
				round, strings.TrimSpace(resp.Content), len(resp.Reasoning), tail)
		}

		// Thinking models may place their response entirely in the
		// reasoning field. Promote reasoning to content when there is
		// no content or tool calls so text-based tool parsing can work.
		if resp.Content == "" && len(resp.ToolCalls) == 0 && resp.Reasoning != "" {
			Debug("[agent_loop] promoting reasoning to content (%d chars)", len(resp.Reasoning))
			resp.Content = resp.Reasoning
		}

		// PromptTools path: parse <tool_call> tags from the text response.
		// Everything is plain text — no native ToolCall/ToolResult objects.
		if cfg.PromptTools {
			tc, preamble := ParsePromptToolCall(resp.Content, handlers)
			if tc == nil {
				// No tool call — LLM is done. But first re-drain any
				// mid-flight injection that landed during this final
				// round (see the native-path finalize for the full
				// rationale); continue instead of finishing if there's
				// pending input. InjectionDrain, not OnRoundStart.
				if cfg.InjectionDrain != nil && round < maxRounds {
					if injected := cfg.InjectionDrain(); len(injected) > 0 {
						Debug("[agent_loop] pre-finalize injection (prompt-tools): %d note(s) — continuing", len(injected))
						history = append(history, Message{Role: "assistant", Content: resp.Content, Reasoning: resp.Reasoning})
						history = append(history, injected...)
						continue
					}
				}
				// Record and return.
				history = append(history, Message{Role: "assistant", Content: resp.Content, Reasoning: resp.Reasoning})
				if cfg.OnStep != nil {
					cfg.OnStep(StepInfo{Round: round, Content: resp.Content, Done: true})
				}
				return resp, history, nil
			}

			if cfg.MaskDebugOutput {
				Debug("[agent_loop] prompt-tool call: %s([masked: %d bytes])", tc.Name, len(formatArgs(tc.Args)))
			} else {
				Debug("[agent_loop] prompt-tool call: %s (args=%d bytes)", tc.Name, len(formatArgs(tc.Args)))
				Trace("[agent_loop] prompt-tool call: %s(%s)", tc.Name, formatArgs(tc.Args))
			}

			// Record the assistant's message (preamble only, strip the tag).
			if preamble != "" {
				history = append(history, Message{Role: "assistant", Content: preamble})
			}

			// Confirmation check.
			if needsConfirm[tc.Name] {
				if !confirmFn(tc.Name, formatArgs(tc.Args)) {
					Debug("[agent_loop] prompt-tool denied: %s", tc.Name)
					history = append(history, Message{
						Role:    "user",
						Content: fmt.Sprintf("Tool call to %s was denied.", tc.Name),
					})
					if cfg.OnStep != nil {
						cfg.OnStep(StepInfo{Round: round, ToolCalls: []ToolCall{*tc}, ToolErrors: 1})
					}
					continue
				}
			}

			// Execute the tool.
			output, toolErr := safeInvoke(tc.Name, handlers[tc.Name], tc.Args)
			toolFiredThisTurn = true
			toolErrors := 0
			var resultText string
			if toolErr != nil {
				resultText = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, toolErr)
				toolErrors = 1
				cumulativeToolErrors++
			} else {
				resultText = fmt.Sprintf("Tool result from %s:\n%s", tc.Name, output)
			}
			if cfg.MaskDebugOutput {
				Debug("[agent_loop] prompt-tool result: %s: [masked: %d bytes]", tc.Name, len(resultText))
			} else {
				Debug("[agent_loop] prompt-tool result: %s (%d bytes)", tc.Name, len(resultText))
				Trace("[agent_loop] prompt-tool result: %s", resultText)
			}

			// Send result back as a plain user message.
			history = append(history, Message{Role: "user", Content: resultText})
			prevHadToolCalls = true

			if cfg.OnStep != nil {
				cfg.OnStep(StepInfo{Round: round, ToolCalls: []ToolCall{*tc}, ToolErrors: toolErrors})
			}
			continue
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
		if len(resp.ToolCalls) > 0 && (strings.Contains(resp.Content, "<tool_call>") || strings.Contains(resp.Content, "<function=")) {
			Debug("[agent_loop] stripping echoed tool-call markup from content alongside native ToolCalls")
			resp.Content = StripToolCallMarkup(resp.Content)
		}

		// Record assistant response.
		history = append(history, Message{
			Role:      "assistant",
			Content:   resp.Content,
			Reasoning: resp.Reasoning,
			ToolCalls: resp.ToolCalls,
		})

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
		if len(resp.ToolCalls) == 0 {
			parsed := ParseTextToolCall(resp.Content, handlers, toolDefs)
			if parsed == nil && resp.Reasoning != "" && strings.Contains(resp.Reasoning, "<function=") {
				if reasoningCall := ParseTextToolCall(resp.Reasoning, handlers, toolDefs); reasoningCall != nil {
					Debug("[agent_loop] parsed tool call out of reasoning channel: %s", reasoningCall.Name)
					parsed = reasoningCall
				}
			}
			if parsed != nil {
				Debug("[agent_loop] parsed text-based tool call: %s", parsed.Name)
				resp.ToolCalls = []ToolCall{*parsed}
				// Strip the synthesized tool-call markup (XML <tool_call>
				// or bare <function=...>...</function>) from resp.Content
				// so subsequent rounds and the rescue path don't expose
				// the markup OR any preceding narration to the user. The
				// real action lives in the dispatched tool now; the text
				// shouldn't trail along.
				resp.Content = StripToolCallMarkup(resp.Content)
				history[len(history)-1] = Message{
					Role:      "assistant",
					Content:   resp.Content,
					Reasoning: resp.Reasoning,
					ToolCalls: resp.ToolCalls,
				}
			} else if strings.Contains(resp.Content, "<function=") || strings.Contains(resp.Content, "<tool_call>") {
				// Orphaned XML — the model emitted a tool-call attempt
				// but the name didn't resolve (typo, hallucinated tool
				// name like "run_shell_command" instead of "run_local").
				// Strip the markup so the user doesn't see XML, and
				// inject a corrective so the model gets a chance to
				// retry with the right name.
				attemptedName, _ := parseFunctionTagToolCall(resp.Content)
				resp.Content = StripToolCallMarkup(resp.Content)
				history[len(history)-1] = Message{
					Role:      "assistant",
					Content:   resp.Content,
					Reasoning: resp.Reasoning,
				}
				if promiseCorrectionsTotal < maxPromiseCorrections && round < maxRounds {
					hint := ""
					if attemptedName != "" {
						hint = fmt.Sprintf(" You attempted to call %q which is not a registered tool.", attemptedName)
						if suggestion := nearestToolName(attemptedName, handlers); suggestion != "" {
							hint += fmt.Sprintf(" Did you mean %q?", suggestion)
						}
					}
					Debug("[agent_loop] orphaned XML tool-call detected (name=%q), re-prompting: correction %d/%d", attemptedName, promiseCorrectionsTotal+1, maxPromiseCorrections)
					emitDiag("tool-markup-corrected", fmt.Sprintf("The reply wrote tool-call XML for an unknown tool (%q); markup stripped and re-prompted for a real call.", attemptedName))
					settleRound() // finalize the stripped prose so the retry doesn't concatenate into it
					history = append(history, Message{
						Role:    "user",
						Content: "Your previous response contained tool-call XML markup with a name that doesn't match any available tool." + hint + " Look at your tool catalog for the exact tool name. Use the native function-calling format, not text markup. Try again now.",
					})
					promiseCorrectionsTotal++
					continue
				}
			} else if containsFakeToolCodeBlock(resp.Content) {
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
				attemptedName := extractFakeToolCodeName(resp.Content)
				resp.Content = stripFakeToolCodeBlocks(resp.Content)
				history[len(history)-1] = Message{
					Role:      "assistant",
					Content:   resp.Content,
					Reasoning: resp.Reasoning,
				}
				if promiseCorrectionsTotal < maxPromiseCorrections && round < maxRounds {
					hint := ""
					if attemptedName != "" {
						hint = fmt.Sprintf(" You appeared to invoke %q.", attemptedName)
					}
					Debug("[agent_loop] fake <tool_code>/::name():: block detected (name=%q), re-prompting: correction %d/%d", attemptedName, promiseCorrectionsTotal+1, maxPromiseCorrections)
					emitDiag("tool-markup-corrected", fmt.Sprintf("The reply wrote a tool call as plain text (%q) instead of a real call; markup stripped and re-prompted.", attemptedName))
					settleRound() // finalize the stripped prose so the retry doesn't concatenate into it
					history = append(history, Message{
						Role:    "user",
						Content: "Your previous response wrote a tool invocation as plain TEXT (in a <tool_code> block or ::name(...):: form)." + hint + " That format does NOT execute — only structured tool_calls do. Re-issue the call NOW using the framework's native tool-calling mechanism. Do not wrap it in <tool_code>, do not use ::name():: syntax, do not narrate 'Creating the tool now…' — just emit the structured call.",
					})
					promiseCorrectionsTotal++
					continue
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
		// up to maxPromiseCorrections times per session.
		if len(resp.ToolCalls) == 0 {
			// Action-promise correction DISABLED for now — it false-positived
			// on ordinary conversational replies ("I'll try to nail the house
			// next time."), burning rounds re-prompting for an action the model
			// never intended. Flip to true to re-enable; the reasoning-collapse
			// correction below is unaffected either way.
			const actionPromiseCorrection = false
			if actionPromiseCorrection && promiseCorrectionsTotal < maxPromiseCorrections && round < maxRounds && !toolFiredThisTurn && containsActionPromise(resp.Content) {
				Debug("[agent_loop] action-promise without tool call detected, re-prompting (correction %d/%d): %q", promiseCorrectionsTotal+1, maxPromiseCorrections, truncForLog(resp.Content, 80))
				history = append(history, Message{
					Role:    "user",
					Content: "You stated an intention to take an action (e.g. 'let me try', 'one moment') but called no tool. Either call the tool now to actually do what you said, or reply plainly that you can't proceed and explain what you tried. Do NOT promise further action without taking it.",
				})
				promiseCorrectionsTotal++
				continue
			}

			// No-arg-tool-mention correction: the model named a KNOWN
			// zero-argument tool in its reply (e.g. "let me get_joke") but
			// emitted no structured call. parseNaturalToolCall can't rescue a
			// no-arg tool from prose (there are no args to extract), so the tool
			// silently never runs and the model may narrate a result it never
			// got. Nudge once to either issue the real call or answer plainly.
			// FAR narrower than the disabled actionPromiseCorrection above: it
			// fires ONLY on an exact, token-bounded, snake_case tool NAME (those
			// don't occur in ordinary prose), only when NO tool fired this turn,
			// is capped by promiseCorrectionsTotal, and the nudge gives an
			// explicit "if you didn't mean to, answer directly" out. Flip the
			// const to disable if it ever proves noisy.
			const noArgToolMentionCorrection = true
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
			contentIsPreamble := len(strings.TrimSpace(resp.Content)) <= noArgCorrectionMaxContentLen
			if noArgToolMentionCorrection && contentIsPreamble && !cfg.DisableToolMentionCorrection && promiseCorrectionsTotal < maxPromiseCorrections && round < maxRounds && !toolFiredThisTurn {
				if name := mentionedNoArgTool(resp.Content, handlers, toolDefs); name != "" {
					Debug("[agent_loop] no-arg tool %q named in prose without a call, re-prompting: correction %d/%d", name, promiseCorrectionsTotal+1, maxPromiseCorrections)
					emitDiag("tool-mention-corrected", fmt.Sprintf("The reply named the %q tool without calling it; re-prompted to either run it or answer plainly.", name))
					settleRound() // finalize the preamble so the retry doesn't concatenate into it
					history = append(history, Message{
						Role:    "user",
						Content: fmt.Sprintf("Your previous response referred to the %q tool but did not actually call it (it takes no arguments, so there was nothing to run). If you intend to use it, emit the real structured tool call NOW. If you did NOT mean to use it, answer the user directly and do not claim you used it.", name),
					})
					promiseCorrectionsTotal++
					continue
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
			// tool. Budget-gated via promiseCorrectionsTotal so it
			// can't loop.
			//
			// The threshold is deliberately near-zero, NOT "short": a
			// complete short reply ("Yes.", "7:57 AM PDT.") is normal
			// for a thinking model, and this round's content has
			// ALREADY streamed to the client — re-prompting makes the
			// model repeat it, so the user watches the same sentence
			// render once per correction (and each retry re-bills the
			// full prompt). Only a round that showed nothing may retry.
			trimmedContent := strings.TrimSpace(resp.Content)
			if promiseCorrectionsTotal < maxPromiseCorrections && round < maxRounds &&
				len(trimmedContent) < 3 && len(resp.Reasoning) > 200 {
				Debug("[agent_loop] reasoning-collapse detected (reasoning=%d chars, content=%d chars), re-prompting: correction %d/%d", len(resp.Reasoning), len(trimmedContent), promiseCorrectionsTotal+1, maxPromiseCorrections)
				emitDiag("empty-round-retried", "A round produced reasoning but no visible reply and no tool call; re-prompted for concrete output.")
				settleRound() // no-op when nothing streamed; keeps the discipline uniform across guards
				history = append(history, Message{
					Role:    "user",
					Content: "Your previous round produced no visible reply (you reasoned but wrote nothing the user can see) and called no tool. Don't end a turn empty-handed: either produce concrete text now, or call a relevant tool. If the user's question is too vague to act on, ask a clarifying question.",
				})
				promiseCorrectionsTotal++
				continue
			}

			// Give-up-with-errors-pending catch. Model emitted no tool
			// calls and ~empty content while tool errors accumulated
			// earlier in this turn AND budget remains — the "I tried,
			// give up" pattern. The forced-final-answer rescue path
			// after the loop would otherwise paper over this with a
			// polite "here's what I did" summary instead of fixing the
			// underlying problem. Push back: inject a continuation
			// nudge that names the error count and the rounds remaining,
			// and re-loop. Budget-gated via promiseCorrectionsTotal so
			// pathological cases can't infinitely re-prompt.
			//
			// Triggers:
			//   - no tool calls THIS round
			//   - empty content (or nearly so — <30 chars after trim)
			//   - cumulative tool errors > 0
			//   - more than 5 rounds remain (don't push at the cap;
			//     the existing wrap-up message owns that case)
			//   - haven't already burned the correction budget
			// trimmedContent reuses the variable declared in the
			// reasoning-collapse check above — same scope, already trimmed.
			roundsLeft := maxRounds - round
			if promiseCorrectionsTotal < maxPromiseCorrections &&
				roundsLeft >= 5 &&
				cumulativeToolErrors > 0 &&
				len(trimmedContent) < 30 {
				Debug("[agent_loop] give-up-with-errors-pending detected (errors=%d, rounds_left=%d, content=%dch), re-prompting: correction %d/%d",
					cumulativeToolErrors, roundsLeft, len(trimmedContent), promiseCorrectionsTotal+1, maxPromiseCorrections)
				emitDiag("giveup-retried", fmt.Sprintf("The turn stopped with %d unaddressed tool error(s) and rounds to spare; re-prompted to adjust and retry rather than give up.", cumulativeToolErrors))
				settleRound() // no-op when nothing streamed; keeps the discipline uniform across guards
				errPlural := ""
				if cumulativeToolErrors != 1 {
					errPlural = "s"
				}
				roundPlural := ""
				if roundsLeft != 1 {
					roundPlural = "s"
				}
				history = append(history, Message{
					Role: "user",
					Content: fmt.Sprintf(
						"You stopped without producing a reply and without calling any tool, but %d tool call%s errored earlier this turn that you didn't follow up on, and you have %d round%s remaining. DON'T end here with a polite summary of what you tried — that's giving up. Re-read the most recent error message(s) carefully, ADJUST your approach (different args, different tool, different sequence), and TRY AGAIN with a real tool call. If you genuinely have no other avenues, say so explicitly — but only after you've actually tried adjusting at least once.",
						cumulativeToolErrors, errPlural, roundsLeft, roundPlural,
					),
				})
				promiseCorrectionsTotal++
				continue
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
			if cfg.InjectionDrain != nil && round < maxRounds {
				if injected := cfg.InjectionDrain(); len(injected) > 0 {
					Debug("[agent_loop] pre-finalize injection: %d note(s) arrived during the final round — continuing instead of finishing", len(injected))
					history = append(history, injected...)
					continue
				}
			}

			if cfg.OnStep != nil {
				cfg.OnStep(StepInfo{
					Round:   round,
					Content: resp.Content,
					Done:    true,
				})
			}
			return resp, history, nil
		}

		// Execute tool calls and collect results.
		// Independent calls run in parallel; confirmable tools are
		// checked serially first to avoid concurrent prompts.
		results := make([]ToolResult, len(resp.ToolCalls))
		toolErrors := 0
		guardBlockedThisRound := false // set when the loop-guard blocks a repeat this round

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
		silentCount := 0
		realCount := 0
		for _, tc := range resp.ToolCalls {
			if tc.Name == "stay_silent" {
				silentCount++
			} else {
				realCount++
			}
		}
		dropAllSilent := silentCount > 0 && realCount > 0
		dedupeSilent := silentCount > 1 && !dropAllSilent
		silentSeen := false

		// First pass: resolve handlers and handle confirmations serially.
		type toolWork struct {
			index   int
			tc      ToolCall
			handler ToolHandlerFunc
			sig     string
		}
		var work []toolWork

		for i, tc := range resp.ToolCalls {
			if tc.Name == "stay_silent" {
				if dropAllSilent {
					Debug("[agent_loop] stay_silent dropped — bundled with %d real tool call(s)", realCount)
					results[i] = ToolResult{
						ID:      tc.ID,
						Content: "Error: stay_silent was ignored because it was bundled with other tool calls. stay_silent closes the turn and must be the ONLY tool call in your response. Complete your other tool work first, observe the results, then call stay_silent alone in a later turn.",
						IsError: true,
					}
					toolErrors++
					continue
				}
				if dedupeSilent {
					if silentSeen {
						Debug("[agent_loop] duplicate stay_silent dropped (already silenced)")
						results[i] = ToolResult{
							ID:      tc.ID,
							Content: "Acknowledged (duplicate). The turn is already closing silently — only one stay_silent call is needed per turn.",
						}
						continue
					}
					silentSeen = true
				}
			}
			if cfg.MaskDebugOutput {
				Debug("[agent_loop] tool call: %s([masked: %d bytes])", tc.Name, len(formatArgs(tc.Args)))
			} else {
				Debug("[agent_loop] tool call: %s (args=%d bytes)", tc.Name, len(formatArgs(tc.Args)))
				Trace("[agent_loop] tool call: %s(%s)", tc.Name, formatArgs(tc.Args))
			}

			handler, ok := handlers[tc.Name]
			if !ok && cfg.ToolFallbackResolver != nil {
				// The name isn't in this round's catalog, but it may be a
				// lazy tool whose handler is still valid (model knows the
				// schema from context and called it directly — no re-load).
				if fb, found := cfg.ToolFallbackResolver(tc.Name); found {
					Debug("[agent_loop] tool %q resolved via fallback (lazy/known tool called directly)", tc.Name)
					handler, ok = fb, true
				}
			}
			if !ok {
				errMsg := fmt.Sprintf("Error: unknown tool '%s'", tc.Name)
				Debug("[agent_loop] %s", errMsg)
				results[i] = ToolResult{ID: tc.ID, Content: errMsg, IsError: true}
				toolErrors++
				continue
			}

			if needsConfirm[tc.Name] {
				if !confirmFn(tc.Name, formatArgs(tc.Args)) {
					Debug("[agent_loop] tool call denied by user: %s", tc.Name)
					results[i] = ToolResult{ID: tc.ID, Content: "Error: tool call denied by user", IsError: true}
					toolErrors++
					continue
				}
			}

			// Repeated-failure loop-guard: this exact call (name+args) has
			// already errored repeatFailLimit times this turn — don't run it
			// again. Hand back a hard STOP so the model breaks the loop instead
			// of hammering the same dead end until the round budget is gone.
			sig := tc.Name + "\x00" + formatArgs(tc.Args)
			if repeatFail[sig] >= repeatFailLimit {
				Debug("[agent_loop] loop-guard: %s blocked (%d prior identical failures this turn)", tc.Name, repeatFail[sig])
				guardBlockedThisRound = true
				results[i] = ToolResult{
					ID:      tc.ID,
					Content: fmt.Sprintf("STOP — you have already called '%s' with these exact arguments %d times this turn and it failed the same way each time. Calling it again will NOT change the result. Do something different: try another approach or different arguments, or tell the user plainly that this isn't working and what you tried. Do not repeat this call.", tc.Name, repeatFail[sig]),
					IsError: true,
				}
				toolErrors++
				continue
			}

			// Identical no-progress guard: this exact call keeps returning the
			// SAME result. Unlike the error guard above this fires on SUCCESS too,
			// catching a valid-but-pointless polling loop the error counter misses.
			if repeatSame[sig] >= repeatSameLimit {
				Debug("[agent_loop] loop-guard: %s blocked (%d identical no-progress repeats this turn)", tc.Name, repeatSame[sig])
				guardBlockedThisRound = true
				results[i] = ToolResult{
					ID:      tc.ID,
					Content: fmt.Sprintf("STOP — you have already called '%s' with these exact arguments %d times this turn and it returned the SAME result every time. It is giving you no new information and making no progress. Do NOT call it again. Answer the user with what you already have, use a DIFFERENT tool, or tell them plainly you cannot get what they asked for.", tc.Name, repeatSame[sig]),
					IsError: true,
				}
				toolErrors++
				continue
			}

			work = append(work, toolWork{index: i, tc: tc, handler: handler, sig: sig})
		}

		// RoundAbortTools: when a control tool (ask_user, respond_directly,
		// plan_set, …) is present in the batch, keep only the FIRST such
		// tool and drop everything else with a SKIPPED notice. The loop
		// will break after this round (handled below). This prevents the
		// LLM from bundling "ask the user a question" with "do the thing
		// anyway" in the same response.
		abortSet := map[string]bool{}
		for _, n := range cfg.RoundAbortTools {
			abortSet[n] = true
		}
		roundAborted := false
		if len(abortSet) > 0 {
			abortIdx := -1
			for i, w := range work {
				if abortSet[w.tc.Name] {
					abortIdx = i
					break
				}
			}
			if abortIdx >= 0 {
				roundAborted = true
				abortName := work[abortIdx].tc.Name
				for i, w := range work {
					if i == abortIdx {
						continue
					}
					results[w.index] = ToolResult{
						ID:      w.tc.ID,
						Content: fmt.Sprintf("[SKIPPED] Tool '%s' was dropped because '%s' was called in the same response. Control tools (ask_user, respond_directly, plan_set, …) end the round — they must be the ONLY tool call. If you need to do other work first, do it in an earlier round.", w.tc.Name, abortName),
						IsError: true,
					}
					toolErrors++
				}
				work = []toolWork{work[abortIdx]}
				Debug("[agent_loop] round aborted by control tool %q — dropped %d other call(s)", abortName, len(resp.ToolCalls)-1)
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
		effectiveGroups := make([][]string, 0, len(cfg.SingleFireGroups)+len(singleFireTools))
		effectiveGroups = append(effectiveGroups, cfg.SingleFireGroups...)
		for name := range singleFireTools {
			effectiveGroups = append(effectiveGroups, []string{name})
		}
		for _, group := range effectiveGroups {
			if len(group) < 1 {
				continue
			}
			groupSet := map[string]bool{}
			for _, n := range group {
				groupSet[n] = true
			}
			firstIdx := -1
			var filtered []toolWork
			for _, w := range work {
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
				results[w.index] = ToolResult{
					ID:      w.tc.ID,
					Content: skipMsg,
					IsError: true,
				}
				toolErrors++
			}
			if firstIdx >= 0 && len(filtered) < len(work) {
				Debug("[agent_loop] single-fire %v — dropped %d excess call(s)", group, len(work)-len(filtered))
				work = filtered
			}
		}

		// SerialTools: discard all but the first approved call so the LLM
		// must observe each result before deciding what to run next.
		if cfg.SerialTools && len(work) > 1 {
			for _, w := range work[1:] {
				results[w.index] = ToolResult{
					ID:      w.tc.ID,
					Content: fmt.Sprintf("[SKIPPED] Submit one tool call at a time. Resubmit '%s' after reviewing the result above.", w.tc.Name),
				}
			}
			work = work[:1]
		}

		// Second pass: execute approved tool calls in parallel.
		debugResult := func(name, output string) {
			if cfg.MaskDebugOutput {
				Debug("[agent_loop] tool result: %s: [masked: %d bytes]", name, len(output))
			} else {
				Debug("[agent_loop] tool result: %s (%d bytes)", name, len(output))
				Trace("[agent_loop] tool result: %s: %s", name, output)
			}
		}
		debugToolErr := func(name string, err error) {
			if cfg.MaskDebugOutput {
				Debug("[agent_loop] tool error: %s: [masked]", name)
			} else {
				Debug("[agent_loop] tool error: %s: %s", name, err)
			}
		}

		if len(work) > 0 {
			toolFiredThisTurn = true
		}
		if len(work) == 1 {
			// Single call — no goroutine overhead.
			w := work[0]
			output, err := safeInvoke(w.tc.Name, w.handler, w.tc.Args)
			if err != nil {
				debugToolErr(w.tc.Name, err)
				results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
				toolErrors++
			} else {
				debugResult(w.tc.Name, output)
				results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
			}
		} else if len(work) > 1 {
			var wg sync.WaitGroup
			var errCount int32
			invokeStore := func(w toolWork) {
				output, err := safeInvoke(w.tc.Name, w.handler, w.tc.Args)
				if err != nil {
					debugToolErr(w.tc.Name, err)
					results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
					atomic.AddInt32(&errCount, 1)
				} else {
					debugResult(w.tc.Name, output)
					results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
				}
			}
			// Partition: serial-fire tool calls run SEQUENTIALLY in submission
			// order, so a stateful authoring batch like tool_def[delete X,
			// create Y] applies in the order the LLM intended and can't race on
			// the same record. Everything else still runs in parallel. work is
			// already in submission order, so one ordered goroutine over the
			// serial slice preserves it while the parallel calls fan out.
			var serial []toolWork
			for _, w := range work {
				if serialFireTools[w.tc.Name] {
					serial = append(serial, w)
					continue
				}
				wg.Add(1)
				go func(w toolWork) {
					defer wg.Done()
					invokeStore(w)
				}(w)
			}
			if len(serial) > 0 {
				wg.Add(1)
				go func(items []toolWork) {
					defer wg.Done()
					for _, w := range items {
						invokeStore(w)
					}
				}(serial)
			}
			wg.Wait()
			toolErrors += int(atomic.LoadInt32(&errCount))
		}

		// Update the repeated-failure loop-guard from this round's outcomes:
		// bump the per-signature error count on failure, reset it on success
		// (so legitimate polling that finally changes isn't penalized).
		for _, w := range work {
			if w.sig == "" {
				continue
			}
			if results[w.index].IsError {
				repeatFail[w.sig]++
			} else {
				delete(repeatFail, w.sig)
			}
			// Identical-repeat tracking (see repeatSame decl). Count byte-identical
			// results per signature on success OR error; a changed result resets.
			if prev, seen := lastToolContent[w.sig]; seen && prev == results[w.index].Content {
				repeatSame[w.sig]++
			} else {
				repeatSame[w.sig] = 0
			}
			lastToolContent[w.sig] = results[w.index].Content
		}

		// Failure-SHAPE bookkeeping (see errShapeCount decl). Counts how many
		// times one normalized failure text has come back this turn, across
		// ANY call that produced it — the signal the signature-keyed guards
		// above miss when the model varies its arguments between attempts.
		for _, w := range work {
			if !results[w.index].IsError {
				continue
			}
			shape := normalizeFailureShape(results[w.index].Content)
			if shape == "" {
				continue
			}
			errShapeCount[shape]++
			n := errShapeCount[shape]
			// Say it plainly, once. The model can see each failure but not
			// that it has now hit the SAME one from several directions —
			// which is the fact that should change its approach.
			if n >= errShapeNudgeAt && !errShapeNudged[shape] {
				errShapeNudged[shape] = true
				Debug("[agent_loop] failure-shape guard: %q seen %d times this turn — nudging", oneLineShape(shape), n)
				msg, consulted := failureShapeCorrection(n, oneLineShape(shape), results[w.index].Content, cfg.Consult)
				history = append(history, Message{Role: "user", Content: msg})
				if consulted {
					Log("[agent_loop] failure-shape guard: consulted on %q after %d hits", oneLineShape(shape), n)
					if cfg.OnDiag != nil {
						cfg.OnDiag("consulted", fmt.Sprintf("Hit the same failure %d times (%q) — a stronger model was consulted and its advice was given to the agent.", n, oneLineShape(shape)))
					}
				}
			}
			// Still hitting it. The turn has stopped being worth frontier
			// tokens — finish it on the worker. De-escalating rather than
			// terminating keeps the failure mode safe: worst case on a false
			// positive is a cheaper model, not a truncated turn.
			if n >= errShapeDeescalateAt && deescalated == "" {
				deescalated = "no-progress"
				Log("[agent_loop] failure-shape guard: %q hit %d times with no progress — remaining rounds run on the worker tier", oneLineShape(shape), n)
				if cfg.OnDiag != nil {
					cfg.OnDiag("tier_deescalated", fmt.Sprintf("Hit the same failure %d times with no progress (%q) — the rest of this turn ran on the worker model instead of the lead model.", n, oneLineShape(shape)))
				}
			}
		}

		// BREADCRUMB: tool dispatch complete. If we see this line but
		// no subsequent "round N+1: starting", the hang is in the
		// bookkeeping/OnStep/iteration-restart path. Log-level (not
		// Debug) so it surfaces regardless of debug flags.
		Log("[agent_loop] round %d: tool dispatch complete (%d tools, %d errors) — appending results to history", round, len(work), toolErrors)
		// Add tool results to history for the next LLM round.
		history = append(history, Message{
			Role:        "user",
			ToolResults: results,
		})
		// If a tool queued images for the model to look at, inject them as a
		// vision message NOW so the next round actually sees them. Producers:
		// view_video (samples frames from a clip) and generate_image (shows the
		// model its own output so it can verify the result matches the request).
		// Without this the bytes were extracted and dropped, and the model
		// hallucinated a description of something it never saw. Goes right after
		// the tool results — the order is assistant-tool_calls -> tool_results ->
		// the images it asked to see — and the wording is producer-agnostic: the
		// preceding tool result says what the images are.
		if cfg.DrainViewImages != nil {
			if imgs := cfg.DrainViewImages(); len(imgs) > 0 {
				history = append(history, Message{
					Role:    "user",
					Content: fmt.Sprintf("Here are %d image(s) queued for you to view, in order — the preceding tool result says what they are. Look, and describe or verify only what is actually visible; do not guess beyond what is shown.", len(imgs)),
					Images:  imgs,
				})
			}
		}
		prevHadToolCalls = true

		// Failure-streak bookkeeping. A round counts as a "failure"
		// when EVERY tool result this round has IsError=true. Any
		// successful result resets the streak. After N consecutive
		// failure rounds, inject the pivot nudge once per streak.
		allFailed := len(results) > 0
		for i := range results {
			if !results[i].IsError {
				allFailed = false
				break
			}
		}
		if allFailed {
			failureStreak++
			if !failureStreakWarned && failureStreak >= failureStreakThreshold {
				Debug("[agent_loop] failure streak hit %d — injecting pivot nudge", failureStreak)
				history = append(history, Message{
					Role: "user",
					Content: fmt.Sprintf(
						"You've hit %d rounds in a row where every tool call failed. Recommending checking other vectors first before resuming this approach — a different tool, a different angle, or asking the user for clarification is often faster than continuing to iterate here.",
						failureStreak),
				})
				failureStreakWarned = true
			}
		} else {
			if failureStreak > 0 {
				Debug("[agent_loop] failure streak reset (was %d) after successful tool call", failureStreak)
			}
			failureStreak = 0
			failureStreakWarned = false
		}

		// Wedge break-out: a round whose only tool activity was a loop-guard-BLOCKED
		// call (blocked this round AND every result errored) is pure spinning — the
		// model is re-issuing the dead call and ignoring the STOP directive. After a
		// couple of these in a row, stop looping and force a clean final answer
		// instead of burning the rest of the budget. (Any successful tool call resets
		// the streak via the else branch.)
		if guardBlockedThisRound && allFailed {
			guardBlockedStreak++
			if guardBlockedStreak >= guardBlockedBreakLimit {
				Debug("[agent_loop] loop-guard wedge: %d blocked-with-no-progress rounds — forcing final answer", guardBlockedStreak)
				forceFinal = true
				break
			}
		} else {
			guardBlockedStreak = 0
		}

		// keep_going spin guard (declared above). A round whose ONLY tool
		// call(s) were keep_going is a promise-to-act with no action. First
		// repeat gets a firm corrective injected; a further repeat forces the
		// final answer so the model can't burn the budget re-promising.
		keepGoingOnly := len(resp.ToolCalls) > 0
		for _, tc := range resp.ToolCalls {
			if tc.Name != "keep_going" {
				keepGoingOnly = false
				break
			}
		}
		if keepGoingOnly {
			keepGoingStreak++
			if keepGoingStreak >= keepGoingSpinLimit {
				Debug("[agent_loop] keep_going spin: %d consecutive keep_going-only rounds — forcing final answer", keepGoingStreak)
				forceFinal = true
				break
			}
			// One firm nudge before the force-final: keep_going fired but no
			// real tool, so the promise-correction path never ran.
			history = append(history, Message{
				Role:    "user",
				Content: "You have signalled continue without taking any action. Do NOT call keep_going again. This round, either emit the ACTUAL tool call you intend (the tool is already loaded — call it directly), or, if you cannot, give your final answer to the user now.",
			})
		} else {
			keepGoingStreak = 0
		}

		if cfg.OnStep != nil {
			cfg.OnStep(StepInfo{
				Round:      round,
				Content:    resp.Content,
				ToolCalls:  resp.ToolCalls,
				ToolErrors: toolErrors,
				Done:       false,
			})
		}
		cumulativeToolErrors += toolErrors

		// stay_silent closes the turn. The "do not call any more tools"
		// instruction in the tool result is unreliable — Qwen 3 in
		// particular keeps emitting stay_silent over and over. Once the
		// model has called stay_silent successfully, break the agent
		// loop server-side so no further LLM rounds happen.
		for _, w := range work {
			if w.tc.Name == "stay_silent" && !results[w.index].IsError {
				Debug("[agent_loop] stay_silent fired — closing turn")
				// Honor the suppression — stay_silent's whole purpose. Blank the
				// reply text so every caller (web reply, channel outbound,
				// dispatch result) emits NOTHING; attachments gathered this turn
				// still flow via their own path. Without this the Silenced flag
				// was set but never consumed, so stay_silent closed the turn yet
				// the model's text still showed ("stay_silent doesn't work").
				if resp != nil {
					resp.Content = ""
				}
				return resp, history, nil
			}
		}

		// RoundAbortTools: if a control tool fired successfully, close the
		// loop server-side. The orchestrate flow uses cancelOrch() in the
		// handler too, but that races against the in-flight tool batch; this
		// is the deterministic stop.
		if roundAborted {
			for _, w := range work {
				if abortSet[w.tc.Name] && !results[w.index].IsError {
					Debug("[agent_loop] control tool %q fired — closing turn", w.tc.Name)
					return resp, history, nil
				}
			}
		}
	}

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
	if lastResp != nil && strings.TrimSpace(lastResp.Content) == "" {
		floor := len(history) - rescueLookback
		if floor < 0 {
			floor = 0
		}
		for i := len(history) - 1; i >= floor; i-- {
			m := history[i]
			if m.Role == "assistant" && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
				Debug("[agent_loop] rescued empty final response; using last non-empty assistant turn (history[%d])", i)
				lastResp = &Response{Content: m.Content}
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
	lastRoundToolCalled := lastResp != nil && len(lastResp.ToolCalls) > 0
	if lastResp != nil && (forceFinal || lastRoundToolCalled || strings.TrimSpace(lastResp.Content) == "") && T.LLM != nil {
		if forceFinal {
			Debug("[agent_loop] wedge break — issuing a forced-final-answer call with no tools")
		} else if lastRoundToolCalled {
			Debug("[agent_loop] budget exhausted mid-tool-call (last content is narration, not a synthesis) — issuing a forced-final-answer call with no tools")
		} else {
			Debug("[agent_loop] empty after lookback rescue — issuing a forced-final-answer call with no tools")
		}
		wrapHistory := append([]Message{}, history...)
		wrapHistory = append(wrapHistory, Message{
			Role:    "user",
			Content: "Stop calling tools now and produce your final answer for the user from whatever you've gathered so far — even if incomplete, summarize what you found and what you tried, and if something didn't work, say so plainly. Just text, no tool calls.",
		})
		// No-tools, no-think final call so the model has nothing to
		// chase — must produce text. Inherit RouteKey for telemetry.
		var wrapOpts []ChatOption
		wrapOpts = append(wrapOpts, WithSystemPrompt(systemPrompt))
		wrapOpts = append(wrapOpts, WithoutAutoDate()) // date is on the user turn, not the system prompt
		f := false
		wrapOpts = append(wrapOpts, WithThink(f))
		if cfg.RouteKey != "" {
			wrapOpts = append(wrapOpts, WithRouteKey(cfg.RouteKey))
		}
		if forced, err := T.LLM.Chat(ctx, wrapHistory, wrapOpts...); err == nil && forced != nil {
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
				if strings.Contains(forced.Content, "<function=") || strings.Contains(forced.Content, "<tool_call>") {
					if name, _ := parseFunctionTagToolCall(forced.Content); strings.TrimSpace(name) != "" {
						forced.Content = "Ran out of steps before finishing — it was about to call \"" + name + "\", which did NOT run. Raise this agent's worker-round limit or narrow its task."
					} else {
						forced.Content = "Ran out of steps before finishing — an action it was about to take did NOT run. Raise this agent's worker-round limit or narrow its task."
					}
				}
				lastResp = forced
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
	if lastResp != nil {
		lastResp.HitRoundCap = true
	}
	return lastResp, history, nil
}

// ParseTextToolCall attempts to extract a tool call from text content when the
// model doesn't use structured tool calling. Tries three forms in order:
//
//  1. XML-style: <function=name><parameter=key>value</parameter></function>,
//     optionally wrapped in <tool_call> tags. Emitted by Llama-3 / Qwen /
//     Hermes-style instruction tunes even in native function-calling mode.
//  2. JSON: {"name": "...", "parameters": {...}} or {"name": "...", "arguments": {...}}.
//  3. Natural-language tool name in prose (last-resort fallback).
//
// toolDefs is consulted to validate that any synthesized call satisfies the
// tool's `Required` fields. If the extractor produces a call missing required
// args (typical of the prose-scan fallback when the model reasons about a
// tool but doesn't emit structured args), it's rejected — better to let the
// loop count the round as "model produced content but didn't act" than to
// fire a guaranteed-to-fail tool call and burn a round on the error.
func ParseTextToolCall(content string, handlers map[string]ToolHandlerFunc, toolDefs []Tool) *ToolCall {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// XML-style first — when the model emits this form, the JSON
	// parser would otherwise see "{" inside the body and try (and
	// fail) to JSON-parse the whole thing. Detect by the function tag.
	if strings.Contains(content, "<function=") {
		// If wrapped in <tool_call>...</tool_call>, peel that off first
		// so the inner XML parser sees the function/parameter pairs
		// directly. Some models emit with wrapper, some without.
		body := content
		if start := strings.Index(body, "<tool_call>"); start >= 0 {
			if end := strings.Index(body, "</tool_call>"); end > start {
				body = strings.TrimSpace(body[start+len("<tool_call>") : end])
			}
		}
		if name, args := parseFunctionTagToolCall(body); name != "" {
			if _, ok := handlers[name]; ok {
				tc := &ToolCall{
					ID:   fmt.Sprintf("text_%s", UUIDv4()),
					Name: name,
					Args: args,
				}
				if hasRequired(tc, toolDefs) {
					return tc
				}
				Debug("[agent_loop] dropping XML-style tool call '%s' — missing required args", name)
			}
		}
	}

	// JSON form. Validate required fields below rather than trusting blindly.
	// Peel a wrapping <tool_call>...</tool_call> first when present —
	// some models emit a JSON call (no <function=> tag pair inside) but
	// still wrap it in the tool-call envelope. Without the peel the JSON
	// parser sees raw "<tool_call>" before the brace and fails; the
	// orchestrator then renders the markup as visible text and burns the
	// round. Mirrors the same peel the XML branch above does.
	jsonBody := content
	if start := strings.Index(jsonBody, "<tool_call>"); start >= 0 {
		if end := strings.Index(jsonBody, "</tool_call>"); end > start {
			jsonBody = strings.TrimSpace(jsonBody[start+len("<tool_call>") : end])
		}
	}
	if tc := parseJSONToolCall(jsonBody, handlers); tc != nil {
		if hasRequired(tc, toolDefs) {
			return tc
		}
		Debug("[agent_loop] dropping synthesized JSON tool call '%s' — missing required args", tc.Name)
	}

	// Last-resort: scan for a known tool name mentioned in the text.
	// Thinking models often reason like "call run_healthcheck with args ..."
	// without emitting the actual structured call.
	if tc := parseNaturalToolCall(content, handlers); tc != nil {
		if hasRequired(tc, toolDefs) {
			return tc
		}
		Debug("[agent_loop] dropping synthesized natural-language tool call '%s' — could not extract required args from prose", tc.Name)
	}
	return nil
}

// StripToolCallMarkup removes fake tool-call markup from streamed
// content so it doesn't leak to the user-visible bubble. Used after
// the agent loop promotes a synthesized tool call (or re-prompts the
// LLM for a corrected call) — the original markup stays out of the
// chat surface, only the corrected behavior is visible.
//
// Handles four shapes:
//   - <tool_call>...</tool_call> (Qwen / Hermes — JSON or function form inside)
//   - <function=...>...</function> (bare Hermes/Qwen)
//   - <tool_code>...</tool_code> (Gemini training-data artifact)
//   - ```tool_code ... ``` (markdown code fence variant of the same)
//
// Unclosed tags drop everything from the open onward — safer than
// leaving partial markup that the bubble renders raw.
func StripToolCallMarkup(s string) string {
	// Drop <tool_call>...</tool_call> wrappers first (they may contain
	// JSON-shape calls or function-tag calls inside).
	for {
		start := strings.Index(s, "<tool_call>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</tool_call>")
		if end < 0 || end < start {
			// Unclosed tag — drop everything from <tool_call> onward
			// to be safe.
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</tool_call>"):]
	}
	// Drop bare <function=...>...</function> blocks (Hermes/Qwen form
	// emitted without the tool_call wrapper).
	for {
		start := strings.Index(s, "<function=")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</function>")
		if end < 0 || end < start {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</function>"):]
	}
	// Drop <tool_code>...</tool_code> blocks. This is Gemini's
	// training-data artifact format — Qwen sometimes copies it under
	// confusion. The promise-detector elsewhere in the loop catches
	// the pattern and re-prompts; stripping here ensures the bubble
	// doesn't show the raw markup if corrections were exhausted or
	// the strip is being called after the loop gave up.
	for {
		start := strings.Index(s, "<tool_code>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</tool_code>")
		if end < 0 || end < start {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</tool_code>"):]
	}
	// Drop ```tool_code ... ``` markdown code-fence variants. Same
	// failure pattern as bare <tool_code> blocks but emitted with
	// markdown wrapping. Fenced blocks may have trailing newlines
	// inside the fence, so match through to the closing ```.
	for {
		start := strings.Index(s, "```tool_code")
		if start < 0 {
			break
		}
		// Find the closing ``` after the open fence.
		searchFrom := start + len("```tool_code")
		end := strings.Index(s[searchFrom:], "```")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[searchFrom+end+len("```"):]
	}
	// Note: we do NOT strip "let me try" / "one moment" narration here
	// even though it's noise the user shouldn't see. The promise-detector
	// elsewhere in the loop catches that pattern and re-prompts the LLM
	// to produce clean output, which is more useful than silent removal
	// (the LLM learns the pattern is wrong; doesn't just keep doing it).
	return strings.TrimSpace(s)
}

// containsFakeToolCodeBlock detects training-data-artifact tool-call
// formats the LLM writes as plain text instead of structured calls:
//
//   - <tool_code>...</tool_code> blocks (Gemini's text tool format)
//   - ```tool_code\n...\n``` markdown code fences tagged tool_code
//   - ```json\n[{"tool_def": {...}}]\n``` JSON-shaped tool-call lists
//     in markdown fences (Qwen variant where the LLM writes what a
//     tool_calls field WOULD look like as JSON content)
//   - ::tool_name(arg=val, ...):: cascade-style invocations (a
//     gohort-shaped fake that Qwen has invented in training data;
//     looks like Smalltalk/Ruby cascade with gohort tool names)
//
// Used by the agent loop to detect "model wrote a tool call as
// narrative text" and inject a corrective re-prompt instead of
// silently terminating with the call un-executed.
func containsFakeToolCodeBlock(s string) bool {
	if strings.Contains(s, "<tool_code>") {
		return true
	}
	if strings.Contains(s, "```tool_code") {
		return true
	}
	if containsFakeJSONToolBlock(s) {
		return true
	}
	// ::name(  — Smalltalk-cascade-shaped fake. Require alphanumeric
	// + underscore for the name, an opening paren, and a closing
	// :: somewhere downstream so we don't false-positive on the
	// common "::" markdown-headline separator or C++-style scope
	// resolution that might appear in legitimate prose.
	if idx := strings.Index(s, "::"); idx >= 0 {
		rest := s[idx+2:]
		nameEnd := 0
		for nameEnd < len(rest) {
			c := rest[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd > 0 && nameEnd < len(rest) && rest[nameEnd] == '(' && strings.Contains(rest[nameEnd:], "::") {
			return true
		}
	}
	return false
}

// fakeJSONFenceToolNames is the set of tool names whose presence
// as a JSON key inside a ```json fence flags the fence as a fake
// tool-call attempt. Restricted to authoring-side names that would
// only legitimately appear via native tool_calls, never as content
// describing what a worker found. Adding ordinary read tools here
// (knowledge_search, fetch_url) would false-positive on workers
// that legitimately return JSON examples mentioning them.
var fakeJSONFenceToolNames = []string{
	"tool_def", "create_agent", "update_agent", "clone_agent",
	"delete_agent", "add_tool", "skill_def", "pipeline",
}

// containsFakeJSONToolBlock detects when the LLM emits a tool call
// as JSON inside a ```json markdown fence — the Qwen variant where
// the model writes what a native tool_calls payload would look like
// as content. The distinguishing signal is an authoring tool name
// appearing as a JSON key inside the fence body. A regular ```json
// fence with no authoring-tool-name key reads as legitimate JSON
// content (a worker showing an API response shape) and is left
// alone.
func containsFakeJSONToolBlock(s string) bool {
	lower := strings.ToLower(s)
	pos := 0
	for {
		idx := strings.Index(lower[pos:], "```json")
		if idx < 0 {
			return false
		}
		fenceStart := pos + idx
		bodyStart := fenceStart + len("```json")
		bodyEnd := strings.Index(s[bodyStart:], "```")
		if bodyEnd < 0 {
			// Unclosed fence — treat as fake if any authoring name
			// appears anywhere from the open onward.
			tail := s[bodyStart:]
			for _, name := range fakeJSONFenceToolNames {
				if strings.Contains(tail, `"`+name+`"`) {
					return true
				}
			}
			return false
		}
		body := s[bodyStart : bodyStart+bodyEnd]
		for _, name := range fakeJSONFenceToolNames {
			if strings.Contains(body, `"`+name+`"`) {
				return true
			}
		}
		// This fence is legit JSON content — advance past it and
		// keep looking for another one.
		pos = bodyStart + bodyEnd + len("```")
	}
}

// extractFakeToolCodeName pulls the first plausible tool-name out
// of a fake <tool_code> or ::name(...):: block so the corrective
// message can reference it ("You appeared to invoke 'tool_def'").
// Returns "" when no name can be extracted.
func extractFakeToolCodeName(s string) string {
	// ::name( form
	if idx := strings.Index(s, "::"); idx >= 0 {
		rest := s[idx+2:]
		nameEnd := 0
		for nameEnd < len(rest) {
			c := rest[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd > 0 && nameEnd < len(rest) && rest[nameEnd] == '(' {
			return rest[:nameEnd]
		}
	}
	// <tool_code>\n[whitespace]name( form
	if start := strings.Index(s, "<tool_code>"); start >= 0 {
		body := s[start+len("<tool_code>"):]
		// Skip leading whitespace and any ::
		body = strings.TrimSpace(body)
		body = strings.TrimPrefix(body, "::")
		nameEnd := 0
		for nameEnd < len(body) {
			c := body[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd > 0 && nameEnd < len(body) && body[nameEnd] == '(' {
			return body[:nameEnd]
		}
	}
	return ""
}

// stripFakeToolCodeBlocks removes <tool_code>...</tool_code> blocks,
// ```tool_code fenced blocks, and ::name(...):: cascades from text
// content so the user-visible message doesn't show the fake
// invocation alongside the narrative that introduced it.
func stripFakeToolCodeBlocks(s string) string {
	// <tool_code>...</tool_code>
	for {
		start := strings.Index(s, "<tool_code>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</tool_code>")
		if end < 0 || end < start {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</tool_code>"):]
	}
	// ```tool_code\n...\n```
	for {
		start := strings.Index(s, "```tool_code")
		if start < 0 {
			break
		}
		end := strings.Index(s[start+len("```tool_code"):], "```")
		if end < 0 {
			s = s[:start]
			break
		}
		closeAt := start + len("```tool_code") + end + len("```")
		s = s[:start] + s[closeAt:]
	}
	// ::name(...)::  — best-effort: drop from "::" through the matching "::".
	for {
		start := strings.Index(s, "::")
		if start < 0 {
			break
		}
		// Confirm this is the cascade-call shape (name follows, then "(").
		rest := s[start+2:]
		nameEnd := 0
		for nameEnd < len(rest) {
			c := rest[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd == 0 || nameEnd >= len(rest) || rest[nameEnd] != '(' {
			break // not a fake call — leave the rest alone
		}
		end := strings.Index(rest, "::")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + rest[end+2:]
	}
	return strings.TrimSpace(s)
}

// containsActionPromise reports whether content includes an explicit
// promise of action — phrases the LLM emits when it intends to call a
// tool but doesn't actually emit the call. Detection is conservative:
// only matches forms that almost always indicate "I'm about to do
// something" and never natural conversational closes ("let me know if
// you have other questions" wouldn't trigger because of the "know if").
//
// Scope: matches the trailing portion of content (last ~200 chars)
// since the action-promise is usually the closing sentence, and a
// promise-shaped phrase mid-text followed by a real conclusion is
// usually fine. Case-insensitive.
func containsActionPromise(content string) bool {
	c := strings.ToLower(strings.TrimSpace(content))
	if c == "" {
		return false
	}
	// Look at trailing 200 chars; longer content with a closing
	// promise is the typical failure shape.
	if len(c) > 200 {
		c = c[len(c)-200:]
	}
	// Phrase set chosen to match "stated intent to act" and avoid
	// natural conversational closes. Each must be followed by some
	// hint of an upcoming action ("try", "pull", "check", etc.) or
	// a temporal hold ("moment", "second", "sec").
	phrases := []string{
		"let me try",
		"let me figure",
		"let me pull",
		"let me look up",
		"let me check",
		"let me see if",
		"let me get",
		"let me find",
		"let me grab",
		"let me look",
		"let me actually",
		"let me first",
		"i'll figure",
		"i'll pull",
		"i'll check",
		"i'll look",
		"i'll try",
		"i'll grab",
		"i'll fetch",
		"one moment",
		"one sec",
		"give me a moment",
		"give me a sec",
		"hold on",
		"stand by",
		"hang on",
		"hold tight",
		"bear with me",
		"working on it",
		"on it",
	}
	for _, p := range phrases {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

// DynamicThinkBudget scales the model's thinking budget based on the
// input token count. Short queries stay cheap; large/dense inputs get
// enough headroom to integrate the context without truncation.
//
// Formula:
//   - Below 4K input tokens: base (8K) — small queries don't need much
//   - Above 4K: linear growth, +1024 budget tokens per 1K input above
//   - Capped at 32K — past that, more thinking rarely helps Qwen3
//
// Used by the agent loop on prior-round input tokens, and exposed for
// one-shot callers (consensus synthesis, judge calls, etc.) that want
// the same scaling without rebuilding the formula. Standalone callers
// that don't have a token count can use EstimateTokens(text) on the
// raw input string — close enough for budget sizing.
//
// Tunable knobs are intentionally hardcoded; the scaling is universal
// enough across reasoning models that exposing them as config would
// be premature optimization.
func DynamicThinkBudget(inputTokens int) int {
	const (
		base      = 8192
		threshold = 4096
		ceiling   = 12288
		// scaleNum/scaleDen = 256/1024 = 0.25 budget tokens per 1 input
		// token above threshold. Qwen's own best-practice card
		// (Qwen3-*-Thinking-2507) is explicit: "To avoid overly verbose
		// reasoning, we set the thinking budget to 8,192 tokens" — a FLAT
		// number, not input-scaled. The model fills whatever budget it's
		// handed, so input-size scaling made trivial tool calls sitting in
		// a long history (e.g. a 21K-token agent turn) deliberate for
		// ~16K tokens / 2+ minutes. We keep a gentle scale for genuinely
		// large synthesis turns but anchor on 8192 and cap at 12288 (1.5×)
		// rather than 32768 — note 32768 is Qwen's recommended TOTAL output
		// length (thinking + answer), not the thinking budget. Callers that
		// genuinely need deeper reasoning pass WithThinkBudget(N) explicitly.
		// At 21K input the budget now lands ~12K instead of ~16.8K; at 26K,
		// ~12.3K (capped) instead of ~19K.
		scaleNum = 256
		scaleDen = 1024
	)
	var budget int
	if inputTokens <= threshold {
		budget = base
	} else {
		extra := (inputTokens - threshold) * scaleNum / scaleDen
		budget = base + extra
		if budget > ceiling {
			budget = ceiling
		}
	}
	Debug("[think_budget] input=%d tokens → budget=%d tokens (base=%d, threshold=%d, ceiling=%d)",
		inputTokens, budget, base, threshold, ceiling)
	return budget
}

// EstimateTokens approximates the token count of a string using the
// standard ~4-chars-per-token heuristic. Accurate enough for sizing
// thinking budgets where exact counts don't matter — DynamicThinkBudget
// caps at 32K and the formula's slope is gradual, so being off by 20%
// on the input estimate moves the resulting budget by <1K tokens.
// For per-billing accuracy, use a real tokenizer.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return len(text) / 4
}

// EstimateMessagesTokens sums the estimated token count across a slice
// of Messages. Convenience wrapper for callers sizing think budgets
// from a chat history. Counts content only — message-role overhead is
// negligible at the scales DynamicThinkBudget cares about.
func EstimateMessagesTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content) / 4
	}
	return total
}

// compactHistory bounds the agent loop's working history so a long
// multi-round session can't grow past the model's context window and
// trigger server-side context-shift (which silently drops the system
// prompt and degrades the model). When the estimated round size crosses
// the budget, it elides the BODIES of OLD tool results
// (Message.ToolResults[].Content) oldest-first — keeping the most recent
// few result messages full plus ALL conversational text — until back
// under budget. Mutates msgs in place: once a body is elided it stays
// elided on later rounds (cumulative). The model keeps the conversational
// structure (it knows the tool ran) but not the stale body, and can
// re-run the tool if it needs the data again. No-op when contextSize<=0
// or already under budget. See project_long_context_management.
//
// budget accounts for what ELSE occupies the window each round — the
// separately-sent system prompt + tool schemas + the thinking/response
// the model still has to generate — so history is trimmed to leave that
// headroom, not to 100% of the window.
// force=true is the LLM-driven / on-demand path (the compact_context
// tool): shed verbose history NOW regardless of budget, keeping only the
// newest result — for when the model knows it's done with a long tool
// output (e.g. a smoke-test report it has finished judging). force also
// works when contextSize is unset.
func compactHistory(msgs []Message, systemPrompt string, contextSize int, force bool) {
	if contextSize <= 0 && !force {
		return
	}
	const (
		// Headroom (tokens) reserved for tool schemas + a near-max thinking
		// budget + the response — what the round needs ON TOP of history.
		genReserve = 34000
		// Don't bother eliding small bodies.
		elideMinBytes = 400
	)
	// Steady-state cap on history relative to the window. Pulled from
	// operator tuning per compaction call so a live admin change tunes
	// without restart. Default 50% — a 200K context targets ~100K of
	// history. Prefill latency, llama.cpp's cache-hit ratio, and
	// Anthropic's prompt cache all degrade sharply when the prefix
	// grows turn-over-turn, so we deliberately don't fill the window.
	// The model can still re-run any tool whose body was elided.
	historyFractionCap := float64(GetAgentLoopTuning().HistoryBudgetPercent) / 100.0
	// Newest tool-result messages always kept full (the model needs recent
	// results to act on this round). On a forced compaction the model has
	// explicitly said it's done with the verbose history, so keep only 1.
	keepRecentToolMsgs := 4
	var budget int
	if force {
		keepRecentToolMsgs = 1
		budget = 0 // elide every elidable old body
	} else {
		budget = contextSize - EstimateTokens(systemPrompt) - genReserve
		// Steady-state cap layered ON TOP of the window-minus-sysprompt
		// budget — whichever is tighter. Long sessions with a small
		// sysprompt would otherwise let history fill 75-85% of the
		// window; this pulls it down to ~50% so each round stays cheap
		// to prefill and the model has room to think+reply.
		if fractionBudget := int(float64(contextSize) * historyFractionCap); fractionBudget < budget {
			budget = fractionBudget
		}
		if floor := contextSize / 4; budget < floor {
			budget = floor // never starve history below 25% of the window
		}
	}
	msgTokens := func(m Message) int {
		n := len(m.Content) / 4
		for _, tr := range m.ToolResults {
			n += len(tr.Content) / 4
		}
		return n
	}
	total := 0
	for i := range msgs {
		total += msgTokens(msgs[i])
	}
	// Per-round breadcrumb — fires every compaction call so a long
	// session's history trajectory is visible under --debug without
	// waiting for an elision to fire the Log line below.
	Debug("[agent_loop] compaction check: history ~%d tokens, budget %d, window %d (msgs=%d)", total, budget, contextSize, len(msgs))
	if total <= budget {
		return
	}
	origTotal := total
	var trIdx []int
	for i := range msgs {
		if len(msgs[i].ToolResults) > 0 {
			trIdx = append(trIdx, i)
		}
	}
	if len(trIdx) <= keepRecentToolMsgs {
		return // nothing old enough to safely elide
	}
	elided := 0
	for _, i := range trIdx[:len(trIdx)-keepRecentToolMsgs] {
		if total <= budget {
			break
		}
		for j := range msgs[i].ToolResults {
			body := msgs[i].ToolResults[j].Content
			if len(body) <= elideMinBytes {
				continue
			}
			marker := fmt.Sprintf("[earlier tool result elided to fit context — was %d bytes; re-run the tool if you still need it]", len(body))
			total -= len(body)/4 - len(marker)/4
			msgs[i].ToolResults[j].Content = marker
			elided++
		}
	}
	if elided > 0 {
		mode := "budget"
		if force {
			mode = "compact_context"
		}
		// Log (not Debug): compaction firing is infrequent (only over
		// budget or when the LLM asks) and notable — surface it so long-
		// session context management is visible without --debug.
		Log("[agent_loop] compaction (%s): elided %d old tool-result body(ies), est %d→%d tokens (budget=%d, window=%d)", mode, elided, origTotal, total, budget, contextSize)
	}
}

// nearestToolName returns the registered tool whose name shares the
// longest common substring (by simple bigram overlap) with attempted.
// Returns empty if no tool overlaps meaningfully — used for the "did
// you mean foo?" hint when the LLM tried a non-existent name.
func nearestToolName(attempted string, handlers map[string]ToolHandlerFunc) string {
	if attempted == "" || len(handlers) == 0 {
		return ""
	}
	att := strings.ToLower(attempted)
	bestName := ""
	bestScore := 0
	for name := range handlers {
		score := bigramOverlap(att, strings.ToLower(name))
		if score > bestScore {
			bestScore = score
			bestName = name
		}
	}
	// Threshold: require at least 2 shared bigrams to suggest, else
	// the suggestion is probably noise.
	if bestScore < 2 {
		return ""
	}
	return bestName
}

// bigramOverlap counts how many character-bigrams from a appear in b.
func bigramOverlap(a, b string) int {
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	count := 0
	for i := 0; i < len(a)-1; i++ {
		bg := a[i : i+2]
		if strings.Contains(b, bg) {
			count++
		}
	}
	return count
}

// truncForLog shortens s to n chars for log preview, replacing newlines
// so the line stays one row.
func truncForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// hasRequired reports whether tc.Args contains every key listed in the
// matching tool's Required slice. Tools with no Required restriction
// always pass. Lookup is case-insensitive so an LLM that emits "URL"
// against a tool declaring "url" doesn't silently get dropped here
// (the dispatcher's downstream canonicalization fixes the value-side
// of the same mismatch).
func hasRequired(tc *ToolCall, toolDefs []Tool) bool {
	if tc == nil {
		return false
	}
	for _, td := range toolDefs {
		if td.Name != tc.Name {
			continue
		}
		for _, req := range td.Required {
			v, ok := tc.Args[req]
			if !ok {
				// Case-insensitive fallback so capitalization
				// drift between tool definition and LLM emission
				// doesn't drop a structurally valid call.
				reqLower := strings.ToLower(req)
				for k, val := range tc.Args {
					if strings.ToLower(k) == reqLower {
						v = val
						ok = true
						break
					}
				}
				if !ok {
					return false
				}
			}
			// Treat empty string / nil as missing — the tool's
			// validation would reject those anyway, and we want the
			// loop to recover, not waste a round.
			if v == nil {
				return false
			}
			if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
				return false
			}
		}
		return true
	}
	// Unknown tool name (handler exists but no def — shouldn't happen
	// in practice). Permit, since we can't validate.
	return true
}

// parseJSONToolCall extracts a tool call from a JSON object in the text.
func parseJSONToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	// Find the first '{' and last '}' to extract a JSON object.
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil
	}
	jsonStr := content[start : end+1]

	var raw map[string]interface{}
	if json.Unmarshal([]byte(jsonStr), &raw) != nil {
		return nil
	}

	name, _ := raw["name"].(string)
	if name == "" {
		return nil
	}

	// Only treat it as a tool call if the name matches a registered handler.
	if _, ok := handlers[name]; !ok {
		return nil
	}

	// Extract arguments from "parameters" or "arguments".
	args := make(map[string]any)
	var params map[string]interface{}
	if p, ok := raw["parameters"].(map[string]interface{}); ok {
		params = p
	} else if a, ok := raw["arguments"].(map[string]interface{}); ok {
		params = a
	}
	for k, v := range params {
		args[k] = v
	}

	return &ToolCall{
		ID:   fmt.Sprintf("text_%s", UUIDv4()),
		Name: name,
		Args: args,
	}
}

// parseCallArgs parses a narrated call's parenthesized argument list — the
// name(key="value", key2=…) or name({…json…}) that follows a tool name when a
// model writes a call as prose instead of emitting a structured tool call. It
// returns nil when `after` has no parenthesized list. Quoted values may contain
// commas (they don't split); bare true/false/number values are coerced.
func parseCallArgs(after string) map[string]any {
	after = strings.TrimSpace(after)
	if !strings.HasPrefix(after, "(") {
		return nil
	}
	// Find the matching close paren, respecting quoted strings.
	depth := 0
	var quote rune
	end := -1
	for i, r := range after {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = r
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	inner := strings.TrimSpace(after[1:end])
	// JSON-object arg form: name({"to": "...", "text": "..."}).
	if strings.HasPrefix(inner, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(inner), &m) == nil && len(m) > 0 {
			return m
		}
	}
	// key=value list, splitting on top-level commas only.
	args := make(map[string]any)
	for _, pair := range splitTopLevel(inner, ',') {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		if key == "" {
			continue
		}
		args[key] = coerceArgValue(pair[eq+1:])
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

// splitTopLevel splits s on sep, ignoring separators inside single/double
// quotes (so a quoted value containing a comma stays intact).
func splitTopLevel(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		if quote != 0 {
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch {
		case r == '"' || r == '\'':
			quote = r
			cur.WriteRune(r)
		case r == sep:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// coerceArgValue strips matching quotes from a narrated arg value and coerces
// bare true/false/number literals; everything else stays a string.
func coerceArgValue(v string) any {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	switch strings.ToLower(v) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return n
	}
	return v
}

// parseNaturalToolCall scans text for a known tool name and extracts any
// arguments that follow it. This handles thinking models that reason about
// which tool to call but stop before emitting a structured call.
func parseNaturalToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	lower := strings.ToLower(content)

	// Find the best (longest) matching tool name in the text.
	var bestName string
	var bestPos int = -1
	for name := range handlers {
		pos := strings.LastIndex(lower, strings.ToLower(name))
		if pos >= 0 && (bestPos < 0 || len(name) > len(bestName)) {
			bestName = name
			bestPos = pos
		}
	}

	if bestName == "" {
		return nil
	}

	// Try to extract args after the tool name mention.
	args := make(map[string]any)
	after := strings.TrimSpace(content[bestPos+len(bestName):])

	// Function-call narration: name(key="value", ...) or name({...json...}).
	// This is the most common shape when a model WRITES a call as text instead
	// of emitting a structured tool call (the observed message_contact failure).
	// Extracting the named args rescues it into a real call — which still runs
	// through the normal confirm/approval gate, so a consequential one (texting a
	// person) isn't silently auto-executed. A well-formed name(args) is a strong
	// intent signal, unlike a bare mention, so the false-positive risk is low.
	if callArgs := parseCallArgs(after); len(callArgs) > 0 {
		return &ToolCall{ID: fmt.Sprintf("text_%s", UUIDv4()), Name: bestName, Args: callArgs}
	}

	// Look for --flag patterns (e.g. "--to user@example.com").
	var flag_args []string
	for _, part := range strings.Fields(after) {
		if strings.HasPrefix(part, "--") {
			flag_args = append(flag_args, part)
		} else if len(flag_args) > 0 {
			// Attach value to the previous flag.
			flag_args = append(flag_args, part)
		}
	}
	if len(flag_args) > 0 {
		args["args"] = strings.Join(flag_args, " ")
	}

	// Guard: don't fire on bare tool-name mentions. If no args were
	// extractable from the prose, the model was almost certainly
	// just REASONING about the tool ("I should call web_search…")
	// rather than emitting an actual call. Firing here produces a
	// missing-required-arg failure that forces a wasted round.
	// Returning nil lets the loop terminate cleanly when the model
	// already finished its turn.
	if len(args) == 0 {
		Debug("[agent_loop] skipping natural-language tool mention %q — no args extractable, treating as reasoning prose", bestName)
		return nil
	}

	Debug("[agent_loop] extracted tool call from reasoning: %s", bestName)

	return &ToolCall{
		ID:   fmt.Sprintf("text_%s", UUIDv4()),
		Name: bestName,
		Args: args,
	}
}

// mentionedNoArgTool returns the name of a known ZERO-required-argument tool
// that appears as a standalone token in content, or "" if none. It's the
// re-prompt trigger for no-arg tools that parseNaturalToolCall can't rescue: a
// model that names such a tool in prose without emitting a structured call would
// otherwise have it silently dropped. Deliberately conservative to avoid the
// false positives that got actionPromiseCorrection disabled:
//   - only tools with NO required args (a bare mention could be a valid call),
//   - only snake_case names (an underscore) — single common words like a
//     hypothetical "help" tool would false-match ordinary prose,
//   - token-bounded match so "image" doesn't fire inside "images".
func mentionedNoArgTool(content string, handlers map[string]ToolHandlerFunc, toolDefs []Tool) string {
	lower := strings.ToLower(content)
	best := ""
	for _, td := range toolDefs {
		// "No-arg" means literally zero parameters — the only case where
		// there is nothing to extract from prose and the mention can't be a
		// real call. Keying off Required instead would match parameterized
		// tools that merely mark everything optional (e.g. tool_def, which
		// validates in-handler, not via the schema), so a purely DESCRIPTIVE
		// mention ("I wrap APIs via tool_def") would trip the correction.
		if td.Name == "" || len(td.Parameters) != 0 || !strings.Contains(td.Name, "_") {
			continue
		}
		if _, ok := handlers[td.Name]; !ok {
			continue
		}
		if mentionsToken(lower, strings.ToLower(td.Name)) && len(td.Name) > len(best) {
			best = td.Name
		}
	}
	return best
}

// mentionsToken reports whether needle occurs in haystack bounded by
// non-identifier characters (so a tool name matches only as a standalone token,
// not inside a longer word). Both arguments must already be lowercase.
func mentionsToken(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(needle)
		beforeOK := i == 0 || !isIdentByte(haystack[i-1])
		afterOK := end >= len(haystack) || !isIdentByte(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		from = i + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z')
}

// BuildToolPrompt generates a text description of available tools for
// injection into the system prompt when PromptTools mode is enabled.
func BuildToolPrompt(tools []AgentToolDef) string {
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\nYou have access to the following tools:\n\n")
	for _, td := range tools {
		b.WriteString(fmt.Sprintf("### %s\n%s\n", td.Tool.Name, td.Tool.Description))
		if len(td.Tool.Parameters) > 0 {
			b.WriteString("Parameters:\n")
			for name, p := range td.Tool.Parameters {
				req := ""
				for _, r := range td.Tool.Required {
					if r == name {
						req = " (required)"
						break
					}
				}
				b.WriteString(fmt.Sprintf("  - %s (%s%s): %s\n", name, p.Type, req, p.Description))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(`To use a tool, respond with EXACTLY this format on its own line:
<tool_call>
{"name": "tool_name", "arguments": {"param": "value"}}
</tool_call>

After each tool result, decide whether you have enough information to fully answer the question. If not, call another tool. Only reply once you can satisfactorily answer the request.
If you do not need a tool, respond normally without any <tool_call> tags.
Only call ONE tool at a time. Wait for the result before calling another.
`)
	return b.String()
}

// ParsePromptToolCall extracts a tool call from <tool_call> tags in the
// LLM's text response. Returns the parsed ToolCall and the surrounding
// text (before the tag) so the caller can preserve any preamble.
func ParsePromptToolCall(content string, handlers map[string]ToolHandlerFunc) (*ToolCall, string) {
	start := strings.Index(content, "<tool_call>")
	if start < 0 {
		return nil, content
	}
	end := strings.Index(content, "</tool_call>")
	if end < 0 || end <= start {
		return nil, content
	}

	preamble := strings.TrimSpace(content[:start])
	body := strings.TrimSpace(content[start+len("<tool_call>") : end])

	// Try the JSON form we instruct first: {"name": "...", "arguments": {...}}.
	// Fall back to the XML-style form Llama-3/Qwen/Hermes models often
	// emit even when prompted otherwise: <function=name><parameter=foo>value</parameter></function>.
	// Different surface forms, same intent — accept both rather than
	// drop the call and burn a round.
	var name string
	args := make(map[string]any)

	var raw map[string]interface{}
	if json.Unmarshal([]byte(body), &raw) == nil {
		name, _ = raw["name"].(string)
		if a, ok := raw["arguments"].(map[string]interface{}); ok {
			for k, v := range a {
				args[k] = v
			}
		}
	} else {
		// Fallback: parse <function=NAME>...<parameter=KEY>VALUE</parameter>...</function>.
		name, args = parseFunctionTagToolCall(body)
	}

	if name == "" {
		return nil, content
	}
	if _, ok := handlers[name]; !ok {
		return nil, content
	}

	return &ToolCall{
		ID:   fmt.Sprintf("prompt_%s", UUIDv4()),
		Name: name,
		Args: args,
	}, preamble
}

// parseFunctionTagToolCall handles the XML-style tool-call body that
// Llama-3 / Qwen / Hermes-style instruction tunes often emit instead
// of the JSON form we instruct. Format:
//
//	<function=tool_name>
//	<parameter=arg1>
//	value1
//	</parameter>
//	<parameter=arg2>
//	value2
//	</parameter>
//	</function>
//
// Returns the function name and parsed args map. Empty name means
// the body wasn't recognizable in this format either; caller treats
// as "drop the call" the same as a JSON parse failure.
func parseFunctionTagToolCall(body string) (string, map[string]any) {
	args := map[string]any{}
	// Find <function=...> or <function=...
	const fnPrefix = "<function="
	si := strings.Index(body, fnPrefix)
	if si < 0 {
		return "", nil
	}
	rest := body[si+len(fnPrefix):]
	// Function name runs until '>'.
	gt := strings.IndexByte(rest, '>')
	if gt < 0 {
		return "", nil
	}
	name := strings.TrimSpace(rest[:gt])
	rest = rest[gt+1:]

	// Walk through every <parameter=KEY>VALUE</parameter> chunk.
	const pPrefix = "<parameter="
	const pClose = "</parameter>"
	for {
		pi := strings.Index(rest, pPrefix)
		if pi < 0 {
			break
		}
		rest = rest[pi+len(pPrefix):]
		gt := strings.IndexByte(rest, '>')
		if gt < 0 {
			break
		}
		paramName := strings.TrimSpace(rest[:gt])
		rest = rest[gt+1:]
		closeIdx := strings.Index(rest, pClose)
		if closeIdx < 0 {
			break
		}
		// Strip leading/trailing whitespace + newlines around the value
		// so a multi-line shell command doesn't keep its surrounding
		// blank lines.
		val := strings.TrimSpace(rest[:closeIdx])
		args[paramName] = val
		rest = rest[closeIdx+len(pClose):]
	}
	return name, args
}
