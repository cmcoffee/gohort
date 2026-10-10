package orchestrate

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
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
	go func() { done <- waitForCards(context.Background(), udb, "a1", sess.ID, sess, "", "", nil) }()
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
	got := waitForCards(context.Background(), udb, "a1", sess.ID, sess, since, "", nil)
	if n := len(observationCardsSince(got.Messages, since)); n != 0 {
		t.Errorf("nothing new, yet %d card(s) came back", n)
	}
}

// A run working for a thread in the background is the thread's to show: the
// registry finds it by session without it being the session's turn (which
// would cancel the person's own), a waiting poll answers the moment it starts
// or ends, and the poll's answer carries it.
func TestABackgroundRunIsShownOnItsThread(t *testing.T) {
	rr := NewRunRegistry()
	_, turn := rr.CreateCancellable(context.Background(), "u", "a1", "s1") // the person's own turn
	_, wake := rr.CreateCancellable(context.Background(), "u", "a1", "")
	rr.WorkFor(wake, "s1", "Picking up a finished background task")
	if turn.Status() != RunStatusRunning {
		t.Fatal("a background run cancelled the session's own turn")
	}
	if got := rr.BackgroundFor("u", "s1"); got != wake {
		t.Fatalf("BackgroundFor = %v", got)
	}
	if rr.BackgroundFor("other", "s1") != nil {
		t.Error("another user's thread of the same id sees it")
	}

	udb := &DBase{Store: kvlite.MemStore()}
	sess, _ := saveChatSession(udb, ChatSession{AgentID: "a1"})
	bg := func() *Run { return rr.BackgroundFor("u", sess.ID) }
	done := make(chan struct{})
	go func() { waitForCards(context.Background(), udb, "a1", sess.ID, sess, "", "", bg); close(done) }()
	time.Sleep(50 * time.Millisecond)
	_, run := rr.CreateCancellable(context.Background(), "u", "a1", "")
	rr.WorkFor(run, sess.ID, "Running the morning report")
	noteSessionChange("a1", sess.ID)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the poll did not answer when a background run started")
	}

	w := httptest.NewRecorder()
	serveObservationCards(w, nil, "", bg())
	if !strings.Contains(w.Body.String(), `"label":"Running the morning report"`) || !strings.Contains(w.Body.String(), `"id":"`+run.ID+`"`) {
		t.Errorf("payload: %s", w.Body.String())
	}
	run.Complete(RunStatusCompleted)
	if rr.BackgroundFor("u", sess.ID) != nil {
		t.Error("a finished run is still shown")
	}
}
