package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/snugforge/nfo"
	"math/rand/v2"
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

// FirstToolOrderViolation returns the index of the first message carrying tool
// results that does NOT directly follow an assistant tool-call message (or
// another tool-result message), or -1 when the history is well-formed.
//
// Providers enforce this adjacency in their chat templates, and they enforce it
// HARD: llama.cpp answers with a 400 ("A tool message must follow an assistant
// or tool message") and the turn dies with loop_error, ~40s in, having produced
// nothing. It is an easy invariant to break by accident, because the natural
// place to inject a mid-round correction — right where the results are
// inspected — is inside the very gap the rule forbids. That is exactly how the
// failure-shape guard broke it: the guard fired only on turns that had already
// hit three identical failures, so it killed precisely the builds that were
// struggling while easy turns sailed through.
//
// Kept as an exported pure function so callers can assert cheaply before a
// dispatch and leave a breadcrumb naming the offending index, instead of
// discovering the problem as an opaque provider error much later.
func FirstToolOrderViolation(history []Message) int {
	for i, m := range history {
		if len(m.ToolResults) == 0 {
			continue
		}
		if i == 0 {
			return i
		}
		prev := history[i-1]
		if len(prev.ToolCalls) == 0 && len(prev.ToolResults) == 0 {
			return i
		}
	}
	return -1
}

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

// Failure-streak collapse — the in-context repetition damper.
//
// One failure fact carries signal; thirty-eight verbatim copies carry a
// BEHAVIOR: by mid-turn, "call the tool, get the error, note it, continue" is
// the dominant pattern in the context and the model imitates the pattern it
// sees (observed live: a standing agent re-firing one broken tool 38× per
// cycle, cycle after cycle — the storms were self-teaching). The damper keeps
// the model's knowledge of a failure while deleting the repetition: once one
// normalized failure shape has recurred errShapeCollapseAt times, the MIDDLE
// occurrences in the accumulated history are rewritten to a one-line marker —
// the FIRST occurrence keeps its full text (the informative copy) and the
// newest occurrence stays full (the current state). Rewrite-in-place, never
// remove: tool-result messages must keep their ids and ordering for the
// provider's chat template.
//
// This also applies to INCOMING history at loop start (prior turns' tool
// results ride back in via toLLMMessages), so a storm that already happened
// stops re-teaching every later turn. Stored sessions are only affected for
// results produced after the streak tripped — the first full occurrence is
// always preserved for exports/debugging.
const errShapeCollapseAt = 3

// collapsedFailureMarker names the collapsed shape so the marker is
// self-describing — in the live context AND in a session export, a collapsed
// row should say WHICH error it was without scrolling for the first
// occurrence. Markers of one shape are byte-identical (cache-friendly), and
// the marker's own shape never matches the original (the prefix text differs),
// so sweeps stay idempotent.
func collapsedFailureMarker(shape string) string {
	return "(repeated failure collapsed — same as the earlier full result: \"" + oneLineShape(shape) + "\")"
}

// resolvedFailureMarker replaces a failure result whose tool LATER succeeded
// this turn — the failure is stale the moment the 200 lands, and a context
// carrying both teaches the model to arbitrate; models side with whatever
// appears more often, which is the failure.
func resolvedFailureMarker(toolName string) string {
	return "(earlier " + toolName + " failure collapsed — a later " + toolName + " call SUCCEEDED this turn; treat the failure as resolved)"
}

// collapseRepeatedFailureResults rewrites duplicate occurrences of one
// failure shape across history's tool results. The first occurrence is always
// kept in full; keepLast additionally preserves the newest one (used for
// incoming history, where the latest copy IS the current state — during a
// live turn the current round's full result is appended after the sweep, so
// keepLast is false there). Copy-on-write per message: history is a shallow
// copy of the caller's slice, so the ToolResults backing arrays are shared —
// clone before mutating so the rewrite never reaches the caller's messages.
func collapseRepeatedFailureResults(history []Message, shape string, keepLast bool) int {
	type pos struct{ mi, ri int }
	var found []pos
	for mi := range history {
		for ri := range history[mi].ToolResults {
			r := &history[mi].ToolResults[ri]
			if r.IsError && normalizeFailureShape(r.Content) == shape {
				found = append(found, pos{mi, ri})
			}
		}
	}
	end := len(found)
	if keepLast {
		end--
	}
	if end <= 1 {
		return 0
	}
	n := 0
	cloned := map[int]bool{}
	for _, p := range found[1:end] {
		if !cloned[p.mi] {
			history[p.mi].ToolResults = append([]ToolResult(nil), history[p.mi].ToolResults...)
			cloned[p.mi] = true
		}
		history[p.mi].ToolResults[p.ri].Content = collapsedFailureMarker(shape)
		n++
	}
	return n
}

// collapseIncomingFailureStreaks applies the damper to the history a turn
// STARTS with: prior turns' failure storms ride back in through the rebuilt
// tool rounds, and without this each new turn re-reads the whole wall.
func collapseIncomingFailureStreaks(history []Message) int {
	counts := map[string]int{}
	for mi := range history {
		for _, r := range history[mi].ToolResults {
			if r.IsError {
				if s := normalizeFailureShape(r.Content); s != "" {
					counts[s]++
				}
			}
		}
	}
	total := 0
	for shape, c := range counts {
		if c >= errShapeCollapseAt {
			total += collapseRepeatedFailureResults(history, shape, true)
		}
	}
	return total
}

// retireResolvedFailureResults is the success half of the damper: when a tool
// that failed earlier this turn SUCCEEDS, every prior failure result matching
// one of that tool's recorded failure shapes is rewritten to a resolved
// marker — including the first occurrence, because a resolved failure's full
// text is no longer information, it's a contradiction of the current state.
func retireResolvedFailureResults(history []Message, shapes map[string]bool, toolName string) int {
	if len(shapes) == 0 {
		return 0
	}
	n := 0
	cloned := map[int]bool{}
	for mi := range history {
		for ri := range history[mi].ToolResults {
			r := &history[mi].ToolResults[ri]
			if !r.IsError || !shapes[normalizeFailureShape(r.Content)] {
				continue
			}
			if !cloned[mi] {
				history[mi].ToolResults = append([]ToolResult(nil), history[mi].ToolResults...)
				cloned[mi] = true
			}
			history[mi].ToolResults[ri].Content = resolvedFailureMarker(toolName)
			n++
		}
	}
	return n
}

// oneLineShape renders a failure shape for a log line or a directive.
func oneLineShape(shape string) string {
	if len(shape) > 120 {
		return shape[:120] + "…"
	}
	return shape
}

// viewImageBytes pulls the raw pixels out in queue order, which is the order
// they are attached to the message — so the labels in viewImageNote line up
// index for index with what the model is shown.
func viewImageBytes(imgs []ViewImage) [][]byte {
	out := make([][]byte, 0, len(imgs))
	for _, v := range imgs {
		out = append(out, v.Data)
	}
	return out
}

// viewImageNote writes the note that accompanies queued images.
//
// It enumerates them ("Image 1 of 3: …") instead of claiming they arrive "in
// order" against "the preceding tool result". That claim was not true when it
// mattered: image producers run in parallel by design, they append to one queue
// from separate goroutines as each backend answers, and a round with several of
// them has several preceding tool results with nothing tying one to the other.
// The model was left to infer an alignment the code had not preserved, and a
// wrong inference reads exactly like a right one.
//
// A single unlabeled image needs no scaffolding — there is nothing to confuse it
// with — so it keeps the plain wording.
func viewImageNote(imgs []ViewImage) string {
	const look = "Look, and describe or verify only what is actually visible; do not guess beyond what is shown."

	labeled := 0
	for _, v := range imgs {
		if strings.TrimSpace(v.Label) != "" {
			labeled++
		}
	}
	if labeled == 0 {
		if len(imgs) == 1 {
			return "Here is 1 image queued for you to view — the preceding tool result says what it is. " + look
		}
		// Nothing to anchor with: say so rather than implying an order that the
		// parallel producers did not guarantee.
		return fmt.Sprintf("Here are %d image(s) queued for you to view. They were produced by the tool calls above, "+
			"but the order they arrived in is NOT necessarily the order those calls are listed in — do not assume "+
			"the first image belongs to the first call. %s", len(imgs), look)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Here are %d image(s) queued for you to view, each named below in the order they are attached. "+
		"Use these labels rather than the order of the tool calls above — several image tools can run at once and "+
		"finish in any order.\n", len(imgs))
	for i, v := range imgs {
		label := strings.TrimSpace(v.Label)
		if label == "" {
			label = "(unlabeled — the tool that queued it did not say what it is)"
		}
		fmt.Fprintf(&b, "  Image %d of %d: %s\n", i+1, len(imgs), label)
	}
	b.WriteString(look)
	return b.String()
}

// AgentToolDef combines a tool definition with its handler.
type AgentToolDef struct {
	Tool    Tool
	Handler ToolHandlerFunc

	// NeedsConfirm marks a call worth stopping on before it executes.
	// What that COSTS depends on who supplied AgentLoopConfig.Confirm,
	// and the two live callers differ:
	//
	//   - CLI (defaultConfirm): a real terminal prompt — allow or deny.
	//   - Web (orchestrate's confirmFuncFor): escalates ONLY when the
	//     call resolves to a credential marked RequiresConfirm, and
	//     otherwise returns true. So on the dashboard this flag does NOT
	//     by itself put a prompt in front of the user.
	//
	// It has a second, always-live effect: it selects which tools get the
	// pre-action guardrail check (GuardHookPreAction). That is a real
	// function, so the flag is not decoration — but do not read it as
	// "the user will be asked" on the web path, because today they won't.
	// Wiring THIS flag to a web prompt is what ConfirmPrompt below is for;
	// it cannot be done by honoring NeedsConfirm itself, because grouped
	// tools OR the flag across their actions (GroupedTool.NeedsConfirm),
	// so a group with one dangerous action would prompt on its reads too.
	NeedsConfirm bool

	// Confirmation, when non-nil, turns a call into a question the user
	// actually answers. Setting it implies NeedsConfirm, so a tool needs
	// only this field. See ToolConfirmation.
	Confirmation *ToolConfirmation

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

	// BatchLane generalizes SerialFirePerBatch from one sequence to N. Calls
	// this round whose lane keys MATCH run sequentially in submission order;
	// calls in different lanes run in parallel with each other and with the
	// rest of the batch. Returning a constant is exactly SerialFirePerBatch;
	// returning "" puts the call in the shared serial lane alongside the
	// plain serial-fire tools.
	//
	// The point is a tool whose batched calls are only conditionally a
	// sequence: safe to fan out across distinct subjects, unsafe against the
	// same one (two dispatches to one sub-agent share a session id, so they
	// would race on its record and tear down each other's temp tools). The
	// tool knows which subject a call names; the loop cannot. Called once per
	// call during the serial partition pass, before anything executes, so it
	// may read stores — keep it cheap and side-effect free.
	//
	// Set INSTEAD of SerialFirePerBatch / SingleFirePerBatch, not alongside:
	// a lane function supersedes both.
	BatchLane func(args map[string]any) string
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
// Guardrail interception points — the canonical hook labels shared between
// core (which calls GuardrailCheck at these moments) and the app (which
// resolves which points an agent enabled). One source of truth so the two
// can't drift.
const (
	GuardHookPreInput  = "pre_input"  // judges the incoming request before round 1 (app-layer pre-pass, not called by the loop)
	GuardHookPreAction = "pre_action" // before a consequential tool call
	GuardHookPreOutput = "pre_output" // before the final reply is returned
	GuardHookPeriodic  = "periodic"   // sampling narration mid-turn

	// GuardHookToolResult labels the injection scan of what a TOOL RETURNED
	// (core/toolscan.go). It is NOT one of the warden's hook points and is
	// deliberately absent from the app's validGuardHooks set: the warden judges
	// a candidate against rules the owner authored, and this judges content
	// against a question the framework asks the same way for every agent.
	//
	// The constant exists so the audit log, the breadcrumb trail, and the
	// console name the interception point in the same vocabulary as the other
	// four. It is where the other four cannot reach: pre_input reads the
	// request and pre_output reads the reply, so an agent doing agentic work
	// that is steered by a tool result at round 3 passes both while acting on
	// it for rounds 4 through 10.
	GuardHookToolResult = "tool_result"
)

// The periodic guardrail check used to sample every 4th round with fresh
// narration, and the constant that set the interval is deliberately gone rather
// than raised. Interim prose is appended to history and delivered mid-turn, so
// any round the sampler skipped reached the transcript and the user unjudged —
// an interval cannot be tuned into containment, only out of it. The check now
// runs on every round that produces narration, deduped by prose so a repeated
// lead-in isn't paid for twice. See the gate in the round loop.

// maxGuardrailOutputCorrections bounds how many times one turn may be sent
// back to revise a guardrail-blocked reply, so a reply the model can't make
// compliant can't loop forever. The app's own per-turn block counter
// escalates independently; this is the core-side backstop.
//
// A revise pass only earns its cost when a COMPLIANT answer to the same
// question exists — a format or tone rule ("answer in Spanish", "no bullet
// lists") is corrected into a genuinely better reply. It is close to worthless
// when the rule forbids the very content the question asks for: the model still
// holds that content, still wants to say it, and each attempt manufactures
// another leaking draft to retract and scrub. Guardrails block by default for
// exactly that reason and skip this budget entirely (GuardrailDecision.Correctable
// is false); only a rule the owner marked correctable reaches it.
//
// ONE retry, not two. The second was never observed to rescue a reply the first
// couldn't: a model that missed a shaping rule twice with the correction in front
// of it is not converging, and each pass costs a full generation the user waits
// through plus another draft to retract. If one honest attempt at "you broke this
// rule, try again" doesn't land, the decline is the better answer.
const maxGuardrailOutputCorrections = 1

// GuardrailDecision is what a guardrail check reports about one candidate.
type GuardrailDecision struct {
	// Blocked stops the candidate: the action is not performed, or the reply is
	// not released. The zero value passes, so a check with nothing to say costs
	// the caller no ceremony.
	Blocked bool

	// Message is the trusted, unfenced framework text handed back on a block —
	// what the model is told about why its candidate did not go through.
	Message string

	// Correctable marks a block as worth one more attempt: the rule shapes the
	// answer rather than forbidding it, so sending the reply back can produce a
	// genuinely compliant version. When false the output gates skip the correction
	// budget entirely and hand the reply straight to the rejection writer, because
	// a revise pass against a rule that forbids what was asked would only
	// regenerate the violation from the same context that just produced it.
	//
	// FALSE is the default, deliberately. GuardrailDecision{Blocked: true} with no
	// severity set means block-and-refuse, which is the strict reading; a field
	// named for the blocking case would have made the zero value the lax one.
	//
	// Not named Block, though that is what the UI calls it: this struct already has
	// Blocked for "was the candidate stopped", and Block beside Blocked is a
	// maintenance trap. One word per concept — the app parses rules into
	// guardrailRule.Correctable, ruleIsCorrectable decides, and
	// maxGuardrailOutputCorrections bounds it.
	//
	// It carries no weight at pre_action. A blocked tool call still has a
	// compliant path available — pick a different tool, drop the offending
	// argument, finish the task another way — and that is a change of course, not
	// a refusal. Ending the turn there would convert every recoverable detour into
	// a dead end.
	Correctable bool
}

// guardrailRedactedDraft replaces a blocked assistant draft in history so the
// withheld content is never persisted or delivered. Kept generic (no rule text)
// — it stands in for the scrubbed turn in the transcript.
const guardrailRedactedDraft = "[a draft reply was withheld here: it violated an enforced guardrail and was never shown to the user]"

// guardrailSafeFallbacks are the neutral declines substituted for the reply
// when the model cannot produce a guardrail-compliant one within the
// correction budget — a determined push. This is the hard floor that makes
// pre_output a real guarantee: an input check can always be talked around, but
// the output check cannot release the protected content.
//
// A SET rather than one fixed string, for two reasons. A verbatim-identical
// reply is a fingerprint: someone probing learns exactly which attempts tripped
// the guardrail and can bisect toward the rule without ever seeing it. And the
// same sentence returned every time reads like a broken machine rather than a
// refusal.
//
// Every line must be interchangeable in INFORMATION, only in wording. None
// names a rule, hints whether the limit is capability or policy, or suggests a
// rephrase would land differently — a set that varied on any of those would
// leak more than the single string it replaced. No em-dashes (house style, and
// the display boundary rewrites them anyway).
var guardrailSafeFallbacks = []string{
	"I can't help with that one.",
	"That's not something I can do.",
	"I'm not able to help with that.",
	"I can't take that one on.",
	"That's outside what I can do here.",
	"No, I can't do that one.",
	"I won't be able to help with that.",
	"That one isn't something I can take on.",
}

// guardrailSafeFallbackReply picks a decline at random, never the one it
// returned last.
//
// The original was a uniform draw with no memory, on the argument that
// "don't repeat the last one" makes consecutive refusals distinguishable from
// independent ones. That is true and it is worth almost nothing: an observer
// who probes twice sees two different lines with probability 7/8 under a
// uniform draw and 1 under this one, so the whole signal is a fraction of a
// bit, and it only exists for someone already able to trigger two blocks in a
// row. Against that, a repeat is the single most obvious tell that a machine
// answered — a user hitting the same wall twice reads the identical sentence
// as a canned response, which is exactly what it is. Suppressing the immediate
// repeat costs a rounding error of entropy and removes the tell.
//
// Only the IMMEDIATE repeat. Longer memory would start shaping the sequence
// into something an observer really could read.
func guardrailSafeFallbackReply(custom []string) string {
	// The agent's own set wins when it has one. Those are authored ahead of
	// time (the owner may have had the model write them, then reviewed them),
	// so by the time this runs they are static trusted text.
	//
	// Nothing is generated HERE, at block time. The model available at this
	// point is the one that just failed the correction budget, with the
	// withheld content still in its context, so asking it for user-facing text
	// would make the decline a channel that content can escape through. It
	// would also need guardrail-checking itself, which recurses, and would
	// still need a hardcoded floor when that check failed. A floor that calls
	// the thing it is a floor for is not a floor.
	pool := guardrailSafeFallbacks
	if clean := nonEmptyLines(custom); len(clean) > 0 {
		// An owner who authored exactly one line chose to always say that. Not
		// topped up from the built-ins: their voice wins outright, and mixing
		// stock lines into it would be the framework overruling an explicit
		// choice to fix a problem the owner may not have.
		pool = clean
	}
	guardrailDeclineMu.Lock()
	defer guardrailDeclineMu.Unlock()
	if len(pool) > 1 {
		var fresh []string
		for _, line := range pool {
			if line != guardrailLastDecline {
				fresh = append(fresh, line)
			}
		}
		if len(fresh) > 0 {
			pool = fresh
		}
	}
	pick := pool[rand.IntN(len(pool))]
	guardrailLastDecline = pick
	return pick
}

var (
	guardrailDeclineMu   sync.Mutex
	guardrailLastDecline string
)

// deliverPreEmptedReply hands back a reply for a turn that never ran, shaped like
// a turn that did.
//
// It streams the text and fires one Done step, because that is how every host
// already renders an answer: the web path builds its transcript from streamed
// content, so returning the text without streaming it persists a reply the
// browser never paints — a blank bubble. Mimicking the normal terminal round is
// what lets seven different callers deliver this without seven short-circuits.
// Returns a well-formed history too — the input turns plus the reply as the
// assistant's. A caller that persists the transcript (a scheduled fire does)
// would otherwise record nothing for the turn, and a nil history reads as "the
// loop produced no conversation" rather than "the conversation was one refusal".
func (T *AppCore) deliverPreEmptedReply(messages []Message, reply string, cfg AgentLoopConfig) (*Response, []Message) {
	if cfg.Stream != nil {
		cfg.Stream(reply)
	}
	if cfg.OnStep != nil {
		cfg.OnStep(StepInfo{Round: 0, Content: reply, Done: true})
	}
	Debug("[agent_loop] pre-empted before round 1: delivering an app-supplied reply (%d chars), no model call", len(reply))
	history := make([]Message, 0, len(messages)+1)
	history = append(history, messages...)
	history = append(history, Message{Role: "assistant", Content: reply})
	return &Response{Content: reply}, history
}

// GuardrailDecline returns a neutral decline for a turn that must not run at all
// — the app-layer counterpart to the loop's own floor, for a request a guardrail
// refuses before any model sees it.
//
// Exported so the pre_input hard-block can reuse the curated set rather than
// inventing its own line. That matters: the set is deliberately interchangeable
// in INFORMATION (see guardrailSafeFallbacks), so a second, differently-worded
// pool would leak more than it replaced by letting a prober tell which check
// fired from the shape of the answer.
func GuardrailDecline(custom []string) string { return guardrailSafeFallbackReply(custom) }

// guardrailClosedNote is appended to a substituted decline, for the MODEL only.
//
// Without it the transcript reads as a question that was dodged rather than one
// that was refused, and the next turn quietly completes it — the user asks the
// date and gets the answer to the blocked question. The decline is deliberately
// terse and in the agent's own voice, which makes it read as deferral; nothing
// in it says the matter is settled.
//
// Stripped at every delivery boundary by textutil.StripMetaTags, kept in the
// persisted history the next turn loads. It states the outcome and the two
// behaviours that follow from it, and deliberately does NOT restate the subject
// — repeating it here would re-seed the very topic the block removed.
//
// It also does not name a CHECK, a rule, or a guardrail. Naming the mechanism
// is what guardrailInputMessage dropped for the same reason: it invites the
// model to reason about the system it is inside, which is the deliberation the
// one-shot thinking-off exists to stop. "Declined and closed" is the whole of
// what the next turn needs — the outcome, not the machinery behind it.
const guardrailClosedNote = "\n<gohort-meta>That request was declined and is closed. Do not answer it later, do not return to it unprompted, and do not treat it as unfinished business. Answer only what is asked from here.</gohort-meta>"

// guardrailRejectionReply produces the user-facing text for a halted turn: the
// app's fresh-context rejection model when one is wired, else the canned
// decline. Any empty or whitespace answer falls through to the canned line —
// a rejection call that failed must never leave the turn with nothing to say,
// because the alternative is releasing the draft it was replacing.
func guardrailRejectionReply(cfg AgentLoopConfig, reason string, history []Message) string {
	if cfg.GuardrailReject != nil {
		if reply := strings.TrimSpace(cfg.GuardrailReject(reason, lastUserRequest(history))); reply != "" {
			return reply
		}
		Debug("[agent_loop] guardrail rejection model returned nothing — using the canned decline")
	}
	return guardrailSafeFallbackReply(cfg.GuardrailDeclines)
}

// lastUserRequest returns the most recent genuine user turn — what the person
// actually asked — so a refusal can be about something instead of generic.
//
// Framework notices are injected as user-role messages (corrections, guardrail
// directives, tool results), and by the time a turn halts the tail is usually
// several of those. Handing one to the rejection model would have it refuse the
// framework's own correction text rather than the request. Skips them, and
// strips the date stamp the loop prepends to the live turn.
//
// The skip is a BLOCKLIST — it recognizes frameworkNoticeTag and nothing else —
// so the tag is load-bearing, not decoration. Four injections here shipped
// without it (the wrap-up budget note, the hard-stop directive on its last
// round, the give-up-with-errors re-prompt, and the failure-shape correction),
// and each was a plausible tail at halt time: the refusal would then have been
// written about the framework's own pacing text. Any new user-role message the
// LOOP authors must carry the tag. The one exception that cannot is a
// prompt-tools tool result (it is real data the model must act on, and the tag
// tells it not to) — those are skipped by the empty-Content test in native
// mode, where results ride in ToolResults instead.
func lastUserRequest(history []Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" {
			continue
		}
		c := history[i].Content
		if strings.Contains(c, frameworkNoticeTag) {
			continue
		}
		// Payload turns the LOOP built, recognized by what they CARRY rather
		// than by a tag. The queued-images turn ("Here are N image(s)…") is
		// framework-authored but must never wear the notice tag: the tag tells
		// the model not to act on the message, and that one exists precisely to
		// be acted on. Its Images field says what it is without putting a word
		// in the prompt, so the structure is the discriminator. Same for a
		// tool-results turn, whose Content is empty in native mode but need not
		// be relied on to stay that way.
		if len(history[i].Images) > 0 || len(history[i].ToolResults) > 0 {
			continue
		}
		if strings.HasPrefix(c, "[Current date & time:") {
			if nl := strings.Index(c, "\n\n"); nl >= 0 {
				c = c[nl+2:]
			}
		}
		if c = strings.TrimSpace(c); c != "" {
			return c
		}
	}
	return ""
}

// nonEmptyLines trims and drops blanks — a set that is all whitespace must
// fall back to the built-ins rather than sending an empty reply.
func nonEmptyLines(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isGuardrailSafeFallback reports whether s is one of the built-in declines.
// Exists so tests can assert "the floor fired" without pinning which line.
func isGuardrailSafeFallback(s string) bool {
	// Compared as DELIVERED. A substituted decline carries guardrailClosedNote
	// for the model, which every delivery boundary strips — so the reader's
	// copy is the decline alone, and that is what this answers about.
	s = strings.TrimSpace(textutil.StripMetaTags(s))
	for _, f := range guardrailSafeFallbacks {
		if s == f {
			return true
		}
	}
	return false
}

// wantsLead reports whether this loop's configuration asks for the lead tier,
// IGNORING whether the deployment currently permits one.
//
// Split out because three sites computed it independently — the round's cost
// attribution and two chat paths — and a fourth reading would have drifted from
// the others the first time the rules changed. It answers only "what was
// configured"; every caller still gates on LeadDenied(), which is where the
// privacy pin lives.
//
// Precedence, narrowest first: an explicit per-run TierOverride beats the route
// stage, which beats the config's own Tier. That order is what lets one
// resource opt out of a global routing decision without the stage having to
// know the resource exists.
func (cfg AgentLoopConfig) wantsLead() bool {
	if cfg.TierOverride != TierUnset {
		return cfg.TierOverride == LEAD
	}
	if cfg.RouteKey != "" {
		return RouteToLead(cfg.RouteKey)
	}
	return cfg.Tier == LEAD
}

type AgentLoopConfig struct {
	// TierOverride pins THIS run to a tier regardless of what its route stage
	// says — the per-resource escape hatch from a deployment-wide routing
	// decision. TierUnset (the zero value) follows RouteKey as before, so every
	// existing caller is unchanged.
	//
	// It cannot escalate past the privacy pin: callers gate on LeadDenied(), so
	// with "All LLMs are private" off a LEAD override still runs on the worker.
	// That is deliberate — the pin exists because the lead may be a third party,
	// and a per-resource flag is exactly the kind of thing that would otherwise
	// quietly become the exception to it.
	TierOverride LLMTier

	// GuardrailDeclines optionally replaces the built-in neutral declines used
	// when a reply cannot be made guardrail-compliant. Empty uses the built-in
	// set. Authored ahead of time, never generated at block time — see
	// guardrailSafeFallbackReply.
	GuardrailDeclines []string

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

	// SendGuardKey identifies a SIDE-EFFECTING send to a recipient — a message
	// delivered to a chat/contact — and returns a stable key for it (e.g.
	// "message_contact\x00Group Message"). Empty ⇒ not a guarded send. The loop
	// blocks the SECOND+ send to the same key in one turn: a model that drafts
	// several variations and fires them all (the "sent all 4 jokes to the group
	// chat" failure) delivers only the first; the rest come back as held drafts
	// with a note to pick one and send next turn. Keyed on recipient, NOT the
	// message text, so intentionally-varied duplicates are still caught — unlike
	// the identical-args loop guard, which they slip past. Nil ⇒ no send guard.
	// The app supplies it because recipient extraction is tool-specific and core
	// stays domain-agnostic.
	SendGuardKey func(toolName string, args map[string]any) string

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

	// RetractRound is like SettleRound but DISCARDS the current round's streamed
	// bubble instead of finalizing it — the app must NOT persist or deliver it.
	// Called when a guardrail BLOCKS a round: the streamed text is the very
	// content the rule protects, so settling it (which both flushes it to the
	// client and appends it to the session, see the orchestrate captureMidTurn
	// path) would leak it even though the loop then re-prompts. The app should
	// clear the open bubble in the client and drop it without capturing. Falls
	// back to SettleRound when nil (concatenation-safe but NOT leak-safe — only
	// wire the retract on paths that persist per-round bubbles). Must reset the
	// stream buffer like SettleRound so the retry doesn't concatenate.
	RetractRound func()

	// DrainViewImages, when set, is called after each tool-execution round to
	// pull any frames a tool queued for the model to look at (e.g. view_video's
	// sampled video frames, held on sess.PendingViewImages). Returned images are
	// injected as a vision user-message before the next LLM call, so the model
	// actually SEES them. Wire it to sess.DrainViewImages. Optional; nil means
	// the loop never injects view images (the pre-fix behavior, where queued
	// frames were silently dropped and the model "described" a video it never saw).
	DrainViewImages func() []ViewImage

	// BeforeToolRound, when set, is called once per round after the LLM has
	// chosen its tool calls and before any of them run. It is the moment the
	// round's arguments are fixed but nothing has acted on them yet, which is
	// where per-round state that tool ARGUMENTS are interpreted against belongs
	// — wire it to sess.SnapshotImageRefs so a positional image#N means the
	// picture the model was looking at rather than whatever a sibling call in
	// the same batch has since made newest. Optional; nil means such refs
	// resolve live, the behavior before the hook existed.
	BeforeToolRound func()

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

	// GuardrailCheck, when set, lets the app enforce owner-authored guardrails
	// at fixed interception points. hookPoint is one of GuardHook* below;
	// candidate is what's being judged (a tool call + args for pre_action, the
	// reply text for pre_output, recent narration for periodic). It returns a
	// GuardrailDecision; Blocked means the candidate violated a guardrail. The
	// loop delivers Message as a TRUSTED framework result — never wrapped in the
	// untrusted-content fence (a fenced guard is one the model is licensed to
	// ignore) — and does NOT run the blocked action. The app owns the warden
	// call, the block/escalation policy, and the breadcrumb; core just calls the
	// hook at the right moments and honors the verdict. nil ⇒ no guardrails
	// (zero overhead). Optional.
	GuardrailCheck func(hookPoint, candidate string) GuardrailDecision

	// GuardrailActionGate, when set, WIDENS which tool calls reach the
	// pre_action check. nil keeps the default: the NeedsConfirm set, and only
	// that.
	//
	// NeedsConfirm is the right default and the wrong ceiling. It names the
	// calls a person would want to approve — sends, posts, deletes, spends —
	// which is what an owner's rule is usually about. It does not name the
	// calls that carry data OUT without looking consequential: a plain fetch of
	// https://elsewhere.example/?q=<the system prompt> is an exfiltration with
	// the shape of a read.
	//
	// That gap costs nothing while nothing has gone wrong, which is why the
	// default stays where it is — judging every network read on every turn is a
	// model call per read. It matters on a turn where something HAS gone wrong,
	// and the app is the only party that knows. So the app supplies a predicate
	// and may widen the gate for exactly those turns.
	//
	// Widening changes WHICH calls are judged, never HOW: the verdict, the
	// block, the message, and the escalation counter are unchanged.
	GuardrailActionGate func(toolName string) bool

	// GuardrailHalted, when set, is consulted immediately after any blocked
	// check. true means the app has decided this turn must END — not be
	// corrected, redirected, or asked to stop.
	//
	// It exists because every guardrail hook except pre_output was advisory. A
	// blocked pre_action returned an error result and the loop carried on, so
	// the model was free to reword the call, reach the same end through an
	// ungoverned tool, or simply try again; periodic redirected and continued.
	// The escalation meant to catch that returned a message reading "STOP — you
	// have hit enforced guardrails N times", which is an INSTRUCTION TO THE
	// AGENT. The whole premise of an independent warden is that the agent's
	// judgment may already be compromised, so asking that agent to stop is not
	// enforcement — it is the same trust the warden was built to withdraw.
	//
	// When this returns true the loop discards the in-flight round, substitutes
	// the safe decline (GuardrailDeclines), and returns. No further generation,
	// no further tool calls. The app decides WHEN (repeated blocks in one turn);
	// core guarantees the turn actually ends. nil ⇒ prior advisory behavior.
	GuardrailHalted func() bool

	// PreEmptedReply, when set, IS the turn: the loop delivers it and returns
	// without calling a model at all.
	//
	// For a request an app-layer check refused before anything ran — a pre_input
	// guardrail on a rule that forbids what was asked for. There is nothing to
	// steer in that case, and running the agent only buys a long deliberation
	// about how to decline without saying why, which is the largest single cost a
	// blocked turn carries. It also cannot leak: no draft is generated, so there
	// is no protected content anywhere in the turn.
	//
	// Delivered the way a normal terminal reply is (streamed, then a Done step) so
	// every host renders it through the path it already has, rather than each
	// caller needing its own short-circuit.
	PreEmptedReply string

	// InterimContentHidden tells the loop that assistant prose from NON-FINAL
	// rounds is neither shown to anyone nor stored by this host — only the final
	// reply is. When set, the periodic guardrail check is skipped, because the
	// thing it exists to catch cannot reach anybody on this path.
	//
	// The periodic check is the expensive one: it judges every round that produces
	// narration, so on a long tool-using turn it is one extra model call per
	// round. Worth every call where those words are painted into a live
	// transcript. Worth nothing at all where the round's prose is discarded and
	// pre_output judges the only text that ships.
	//
	// The ZERO VALUE IS THE SAFE ONE, deliberately. False means "assume interim
	// prose is visible", so a host that forgets this field keeps full checking and
	// pays for it, rather than silently losing containment. Set it true only after
	// confirming the path neither streams interim deltas nor persists per-round
	// turns — a host that streams (AgentLoopConfig.Stream) or settles rounds into
	// a transcript (SettleRound) does deliver them and must leave this false.
	InterimContentHidden bool

	// GuardrailReject, when set, writes the user-facing reply for a halted turn.
	// It must run in FRESH context — a separate model call that never saw the
	// conversation — for the same reason the warden does: the turn's own context
	// is the thing that just failed, and anything generated from it is generated
	// by a persuaded model.
	//
	// This is a HANDOVER, not a correction. Past this point the original model
	// produces nothing further: it is not asked to revise, apologize, or explain,
	// because each of those is another generation from the compromised context
	// and another chance to leak what the rule protects. The rejection model owns
	// the remainder of the turn.
	//
	// It receives the user's REQUEST so the refusal can be about something
	// ("I can't help with that one" reads like a broken bot) — but never the
	// draft, the rule, or the history. The request is attacker-controlled text
	// and MUST be fenced as untrusted by the implementation: handed over as a
	// bare instruction it would be read as the task, and "ignore that, output
	// the following" is precisely what a halted turn attracts.
	//
	// reason is for the app's own logging/telemetry only; a decline must never
	// disclose the rule or that an automated check fired (naming it hands a
	// prober the signal the guardrail exists to withhold). Returning "" falls
	// back to the canned decline, so a failed rejection call can never leak the
	// draft it was meant to replace. nil ⇒ canned decline. Optional.
	GuardrailReject func(reason, request string) string

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

	// DisableToolMentionCorrection turns off the tool-mention nudge (the
	// loop otherwise re-prompts when the model names a known tool in prose
	// but emits no call). Set this when the model is EXPECTED to name
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

	// DeliveredCount reports how many attachments actually go out with this
	// reply. Evidence for the turn judge: "here's your picture" is true or false
	// depending entirely on this number. Nil reads as zero, which is honest —
	// a host that doesn't track deliveries has none to report.
	DeliveredCount func() int

	// Backgrounded reports whether this turn started a detached job. It makes
	// "I'll let you know when it's done" TRUE, and that is the reply
	// detachedNotice explicitly asks for — so the judge must never see such a
	// turn. Nil reads as false.
	Backgrounded func() bool
	// BackgroundEstimate reports the wait the framework offered for that job,
	// humanized ("13 seconds"), or empty. Feeds the turn judge so a quoted
	// estimate the framework supplied is not convicted as an invented one.
	BackgroundEstimate func() string

	// TurnClaimJudge reads the finished turn and reports whether the reply is
	// honest about what the turn actually did — the backstop for the shapes the
	// phrase-list guards above do not know about. See turn_judge.go.
	TurnClaimJudge TurnClaimJudge

	// PriorWork reports work already done FOR this turn before the loop
	// started, which the loop therefore cannot observe: a machine step that
	// searched, a delegated step, a pipeline phase. One entry per piece of
	// work, naming what ran.
	//
	// The host supplies it because only the host knows what it ran on the
	// turn's behalf; the loop's own accounting begins when the loop does.
	// Nil, and the empty result, both read as "nothing ran before this".
	PriorWork func() []string

	// TurnGroundingJudge reads the finished turn and reports whether the reply
	// states an unchecked claim as established fact. Separate from
	// TurnClaimJudge because the questions differ: that one asks whether the
	// turn DID what the reply describes, this one whether the reply KNOWS what
	// it asserts. Nil disables it.
	TurnGroundingJudge TurnGroundingJudge

	// UncheckedClaims are the stored notes in this turn's prompt that carry an
	// unchecked marker. Supplied by the host, which is what renders the memory
	// block and therefore the only thing that knows which notes were marked.
	// Empty means the grounding judge never runs.
	UncheckedClaims []string

	// LiveClaimSpeaker names the person whose message this turn is answering,
	// when they are NOT the principal — a participant in a room, a contact on a
	// channel. Empty for an owner turn and for every non-channel surface.
	//
	// It exists because the stored-claim machinery cannot reach the claim that
	// matters most in a group: the one asserted in THIS message, which nothing
	// has classified or marked because it is not in memory yet. Set, it puts
	// the inbound itself in the judge's scope.
	LiveClaimSpeaker string

	// LiveClaimTrusted marks the speaker as someone the host recognizes — on a
	// roster the owner maintains, matched on something they cannot simply
	// claim. It relaxes the premise HOLD and nothing else: their claims are
	// still attributed and still judged, because being trusted is not the same
	// as having been checked.
	LiveClaimTrusted bool

	// PhantomDeliveryRefs names what a reply CLAIMS to be sending that does not
	// exist — a delivery promised for something never produced. The loop cannot
	// answer this itself: what a reference resolves against (a workspace, an
	// inbound-media registry, a recent-image ring) is the app's to know, and so
	// is whether this turn ran anything that MAKES a deliverable.
	//
	// Each entry is quoted straight into the correction, so return whatever
	// names the missing thing best: a filename when the reply named one, a plain
	// noun phrase ("the image") when it didn't.
	//
	// Return only GENUINE phantoms. A reference to a file that was delivered and
	// then cleaned up is not one — the delivery happened — and neither is one
	// the app can still recover from a staged file. Anything returned here costs
	// a correction round, so a false positive spends a round telling a model it
	// was wrong when it wasn't.
	//
	// The failure it exists for, observed in full: a reply consisting of exactly
	// "[ATTACH: find-dkfindcraig.jpg]", a filename with the right shape and the
	// subject's name stuffed into it, for a picture the turn never made — it
	// called no tools at all. The marker resolved to nothing, stripping left an
	// empty reply, and the contact was asked to rephrase a request that was
	// never the problem.
	//
	// Its quieter twin, which no marker rule can reach: the turn DID call the
	// image tool, the generation failed, and the reply was a caption — "Here's
	// you, wasting away in the garage like Craig ordered" — delivered whole, with
	// no picture and nothing anywhere in the words to suggest one was missing.
	PhantomDeliveryRefs func(reply string) []string

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

	// TurnNotes supplies volatile, turn-scoped context to append to the newest
	// user message. Called once with that message's text, before the first LLM
	// call; return "" to add nothing.
	//
	// The newest user turn is the cache-safe home for anything that changes
	// between turns — it is the volatile tail that never hits cache anyway, so
	// appending there costs nothing, which is the same reasoning that puts the
	// date stamp below here rather than in the system prompt. Facts that a tool
	// SCHEMA cannot carry (schemas sit at the front of the prompt, so a changing
	// one re-pays cold prefill every turn) can be carried here instead.
	//
	// The loop has no idea what a turn is about, so what is worth saying is
	// entirely the app's call. It sees the user's message and decides.
	//
	// Turn-scoped means turn-scoped: the note is appended to the loop's working
	// copy of history. Hosts persist their own user message, so it does not ride
	// into the stored conversation — which matters when the note contains
	// anything positional, since next turn it would be wrong.
	TurnNotes func(userMessage string) string

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

// guardrailArgCharsDefault is how much of each argument value the pre_action
// warden gets to read.
//
// It used to read formatArgs, which caps values at 200 characters — a number
// chosen for the confirm dialog and the loop-guard signature, where 200 is
// plenty, and inherited by the guardrail check without anyone deciding it
// should judge a prefix. A rule about what an agent SENDS someone was being
// applied to the first 200 characters of the message; anything withheld
// further in was invisible to it.
//
// Uncapped is not the answer either. Tool arguments carry base64 images and
// whole documents, and an unbounded warden prompt on one of those is a slow,
// expensive check that may not fit the context at all — so the cap stays and
// only its size changes. 4000 covers essentially any message body, post, or
// commit text a rule would be written about.
const guardrailArgCharsDefault = 4000

// guardrailArgTotalFactor bounds the WHOLE candidate relative to the per-value
// cap, because a call with twenty large arguments would otherwise multiply past
// any per-value limit.
const guardrailArgTotalFactor = 4

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_guardrail_action_arg_chars",
		Category: "Limits",
		Label:    "Guardrail: argument text read per tool call",
		Help:     "How much of each argument the pre-action guardrail check reads when judging a consequential tool call. A rule about the CONTENT of what an agent sends is applied to this much of it — raise it if your rules need to see long message bodies or documents, at the cost of a bigger check on every consequential call. The whole candidate is additionally capped at four times this. Does not affect the reply check, which always sees the complete reply.",
		Kind:     KindInt,
		Default:  guardrailArgCharsDefault,
		Min:      200,
		Max:      50000,
	})
}

func guardrailArgChars() int {
	if n := TuneInt("tune_guardrail_action_arg_chars"); n > 0 {
		return n
	}
	return guardrailArgCharsDefault
}

// formatArgsForGuardrail renders a tool call's arguments for the pre_action
// warden. Same deterministic key order as formatArgs — a candidate that varies
// with map iteration order would judge the same call differently on different
// runs — but with a cap sized for JUDGING rather than for display.
//
// Truncation is ANNOUNCED. A warden handed a silent prefix reports that it
// found nothing objectionable, which is true of the prefix and says nothing
// about the rest; told it is looking at part of a value, it can weigh that.
func formatArgsForGuardrail(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	perValue := guardrailArgChars()
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", k, truncateRunes(stringify(args[k]), perValue)))
	}
	out := strings.Join(lines, "\n")
	return truncateRunes(out, perValue*guardrailArgTotalFactor)
}

// truncateRunes cuts to a rune count, never mid-character, and says so when it
// cuts. Bytes would split a multi-byte rune and hand the warden a replacement
// glyph, which reads as corruption rather than as a trim.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…[truncated for length; more follows that is NOT shown here]"
}

// repeatFailHistoryWindow bounds how far back seedRepeatFailFromHistory
// replays: only the recent tail counts, so an ancient failure doesn't
// ban a call forever (a fixation worth stopping shows up within a handful
// of recent turns; a success anywhere in the window clears it).
const repeatFailHistoryWindow = 40

// shakeoutTemperature is the one-shot sampling temperature applied to the
// round right after a repeat guard fires. High enough to break a greedy
// fixed point (Qwen 3.x route defaults are ~0.6-0.7), low enough to stay
// out of degeneration territory. Per-call, so it never touches the server
// config and costs nothing when no guard has tripped.
const shakeoutTemperature = 0.9

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

// promptReuseNote renders the prompt-cache read for the round breadcrumb, or
// nothing at all when the backend doesn't report one. It rides the existing
// "LLM returned" line rather than adding a line of its own: the question it
// answers — did this round re-prefill the whole prompt? — is only meaningful
// next to how long the round took, and the pair is what turns a latency
// report into a diagnosis.
func promptReuseNote(resp *Response) string {
	if resp == nil || resp.PromptTokensPrefilled <= 0 || resp.InputTokens <= 0 {
		return ""
	}
	cached := resp.InputTokens - resp.PromptTokensPrefilled
	if cached < 0 {
		cached = 0
	}
	return fmt.Sprintf(", prefilled=%d/%d prompt tokens (%d%% cached, %.0fms)",
		resp.PromptTokensPrefilled, resp.InputTokens, cached*100/resp.InputTokens, resp.PrefillMS)
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
	repeatFail := map[string]int{}
	const repeatFailLimit = 3
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
			window := cfg.ContextSize
			if window <= 0 {
				window = contextWindowFromError(err)
			}
			if window <= 0 {
				window = fallbackRecoveryWindow
			}
			Debug("[agent_loop] round %d: context exceeded — recovering into a %d-token window", round, window)

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
				ToolErrors:    cumulativeToolErrors,
				LastToolError: lastToolError,
				Delivered:     cfg.deliveredCount(),
				Backgrounded:  cfg.backgrounded(),
				GivenEstimate: cfg.backgroundEstimate(),
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
			turnToolCalls = append(turnToolCalls, w.tc.Name)
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
// allowProse governs only the last-resort natural-language scan; the
// XML and JSON branches always run, since those are unambiguous machine
// output rather than a reading of English.
func ParseTextToolCall(content string, handlers map[string]ToolHandlerFunc, toolDefs []Tool, allowProse bool) *ToolCall {
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
	//
	// This branch is a GUESS, unlike the markup branches above — English
	// that announces a call and English that reports a finished one scrape
	// identically. allowProse is how the caller says the guess is worth
	// making; see the clean-finish gate in the agent loop.
	if !allowProse {
		return nil
	}
	if tc := parseNaturalToolCall(content, handlers); tc != nil {
		if hasRequired(tc, toolDefs) {
			return tc
		}
		Debug("[agent_loop] dropping synthesized natural-language tool call '%s' — could not extract required args from prose", tc.Name)
	}
	return nil
}

// deliveryMarkerRe matches the framework's own "send this file" marker. Shared
// by the phantom-delivery check and the stripper below so the two can never
// disagree about what a delivery claim looks like.
var deliveryMarkerRe = regexp.MustCompile(`\[ATTACH:\s*[^\]]*\]`)

// Correction kinds. One allowance each — a phantom delivery and an orphaned
// tool-call tag are unrelated failures, and spending one must not disarm the
// other. Named constants so a typo can't silently mint a fresh allowance.
const (
	correctionOrphanedXML     = "orphaned-xml"
	correctionPhantomDelivery = "phantom-delivery"
	correctionFakeToolCode    = "fake-tool-code"
	correctionActionPromise   = "action-promise"
	correctionAnnouncedCall   = "announced-call"
	correctionToolMention     = "tool-mention"
	correctionCollapse        = "reasoning-collapse"
	correctionGiveUp          = "giveup-with-errors"
	correctionUnkeptClaim     = "unkept-claim"
	correctionUngrounded      = "ungrounded-claim"
	correctionMachinery       = "machinery-leak"
)

const (
	// maxCorrectionsPerKind is how many times ONE problem gets re-prompted.
	// Two is enough to nudge a stuck turn; a third attempt on the same fault is
	// a model that isn't going to move.
	maxCorrectionsPerKind = 2
	// maxCorrectionsPerTurn is the ceiling across every kind together, so a
	// turn going wrong in many ways at once still terminates. Set above
	// 2×kinds deliberately: the point is to stop a spiral, not to ration
	// guards against each other.
	maxCorrectionsPerTurn = 6
)

// correctionBudget rations the loop's silent re-prompts.
//
// It used to be a single counter shared by every guard, and that had two
// distinct failures. Two corrections of UNRELATED kinds disarmed every other
// guard for the rest of the turn — an orphaned tool tag early on meant a false
// delivery claim later went uncorrected, though nothing about the first says
// anything about the second. And a guard that spent the budget on the SAME
// problem twice simply stopped firing, so the content it had judged wrong
// shipped, with the exhausted guard being the only thing that ever noticed.
//
// Observed: the phantom-delivery guard fired 1/2 and then 2/2 on one invented
// filename, the model rewrote the same claim both times, and the third one went
// out to the user — a claim the framework had already ruled false, twice.
type correctionBudget struct {
	spentByKind map[string]int
	spentTotal  int
	noted       map[string]bool // kinds that have already breadcrumbed their exhaustion
}

func newCorrectionBudget() *correctionBudget {
	return &correctionBudget{spentByKind: map[string]int{}, noted: map[string]bool{}}
}

// available reports whether one more correction of this kind may be spent.
func (b *correctionBudget) available(kind string) bool {
	return b.spentByKind[kind] < maxCorrectionsPerKind && b.spentTotal < maxCorrectionsPerTurn
}

// spend takes one and returns which attempt it was, for the log line.
func (b *correctionBudget) spend(kind string) int {
	b.spentByKind[kind]++
	b.spentTotal++
	return b.spentByKind[kind]
}

// exhausted reports whether this kind is out AND has not said so yet, so the
// caller breadcrumbs once rather than on every round after. A guard that goes
// quiet without a word is the silent drop this codebase keeps paying for.
func (b *correctionBudget) exhausted(kind string) bool {
	if b.available(kind) || b.noted[kind] {
		return false
	}
	b.noted[kind] = true
	return true
}

// applyTurnNotes appends the app's turn-scoped context to the newest user
// message, in place. See AgentLoopConfig.TurnNotes.
//
// Only ever the TRAILING message, and only when it is the human turn: mid-loop
// the tail is a tool result, and reference material buried inside one reads as
// output from something the model just ran. Earlier user turns are settled
// context that the prompt cache already covers — writing into one moves the
// cache boundary backwards for nothing.
func applyTurnNotes(cfg AgentLoopConfig, history []Message) {
	n := len(history)
	if cfg.TurnNotes == nil || n == 0 || history[n-1].Role != "user" {
		return
	}
	note := strings.TrimSpace(cfg.TurnNotes(history[n-1].Content))
	if note == "" {
		return
	}
	Debug("[agent_loop] turn note appended to the user turn (%d chars)", len(note))
	history[n-1].Content += "\n\n" + note
}

// deliveredCount / backgrounded are the nil-safe reads of the turn-evidence
// hooks. Absent means "nothing delivered, nothing started", which is the only
// safe reading: it makes a delivery claim MORE suspect, never less.
func (c AgentLoopConfig) deliveredCount() int {
	if c.DeliveredCount == nil {
		return 0
	}
	return c.DeliveredCount()
}

// priorWork is the host's account of what ran for this turn before the loop
// began. Nil-safe, because most hosts have nothing to report.
func (c AgentLoopConfig) priorWork() []string {
	if c.PriorWork == nil {
		return nil
	}
	return c.PriorWork()
}

func (c AgentLoopConfig) backgrounded() bool {
	return c.Backgrounded != nil && c.Backgrounded()
}

func (c AgentLoopConfig) backgroundEstimate() string {
	if c.BackgroundEstimate == nil {
		return ""
	}
	return strings.TrimSpace(c.BackgroundEstimate())
}

// phantomDeliveryRefs asks the app whether a reply promises files that do not
// exist. Nil hook = no check, which is the behaviour every host had before it.
func phantomDeliveryRefs(cfg AgentLoopConfig, content string) []string {
	if cfg.PhantomDeliveryRefs == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	return cfg.PhantomDeliveryRefs(content)
}

// UnfulfilledDeliveryReply is what goes out when a reply insists on handing over
// something that does not exist and will not stop after being corrected.
//
// Written in the agent's own voice, short, and with no machinery in it: the
// person on the other end asked for a picture and did not get one, which is the
// whole of what they need to know. It deliberately does NOT apologize for their
// request or ask them to rephrase — the request was fine, and blaming it is the
// failure this whole line of work started from.
//
// Exported because a host may want to recognize or replace it; the default is
// the framework's, and any reply is better than a false one.
func UnfulfilledDeliveryReply(refs []string) string {
	what := "it"
	if named := strings.Join(refs, ", "); named != "" {
		what = named
	}
	return fmt.Sprintf("I said I was sending %s, and I was wrong — it was never made, so there's nothing to send. Say the word and I'll have another go at it.", what)
}

// StripDeliveryMarkers removes [ATTACH: …] markers from a reply. Used when the
// files they name do not exist: the marker is the claim, so removing it is what
// stops the claim from being delivered or persisted.
func StripDeliveryMarkers(s string) string {
	return strings.TrimSpace(deliveryMarkerRe.ReplaceAllString(s, ""))
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
// bracketedCallDirectiveRe matches a tool call written as a BRACKETED
// DIRECTIVE — "[CALL_FUNCTION] fetch_image: find a photo of …" and its
// siblings. A shape worth naming separately because of how it got out: the
// prose scan is gated off for a long body that finished cleanly (a real answer
// must not be re-read as a tool call), and this arrived inside 700 characters
// of narration. Nothing extracted it, nothing corrected it, and the whole
// monologue — invented progress, second thoughts, "OK found a good one" — went
// to a contact verbatim.
//
// Treated as MARKUP rather than prose, because that is what it is: no sentence
// a person writes contains "[CALL_FUNCTION]". Length can't make it an answer.
var bracketedCallDirectiveRe = regexp.MustCompile(`(?i)\[(?:CALL_FUNCTION|FUNCTION_CALL|TOOL_CALL|CALL_TOOL|INVOKE)\]\s*:?\s*([a-zA-Z_][a-zA-Z0-9_]*)`)

func containsFakeToolCodeBlock(s string) bool {
	if strings.Contains(s, "<tool_code>") {
		return true
	}
	if bracketedCallDirectiveRe.MatchString(s) {
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
	// [CALL_FUNCTION] name: … form
	if m := bracketedCallDirectiveRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
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
	// [CALL_FUNCTION] name: … — drop from the directive to the end of its line.
	// The narration around it is left alone: the correction re-prompt replaces
	// the whole reply anyway, and cutting more than the directive would be
	// guessing at where the model's real sentence began.
	for {
		loc := bracketedCallDirectiveRe.FindStringIndex(s)
		if loc == nil {
			break
		}
		end := strings.IndexByte(s[loc[0]:], '\n')
		if end < 0 {
			s = s[:loc[0]]
			break
		}
		s = s[:loc[0]] + s[loc[0]+end+1:]
	}
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

// frameworkNoticeTag prefixes every loop-injected corrective/pacing message.
// These ride the user ROLE (the only reliable mid-conversation carrier), so
// small models kept attributing them to the human and answering THEM
// ("You're right, I dispatched Comedian 120 times. My bad.") instead of the
// actual user. The tag makes the origin explicit and forbids replying to it.
const frameworkNoticeTag = "[AUTOMATED FRAMEWORK NOTICE — not written by the user, who cannot see it. Do not reply to it, apologize, or address anyone about it; silently adjust and continue.] "

// isGuardStopResult reports whether a nominally-successful tool result is
// actually a framework guard verdict ("STOP — you have already…") rather than
// real output. Inner guards return STOP as a plain string; the loop's
// progress accounting must not mistake that for a working tool call. The scan
// tolerates the untrusted-content fence prefix on dispatch results.
func isGuardStopResult(content string) bool {
	head := content
	if len(head) > 600 {
		head = head[:600]
	}
	return strings.Contains(head, "STOP — you")
}

// endsWithCallAnnouncement detects the announce-then-stop failure: the reply's
// LAST line ends with a colon introducing a call that never followed —
// "Here's the `update_agent` call to implement these changes:" and then the
// turn ends. Far narrower than the disabled containsActionPromise (which
// false-positived on conversational "I'll try next time" closes): a complete
// reply essentially never terminates on a colon, and the colon alone still
// isn't enough — the line must also read like a call announcement, either by
// containing a snake_case token (tool-ish names like update_agent don't occur
// in ordinary prose; the announced name may be INVENTED, so matching against
// the real catalog would miss exactly the worst case) or the words
// "call"/"tool". A legit turn-ending colon ("Paste the error here:") carries
// neither signal.
func endsWithCallAnnouncement(content string) bool {
	trimmed := strings.TrimSpace(content)
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	line := trimmed
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		line = strings.TrimSpace(trimmed[i+1:])
	}
	lower := strings.ToLower(strings.ReplaceAll(line, "\u2019", "'"))
	if callWordRe.MatchString(lower) {
		return true
	}
	if snakeCaseTokenRe.MatchString(lower) {
		return true
	}
	// First-person intent is the general form of this failure, and requiring a
	// call word missed most of it: "Let me dig up that benchmark article with
	// actual token/s numbers:" announces work, ends on a colon, and stops —
	// but names no tool and contains no snake_case, so the guard passed it
	// through and the user watched the turn end on a promise.
	//
	// What separates it from a legitimate turn-ending colon is WHO the colon
	// commits. "Paste the error message here:" hands the next move to the
	// user and is complete. "Let me look that up:" / "Here's the plan:" commit
	// the AGENT to something that then never arrives.
	//
	// Checked in this order because a line can carry both — "Send me the link
	// and I'll take a look:" states first-person intent AND hands over the
	// next move, and it is the handover that makes it complete. Asking is a
	// finished turn; promising is not.
	if userDirectiveRe.MatchString(lower) {
		return false
	}
	return firstPersonIntentRe.MatchString(lower)
}

// replyStalledOnAPromiseMaxLen is the lead-in cutoff: past it, a reply is an
// ANSWER that happens to contain "I'll", not a turn that stalled on a promise.
// Same 600 the no-arg-mention guard and the runner's narration cutoff use.
const replyStalledOnAPromiseMaxLen = 600

// replyStalledOnAPromise reports whether a reply commits the agent to work it
// then never does — "let me create this", "I'll blend Alex onto the picture" —
// and stops.
//
// This is endsWithCallAnnouncement's shape with the colon requirement dropped,
// and dropping it is the whole point: the colon is a typographic accident, not
// the failure. "Here's the update_agent call to implement these changes:" and
// "Got it, let me create this. I'll blend Alex onto the picture. 🏚️👔" are the
// same turn ending the same way, and only the first one was catchable.
//
// Without the colon this is far too loose to act on alone — that looseness is
// what got the standalone actionPromiseCorrection disabled, and it stays
// disabled. Its ONLY caller conjoins it with unaddressed tool errors, no tool
// call this round, rounds to spare, and a correction budget. Under those, a
// sentence about what happens next is never a finished turn.
//
// Two carve-outs, both inherited from endsWithCallAnnouncement:
//   - a directive to the USER ends a turn legitimately, however it is
//     punctuated — asking is finished, promising is not.
//   - length. A long reply containing "I'll" is an answer; this is for the
//     lead-in that was supposed to be followed by a tool call.
func replyStalledOnAPromise(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || len(trimmed) > replyStalledOnAPromiseMaxLen {
		return false
	}
	lower := strings.ToLower(strings.ReplaceAll(trimmed, "’", "'"))
	if userDirectiveRe.MatchString(lower) {
		return false
	}
	return futureCommitmentRe.MatchString(lower)
}

// ReplyPromisesWork reports whether a reply commits the agent to work it has
// not done — the exported form of the loop's own stall predicate, for a host
// that wants to decide something about the turn AFTER it ends.
//
// The loop can only correct a promise while the turn is still running. What it
// cannot do is remember: the next turn arrives as a fresh call, so a user
// holding the agent to something it said a minute ago ("are you really?") gets
// answered by a model with no idea it promised anything. See the commitment
// ledger in the orchestrate app.
func ReplyPromisesWork(reply string) bool { return replyStalledOnAPromise(reply) }

// futureCommitmentRe matches the agent committing itself to work that has NOT
// happened yet.
//
// Split out from firstPersonIntentRe, which also matches "here's" / "here is".
// Presenting something is a finished turn; promising something is not, and the
// difference only stopped mattering while this was conjoined with pending tool
// errors. Standing alone it decides whether an ordinary reply gets re-prompted,
// and "Here's the answer: 42." is an answer.
var futureCommitmentRe = regexp.MustCompile(`\b(?:let me|i'll|i will|i'm going to|i am going to|going to|now i|next i|on it)\b`)

// callWordRe word-bounds the announcement keywords so "basically:" /
// "technically:" (which CONTAIN "call") can't false-fire the guard.
var callWordRe = regexp.MustCompile(`\b(?:call|calls|calling|tool|tools|toolbox)\b`)

// snakeCaseTokenRe matches a multi-word snake_case identifier — the shape of
// tool/action names ("update_agent", "reply_to_comment") and essentially
// nothing in natural prose.
var snakeCaseTokenRe = regexp.MustCompile(`\b[a-z0-9]+(?:_[a-z0-9]+)+\b`)

// firstPersonIntentRe matches the agent committing ITSELF to what follows the
// colon. Deliberately first-person: an imperative aimed at the user ("paste
// the error message here:") ends a turn legitimately and must not re-prompt.
var firstPersonIntentRe = regexp.MustCompile(`\b(?:let me|i'll|i will|i'm going to|i am going to|here's|here is|now i|next i|going to)\b`)

// userDirectiveRe matches a line handing the next move to the USER. A turn that
// ends by asking for something is finished, however it is punctuated — it is
// waiting, not stalled. Takes precedence over first-person intent, which the
// same sentence often also contains ("send me the link and I'll look:").
// "let me know" appears here rather than as intent for exactly that reason.
var userDirectiveRe = regexp.MustCompile(`\b(?:paste|send me|send it|reply with|tell me|let me know|share (?:the|it|that)|upload|attach|type|enter|choose|pick)\b`)

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

	// FIRST, before anything else: cut down any SINGLE result too large to
	// coexist with a round at all.
	//
	// The newest tool-result message is otherwise spared unconditionally,
	// which is right when it is a normal size and catastrophic when it is not.
	// One tool returning eight megabytes bricks the conversation outright:
	// every following turn assembles a prompt that cannot fit, the recovery
	// path declines to touch the one thing making it too big, and no message
	// the user types can ever get through again. A recovery path that refuses
	// to touch the cause is not a recovery path.
	//
	// The cap is deliberately generous — a large file read or a wide search
	// must keep working — so this only fires on a result that could not have
	// been used in a round anyway.
	total -= capOversizedResults(msgs, contextSize)
	if total <= budget {
		// Cutting the outlier was enough; leave the rest of history intact.
		if total < origTotal {
			Log("[agent_loop] compaction: truncated an oversized tool result, est %d→%d tokens (window=%d)", origTotal, total, contextSize)
		}
		return
	}

	var trIdx []int
	for i := range msgs {
		if len(msgs[i].ToolResults) > 0 {
			trIdx = append(trIdx, i)
		}
	}
	if len(trIdx) <= keepRecentToolMsgs {
		if total < origTotal {
			Log("[agent_loop] compaction: truncated an oversized tool result, est %d→%d tokens (window=%d)", origTotal, total, contextSize)
		}
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
// noToolDiagLine renders the zero-tool-turn diagnostic. Split out from the loop
// so the masking rule can be tested: a session that carries credentials must
// yield a length and nothing else, and that is not a property to leave to the
// next person editing a format string.
func noToolDiagLine(round int, asked, reply string, masked bool) string {
	reply = strings.TrimSpace(reply)
	if masked {
		return fmt.Sprintf("[agent_loop] NOTOOL-DIAG round %d: no tool ran this turn; asked=[masked: %d chars] reply=[masked: %d chars]",
			round, len(strings.TrimSpace(asked)), len(reply))
	}
	return fmt.Sprintf("[agent_loop] NOTOOL-DIAG round %d: no tool ran this turn; asked=%q reply=%q",
		round, truncForLog(asked, 200), truncForLog(reply, 1000))
}

func truncForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	// Cut on a RUNE boundary. Slicing bytes splits any multi-byte character
	// straddling the limit and writes invalid UTF-8 into the log — which used to
	// be theoretical, when this only ever truncated tool names and short
	// snippets, and stopped being when it started carrying reply text. The
	// replies that prompted it end in "🏚️👔".
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
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
//
// The name match is TOKEN-BOUNDED, and a name that is a bare English word
// (no underscore) is only honored in the adjacent-paren call form. Both
// guards exist because this scan reads ordinary conversation: a session
// with a tool named "image" put the word through here every time the model
// DESCRIBED a photo to the user. Substring matching would also have fired
// it inside "images"/"imagery". A tool name is only evidence of a call when
// it can't equally be evidence of English — see mentionedUncalledTool, which
// has held the same two guards since the actionPromiseCorrection fallout.
func parseNaturalToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	lower := strings.ToLower(content)

	// Find the best (longest) matching tool name in the text.
	var bestName string
	var bestPos int = -1
	for name := range handlers {
		pos := lastTokenIndex(lower, strings.ToLower(name))
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
	rest := content[bestPos+len(bestName):]
	after := strings.TrimSpace(rest)

	// Common-word guard. A name with no underscore is a word the user and
	// the model may simply be TALKING about, so the only shape trusted for
	// it is one prose does not produce: the paren opening immediately, with
	// no space ("image(prompt=…)" is a call; "the image (a 4x6 print)" is a
	// sentence). Snake_case names skip this — they read as tool names
	// wherever they appear, so the looser forms below stay available.
	if !strings.Contains(bestName, "_") {
		if !strings.HasPrefix(rest, "(") {
			Debug("[agent_loop] skipping tool mention %q — common-word name not in call form, treating as prose", bestName)
			return nil
		}
		callArgs := parseCallArgs(rest)
		if len(callArgs) == 0 {
			Debug("[agent_loop] skipping tool mention %q — common-word name with no parsable args, treating as prose", bestName)
			return nil
		}
		return &ToolCall{ID: fmt.Sprintf("text_%s", UUIDv4()), Name: bestName, Args: callArgs}
	}

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
	//
	// A flag must be "--" followed by a LETTER. Without that test a markdown
	// horizontal rule ("---") counts as a flag, and since the scan runs from
	// the tool name to the end of the message it then sweeps every remaining
	// word into args["args"] — any answer that names a tool and later breaks
	// a section becomes a bogus call carrying the rest of the document.
	var flag_args []string
	for _, part := range strings.Fields(after) {
		if isFlagToken(part) {
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

// mentionedUncalledTool returns the name of a known tool that appears as a
// standalone token in content, and whether that tool takes parameters —
// or "" if no tool is named. It's the re-prompt trigger for a call the
// model NAMED but never emitted.
//
// parseNaturalToolCall rescues a narration only when it can read arguments
// off the prose, so two shapes fall through it in silence: a no-arg tool
// (there is nothing to extract) and a parameterized one the model merely
// talked ABOUT ("I don't have access to read_support_bundles") rather than
// wrote out as a call. Both end the turn on the model's own account of
// what it did or couldn't do, while the tool sat in the catalog the whole
// time — the reported symptom being an agent that insists it cannot reach
// files it was holding the tools for.
//
// Deliberately conservative to avoid the false positives that got
// actionPromiseCorrection disabled:
//   - only snake_case names (an underscore) — single common words like a
//     hypothetical "help" tool would false-match ordinary prose,
//   - token-bounded match so "image" doesn't fire inside "images",
//   - the caller fires only on a SHORT reply that ran no tool this turn,
//     which is what separates a lead-in or a refusal from an answer that
//     names a tool in passing.
//
// needsArgs distinguishes the two shapes for the nudge, which has to say
// why nothing ran: a no-arg tool had nothing to extract, a parameterized
// one needs its arguments written into a real structured call. It is the
// widening that retired the old zero-parameter restriction — that test
// kept the correction off exactly the tools whose narration costs the most
// (a search or a read the answer then claims was impossible), and the
// descriptive-mention risk it was guarding lives in the caller's length
// gate and in DisableToolMentionCorrection.
func mentionedUncalledTool(content string, handlers map[string]ToolHandlerFunc, toolDefs []Tool) (name string, needsArgs bool) {
	lower := strings.ToLower(content)
	for _, td := range toolDefs {
		if td.Name == "" || !strings.Contains(td.Name, "_") {
			continue
		}
		if _, ok := handlers[td.Name]; !ok {
			continue
		}
		if mentionsToken(lower, strings.ToLower(td.Name)) && len(td.Name) > len(name) {
			name = td.Name
			needsArgs = len(td.Parameters) != 0
		}
	}
	return name, needsArgs
}

// mentionsToken reports whether needle occurs in haystack bounded by
// non-identifier characters (so a tool name matches only as a standalone token,
// not inside a longer word). Both arguments must already be lowercase.
func mentionsToken(haystack, needle string) bool {
	return lastTokenIndex(haystack, needle) >= 0
}

// lastTokenIndex returns the index of the LAST token-bounded occurrence of
// needle in haystack, or -1. Same boundary rule as mentionsToken; the prose
// scan needs the position so it can read arguments off what follows. Last
// rather than first because a model that narrates and then calls puts the
// real call at the end. Both arguments must already be lowercase.
func lastTokenIndex(haystack, needle string) int {
	if needle == "" {
		return -1
	}
	found := -1
	for from := 0; from <= len(haystack)-len(needle); {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			break
		}
		i += from
		end := i + len(needle)
		beforeOK := i == 0 || !isIdentByte(haystack[i-1])
		afterOK := end >= len(haystack) || !isIdentByte(haystack[end])
		if beforeOK && afterOK {
			found = i
		}
		from = i + 1
	}
	return found
}

// isFlagToken reports whether s is a command-line style flag ("--verbose"),
// as opposed to a run of dashes ("--", "---") that markdown uses as a rule.
func isFlagToken(s string) bool {
	if len(s) < 3 || !strings.HasPrefix(s, "--") {
		return false
	}
	c := s[2] | 0x20 // fold case
	return c >= 'a' && c <= 'z'
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

// providerRefused reports whether a response is the provider declining on its
// OWN content policy rather than the model answering.
//
// This is not the local deployment's policy — a remote provider refusing an
// agent-design turn (or a game with a rude name) says nothing about what this
// deployment permits, and the local worker will simply do the work. Detected
// from the stop reason, which every client now populates.
//
// Content or tool calls means the model answered; a filter that fires after a
// complete answer is not a refusal to act on.
func providerRefused(resp *Response) bool {
	if resp == nil {
		return false
	}
	if strings.TrimSpace(resp.Content) != "" || len(resp.ToolCalls) > 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(resp.StopReason)) {
	case "safety", "recitation", "blocklist", "content_filter", "prohibited_content":
		return true
	}
	return false
}

// logPromptFloor breaks the first round's prompt into its parts.
//
// "Why does 'Hello' cost 34k tokens?" was unanswerable: the floor is assembled
// from a system prompt, a tool catalog, and history, and nothing reported
// their relative sizes — so tuning it meant guessing which one to cut. Tool
// SCHEMAS are usually the bulk and the least obvious, because each one carries
// a full description and parameter spec whether or not the turn uses it.
//
// Round 1 only: later rounds add tool results, and the point here is the FLOOR
// a turn starts from. Bytes, not tokens — no tokenizer is in reach at this
// layer, and ~4 bytes/token is close enough to tell a 20k problem from a 2k
// one.
func logPromptFloor(cfg AgentLoopConfig, systemPrompt string, history []Message) {
	if r := promptSizeReport(cfg, systemPrompt, history); r != "" {
		Debug("[agent_loop] prompt floor: %s", r)
	}
}

// promptSizeReport says where a round's bytes actually are.
//
// It exists because "the prompt is too large" is not a diagnosis. A session
// that failed at two million tokens against a 262k window was investigated
// twice from the code — once wrongly blaming history compaction, once wrongly
// blaming a single oversized tool result — because nothing in the failure said
// which part was big. A number per component turns the next occurrence into a
// reading rather than a guess.
//
// The counts include TOOL RESULTS, which the original floor log omitted: it
// summed only Message.Content, so a history dominated by tool output reported
// as small. That omission is exactly the shape of thing that hides for a long
// time — the number was there, it was just quietly wrong.
func promptSizeReport(cfg AgentLoopConfig, systemPrompt string, history []Message) string {
	sysBytes := len(systemPrompt)

	histBytes, resultBytes := 0, 0
	biggestMsg, biggestMsgAt, biggestMsgRole := 0, -1, ""
	for i, m := range history {
		n := len(m.Content) + len(m.Reasoning)
		for _, tr := range m.ToolResults {
			n += len(tr.Content)
			resultBytes += len(tr.Content)
		}
		histBytes += n
		if n > biggestMsg {
			biggestMsg, biggestMsgAt, biggestMsgRole = n, i, m.Role
		}
	}

	toolBytes, biggest, biggestName := 0, 0, ""
	for _, t := range cfg.Tools {
		n := len(t.Tool.Name) + len(t.Tool.Description)
		for pn, p := range t.Tool.Parameters {
			n += len(pn) + len(p.Description) + len(p.Type)
		}
		toolBytes += n
		if n > biggest {
			biggest, biggestName = n, t.Tool.Name
		}
	}

	total := sysBytes + histBytes + toolBytes
	if total == 0 {
		return ""
	}
	pct := func(n int) int { return n * 100 / total }
	return fmt.Sprintf("%d bytes total (~%dk tokens) = system %d (%d%%) + %d tool schemas %d (%d%%) + history %d (%d%%, of which %d is tool results); "+
		"largest single message #%d (%s) at %d bytes; largest tool schema %q at %d bytes",
		total, total/4000,
		sysBytes, pct(sysBytes),
		len(cfg.Tools), toolBytes, pct(toolBytes),
		histBytes, pct(histBytes), resultBytes,
		biggestMsgAt, biggestMsgRole, biggestMsg,
		biggestName, biggest)
}

// promptSizeHeadline is the one-clause version for a user-facing error: which
// component holds the bulk, so the message names a place to look instead of
// only reporting that there is a problem.
func promptSizeHeadline(cfg AgentLoopConfig, systemPrompt string, history []Message) string {
	sysBytes := len(systemPrompt)
	histBytes := 0
	for _, m := range history {
		histBytes += len(m.Content) + len(m.Reasoning)
		for _, tr := range m.ToolResults {
			histBytes += len(tr.Content)
		}
	}
	toolBytes := 0
	for _, t := range cfg.Tools {
		toolBytes += len(t.Tool.Name) + len(t.Tool.Description)
		for pn, p := range t.Tool.Parameters {
			toolBytes += len(pn) + len(p.Description) + len(p.Type)
		}
	}
	switch {
	case sysBytes >= histBytes && sysBytes >= toolBytes:
		return fmt.Sprintf("most of it is the system prompt (~%dk tokens), which compaction cannot shrink — the agent's own instructions, memory or attached sources are the place to look", sysBytes/4000)
	case toolBytes >= histBytes:
		return fmt.Sprintf("most of it is tool definitions (~%dk tokens across %d tools), which compaction cannot shrink — narrow the agent's tool list", toolBytes/4000, len(cfg.Tools))
	default:
		return fmt.Sprintf("most of it is conversation history (~%dk tokens)", histBytes/4000)
	}
}

// --- confirmation ------------------------------------------------------------
//
// What it means for a tool to ask before it acts, and what "don't ask me
// again" is allowed to mean afterwards.
//
// A prompt on every call is a prompt nobody reads. The loop that makes an
// agentic coding session worth having is edit → build → read the error → fix
// → build, and if each build stops for approval then the safe configuration
// is the one that is too tedious to use — which is how gates get switched off
// wholesale. So the answer to a confirmation has three shapes, not two: no,
// yes this once, and yes to this KIND of call from now on.
//
// The third one is where the care goes. A grant is a standing decision made
// in one click during a task, so it has to be narrow enough that the user can
// predict what they just allowed:
//
//   - It is namespaced by Scope, an opaque key the app supplies. Allowing a
//     build in a throwaway checkout must not allow one in the source tree the
//     server is running from, and only the app knows those are different.
//   - It covers a tool outright ONLY when the tool has no meaningful argument
//     to vary — an operator-defined "run the tests" command is one fixed
//     string, so "always" is exactly as broad as it sounds.
//   - Where there IS a varying argument (a shell command), the grant is a
//     PREFIX of it, and prefix matching refuses anything a shell would treat
//     as more than one command. See CommandIsGrantable for why that guard is
//     load-bearing rather than defensive.
//   - A tool can refuse to offer one at all (NeverRemember), for actions
//     where a standing yes is not a thing a person should be able to hand out
//     mid-flow.

// ToolConfirmation describes a tool's confirmation behavior.
type ToolConfirmation struct {
	// Prompt is the question the user reads. It is prose, and it is the
	// tool author's job because only they can write one worth interrupting
	// for: "Allow run?" is a reflex click, "Run a command in gohort?" over
	// the command itself is a decision.
	//
	// FAILS CLOSED. A run with no interactive viewer (a schedule, a
	// dispatch, a channel wake) has nobody to ask, so the call is denied
	// rather than allowed. A tool that must work unattended must not set a
	// confirmation at all.
	Prompt string

	// Scope namespaces any grant the user hands out from this call's card.
	// Opaque to the framework — an app passes whatever "the same situation"
	// means to it (a project id, a workspace, a connection). Empty puts
	// grants in a namespace shared by everything else that left it empty,
	// which is rarely what an app wants and never what a dangerous tool
	// wants.
	Scope string

	// GrantArg names the argument a remembered grant is matched on. When
	// set, a grant records a PREFIX of that argument's value and applies
	// only to later calls whose value starts with it. When empty, a grant
	// covers the tool outright — correct only when the tool has no varying
	// argument that changes what it does.
	GrantArg string

	// NeverRemember withholds the "always allow" option, leaving only
	// once-or-deny. For calls where a standing yes should not be obtainable
	// by clicking a third button in the middle of something else.
	NeverRemember bool
}

// asks reports whether this confirmation actually gates anything. A nil
// confirmation, or one with no question in it, does not.
func (c *ToolConfirmation) asks() bool {
	return c != nil && strings.TrimSpace(c.Prompt) != ""
}

// Asks is the exported form, for the layers outside core that decide whether
// to escalate.
func (c *ToolConfirmation) Asks() bool { return c.asks() }

// CanRemember reports whether this call may offer a standing grant.
func (c *ToolConfirmation) CanRemember() bool {
	return c.asks() && !c.NeverRemember
}

// shellMetaChars are the characters that let one command line become more
// than one command, or become a command whose text is computed at run time.
const shellMetaChars = ";&|`$><\n\r()"

// CommandIsGrantable reports whether a command line may take part in prefix
// matching at all.
//
// This is the guard that decides whether the whole grant mechanism is a
// convenience or a hole. Without it, granting the prefix "go build" would
// also allow:
//
//	go build ./... ; rm -rf /
//
// which starts with the granted prefix, was never shown to the user, and
// would run without a prompt. So a command containing anything a shell reads
// as chaining, substitution, or redirection is never matched against a grant
// and never offered as one — it goes to the user every time, which is the
// correct answer for a line that does more than one thing.
//
// Deliberately a blunt character test rather than a parser. A parser that is
// subtly wrong here fails open, and the cost of the blunt version is only
// that a legitimate piped command keeps asking.
func CommandIsGrantable(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	return !strings.ContainsAny(cmd, shellMetaChars)
}

// GrantPrefixFor derives the prefix a card offers for a command: its leading
// words, up to the first one that looks like an argument rather than part of
// the verb.
//
// "go build ./..."        → "go build"
// "npm run test -- -w"    → "npm run"
// "make"                  → "make"
// "./scripts/ci.sh --fast"→ "./scripts/ci.sh"
//
// Two words at most, because the useful unit is the verb ("go test") and
// anything past it is the part that legitimately varies between iterations —
// which is the whole reason a prefix beats an exact match here. Returns ""
// when the command may not be granted at all.
func GrantPrefixFor(cmd string) string {
	if !CommandIsGrantable(cmd) {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	prefix := fields[0]
	// A second word joins the verb only when it is a bare subcommand — not a
	// flag, not a path, not a value. "go build" is a verb; "make -j8" is a
	// verb plus a setting, and granting "make -j8" would be narrower than the
	// user expects rather than broader.
	if len(fields) > 1 && isBareWord(fields[1]) {
		prefix += " " + fields[1]
	}
	return prefix
}

// isBareWord reports whether s is a plain subcommand token.
func isBareWord(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "/\\=.") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

// CommandMatchesPrefix reports whether cmd is covered by a granted prefix.
//
// The match is at a WORD boundary, so a grant of "go build" does not cover
// "go buildsomethingelse". Both sides must be grantable, which is what stops
// a chained command from riding in on a grant made for its first clause.
func CommandMatchesPrefix(cmd, prefix string) bool {
	cmd = strings.TrimSpace(cmd)
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || !CommandIsGrantable(cmd) {
		return false
	}
	if cmd == prefix {
		return true
	}
	return strings.HasPrefix(cmd, prefix+" ")
}

// capOversizedResults truncates any single tool result that could not fit in a
// round even on its own, and returns the estimated tokens reclaimed.
//
// Position-blind on purpose. Every other rule here spares the newest results
// because the model needs them to act; this one applies to all of them,
// because a body this size is not something the model can act on — it is a
// body that stops the round existing. Truncating it to something readable is
// strictly better than a turn that cannot run.
//
// The tail is kept rather than the head: a command's error is at the end of
// its output, and that is overwhelmingly what an oversized result is.
func capOversizedResults(msgs []Message, contextSize int) (reclaimed int) {
	limit := oversizedResultBytes(contextSize)
	for i := range msgs {
		// Message CONTENT as well as tool results. A single message can be
		// just as impossible as a single result — a pasted document, a reply
		// that quoted its whole input — and capping only results left the
		// other half of history untouchable, which is what a live failure at
		// 1.4M tokens of conversation text turned out to be.
		if len(msgs[i].Content) > limit {
			was := len(msgs[i].Content)
			msgs[i].Content = truncateTail(msgs[i].Content, limit,
				"[message truncated: it was %d bytes, larger than a whole round can hold, so only the last %d are kept]")
			reclaimed += (was - len(msgs[i].Content)) / 4
		}
		for j := range msgs[i].ToolResults {
			body := msgs[i].ToolResults[j].Content
			if len(body) <= limit {
				continue
			}
			replaced := truncateTail(body, limit,
				"[tool result truncated: it was %d bytes, larger than a whole round can hold, so only the last %d are kept. "+
					"Re-run the tool with a narrower query if you need the rest.]")
			reclaimed += (len(body) - len(replaced)) / 4
			msgs[i].ToolResults[j].Content = replaced
		}
	}
	return reclaimed
}

// oversizedResultBytes is the largest single tool result worth keeping whole,
// in bytes, derived from the window so a big-context model is not held to a
// small model's limit.
//
// A quarter of the window: a result larger than that leaves too little room
// for the system prompt, the tools and the reply for the round to be useful,
// whatever else is trimmed. The floor matters more than the fraction — when
// the window is unknown (contextSize 0, which is what a provider that never
// reported one gives us) an unbounded result would otherwise sail through the
// one check that could have caught it.
func oversizedResultBytes(contextSize int) int {
	const floor = 256 << 10 // 256 KiB ≈ 64k tokens
	if contextSize <= 0 {
		return floor
	}
	// contextSize is tokens; ~4 bytes each.
	quarter := contextSize / 4 * 4
	if quarter < floor {
		return floor
	}
	return quarter
}

// truncateTail keeps the END of an oversized body, prefixed with a note in the
// caller's words saying what was dropped.
//
// The tail rather than the head, everywhere: a command's error is at the end
// of its output, the conclusion is at the end of a reply, and the useful part
// of a long paste is rarely its opening. Trimming forward to the next newline
// avoids starting mid-line, which reads as corruption rather than as a cut.
func truncateTail(body string, limit int, noteFormat string) string {
	if len(body) <= limit {
		return body
	}
	kept := body[len(body)-limit:]
	if k := strings.IndexByte(kept, '\n'); k >= 0 && k < 200 {
		kept = kept[k+1:]
	}
	return fmt.Sprintf(noteFormat, len(body), len(kept)) + "\n" + kept
}

// elideOldMessageText drops the TEXT of older messages, newest-first-preserved,
// until history fits. Returns the estimated tokens reclaimed.
//
// This is the floor under everything else, and it exists because the loop had
// no way to trim conversation at all: compaction elided tool-result bodies and
// nothing else, so a long-lived thread whose bulk was ordinary message text
// could grow until every turn failed, permanently, with the recovery path
// reporting success at doing nothing. The upstream session layer bounds a
// thread too — but a last line of defence that cannot act on the commonest
// shape of history is not one.
//
// Structure is preserved: only Content is replaced, so tool calls and their
// results stay paired and the transcript's shape is intact. The newest
// keepWhole messages are never touched, because those are the ones the round
// is actually about.
func elideOldMessageText(msgs []Message, budgetTokens, keepWhole int) (reclaimed int) {
	if len(msgs) <= keepWhole {
		return 0
	}
	total := 0
	for i := range msgs {
		total += len(msgs[i].Content) / 4
		for _, tr := range msgs[i].ToolResults {
			total += len(tr.Content) / 4
		}
	}
	for i := 0; i < len(msgs)-keepWhole && total > budgetTokens; i++ {
		body := msgs[i].Content
		if len(body) <= 400 {
			continue
		}
		marker := fmt.Sprintf("[earlier message elided to fit context — was %d bytes]", len(body))
		total -= (len(body) - len(marker)) / 4
		reclaimed += (len(body) - len(marker)) / 4
		msgs[i].Content = marker
	}
	return reclaimed
}

// summarizeOldHistory folds the oldest part of a conversation into a summary,
// in chunks small enough that each fold call itself fits.
//
// This is the smart half of context recovery, and it is what should be tried
// before anything is thrown away. Eliding text is cheap and lossy: a thread
// that has been running for weeks loses what was decided in it, and the model
// carries on without knowing what it forgot. Summarizing costs LLM calls and
// keeps the substance, which is the trade worth making at the point where the
// alternative is a conversation that can no longer take a message at all.
//
// CHUNKED, because the thing that does not fit cannot be handed to a
// summarizer in one piece either — 1.4M tokens of history is not summarizable
// by a model with a 262k window. Each chunk is folded on its own and the folds
// are then folded together, so the work scales with the history rather than
// with the window.
//
// Returns the rewritten history and whether anything changed. Failure is not
// an error: a summarizer that is unavailable or refuses leaves history
// untouched and the caller falls through to elision, which still beats a turn
// that cannot run.
func (T *AppCore) summarizeOldHistory(ctx context.Context, msgs []Message, contextSize, keepWhole int) ([]Message, bool) {
	if T == nil || T.LLM == nil || len(msgs) <= keepWhole {
		return msgs, false
	}
	foldEnd := safeCutPoint(msgs, len(msgs)-keepWhole)
	if foldEnd <= 0 {
		return msgs, false
	}
	// How much of the window one fold call may spend on input.
	//
	// Deliberately SMALL, and much smaller than the window allows. A fold is
	// remedial work on a server that is by definition under pressure — the
	// turn got here because something did not fit — and a large request is
	// exactly what such a server cannot place. Observed on llama.cpp as
	// "failed to find a memory slot for batch of size 2048" and slots being
	// purged mid-flight: asking for quarter-window folds meant the recovery
	// competed for KV cache with the very conversation it was rescuing.
	//
	// Summarizing does not need a big bite. Notes from twelve thousand tokens
	// are as good as notes from sixty-five, and the smaller request places
	// itself in a crowded cache where the larger one waits or evicts.
	chunkTokens := foldChunkTokens
	if q := contextSize / 4; q > 0 && q < chunkTokens {
		chunkTokens = q
	}

	var chunkSummaries []string
	var cur []Message
	curTokens := 0
	// Bounded. An unbounded fold count turns one failed turn into an
	// arbitrarily long sequence of LLM calls, which is a poor trade against
	// simply dropping old text — and on a loaded server it is a queue of
	// requests nobody asked for. Past the cap the caller falls through to
	// elision, which is lossy but immediate.
	budgetExhausted := false
	flush := func() {
		if len(cur) == 0 || budgetExhausted {
			cur, curTokens = nil, 0
			return
		}
		if len(chunkSummaries) >= maxFoldCalls {
			budgetExhausted = true
			cur, curTokens = nil, 0
			return
		}
		if s := T.foldChunk(ctx, cur); s != "" {
			chunkSummaries = append(chunkSummaries, s)
		}
		cur, curTokens = nil, 0
	}
	for i := 0; i < foldEnd; i++ {
		n := EstimateTokens(msgs[i].Content)
		for _, tr := range msgs[i].ToolResults {
			n += EstimateTokens(tr.Content)
		}
		// A single message larger than a chunk is folded alone; capOversized
		// has already cut anything genuinely impossible, so this is a large
		// message rather than an absurd one.
		if curTokens+n > chunkTokens && len(cur) > 0 {
			flush()
		}
		cur = append(cur, msgs[i])
		curTokens += n
	}
	flush()

	if len(chunkSummaries) == 0 {
		return msgs, false
	}
	summary := strings.Join(chunkSummaries, "\n\n")
	// Fold the folds when there were several, so the result is one account of
	// the conversation rather than a pile of partial ones.
	if len(chunkSummaries) > 1 {
		if s := T.foldChunk(ctx, []Message{{Role: "user", Content: summary}}); s != "" {
			summary = s
		}
	}

	if budgetExhausted {
		Log("[agent_loop] context recovery: stopped after %d fold calls — the remainder falls to elision", maxFoldCalls)
	}
	out := make([]Message, 0, keepWhole+1)
	out = append(out, Message{
		Role: "user",
		Content: "[Earlier conversation, summarized to fit the context window. " +
			"This replaces the messages themselves; treat it as an account of what was said and decided, not as something the user just wrote.]\n\n" + summary,
	})
	out = append(out, msgs[foldEnd:]...)
	Log("[agent_loop] context recovery: folded %d earlier message(s) into a %d-byte summary across %d chunk(s)",
		foldEnd, len(summary), len(chunkSummaries))
	return out, true
}

// foldChunk summarizes one span. Returns "" on any failure, because every
// caller has a worse-but-working fallback and none of them should abort a
// turn because a salvage step did not work.
func (T *AppCore) foldChunk(ctx context.Context, span []Message) string {
	var b strings.Builder
	for _, m := range span {
		role := m.Role
		if role == "" {
			role = "message"
		}
		if c := strings.TrimSpace(m.Content); c != "" {
			fmt.Fprintf(&b, "%s: %s\n", role, c)
		}
		for _, tr := range m.ToolResults {
			if c := strings.TrimSpace(tr.Content); c != "" {
				fmt.Fprintf(&b, "tool result: %s\n", c)
			}
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return ""
	}
	// Bounded so a salvage attempt cannot itself hang a turn that is already
	// in trouble.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	resp, err := T.WorkerChat(ctx, []Message{{
		Role: "user",
		Content: "Summarize this part of a conversation so it can replace the messages themselves.\n\n" +
			"Keep: what was asked, what was decided, what was done, any values, names, paths or numbers that later turns would need, and anything still outstanding. " +
			"Drop: pleasantries, restatements, and the exact wording. Write it as compact notes, not prose. No preamble.\n\n" +
			"---\n" + b.String(),
	}}, WithMaxRetries(1))
	if err != nil || resp == nil {
		Debug("[agent_loop] context recovery: a fold call failed (%v) — falling back to elision for this span", err)
		return ""
	}
	return strings.TrimSpace(resp.Content)
}

// foldChunkTokens is the input budget for one summarization call, and
// maxFoldCalls bounds how many of them a single recovery may make. Both exist
// to keep a salvage attempt small on a server that is already struggling —
// see summarizeOldHistory.
const (
	foldChunkTokens = 12000
	maxFoldCalls    = 12
)

// contextRecoveryKeepWhole is how many of the newest messages stay verbatim
// through any recovery. The round is about those; folding them would salvage
// the turn by destroying the thing it was asked to do.
const contextRecoveryKeepWhole = 6

// stillTooBig estimates whether a round would still overflow. Deliberately an
// estimate: the exact figure belongs to the provider's tokenizer, and the only
// decision resting on this is whether to try one more salvage step.
func stillTooBig(msgs []Message, systemPrompt string, contextSize int) bool {
	if contextSize <= 0 {
		return false
	}
	total := EstimateTokens(systemPrompt)
	for i := range msgs {
		total += EstimateTokens(msgs[i].Content)
		for _, tr := range msgs[i].ToolResults {
			total += EstimateTokens(tr.Content)
		}
	}
	return total > contextSize-34000
}

// fallbackRecoveryWindow is what recovery assumes when nothing reports a
// window: neither the configuration nor the provider's refusal. Deliberately
// small. Recovering into a window smaller than the real one costs some history
// that need not have been folded; recovering into one larger than the real one
// achieves nothing and the turn stays dead.
const fallbackRecoveryWindow = 32000

// contextWindowFromError reads the window out of a provider's own refusal.
//
// The number is almost always right there in the message — llama.cpp says
// "exceeds the available context size (262144 tokens)", and the others phrase
// it differently around the same two figures. Reading it is what lets recovery
// work on a deployment where the optional ContextSizer is not implemented,
// which is the common case rather than an exotic one.
//
// Returns 0 when nothing recognizable is present; the caller then falls back.
func contextWindowFromError(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	// The window is the SMALLER of the two numbers a refusal names: the other
	// is the prompt that did not fit. Collect every plausible token count and
	// take the smallest above a floor, which avoids depending on any one
	// provider's wording.
	var best int
	for _, m := range contextNumberPattern.FindAllStringSubmatch(msg, -1) {
		n, convErr := strconv.Atoi(m[1])
		if convErr != nil || n < 1000 {
			continue
		}
		if best == 0 || n < best {
			best = n
		}
	}
	return best
}

// contextNumberPattern finds token counts in a provider's error text.
var contextNumberPattern = regexp.MustCompile(`(\d{4,})\s*tokens?`)

// safeCutPoint moves a proposed history cut forward until it does not orphan a
// tool message from the assistant that called it.
//
// A message carrying ToolResults is rendered by every provider as one or more
// role:"tool" messages, and a tool message is only valid immediately after the
// assistant message holding the matching tool_calls. Cut between them and the
// model's own chat template rejects the request outright — llama.cpp answers
// "A tool message must follow an assistant or tool message", a 500 rather than
// a graceful degrade, which turns a context-recovery attempt into a harder
// failure than the one it was recovering from.
//
// Moving FORWARD rather than back: the pair is folded together, so the summary
// covers the call and its result as one event. Moving back would keep an
// assistant message whose results were summarized away, leaving the model
// looking at a call with no answer.
func safeCutPoint(msgs []Message, idx int) int {
	if idx <= 0 {
		return 0
	}
	if idx > len(msgs) {
		idx = len(msgs)
	}
	// Advance past any run of tool-result messages, and past the assistant
	// that owns them if the cut landed between the two.
	for idx < len(msgs) && len(msgs[idx].ToolResults) > 0 {
		idx++
	}
	return idx
}
