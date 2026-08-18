package orchestrate

import (
	"encoding/json"
	"testing"
	"time"
)

func cardAt(from string, sec int) ChatMessage {
	return ChatMessage{
		Role: "assistant", ReportFrom: from, Content: "something happened",
		Created: time.Date(2026, 8, 17, 12, 0, sec, 0, time.UTC),
	}
}

func turnAt(sec int) ChatMessage {
	return ChatMessage{
		Role: "user", Content: "a chat turn",
		Created: time.Date(2026, 8, 17, 12, 0, sec, 0, time.UTC),
	}
}

// The poll wants cards, not the thread. Chat turns ride SSE and the replay, so
// serving them here is pure transfer the client throws away.
func TestObservationCardsSkipsChatTurns(t *testing.T) {
	got := observationCardsSince([]ChatMessage{
		turnAt(1), cardAt("Dana · iPhone", 2), turnAt(3), cardAt("Monitor", 4),
	}, "")
	if len(got) != 2 {
		t.Fatalf("want 2 cards, got %d", len(got))
	}
	if got[0].ReportFrom != "Dana · iPhone" || got[1].ReportFrom != "Monitor" {
		t.Errorf("wrong cards or order: %+v", got)
	}
}

// `since` is what turns a six-second refetch of the whole thread into an empty
// answer. Strictly after, so the boundary card is not re-sent.
func TestObservationCardsSince(t *testing.T) {
	msgs := []ChatMessage{cardAt("a", 1), cardAt("b", 2), cardAt("c", 3)}
	boundary := msgs[1].Created.Format(time.RFC3339Nano)
	got := observationCardsSince(msgs, boundary)
	if len(got) != 1 || got[0].ReportFrom != "c" {
		t.Fatalf("want only the card after the boundary, got %+v", got)
	}
	// Caught up: the steady state is nothing at all.
	got = observationCardsSince(msgs, msgs[2].Created.Format(time.RFC3339Nano))
	if len(got) != 0 {
		t.Errorf("want no cards when caught up, got %+v", got)
	}
}

// An unparseable or absent `since` must serve everything rather than nothing —
// a client with no watermark yet is asking for the set it does not have.
func TestObservationCardsBadSinceServesAll(t *testing.T) {
	msgs := []ChatMessage{cardAt("a", 1), cardAt("b", 2)}
	for _, since := range []string{"", "   ", "not-a-time", "0"} {
		if got := observationCardsSince(msgs, since); len(got) != 2 {
			t.Errorf("since=%q: want all 2 cards, got %d", since, len(got))
		}
	}
}

// The client reads this array by a configurable field name defaulting to
// "Messages" — the exact tag ChatSession uses. A lowercase key here would read
// as a poll that returns nothing, forever, with no error anywhere.
func TestCardsPayloadFieldMatchesSession(t *testing.T) {
	b, err := json.Marshal(cardsPayload{Messages: []ChatMessage{cardAt("a", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["Messages"]; !ok {
		t.Fatalf("payload must use the \"Messages\" key like ChatSession does: %s", b)
	}
	// And an empty result stays an empty ARRAY, not a missing field.
	b, _ = json.Marshal(cardsPayload{Messages: []ChatMessage{}})
	raw = map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &raw)
	if string(raw["Messages"]) != "[]" {
		t.Errorf("empty poll should serve [], got %s", b)
	}
}

// A cortex thread gets its own tighter default; an explicit client limit still
// wins, or "load earlier" could never walk back through one.
func TestResolveTailLimitForCortex(t *testing.T) {
	if got, want := resolveTailLimitFor("", true), cortexTailLimit(); got != want {
		t.Errorf("cortex default = %d, want %d", got, want)
	}
	if got, want := resolveTailLimitFor("", false), sessionTailLimit(); got != want {
		t.Errorf("session default = %d, want %d", got, want)
	}
	if got := resolveTailLimitFor("500", true); got != 500 {
		t.Errorf("explicit limit ignored on a cortex thread: got %d", got)
	}
	if got := resolveTailLimitFor("0", true); got != 0 {
		t.Errorf("explicit 0 (whole thread) must survive the cortex default: got %d", got)
	}
}

// ChunkSize must be the thread's OWN default, never the accumulated limit the
// request carried. Echoing the request back would make each "show earlier"
// press add as much as everything already loaded — the doubling this replaced.
func TestChunkSizeIsTheThreadDefaultNotTheRequest(t *testing.T) {
	// A "show earlier" request carries a large accumulated limit...
	if got := resolveTailLimitFor("640", true); got != 640 {
		t.Fatalf("the request limit should still govern what is SERVED: %d", got)
	}
	// ...while the chunk advertised back stays one thread-default step.
	if got, want := resolveTailLimitFor("", true), cortexTailLimit(); got != want {
		t.Errorf("cortex chunk = %d, want %d", got, want)
	}
	if got, want := resolveTailLimitFor("", false), sessionTailLimit(); got != want {
		t.Errorf("session chunk = %d, want %d", got, want)
	}
}
