package orchestrate

// A pinned note can say how long it holds. Notes were kept until something
// replaced them, so a passing status ("at practice until six") and a record of
// something the agent did (a picture it made) sat in every prompt for good.
// One saved with an end leaves the prompt at that end, and the daily pass lets
// it go: not into reference memory, since it was saved as something that
// would stop being true.

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/provenance"
)

func expiryTurn(t *testing.T) (*chatTurn, Database) {
	t.Helper()
	root := depStores(t, "u")
	udb := UserDB(root, "u")
	agent, err := saveAgent(udb, AgentRecord{ID: "ag", Name: "Agent", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	return &chatTurn{app: orchRef, user: "u", udb: udb, agent: agent}, udb
}

func TestAPinnedNoteCanSayHowLongItHolds(t *testing.T) {
	turn, udb := expiryTurn(t)
	remember := turn.rememberToolDef().Handler
	out, err := remember(context.Background(), map[string]any{"content": "Rory is at practice this evening", "pin": true, "expires": "6h"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "until") || !strings.Contains(out, "then it leaves your notes") {
		t.Errorf("the save should say when the note leaves: %q", out)
	}
	facts := ListMemoryFacts(udb, factsNamespace("ag"))
	if len(facts) != 1 {
		t.Fatalf("one note should be saved: %+v", facts)
	}
	if left := time.Until(facts[0].ExpiresAt); left < 5*time.Hour || left > 7*time.Hour {
		t.Errorf("6h should end about six hours from now, ends in %s", left)
	}

	for expires, want := range map[string]string{"soon": "must be a date", "2020-01-01": "already past", "0d": "must be a date"} {
		if _, err := remember(context.Background(), map[string]any{"content": "a passing thing " + expires, "pin": true, "expires": expires}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("expires %q: want an error naming %q, got %v", expires, want, err)
		}
	}
	// A record of a picture the agent made points at one that will not stay.
	if _, err := remember(context.Background(), map[string]any{"content": "two renders produced (image#r.72967687)", "pin": true}); err == nil || !strings.Contains(err.Error(), "stop resolving") {
		t.Errorf("a ring id should be refused, got %v", err)
	}
	if n := len(ListMemoryFacts(udb, factsNamespace("ag"))); n != 1 {
		t.Errorf("a refused note was saved anyway: %d notes", n)
	}
}

func TestANoteLeavesAtItsEndAndTheDailyPassLetsItGo(t *testing.T) {
	_, udb := expiryTurn(t)
	putFact(udb, "ag", "status", "The deploy is broken right now", provenance.MemKindFact, time.Hour,
		func(f *MemoryFact) { f.ExpiresAt = time.Now().Add(-time.Minute) })
	putFact(udb, "ag", "later", "Out of office until Friday", provenance.MemKindFact, time.Hour,
		func(f *MemoryFact) { f.ExpiresAt = time.Now().Add(48 * time.Hour) })
	putFact(udb, "ag", "pref", "Prefers dark mode", provenance.MemKindFact, time.Hour, nil)

	// Out of the prompt the moment it ends, before any pass has run.
	if got := liveIDs(udb, "ag"); !sameSet(got, "later,pref") {
		t.Fatalf("live notes = %s, want later,pref", got)
	}
	if exp := ExpiredMemoryFacts(udb, factsNamespace("ag")); len(exp) != 1 || exp[0].ID != "status" {
		t.Fatalf("the ended note should wait for the pass: %+v", exp)
	}

	runMemoryLifecycle(context.Background())
	gone, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "status")
	if gone.Reason != provenance.RetireExpired || gone.Successor != "" {
		t.Fatalf("the ended note should be let go, not moved: %+v", gone.MemoryProvenance)
	}
	if len(ExpiredMemoryFacts(udb, factsNamespace("ag"))) != 0 {
		t.Error("a retired note is still waiting to expire")
	}
	moves := listMemoryMoves(udb, "ag")
	if len(moves) != 1 || moves[0].Kind != "expired" {
		t.Fatalf("the Memory panel should list the expiry: %+v", moves)
	}

	// Undo puts it back as standing: its end has passed, so keeping it would
	// retire it again on the next pass.
	if err := undoMemoryMove(udb, "ag", moves[0].ID); err != nil {
		t.Fatal(err)
	}
	back, _ := GetMemoryFactByID(udb, factsNamespace("ag"), "status")
	if back.Retired() || !back.ExpiresAt.IsZero() || !strings.Contains(liveIDs(udb, "ag"), "status") {
		t.Errorf("undo should put the note back with no end: %+v", back.MemoryProvenance)
	}
}
