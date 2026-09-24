// A confirmation may only be answered by the person whose session is asking,
// and only lands on the command the card is about.
//
// Selection used to be "the first channel in the process that will take it",
// which with two people working at once let either one's Allow release the
// other's command. Scoping that to the caller's own sessions still left one
// person's two waiting sessions answering each other, and a stale card from a
// command that had timed out able to release the next one. The card id now
// names its session and command (confirmCardID); these tests pin the rule,
// since it is the whole of the access decision.
package servitor

import "testing"

// register stands up one pending confirmation the way a live session does -
// the channel, and the command it is waiting on - and returns the card id the
// events bridge would stamp on its approval card. Cleaned up so the
// package-global maps don't leak between tests.
func register(t *testing.T, sid, owner string, interactive bool) (chan bool, string) {
	t.Helper()
	const cmd = "systemctl restart web"
	ch := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: ch, owner: owner, interactive: interactive})
	pendingCmds.Store(sid, cmd)
	t.Cleanup(func() {
		confirmChans.Delete(sid)
		pendingCmds.Delete(sid)
	})
	return ch, confirmCardID(sid, cmd, "n1")
}

func expectNothing(t *testing.T, ch chan bool, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s (got %v)", what, v)
	default:
	}
}

func TestConfirmReachesOwnSession(t *testing.T) {
	ch, card := register(t, "s-mine", "alice", true)

	if got := deliverConfirm("alice", card, true); got != "s-mine" {
		t.Fatalf("owner's answer must reach their own session, went to %q", got)
	}
	select {
	case v := <-ch:
		if !v {
			t.Error("allow must arrive as true")
		}
	default:
		t.Error("nothing was delivered to the channel")
	}
}

func TestConfirmCannotAnswerAnotherUsersSession(t *testing.T) {
	// bob holds alice's card id (session ids are public): it still must
	// not release alice's command.
	ch, card := register(t, "s-alice", "alice", true)

	if got := deliverConfirm("bob", card, true); got != "" {
		t.Fatalf("bob must not answer alice's session, but it went to %q", got)
	}
	expectNothing(t, ch, "alice's command was released by bob")
}

func TestConfirmReachesTheSessionOnTheCard(t *testing.T) {
	// Two of one person's sessions waiting at once: the answer goes to the
	// one whose card was clicked, not whichever the map visits first.
	first, _ := register(t, "s-one", "alice", true)
	second, card := register(t, "s-two", "alice", true)

	if got := deliverConfirm("alice", card, true); got != "s-two" {
		t.Fatalf("expected the session named on the card, got %q", got)
	}
	expectNothing(t, first, "the other session was answered")
	select {
	case <-second:
	default:
		t.Error("the session on the card was not answered")
	}
}

func TestConfirmForAnOldCommandDoesNotReleaseTheNextOne(t *testing.T) {
	ch, staleCard := register(t, "s-mine", "alice", true)
	// The command the card was about timed out; the run is now waiting on
	// a different one.
	pendingCmds.Store("s-mine", "rm -rf /var/lib/app")

	if got := deliverConfirm("alice", staleCard, true); got != "" {
		t.Fatalf("a card for an earlier command released the current one (%q)", got)
	}
	expectNothing(t, ch, "stale approval was delivered")

	// And with nothing waiting at all, nothing is queued to answer the next
	// prompt early.
	pendingCmds.Delete("s-mine")
	_, card := register(t, "s-idle", "alice", true)
	pendingCmds.Delete("s-idle")
	if got := deliverConfirm("alice", card, true); got != "" {
		t.Fatalf("an answer was queued while nothing was waiting (%q)", got)
	}
}

func TestConfirmNeverReachesAnAutoDeniedSession(t *testing.T) {
	// Guide investigations and workspace drills register a channel too, but a
	// goroutine feeds theirs a standing denial to keep the run read-only. An
	// operator's Allow landing there would turn a read-only drill into a
	// mutating one.
	readonly, card := register(t, "guide-investigate-1", "alice", false)

	if got := deliverConfirm("alice", card, true); got != "" {
		t.Fatalf("an auto-denied session must not be answerable, went to %q", got)
	}
	expectNothing(t, readonly, "read-only run received an operator decision")
}

func TestConfirmUntaggedSessionIsUnanswerable(t *testing.T) {
	// Fail closed: a channel nobody can be shown to own is one nobody may
	// answer, rather than one everybody may.
	ch, card := register(t, "s-orphan", "", true)

	for _, who := range []string{"alice", "bob", ""} {
		if got := deliverConfirm(who, card, true); got != "" {
			t.Errorf("viewer %q answered an unowned session (%q)", who, got)
		}
	}
	expectNothing(t, ch, "an unowned session was answered")
}

func TestConfirmEmptyUserMatchesNothing(t *testing.T) {
	ch, card := register(t, "s-alice", "alice", true)

	if got := deliverConfirm("  ", card, true); got != "" {
		t.Fatalf("a blank user answered %q", got)
	}
	expectNothing(t, ch, "a blank user released a real session")
}

func TestConfirmCardWithoutASessionIsRefused(t *testing.T) {
	// The bridge's bare random id names no session. Routing it by owner is
	// what this replaced; it is refused instead.
	ch, _ := register(t, "s-alice", "alice", true)
	for _, id := range []string{"c-150405.000001-7", "", "c~", "c~~abc~n", "c~s-alice~~n"} {
		if got := deliverConfirm("alice", id, true); got != "" {
			t.Errorf("card id %q was routed to %q", id, got)
		}
	}
	expectNothing(t, ch, "an unbound card released a command")
}

func TestConfirmCardIDSurvivesAnOddSessionID(t *testing.T) {
	// Chat session ids can be client-chosen, so the id is parsed from the
	// right and a separator inside the session id does not shift the parts.
	sid := "a~b~c"
	card := confirmCardID(sid, "ls", "x~y")
	got, tag, ok := parseConfirmCardID(card)
	if !ok || got != sid || tag != confirmCmdTag("ls") {
		t.Fatalf("parse(%q) = %q, %q, %v", card, got, tag, ok)
	}
}

func TestConfirmDenyTravelsAsFalse(t *testing.T) {
	ch, card := register(t, "s-mine", "alice", true)

	if got := deliverConfirm("alice", card, false); got != "s-mine" {
		t.Fatalf("a denial must still be delivered, went to %q", got)
	}
	if v := <-ch; v {
		t.Error("deny must arrive as false")
	}
}

func TestConfirmReportsWhenNothingWasWaiting(t *testing.T) {
	// The empty return is what makes the handler answer 409 instead of a
	// silent 204 that settles the card while the run stays blocked.
	if got := deliverConfirm("alice", confirmCardID("s-none", "ls", "n"), true); got != "" {
		t.Fatalf("expected no delivery with no sessions registered, got %q", got)
	}
}

func TestSessionIDLiveUnderAnotherUserIsNotAdopted(t *testing.T) {
	// A chat request naming somebody else's live session must not be run
	// under that id: it would re-register their run and their approvals here.
	probeSessions.Register("s-live", "x", func() {}).SetOwner("alice")
	t.Cleanup(func() { probeSessions.CancelSession("s-live") })
	if sessionIDUsableBy("s-live", "bob") {
		t.Error("bob may adopt alice's live session id")
	}
	if !sessionIDUsableBy("s-live", "alice") {
		t.Error("alice may not continue her own session")
	}
	register(t, "s-waiting", "alice", true)
	if sessionIDUsableBy("s-waiting", "bob") {
		t.Error("bob may adopt a session id holding alice's pending approval")
	}
	if !sessionIDUsableBy("", "bob") || !sessionIDUsableBy("s-fresh", "bob") {
		t.Error("a fresh or empty id must be usable")
	}
}
