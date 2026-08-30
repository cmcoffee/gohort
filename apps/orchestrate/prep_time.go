// How long the user waited before their message reached a model.
//
// The turn already accounts for itself once the loop is running: "[agent_loop]
// turn time: 3.268s total = 2.78s in 1 LLM call(s) + 488ms gohort (14%)". That
// clock starts INSIDE RunAgentLoop, with the system prompt already assembled,
// the tool catalog already built and the recall already done — so everything
// between the request landing and that point was unmeasured, and a turn that
// spent four seconds there reported 14% overhead.
//
// Observed: a request logged 8.432s end to end while the loop claimed 3.268s.
// The missing 4.4 seconds were a knowledge recall, which said so in a line of
// its own — but only somebody who already suspected recall would have gone
// looking for it. This puts the total on the critical path where the question
// is asked, and names the phases that reported, so the next one does not need
// to be guessed at.

package orchestrate

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// prepClock measures request-received to first-LLM-call, and collects what the
// phases in between say they cost.
//
// Phases report into it rather than it reaching into them: each already knows
// its own boundaries, and several are optional or run concurrently. What is not
// reported still shows up, in the gap between the total and the parts — which
// is the useful property, because the next slow phase is by definition one
// nobody has instrumented yet.
type prepClock struct {
	mu       sync.Mutex
	received time.Time
	marks    []prepMark
	logged   bool
}

type prepMark struct {
	name string
	d    time.Duration
}

// startPrepClock stamps arrival. Called at the top of the send handler, before
// any work — a clock started after the first phase cannot measure it.
func startPrepClock() *prepClock { return &prepClock{received: time.Now()} }

// mark records what one phase cost. Safe on a nil clock so a caller reached
// from a path that has none (a fire, a test) needs no branch of its own.
func (p *prepClock) mark(name string, d time.Duration) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.marks = append(p.marks, prepMark{name, d})
}

// done logs the wait, once. Called where the turn's own model call begins,
// which is the moment the person stops waiting on gohort and starts waiting on
// the model.
//
// Once, because the loop calls a model many times per turn and only the first
// one measures a person waiting. Later rounds are the turn working, and
// [agent_loop] turn time already accounts for those.
func (p *prepClock) done() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.logged || p.received.IsZero() {
		return
	}
	p.logged = true
	p.render(func(line string) {
		if time.Since(p.received) >= prepSlowThreshold {
			Log("%s — the user waited this long before the model saw the message; the largest phase above is the one to fix", line)
		} else {
			Debug("%s", line)
		}
	})
}

// render composes the line and hands it to emit. Split out so a test can read
// what would be logged: the arithmetic here — which phases are named, what the
// remainder is, that it never goes negative — is the part worth pinning down.
// Caller holds the lock.
func (p *prepClock) render(emit func(string)) {
	total := time.Since(p.received)

	var parts []string
	var named time.Duration
	for _, m := range p.marks {
		parts = append(parts, fmt.Sprintf("%s %s", m.name, m.d.Round(time.Millisecond)))
		named += m.d
	}
	// The remainder is everything nobody measured: request parsing, session
	// load, attachment extraction, history assembly. Reported rather than
	// hidden, because a large one is the signal that the next thing to
	// instrument lives in there. Concurrent phases can push the named total
	// past the wall clock, so it is floored rather than shown negative.
	rest := total - named
	if rest < 0 {
		rest = 0
	}
	parts = append(parts, fmt.Sprintf("other %s", rest.Round(time.Millisecond)))

	emit(fmt.Sprintf("[orchestrate.orch] prep time: %s from request received to the turn's first LLM call (%s)",
		total.Round(time.Millisecond), strings.Join(parts, ", ")))
}

// prepSlowThreshold is where a wait stops being invisible to the person having
// it. Under a second reads as the message sending; two seconds reads as a
// stall, and by then it is worth a line somebody will see without asking.
const prepSlowThreshold = 2 * time.Second
