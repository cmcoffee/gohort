package changes

import (
	"context"
	"testing"
	"time"
)

// A page asking with what it last saw is answered the moment a topic moves,
// at once when it already has, and with the same versions when nothing did.
func TestAWaitIsAnsweredByTheChange(t *testing.T) {
	a, b := ForUser("sessions", "alice"), ForUser("sessions", "bob")
	first := Wait(context.Background(), map[string]string{a: "", b: ""}, time.Second)
	if first[a] == "" || first[b] == "" {
		t.Fatalf("a first ask should be answered at once with versions: %v", first)
	}

	got := make(chan map[string]string, 1)
	go func() { got <- Wait(context.Background(), first, 5*time.Second) }()
	time.Sleep(20 * time.Millisecond)
	Note(b)
	select {
	case v := <-got:
		if v[b] == first[b] || v[a] != first[a] {
			t.Errorf("only bob's topic should have moved: %v -> %v", first, v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a change did not answer the wait")
	}

	// A change between two asks is not lost: the next ask sees it at once.
	seen := Wait(context.Background(), map[string]string{a: ""}, time.Second)
	Note(a)
	start := time.Now()
	if v := Wait(context.Background(), seen, 5*time.Second); v[a] == seen[a] || time.Since(start) > time.Second {
		t.Errorf("a change made between asks should answer the next ask at once: %v %v", seen, v)
	}

	// Nothing moving: the same versions back, after the wait.
	same := Wait(context.Background(), map[string]string{a: ""}, time.Second)
	if v := Wait(context.Background(), same, 50*time.Millisecond); v[a] != same[a] {
		t.Errorf("nothing changed, yet the version did: %v %v", same, v)
	}
}
