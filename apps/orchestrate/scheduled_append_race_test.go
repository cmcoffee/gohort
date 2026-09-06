package orchestrate

import (
	"fmt"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestAppendToStoredSessionKeepsConcurrentCards: two scheduled fires on the
// same thread each loaded the session before a long run, then appended their
// card to that stale copy and saved — the second save erased the first's
// card. Both fires now append through the locked re-read, so every card lands
// regardless of which finishes last.
func TestAppendToStoredSessionKeepsConcurrentCards(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	seed := ChatSession{ID: "s1", AgentID: "ag", Messages: []ChatMessage{{Role: "user", Content: "hello"}}}
	if _, err := saveChatSession(db, seed); err != nil {
		t.Fatal(err)
	}
	// Every fire holds the same stale snapshot, taken before any of them ran.
	stale, _ := loadChatSession(db, "ag", "s1")

	const fires = 8
	var wg sync.WaitGroup
	for i := 0; i < fires; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			card := ChatMessage{Role: "assistant", Content: fmt.Sprintf("card %d", i), ReportFrom: fmt.Sprintf("task %d", i)}
			if err := appendToStoredSession(db, "ag", "s1", stale, card); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	got, ok := loadChatSession(db, "ag", "s1")
	if !ok {
		t.Fatal("session vanished")
	}
	if len(got.Messages) != 1+fires {
		t.Fatalf("stored thread has %d messages, want %d (seed + one card per fire); a fire's card was overwritten", len(got.Messages), 1+fires)
	}
	seen := map[string]bool{}
	for _, m := range got.Messages[1:] {
		seen[m.ReportFrom] = true
	}
	for i := 0; i < fires; i++ {
		if !seen[fmt.Sprintf("task %d", i)] {
			t.Errorf("card from task %d missing", i)
		}
	}
}

// TestAppendToStoredSessionSynthesizesMissingThread: a cortex home thread that
// has never been posted to does not exist in the store; the fallback the
// caller synthesized is what gets materialized, with the card on it.
func TestAppendToStoredSessionSynthesizesMissingThread(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	fallback := ChatSession{ID: "channel:ag", AgentID: "ag"}
	if err := appendToStoredSession(db, "ag", "channel:ag", fallback, ChatMessage{Role: "assistant", Content: "first card"}); err != nil {
		t.Fatal(err)
	}
	got, ok := loadChatSession(db, "ag", "channel:ag")
	if !ok || len(got.Messages) != 1 || got.Messages[0].Content != "first card" {
		t.Fatalf("thread not materialized from fallback: ok=%v msgs=%+v", ok, got.Messages)
	}
}
