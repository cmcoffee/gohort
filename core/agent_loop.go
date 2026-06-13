package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cmcoffee/snugforge/nfo"
)

// ToolHandlerFunc is a function that executes a tool call and returns its output.
type ToolHandlerFunc func(args map[string]any) (string, error)

// ErrToolDenied is returned when the user denies a tool call.
var ErrToolDenied = fmt.Errorf("tool call denied by user")

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
	var lines []string
	for k, v := range args {
		display := stringify(v)
		if len(display) > 200 {
			display = display[:200] + "..."
		}
		lines = append(lines, fmt.Sprintf("%s: %s", k, display))
	}
	return strings.Join(lines, "\n")
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
		for _, td := range active {
			toolDefs = append(toolDefs, td.Tool)
			handlers[td.Tool.Name] = td.Handler
			if td.NeedsConfirm {
				needsConfirm[td.Tool.Name] = true
			}
			if td.SingleFirePerBatch {
				singleFireTools[td.Tool.Name] = true
			}
		}
	}
	rebuildToolMaps(tools)

	history := make([]Message, len(messages))
	copy(history, messages)

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
		// Capability-first — tool SELECTION, distinct from Grounding (which
		// governs specifics once you have results). The failure mode: the
		// model answers a recency-sensitive or job-specific question straight
		// from training when a tool or agent for it is sitting in the catalog
		// (e.g. reciting "the news" from priors instead of searching). The
		// tool-vs-agent choice is a SIZING decision (how big is the job), kept
		// separate from the trust decision (where the answer comes from) so
		// this doesn't push the model away from delegating real multi-step
		// work. Written without em-dashes so it doesn't model the tic.
		systemPrompt += "\n\n[Capability-first: when a tool or an agent can do the job, or get fresher information than you hold (current events, news, prices, status, anything that may have changed since your training, or anything the user expects to be up to date), use it instead of answering from training or memory. Match the capability to the job: a direct tool when one lookup or action answers it; an agent when the job needs multiple steps, decomposition, or specialized work a single tool cannot cover. Do not substitute a quick tool for an agent the job needs, nor spin up an agent for what one tool answers. Treat prior knowledge as a fallback for gaps no tool or agent can fill, never as a substitute for a capability that exists for the job. If you fall back to prior knowledge, say so and offer to verify.]"
		systemPrompt += "\n\n[Grounding: state a precise specific — a number, a name, a citation or reference (statute, case, version, ID), a date, a figure, a dosage, a direct quote — ONLY when it appears in a tool result or in material the user gave you THIS turn. Never from memory, even when you're confident: a plausible-looking specific you can't point to is worse than not having one. This holds in CASUAL conversation as much as in formal answers — you may state the general shape of the answer, but do NOT attach a specific identifier you can't source right now; if you can't verify the exact one, say you're not certain rather than guessing. If a tool you relied on fails, errors, times out, or returns empty, treat the data as missing — never quietly supply it from memory. And if the user CORRECTS a specific you gave, do NOT swap in another from memory or invent a rationale for the mistake — admit you're not certain and offer to look it up. A confident wrong specific, then a confident second wrong one, is the failure to avoid.]"
		// Contradiction discipline — the sibling of Grounding aimed the OPPOSITE
		// direction: not the model's own volunteered specifics, but the model
		// DISPUTING a fact the user stated or assumed. The failure mode is a
		// confident "well, actually that's wrong" sourced from stale training,
		// which is worse than the user's claim when the priors are out of date.
		// Scoped to CONTRADICTING the user (not to general answers) so it does
		// not add hedging to the decisive-language posture elsewhere. Written
		// without em-dashes (house style).
		systemPrompt += "\n\n[Disagreeing with the user: your training is not sufficient grounds to tell the user they are wrong. When they state or assume a fact and you think it is mistaken, treat your own prior knowledge as possibly stale or incomplete. If a tool can check it, verify FIRST, then correct with the source in hand. If nothing can verify it (no tool fits, the tool fails or returns empty, or you are offline), do NOT assert they are wrong from memory: say you are not certain and offer to check, or ask a clarifying question. This is about EMPIRICAL claims (dates, numbers, who or what or when, current state, how something works, what is true now). Reasoning, logic, math you can show step by step, and the user's own stated preferences you can still engage with directly and decisively. Do not manufacture a confident contradiction from priors; ground the disagreement or hold it as uncertain until you can.]"
		systemPrompt += "\n\n[Numbers: when you state a figure, price, count, or measurement from a source, reproduce it exactly as written, keep its unit or currency attached, and keep it bound to the specific thing it describes (which item, which date, which place) so you never swap two values that appeared in the same source. Do not perform multi-step arithmetic, percentages, or unit/currency conversion in your head and present the result as fact: if a calculation matters, show the steps so it can be checked, or use a tool. If two sources disagree on a number, say so rather than silently picking one. Prices and other time-sensitive figures go stale: when you quote a price, make sure it is current, note when it was observed if the source says, and never present an old or cached figure as today's price — if you cannot confirm it is current, say so or re-check rather than stating it as fact.]"
	}
	// Output style — universal (every reply, with or without tools).
	// Suppresses persistent LLM lexical/punctuation tics the user flagged.
	// The rule itself is written WITHOUT em-dashes so it doesn't model the
	// behavior it forbids.
	systemPrompt += "\n\n[Style: (1) Stop reaching for the word \"classic\"; you lean on it as filler. Drop it unless it's literally accurate (a \"classic car\", a named \"classic\" edition), never as a generic intensifier for something ordinary. (2) Do NOT use em-dashes (the \"—\" character, U+2014) at all. Where you'd reach for one, use a comma, parentheses, a colon, or two sentences instead.]"
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
		if streamHandler != nil {
			resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, opts...)
		} else {
			// NoLead redirects all routing to worker — no escalation.
			useLead := cfg.Tier == LEAD && !T.NoLead
			if cfg.RouteKey != "" && !T.NoLead {
				useLead = RouteToLead(cfg.RouteKey)
			}
			callFn := T.WorkerChat
			if useLead {
				callFn = T.LeadChat
			}
			// Empty/timeout/empty-error retry happens inside retryLLM
			// (core/llm.go) — every caller gets it for free, including
			// direct WorkerChat/LeadChat and chat-handler ChatStream.
			resp, err = callFn(ctx, history, opts...)
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
				resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, opts...)
			} else {
				useLead := cfg.Tier == LEAD && !T.NoLead
				if cfg.RouteKey != "" && !T.NoLead {
					useLead = RouteToLead(cfg.RouteKey)
				}
				callFn := T.WorkerChat
				if useLead {
					callFn = T.LeadChat
				}
				resp, err = callFn(ctx, history, opts...)
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

		Debug("[agent_loop] round %d: content=%d chars, reasoning=%d chars, tool_calls=%d", round, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))
		// BREADCRUMB: LLM returned. Pair with the "→ LLM call"
		// breadcrumb above to detect a wedged provider call.
		Log("[agent_loop] round %d: ← LLM returned (content=%d, tools=%d)", round, len(resp.Content), len(resp.ToolCalls))

		// DIAGNOSTIC: collapse-ish round — the model wrote a large reasoning
		// block but little visible content and called no tool. The existing
		// reasoning-collapse re-prompt below only triggers at <30 chars, so a
		// near-miss (e.g. 35 chars of content over 4k tokens of reasoning) is
		// returned as the reply with the reasoning silently dropped. Dump the
		// reasoning here so we can confirm whether the actual answer was buried
		// in the thinking channel. Thinking models put the conclusion at the
		// END, so log the tail in full rather than truncating it off.
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
			output, toolErr := handlers[tc.Name](tc.Args)
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

			// Reasoning-collapse correction: Qwen-style models with
			// thinking enabled sometimes burn the entire budget on
			// reasoning and emit ~no visible content, while reporting
			// finish=stop. From the user's view: black hole — sent a
			// message, got nothing back. Detect: substantial reasoning
			// (>200 chars), trivially-short content (<30 chars after
			// trim), and no tool calls. Inject a corrective and retry
			// so the next round either produces text or calls a tool.
			// Budget-gated via promiseCorrectionsTotal so it can't loop.
			trimmedContent := strings.TrimSpace(resp.Content)
			if promiseCorrectionsTotal < maxPromiseCorrections && round < maxRounds &&
				len(trimmedContent) < 30 && len(resp.Reasoning) > 200 {
				Debug("[agent_loop] reasoning-collapse detected (reasoning=%d chars, content=%d chars), re-prompting: correction %d/%d", len(resp.Reasoning), len(trimmedContent), promiseCorrectionsTotal+1, maxPromiseCorrections)
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

			work = append(work, toolWork{index: i, tc: tc, handler: handler})
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
			output, err := w.handler(w.tc.Args)
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
			for _, w := range work {
				wg.Add(1)
				go func(w toolWork) {
					defer wg.Done()
					output, err := w.handler(w.tc.Args)
					if err != nil {
						debugToolErr(w.tc.Name, err)
						results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
						atomic.AddInt32(&errCount, 1)
					} else {
						debugResult(w.tc.Name, output)
						results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
					}
				}(w)
			}
			wg.Wait()
			toolErrors += int(atomic.LoadInt32(&errCount))
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
	if lastResp != nil && strings.TrimSpace(lastResp.Content) == "" && T.LLM != nil {
		Debug("[agent_loop] empty after lookback rescue — issuing a forced-final-answer call with no tools")
		wrapHistory := append([]Message{}, history...)
		wrapHistory = append(wrapHistory, Message{
			Role:    "user",
			Content: "Your previous response had no content for the user. Stop calling tools. Produce a final answer NOW from whatever you've gathered so far — even if incomplete, summarize what you found and what you tried. The user is waiting and seeing nothing. Just text, no tool calls.",
		})
		// No-tools, no-think final call so the model has nothing to
		// chase — must produce text. Inherit RouteKey for telemetry.
		var wrapOpts []ChatOption
		wrapOpts = append(wrapOpts, WithSystemPrompt(systemPrompt))
		f := false
		wrapOpts = append(wrapOpts, WithThink(f))
		if cfg.RouteKey != "" {
			wrapOpts = append(wrapOpts, WithRouteKey(cfg.RouteKey))
		}
		if forced, err := T.LLM.Chat(ctx, wrapHistory, wrapOpts...); err == nil && forced != nil && strings.TrimSpace(forced.Content) != "" {
			lastResp = forced
			Debug("[agent_loop] forced-final-answer rescue produced %d chars", len(forced.Content))
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
