// Conversation knowledge — the searchable counterpart to the rolling
// Summary (compaction.go).
//
// The Summary gives narrative CONTINUITY ("we talked about X and decided
// Y") but is lossy — it can't hold a specific number, address, or exact
// thing the contact said weeks ago. As older messages age out of the
// verbatim window AND get folded into the summary, we ALSO fold them into
// a per-conversation, private, searchable knowledge store. The LLM reaches
// it via recall_conversation(query) when the recent window + summary don't
// have the specific detail it needs.
//
// Private + per-conversation by construction: chunks are tagged with a
// source unique to this chat_id, so one contact's history never surfaces
// in another's recall. This is the "auto-fold long context into knowledge"
// half of the compaction design — distill into recall, complementing the
// summary, not replacing it.

package phantom

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// phantomConvoKnowledgeSource is the chunk Source tag scoping a
// conversation's folded-in history to that chat_id alone.
func phantomConvoKnowledgeSource(chatID string) string {
	return "phantom_convo:" + chatID
}

// ingestAgingToKnowledge folds the aging-out exchanges into this
// conversation's private searchable store. Per-batch reportID (keyed by
// the last message ID) so successive compactions accumulate distinct docs
// without re-ingesting earlier ones. Raw exchanges (not distilled) because
// the recall target here IS what was literally said — and the store is
// private to the conversation, so there's no shared-corpus pollution risk.
func (T *Phantom) ingestAgingToKnowledge(chatID string, conv Conversation, aging []PhantomMessage) {
	if VectorDB == nil || len(aging) == 0 {
		return
	}
	contact := strings.TrimSpace(conv.DisplayName)
	if contact == "" {
		contact = strings.TrimSpace(conv.Handle)
	}
	if contact == "" {
		contact = "contact"
	}
	var b strings.Builder
	for _, m := range aging {
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		label := contact
		if m.Role == "assistant" {
			label = "assistant"
		}
		fmt.Fprintf(&b, "%s: %s\n", label, text)
	}
	body := strings.TrimSpace(b.String())
	if body == "" {
		return
	}
	reportID := "convo-" + chatID + "-" + aging[len(aging)-1].ID
	title := "Conversation with " + contact
	// Shared core primitive: redacts secret-shaped lines, then ingests.
	IngestRecallSpan(context.Background(), VectorDB, phantomConvoKnowledgeSource(chatID), reportID, title, body, "phantom_convo")
	Log("[phantom/convo-knowledge] chat=%s folded %d msg(s) into recall store", chatID, len(aging))
}

// searchConvoKnowledge searches a conversation's folded-in history via the
// shared core recall primitive (semantic when embeddings are on, substring
// otherwise), scoped to this chat_id's source.
func searchConvoKnowledge(chatID, query string, k int) []SearchHit {
	return SearchRecall(VectorDB, phantomConvoKnowledgeSource(chatID), query, k)
}

// recallConversationToolDef builds recall_conversation(query) — the LLM's
// handle on this conversation's folded-in older history.
func (T *Phantom) recallConversationToolDef(chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "recall_conversation",
			Description: "Search your EARLIER history with this contact for something specific that's no longer in the recent messages or the summary — a detail, a number, a name, something they said a while back. Returns matching past exchanges. Use it when the recent messages and prior-context summary don't have what you need; don't guess at older details.",
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What to look for in the older conversation (natural language)."},
			},
			Required: []string{"query"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(StringArg(args, "query"))
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			hits := searchConvoKnowledge(chatID, query, 6)
			if len(hits) == 0 {
				return "Nothing in your earlier history with this contact matches that.", nil
			}
			var b strings.Builder
			b.WriteString("From earlier in this conversation:\n")
			for _, h := range hits {
				b.WriteString("\n--- ")
				label := strings.TrimSpace(h.Title)
				if label == "" {
					label = strings.TrimSpace(h.Section)
				}
				b.WriteString(label)
				b.WriteString(" ---\n")
				b.WriteString(strings.TrimSpace(h.Text))
			}
			return b.String(), nil
		},
	}
}
