package orchestrate

import (
	"strings"
	"testing"
)

// TestBareAskIsNeverDeduped — a bare ask_user (no options) renders as prose
// through the same emitter replies use, and that emitter suppresses a
// near-duplicate of the last streamed bubble.
//
// For a reply that is right; a repeat is noise. For an ASK it is fatal. The
// model routinely streams a lead-in ("Several things are ambiguous:") and then
// captures a question that BEGINS with those same words, which scores 1.0 on
// isNearDuplicate's longest-common-prefix test and drops the whole ask.
// AwaitingUserConfirm is still set, so the turn ends parked on a question the
// user never saw: a lead-in, a colon, and nothing. Seen live in Guides.
//
// runPlan is far too large to drive from a unit test, so this pins the wiring
// at the source: the ask path must reach for the raw emitter.
func TestBareAskIsNeverDeduped(t *testing.T) {
	src := readOwnSource(t, "runner.go")
	const marker = "if len(pr.capturedOptions) == 0 {"
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatal("the bare-ask branch moved; re-point this test rather than deleting it")
	}
	// The branch is short; look at the block that follows it.
	block := src[i : i+900]
	if !strings.Contains(block, "pr.emitBubble(pr.capturedQuest)") {
		t.Error("the bare-ask path must emit through emitBubble (no dedup) — emitCapturedAsBubble drops an ask whose text repeats the streamed lead-in")
	}
	if strings.Contains(block, "pr.emitCapturedAsBubble(pr.capturedQuest)") {
		t.Error("the bare-ask path is going through the near-duplicate guard again; a swallowed question leaves the turn parked with nothing on screen")
	}
}

// TestNearDuplicateCatchesAPrefixLeadIn documents the exact scoring that made
// the bug possible, so a future change to isNearDuplicate cannot quietly make
// this test's premise false while it still passes.
func TestNearDuplicateCatchesAPrefixLeadIn(t *testing.T) {
	leadIn := "Let me get your attention on what this guide should cover before I start writing. Several things are ambiguous:"
	ask := leadIn + " 1) Who is the audience? 2) Which product? 3) How long should it be?"
	if !isNearDuplicate(ask, leadIn) {
		t.Skip("isNearDuplicate no longer scores a streamed lead-in as a duplicate of the ask that repeats it; the ask path's guard-free emit is now belt-and-braces")
	}
}

// A retry that changes only punctuation is the reply already on screen. The
// loop retracted the retry's stream as the same reply, and the host showed it
// anyway: its prefix test stopped at the dash. The pair is the 2026-10-25
// goodnight, word for word.
func TestARetryThatOnlyChangesPunctuationIsTheShownReply(t *testing.T) {
	shown := "Night. Lights out — I'll be here when you wake up."
	retry := "Night. Lights out, I'll be here when you wake up."
	if !repeatsWithoutAdding(retry, shown) {
		t.Errorf("a dash-for-comma retry was shown as a second bubble")
	}
	if repeatsWithoutAdding("Night. Lights out, I'll be here when you wake up. One thing before you go: your 9am with the dentist moved to 10:30, and the reminder is set for 9:45.", shown) {
		t.Errorf("a retry that says more must still be shown")
	}
}
