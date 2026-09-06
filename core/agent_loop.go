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

func (T *AppCore) runAgentLoopInner(ctx context.Context, messages []Message, cfg AgentLoopConfig) (*Response, []Message, error) {
	if T.LLM == nil {
		return nil, messages, fmt.Errorf("LLM is not configured")
	}

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
	turnStarted := time.Now()
	var llmWall time.Duration
	var llmCalls int
	defer func() {
		total := time.Since(turnStarted)
		own := total - llmWall
		if own < 0 {
			own = 0
		}
		pct := 0
		if total > 0 {
			pct = int(own * 100 / total)
		}
		Log("[agent_loop] turn time: %s total = %s in %d LLM call(s) + %s gohort (%d%%)",
			total.Round(time.Millisecond), llmWall.Round(time.Millisecond), llmCalls,
			own.Round(time.Millisecond), pct)
	}()

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
	// One hold per turn, for a turn driven by somebody who is not the principal.
	premise := newPremiseGate(cfg.LiveClaimSpeaker, LatestUserContent(messages), cfg.LiveClaimTrusted)
	needsConfirm := make(map[string]bool)
	// Which tools DO something, for the unverified-premise gate below. Caps are
	// the framework's own annotation, not a name list, so a tool added later is
	// covered by declaring what it is.
	writeTools := make(map[string]bool)
	singleFireTools := make(map[string]bool)
	serialFireTools := make(map[string]bool)
	batchLaneFns := make(map[string]func(map[string]any) string)
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
		for k := range batchLaneFns {
			delete(batchLaneFns, k)
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
			// A declared ConfirmPrompt implies NeedsConfirm. Without this the
			// two flags are a trap: a tool author writes the sentence the user
			// is meant to read, forgets the boolean, and the loop never calls
			// Confirm at all — so the tool runs unasked and the only evidence
			// is a prompt string nothing ever renders.
			if td.NeedsConfirm || td.Confirmation.asks() {
				needsConfirm[td.Tool.Name] = true
			}
			for _, c := range td.Tool.Caps {
				if c == CapWrite || c == CapExecute {
					writeTools[td.Tool.Name] = true
					break
				}
			}
			// Serial-fire takes precedence: a serial tool must NOT be added to
			// the single-fire set, or the enforcement pass would drop its
			// excess calls before the executor gets to run them in order. A
			// lane function supersedes both for the same reason — its calls
			// all run, and the lanes decide which of them run together.
			if td.BatchLane != nil {
				batchLaneFns[td.Tool.Name] = td.BatchLane
			} else if td.SerialFirePerBatch {
				serialFireTools[td.Tool.Name] = true
			} else if td.SingleFirePerBatch {
				singleFireTools[td.Tool.Name] = true
			}
		}
	}
	rebuildToolMaps(tools)

	history := make([]Message, len(messages))
	copy(history, messages)
	// Damp prior-turn failure storms before the model re-reads them: rebuilt
	// tool rounds carry every old error verbatim, and a wall of identical
	// failures is in-context training data for producing more of them. Keeps
	// the first and newest copy of each repeated shape, collapses the middle.
	if n := collapseIncomingFailureStreaks(history); n > 0 {
		Debug("[agent_loop] failure-streak collapse: %d repeated failure result(s) in incoming history collapsed", n)
	}

	// Turn-scoped notes from the app, appended to the newest user turn BEFORE
	// the stamp goes on the front — so the app is handed the user's own words,
	// not a message that opens with a timestamp it has to look past.
	applyTurnNotes(cfg, history)

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
	// Every framework clause below arrives through the prompts registry, which
	// hands back "" for a block an operator has switched off. One conditional
	// append here beats fifteen at the call sites.
	//
	// The key rides along because the digest's question is "which rules were
	// live on THIS turn?", and a gate ("only when the agent has tools") answers
	// that in general while a per-turn list answers it for the turn that
	// actually went wrong.
	var clauseKeys []string
	addClause := func(key, clause string) {
		if clause != "" {
			systemPrompt += "\n\n" + clause
			clauseKeys = append(clauseKeys, key)
		}
	}
	if cfg.PromptTools && len(tools) > 0 {
		systemPrompt += BuildToolPrompt(tools)
		clauseKeys = append(clauseKeys, "framework.tools_directive")
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
		addClause(prompts.AnsweringRoundsKey, prompts.AnsweringRoundsClause())
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
		addClause(prompts.GroundingContractKey, prompts.GroundingContractClause())
		// Capability-first — tool SELECTION, distinct from Grounding (which
		// governs specifics once you have results). The failure mode: the
		// model answers a recency-sensitive or job-specific question straight
		// from training when a tool or agent for it is sitting in the catalog
		// (e.g. reciting "the news" from priors instead of searching). The
		// tool-vs-agent choice is a SIZING decision (how big is the job), kept
		// separate from the trust decision (where the answer comes from) so
		// this doesn't push the model away from delegating real multi-step
		// work. Written without em-dashes so it doesn't model the tic.
		addClause(prompts.CapabilityFirstKey, prompts.CapabilityFirstClause())
		addClause(prompts.GroundingKey, prompts.GroundingClause())
		// Action grounding — the sibling of Grounding aimed at ACTIONS rather than
		// facts. The failure mode (observed live: an agent in a group chat said "I
		// sent a meme" with zero tool calls): the model narrates a completed action
		// it never performed, because its reply text feels like doing the thing.
		// Written without em-dashes (house style).
		addClause(prompts.ActionsKey, prompts.ActionsClause())
		// Stated where [Actions:] is stated, because they are the same mistake
		// from opposite ends: that one stops an agent claiming work it never did,
		// this one stops it withholding work it actually did.
		addClause(prompts.ToolVisibilityKey, prompts.ToolVisibilityClause())
		// Contradiction discipline — the sibling of Grounding aimed the OPPOSITE
		// direction: not the model's own volunteered specifics, but the model
		// DISPUTING a fact the user stated or assumed. The failure mode is a
		// confident "well, actually that's wrong" sourced from stale training,
		// which is worse than the user's claim when the priors are out of date.
		// Scoped to CONTRADICTING the user (not to general answers) so it does
		// not add hedging to the decisive-language posture elsewhere. Written
		// without em-dashes (house style).
		addClause(prompts.DisagreeingKey, prompts.DisagreeingClause())
		addClause(prompts.NumbersKey, prompts.NumbersClause())
		// False-precision prevention — the behavioral half of the same concern
		// the [Numbers] / [Grounding] blocks address: stop the model inventing a
		// percentage / fraction / dollar figure for rhetorical weight. This is
		// prompt-only by design; the mechanical re-prompt gate that used to back
		// it was removed because its verbatim-corpus match couldn't tell a
		// correctly COMPUTED figure ("$120 over MSRP") from a fabricated one and
		// false-flagged the model's own arithmetic.
		addClause(prompts.NoFalsePrecisionKey, prompts.NoFalsePrecisionClause())
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
		for _, td := range tools {
			if n := td.Tool.Name; n == "web_search" || n == "fetch_url" || n == "browse_page" {
				hasWebTool = true
				break
			}
		}
		addClause(prompts.VolatileFactsKey, prompts.VolatileFactsClause(hasWebTool))
	}
	// Global rules — the deployment's own, ahead of everything else this
	// section adds. Injected here and ONLY here: the per-namespace rules panels
	// display them so a person can see the whole set on one screen, and a
	// display is not a second injection.
	addClause(prompts.GlobalRulesKey, prompts.GlobalRulesClause())
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
	addClause(prompts.StyleKey, prompts.StyleClause())
	// Secret handling — universal. Stops any agent from soliciting API
	// credentials in chat (the OPNsense-controller failure mode); auth is
	// injected server-side via Admin > APIs credentials, so the secret never
	// belongs in the conversation or the tool-call logs.
	addClause(prompts.SecretsKey, prompts.SecretsClause())

	// Internal-marker convention: gives the model a sanctioned, always-scrubbed
	// wrapper for internal-only notes AND tells it not to type bare delivery
	// markers into user-facing text (the textutil.StripMetaTags safety net catches both).
	addClause(prompts.InternalMarkersKey, prompts.InternalMarkersClause())
	// Round-budget awareness — let the LLM know how many rounds it has
	// for the whole turn so it can pace itself (vs. exploring as if
	// budget were infinite, then getting truncated). Only emit for
	// sessions with meaningful budgets; short fixed loops (judges,
	// classifiers) don't need the noise.
	if maxRounds >= 10 {
		addClause(prompts.RoundBudgetKey, prompts.RoundBudgetClause(maxRounds))
	}

	var lastResp *Response
	// Per-turn prompt digest: built on the first round, emitted once the first
	// response carries the provider's own token count.
	var digest PromptDigest
	digestBuilt, digestSent := false, false
	prevHadToolCalls := false
	// Rations the loop's silent re-prompts. Per KIND, not one pot — see
	// correctionBudget for why that mattered.
	corrections := newCorrectionBudget()
	guardrailOutputCorrections := 0 // pre_output revise passes used this turn
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
	judgedNarration := map[string]bool{}
	skippedInterimGuard := false // logged once per turn, not once per round

	// emitDiag breadcrumbs a silent correction into the app's session-diag
	// trail (nil-safe). Kept here so every correction guard below records the
	// framework decision it just made — see AgentLoopConfig.OnDiag.
	emitDiag := func(kind, detail string) {
		if cfg.OnDiag != nil {
			cfg.OnDiag(kind, detail)
		}
	}

	// noteUncorrected breadcrumbs a guard that spotted its problem and has run
	// out of budget to do anything about it. Once per kind, and a no-op until
	// then. Without it an exhausted guard is indistinguishable from one that
	// never fired: the turn ships the flaw and the trail says nothing happened.
	noteUncorrected := func(kind, detail string) {
		if corrections.exhausted(kind) {
			Debug("[agent_loop] %s detected again but its correction budget is spent — letting it stand", kind)
			emitDiag(kind+"-uncorrected", detail)
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

	// retractRound discards the current round's streamed bubble on a guardrail
	// block (never persisted/delivered). Falls back to settleRound when the app
	// wired no retract — that's concatenation-safe but still persists the bubble,
	// so only paths that set RetractRound get the leak-proof behavior.
	retractRound := func() {
		if cfg.RetractRound != nil {
			cfg.RetractRound()
			return
		}
		settleRound()
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
	replaceBlockedDraft := func(with string) {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == "assistant" {
				history[i].Content = with
				history[i].Reasoning = ""
				history[i].ToolCalls = nil
				return
			}
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
	lastToolError := ""         // most recent failure text, for the turn judge
	turnToolCalls := []string{} // every tool this turn ran, in order, duplicates kept

	// Repeated-failure loop-guard. Small models fixate: they re-issue the
	// SAME tool call with the SAME args, get the SAME error, and never adapt
	// (e.g. polling inspect_run with an approval id that has no run — 25× until the
	// round budget burns). repeatFail counts consecutive errors per call
	// signature (tool name + args); once a signature crosses repeatFailLimit we
	// stop executing it and feed back a hard STOP directive instead. Signature-
	// scoped (not tool-scoped) so the SAME tool with DIFFERENT args is fine, and
	// a success resets the counter so legitimate polling isn't penalized.
	// Over budget before the first call: refuse the turn rather than start
	// work that will de-escalate on its first round anyway.
	if over, spent := overDailySpend(cfg); over {
		Log("[agent_loop] daily spend cap reached for %q ($%.2f of $%.2f) — turn refused", cfg.BudgetKey, spent, cfg.DailySpendUSD)
		emitDiag("spend-cap", fmt.Sprintf("This agent has spent $%.2f of its $%.2f daily allowance; the turn was not run.", spent, cfg.DailySpendUSD))
		return &Response{Content: fmt.Sprintf(
			"I've reached my spending limit for now — $%.2f of the $%.2f allowed in a 24-hour window — so I didn't run this. It frees up as earlier work ages out, or the owner can raise the limit.",
			spent, cfg.DailySpendUSD)}, messages, nil
	}

	repeatFail := map[string]int{}
	const repeatFailLimit = 3
	// Carry in what this standing work already learned, before history is
	// consulted: a scheduled fire's history has no tool results to learn from.
	loadFailureMemory(cfg.FailureMemoryKey, repeatFail, repeatFailLimit-1)
	defer func() { saveFailureMemory(cfg.FailureMemoryKey, repeatFail) }()
	// How much content makes a clean "stop" finish read as an ANSWER rather
	// than a lead-in to a narrated tool call, for the prose-scan gate below.
	// Under it the scan still runs, so a model that only ever describes its
	// calls keeps working; over it we trust the model that said it was done.
	// The case this comes from was 8366 chars; narrated intents run a few
	// hundred. Wide margin on both sides on purpose.
	const cleanFinishProseFloor = 2000
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
	sentThisTurn := map[string]bool{}
	// shakeoutNextRound: a repeat guard fired last round, so the model is
	// provably stuck re-emitting the same call. The NEXT LLM call gets a
	// one-shot temperature bump (shakeoutTemperature) to jolt it off the
	// fixed point — near-greedy sampling on near-identical context is what
	// makes these orbits stable. One round only, then back to route defaults;
	// zero cost in the steady state (unlike a global penalty sampler, which
	// measured ~23% tok/s on this rig).
	shakeoutNextRound := false
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
	guardrailQuietNextRound := false
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
	const errShapeNudgeAt = 3      // say it plainly, once per shape
	const errShapeDeescalateAt = 6 // stop paying lead rates to keep hitting it
	// toolFailShapes records which failure shapes each TOOL produced this
	// turn, so a later success of that tool can retire its stale failure
	// residue from the context (retireResolvedFailureResults).
	toolFailShapes := map[string]map[string]bool{}
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
	// truncatedLead holds the text of every reply that was cut off and then
	// continued this turn, in order. See joinContinuation for who needs it.
	var truncatedLead strings.Builder

	// Wedge break-out. The loop-guard above blocks a repeated failing call, but a
	// small model may keep RE-EMITTING that identical blocked call every round
	// (often as a TEXT/reasoning tool call), ignoring the STOP directive and
	// making zero progress. Counting rounds that were nothing-but-a-blocked-call,
	// once past this small limit we stop looping and force a clean final answer
	// rather than grinding to the round cap. forceFinal drives the post-loop rescue.
	guardBlockedStreak := 0
	const guardBlockedBreakLimit = 2
	forceFinal := false

	// synthesizedFrom holds the model's own text for a round whose tool
	// call was READ OUT OF that text rather than emitted structurally.
	// If the loop-guard then blocks the synthesized call, the wedge would
	// regenerate an answer we already have — so it returns this instead.
	// Reset each round and cleared the moment a real tool executes, so it
	// can only ever short-circuit a round that did no actual work.
	synthesizedFrom := ""

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
		if round == 1 {
			logPromptFloor(cfg, systemPrompt, history)
		}
		// BREADCRUMB: round-top reached. Mirrors the Debug above at
		// Log level — paired with the "dispatch complete" breadcrumb
		// after tools, the gap between the two pinpoints whether
		// the hang is in iteration restart (Debug fires) or pre-
		// iteration bookkeeping (Debug does NOT fire).
		Log("[agent_loop] round %d: top of iteration", round)
		// Per-round: only a call synthesized THIS round may short-circuit
		// the wedge. A stale value from an earlier round would let a real
		// blocked call return someone else's text.
		synthesizedFrom = ""
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
						frameworkNoticeTag+"Fresh budget window: you have %d rounds for this phase. The framework will nudge you at the halfway mark and again near the cap — pace this phase as if starting clean. (Hard MaxRounds cap is still %d total for the turn.)",
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
					frameworkNoticeTag+"Halfway checkpoint: you're at round %d of %d for this phase. Taking stock is worth a moment — if you're making real progress, keep going; if not, consider switching tools, trying a different angle, or asking the user for clarification before the remaining budget gets spent.",
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
					frameworkNoticeTag+"Budget checkpoint: %d rounds left of a %d-round budget, and %d authorized work item(s) still remain on your list. Finish the current item cleanly with a real result, then move to the next one — do NOT skip the remaining items and do NOT start new exploration outside the list. If you genuinely can't complete an item with the rounds remaining, mark it as such and continue.",
					remaining, maxRounds, pending)
			} else {
				wrapUpMsg = fmt.Sprintf(
					frameworkNoticeTag+"You have %d rounds left of a %d-round budget. Stop exploring and produce a final answer NOW with what you've gathered. If the task isn't complete, summarize what you found, what you tried, and what's still open. Do NOT start new investigations — wind down cleanly.",
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
				msg = frameworkNoticeTag + "[ROUND LIMIT — HARD STOP after this round. Produce your final answer NOW from what you already have. Start no new work; make a tool call only if it is the single step needed to finish, then answer.]"
			} else {
				msg = fmt.Sprintf(frameworkNoticeTag+"[Round limit reached — wrap up and give your final answer. %d round(s) left before a hard stop. Finish in-flight work only; start nothing new.]", left)
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
		// effectiveContextSize, not cfg.ContextSize: if this window has recently
		// refused a prompt, the next one has to be built against what the
		// worker actually granted. Otherwise every turn rebuilds to the claim
		// and rediscovers the wall a round later.
		window := effectiveContextSize(cfg.ContextSize)
		budget := compactHistory(history, systemPrompt, window, forceCompact)
		// One digest per TURN, measured on the prompt of the first round after
		// compaction has run — the prompt that is actually sent. Later rounds
		// grow history with results the model asked for; round 1 is the part a
		// person can act on (a system prompt, a tool catalog, and a thread they
		// can trim). Provider numbers are filled in below, once the response
		// makes them real.
		if !digestBuilt {
			digest = buildPromptDigest(systemPrompt, clauseKeys, tools, history, window, budget)
			digestBuilt = true
		}
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
		// One-shot sampling shake-out after a repeat-guard trip (see the
		// shakeoutNextRound decl). Appended last so it wins over static opts.
		if shakeoutNextRound {
			shakeoutNextRound = false
			opts = append(opts, WithTemperature(shakeoutTemperature))
			Debug("[agent_loop] shake-out round: one-shot temperature %.2f after a repeat-guard trip", shakeoutTemperature)
		}
		// One-shot thinking-off after a guardrail block (see the
		// guardrailQuietNextRound decl). Appended last so it beats the static
		// option slices and any per-agent think budget.
		if guardrailQuietNextRound {
			guardrailQuietNextRound = false
			opts = append(opts, WithThink(false))
			Debug("[agent_loop] guardrail round: thinking OFF for one round after a block")
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
		llmStarted := time.Now()
		// Assert the tool-result adjacency invariant before we hand the history
		// to a provider. Violating it is a hard 400 from the chat template
		// tens of seconds later, with an error that names a line in a Jinja
		// file rather than the message we mis-ordered. Log, don't block: the
		// request may still succeed on a provider with a laxer template, and a
		// guard that kills the turn to prevent a bad turn is the mistake this
		// invariant already caused once.
		if bad := FirstToolOrderViolation(history); bad >= 0 {
			prev := "(start of history)"
			if bad > 0 {
				prev = strconv.Quote(history[bad-1].Role)
			}
			Log("[agent_loop] round %d: WARNING history[%d] carries tool results but follows %s, which has no tool calls — providers reject this ordering (a mid-round correction injected before the tool-results message is the usual cause)",
				round, bad, prev)
		}
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
		} else if cfg.TierOverride != TierUnset {
			// Carry the per-run pin onto the call itself. The non-streaming
			// branch below reads cfg directly, but ChatStreamWithReport gets
			// only these options — so without this the pin reached every path
			// EXCEPT the streaming one, and the streaming one is every
			// interactive turn. Copied rather than appended in place: opts is
			// the caller's slice and is reused every round.
			//
			// Skipped while de-escalated, where the cleared route key is the
			// decision and a pin must not undo it.
			callOpts = append(append([]ChatOption{}, opts...), WithTierOverride(cfg.TierOverride))
		}
		// Whether THIS round went to the lead — the fallback below only fires
		// for rounds the lead actually served, so a worker failure isn't
		// pointlessly retried on the worker.
		roundUsedLead := false
		if deescalated == "" && !T.LeadDenied() {
			roundUsedLead = cfg.wantsLead()
		}
		if streamHandler != nil {
			resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, callOpts...)
		} else {
			// A binding private pin redirects all routing to worker — no escalation.
			useLead := !T.LeadDenied() && cfg.wantsLead()
			if deescalated != "" {
				useLead = false
			}
			callFn := T.WorkerChat
			if useLead {
				callFn = T.LeadChat
				// The tier is settled HERE, from wantsLead — which already
				// consulted the route stage and any per-run override. Without
				// saying so, LeadChat re-derives it from the same RouteKey,
				// finds the stage says worker, and transparently delegates
				// back: the override reaches the call and is undone one frame
				// later. RouteKey stays on the options because it still
				// carries the stage's thinking preference.
				callOpts = append(callOpts, WithTierResolved())
				// An EXPLICIT pin also refuses the quiet degrade. A routing
				// preference should keep the session alive on the worker when
				// the lead is unavailable; somebody who pinned one system to the
				// lead said which model they wanted, and answering from the
				// other one behind a debug line is the substitution the pin
				// exists to prevent.
				if cfg.TierOverride == LEAD {
					callOpts = append(callOpts, WithNoTierFallback())
				}
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
			refused := estimatePromptTokens(history, systemPrompt)
			noteContextRefusal(cfg.ContextSize, refused)
			window := recoveryWindow(cfg.ContextSize, err, refused)
			Debug("[agent_loop] round %d: context exceeded — recovering into a %d-token window (refused prompt ~%d tokens)", round, window, refused)

			compactHistory(history, systemPrompt, window, true)
			// Compaction only cuts bodies. If the bulk is ordinary
			// conversation it is still there, so SUMMARIZE the older span
			// before considering throwing any of it away — the recovery
			// ladder is cheap-and-lossless, then costly-and-faithful, then
			// cheap-and-lossy, in that order.
			if stillTooBig(history, systemPrompt, window) {
				if folded, ok := T.summarizeOldHistory(ctx, history, window, contextRecoveryKeepWhole); ok {
					history = folded
				}
			}
			// Last resort. A summarizer that is unavailable or failing must
			// not leave the turn dead when dropping old text would let it run.
			if stillTooBig(history, systemPrompt, window) {
				budget := window - EstimateTokens(systemPrompt) - 34000
				if n := elideOldMessageText(history, budget, contextRecoveryKeepWhole); n > 0 {
					Log("[agent_loop] context recovery: summarization unavailable — elided ~%d tokens of older message text", n)
				}
			}
			if streamHandler != nil {
				resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, callOpts...)
			} else {
				useLead := !T.LeadDenied() && cfg.wantsLead()
				if deescalated != "" {
					useLead = false
				}
				callFn := T.WorkerChat
				if useLead {
					callFn = T.LeadChat
					callOpts = append(callOpts, WithTierResolved())
					if cfg.TierOverride == LEAD {
						callOpts = append(callOpts, WithNoTierFallback())
					}
				}
				resp, err = callFn(ctx, history, callOpts...)
			}
			if err != nil && IsContextExceededError(err) {
				// Log, not Debug. This is the moment somebody needs the
				// breakdown, and requiring --debug to learn where two million
				// tokens went means the answer is missing exactly when it is
				// being asked for.
				Log("[agent_loop] round %d: context exceeded after force-compact — %s", round, promptSizeReport(cfg, systemPrompt, history))
				return resp, history, fmt.Errorf("context exhausted: %s. Compaction only trims conversation history, so if the bulk is elsewhere a new session will not help (%w)",
					promptSizeHeadline(cfg, systemPrompt, history), err)
			}
			if err == nil {
				Debug("[agent_loop] round %d: context-exceeded recovered after force-compact", round)
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
		if err != nil || providerRefused(resp) {
			if roundUsedLead && deescalated == "" && !T.LeadDenied() {
				why := "the lead model call failed"
				diag := "The lead model could not complete this round"
				if err == nil {
					why = "the lead model refused this round on its own content policy"
					diag = "The lead model refused this round on its provider's content policy"
				}
				Log("[agent_loop] round %d: %s — retrying on the worker model", round, why)
				deescalated = "lead-unavailable"
				if cfg.OnDiag != nil {
					cfg.OnDiag("tier_deescalated", diag+" — this turn continued on the local worker model instead of stopping.")
				}
				workerOpts := append(append([]ChatOption{}, opts...), WithRouteKey(""))
				if streamHandler != nil {
					resp, err = T.ChatStreamWithReport(ctx, history, streamHandler, workerOpts...)
				} else {
					resp, err = T.WorkerChat(ctx, history, workerOpts...)
				}
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

		// Charge this round, then check the line. A turn under way finishes —
		// on the worker tier once it crosses — because stranding half-done
		// work costs the owner more than the round would have.
		if spent, crossed := chargeDailySpend(cfg, resp); crossed && deescalated == "" && !T.LeadDenied() {
			deescalated = "spend-cap"
			Log("[agent_loop] daily spend cap reached mid-turn for %q ($%.2f of $%.2f) — remaining rounds run on the worker tier", cfg.BudgetKey, spent, cfg.DailySpendUSD)
			emitDiag("spend-cap", fmt.Sprintf("This agent crossed its $%.2f daily allowance mid-turn; the rest of the turn ran on the local worker model.", cfg.DailySpendUSD))
		}

		Debug("[agent_loop] round %d: content=%d chars, reasoning=%d chars, tool_calls=%d", round, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))
		// BREADCRUMB: LLM returned. Pair with the "→ LLM call"
		// breadcrumb above to detect a wedged provider call.
		// The tier is on this line and not the "→ LLM call" one because it is
		// the tier that actually SERVED the round, taken off the response — a
		// fallback or a de-escalation is recorded here as what happened, where
		// anything logged before the call would only be what was intended. It
		// is the direct answer to "did this really run on the lead", which no
		// amount of reading the routing config can settle.
		llmWall += time.Since(llmStarted)
		llmCalls++
		Log("[agent_loop] round %d: ← LLM returned in %s (tier=%v, content=%d, tools=%d%s)", round, time.Since(llmStarted).Round(time.Millisecond), resp.Tier, len(resp.Content), len(resp.ToolCalls), promptReuseNote(resp))

		// The turn's prompt digest, completed with what the provider charged
		// and handed to whatever outlives the turn. Emitted here, after the
		// first response, because InputTokens is the one number in it that is
		// measured rather than estimated — and when it disagrees badly with
		// the estimate, the estimator is the thing that is wrong.
		if digestBuilt && !digestSent {
			digest.InputTokens = resp.InputTokens
			digest.Prefilled = resp.PromptTokensPrefilled
			digest.PrefillMS = resp.PrefillMS
			digestSent = true
			emitPromptDigest(ctx, cfg, digest)
		}

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
				Debug("[agent_loop] prompt-tool call: %s([masked: %d bytes])", toolCallLabel(*tc), len(formatArgs(tc.Args)))
			} else {
				Debug("[agent_loop] prompt-tool call: %s (args=%d bytes)", toolCallLabel(*tc), len(formatArgs(tc.Args)))
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

			// Unverified premise: this turn is answering someone who is not the
			// principal, and the thing about to happen rests on what they said.
			// Deflected ONCE, then allowed — see premiseGate.
			if note, held := premise.hold(tc.Name, writeTools[tc.Name]); held {
				Debug("[agent_loop] premise gate: held %s — turn rests on %s's unverified claim", tc.Name, cfg.LiveClaimSpeaker)
				emitDiag("unverified-premise-held", fmt.Sprintf("Held %s: this turn acts on %s's unverified claim. Asked to check it first.", tc.Name, cfg.LiveClaimSpeaker))
				history = append(history, Message{Role: "user", Content: frameworkNoticeTag + note})
				if cfg.OnStep != nil {
					cfg.OnStep(StepInfo{Round: round, ToolCalls: []ToolCall{*tc}})
				}
				continue
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
			if resp.StopReason == "stop" && len(resp.Content) >= cleanFinishProseFloor {
				allowProse = false
				Debug("[agent_loop] prose tool-call scan skipped — model finished cleanly with %d chars (stop_reason=%q)", len(resp.Content), resp.StopReason)
			}
			parsed := ParseTextToolCall(resp.Content, handlers, toolDefs, allowProse)
			if parsed == nil && resp.Reasoning != "" && strings.Contains(resp.Reasoning, "<function=") {
				// Reasoning-channel markup only — never the prose scan.
				if reasoningCall := ParseTextToolCall(resp.Reasoning, handlers, toolDefs, false); reasoningCall != nil {
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
				if txt := strings.TrimSpace(StripToolCallMarkup(resp.Content)); txt != "" {
					synthesizedFrom = txt
				}
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
				noteUncorrected(correctionOrphanedXML, "The reply again wrote tool-call XML for an unknown tool; the markup was stripped but no further re-prompt was left to spend.")
				if corrections.available(correctionOrphanedXML) && round < maxRounds {
					hint := ""
					if attemptedName != "" {
						hint = fmt.Sprintf(" You attempted to call %q which is not a registered tool.", attemptedName)
						if suggestion := nearestToolName(attemptedName, handlers); suggestion != "" {
							hint += fmt.Sprintf(" Did you mean %q?", suggestion)
						}
					}
					Debug("[agent_loop] orphaned XML tool-call detected (name=%q), re-prompting: correction %d/%d", attemptedName, corrections.spend(correctionOrphanedXML), maxCorrectionsPerKind)
					emitDiag("tool-markup-corrected", fmt.Sprintf("The reply wrote tool-call XML for an unknown tool (%q); markup stripped and re-prompted for a real call.", attemptedName))
					settleRound() // finalize the stripped prose so the retry doesn't concatenate into it
					history = append(history, Message{
						Role:    "user",
						Content: frameworkNoticeTag + "Your previous response contained tool-call XML markup with a name that doesn't match any available tool." + hint + " Look at your tool catalog for the exact tool name. Use the native function-calling format, not text markup. Try again now.",
					})
					continue
				}
			} else if refs := phantomDeliveryRefs(cfg, resp.Content); len(refs) > 0 {
				// The reply promises a file that does not exist and the turn
				// produced nothing to deliver. Same class as a fake tool call —
				// an action claimed but never taken — and it gets the same
				// remedy: strip the claim, say what was wrong, let the model
				// either do the work or admit it can't. Left alone, this leaves
				// the reply empty after stripping and the person on the other
				// end gets a generic apology about their phrasing.
				resp.Content = StripDeliveryMarkers(resp.Content)
				history[len(history)-1] = Message{
					Role:      "assistant",
					Content:   resp.Content,
					Reasoning: resp.Reasoning,
				}
				if corrections.available(correctionPhantomDelivery) && round < maxRounds {
					// Joined, not %v: a ref is a filename when the reply named
					// one and a plain noun phrase ("the image") when it didn't,
					// and "[the image]" reads as a placeholder the model is
					// meant to fill in rather than the thing it just claimed.
					named := strings.Join(refs, ", ")
					Debug("[agent_loop] phantom delivery detected (%s), re-prompting: correction %d/%d", named, corrections.spend(correctionPhantomDelivery), maxCorrectionsPerKind)
					emitDiag("phantom-delivery-corrected", fmt.Sprintf("The reply presented %s as delivered, but nothing was attached and nothing exists to attach. The claim was removed and the model re-prompted.", named))
					// Retract, not settle. On a streaming surface the false
					// claim has already been painted, and settling would leave
					// it standing above the correction — the user reads "Here's
					// your picture" and then, underneath, that there is no
					// picture. Same class as a blocked guardrail draft: a
					// statement the framework has decided must not stand.
					// It stays in `history` either way, which is what the model
					// needs to see to understand what it is being corrected on.
					// Falls back to settleRound on hosts with no retract wired.
					retractRound()
					history = append(history, Message{
						Role: "user",
						Content: frameworkNoticeTag + fmt.Sprintf(
							"You wrote your reply as though you were handing over %s. Nothing was attached and nothing exists to attach — it was never created, fetched, or it failed. The user received your words and no file. Either call the tool that actually produces it now, or tell them plainly that you do not have it. Do NOT present a file you have not made, and do not write a delivery marker for one.", named),
					})
					continue
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
				if corrections.exhausted(correctionPhantomDelivery) {
					named := strings.Join(refs, ", ")
					Debug("[agent_loop] phantom delivery still uncorrected after %d attempts (%s) — substituting a truthful reply", maxCorrectionsPerKind, named)
					emitDiag("phantom-delivery-uncorrected", fmt.Sprintf("The reply claimed %s again after two corrections, and no such file exists. The claim was replaced rather than delivered.", named))
					retractRound()
					resp.Content = UnfulfilledDeliveryReply(refs)
					history[len(history)-1] = Message{
						Role:      "assistant",
						Content:   resp.Content,
						Reasoning: resp.Reasoning,
					}
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
				noteUncorrected(correctionFakeToolCode, "The reply again wrote a tool call as a text block instead of calling it; the markup was stripped but no further re-prompt was left to spend.")
				if corrections.available(correctionFakeToolCode) && round < maxRounds {
					hint := ""
					if attemptedName != "" {
						hint = fmt.Sprintf(" You appeared to invoke %q.", attemptedName)
					}
					Debug("[agent_loop] fake <tool_code>/::name():: block detected (name=%q), re-prompting: correction %d/%d", attemptedName, corrections.spend(correctionFakeToolCode), maxCorrectionsPerKind)
					emitDiag("tool-markup-corrected", fmt.Sprintf("The reply wrote a tool call as plain text (%q) instead of a real call; markup stripped and re-prompted.", attemptedName))
					settleRound() // finalize the stripped prose so the retry doesn't concatenate into it
					history = append(history, Message{
						Role:    "user",
						Content: frameworkNoticeTag + "Your previous response wrote a tool invocation as plain TEXT (in a <tool_code> block or ::name(...):: form)." + hint + " That format does NOT execute — only structured tool_calls do. Re-issue the call NOW using the framework's native tool-calling mechanism. Do not wrap it in <tool_code>, do not use ::name():: syntax, do not narrate 'Creating the tool now…' — just emit the structured call.",
					})
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
		// up to maxCorrectionsPerKind times per turn.
		if len(resp.ToolCalls) == 0 {
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
			if responseWasTruncated(resp) {
				if corrections.available(correctionTruncated) {
					Debug("[agent_loop] round %d: output truncated (stop_reason=%q, %d chars) — continuing (correction %d/%d)",
						round, resp.StopReason, len(resp.Content), corrections.spend(correctionTruncated), maxCorrectionsPerKind)
					emitDiag("output-truncated", truncationDiag(resp))
					settleRound() // finalize the partial so the continuation doesn't concatenate into it
					// The continuation needs a round of its own. This used to
					// be gated on round < maxRounds, which left a one-round
					// call — a synthesis pass, a summary — with no way to
					// finish: its only round was the cut one, and the fragment
					// shipped as the report. Extend the runway by the round the
					// continuation takes, keeping an active wrap-up hard stop
					// in step; the correction budget above bounds how often.
					graceRounds++
					if hardStop >= 0 {
						hardStop++
					}
					truncatedLead.WriteString(resp.Content)
					history = append(history, Message{
						Role:    "user",
						Content: frameworkNoticeTag + "Your previous reply was CUT OFF before you finished it — you did not choose to stop. Continue from where you left off without repeating what you already said. If you were about to call a tool, emit the real structured tool call now; keep any preamble short so the call itself fits.",
					})
					continue
				}
				noteUncorrected(correctionTruncated, "The reply was cut off at the output limit again and no further continuation was left to spend, so the partial answer was delivered as written.")
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
			if providerCutReply(resp) {
				Log("[agent_loop] round %d: provider stopped the reply partway (stop_reason=%q, %d chars) — delivering the fragment with a diagnostic",
					round, resp.StopReason, len(resp.Content))
				emitDiag("provider-refusal", "The provider's content classifier stopped this reply partway (stop_reason=refusal); what you see is the fragment produced before the stop, not a finished answer. Rephrasing the request or retrying may get a complete one.")
			}

			// Action-promise correction DISABLED for now — it false-positived
			// on ordinary conversational replies ("I'll try to nail the house
			// next time."), burning rounds re-prompting for an action the model
			// never intended. Flip to true to re-enable; the reasoning-collapse
			// correction below is unaffected either way.
			const actionPromiseCorrection = false
			if actionPromiseCorrection && corrections.available(correctionActionPromise) && round < maxRounds && !toolFiredThisTurn && containsActionPromise(resp.Content) {
				Debug("[agent_loop] action-promise without tool call detected, re-prompting (correction %d/%d): %q", corrections.spend(correctionActionPromise), maxCorrectionsPerKind, truncForLog(resp.Content, 80))
				history = append(history, Message{
					Role:    "user",
					Content: frameworkNoticeTag + "You stated an intention to take an action (e.g. 'let me try', 'one moment') but called no tool. Either call the tool now to actually do what you said, or reply plainly that you can't proceed and explain what you tried. Do NOT promise further action without taking it.",
				})
				continue
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
			if endsWithCallAnnouncement(resp.Content) {
				noteUncorrected(correctionAnnouncedCall, "The reply again ended announcing a call it never made; no further re-prompt was left to spend, so it was delivered as written.")
			}
			if corrections.available(correctionAnnouncedCall) && round < maxRounds && endsWithCallAnnouncement(resp.Content) {
				Debug("[agent_loop] reply ends announcing a call that never followed, re-prompting: correction %d/%d: %q", corrections.spend(correctionAnnouncedCall), maxCorrectionsPerKind, truncForLog(resp.Content, 80))
				emitDiag("announced-call-corrected", "The reply ended by announcing a tool call it never made; re-prompted to actually make the call or finish the reply.")
				settleRound() // finalize the announcement so the retry doesn't concatenate into it
				history = append(history, Message{
					Role:    "user",
					Content: frameworkNoticeTag + "Your previous reply ended by announcing a call or content that never followed (it ends with a colon). If you meant to run a tool, emit the REAL structured tool call NOW — never write it out as text or stop after describing it. If no tool exists for what you described, say so plainly and finish the reply instead.",
				})
				continue
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
			contentIsPreamble := len(strings.TrimSpace(resp.Content)) <= noArgCorrectionMaxContentLen
			if toolMentionCorrection && contentIsPreamble && !cfg.DisableToolMentionCorrection && !toolFiredThisTurn {
				name, needsArgs := mentionedUncalledTool(resp.Content, handlers, toolDefs)
				if name != "" && !(corrections.available(correctionToolMention) && round < maxRounds) {
					noteUncorrected(correctionToolMention, "The reply again named a tool in prose without calling it; no further re-prompt was left to spend.")
				} else if name != "" {
					Debug("[agent_loop] tool %q named in prose without a call (needs_args=%v), re-prompting: correction %d/%d", name, needsArgs, corrections.spend(correctionToolMention), maxCorrectionsPerKind)
					emitDiag("tool-mention-corrected", fmt.Sprintf("The reply named the %q tool without calling it; re-prompted to either run it or answer plainly.", name))
					settleRound() // finalize the preamble so the retry doesn't concatenate into it
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
					history = append(history, Message{
						Role:    "user",
						Content: fmt.Sprintf(frameworkNoticeTag+"Your previous response referred to the %q tool but did not actually call it (%s). That tool IS available to you on this turn — do not say you lack access to what it reaches. If you intend to use it, emit the real structured tool call NOW. If you did NOT mean to use it, answer the user directly and do not claim you used it.", name, why),
					})
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
			trimmedContent := strings.TrimSpace(resp.Content)
			collapsed := len(trimmedContent) < 3 && len(resp.Reasoning) > 200
			if collapsed {
				noteUncorrected(correctionCollapse, "The round again produced no visible reply and called no tool; no further re-prompt was left to spend.")
			}
			if collapsed && corrections.available(correctionCollapse) && round < maxRounds {
				Debug("[agent_loop] reasoning-collapse detected (reasoning=%d chars, content=%d chars), re-prompting: correction %d/%d", len(resp.Reasoning), len(trimmedContent), corrections.spend(correctionCollapse), maxCorrectionsPerKind)
				emitDiag("empty-round-retried", "A round produced reasoning but no visible reply and no tool call; re-prompted for concrete output.")
				settleRound() // no-op when nothing streamed; keeps the discipline uniform across guards
				history = append(history, Message{
					Role:    "user",
					Content: frameworkNoticeTag + "Your previous round produced no visible reply (you reasoned but wrote nothing the user can see) and called no tool. Don't end a turn empty-handed: either produce concrete text now, or call a relevant tool. If the user's question is too vague to act on, ask a clarifying question.",
				})
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
			roundsLeft := maxRounds - round
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
			stalledOnErrors := cumulativeToolErrors > 0 && (len(trimmedContent) < 30 || promised)
			stalledOnNothing := promised && !toolFiredThisTurn
			gaveUp := roundsLeft >= 5 && (stalledOnErrors || stalledOnNothing)
			if gaveUp {
				noteUncorrected(correctionGiveUp, "The turn again stopped with tool errors unaddressed and rounds to spare; no further re-prompt was left to spend.")
			}
			if gaveUp && corrections.available(correctionGiveUp) {
				Debug("[agent_loop] give-up-with-errors-pending detected (errors=%d, rounds_left=%d, content=%dch, promised=%v), re-prompting: correction %d/%d",
					cumulativeToolErrors, roundsLeft, len(trimmedContent), promised, corrections.spend(correctionGiveUp), maxCorrectionsPerKind)
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
					if cumulativeToolErrors != 1 {
						errPlural = "s"
					}
					stopped := "stopped without producing a reply and without calling any tool"
					diag = fmt.Sprintf("The turn stopped with %d unaddressed tool error(s) and rounds to spare; re-prompted to adjust and retry rather than give up.", cumulativeToolErrors)
					if promised && len(trimmedContent) >= 30 {
						stopped = "ended your turn by saying you were ABOUT to do the work, and then called no tool"
						diag = fmt.Sprintf("The reply promised work it never did — it announced the next step, called no tool, and left %d tool error(s) unaddressed with rounds to spare. Re-prompted to actually do it.", cumulativeToolErrors)
					}
					nudge = fmt.Sprintf(
						frameworkNoticeTag+"You %s, but %d tool call%s errored earlier this turn that you didn't follow up on, and you have %d round%s remaining. Saying what you are about to do is not doing it — the user sees the sentence and nothing else, and nothing runs after your turn ends. DON'T end here with a polite summary of what you tried — that's giving up. Re-read the most recent error message(s) carefully, ADJUST your approach (different args, different tool, different sequence), and TRY AGAIN with a real tool call. If you genuinely have no other avenues, say so explicitly — but only after you've actually tried adjusting at least once.",
						stopped, cumulativeToolErrors, errPlural, roundsLeft, roundPlural)
				} else {
					diag = "The reply said the work was about to happen and then ended the turn without calling a single tool. Re-prompted to do it now or say plainly what is stopping it."
					nudge = fmt.Sprintf(
						frameworkNoticeTag+"You ended your turn saying you were about to do something, and then called no tool at all — so nothing happened. Nothing runs after your turn ends; the user is left holding a sentence. You have %d round%s remaining. Do it NOW with a real tool call, or say plainly what is stopping you. Do not repeat the promise, and do not apologize for it: do the work or explain why you can't.",
						roundsLeft, roundPlural)
				}
				emitDiag("giveup-retried", diag)
				settleRound() // no-op when nothing streamed; keeps the discipline uniform across guards
				history = append(history, Message{Role: "user", Content: nudge})
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
			if len(turnToolCalls) == 0 {
				Debug("%s", noToolDiagLine(round, LatestUserContent(messages), resp.Content, cfg.MaskDebugOutput))
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
			if verdict, convicted := judgeTurnClaim(cfg, TurnClaimEvidence{
				Request:       LatestUserContent(messages),
				Reply:         resp.Content,
				ToolCalls:     turnToolCalls,
				PriorWork:     cfg.priorWork(),
				PriorReports:  cfg.priorReports(),
				ToolErrors:    cumulativeToolErrors,
				LastToolError: lastToolError,
				Delivered:     cfg.deliveredCount(),
				Backgrounded:  cfg.backgrounded(),
				GivenEstimate: cfg.backgroundEstimate(),
				Unattended:    cfg.Unattended,
			}); convicted {
				// Two independent findings share one verdict, so each branch checks
				// its own. A machinery-only conviction reaching the claim branch
				// would tell the model its reply "did not happen" about a sentence
				// that was true.
				if verdict.Unkept && corrections.available(correctionUnkeptClaim) && round < maxRounds {
					Debug("[agent_loop] turn judge: reply claims work the turn did not do (%q) — %s; re-prompting: correction %d/%d",
						truncForLog(verdict.Claim, 80), verdict.Why, corrections.spend(correctionUnkeptClaim), maxCorrectionsPerKind)
					emitDiag("unkept-claim-corrected", fmt.Sprintf("The reply said %q, which did not happen: %s. Re-prompted to do it or say so.", truncForLog(verdict.Claim, 120), verdict.Why))
					// Retract rather than settle: the claim is false and, on a
					// streaming surface, already painted. Same call as the phantom
					// guard makes about the same class of statement.
					retractRound()
					history[len(history)-1] = Message{Role: "assistant", Content: resp.Content, Reasoning: resp.Reasoning}
					history = append(history, Message{
						Role: "user",
						Content: frameworkNoticeTag + fmt.Sprintf(
							"Your reply says: %q. That did not happen — %s. The user reads your words and gets nothing else; nothing runs after your turn ends. Either do it NOW with a real tool call, or rewrite the reply to say plainly what actually happened and what you could not do. Do not apologize, do not restate the claim, and do not promise it for later.",
							verdict.Claim, verdict.Why),
					})
					continue
				}
				if verdict.Unkept && corrections.exhausted(correctionUnkeptClaim) {
					emitDiag("unkept-claim-uncorrected", fmt.Sprintf("The reply still says %q after correction, and it did not happen: %s. Delivered as written.", truncForLog(verdict.Claim, 120), verdict.Why))
				}
				// Machinery is a separate finding with a separate budget, because it
				// is a separate failure: the reply is usually TRUE and merely says
				// things nobody asked to hear. A rewrite fixes it, where a false
				// claim needs the work done or admitted — so it must not spend the
				// allowance the serious one might need in the same turn.
				if leak := strings.TrimSpace(verdict.Machinery); leak != "" && !verdict.Unkept {
					if corrections.available(correctionMachinery) && round < maxRounds {
						Debug("[agent_loop] turn judge: reply explains machinery (%q); re-prompting: correction %d/%d",
							truncForLog(leak, 80), corrections.spend(correctionMachinery), maxCorrectionsPerKind)
						emitDiag("machinery-corrected", fmt.Sprintf("The reply explained how the work is being run (%q), which nobody asked about. Re-prompted for the same message without it.", truncForLog(leak, 120)))
						retractRound()
						history[len(history)-1] = Message{Role: "assistant", Content: resp.Content, Reasoning: resp.Reasoning}
						history = append(history, Message{
							Role: "user",
							Content: frameworkNoticeTag + fmt.Sprintf(
								"Your reply says: %q. That is plumbing — how the work is being carried out — and they did not ask about it. Nothing else is wrong with the reply. Send the SAME message with that part removed: what you are doing for them, in one line, the way a person would. No ids, no mention of how or where anything runs, no invitation to check back, no time estimate you were not given.",
								leak),
						})
						continue
					}
					if corrections.exhausted(correctionMachinery) {
						emitDiag("machinery-uncorrected", fmt.Sprintf("The reply still explains how the work is run (%q) after correction. Delivered as written.", truncForLog(leak, 120)))
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
			liveNote := liveClaimNote(cfg.LiveClaimSpeaker, LatestUserContent(messages))
			if gv, convicted := judgeTurnGrounding(cfg, TurnGroundingEvidence{
				Reply: resp.Content,
				// Stored notes plus, on a channel, whatever the person just
				// said. Composed here rather than by the host so the live entry
				// is worded the same way everywhere it is judged.
				Unchecked: withLiveClaim(cfg.UncheckedClaims, cfg.LiveClaimSpeaker, LatestUserContent(messages)),
				ToolCalls: turnToolCalls,
			}); convicted {
				if corrections.available(correctionUngrounded) && round < maxRounds {
					Debug("[agent_loop] grounding judge: reply asserts an unchecked claim (%q) — re-prompting: correction %d/%d",
						truncForLog(gv.Claim, 80), corrections.spend(correctionUngrounded), maxCorrectionsPerKind)
					emitDiag("ungrounded-claim-corrected", fmt.Sprintf("The reply stated %q as fact; it traces to an unchecked note (%q). Re-prompted to check it or attribute it.",
						truncForLog(gv.Claim, 120), truncForLog(gv.Basis, 120)))
					// NOT retracted, unlike an unkept claim. That one is false and
					// has to be taken back; this one may well be true — nobody
					// checked, which is a different and lesser thing. Settling the
					// round and asking for a rewrite keeps a correct answer from
					// being yanked off the screen over its phrasing.
					history[len(history)-1] = Message{Role: "assistant", Content: resp.Content, Reasoning: resp.Reasoning}
					// Two shapes of basis, and they call for different rewrites.
					// A stored note is something the agent holds; a live claim is
					// something a person in the room said moments ago, where the
					// natural fix is "they posted…", not "per your note…".
					basis := fmt.Sprintf("a stored note marked as not independently checked: %q", gv.Basis)
					if basisIsLiveClaim(liveNote, gv.Basis) {
						who := strings.TrimSpace(cfg.LiveClaimSpeaker)
						if who == "" {
							who = "the sender"
						}
						basis = fmt.Sprintf("what %s just put in the conversation, which nothing has checked: %q", who, gv.Basis)
					}
					history = append(history, Message{
						Role: "user",
						Content: frameworkNoticeTag + fmt.Sprintf(
							"Your reply states %q as established fact. That traces to %s. Either CHECK it now with a real tool call and then say what you found, or rewrite that one sentence to say where it came from (\"you mentioned…\", \"they posted…\"). If it was never offered as fact in the first place, a joke, a meme, teasing, obvious exaggeration, do NEITHER of those: reply in the register it was sent in and just don't restate its content as true. Describing what a picture you were shown visibly contains is not a claim and needs no hedge. Send the SAME reply with only that fixed: do not apologise, do not say you should have checked, do not mention this instruction, and do not add a disclaimer or hedge anything else.",
							gv.Claim, basis),
					})
					continue
				}
				if corrections.exhausted(correctionUngrounded) {
					emitDiag("ungrounded-claim-uncorrected", fmt.Sprintf("The reply still states %q as fact after correction. Delivered as written.", truncForLog(gv.Claim, 120)))
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
			if cfg.GuardrailCheck != nil && strings.TrimSpace(resp.Content) != "" {
				if dec := cfg.GuardrailCheck(GuardHookPreOutput, resp.Content); dec.Blocked {
					gmsg := dec.Message
					// Halt overrides the correction budget: once the app has
					// decided this turn is over, asking the same model for one
					// more revision is another generation from the context that
					// just failed. A block on a rule that is not Correctable says the
					// same thing about the FIRST attempt — the rule forbids what was
					// asked for, so there is no compliant revision to wait for.
					halted := cfg.GuardrailHalted != nil && cfg.GuardrailHalted()
					if !halted && dec.Correctable && guardrailOutputCorrections < maxGuardrailOutputCorrections && round < maxRounds {
						Debug("[agent_loop] guardrail blocked pre-output, re-prompting (correction %d/%d)", guardrailOutputCorrections+1, maxGuardrailOutputCorrections)
						emitDiag("guardrail-blocked-output", "The reply was withheld by an enforced guardrail; re-prompted to revise.")
						retractRound()                              // DISCARD the withheld bubble — not persisted or delivered (settle would commit it)
						replaceBlockedDraft(guardrailRedactedDraft) // scrub the leaked draft from history — never persisted or delivered
						history = append(history, Message{Role: "user", Content: frameworkNoticeTag + gmsg})
						guardrailOutputCorrections++
						// The revise pass is a rewrite of text the model just
						// produced, against one stated constraint — the most
						// expensive round in the turn and the one with least to
						// reason about.
						guardrailQuietNextRound = true
						continue
					}
					// Not correctable, halted, or the budget/rounds are spent and the
					// reply STILL violates. Do not release it — overwrite the draft in
					// place with the safe decline and return that. The floor is a
					// canned reply, not the leak, no matter how hard the turn was
					// pushed.
					Debug("[agent_loop] guardrail pre-output final (correctable=%v halted=%v) — handing the reply to the rejection model", dec.Correctable, halted)
					emitDiag("guardrail-output-substituted", "A reply kept violating an enforced guardrail; a neutral decline was substituted so nothing protected was released.")
					retractRound() // DISCARD the leaking draft bubble; the safe reply below is what gets delivered
					fallback := guardrailRejectionReply(cfg, "pre_output", history)
					replaceBlockedDraft(fallback)
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
					resp.Content = fallback + guardrailClosedNote
					resp.Reasoning = ""
					resp.ToolCalls = nil
					return resp, history, nil
				}
			}

			if truncatedLead.Len() > 0 && cfg.SettleRound == nil {
				// A caller with no SettleRound never displayed the cut-off
				// part — the loop is the only thing that saw it — so the reply
				// it gets has to carry it. (A caller WITH one already showed
				// the partial as its own bubble; handing it back joined would
				// render it twice.) Before this, every headless consumer of a
				// continued reply got the continuation alone: a report that
				// began mid-sentence, or, when the continuation was itself the
				// cut one, a report that simply ended short.
				resp.Content = joinContinuation(truncatedLead.String(), resp.Content)
				Debug("[agent_loop] round %d: reply joined with %d chars cut off earlier this turn (caller never saw them)", round, truncatedLead.Len())
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
		if cfg.GuardrailCheck != nil && cfg.InterimContentHidden && strings.TrimSpace(resp.Content) != "" && !skippedInterimGuard {
			skippedInterimGuard = true // once per turn; this is a property of the host, not the round
			Debug("[agent_loop] periodic guardrail skipped: this host does not show or store interim round prose (pre_output still judges the final reply)")
		}
		if cfg.GuardrailCheck != nil && !cfg.InterimContentHidden && strings.TrimSpace(resp.Content) != "" &&
			!judgedNarration[resp.Content] {
			judgedNarration[resp.Content] = true
			if dec := cfg.GuardrailCheck(GuardHookPeriodic, resp.Content); dec.Blocked {
				gmsg := dec.Message
				retractRound()
				replaceBlockedDraft(guardrailRedactedDraft) // scrub the leaked narration (and its unexecuted tool calls) from history
				// A redirect asks the same model to carry on differently. It is
				// only available while there is budget and rounds left to carry on
				// INTO, the app hasn't halted the turn, and the rule that fired was
				// one a different course could satisfy. Otherwise the turn ends
				// here — the one thing that must never happen is releasing the
				// narration because there was no correction left to spend.
				canRedirect := dec.Correctable &&
					!(cfg.GuardrailHalted != nil && cfg.GuardrailHalted()) &&
					guardrailOutputCorrections < maxGuardrailOutputCorrections &&
					round < maxRounds
				if !canRedirect {
					Debug("[agent_loop] guardrail periodic final at round %d (correctable=%v) — handing over to the rejection model", round, dec.Correctable)
					emitDiag("guardrail-halted", "An enforced guardrail stopped this turn; the reply was written by a separate check, not by the agent.")
					reply := guardrailRejectionReply(cfg, GuardHookPeriodic, history)
					replaceBlockedDraft(reply)
					resp.Content = reply
					resp.Reasoning = ""
					resp.ToolCalls = nil
					return resp, history, nil
				}
				Debug("[agent_loop] guardrail periodic block at round %d, redirecting", round)
				emitDiag("guardrail-periodic-block", "An enforced guardrail flagged the turn mid-flight; redirected before running this round's tools.")
				history = append(history, Message{Role: "user", Content: frameworkNoticeTag + gmsg})
				guardrailOutputCorrections++
				continue
			}
		}

		// Execute tool calls and collect results.
		// Independent calls run in parallel; confirmable tools are
		// checked serially first to avoid concurrent prompts.
		//
		// Anything the round's ARGUMENTS are read against is pinned first: the
		// calls are settled, none has run, and no sibling has yet changed the
		// state a later one refers to.
		if cfg.BeforeToolRound != nil {
			cfg.BeforeToolRound()
		}
		results := make([]ToolResult, len(resp.ToolCalls))
		toolErrors := 0
		guardBlockedThisRound := false // set when the loop-guard blocks a repeat this round
		guardrailHalt := ""            // non-empty ⇒ the app halted the turn at this hook; end it after the round settles

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
			sendKey string
		}
		var work []toolWork
		// batchSend claims recipients WITHIN this round — the sends a model
		// batches into one response (the "fired all 4 jokes at once" case) share
		// a round, so sentThisTurn (marked only post-execution) wouldn't yet see
		// the first when the second is checked. Claimed pre-execution here so the
		// second in the same batch is held too.
		batchSend := map[string]bool{}

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
		batchSig := map[string]int{}
		var batchDup [][2]int

		// Round batch cap. A model can emit an arbitrarily large tool batch in
		// ONE round (observed: ~120 agent dispatches in a single response) —
		// per-tool guards then block each call individually, but all of them
		// still execute-or-STOP and the round takes the full hit. Cap the
		// batch: calls past the cap get an error result (the API still needs a
		// result per call id) and never reach a handler. Counts as a guard
		// block so repeated capped rounds feed the wedge break-out below.
		const maxToolCallsPerRound = 24
		if len(resp.ToolCalls) > maxToolCallsPerRound {
			emitDiag("round-batch-capped", fmt.Sprintf("The model emitted %d tool calls in one round; only the first %d ran.", len(resp.ToolCalls), maxToolCallsPerRound))
			guardBlockedThisRound = true
		}
		// Identifiers this conversation has actually produced: everything the
		// user wrote, every tool result, and the system prompt (which carries
		// the appliance / agent / memory ids a turn is entitled to name). The
		// model's own prose is deliberately absent — that is where an invented
		// id is written, and treating it as a source would launder one.
		knownIDs := collectKnownIDs(systemPrompt, history)
		for i, tc := range resp.ToolCalls {
			if i >= maxToolCallsPerRound {
				results[i] = ToolResult{ID: tc.ID, Content: fmt.Sprintf("Error: round batch cap — a single round may fire at most %d tool calls; this call (#%d) was dropped. Use the results you already have, or continue next round with a SMALLER, deliberate batch.", maxToolCallsPerRound, i+1), IsError: true}
				toolErrors++
				continue
			}
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
				Debug("[agent_loop] tool call: %s([masked: %d bytes])", toolCallLabel(tc), len(formatArgs(tc.Args)))
			} else {
				Debug("[agent_loop] tool call: %s (args=%d bytes)", toolCallLabel(tc), len(formatArgs(tc.Args)))
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

			// Guardrail pre-action gate: before a CONSEQUENTIAL tool call
			// (the NeedsConfirm set — sends, posts, deletes, spends) runs, an
			// independent warden judges it against the agent's guardrails. A
			// violation blocks the call and hands back the app's trusted
			// message (never fenced). guardBlockedThisRound feeds the wedge
			// machinery so repeated blocks settle the turn.
			judgeThisCall := needsConfirm[tc.Name]
			if !judgeThisCall && cfg.GuardrailActionGate != nil {
				judgeThisCall = cfg.GuardrailActionGate(tc.Name)
			}
			if cfg.GuardrailCheck != nil && judgeThisCall {
				// Decision.Correctable is deliberately ignored here: a blocked call
				// still leaves the agent a compliant way to finish the task, so
				// this stays block-and-continue no matter which rule fired.
				// Ending the turn is the escalation counter's job.
				if dec := cfg.GuardrailCheck(GuardHookPreAction, tc.Name+" "+formatArgsForGuardrail(tc.Args)); dec.Blocked {
					Debug("[agent_loop] guardrail blocked pre-action: %s", tc.Name)
					guardBlockedThisRound = true
					// The next round reads a refusal it did not expect and is
					// told not to name the mechanism. Deliberating on that is
					// how a blocked turn burns thousands of reasoning tokens
					// and emits nothing. Acting on the message is a short step.
					guardrailQuietNextRound = true
					results[i] = ToolResult{ID: tc.ID, Content: dec.Message, IsError: true}
					toolErrors++
					// Blocking one call is not enforcement when the model can
					// simply pick a different route to the same end. If the app
					// says this turn is over, stop the whole turn — recorded here
					// and acted on once the round's remaining results settle, so
					// no half-executed batch is left behind.
					if cfg.GuardrailHalted != nil && cfg.GuardrailHalted() {
						guardrailHalt = "pre_action"
					}
					continue
				}
			}

			if needsConfirm[tc.Name] {
				if !confirmFn(tc.Name, formatArgs(tc.Args)) {
					Debug("[agent_loop] tool call denied by user: %s", tc.Name)
					// The bare "denied by user" line TAUGHT a workaround:
					// agents whose governed tool was denied on scheduled
					// fires learned to reach the same service through
					// ungoverned tools instead (hand-rolled fetch_url
					// against a guessed API — a habit that outlived the
					// gate). A denial denies the OPERATION, not one route
					// to it — say so.
					results[i] = ToolResult{ID: tc.ID, Content: "Error: tool call denied by user — this operation was not authorized to run. Do NOT work around the denial by attempting the same operation through a different tool (raw fetch_url, shell, or a dispatch); proceed without it, or report that it needs the owner's authorization.", IsError: true}
					toolErrors++
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
			if !cfg.DisableIDProvenanceGate {
				if refusal := idProvenanceRefusal(tc.Name, tc.Args, knownIDs); refusal != "" {
					Debug("[agent_loop] id-provenance: %s blocked (argument id was never issued this session)", tc.Name)
					guardBlockedThisRound = true
					emitDiag("invented-id", fmt.Sprintf("A call to '%s' referenced an id that nothing in this conversation produced; it was refused before it ran.", tc.Name))
					results[i] = ToolResult{ID: tc.ID, Content: refusal, IsError: true}
					toolErrors++
					continue
				}
			}

			// Action quota: this action has already run its allowance in the
			// last 24 hours. Refused before it runs, and counted below only
			// when it SUCCEEDS — a failed call consumed nothing anyone cares
			// about, and charging for it would end a day's budget on an
			// outage.
			if refusal, action := actionQuotaRefusal(cfg, tc.Name, tc.Args); refusal != "" {
				Debug("[agent_loop] quota: %s blocked (%s is at its 24h allowance)", tc.Name, action)
				guardBlockedThisRound = true
				emitDiag("action-quota", fmt.Sprintf("'%s' has used its allowance of %d per 24 hours; further calls were refused this turn.", action, cfg.ActionQuotas[action]))
				results[i] = ToolResult{ID: tc.ID, Content: refusal, IsError: true}
				toolErrors++
				continue
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

			// Duplicate-send guard: a second delivery to the same recipient this
			// turn is HELD, not sent. Catches the "drafted several messages and
			// fired them all" mistake that the identical-args guards miss (the
			// drafts differ). Keyed on recipient, not text.
			sendKey := ""
			if cfg.SendGuardKey != nil {
				sendKey = cfg.SendGuardKey(tc.Name, tc.Args)
			}
			if sendKey != "" && (sentThisTurn[sendKey] || batchSend[sendKey]) {
				Debug("[agent_loop] send-guard: %s held (already sent to this recipient this turn)", tc.Name)
				guardBlockedThisRound = true
				results[i] = ToolResult{
					ID:      tc.ID,
					Content: fmt.Sprintf("HELD — you already sent a message to this recipient this turn via '%s', so this additional send was NOT delivered (it would double-message them). If you drafted several variations, that's expected: pick the ONE you want and send it on your NEXT turn. If you genuinely need to send a distinct follow-up, do it next turn, not batched with the first.", tc.Name),
					IsError: true,
				}
				toolErrors++
				continue
			}

			// Identical sibling already approved this batch — run it once and
			// copy the result. Claimed here, at the append, so a call that got
			// held by a guard above never becomes the canonical for a sibling
			// that would otherwise have run.
			if canon, dup := batchSig[sig]; dup {
				Debug("[agent_loop] batch-dedup: %s call #%d is identical to #%d — running once", tc.Name, i+1, canon+1)
				batchDup = append(batchDup, [2]int{i, canon})
				continue
			}
			batchSig[sig] = i

			if sendKey != "" {
				batchSend[sendKey] = true // claim so a same-batch duplicate is held
			}
			work = append(work, toolWork{index: i, tc: tc, handler: handler, sig: sig, sendKey: sendKey})
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
				debugToolErr(toolCallLabel(w.tc), err)
				results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
				toolErrors++
			} else {
				debugResult(toolCallLabel(w.tc), output)
				results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
				chargeActionQuota(cfg, w.tc.Name, w.tc.Args)
			}
		} else if len(work) > 1 {
			var wg sync.WaitGroup
			var errCount int32
			invokeStore := func(w toolWork) {
				output, err := safeInvoke(w.tc.Name, w.handler, w.tc.Args)
				if err != nil {
					debugToolErr(toolCallLabel(w.tc), err)
					results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
					atomic.AddInt32(&errCount, 1)
				} else {
					debugResult(toolCallLabel(w.tc), output)
					results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
					chargeActionQuota(cfg, w.tc.Name, w.tc.Args)
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
			for _, w := range work {
				lane, laned := "", false
				if fn := batchLaneFns[w.tc.Name]; fn != nil {
					laned = true
					if key := fn(w.tc.Args); key != "" {
						lane = w.tc.Name + "\x00" + key
					}
				} else if serialFireTools[w.tc.Name] {
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
			toolErrors += int(atomic.LoadInt32(&errCount))
		}

		// Satisfy the deduped siblings from the canonical call's result. The API
		// needs a result per tool_call id, so these can't just be omitted. They
		// carry the SAME content (the model gets consistent data, not an error
		// it has to reconcile) behind a one-line note, so a model that meant to
		// vary the args can see that it didn't. IsError is copied but the error
		// is NOT re-counted — one call ran, so one outcome is the honest count.
		for _, d := range batchDup {
			dup, canon := d[0], d[1]
			src := results[canon]
			results[dup] = ToolResult{
				ID:      resp.ToolCalls[dup].ID,
				Content: fmt.Sprintf("[DUPLICATE CALL — you issued this exact call %d times in one response; it ran ONCE and every copy returns the same result below. To get something different, change the arguments.]\n\n%s", countBatchDupes(batchDup, canon)+1, src.Content),
				IsError: src.IsError,
			}
		}

		// Corrections raised while inspecting this round's results. They MUST
		// land after the tool-results message, never before it: providers
		// enforce that a tool result directly follows an assistant-or-tool
		// message, and a plain user turn wedged into that gap is a hard 400
		// from the chat template, not a soft degradation.
		var pendingCorrections []Message

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
				// Success half of the failure-streak damper: this tool worked,
				// so its earlier failure results are stale — rewrite them to
				// resolved markers before the model has to arbitrate between
				// "it's broken" (repeated) and "it works" (said once).
				if shapes := toolFailShapes[w.tc.Name]; len(shapes) > 0 {
					if n := retireResolvedFailureResults(history, shapes, w.tc.Name); n > 0 {
						Debug("[agent_loop] failure-streak collapse: %s succeeded — %d earlier failure result(s) marked resolved", w.tc.Name, n)
					}
					delete(toolFailShapes, w.tc.Name)
				}
			}
			// Mark the recipient reached only on a SUCCESSFUL send — a failed
			// delivery shouldn't block a legitimate retry to the same recipient.
			if w.sendKey != "" && !results[w.index].IsError {
				sentThisTurn[w.sendKey] = true
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
			if m := toolFailShapes[w.tc.Name]; m == nil {
				toolFailShapes[w.tc.Name] = map[string]bool{shape: true}
			} else {
				m[shape] = true
			}
			// Streak damper: from the errShapeCollapseAt-th recurrence on,
			// collapse the earlier duplicates in the accumulated history.
			// The first occurrence stays full; this round's copy is appended
			// after this loop, so the model always sees first + latest.
			if n >= errShapeCollapseAt {
				if c := collapseRepeatedFailureResults(history, shape, false); c > 0 {
					Debug("[agent_loop] failure-streak collapse: %q — %d earlier duplicate result(s) collapsed", oneLineShape(shape), c)
				}
			}
			// Say it plainly, once. The model can see each failure but not
			// that it has now hit the SAME one from several directions —
			// which is the fact that should change its approach.
			if n >= errShapeNudgeAt && !errShapeNudged[shape] {
				errShapeNudged[shape] = true
				Debug("[agent_loop] failure-shape guard: %q seen %d times this turn — nudging", oneLineShape(shape), n)
				msg, consulted := failureShapeCorrection(n, oneLineShape(shape), results[w.index].Content, cfg.Consult)
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
				pendingCorrections = append(pendingCorrections, Message{Role: "user", Content: frameworkNoticeTag + msg})
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
		// Now the deferred corrections — after the results, where a plain user
		// turn is legal and the model reads them as commentary on what it just
		// saw rather than as an interruption of the tool exchange.
		history = append(history, pendingCorrections...)
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
					Content: viewImageNote(imgs),
					Images:  viewImageBytes(imgs),
				})
			}
		}
		prevHadToolCalls = true

		// Failure-streak bookkeeping. A round counts as a "failure"
		// when EVERY tool result this round has IsError=true. Any
		// successful result resets the streak. After N consecutive
		// failure rounds, inject the pivot nudge once per streak.
		// A pre_action halt ends the turn here — after the round's results are
		// assembled (so nothing is left half-executed) and before the model is
		// asked for another word. The blocked round is retracted and the reply
		// comes from the rejection model, never from the context that just
		// tripped the rule.
		if guardrailHalt != "" {
			Debug("[agent_loop] guardrail halt at %s — ending the turn, handing over to the rejection model", guardrailHalt)
			emitDiag("guardrail-halted", "An enforced guardrail stopped this turn; the reply was written by a separate check, not by the agent.")
			retractRound()
			reply := guardrailRejectionReply(cfg, guardrailHalt, history)
			replaceBlockedDraft(reply)
			resp.Content = reply
			resp.Reasoning = ""
			resp.ToolCalls = nil
			return resp, history, nil
		}

		allFailed := len(results) > 0
		for i := range results {
			if !results[i].IsError && !isGuardStopResult(results[i].Content) {
				allFailed = false
				break
			}
			if isGuardStopResult(results[i].Content) {
				// Inner guards (the agents tool's dispatch ceiling, the
				// identical-dispatch check) return their STOP verdict as a
				// SUCCESSFUL result string — without this, a wall of STOPs
				// read as progress, the wedge streak reset every round, and
				// the model could burn hundreds of rounds re-dispatching
				// into the same ceiling (observed with 120+ blocked
				// Comedian dispatches).
				guardBlockedThisRound = true
			}
		}
		if allFailed {
			failureStreak++
			if !failureStreakWarned && failureStreak >= failureStreakThreshold {
				Debug("[agent_loop] failure streak hit %d — injecting pivot nudge", failureStreak)
				history = append(history, Message{
					Role: "user",
					Content: fmt.Sprintf(
						frameworkNoticeTag+"You've hit %d rounds in a row where every tool call failed. Recommending checking other vectors first before resuming this approach — a different tool, a different angle, or asking the user for clarification is often faster than continuing to iterate here.",
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
		if guardBlockedThisRound {
			shakeoutNextRound = true
		}
		if guardBlockedThisRound && allFailed {
			// The blocked call was read out of the model's own prose and
			// the answer that prose came from is still in hand — so the
			// wedge would spend a whole extra generation rebuilding
			// something we already have. Return it and end the turn.
			// (Observed: 39s and 3162 output tokens to regenerate a
			// finished 8366-char answer.) Only fires when the round's
			// ONLY calls were synthesized and every one of them failed,
			// so a real tool call is never short-circuited.
			if synthesizedFrom != "" {
				Debug("[agent_loop] loop-guard: blocked call was synthesized from prose — returning the model's own answer (%d chars) instead of regenerating", len(synthesizedFrom))
				resp.Content = synthesizedFrom
				resp.ToolCalls = nil
				return resp, history, nil
			}
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
		if keepGoingOnly && cfg.backgrounded() {
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
			emitDiag("keep-going-while-detached", "The turn asked for another round while a background job was still running. There is nothing to wait for in-turn — the result arrives on its own — so the turn was finalized instead of spinning.")
			forceFinal = true
			break
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
				Content: frameworkNoticeTag + "You have signalled continue without taking any action. Do NOT call keep_going again. This round, either emit the ACTUAL tool call you intend (the tool is already loaded — call it directly), or, if you cannot, give your final answer to the user now.",
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
		// Evidence for the turn judge: what ran, and what the last failure said.
		// Duplicates are kept on purpose — three image calls are three attempts,
		// and a judge that sees one of them is reading a different turn.
		for _, w := range work {
			// The LABEL, not the bare name: a grouped tool's read and its write
			// share a name, and "moltbook ran nine times" is consistent with a
			// reply claiming three posts. "moltbook/get_feed" is not.
			turnToolCalls = append(turnToolCalls, toolCallLabel(w.tc))
			if w.index < len(results) && results[w.index].IsError {
				lastToolError = results[w.index].Content
			}
		}

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
