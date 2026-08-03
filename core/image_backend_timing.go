// How long a backend ACTUALLY takes, as opposed to how long we are willing to
// wait for it.
//
// Those two numbers were the same number, and it read badly. The deadline is a
// ceiling — fifteen minutes for an edit, because a large model loading cold on
// a shared GPU can genuinely take that long — so a render that finishes in
// forty seconds was announced to the user as "about 15 minutes". A ceiling
// quoted as an estimate is wrong every time the ceiling isn't hit, which is
// almost always.
//
// So the ceiling keeps its job (deciding whether a call can hold a turn open,
// and when to give up) and this keeps the other one: what to TELL someone. It
// is measured, and until a backend has been measured it says nothing rather
// than guessing.
//
// In memory on purpose. A restart forgets, the next render re-learns, and the
// only cost of forgetting is that the agent declines to put a number on it for
// one call. That is the correct behaviour when we genuinely don't know.
package core

import (
	"sort"
	"sync"
	"time"
)

// renderSamples is how many recent renders inform the estimate. Small enough
// that a backend which just got faster (a model now resident, a GPU freed up)
// is reflected within a few calls, rather than being averaged against how it
// behaved yesterday.
const renderSamples = 8

var (
	renderTimingMu sync.Mutex
	renderTimings  = map[string][]time.Duration{}
)

// RecordImageBackendDuration notes how long one successful render took.
// Failures and timeouts are deliberately NOT recorded: a call that gave up at
// the deadline measures the deadline, not the backend, and folding those in
// would drag the estimate back toward the very ceiling this exists to replace.
func RecordImageBackendDuration(backend string, d time.Duration) {
	if backend == "" || d <= 0 {
		return
	}
	renderTimingMu.Lock()
	defer renderTimingMu.Unlock()
	s := append(renderTimings[backend], d)
	if len(s) > renderSamples {
		s = s[len(s)-renderSamples:]
	}
	renderTimings[backend] = s
}

// TypicalImageBackendDuration reports what this backend usually takes, or zero
// when it has not been measured yet — which callers must read as "say nothing
// about the time", never as "instant".
//
// The MEDIAN, not the mean: one cold start that paid a full model load is not
// what the next call will cost, and an average lets that single outlier set the
// number every render is described by.
func TypicalImageBackendDuration(backend string) time.Duration {
	renderTimingMu.Lock()
	defer renderTimingMu.Unlock()
	s := renderTimings[backend]
	if len(s) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), s...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// timeImageRender runs fn and records how long it took when it succeeds.
func timeImageRender(backend string, fn func() (restImageOutcome, error)) (restImageOutcome, error) {
	started := time.Now()
	out, err := fn()
	if err == nil {
		RecordImageBackendDuration(backend, time.Since(started))
	}
	return out, err
}
