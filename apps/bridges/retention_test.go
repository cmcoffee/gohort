package bridges

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func newRetentionBridges() *Bridges { return &Bridges{AppCore{DB: OpenCache()}} }

func stamp(age time.Duration) string {
	return time.Now().Add(-age).UTC().Format(time.RFC3339)
}

// A read used to walk EVERY message in EVERY conversation and keep the ones
// whose key carried the right prefix. Per-chat tables mean one chat's messages
// are the only ones touched — and, just as importantly, that a message can never
// leak into another chat's thread.
func TestThreadReadsAreScopedToTheirChat(t *testing.T) {
	T := newRetentionBridges()
	T.storeMessage(StoredMessage{ID: "1", ChatID: "chatA", Role: "user", Text: "from A", Timestamp: stamp(time.Minute)})
	T.storeMessage(StoredMessage{ID: "2", ChatID: "chatB", Role: "user", Text: "from B", Timestamp: stamp(time.Minute)})

	got := T.recentMessages("chatA", 0)
	if len(got) != 1 || got[0].Text != "from A" {
		t.Fatalf("chatA should hold exactly its own message, got %+v", got)
	}
	// A chat id that is a PREFIX of another keeps its own thread. The old flat
	// layout got this right (it matched on "chatID:", so "chat:" never caught
	// "chatA:1"); this pins the property so the per-chat layout keeps it.
	T.storeMessage(StoredMessage{ID: "3", ChatID: "chat", Role: "user", Text: "short id", Timestamp: stamp(time.Minute)})
	if got := T.recentMessages("chat", 0); len(got) != 1 || got[0].Text != "short id" {
		t.Errorf("a chat id that prefixes another must keep a separate thread, got %+v", got)
	}
}

func TestDeletingAConversationDropsItsThread(t *testing.T) {
	T := newRetentionBridges()
	T.storeMessage(StoredMessage{ID: "1", ChatID: "gone", Role: "user", Text: "bye", Timestamp: stamp(time.Minute)})
	T.storeMessage(StoredMessage{ID: "1", ChatID: "kept", Role: "user", Text: "still here", Timestamp: stamp(time.Minute)})

	T.deleteConvo("gone")

	if got := T.recentMessages("gone", 0); len(got) != 0 {
		t.Errorf("the deleted conversation's messages should be gone, got %+v", got)
	}
	if got := T.recentMessages("kept", 0); len(got) != 1 {
		t.Errorf("deleting one conversation must not touch another, got %+v", got)
	}
}

// Messages older than the retention window go; recent ones stay.
func TestOldMessagesExpire(t *testing.T) {
	T := newRetentionBridges()
	T.storeMessage(StoredMessage{ID: "old", ChatID: "c", Role: "user", Text: "ancient", Timestamp: stamp(messageRetention + 24*time.Hour)})
	T.storeMessage(StoredMessage{ID: "new", ChatID: "c", Role: "user", Text: "recent", Timestamp: stamp(time.Hour)})

	T.sweepRetention()

	got := T.recentMessages("c", 0)
	if len(got) != 1 || got[0].Text != "recent" {
		t.Fatalf("only the recent message should survive, got %+v", got)
	}
}

// An unreadable timestamp is not evidence of age, and deleting a message can't
// be undone — so undated messages are kept rather than swept.
func TestUndatedMessagesAreKept(t *testing.T) {
	T := newRetentionBridges()
	T.DB.Set(chatMessagesTable("c"), "weird", StoredMessage{ID: "weird", ChatID: "c", Text: "no date", Timestamp: "not-a-time"})

	T.sweepRetention()

	if got := T.recentMessages("c", 0); len(got) != 1 {
		t.Errorf("a message with an unparseable timestamp must be kept, got %+v", got)
	}
}

// A busy room is capped by count even when every message is recent, and the
// NEWEST are what survive.
func TestBusyChatIsTrimmedToTheCap(t *testing.T) {
	T := newRetentionBridges()
	total := maxMessagesPerChat + 50
	for i := 0; i < total; i++ {
		// Oldest first: index 0 is the furthest back, so the last ones written
		// are the newest and must be the ones kept.
		T.storeMessage(StoredMessage{
			ID: string(rune('a'+i%26)) + time.Duration(i).String(), ChatID: "busy", Role: "user",
			Text: "msg", Timestamp: stamp(time.Duration(total-i) * time.Minute),
		})
	}
	T.sweepRetention()

	got := T.recentMessages("busy", 0)
	if len(got) > maxMessagesPerChat {
		t.Errorf("chat should be trimmed to %d, got %d", maxMessagesPerChat, len(got))
	}
	// recentMessages sorts oldest-first, so the tail is the newest message and
	// must have survived the trim.
	if len(got) > 0 {
		newest := got[len(got)-1].Timestamp
		if parsed, err := time.Parse(time.RFC3339, newest); err == nil && time.Since(parsed) > 2*time.Minute {
			t.Errorf("the trim kept the wrong end: newest survivor is %s old", time.Since(parsed))
		}
	}
}

// Dedup keys are the pure-garbage store — one permanent row per message, kept
// only to answer "seen this before". They must expire, and a still-live key must
// keep suppressing its message.
func TestDedupKeysExpire(t *testing.T) {
	T := newRetentionBridges()
	if T.seenMessage("c", "m1") {
		t.Fatal("a message seen for the first time must not report as a duplicate")
	}
	if !T.seenMessage("c", "m1") {
		t.Fatal("the same message must report as a duplicate while its key lives")
	}

	// Age the key past the window.
	T.DB.Set(seenMsgTable, "c:m1", time.Now().Add(-seenRetention-time.Hour).UTC().Format(time.RFC3339))
	T.sweepRetention()

	if T.seenMessage("c", "m1") {
		t.Error("an expired dedup key must no longer suppress its message")
	}
}

// The dedup TTL has to outlast the staleness window: once a key expires, the
// message's own send time is the only thing standing between a re-delivery and
// the agent.
func TestDedupOutlastsTheStalenessWindow(t *testing.T) {
	if seenRetention <= staleInboundAge {
		t.Errorf("seenRetention (%s) must exceed staleInboundAge (%s)", seenRetention, staleInboundAge)
	}
}

// Messages written under the old flat "chatID:msgID" layout must land in the
// right per-chat table, including ids that themselves contain a colon.
func TestLegacyMessagesMigrate(t *testing.T) {
	T := newRetentionBridges()
	T.DB.Set(messagesTable, "chatA:abc", StoredMessage{ID: "abc", ChatID: "chatA", Text: "legacy", Timestamp: stamp(time.Hour)})
	T.DB.Set(messagesTable, "chatA:row:42", StoredMessage{ID: "row:42", ChatID: "chatA", Text: "row id", Timestamp: stamp(time.Hour)})

	T.migrateFlatMessages()

	got := T.recentMessages("chatA", 0)
	if len(got) != 2 {
		t.Fatalf("both legacy messages should have migrated, got %+v", got)
	}
	if len(T.DB.Keys(messagesTable)) != 0 {
		t.Error("the flat table should be empty after migration")
	}
	// Running it again must be a no-op, not a duplication.
	T.migrateFlatMessages()
	if got := T.recentMessages("chatA", 0); len(got) != 2 {
		t.Errorf("migration must be idempotent, got %d messages", len(got))
	}
}

// The crash this cost us: the dedup table's value type changed from int to a
// timestamp string, and kvlite decodes through gob while DBase.Get routes a
// decode error to Critical — so reading one legacy entry did not fail softly,
// it terminated gohort on startup ("gob: decoding into local type *string,
// received remote type int"). Legacy entries must be DROPPED, never read.
func TestLegacyDedupTableIsDroppedNotRead(t *testing.T) {
	T := newRetentionBridges()
	// Exactly what the old code wrote: a bare int under the old table name.
	T.DB.Set(legacySeenMsgTable, "chat:1", 1)
	T.DB.Set(legacySeenMsgTable, "chat:2", 1)

	T.dropLegacySeenMessages()

	if n := T.DB.CountKeys(legacySeenMsgTable); n != 0 {
		t.Errorf("legacy dedup entries should be gone, %d remain", n)
	}
	// The sweep must now be safe to run — it reads strings, and nothing in the
	// live table can be an int.
	T.sweepRetention()

	// Dedup still works going forward.
	if T.seenMessage("chat", "3") {
		t.Error("a fresh message must not report as already seen")
	}
	if !T.seenMessage("chat", "3") {
		t.Error("dedup must work after the legacy table is dropped")
	}
}

// The two tables must never be the same name, or a legacy int and a new
// timestamp share a key space and the fatal decode returns.
func TestDedupTablesAreDistinct(t *testing.T) {
	if seenMsgTable == legacySeenMsgTable {
		t.Fatal("the timestamped dedup table must not reuse the legacy table's name")
	}
}

// Dropping is idempotent — a second start with no legacy table must be a no-op.
func TestDroppingLegacyTwiceIsSafe(t *testing.T) {
	T := newRetentionBridges()
	T.dropLegacySeenMessages()
	T.dropLegacySeenMessages()
}
