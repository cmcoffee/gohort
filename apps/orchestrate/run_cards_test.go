package orchestrate

// A card is drawn early in a run and updated in place, so a long run pushes
// its frame out of the event ring. A page that rejoined the run (reopened on a
// phone, mid-build) got every frame after that and never drew the card:
// Builder's plan came back as a blank bar. The ring now keeps each card's
// latest frame and hands back the ones it has dropped, first.

import (
	"encoding/json"
	"strings"
	"testing"
)

func runFrame(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte("data: "), b...), '\n', '\n')
}

func TestARejoinedRunStillDrawsItsCards(t *testing.T) {
	r := NewRunRegistry().Create("u", "a", "s", nil)
	r.Append(runFrame(t, map[string]any{"kind": "block", "type": "orchestrate_plan", "id": "plan-1", "plan": []string{"step one"}}))
	r.Append(runFrame(t, map[string]any{"kind": "block", "type": "orchestrate_plan", "id": "plan-1", "plan": []string{"step one", "step two"}}))
	r.Append(runFrame(t, map[string]any{"kind": "block", "type": "orchestrate_intent", "id": "intent-1", "text": "looking"}))
	// A build's worth of streaming: far more than the ring holds.
	for i := 0; i < runMaxEvents()+200; i++ {
		r.Append(runFrame(t, map[string]any{"kind": "chunk", "id": "m1", "text": "x"}))
	}
	sub := r.Subscribe(0)
	defer r.Unsubscribe(sub)
	if len(sub.Backlog) < 2 {
		t.Fatalf("backlog has %d frames", len(sub.Backlog))
	}
	first, second := string(sub.Backlog[0].Frame), string(sub.Backlog[1].Frame)
	if !strings.Contains(first, `"id":"plan-1"`) || !strings.Contains(first, "step two") {
		t.Errorf("the plan's latest frame does not lead the rejoin: %s", first)
	}
	if !strings.Contains(second, `"id":"intent-1"`) {
		t.Errorf("the second card was not handed back: %s", second)
	}
	plans := 0
	for _, ev := range sub.Backlog {
		if strings.Contains(string(ev.Frame), `"id":"plan-1"`) {
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("the plan came back %d times; only its latest frame belongs in a rejoin", plans)
	}

	// A page that already has everything up to the end gets no cards again.
	if again := r.Subscribe(sub.Backlog[len(sub.Backlog)-1].Seq); len(again.Backlog) != 0 {
		t.Errorf("a caught-up page was handed %d frames", len(again.Backlog))
		r.Unsubscribe(again)
	}
}
