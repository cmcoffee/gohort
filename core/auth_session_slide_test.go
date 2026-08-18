package core

import (
	"testing"
	"time"
)

// withSessionDays runs fn with the session window (and absolute ceiling) pinned,
// restoring the previous hooks afterwards.
func withSessionDays(window, absolute int, fn func()) {
	prevW, prevA := AuthSessionDays, AuthSessionAbsoluteDays
	AuthSessionDays = func() int { return window }
	AuthSessionAbsoluteDays = func() int { return absolute }
	defer func() { AuthSessionDays, AuthSessionAbsoluteDays = prevW, prevA }()
	fn()
}

// A session with most of its window left is not rewritten — otherwise this
// costs a database write on every request instead of one per half window.
func TestNoRenewalBeforeHalfway(t *testing.T) {
	withSessionDays(7, 90, func() {
		now := time.Now()
		sess := authSession{
			Created: now.Add(-1 * time.Hour).Unix(),
			Expires: now.Add(6 * 24 * time.Hour).Unix(), // 6 of 7 days left
		}
		if _, renew := renewedExpiry(sess, now); renew {
			t.Error("renewed a session that still has most of its window left")
		}
	})
}

// Past the halfway mark an active session slides out to a full window from now.
// This is the whole point: working in gohort must not log you out.
func TestRenewalPastHalfway(t *testing.T) {
	withSessionDays(7, 90, func() {
		now := time.Now()
		sess := authSession{
			Created: now.Add(-5 * 24 * time.Hour).Unix(),
			Expires: now.Add(2 * 24 * time.Hour).Unix(), // 2 of 7 days left
		}
		next, renew := renewedExpiry(sess, now)
		if !renew {
			t.Fatal("did not renew a session past its halfway mark")
		}
		want := now.Add(7 * 24 * time.Hour).Unix()
		if next < want-5 || next > want+5 {
			t.Errorf("renewed to %d, want ~%d (a full window from now)", next, want)
		}
	})
}

// Sliding must not outrun the absolute ceiling — that ceiling is what still
// ends a session that never goes idle, and what bounds a stolen cookie.
func TestRenewalStopsAtAbsoluteCeiling(t *testing.T) {
	withSessionDays(7, 30, func() {
		now := time.Now()
		created := now.Add(-28 * 24 * time.Hour)
		sess := authSession{
			Created: created.Unix(),
			Expires: now.Add(1 * 24 * time.Hour).Unix(),
		}
		next, renew := renewedExpiry(sess, now)
		if !renew {
			t.Fatal("expected a (clamped) renewal")
		}
		ceiling := created.Add(30 * 24 * time.Hour).Unix()
		if next != ceiling {
			t.Errorf("renewed to %d, want the ceiling %d", next, ceiling)
		}
		// Already AT the ceiling: nothing to extend, so nothing to write.
		sess.Expires = ceiling
		if _, renew := renewedExpiry(sess, now); renew {
			t.Error("kept renewing a session already at its absolute ceiling")
		}
	})
}

// A ceiling of 0 means the operator turned it off; sliding then has no bound
// beyond going idle.
func TestNoCeilingWhenDisabled(t *testing.T) {
	withSessionDays(7, 0, func() {
		now := time.Now()
		sess := authSession{
			Created: now.Add(-5 * 365 * 24 * time.Hour).Unix(), // years old
			Expires: now.Add(1 * time.Hour).Unix(),
		}
		next, renew := renewedExpiry(sess, now)
		if !renew {
			t.Fatal("a disabled ceiling should not block renewal")
		}
		if want := now.Add(7 * 24 * time.Hour).Unix(); next < want-5 {
			t.Errorf("renewed to %d, want ~%d", next, want)
		}
	})
}

// A ceiling shorter than one window would expire sessions EARLIER than the
// setting they were issued under — the opposite of this feature's purpose.
func TestCeilingNeverShorterThanWindow(t *testing.T) {
	withSessionDays(30, 7, func() {
		if got, want := sessionAbsoluteDuration(), 30*24*time.Hour; got != want {
			t.Errorf("ceiling %v, want it raised to the window %v", got, want)
		}
	})
}
