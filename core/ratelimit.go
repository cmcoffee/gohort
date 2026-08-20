// A ceiling on work a caller can demand.
//
// Several surfaces let somebody who has presented little or nothing make this
// instance do something expensive: run a sandboxed script, start an LLM turn,
// read a table. Authentication answers whether they may; it says nothing about
// how often, and "as often as requests can be issued" is the answer whenever
// nothing is counting.
//
// The peer surface worked this out first and grew two limiters of its own (see
// peer_key.go): a per-key call ceiling and a per-source failure throttle, both
// over a rolling window with a bounded map. Those are the right shapes, and
// writing them a third time by hand is how they drift. This is the generic
// version; peer_key.go's remain because they carry peer-specific policy on top.
package core

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter counts events per key within a rolling window.
//
// In memory on purpose. The ceiling exists to stop a runaway or hostile caller
// from saturating this instance right now; one that survives restarts would be
// a different feature, and a heavier one.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
	limit   int
	window  time.Duration
	// maxKeys caps the tracking map. Reached only under a distributed attempt,
	// where the map itself would otherwise be the exhaustion vector the
	// limiter exists to prevent.
	maxKeys int
}

type rateWindow struct {
	start time.Time
	n     int
}

// defaultRateLimiterKeys bounds the tracking map for a limiter built without
// an explicit cap. Generous next to any real caller count, small enough that
// the map cannot become the problem.
const defaultRateLimiterKeys = 4096

// NewRateLimiter returns a limiter allowing limit events per key per window.
// A limit of zero or less allows everything, so a caller can disable one by
// configuration without the call sites growing a branch.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		windows: map[string]*rateWindow{},
		limit:   limit,
		window:  window,
		maxKeys: defaultRateLimiterKeys,
	}
}

// Allow records one event against key and reports whether it fits under the
// ceiling. An empty key is always allowed: a limiter cannot meaningfully
// bucket what it cannot identify, and refusing would turn "we could not tell
// who this is" into an outage.
func (r *RateLimiter) Allow(key string) bool {
	if r == nil || r.limit <= 0 || key == "" {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	w := r.windows[key]
	if w == nil || now.Sub(w.start) >= r.window {
		if w == nil && len(r.windows) >= r.maxKeys {
			// Sweep expired entries while the lock is held. Bounded work, and
			// it keeps the map proportional to ACTIVE keys rather than to
			// every key ever seen.
			for k, old := range r.windows {
				if now.Sub(old.start) >= r.window {
					delete(r.windows, k)
				}
			}
			if len(r.windows) >= r.maxKeys {
				// Still full: every tracked key is live. Refuse rather than
				// grow, since growing is the exhaustion this cap prevents.
				return false
			}
		}
		w = &rateWindow{start: now}
		r.windows[key] = w
	}
	if w.n >= r.limit {
		return false
	}
	w.n++
	return true
}

// RequestSource identifies a caller for rate limiting: the TCP peer address,
// never a header.
//
// X-Forwarded-For is deliberately ignored. It is caller-supplied, so honoring
// it lets one source present a fresh identity per request and walk around the
// very limit being imposed — which is the difference between a limiter and the
// appearance of one. Behind a reverse proxy every caller shares the proxy's
// address, which throttles harder rather than less, and that is the safe
// direction to be wrong in.
func RequestSource(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || host == "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// TooManyRequests writes a 429 with a Retry-After, so a well-behaved client
// backs off instead of hammering.
func TooManyRequests(w http.ResponseWriter, retryAfter time.Duration, msg string) {
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	w.Header().Set("Retry-After", itoaSeconds(retryAfter))
	http.Error(w, msg, http.StatusTooManyRequests)
}

func itoaSeconds(d time.Duration) string {
	n := int(d / time.Second)
	if n < 1 {
		n = 1
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if digits == "" {
		digits = "1"
	}
	return digits
}
