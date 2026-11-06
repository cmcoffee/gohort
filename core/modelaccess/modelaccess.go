// Package modelaccess is the per-user limits on Model access: raw use of the
// deployment's models with no agent in front, the Ollama proxy and the Model
// API's worker and lead tiers (docs/design/external-access.md). Raw compute has
// no persona, no guardrails and no agent's spend limit, so every request counts
// against limits an administrator sets. Who may use it at all is the Closed
// feature core.ModelAccessFeature, in Feature Access.
//
// A leaf package rather than a file in core for the reason core/notices is:
// core sits at its file ceiling.
package modelaccess

import (
	"fmt"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/netgate"
)

// Where the limits are kept, in the auth store's settings table.
const (
	perMinuteKey = "model_access_per_minute"
	atOnceKey    = "model_access_at_once"
)

// The limits until an administrator sets others.
const (
	DefaultPerMinute = 30
	DefaultAtOnce    = 2
)

// Limits are the per-user limits on raw model use.
type Limits struct {
	PerMinute int `json:"per_minute"` // requests started per minute
	AtOnce    int `json:"at_once"`    // requests running at once
}

// Load is the limits now: what an administrator set, else the defaults. A zero
// or negative stored value reads as the default, never as unlimited.
func Load() Limits {
	l := Limits{PerMinute: DefaultPerMinute, AtOnce: DefaultAtOnce}
	if core.AuthDB == nil {
		return l
	}
	db := core.AuthDB()
	if db == nil {
		return l
	}
	var n int
	if db.Get(core.WebTable, perMinuteKey, &n) && n > 0 {
		l.PerMinute = n
	}
	n = 0
	if db.Get(core.WebTable, atOnceKey, &n) && n > 0 {
		l.AtOnce = n
	}
	return l
}

// Save stores the limits. Administrator-authorized at the caller. A value of
// zero or less is refused rather than read as unlimited.
func Save(l Limits) error {
	if l.PerMinute <= 0 || l.AtOnce <= 0 {
		return fmt.Errorf("each limit is at least 1")
	}
	if core.AuthDB == nil || core.AuthDB() == nil {
		return fmt.Errorf("the store is not ready")
	}
	db := core.AuthDB()
	db.Set(core.WebTable, perMinuteKey, l.PerMinute)
	db.Set(core.WebTable, atOnceKey, l.AtOnce)
	return nil
}

// The limiters, rebuilt when the limits change so a new number applies at once.
var state struct {
	mu     sync.Mutex
	limits Limits
	rate   *core.RateLimiter
	busy   *netgate.InFlight
}

// Admit counts one raw model request for user against the limits, returning
// the func that ends it, or why it is refused: the per-minute limit or the
// at-once one, said so a client can tell which. Every Model access surface
// admits through here, so the limits are one across them.
func Admit(user string) (release func(), refusal string) {
	l := Load()
	state.mu.Lock()
	switch {
	case state.rate == nil:
		state.limits = l
		state.rate = core.NewRateLimiter(l.PerMinute, time.Minute)
		state.busy = netgate.NewInFlight(l.AtOnce)
	case state.limits != l:
		// Changed in place. Rebuilt, every count went back to zero: a
		// request running released into the old limiter, the new one
		// started empty, and for a moment anyone could run twice the
		// at-once limit and have a fresh minute.
		state.limits = l
		state.rate.SetLimit(l.PerMinute)
		state.busy.SetMax(l.AtOnce)
	}
	rate, busy := state.rate, state.busy
	state.mu.Unlock()
	if !rate.Allow(user) {
		return func() {}, fmt.Sprintf("model access: more than %d requests this minute; wait and try again", l.PerMinute)
	}
	release, ok := busy.Acquire(user)
	if !ok {
		return func() {}, fmt.Sprintf("model access: %d requests already running; wait for one to finish", l.AtOnce)
	}
	return release, ""
}
