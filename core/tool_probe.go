package core

// Probing what an agent reaches for.
//
// A run carrying a ToolProbe stops at the first call its watch picks out,
// records the call, and makes nothing: the call's handler never runs and the
// run is cancelled. Calls the watch passes over run as usual, so an agent
// can read help, list or look things up on its way to the choice that is
// being probed. What the probe answers is what the agent DID first, not
// what it says it would do: the two are not the same, and the doing is what
// a prompt has to get right.
//
// It rides on the context, so it reaches every tool call of the run through
// safeInvoke, the one place tools are invoked.

import (
	"context"
	"sync"
)

// ProbeCall is one tool call a probe saw.
type ProbeCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// ToolProbe watches a run's tool calls for the first one it is looking for.
type ToolProbe struct {
	watch  func(name string, args map[string]any) bool
	cancel context.CancelFunc

	mu   sync.Mutex
	seen []ProbeCall
	hit  *ProbeCall
}

type toolProbeKey struct{}

// WithToolProbe returns a context whose runs stop at the first tool call
// watch picks out, and the probe that records it. Cancel the context when
// done with it, as with any WithCancel.
func WithToolProbe(ctx context.Context, watch func(name string, args map[string]any) bool) (context.Context, *ToolProbe, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	p := &ToolProbe{watch: watch, cancel: cancel}
	return context.WithValue(ctx, toolProbeKey{}, p), p, cancel
}

func toolProbeFrom(ctx context.Context) *ToolProbe {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(toolProbeKey{}).(*ToolProbe)
	return p
}

// observe records a call and says whether it is to be stopped: the one the
// watch picks out, and anything after it.
func (p *ToolProbe) observe(name string, args map[string]any) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hit != nil {
		return true
	}
	call := ProbeCall{Name: name, Args: args}
	p.seen = append(p.seen, call)
	if p.watch != nil && p.watch(name, args) {
		p.hit = &call
		p.cancel()
		return true
	}
	return false
}

// Hit is the call the probe stopped at, if the run reached one.
func (p *ToolProbe) Hit() (ProbeCall, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hit == nil {
		return ProbeCall{}, false
	}
	return *p.hit, true
}

// Seen is every call the run made up to and including the stop, in order.
func (p *ToolProbe) Seen() []ProbeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ProbeCall(nil), p.seen...)
}

// probeStoppedText is what a stopped call answers, for the moment before the
// cancelled run notices.
const probeStoppedText = "Stopped here: this run is a probe of what you reach for first, and the call was recorded, not made."
