package orchestrate

import (
	"context"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A waiting poll answers the moment a card is saved to the thread, not on the
// next tick, and answers empty once cardsWait passes with nothing new.
func TestAWaitingPollAnswersWhenACardLands(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	sess, err := saveChatSession(udb, ChatSession{AgentID: "a1", Messages: []ChatMessage{{Role: "user", Content: "draw a ship"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan ChatSession, 1)
	go func() { done <- waitForCards(context.Background(), udb, "a1", sess.ID, sess, "") }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	card := ChatMessage{Role: "assistant", Content: "here it is", ReportFrom: "background task", Created: time.Now()}
	if err := appendToStoredSession(udb, "a1", sess.ID, sess, card); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if len(observationCardsSince(got.Messages, "")) != 1 {
			t.Fatalf("woke without the card: %+v", got.Messages)
		}
		if time.Since(start) > time.Second {
			t.Errorf("took %v to answer after the card was saved", time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the poll did not answer when the card landed")
	}

	prev := cardsWait
	cardsWait = 100 * time.Millisecond
	t.Cleanup(func() { cardsWait = prev })
	since := card.Created.Format(time.RFC3339Nano)
	got := waitForCards(context.Background(), udb, "a1", sess.ID, sess, since)
	if n := len(observationCardsSince(got.Messages, since)); n != 0 {
		t.Errorf("nothing new, yet %d card(s) came back", n)
	}
}
