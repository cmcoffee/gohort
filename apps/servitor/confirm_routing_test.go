// A confirmation may only be answered by the person whose session is asking.
//
// The confirm endpoint cannot address a channel: the card's id is generated per
// event by the bridge and carries no session, so the handler selects one
// instead. Selection used to be "the first channel in the process that will
// take it", which with two people working at once let either one's Allow
// release the other's command — and the command in question is the one running
// over SSH on somebody's appliance. These tests pin the selection rule, since
// it is the whole of the access decision.
package servitor

import "testing"

// register stands up one pending confirmation the way a live session does, and
// cleans it up so the package-global map doesn't leak between tests.
func register(t *testing.T, sid, owner string, interactive bool) chan bool {
	t.Helper()
	ch := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: ch, owner: owner, interactive: interactive})
	t.Cleanup(func() { confirmChans.Delete(sid) })
	return ch
}

func TestConfirmReachesOwnSession(t *testing.T) {
	ch := register(t, "s-mine", "craig", true)

	if got := deliverConfirm("craig", true); got != "s-mine" {
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
	// The bug this file exists for: dana clicks Allow, craig's destructive
	// command runs.
	ch := register(t, "s-craig", "craig", true)

	if got := deliverConfirm("dana", true); got != "" {
		t.Fatalf("dana must not answer craig's session, but it went to %q", got)
	}
	select {
	case v := <-ch:
		t.Fatalf("craig's command was released by dana (got %v)", v)
	default:
	}
}

func TestConfirmSkipsOtherUsersAndFindsOwn(t *testing.T) {
	// With several sessions live, the caller's own must be selected rather
	// than whichever the map happens to visit first.
	other := register(t, "s-dana", "dana", true)
	mine := register(t, "s-craig", "craig", true)

	if got := deliverConfirm("craig", true); got != "s-craig" {
		t.Fatalf("expected craig's own session, got %q", got)
	}
	select {
	case <-other:
		t.Error("dana's session was answered by craig")
	default:
	}
	select {
	case <-mine:
	default:
		t.Error("craig's own session was not answered")
	}
}

func TestConfirmNeverReachesAnAutoDeniedSession(t *testing.T) {
	// Guide investigations and workspace drills register a channel too, but a
	// goroutine feeds theirs a standing denial to keep the run read-only. An
	// operator's Allow landing there would turn a read-only drill into a
	// mutating one, and Range order is unspecified, so being the caller's own
	// session must not be enough.
	readonly := register(t, "guide-investigate-1", "craig", false)

	if got := deliverConfirm("craig", true); got != "" {
		t.Fatalf("an auto-denied session must not be answerable, went to %q", got)
	}
	select {
	case v := <-readonly:
		t.Fatalf("read-only run received an operator decision (%v)", v)
	default:
	}
}

func TestConfirmUntaggedSessionIsUnanswerable(t *testing.T) {
	// Fail closed, the same way MaskedLabel does with an untagged session: a
	// channel nobody can be shown to own is one nobody may answer, rather than
	// one everybody may.
	ch := register(t, "s-orphan", "", true)

	for _, who := range []string{"craig", "dana", ""} {
		if got := deliverConfirm(who, true); got != "" {
			t.Errorf("viewer %q answered an unowned session (%q)", who, got)
		}
	}
	select {
	case <-ch:
		t.Error("an unowned session was answered")
	default:
	}
}

func TestConfirmEmptyUserMatchesNothing(t *testing.T) {
	// An unauthenticated caller must not collapse into "matches every session".
	ch := register(t, "s-craig", "craig", true)

	if got := deliverConfirm("  ", true); got != "" {
		t.Fatalf("a blank user answered %q", got)
	}
	select {
	case <-ch:
		t.Error("a blank user released a real session")
	default:
	}
}

func TestConfirmDenyTravelsAsFalse(t *testing.T) {
	ch := register(t, "s-mine", "craig", true)

	if got := deliverConfirm("craig", false); got != "s-mine" {
		t.Fatalf("a denial must still be delivered, went to %q", got)
	}
	if v := <-ch; v {
		t.Error("deny must arrive as false")
	}
}

func TestConfirmReportsWhenNothingWasWaiting(t *testing.T) {
	// The empty return is what makes the handler answer 409 instead of a
	// silent 204 that settles the card while the run stays blocked.
	if got := deliverConfirm("craig", true); got != "" {
		t.Fatalf("expected no delivery with no sessions registered, got %q", got)
	}
}
