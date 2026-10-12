package orchestrate

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// A call made beneath a dispatch names the dispatch's card, so the page draws
// it there; a dispatch reports its round and its thinking time on the tick,
// and never the reasoning itself.
func TestADispatchReportsOnItsOwnCard(t *testing.T) {
	buf := &bytes.Buffer{}
	turn := &chatTurn{sse: &sseWriter{live: buf}}

	turn.emitToolCallUnder("parent-1", "m1", "web_search", map[string]any{"q": "x"}, "", "↳ [Researcher] ")
	if !strings.Contains(buf.String(), `"parent_call_id":"parent-1"`) {
		t.Fatalf("a child call should name its parent: %s", buf.String())
	}
	buf.Reset()
	turn.emitToolCall("m1", "agents", nil, "")
	if strings.Contains(buf.String(), "parent_call_id") {
		t.Error("a top-level call named a parent")
	}

	card := toolCard{msgID: "m1", callID: "dispatch-1"}
	if got := toolCardFrom(withToolCard(context.Background(), card)); got != card {
		t.Errorf("the card did not ride the context: %+v", got)
	}

	buf.Reset()
	p := turn.newDispatchProgress(card, "Researcher")
	p.roundStart()
	if !strings.Contains(buf.String(), `"kind":"tool_progress"`) || !strings.Contains(buf.String(), `"round":1`) || !strings.Contains(buf.String(), `"call_id":"dispatch-1"`) {
		t.Fatalf("round start: %s", buf.String())
	}
	buf.Reset()
	p.chunk(strings.Repeat("x", 400))
	for i := 0; i < 20; i++ {
		p.chunk("secret reasoning ")
	}
	if n := strings.Count(buf.String(), `"tool_progress"`); n != 1 {
		t.Errorf("a burst of reasoning inside one tick sent %d events, want 1", n)
	}
	if !strings.Contains(buf.String(), `"thinking_ms"`) || !strings.Contains(buf.String(), `"tokens":100`) {
		t.Errorf("thinking progress: %s", buf.String())
	}
	if strings.Contains(buf.String(), "secret reasoning") {
		t.Error("the sub-agent's reasoning reached the page")
	}
	p.mu.Lock()
	p.lastSent = time.Now().Add(-thinkTick)
	p.mu.Unlock()
	p.chunk("more")
	if n := strings.Count(buf.String(), `"tool_progress"`); n != 2 {
		t.Errorf("a tick later it should report again: %d", n)
	}

	if (*dispatchProgress)(nil) != turn.newDispatchProgress(toolCard{}, "x") {
		t.Error("no card, no reporter")
	}
}
