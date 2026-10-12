package orchestrate

// What a dispatched sub-agent is doing, shown on the call that dispatched it.
//
// A dispatch used to read, to the person watching, as one tool call and the
// bare thinking dots for as long as the sub-agent ran: its own tool calls went
// into the same flat list as the parent's, and nothing said which round it was
// on or that it was thinking. Now each call a sub-agent makes is drawn under
// the call that started it, and the dispatch reports its round and its
// thinking time on the same two-second tick the parent's thinking line uses.
//
// What crosses: the round, which tool, how long it has thought and roughly how
// much. Never the sub-agent's interim prose (the guardrail check that would
// have to run on it is skipped when nobody sees it) and never its reasoning
// text, which the parent's own thinking line does not show either.

import (
	"context"
	"sync"
	"time"
)

// toolCard is the chip a running tool call is drawn as: which reply bubble it
// sits in, and its id. A call carries its card on the context it runs with,
// so anything that runs beneath it (a dispatched agent's tool calls) can say
// which card it belongs under.
type toolCard struct {
	msgID  string
	callID string
}

type toolCardKey struct{}

func withToolCard(ctx context.Context, c toolCard) context.Context {
	if ctx == nil || c.callID == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCardKey{}, c)
}

func toolCardFrom(ctx context.Context) toolCard {
	if ctx == nil {
		return toolCard{}
	}
	c, _ := ctx.Value(toolCardKey{}).(toolCard)
	return c
}

// dispatchProgress reports one dispatched agent's progress onto its card.
type dispatchProgress struct {
	t     *chatTurn
	card  toolCard
	agent string

	mu        sync.Mutex
	round     int
	spanStart time.Time
	chars     int
	lastSent  time.Time
}

// newDispatchProgress is nil when there is no card to report on (a hidden
// call, or no bubble), and every method is safe on nil.
func (t *chatTurn) newDispatchProgress(card toolCard, agent string) *dispatchProgress {
	if t == nil || card.callID == "" {
		return nil
	}
	return &dispatchProgress{t: t, card: card, agent: agent}
}

func (p *dispatchProgress) send(round int, extra map[string]any) {
	ev := map[string]any{
		"kind":    "tool_progress",
		"msg_id":  p.card.msgID,
		"call_id": p.card.callID,
		"agent":   p.agent,
		"round":   round,
	}
	for k, v := range extra {
		ev[k] = v
	}
	p.t.sse.Send(ev)
}

// roundStart begins a round: its thinking is timed from its own first chunk.
func (p *dispatchProgress) roundStart() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.round++
	round := p.round
	p.spanStart, p.chars, p.lastSent = time.Time{}, 0, time.Time{}
	p.mu.Unlock()
	p.send(round, nil)
}

// chunk counts reasoning and reports on the thinking tick, at once for the
// first chunk of a round so "Thinking" shows the moment it starts.
func (p *dispatchProgress) chunk(s string) {
	if p == nil {
		return
	}
	now := time.Now()
	p.mu.Lock()
	if p.spanStart.IsZero() {
		p.spanStart = now
	}
	p.chars += len(s)
	due := p.lastSent.IsZero() || now.Sub(p.lastSent) >= thinkTick
	var extra map[string]any
	round := p.round
	if due {
		p.lastSent = now
		extra = map[string]any{
			"thinking_ms": now.Sub(p.spanStart).Milliseconds(),
			"started_ms":  p.spanStart.UnixMilli(),
			"tokens":      p.chars / thinkCharsPerToken,
		}
	}
	p.mu.Unlock()
	if extra != nil {
		p.send(round, extra)
	}
}
