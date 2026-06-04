// Side storage for async dispatch results.
//
// When a chat agent dispatches to a specialist (OSINT, research, etc.),
// the raw report can be sizable — keeping it in chat history wastes
// prompt budget across every subsequent turn. Instead, store the raw
// under a short ID, pass a worker-LLM-generated SUMMARY back into the
// chat loop as the synthetic input, and expose a recall_dispatch_result
// tool the chat LLM can use to fetch the raw if the user asks for more.
//
// Storage shape:
//   key: <chat_id>:<dispatch_id>
//   val: dispatchResult{Agent, Brief, Raw, Summary, Created}
//
// Retention: capped at maxDispatchResultsPerChat per chat. Oldest
// dispatches age out when the cap is exceeded. No time-based expiry —
// a user can ask about an older dispatch within the chat lifecycle.

package phantom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	dispatchResultsTable        = "phantom_dispatch_results"
	maxDispatchResultsPerChat   = 20    // cap retained results per chat; older ones get pruned
	dispatchSummaryMaxTokens    = 350   // summary length (~250 words) — fits in one iMessage with room for chat LLM wrapping
	dispatchSummaryTimeout      = 90 * time.Second
)

// dispatchResult is one stored async-dispatch outcome.
type dispatchResult struct {
	ID      string    `json:"id"`       // short hash; surface to the LLM as the recall handle
	ChatID  string    `json:"chat_id"`  // which chat owns this result
	Agent   string    `json:"agent"`    // dispatched agent's name/id
	Brief   string    `json:"brief"`    // original dispatch brief — useful for the LLM to know what was asked
	Raw     string    `json:"raw"`      // full report (retrieved by recall_dispatch_result)
	Summary string    `json:"summary"`  // worker-LLM summary used in the synthetic input
	Created time.Time `json:"created"`
}

// dispatchResultKey returns the kvlite key for a stored result.
func dispatchResultKey(chatID, id string) string {
	return chatID + ":" + id
}

// newDispatchResultID makes a short stable ID for a dispatch result.
// Uses a hash of (chatID, agent, brief, timestamp) — collisions are
// unlikely at our scale, and a recurring brief gets distinct IDs per
// invocation thanks to the nanosecond timestamp.
func newDispatchResultID(chatID, agent, brief string) string {
	now := time.Now().UnixNano()
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", chatID, agent, brief, now)))
	return hex.EncodeToString(h[:6]) // 12 hex chars — short enough to type back, plenty unique
}

// summarizeDispatchResult condenses the raw report into a compact
// summary suitable for the chat LLM's synthetic input. Worker tier,
// no tools, terse output. On failure returns the first ~1200 chars
// of raw as a heuristic fallback so the LLM still has SOMETHING to
// reply with.
func summarizeDispatchResult(llm LLM, agentName, brief, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Short reports pass through — no point burning a worker round
	// to summarize something already iMessage-sized.
	if len(raw) <= 800 {
		return raw
	}
	if llm == nil {
		return truncateForSummaryFallback(raw, 1200)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dispatchSummaryTimeout)
	defer cancel()

	sys := "You compact agent dispatch reports into iMessage-sized summaries. " +
		"The chat persona will use your summary to compose a natural reply to the user. " +
		"Preserve: concrete facts (names, addresses, dates, numbers, URLs), the headline answer to the user's question, anything that DIRECTLY addresses the brief. " +
		"Drop: meta-commentary, methodology, hedging, repeated information, source-evaluation language. " +
		"Output a tight summary of ~250 words (no bullets, no headers, no preamble). If the report says \"nothing found\" or similar, just say that plainly."

	user := fmt.Sprintf(
		"Brief that was dispatched: %q\nAgent: %s\n\nReport to compact:\n\n%s",
		brief, agentName, raw,
	)
	resp, err := llm.Chat(ctx,
		[]Message{{Role: "user", Content: user}},
		WithSystemPrompt(sys), WithMaxTokens(dispatchSummaryMaxTokens),
	)
	if err != nil || resp == nil {
		Log("[phantom/dispatch_summary] worker LLM failed: %v — falling back to truncation", err)
		return truncateForSummaryFallback(raw, 1200)
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return truncateForSummaryFallback(raw, 1200)
	}
	return out
}

// truncateForSummaryFallback returns the first N chars of raw plus a
// short "trimmed" note. This text is delivered DIRECTLY to the user (the
// dispatch direct-return path), so the note must read for THEM — never
// name an internal tool. The LLM still learns it can pull the full report
// from the [#dispatch:<id>] marker appended to chat history, not from
// this line; an earlier version leaked "via recall_dispatch_result" into
// the user's message whenever the worker summarizer fell back here.
func truncateForSummaryFallback(raw string, n int) string {
	if len(raw) <= n {
		return raw
	}
	return strings.TrimSpace(raw[:n]) + "\n\n(Trimmed for length — say the word if you want the full details.)"
}

// scrubInternalScaffolding strips any internal dispatch markers from text
// that's about to be delivered DIRECTLY to the user. The async dispatch
// path delivers the summary with no LLM re-render, so there's no model in
// the loop to drop these — this is the safety net. Today only the
// trailing [#dispatch:<id>] marker is structured-internal (it's appended
// to STORED history, not delivered text, so this should be a no-op in the
// normal path); kept defensive so a future change that accidentally
// delivers the stored variant can't leak the marker to a user's message.
func scrubInternalScaffolding(text string) string {
	for {
		open := strings.Index(text, "[#dispatch:")
		if open < 0 {
			break
		}
		end := strings.IndexByte(text[open:], ']')
		if end < 0 {
			break
		}
		text = text[:open] + text[open+end+1:]
	}
	return strings.TrimSpace(text)
}

// storeDispatchResult persists the raw report and returns the stored
// record (with summary already computed by the caller). Also prunes
// the oldest results when the per-chat cap is exceeded.
func storeDispatchResult(db Database, r dispatchResult) {
	if db == nil || r.ChatID == "" || r.ID == "" {
		return
	}
	db.Set(dispatchResultsTable, dispatchResultKey(r.ChatID, r.ID), r)
	pruneDispatchResults(db, r.ChatID)
}

// loadDispatchResult retrieves a stored result by its ID within the
// given chat. Returns (zero, false) when not found.
func loadDispatchResult(db Database, chatID, id string) (dispatchResult, bool) {
	if db == nil || chatID == "" || id == "" {
		return dispatchResult{}, false
	}
	var r dispatchResult
	if !db.Get(dispatchResultsTable, dispatchResultKey(chatID, id), &r) {
		return dispatchResult{}, false
	}
	return r, true
}

// listDispatchResultsForChat returns all stored results for a chat,
// sorted newest-first. Used by the recall tool's list-mode and by
// pruning.
func listDispatchResultsForChat(db Database, chatID string) []dispatchResult {
	if db == nil || chatID == "" {
		return nil
	}
	prefix := chatID + ":"
	var out []dispatchResult
	for _, k := range db.Keys(dispatchResultsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var r dispatchResult
		if db.Get(dispatchResultsTable, k, &r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// pruneDispatchResults trims the chat's results to maxDispatchResultsPerChat,
// dropping oldest first. Called after each store; cheap because the
// list is short.
func pruneDispatchResults(db Database, chatID string) {
	if db == nil {
		return
	}
	all := listDispatchResultsForChat(db, chatID)
	if len(all) <= maxDispatchResultsPerChat {
		return
	}
	// `all` is newest-first; drop everything past the cap.
	for _, r := range all[maxDispatchResultsPerChat:] {
		db.Unset(dispatchResultsTable, dispatchResultKey(r.ChatID, r.ID))
	}
}
