package core

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
// of Messages — content AND the tool-result bodies hanging off it.
//
// The bodies are the point. A message's own content is a sentence; a tool
// result on it can be a hundred kilobytes, and a conversation's whole weight
// routinely lives there rather than in anything anybody typed. This counted
// content alone once, and the reading it produced ("history 4,635 tokens over
// 22 messages") was off by a factor of thirty on a thread the loop's own
// compaction measured at 151k — which is the sort of wrong that sends two
// fixes at the wrong layer before anyone doubts the instrument.
//
// Message-role overhead is still ignored: negligible at the scales any caller
// here cares about.
func EstimateMessagesTokens(msgs []Message) int {
	total := 0
	for i := range msgs {
		total += estimateMessageTokens(msgs[i])
	}
	return total
}

// estimateMessageTokens estimates one message: its content plus every tool
// result carried on it. The single definition of "how big is a message",
// shared by history compaction and by EstimateMessagesTokens, so two numbers
// about the same conversation cannot disagree. Unexported — callers outside
// core size whole histories, never one message.
func estimateMessageTokens(m Message) int {
	n := len(m.Content) / 4
	for _, tr := range m.ToolResults {
		n += len(tr.Content) / 4
	}
	return n
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
// It returns the history budget it computed, so a caller that wants to RECORD
// the budget reads the number this function actually used rather than
// recomputing the formula beside it. Two copies of one quantity is how a
// standing thread came to be reported at 4,635 tokens and 151,251 tokens by
// two layers of the same turn. 0 means compaction was off for this call.
func compactHistory(msgs []Message, systemPrompt string, contextSize int, force bool) int {
	if contextSize <= 0 && !force {
		return 0
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
	total := EstimateMessagesTokens(msgs)
	// Per-round breadcrumb — fires every compaction call so a long
	// session's history trajectory is visible under --debug without
	// waiting for an elision to fire the Log line below.
	Debug("[agent_loop] compaction check: history ~%d tokens, budget %d, window %d (msgs=%d)", total, budget, contextSize, len(msgs))
	if total <= budget {
		return budget
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
		return budget
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
		return budget // nothing old enough to safely elide
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
	return budget
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
	return estimatePromptTokens(msgs, systemPrompt) > contextSize-34000
}

// estimatePromptTokens is what the next call will cost: the system prompt plus
// every message body and tool result. Shared with the refusal bookkeeping below
// so "how big was the prompt we sent" and "is the prompt still too big" can
// never answer with different arithmetic.
//
// Tool SCHEMAS are not counted, so this reads low by however many the catalog
// carries (~7.5k tokens on the turn that prompted this work). That is the safe
// direction for both callers: an under-count makes stillTooBig keep cutting and
// makes a recorded ceiling stricter.
func estimatePromptTokens(msgs []Message, systemPrompt string) int {
	total := EstimateTokens(systemPrompt)
	for i := range msgs {
		total += EstimateTokens(msgs[i].Content)
		for _, tr := range msgs[i].ToolResults {
			total += EstimateTokens(tr.Content)
		}
	}
	return total
}

// recoveryWindow is the window to recover INTO after a provider has refused a
// prompt as too large: the smallest credible number available, because this is
// the one place where guessing high is useless and guessing low merely costs
// some history.
//
// refused is the estimated size of the prompt that was just rejected, and it is
// the only hard evidence in this branch — whatever the real ceiling is, it is
// below that. Recovering into anything larger is what let the whole ladder
// no-op: stillTooBig would report that the history already fits, so nothing
// after the first rung ever ran and the retry re-sent what had just been
// refused.
//
// Floored, so a refusal that has nothing to do with size can't drive recovery
// into a window too small to hold a turn at all.
func recoveryWindow(configured int, refusal error, refused int) int {
	window := configured
	if window <= 0 {
		window = contextWindowFromError(refusal)
	}
	if window <= 0 {
		window = fallbackRecoveryWindow
	}
	if refused > 0 {
		if capped := refused * 3 / 4; capped < window {
			window = capped
		}
	}
	if window < observedCeilingFloor {
		window = observedCeilingFloor
	}
	return window
}

// --- observed context ceilings ---------------------------------------------
//
// The configured window is a CLAIM. A refusal is a MEASUREMENT. Until these,
// the claim won every time, including in the branch that exists BECAUSE the
// claim was just proved wrong.
//
// What that looked like: a worker advertising a 262144-token window refused a
// prompt estimated at ~130k. Recovery read cfg.ContextSize, announced it was
// "recovering into a 262144-token window", and asked stillTooBig whether ~117k
// still didn't fit. It fit fine. So the ladder's summarize and elide rungs
// never ran, the retry sent an all-but-identical prompt, the same refusal came
// back, and the turn died — holding a window half of which it was never going
// to get. Every following turn then rebuilt a prompt against the same 262144
// and walked into the same wall.
//
// Note the shape of the evidence: on that deployment a 149,267-token prompt was
// ACCEPTED and a ~130k one refused later the same day, and refusals also hit
// ~5k-token summarizer sub-calls that could not have exhausted anything by
// themselves. So the real ceiling is not a property of our prompt at all — it
// moves with what else the worker is serving. That is what makes measuring it
// the right approach and a static number the wrong one, whatever the underlying
// cause turns out to be.

// observedCeilingTTL is how long a refusal is believed. Long enough to carry a
// conversation through the condition that caused it; short enough that a busy
// minute doesn't surrender half the window for the rest of the day.
const observedCeilingTTL = 30 * time.Minute

// observedCeilingFloor stops the feedback loop from collapsing the window to
// nothing if refusals arrive for some reason that has nothing to do with size.
// Below this an agent cannot carry a system prompt and a round of tool results,
// so clamping further would trade a failing turn for a useless one.
const observedCeilingFloor = 24000

// observedCeilings maps a configured window to the ceiling actually observed
// under it. Keyed by the configured number rather than by model because that is
// the figure loopContextSize already collapses both tiers into — the
// observation attaches to the same number the budget is derived from.
var observedCeilings sync.Map

// int (configured) → observedCeiling

type observedCeiling struct {
	tokens int       // largest prompt we are now willing to build
	at     time.Time // when the refusal was seen; the entry expires from here
}

// noteContextRefusal records that a prompt of refusedTokens was rejected as too
// large under the given configured window.
//
// Keeps three quarters of the refused size. The refusal proves the ceiling is
// BELOW that number but says nothing about how far below, and the margin also
// has to cover what estimatePromptTokens does not count (tool schemas) — so the
// recorded figure is deliberately stricter than the evidence strictly requires.
//
// The smallest refusal within the TTL wins: two refusals mean the ceiling is
// under both.
func noteContextRefusal(configured, refusedTokens int) {
	if configured <= 0 || refusedTokens <= 0 {
		return
	}
	ceiling := refusedTokens * 3 / 4
	if ceiling < observedCeilingFloor {
		ceiling = observedCeilingFloor
	}
	if ceiling >= configured {
		return // nothing learned: the refusal was already at or above the claim
	}
	if prev, ok := observedCeilings.Load(configured); ok {
		if p := prev.(observedCeiling); time.Since(p.at) < observedCeilingTTL && p.tokens <= ceiling {
			observedCeilings.Store(configured, observedCeiling{tokens: p.tokens, at: time.Now()})
			return
		}
	}
	Log("[agent_loop] context ceiling: a %d-token prompt was refused under a configured %d-token window — building to %d for the next %s",
		refusedTokens, configured, ceiling, observedCeilingTTL)
	observedCeilings.Store(configured, observedCeiling{tokens: ceiling, at: time.Now()})
}

// effectiveContextSize is the window to actually build against: the configured
// one, or the smaller ceiling a recent refusal measured.
//
// Expired observations are dropped rather than merely ignored, so a worker that
// stops refusing gets its full window back on its own — no restart, no setting.
func effectiveContextSize(configured int) int {
	if configured <= 0 {
		return configured
	}
	v, ok := observedCeilings.Load(configured)
	if !ok {
		return configured
	}
	obs := v.(observedCeiling)
	if time.Since(obs.at) >= observedCeilingTTL {
		observedCeilings.Delete(configured)
		return configured
	}
	if obs.tokens < configured {
		return obs.tokens
	}
	return configured
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
