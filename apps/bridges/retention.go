// Message storage layout and retention.
//
// Two problems live together here because the fix for one is the fix for the
// other.
//
// LAYOUT. The thread transcript used to be one flat table keyed "chatID:msgID",
// so every read walked EVERY message ever received across EVERY conversation,
// unmarshalled the ones whose key happened to carry the right prefix, sorted
// them, and returned the last 50. Cost grew with total history to display a
// fixed-size window, and deleting one conversation scanned the whole table too.
// Messages now live in a table per chat, so a read touches only that chat and a
// delete is a single Drop.
//
// RETENTION. Nothing here ever expired. The dedup table gained one permanent row
// per message received, forever, purely to answer "have I seen this before" —
// a question that stops mattering within hours. The transcript grew without
// bound to back a view that shows 50 messages. Both are now swept on a daily
// tick.
//
// The agent's own session history is deliberately NOT touched: long-context
// management already archives evicted spans into the vector store, so that
// content stays semantically recallable instead of merely deleted.
package bridges

import (
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// seenRetention bounds the inbound dedup table. These rows answer "did this
	// connector already deliver this message", which is a question about retries
	// and restarts — hours of real duty. Kept at a month anyway because the rows
	// are tiny and the failure mode of forgetting too early is re-answering an
	// old message.
	//
	// Must stay comfortably longer than staleInboundAge: once a message ages out
	// of here, only its send time stands between a re-delivery and the agent.
	seenRetention = 30 * 24 * time.Hour

	// messageRetention bounds the thread transcript — the scroll-back a person
	// actually reads. Long enough to answer "what did they say about that last
	// month", short enough that a chat doesn't accumulate forever.
	messageRetention = 90 * 24 * time.Hour

	// maxMessagesPerChat caps a single busy conversation regardless of age. The
	// thread view renders 50; this leaves generous headroom for scroll-back
	// without letting one group room outgrow every other chat combined.
	maxMessagesPerChat = 500

	// retentionInterval is how often the sweep runs. This is housekeeping, not a
	// guard — nothing depends on it being prompt.
	retentionInterval = 24 * time.Hour
)

// chatMessagesTable is the per-conversation transcript table. One table per chat
// is what makes a read cost the size of that chat instead of the size of all
// history.
func chatMessagesTable(chatID string) string { return messagesTable + ":" + chatID }

// isChatMessagesTable reports whether a table name is one of the per-chat
// transcripts, and returns the chat id it belongs to.
func isChatMessagesTable(table string) (string, bool) {
	prefix := messagesTable + ":"
	if !strings.HasPrefix(table, prefix) {
		return "", false
	}
	return strings.TrimPrefix(table, prefix), true
}

// startRetention migrates any legacy flat-table messages, then sweeps expired
// data on a daily tick. Called once at route-registration time, when T.DB is
// live. Runs in the background: none of this is on a request path.
func (T *Bridges) startRetention() {
	go func() {
		T.dropLegacySeenMessages()
		T.migrateFlatMessages()
		for {
			T.sweepRetention()
			time.Sleep(retentionInterval)
		}
	}()
}

// migrateFlatMessages moves messages out of the old single flat table into one
// table per chat. Idempotent: it works off whatever is still in the flat table,
// so an interrupted run simply resumes, and a second run finds nothing to do.
//
// Legacy keys are "chatID:msgID". Chat ids don't contain a colon (iMessage uses
// semicolons, handles are numbers or addresses), so the FIRST colon separates —
// which also survives the "row:<id>" message ids used now.
func (T *Bridges) migrateFlatMessages() {
	keys := T.DB.Keys(messagesTable)
	if len(keys) == 0 {
		return
	}
	moved, dropped := 0, 0
	for _, k := range keys {
		parts := strings.SplitN(k, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			// Unparseable key — it can't be addressed by any chat, so it could
			// never be read back. Drop it rather than carry it forward.
			T.DB.Unset(messagesTable, k)
			dropped++
			continue
		}
		var m StoredMessage
		if !T.DB.Get(messagesTable, k, &m) {
			T.DB.Unset(messagesTable, k)
			dropped++
			continue
		}
		if m.ChatID == "" {
			m.ChatID = parts[0]
		}
		if m.ID == "" {
			m.ID = parts[1]
		}
		T.DB.Set(chatMessagesTable(m.ChatID), m.ID, m)
		T.DB.Unset(messagesTable, k)
		moved++
	}
	Log("[bridges] message store migrated to per-chat tables: %d moved, %d unreadable dropped", moved, dropped)
}

// sweepRetention expires old dedup keys and old transcript messages.
func (T *Bridges) sweepRetention() {
	seen := T.sweepSeenMessages()
	msgs, chats := T.sweepMessages()
	if seen > 0 || msgs > 0 {
		Log("[bridges] retention sweep: dropped %d dedup key(s) and %d message(s) across %d chat(s)", seen, msgs, chats)
	}
}

// dropLegacySeenMessages removes the pre-timestamp dedup table in one Drop.
//
// It must be a DROP, never a read-and-convert. Those entries hold a bare int,
// kvlite decodes through gob, and DBase.Get sends a decode error to Critical —
// so much as reading one into the current string type terminates the process.
// (It did: "gob: decoding into local type *string, received remote type int".)
// Nothing is lost that matters — the entries only answer "have I seen this
// message", and the staleness guard covers a re-delivery of anything genuinely
// old. Idempotent: dropping an absent table is a no-op.
func (T *Bridges) dropLegacySeenMessages() {
	if n := T.DB.CountKeys(legacySeenMsgTable); n > 0 {
		T.DB.Drop(legacySeenMsgTable)
		Log("[bridges] dropped %d legacy dedup key(s) — the table stored an untimed value that can no longer be read", n)
	}
}

// sweepSeenMessages drops dedup entries past seenRetention. Every entry in this
// table is a timestamp string — see seenMsgTable on why the legacy table is
// dropped rather than migrated.
func (T *Bridges) sweepSeenMessages() int {
	cutoff := time.Now().Add(-seenRetention)
	dropped := 0
	for _, k := range T.DB.Keys(seenMsgTable) {
		var at string
		if T.DB.Get(seenMsgTable, k, &at) {
			if seenAt, err := time.Parse(time.RFC3339, at); err == nil && seenAt.After(cutoff) {
				continue
			}
		}
		T.DB.Unset(seenMsgTable, k)
		dropped++
	}
	return dropped
}

// sweepMessages expires transcript messages past messageRetention and trims any
// chat still over maxMessagesPerChat, newest kept. Returns messages dropped and
// chats touched.
func (T *Bridges) sweepMessages() (dropped, chats int) {
	cutoff := time.Now().Add(-messageRetention)
	for _, table := range T.DB.Tables() {
		chatID, ok := isChatMessagesTable(table)
		if !ok || chatID == "" {
			continue
		}
		n := T.sweepChat(table, cutoff)
		if n > 0 {
			dropped += n
			chats++
		}
	}
	return dropped, chats
}

// sweepChat expires one conversation's transcript. Undated messages are kept:
// an unreadable timestamp is not evidence of age, and dropping a message is not
// reversible.
func (T *Bridges) sweepChat(table string, cutoff time.Time) int {
	type entry struct {
		key string
		at  time.Time
	}
	var dated []entry
	dropped := 0
	for _, k := range T.DB.Keys(table) {
		var m StoredMessage
		if !T.DB.Get(table, k, &m) {
			continue
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(m.Timestamp))
		if err != nil {
			continue // undated: keep
		}
		if at.Before(cutoff) {
			T.DB.Unset(table, k)
			dropped++
			continue
		}
		dated = append(dated, entry{key: k, at: at})
	}
	// Still oversized after the age pass — trim the oldest dated messages.
	if over := len(dated) - maxMessagesPerChat; over > 0 {
		sort.Slice(dated, func(i, j int) bool { return dated[i].at.Before(dated[j].at) })
		for _, e := range dated[:over] {
			T.DB.Unset(table, e.key)
			dropped++
		}
	}
	return dropped
}
