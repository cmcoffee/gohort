// Per-chat vector knowledge for Phantom — a long-term-memory layer
// scoped to ONE conversation (individual or group) so the agent can
// recall details from prior turns without the whole chat history
// having to fit in context. Distinct from phantom_memory (manual
// plain-text notes): knowledge is semantic-search over auto-ingested
// turn synthesis + explicit LLM saves.
//
// Source-tag isolation: every chunk lives under
// "phantom:<chat_id>" so retrieval can never cross-pollinate
// between two chats. The same EmbeddedChunks table that orchestrate
// + servitor use is reused; the source tag is the only thing that
// keeps namespaces apart.
//
// Lifetimes:
//   - knowledge_save (action="save") deposits a chunk explicitly when
//     the LLM decides something is worth keeping.
//   - autoIngestChatTurn runs at turn close, capturing the user's
//     message + the assistant's reply as a single chunk — so the
//     store grows even when the LLM forgets the explicit save.
//   - The system prompt gets a pre-search injection at turn start,
//     surfacing the top-k chunks relevant to the incoming user
//     message so the LLM can reference past turns without scanning
//     the full transcript.
//
// Both auto-ingest and prompt injection are gated by the per-chat
// "knowledge" tool toggle so an admin can enable it only on chats
// where long-term memory is desired.

package phantom

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// knowledgeIngestTimeout caps the embedding round-trip; matches
// orchestrate's cap so the two paths feel the same under load.
const knowledgeIngestTimeout = 45 * time.Second

// autoInjectMinScore is the cosine-similarity floor for chunks
// pre-injected into the system prompt. Vector search ALWAYS returns
// the top-K even when nothing is meaningfully close — injecting weak
// matches gives the LLM "recalled context" that's actually unrelated,
// which it then anchors to and extends with hallucinated specifics.
// 0.65 catches the genuine semantic matches our embeddings produce
// without dragging in low-confidence neighbors. Explicit
// knowledge_search (action="search") returns hits without this floor —
// when the LLM asks on purpose, low-confidence hits are still useful.
const autoInjectMinScore = 0.65

// autoInjectTopK is the cap on auto-injected chunks per turn. Lower
// than the explicit-search default (5) because pre-injection rides on
// EVERY turn — tight prompts + fewer anchoring surfaces means less
// noise for the LLM to incorporate when the question isn't actually
// recall-shaped.
const autoInjectTopK = 3

// chatKnowledgeEnabled reports whether the "knowledge" tool is in
// the conv's effective enabled-tools set. Mirrors the toolEnabled
// closure in buildConvTools so the prompt-injection and auto-ingest
// paths gate on the SAME signal the tool catalog uses — admins flip
// "knowledge" on a chat and all three (tool / inject / ingest)
// activate together.
func chatKnowledgeEnabled(conv Conversation, cfg PhantomConfig) bool {
	names := cfg.EnabledTools
	if conv.EnabledTools != nil {
		names = conv.EnabledTools
	}
	// Empty list = "all tools enabled by default" per buildConvTools'
	// toolEnabled. Match that semantic so a chat without an explicit
	// allowlist still gets knowledge.
	if len(names) == 0 {
		return true
	}
	for _, n := range names {
		if n == "knowledge" {
			return true
		}
	}
	return false
}

// chatKnowledgeSource returns the EmbeddedChunk.Source tag for a
// chat's namespace. Empty chatID returns "" so callers can guard with
// a single check.
func chatKnowledgeSource(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	return "phantom:" + chatID
}

// ingestChatKnowledge writes one chunk under the chat's namespace.
// title becomes the chunk's section header; body is the indexed
// content. Each call gets a fresh reportID so prior entries persist
// (IngestReport otherwise replaces all chunks under the same
// reportID).
func ingestChatKnowledge(ctx context.Context, db Database, chatID, title, body string) {
	if db == nil || chatID == "" {
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Note"
	}
	reportID := fmt.Sprintf("phantom-know-%s-%d", chatID, time.Now().UnixNano())
	doc := "## " + title + "\n\n" + body
	IngestReport(ctx, db, chatKnowledgeSource(chatID), reportID, doc)
}

// searchChatKnowledge returns up to k semantically relevant chunks
// from this chat's namespace. Embedding-first with substring
// fallback, same shape as orchestrate's per-agent search.
func searchChatKnowledge(ctx context.Context, db Database, chatID, query string, k int) []SearchHit {
	if db == nil || strings.TrimSpace(query) == "" || k <= 0 {
		return nil
	}
	src := chatKnowledgeSource(chatID)
	if src == "" {
		return nil
	}
	cfg := GetEmbeddingConfig()
	var vec []float32
	if cfg.Enabled {
		if v, err := Embed(ctx, query); err == nil && len(v) > 0 {
			vec = v
		}
	}
	var raw []SearchHit
	if len(vec) > 0 {
		raw = SearchChunks(db, vec, k*8)
	} else {
		raw = SearchChunksSubstring(db, query, k*8)
	}
	out := make([]SearchHit, 0, k)
	for _, h := range raw {
		if h.Source != src {
			continue
		}
		out = append(out, h)
		if len(out) >= k {
			break
		}
	}
	return out
}

// renderChatKnowledgePromptSection formats a slice of search hits as
// a markdown block suitable for prepending to the system prompt.
// Empty input → empty string so callers can concatenate unconditionally.
//
// Framing is deliberately cautious: these are auto-recalled notes that
// MIGHT be relevant by semantic similarity to the inbound message, not
// confirmed-relevant facts. Older auto-inject prompts ("Things you
// recall…") read as authoritative recall, which the LLM would then
// anchor on and extend with hallucinated specifics. Reframe as
// "possibly relevant — verify against current message" so the LLM
// treats them as one source among others, not a fact pile to riff off.
func renderChatKnowledgePromptSection(hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nPossibly relevant notes from earlier turns in this chat (semantic-search hits — VERIFY against the current message before relying on any of them; ignore entries that don't actually fit the current question):\n\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(h.Text))
	}
	return b.String()
}

// autoIngestChatTurn captures the user's message as a knowledge chunk
// at turn close. Runs for chats that have knowledge enabled, so the
// store accrues without the LLM having to call knowledge_save
// explicitly.
//
// **Only the user's message is ingested — NOT the assistant's reply.**
// Ingesting the reply (the prior behavior) was the compounding-
// hallucination loop: a wrong answer got embedded as "recall," then
// surfaced on a later semantically-similar question, and the LLM
// extended it with growing confidence because it was now in its
// "memory." Even when the user later corrected it, both versions sat
// in the store and search returned both — the LLM had to gamble on
// which to believe. User messages are grounded by definition; the
// model can't have hallucinated something the user actually typed.
// For findings worth keeping that came from the assistant, the LLM
// calls knowledge(action="save") deliberately — that path stays open
// and is the right shape for curated saves.
//
// Short messages are skipped (conversational filler — "hi" / "thanks"
// won't help future retrieval). A retention sweep runs piggybacked
// after a successful ingest so old chunks for this chat get pruned
// without a background goroutine.
func autoIngestChatTurn(ctx context.Context, db Database, chatID, userMsg, reply string, retentionDays int) {
	if db == nil || chatID == "" {
		return
	}
	userMsg = strings.TrimSpace(userMsg)
	// reply is intentionally unused — see header comment. Kept in the
	// signature so callers don't have to change.
	_ = reply
	if userMsg == "" {
		return
	}
	// Skip short exchanges — conversational filler ("hi" / "thanks")
	// won't help future retrieval.
	if len(userMsg) < 30 {
		return
	}
	title := userMsg
	if len(title) > 80 {
		title = title[:80] + "…"
	}
	ingestChatKnowledge(ctx, db, chatID, title, userMsg)
	pruneOldChatKnowledge(db, chatID, retentionDays)
}

// pruneOldChatKnowledge walks this chat's chunks and deletes any
// older than retentionDays. retentionDays<=0 (or no parseable date
// on the chunk) means "keep" — the latter is defensive against a
// future schema where Date is absent or unparseable. Cheap when the
// chat's chunk count is small; amortized across each ingest so
// there's no separate goroutine to manage.
func pruneOldChatKnowledge(db Database, chatID string, retentionDays int) {
	if db == nil || retentionDays <= 0 {
		return
	}
	src := chatKnowledgeSource(chatID)
	if src == "" {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	var toDelete []string
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.Source != src {
			continue
		}
		if c.Date == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, c.Date)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			toDelete = append(toDelete, key)
		}
	}
	if len(toDelete) > 0 {
		DeleteChunksByIDs(db, toDelete)
		Log("[phantom/knowledge] retention sweep chat=%s pruned=%d retention_days=%d", chatID, len(toDelete), retentionDays)
	}
}

// knowledgeGroupedToolDef builds the LLM-facing grouped tool for the
// chat knowledge surface — action discriminator handles save / search
// / forget / help. Mirrors orchestrate's three-tool split but folded
// into one grouped entry so the Phantom catalog stays compact (chat
// agents tend to have tight allowlists; one entry beats three).
func knowledgeGroupedToolDef(db Database, chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "knowledge",
			Description: "IMPLICIT long-term memory for THIS chat — semantic-search over auto-ingested past turns plus explicit saves. The framework automatically pulls relevant chunks into your prompt each turn; you can also call action=\"search\" to look up something on demand, action=\"save\" to deposit a bulkier finding (\"the user runs a Postgres 16 cluster with these specific tuning quirks\"), action=\"forget\" to drop entries that have gone stale. Counterpart to the EXPLICIT memory tool (short facts always visible in your prompt every turn). Rule of thumb: if a fact deserves to be visible to you on EVERY turn forever, use memory; if it's recall-worthy but situational, use knowledge.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "Which sub-action: save | search | forget | help."},
				"title":  {Type: "string", Description: "(save) Short heading for what this finding is about. Example: \"User's daughter's birthday\"."},
				"body":   {Type: "string", Description: "(save) The finding itself, a few sentences. Self-contained — include enough context that it'll make sense without seeing this conversation."},
				"query":  {Type: "string", Description: "(search, forget) Natural-language query. Search returns up to k hits; forget DELETES the top hits — so make forget queries specific."},
				"k":      {Type: "number", Description: "(search default 5 cap 20; forget default 3 cap 10) Max matches to return / delete."},
			},
			Required: []string{"action"},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.TrimSpace(StringArg(args, "action"))
			switch action {
			case "", "help":
				return knowledgeHelp(), nil
			case "save":
				return knowledgeSave(db, chatID, args)
			case "search":
				return knowledgeSearch(db, chatID, args)
			case "forget":
				return knowledgeForget(db, chatID, args)
			default:
				return "", fmt.Errorf("unknown action %q. valid: save, search, forget, help", action)
			}
		},
	}
}

func knowledgeHelp() string {
	return `knowledge — usage:

  action="save" — persist a finding to this chat's long-term store.
    Required: body. Optional: title.

  action="search" — semantic search over this chat's stored findings
    (both manual saves and auto-ingested past turns). Required: query.
    Optional: k (default 5, max 20).

  action="forget" — delete the top matches for a query. Be specific —
    a loose query nukes more than you intended. Required: query.
    Optional: k (default 3, max 10).

  action="help" — show this spec.
`
}

func knowledgeSave(db Database, chatID string, args map[string]any) (string, error) {
	body := strings.TrimSpace(StringArg(args, "body"))
	if body == "" {
		return "", fmt.Errorf("body is required")
	}
	title := strings.TrimSpace(StringArg(args, "title"))
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
	defer cancel()
	ingestChatKnowledge(ctx, db, chatID, title, body)
	Log("[phantom/knowledge] save chat=%s len=%d", chatID, len(body))
	return fmt.Sprintf("Saved %d chars to this chat's knowledge.", len(body)), nil
}

func knowledgeSearch(db Database, chatID string, args map[string]any) (string, error) {
	query := strings.TrimSpace(StringArg(args, "query"))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	k := 5
	if v, ok := args["k"].(float64); ok && v > 0 {
		k = int(v)
		if k > 20 {
			k = 20
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
	defer cancel()
	hits := searchChatKnowledge(ctx, db, chatID, query, k)
	if len(hits) == 0 {
		return "No matching knowledge in this chat.", nil
	}
	payload := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		payload = append(payload, map[string]any{
			"title":   h.Section,
			"content": h.Text,
			"score":   h.Score,
		})
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func knowledgeForget(db Database, chatID string, args map[string]any) (string, error) {
	query := strings.TrimSpace(StringArg(args, "query"))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	k := 3
	if v, ok := args["k"].(float64); ok && v > 0 {
		k = int(v)
		if k > 10 {
			k = 10
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
	defer cancel()
	hits := searchChatKnowledge(ctx, db, chatID, query, k)
	if len(hits) == 0 {
		return "Nothing matched — nothing to forget.", nil
	}
	ids := make([]string, 0, len(hits))
	deleted := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if h.ID == "" {
			continue
		}
		ids = append(ids, h.ID)
		deleted = append(deleted, map[string]any{
			"id":      h.ID,
			"title":   h.Section,
			"snippet": truncateText(h.Text, 160),
			"score":   h.Score,
		})
		Log("[phantom/knowledge] forget chat=%s id=%s score=%.3f", chatID, h.ID, h.Score)
	}
	DeleteChunksByIDs(db, ids)
	out, err := json.Marshal(map[string]any{"deleted": deleted, "count": len(deleted)})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// truncateText is a small local helper so this file doesn't reach
// across into orchestrate's truncate. Trims to n runes and tacks on
// an ellipsis if it had to cut.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// countChatKnowledgeChunks walks EmbeddedChunks and tallies how many
// rows belong to this chat's namespace. O(N) scan over the table —
// fine at Phantom's scale, but a future optimization would be to
// cache the per-source count when the chunk cache is rebuilt.
func countChatKnowledgeChunks(db Database, chatID string) int {
	if db == nil {
		return 0
	}
	src := chatKnowledgeSource(chatID)
	if src == "" {
		return 0
	}
	n := 0
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.Source == src {
			n++
		}
	}
	return n
}

// wipeChatKnowledge deletes every chunk in this chat's namespace and
// returns the count removed. Used by the admin UI's "blank it out"
// button — the LLM's knowledge_forget tool only does targeted deletes
// (by query), so this is the operator-side nuclear option for "start
// fresh."
func wipeChatKnowledge(db Database, chatID string) int {
	src := chatKnowledgeSource(chatID)
	if src == "" {
		return 0
	}
	return WipeChunksBySourcePrefix(db, src)
}
