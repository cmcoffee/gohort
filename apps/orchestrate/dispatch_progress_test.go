package orchestrate

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
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
	if got := toolCardFrom(withToolCard(context.Background(), card)); got.msgID != card.msgID || got.callID != card.callID {
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

// A run with no stream of its own (delegate, a pipeline stage) reports through
// the card it was started under: the round and the tool each round ran.
func TestASyncDispatchReportsThroughItsCard(t *testing.T) {
	var got []map[string]any
	card := toolCard{msgID: "m1", callID: "d1", send: func(ev map[string]any) { got = append(got, ev) }}
	ctx := withToolCard(context.Background(), card)

	p := dispatchProgressFrom(ctx, "Writer")
	roundStart, reasoning := p.hooks()
	if roundStart == nil || reasoning == nil {
		t.Fatal("a run under a card should report")
	}
	roundStart()
	p.step(StepInfo{Round: 1, ToolCalls: []ToolCall{{Name: "fetch_url"}, {Name: "web_search"}}})
	if len(got) != 2 || got[1]["tool"] != "web_search" || got[1]["call_id"] != "d1" || got[1]["agent"] != "Writer" {
		t.Errorf("events: %+v", got)
	}
	if p := dispatchProgressFrom(context.Background(), "Writer"); p != nil {
		t.Error("a run with no card reported")
	}
	if rs, r := (*dispatchProgress)(nil).hooks(); rs != nil || r != nil {
		t.Error("no reporter, no hooks")
	}
}

// A run that reports its own steps in words (a publish) puts each on its
// card's live line, in the round it is in; with no card it says nothing.
func TestAStepGoesOnTheCardsLiveLine(t *testing.T) {
	var sent []map[string]any
	card := toolCard{msgID: "m1", callID: "c1", send: func(ev map[string]any) { sent = append(sent, ev) }}
	p := dispatchProgressOn(card, "Publishing")
	roundStart, _ := p.hooks()
	roundStart()
	p.say("Update a Confluence page · Runbook")
	last := sent[len(sent)-1]
	if last["kind"] != "tool_progress" || last["call_id"] != "c1" || last["agent"] != "Publishing" ||
		last["round"] != 1 || last["tool"] != "Update a Confluence page · Runbook" {
		t.Errorf("the step did not reach the card: %+v", last)
	}
	var none *dispatchProgress
	none.say("nobody is watching") // must not panic
}

// A publish run with no card (Scribe's publish job) lists each round as a step
// and says its round, its thinking and the step it is on as the publish's
// current activity.
func TestAPublishRunWithNoCardReportsItsRounds(t *testing.T) {
	var steps, now []string
	ctx := docs.WithPublishSteps(context.Background(), func(s string) { steps = append(steps, s) })
	ctx = docs.WithPublishActivity(ctx, func(s string) { now = append(now, s) })
	l := &publishLive{ctx: ctx}
	l.say("Publishing through wikiapi, with 1 of its tools")
	l.roundStart()
	l.chunk(strings.Repeat("x", 400))
	l.say("Update a page · Runbook")
	if strings.Join(steps, "|") != "Round 1" {
		t.Errorf("steps = %q", steps)
	}
	want := []string{"Publishing through wikiapi, with 1 of its tools", "round 1", "round 1 · Thinking · ~100 tokens", "round 1 · Update a page · Runbook"}
	if strings.Join(now, "|") != strings.Join(want, "|") {
		t.Errorf("activity = %q\nwant       %q", now, want)
	}
}
