// The relay's position in chat.db, and the Apple-epoch conversion it rests on.
//
// Deliberately NOT darwin-gated, unlike the rest of the package: this is pure
// time arithmetic with no macOS dependency, and it's where the "old messages
// relayed as new" bug lived. Keeping it in the darwin-only file meant it could
// only be tested on a Mac — i.e. not at all, in practice. Here it compiles and
// tests on the machine the code is written on.
package imsg

import "time"

// relayCursor is the relay's position in chat.db: how far the ROWID scan has
// advanced, and the send DATE of the newest message relayed so far.
//
// The date half exists because ROWID is INSERTION order, not message time.
// Messages re-inserts rows with fresh ROWIDs whenever it rebuilds the db —
// an iCloud history sync after a wake or reconnect, or the compaction that
// follows DELETING a thread. Those ROWIDs land above the watermark, so a
// months-old message looks brand new to a ROWID-only cursor and gets relayed as
// new inbound. Messages the owner sent from their OWN phone are the worst case:
// the relay never saw them go out, so neither self-send skip path recognizes
// them, and the agent wakes up answering a conversation from weeks ago.
// Server-side dedup can't catch them either — it keys on the ROWID, which
// really is new.
type relayCursor struct {
	rowID int64
	date  time.Time
}

// dateSlack is how far behind the date floor a message may be and still count as
// new traffic. Covers ordinary delivery jitter; a history backfill is
// hours-to-months behind, far past this.
const dateSlack = 10 * time.Minute

// backfilled reports whether a row is history being re-inserted rather than new
// traffic: its send date sits behind the cursor's date floor by more than
// dateSlack. A zero (unreadable) date is never treated as old — a row with no
// usable date falls through to the ROWID rule alone.
//
// The slack matters: a message delayed in transit can land with a date slightly
// behind one already relayed, and dropping those would lose real traffic. It's
// sized for delivery jitter, orders of magnitude below the gap that makes a
// backfill obvious.
func (c relayCursor) backfilled(sent time.Time) bool {
	if sent.IsZero() || c.date.IsZero() {
		return false
	}
	return sent.Before(c.date.Add(-dateSlack))
}

// advance moves the cursor past a row, carrying the date floor forward only when
// this message is newer — so the floor tracks the high-water mark, not the last
// row scanned.
func (c relayCursor) advance(rowID int64, sent time.Time) relayCursor {
	c.rowID = rowID
	if sent.After(c.date) {
		c.date = sent
	}
	return c
}

func (c relayCursor) dateLabel() string {
	if c.date.IsZero() {
		return "none"
	}
	return c.date.UTC().Format(time.RFC3339)
}

// appleTime converts Apple's epoch to a time.Time. Older macOS versions stored
// seconds since 2001-01-01, newer store nanoseconds; we detect by magnitude.
// A non-positive value returns the zero time — callers treat that as "unknown",
// never as "very old", so a row with no usable date is never filtered on it.
func appleTime(stamp int64) time.Time {
	if stamp <= 0 {
		return time.Time{}
	}
	appleEpoch := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if stamp > 1e15 {
		return appleEpoch.Add(time.Duration(stamp)) // nanoseconds
	}
	return appleEpoch.Add(time.Duration(stamp) * time.Second) // seconds
}

// appleTimeToRFC3339 renders Apple's epoch as RFC3339 for the wire.
func appleTimeToRFC3339(stamp int64) string {
	return appleTime(stamp).UTC().Format(time.RFC3339)
}
