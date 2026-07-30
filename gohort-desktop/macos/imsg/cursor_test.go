package imsg

import (
	"testing"
	"time"
)

// The reported failure: after a thread was deleted in Messages, the db rebuild
// re-inserted old rows with fresh ROWIDs above the watermark, and the relay sent
// months-old messages up as new inbound. A ROWID-only cursor cannot tell those
// apart from new traffic — the date floor is what does.
func TestBackfilledHistoryIsSkipped(t *testing.T) {
	now := time.Now()
	cur := relayCursor{rowID: 5000, date: now.Add(-2 * time.Minute)}

	for _, age := range []time.Duration{30 * 24 * time.Hour, 24 * time.Hour, time.Hour} {
		if !cur.backfilled(now.Add(-age)) {
			t.Errorf("message %s old should be treated as backfilled history", age)
		}
	}
}

// The other half of the guard: real traffic must still get through. A message
// newer than the floor, or behind it only by delivery jitter, is not history.
func TestCurrentTrafficPasses(t *testing.T) {
	now := time.Now()
	cur := relayCursor{rowID: 5000, date: now.Add(-2 * time.Minute)}

	if cur.backfilled(now) {
		t.Error("a message sent now must not be treated as history")
	}
	// Behind the floor, but inside the jitter allowance.
	if cur.backfilled(now.Add(-5 * time.Minute)) {
		t.Error("a slightly-delayed message must not be treated as history")
	}
}

// Fail open on a date we can't read: an unusable timestamp must not classify a
// row as ancient, or a schema surprise would silently stop relaying everything.
func TestUnknownDatesFailOpen(t *testing.T) {
	now := time.Now()
	if (relayCursor{date: now}).backfilled(time.Time{}) {
		t.Error("a row with no readable date must fall through to the ROWID rule")
	}
	// A cold cursor has no floor yet, so it can't judge anything as history.
	if (relayCursor{}).backfilled(now.Add(-30 * 24 * time.Hour)) {
		t.Error("a cursor with no date floor must not filter on date")
	}
	if !appleTime(0).IsZero() || !appleTime(-1).IsZero() {
		t.Error("a non-positive Apple stamp must read as unknown, not as year 2001")
	}
}

// The floor tracks the newest message relayed, not the last row scanned —
// otherwise one out-of-order row would drag it backward and reopen the replay.
func TestAdvanceKeepsHighWaterDate(t *testing.T) {
	newest := time.Now()
	cur := relayCursor{}.advance(10, newest)
	cur = cur.advance(11, newest.Add(-time.Hour))

	if cur.rowID != 11 {
		t.Errorf("rowID = %d, want 11 (must always follow the scan)", cur.rowID)
	}
	if !cur.date.Equal(newest) {
		t.Errorf("date floor = %s, want %s (an older row must not lower it)", cur.date, newest)
	}
}

// Both Apple epoch encodings must land on the same instant — the seconds form on
// older macOS, the nanosecond form on newer. Getting this wrong would put every
// message ~30 years off and defeat the date floor entirely.
func TestAppleTimeHandlesBothEncodings(t *testing.T) {
	want := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	epoch := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	secs := int64(want.Sub(epoch) / time.Second)

	if got := appleTime(secs); !got.Equal(want) {
		t.Errorf("seconds encoding: got %s, want %s", got, want)
	}
	if got := appleTime(secs * int64(time.Second)); !got.Equal(want) {
		t.Errorf("nanosecond encoding: got %s, want %s", got, want)
	}
}
