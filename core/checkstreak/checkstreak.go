// Package checkstreak notices a check whose model keeps giving no verdict.
//
// A check that cannot read its model's reply falls back to what it does
// without one: the memory gate keeps the note unjudged, the conflict check
// saves the finding unflagged. Each fallback is right once, and said only in
// the log, so a model that declines a check every time (a refusal, a reply
// in another shape) went on doing it unseen. After a run of them in a row on
// one model, OnStreak is told once; a verdict from that model ends the run.
//
// A leaf of core: core and core/decide both report here, and core sits at
// its export ceiling.
package checkstreak

import (
	"context"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/llmcalls"
)

// After is how many replies in a row on one model gave no verdict before
// OnStreak is told.
const After = 3

// OnStreak is told once per run of After or more replies in a row that gave
// no verdict: the check's key, the model, why the last one gave none, and
// how many in a row. Set by core/decide, which tells the admins. Nil: no one.
var OnStreak func(check, model, reason string, n int)

type run struct {
	n    int
	told bool
}

var (
	mu   sync.Mutex
	runs = map[string]*run{}
)

func runKey(check, model string) string { return check + "\x00" + model }

// Failed counts one reply from model that gave check no verdict, reason
// being why. A check test's calls (llmcalls.Watched) ask a model on purpose
// and are not counted.
func Failed(ctx context.Context, check, model, reason string) {
	if llmcalls.Watched(ctx) || strings.TrimSpace(check) == "" {
		return
	}
	k := runKey(check, model)
	mu.Lock()
	r := runs[k]
	if r == nil {
		r = &run{}
		runs[k] = r
	}
	r.n++
	tell := r.n >= After && !r.told
	if tell {
		r.told = true
	}
	n := r.n
	mu.Unlock()
	if tell && OnStreak != nil {
		OnStreak(check, model, reason, n)
	}
}

// Answered ends check's run on model: it gave a verdict.
func Answered(ctx context.Context, check, model string) {
	if llmcalls.Watched(ctx) {
		return
	}
	mu.Lock()
	delete(runs, runKey(check, model))
	mu.Unlock()
}
