package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

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
