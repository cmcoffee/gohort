package media

// An address that answered 429 (a peer key's per-minute ceiling, a hosted
// API's quota) is paused until the time its Retry-After names, per host. Every
// call to it waits that out instead of sending into the refusal: a repair pass
// that re-embedded one chunk after another hit a peer key's 600 a minute,
// counted each refusal as a failure, and five in a row ended the pass as
// though the endpoint were down. The service connectors' transport and the
// peer embedder both keep their pauses here, so a pass reads one place.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

var (
	pauseMu sync.Mutex
	pauses  = map[string]time.Time{}
)

// pauseDefault is the pause when a 429 names no Retry-After.
const pauseDefault = 10 * time.Second

// pauseKey is the host an address's pause is kept under: its scheme and
// host, so every path on one server shares the server's limit.
func pauseKey(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(rawURL)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// NotePause records a 429 from rawURL: its host is paused for retryAfter
// (seconds, or an HTTP date), capped at a minute.
func NotePause(rawURL, retryAfter string) {
	wait := pauseDefault
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s > 0 {
		wait = time.Duration(s) * time.Second
	} else if t, err := http.ParseTime(strings.TrimSpace(retryAfter)); err == nil {
		wait = time.Until(t)
	}
	wait = min(max(wait, time.Second), time.Minute)
	key := pauseKey(rawURL)
	until := time.Now().Add(wait)
	pauseMu.Lock()
	if until.After(pauses[key]) {
		pauses[key] = until
	}
	pauseMu.Unlock()
	nfo.Log("[rate-limit] %s is rate limiting: pausing calls to it for %s", key, wait.Round(time.Second))
}

// PausedFor is how long calls to rawURL's host still wait, or 0.
func PausedFor(rawURL string) time.Duration {
	key := pauseKey(rawURL)
	pauseMu.Lock()
	defer pauseMu.Unlock()
	if d := time.Until(pauses[key]); d > 0 {
		return d
	}
	delete(pauses, key)
	return 0
}

// WaitPause waits out rawURL's pause. A deadline that ends first is the
// error, without sending: a call that cannot wait (a turn's query embed)
// fails fast instead of adding to the refusals.
func WaitPause(ctx context.Context, rawURL string) error {
	d := PausedFor(rawURL)
	if d <= 0 {
		return nil
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return fmt.Errorf("rate limited by %s for another %s", pauseKey(rawURL), d.Round(time.Second))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
