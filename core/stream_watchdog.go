package core

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout is the wall-clock budget for "no bytes
// arrived" on a streaming LLM response before the read aborts.
// Distinct from LLMProviderConfig.RequestTimeout (which iotimeout
// applies as a per-read deadline at 5+ minutes) — this is shorter
// because a healthy stream produces bytes at a steady cadence and
// a multi-minute gap is almost certainly a server stall, not a slow
// generation. Operators tuning for genuinely slow models (heavy
// thinking budgets, cold cache prefills) can raise this via
// LLMProviderConfig.StreamIdleTimeout.
const DefaultStreamIdleTimeout = 60 * time.Second

// streamIdleTimeoutMarker is the substring isTransientError matches on
// so the retry layer kicks in when an idle deadline fires. Keep this
// stable — changing it would silently demote idle timeouts to
// non-transient.
const streamIdleTimeoutMarker = "stream idle timeout"

// errStreamIdleTimeout is returned when the watchdog closes the body
// due to inactivity. Wrapped with context (provider, elapsed) at the
// call site for the log line, but isTransientError matches on the
// marker substring so the retry layer recognizes any wrapped form.
var errStreamIdleTimeout = errors.New(streamIdleTimeoutMarker)

// IsStreamIdleTimeoutError reports whether err originated from a
// stream-idle deadline. Callers can branch on this when they want
// distinct handling (e.g. surfacing a user-visible "stream stalled"
// message vs. a generic retry).
func IsStreamIdleTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errStreamIdleTimeout) {
		return true
	}
	return strings.Contains(err.Error(), streamIdleTimeoutMarker)
}

// streamWatchdog wraps an http.Response.Body (or any io.ReadCloser)
// with an idle-read deadline. A background goroutine compares the
// timestamp of the last Read-with-progress against the timeout; if
// the gap exceeds the budget, the watchdog closes the body, which
// forces the next blocking Read to return an error. The caller
// observes this through scanner.Scan() returning false / Err() being
// set, and can check Fired() to distinguish idle-stall from clean
// end-of-stream.
//
// Built specifically for LLM stream paths where the server can accept
// a request, return 200 OK, and then go silent under load (KV-cache
// contention, slot deadlock, network hiccup). The Go HTTP client has
// no body-read timeout on streams by design, so without this a stalled
// upstream becomes an indefinite client wait.
//
// Stop() MUST be called when the caller is done with the body
// (typically via defer) — otherwise the watchdog goroutine leaks until
// the timeout fires. Stop() is idempotent.
type streamWatchdog struct {
	src      io.Reader
	closer   io.Closer
	timeout  time.Duration
	provider string

	// lastAct is the unix-nano timestamp of the most recent Read with
	// n>0. Atomic because the watchdog goroutine reads it from a
	// different goroutine than the caller's Read loop writes to.
	lastAct atomic.Int64

	stopCh chan struct{}
	fired  atomic.Bool
	once   atomic.Bool // guards close(stopCh)
}

// newStreamWatchdog wraps body with an idle-read deadline and launches
// the watchdog goroutine. timeout <= 0 falls back to
// DefaultStreamIdleTimeout. provider is used in the abort log line so
// the operator can correlate which backend stalled.
func newStreamWatchdog(body io.ReadCloser, timeout time.Duration, provider string) *streamWatchdog {
	if timeout <= 0 {
		timeout = DefaultStreamIdleTimeout
	}
	w := &streamWatchdog{
		src:      body,
		closer:   body,
		timeout:  timeout,
		provider: provider,
		stopCh:   make(chan struct{}),
	}
	w.lastAct.Store(time.Now().UnixNano())
	go w.watch()
	return w
}

// watch ticks at timeout/4 (clamped to >= 1s) and checks elapsed time
// since the last successful Read. On exceed it sets fired, force-closes
// the body, and exits. Exit on Stop() is the normal-shutdown path.
func (w *streamWatchdog) watch() {
	interval := w.timeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			elapsed := time.Duration(time.Now().UnixNano() - w.lastAct.Load())
			if elapsed >= w.timeout {
				w.fired.Store(true)
				// Distinctive log line — when a future hang investigation
				// looks at the server log, this is the marker that says
				// "the client gave up on a stalled stream." Keep the
				// message stable enough to grep for.
				Err("[%s] stream idle timeout fired: no bytes for %v (budget %v) — closing body to force the read to abort",
					w.provider, elapsed.Round(time.Second), w.timeout)
				_ = w.closer.Close()
				return
			}
		}
	}
}

// Read passes through to the wrapped body and updates lastAct on each
// successful read. Zero-byte reads don't reset the deadline — those
// are spurious wake-ups that don't represent server activity.
func (w *streamWatchdog) Read(p []byte) (int, error) {
	n, err := w.src.Read(p)
	if n > 0 {
		w.lastAct.Store(time.Now().UnixNano())
	}
	return n, err
}

// Stop signals the watchdog goroutine to exit. Safe to call multiple
// times. Does NOT close the body — that's the caller's responsibility
// (typically via defer on the original resp.Body.Close()).
func (w *streamWatchdog) Stop() {
	if w.once.CompareAndSwap(false, true) {
		close(w.stopCh)
	}
}

// Fired reports whether the watchdog tripped the idle deadline. Used
// at the end of a stream loop to distinguish idle-stall from
// clean-end-of-stream when scanner.Err() returns nil but the loop
// exited due to the body being closed out from under it.
func (w *streamWatchdog) Fired() bool {
	return w.fired.Load()
}
