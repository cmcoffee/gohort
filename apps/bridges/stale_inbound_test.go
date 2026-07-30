package bridges

import (
	"testing"
	"time"
)

// The reported failure: a connector re-delivered messages sent months ago and
// the agent answered them as live conversation. The id/content dedupe cannot
// help — old traffic arriving for the FIRST time is new to this server. The
// message's own send time is the only signal that settles it.
func TestReplayedHistoryIsNotRouted(t *testing.T) {
	for _, age := range []time.Duration{90 * 24 * time.Hour, 24 * time.Hour, 7 * time.Hour} {
		ts := time.Now().Add(-age).Format(time.RFC3339)
		if _, stale := inboundIsStale(ts); !stale {
			t.Errorf("a message sent %s ago must not wake an agent", age)
		}
	}
}

// A connector that was offline overnight delivers real messages that deserve an
// answer. The window has to clear those, or the guard becomes the outage.
func TestDelayedButRealMessagesStillRoute(t *testing.T) {
	for _, age := range []time.Duration{0, time.Minute, time.Hour, 5 * time.Hour} {
		ts := time.Now().Add(-age).Format(time.RFC3339)
		if _, stale := inboundIsStale(ts); stale {
			t.Errorf("a message sent %s ago is current traffic and must route", age)
		}
	}
}

// Fail open: a connector that sends no timestamp, a malformed one, or a
// clock-skewed future one must behave exactly as before the field existed.
// Dropping a real message is worse than answering an extra one.
func TestUnknownSendTimeFailsOpen(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	for _, ts := range []string{"", "   ", "not-a-time", "1699999999", future} {
		if _, stale := inboundIsStale(ts); stale {
			t.Errorf("timestamp %q must be treated as current, not stale", ts)
		}
	}
}

// The iMessage relay sends row_id, never msg_id — its field comment even says
// "DB ROWID for server-side deduplication". The server decoded row_id and then
// keyed everything on msg_id, so every iMessage inbound looked id-less: the
// persistent dedupe bailed out and the 2-minute content hash became the only
// guard. That hash cannot tell a re-delivery from two people saying the same
// short thing, so ordinary messages were dropped as duplicates.
func TestRowIDIdentifiesRelayMessages(t *testing.T) {
	if got := inboundMsgID(hookRequest{RowID: 12345}); got != "row:12345" {
		t.Errorf("a relay message must be identifiable by its row id, got %q", got)
	}
	// A connector's own id wins when it sends one.
	if got := inboundMsgID(hookRequest{MsgID: "abc", RowID: 12345}); got != "abc" {
		t.Errorf("msg_id must take precedence, got %q", got)
	}
	// Namespaced so an integer row id can never collide with an opaque msg_id.
	if inboundMsgID(hookRequest{RowID: 7}) == inboundMsgID(hookRequest{MsgID: "7"}) {
		t.Error("a row id and a msg_id of the same digits must not collide")
	}
	// Nothing to key on: the content fallback stays available for connectors
	// that genuinely cannot identify a message.
	if got := inboundMsgID(hookRequest{}); got != "" {
		t.Errorf("an inbound with no id at all must report none, got %q", got)
	}
}

// Two people saying the same short thing in one room are two messages, not a
// duplicate — the case the content-hash fallback got wrong and a real id fixes.
func TestIdenticalTextFromDifferentRowsIsNotADuplicate(t *testing.T) {
	a := inboundMsgID(hookRequest{RowID: 100, Text: "ok"})
	b := inboundMsgID(hookRequest{RowID: 101, Text: "ok"})
	if a == b || a == "" || b == "" {
		t.Errorf("identical text on different rows must yield distinct ids, got %q and %q", a, b)
	}
}
