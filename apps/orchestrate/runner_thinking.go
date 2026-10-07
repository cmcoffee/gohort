package orchestrate

import (
	"sync"
	"time"
)

// thinkProgress turns the model's reasoning stream into something a person
// can watch. A thinking model can reason for most of a minute before it says
// anything, and until now the only sign of life in that time was three
// bouncing dots: no elapsed time, no sense of how far along it was.
//
// It never shows the reasoning itself, only that it is happening, for how
// long and roughly how much: a "thinking" event on a ~2s tick (the run buffer
// replays events to a page that rejoins, so a tick per chunk would crowd out
// the conversation). It also totals the time spent thinking across the turn,
// for the stats footer.
//
// Usable at its zero value, so every way a chatTurn is built gets it.
type thinkProgress struct {
	mu        sync.Mutex
	spanStart time.Time // first reasoning chunk of the current call
	last      time.Time // latest reasoning chunk
	chars     int       // reasoning received in the current span
	doneMS    int64     // thinking time of spans already closed
	lastSent  time.Time
}

// thinkTick is how often progress is reported while the model thinks.
const thinkTick = 2 * time.Second

// thinkCharsPerToken turns streamed reasoning into an approximate token count.
// The real count arrives with the response; this is only for the live line,
// which says "~" so it does not read as exact.
const thinkCharsPerToken = 4

// chunk records a reasoning chunk and, when a tick is due, reports progress.
// The first chunk of a span reports at once, so the indicator says
// "Thinking" the moment the model starts rather than two seconds later.
func (t *chatTurn) thinkChunk(s string) {
	p := &t.think
	now := time.Now()
	p.mu.Lock()
	if p.spanStart.IsZero() {
		p.spanStart = now
	}
	p.last = now
	p.chars += len(s)
	due := p.lastSent.IsZero() || now.Sub(p.lastSent) >= thinkTick
	var payload map[string]any
	if due {
		p.lastSent = now
		payload = map[string]any{
			"kind":       "thinking",
			"elapsed_ms": now.Sub(p.spanStart).Milliseconds(),
			"tokens":     p.chars / thinkCharsPerToken,
		}
	}
	p.mu.Unlock()
	if payload != nil {
		t.sse.Send(payload)
	}
}

// thinkNewCall closes the current span, if any. Called at the start of every
// round, so a second call's thinking is timed from its own first chunk rather
// than across the tool run in between.
func (t *chatTurn) thinkNewCall() {
	p := &t.think
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeSpan()
}

func (p *thinkProgress) closeSpan() {
	if !p.spanStart.IsZero() {
		p.doneMS += p.last.Sub(p.spanStart).Milliseconds()
	}
	p.spanStart, p.last, p.chars, p.lastSent = time.Time{}, time.Time{}, 0, time.Time{}
}

// thinkTotalMS is the time the model spent thinking this turn, the current
// span included.
func (t *chatTurn) thinkTotalMS() int64 {
	p := &t.think
	p.mu.Lock()
	defer p.mu.Unlock()
	total := p.doneMS
	if !p.spanStart.IsZero() {
		total += p.last.Sub(p.spanStart).Milliseconds()
	}
	return total
}
