package core

import (
	"context"
	"strings"
	"testing"
)

// A borrowed model runs on this machine's GPU and competes with everything the
// operator is doing on it. Until now that was invisible: a turn took longer,
// nothing said why, and the only honest description was "it got slow". The
// scheduler had been counting it per caller the whole time; nothing read it out.

// TestAPeerRunningTheModelIsVisible — the reported gap, end to end through the
// same registry the live pill reads.
func TestAPeerRunningTheModelIsVisible(t *testing.T) {
	StartLlamacppScheduler(2)
	if err := AcquireLlamacppSlot(context.Background(), "peer:studio-mac"); err != nil {
		t.Fatal(err)
	}
	defer ReleaseLlamacppSlot("peer:studio-mac")

	var found *LiveEntry
	for _, e := range peerLiveEntries() {
		if strings.Contains(e.Label, "studio-mac") {
			row := e
			found = &row
		}
	}
	if found == nil {
		t.Fatal("a peer holding a model slot produces no live row — the GPU is busy with " +
			"someone else's work and nothing on screen says so")
	}
	if !found.Background {
		t.Error("peer work is not marked Background — it started without the viewer, which is " +
			"exactly what that flag means and how the pill paints it differently")
	}
	if found.App != "Peers" {
		t.Errorf("row is filed under %q", found.App)
	}
	if !strings.Contains(found.Label, "running a model") {
		t.Errorf("the row does not say what the peer is doing: %q", found.Label)
	}
}

// TestLocalWorkIsNotDuplicated — apps already contribute live rows for their own
// turns. Reporting them here too would double every row in the ribbon.
func TestLocalWorkIsNotDuplicated(t *testing.T) {
	StartLlamacppScheduler(2)
	if err := AcquireLlamacppSlot(context.Background(), "chat-session-42"); err != nil {
		t.Fatal(err)
	}
	defer ReleaseLlamacppSlot("chat-session-42")
	for _, e := range peerLiveEntries() {
		if strings.Contains(e.Label, "chat-session-42") {
			t.Errorf("local work appeared as a peer row: %q", e.Label)
		}
	}
}

// TestQueuedPeerWorkReadsDifferently — "a peer is using the GPU" and "a peer is
// waiting behind you" are different facts, and only the first means the machine
// is busy with someone else's work right now.
func TestQueuedPeerWorkReadsDifferently(t *testing.T) {
	rows := peerRowsFrom(OllamaSchedStats{
		Callers: map[string]int{"peer:mac": 1},
		Queued:  map[string]int{"peer:laptop": 3},
	}, "running a model")

	var running, queued *LiveEntry
	for i := range rows {
		switch {
		case strings.Contains(rows[i].Label, "mac"):
			running = &rows[i]
		case strings.Contains(rows[i].Label, "laptop"):
			queued = &rows[i]
		}
	}
	if running == nil || queued == nil {
		t.Fatalf("expected a running row and a queued row, got %d rows", len(rows))
	}
	if running.Queued {
		t.Error("in-flight work is marked queued")
	}
	if !queued.Queued {
		t.Error("waiting work is not marked queued, so it reads as using the GPU right now")
	}
	if !strings.Contains(queued.Label, "3 waiting") {
		t.Errorf("the queued row does not say how deep it is: %q", queued.Label)
	}
}

// TestTheRowNamesThePeer — the key's LABEL is what somebody typed so they could
// recognize the far side later. A row reading "peer:3f2a…" answers none of the
// question it exists to answer.
func TestTheRowNamesThePeer(t *testing.T) {
	if got := peerDisplayName("peer:studio-mac"); got != "Peer studio-mac" {
		t.Errorf("display name = %q", got)
	}
	// A blank label still produces something readable rather than "Peer ".
	if got := peerDisplayName("peer:"); got != "A peer" {
		t.Errorf("an unlabelled key renders as %q", got)
	}
}

// TestConcurrentWorkSaysHowMuch — one peer holding several slots is a different
// situation from one holding one, and the ribbon has room to say which.
func TestConcurrentWorkSaysHowMuch(t *testing.T) {
	rows := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:mac": 3}}, "rendering an image")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want one", len(rows))
	}
	if !strings.Contains(rows[0].Label, "3 at once") {
		t.Errorf("a peer holding three slots reads as one: %q", rows[0].Label)
	}
}

// TestOrderIsStableAcrossPolls — the ribbon is polled, and a row that moves
// reads as a new row. Map iteration would reshuffle these every second.
func TestOrderIsStableAcrossPolls(t *testing.T) {
	StartLlamacppScheduler(4)
	for _, p := range []string{"peer:a", "peer:b", "peer:c"} {
		if err := AcquireLlamacppSlot(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		defer ReleaseLlamacppSlot(p)
	}
	first := peerLiveEntries()
	for i := 0; i < 20; i++ {
		again := peerLiveEntries()
		if len(again) != len(first) {
			t.Fatalf("row count is unstable: %d vs %d", len(first), len(again))
		}
		for j := range first {
			if again[j].ID != first[j].ID {
				t.Fatal("row order is unstable between polls — the ribbon would reshuffle every refresh")
			}
		}
	}
}
