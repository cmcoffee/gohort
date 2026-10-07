package browser

import (
	"testing"
	"time"
)

// A framed document's load-time fetch is bridged through the top page by
// message, so it starts after the idle wait has already returned. The check
// used to snapshot the moment nothing was in flight, before that fetch began,
// and reported a page that did fetch its data as never having fetched it.
func TestCheckWaitsForTheNetworkToGoQuiet(t *testing.T) {
	settled := time.Unix(1000, 0)
	at := func(d time.Duration) time.Time { return settled.Add(d) }

	// Nothing ever started after the idle wait: the window still runs from
	// the idle wait, so a late bridged fetch has its chance to begin.
	if checkSettled(at(300*time.Millisecond), settled, at(-5*time.Second), 0) {
		t.Error("an empty network is not yet a quiet one: wait out the window from the idle wait")
	}
	if !checkSettled(at(checkQuietWindow), settled, at(-5*time.Second), 0) {
		t.Error("an already-quiet page should pay the window once and no more")
	}

	// A request started after the idle wait restarts the window.
	if checkSettled(at(1500*time.Millisecond), settled, at(800*time.Millisecond), 0) {
		t.Error("a request started 700ms ago means the page is still loading")
	}
	if !checkSettled(at(1900*time.Millisecond), settled, at(800*time.Millisecond), 0) {
		t.Error("a full quiet window after the last request should settle")
	}

	// In flight never settles; the caller's deadline bounds that case.
	if checkSettled(at(time.Minute), settled, at(-time.Second), 1) {
		t.Error("a request still in flight must not settle")
	}

	// A page that never goes quiet (a fast poll) stops waiting for quiet
	// after checkQuietMax, once nothing is in flight.
	if !checkSettled(at(checkQuietMax), settled, at(checkQuietMax-100*time.Millisecond), 0) {
		t.Error("a chatty page must not hold the check past checkQuietMax")
	}
}
