// How the loop rations its silent re-prompts.
//
// It used to be one counter shared by every guard, which failed two ways.
// Corrections of UNRELATED kinds disarmed each other — an orphaned tool tag
// early in a turn meant a false delivery claim later went uncorrected, though
// nothing about the first says anything about the second. And a guard that
// spent the budget on the SAME problem twice simply stopped firing, so the
// content it had ruled wrong shipped anyway.
//
// Observed: the phantom-delivery guard fired 1/2, then 2/2, on one invented
// filename; the model rewrote the same claim both times; the third went to the
// user — a claim the framework had already judged false, twice.
package core

import (
	"strings"
	"testing"
)

func TestOneKindRunningOutLeavesTheOthersArmed(t *testing.T) {
	b := newCorrectionBudget()

	for i := 1; i <= maxCorrectionsPerKind; i++ {
		if !b.available(correctionPhantomDelivery) {
			t.Fatalf("attempt %d must be allowed", i)
		}
		if n := b.spend(correctionPhantomDelivery); n != i {
			t.Errorf("spend returned %d, want %d", n, i)
		}
	}
	if b.available(correctionPhantomDelivery) {
		t.Error("a third attempt on the same fault must be refused")
	}
	// The whole point: an unrelated guard is untouched.
	if !b.available(correctionOrphanedXML) {
		t.Error("an unrelated kind must still be armed")
	}
	if !b.available(correctionGiveUp) {
		t.Error("an unrelated kind must still be armed")
	}
}

func TestATurnWideCeilingStillStopsASpiral(t *testing.T) {
	// Per-kind budgets must not add up to unbounded. A turn going wrong in many
	// ways at once still has to terminate.
	b := newCorrectionBudget()
	kinds := []string{
		correctionOrphanedXML, correctionPhantomDelivery, correctionFakeToolCode,
		correctionAnnouncedCall, correctionToolMention, correctionCollapse, correctionGiveUp,
	}
	spent := 0
	for _, k := range kinds {
		for i := 0; i < maxCorrectionsPerKind; i++ {
			if !b.available(k) {
				continue
			}
			b.spend(k)
			spent++
		}
	}
	if spent != maxCorrectionsPerTurn {
		t.Errorf("spent %d corrections, want the turn ceiling of %d", spent, maxCorrectionsPerTurn)
	}
	for _, k := range kinds {
		if b.available(k) {
			t.Errorf("%s still armed past the turn ceiling", k)
		}
	}
}

func TestExhaustionBreadcrumbsExactlyOnce(t *testing.T) {
	// A guard that goes quiet without a word is indistinguishable from one that
	// never fired — the silent drop this codebase keeps paying for. But it is
	// checked every round after, so it must not repeat either.
	b := newCorrectionBudget()
	if b.exhausted(correctionGiveUp) {
		t.Error("a kind with budget left is not exhausted")
	}
	for i := 0; i < maxCorrectionsPerKind; i++ {
		b.spend(correctionGiveUp)
	}
	if !b.exhausted(correctionGiveUp) {
		t.Error("the first check after running out must report it")
	}
	for i := 0; i < 5; i++ {
		if b.exhausted(correctionGiveUp) {
			t.Fatal("exhaustion must breadcrumb once, not once per round")
		}
	}
}

func TestTheSubstitutedReplyIsTrueAndBlamesNobody(t *testing.T) {
	got := UnfulfilledDeliveryReply([]string{"the image"})

	if !strings.Contains(got, "the image") {
		t.Errorf("must name what was claimed: %q", got)
	}
	// It has to actually correct the claim, not soften it.
	if !strings.Contains(got, "nothing to send") {
		t.Errorf("must say plainly that there is no file: %q", got)
	}
	// The failure that started this whole line of work was a reply that
	// apologized for the REQUEST and asked the user to rephrase. The request
	// was fine every time.
	for _, blame := range []string{"rephrase", "try asking", "didn't understand", "could you clarify"} {
		if strings.Contains(strings.ToLower(got), blame) {
			t.Errorf("must not blame the request (%q): %q", blame, got)
		}
	}
	// A filename ref reads just as well as a noun phrase.
	if named := UnfulfilledDeliveryReply([]string{"garage.png"}); !strings.Contains(named, "garage.png") {
		t.Errorf("must name a filename ref too: %q", named)
	}
	// And it never renders an empty subject.
	if bare := UnfulfilledDeliveryReply(nil); strings.Contains(bare, "sending ,") || strings.Contains(bare, "sending .") {
		t.Errorf("no refs must still read cleanly: %q", bare)
	}
}
