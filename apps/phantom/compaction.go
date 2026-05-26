// Compaction: rolling-summary pass for conversation history.
//
// Without compaction, phantom forgets everything older than the
// MessageHistoryDepth window — a long-running chat thread loses
// prior context the moment turn N+depth arrives. With compaction,
// the oldest messages that have aged out of the verbatim window get
// folded into a per-conversation Summary string via WorkerChat. The
// summary then prepends the LLM's context every turn, so the model
// still knows the gist of older exchanges even when their raw text
// is no longer in window.
//
// Lossy by design: facts the user actually needs the model to
// remember go through the separate memory(action="save") tool, which
// writes to a structured key/value store the LLM always sees. The
// summary is for narrative continuity — "we talked about kubernetes
// scheduling earlier and decided to use HPA" — not authoritative
// recall.

package phantom

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// compactionTimeout caps the worker LLM call. Summaries are short
// (≤ 600 tokens output, typically) so 90 seconds covers cold-start
// load + the actual summarization.
const compactionTimeout = 90 * time.Second

// compactionMaxSummaryChars truncates the running summary when it
// grows past this size. Without a cap, every compaction pass adds
// more text and the summary itself eventually dominates the prompt.
// 4000 chars ≈ ~1000 tokens — meaningful narrative density without
// crowding the verbatim history.
const compactionMaxSummaryChars = 4000

// renderConversationSummaryBlock returns the "[Prior conversation
// context: ...]" block prepended to the system prompt. Empty when
// the conversation has no summary yet.
func renderConversationSummaryBlock(conv Conversation) string {
	s := strings.TrimSpace(conv.Summary)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Prior conversation context — summary of older exchanges that have aged out of the verbatim window. Use for continuity; treat as recall, not transcript. The verbatim history below is what the user said most recently.]\n")
	b.WriteString(s)
	b.WriteString("\n")
	return b.String()
}

// maybeCompact fires the rolling-summary update when total stored
// messages exceed 2× the configured history depth AND there are
// messages that haven't yet been folded into the summary. Runs the
// worker LLM in a goroutine — non-blocking for the foreground turn.
//
// Idempotent: if there's nothing new to summarize, returns
// immediately without an LLM call. Safe to fire after every turn.
func (T *Phantom) maybeCompact(chatID string, conv Conversation, cfg PhantomConfig) {
	if !effectiveCompactionEnabled(conv, cfg) {
		return
	}
	if T.LLM == nil || T.DB == nil {
		return
	}
	threshold := effectiveHistoryDepth(conv, cfg) * 2

	all := allMessagesAsc(T.DB, chatID)
	if len(all) <= threshold {
		return // nothing aged out yet
	}
	// Verbatim window keeps the most recent `depth` messages; older
	// ones are candidates for compaction. SummarizedThrough tracks
	// the last message we already folded, so we only summarize the
	// gap between (already summarized) and (now aging out).
	depth := effectiveHistoryDepth(conv, cfg)
	cutoff := len(all) - depth
	aging := all[:cutoff]
	// Strip messages already folded into the existing summary.
	if conv.SummarizedThroughTimestamp != "" {
		unsummarized := aging[:0]
		for _, m := range aging {
			if m.ID > conv.SummarizedThroughTimestamp {
				unsummarized = append(unsummarized, m)
			}
		}
		aging = unsummarized
	}
	if len(aging) == 0 {
		return // verbatim has grown but no new aging-out msgs
	}

	go func(chatID string, conv Conversation, aging []PhantomMessage) {
		defer func() {
			if r := recover(); r != nil {
				Log("[phantom/compaction] panic for chat=%s: %v", chatID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), compactionTimeout)
		defer cancel()

		// Build the prompt — keep it terse, the worker LLM doesn't
		// need much hand-holding for narrative summarization.
		var sb strings.Builder
		if existing := strings.TrimSpace(conv.Summary); existing != "" {
			sb.WriteString("EXISTING SUMMARY of earlier exchanges:\n\n")
			sb.WriteString(existing)
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("NEW EXCHANGES TO FOLD IN (oldest first):\n\n")
		for _, m := range aging {
			role := m.Role
			if role == "" {
				role = "user"
			}
			text := strings.TrimSpace(m.Text)
			if text == "" {
				continue
			}
			fmt.Fprintf(&sb, "[%s] %s\n", role, text)
		}

		sys := "You compact a messaging conversation's older exchanges into a narrative summary. " +
			"Preserve: decisions made, facts about the user, ongoing tasks, references to prior content, " +
			"and any commitments/promises either side made. Drop: chitchat, formality, exact wording. " +
			"Output a tight running narrative (no bullets, no headers) that the next reply needs to know " +
			"about what happened before. If an existing summary is present, EXTEND it with the new exchanges — " +
			"don't restart from scratch. Cap your output at ~600 tokens; favor density over completeness."
		resp, err := T.LLM.Chat(ctx,
			[]Message{{Role: "user", Content: sb.String()}},
			WithSystemPrompt(sys), WithMaxTokens(700),
		)
		if err != nil || resp == nil {
			Log("[phantom/compaction] worker LLM failed for chat=%s: %v", chatID, err)
			return
		}
		updated := strings.TrimSpace(resp.Content)
		if updated == "" {
			Log("[phantom/compaction] worker LLM returned empty for chat=%s", chatID)
			return
		}
		// Truncate runaway summaries — preserve the END (latest
		// narrative) which is the most relevant context.
		if len(updated) > compactionMaxSummaryChars {
			updated = "[...older summary trimmed...]\n" + updated[len(updated)-compactionMaxSummaryChars:]
		}

		// Persist back to the conversation. Re-load to avoid
		// stomping on concurrent updates to other Conversation fields.
		var fresh Conversation
		if T.DB.Get(conversationTable, chatID, &fresh) {
			fresh.Summary = updated
			fresh.SummarizedThroughTimestamp = aging[len(aging)-1].ID
			T.DB.Set(conversationTable, chatID, fresh)
			Log("[phantom/compaction] chat=%s folded %d msg(s) into summary (%d chars)",
				chatID, len(aging), len(updated))
		}
	}(chatID, conv, aging)
}

// allMessagesAsc returns every stored message for a conversation,
// sorted oldest-first by ID (which embeds RFC3339 timestamp). Used
// by compaction to know how many messages have aged out of the
// verbatim window. Reads can be slow on long-running conversations;
// compaction runs on a goroutine so this isn't on the hot path.
func allMessagesAsc(db Database, chatID string) []PhantomMessage {
	if db == nil {
		return nil
	}
	prefix := chatID + ":"
	var msgs []PhantomMessage
	for _, k := range db.Keys(messageTable) {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			var m PhantomMessage
			if db.Get(messageTable, k, &m) {
				msgs = append(msgs, m)
			}
		}
	}
	// Simple insertion sort (data is already mostly-sorted by ID,
	// the DB iteration order varies). Matches recentMessages' sort.
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].ID < msgs[j-1].ID; j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
	return msgs
}
