// A turn that ends by saying it is ABOUT to do the work. Observed with two
// image backends erroring and four rounds still on the clock: "Got it — let me
// create this. I'll blend Rory onto the picture of me wasting away in the
// garage. 🏚️👔" — and then nothing. The next message from the user was "you
// forgot to attach the image."
//
// The give-up guard already covered the silent version of this (empty content,
// errors pending). The promise is the same turn, and the worse one: it reads
// as progress.
package core

import (
	"strings"
	"testing"
)

func TestAPromiseToActIsNotAFinishedTurn(t *testing.T) {
	for _, stall := range []string{
		"Got it — let me create this. I'll blend Rory onto the picture of me wasting away in the garage. 🏚️👔",
		"Let me create this.",
		"I'll grab a fresh copy and try that again.",
		"Now I'm going to run that with the source photos instead.",
		// From the live transcript that exposed the no-errors gap: promised,
		// called nothing, turn over.
		"On it — let me grab some reference photos and composite them into that scene.",
		"You're right, Craig. Let me actually fix this now instead of making promises I don't keep.",
	} {
		if !replyStalledOnAPromise(stall) {
			t.Errorf("a promise of work is not a finished turn: %q", stall)
		}
	}
}

func TestAskingTheUserForSomethingIsAFinishedTurn(t *testing.T) {
	// Waiting is not stalling. A turn that hands the next move to the user is
	// complete however it's punctuated — re-prompting it talks over a question
	// the user is already answering.
	for _, done := range []string{
		"Send me both photos in one message and I'll do the blend.",
		"Paste the error message here and let me know what you see.",
		"Which of the two did you want on the hood? Tell me and I'll run it.",
		"Attach the original and I'll try again.",
	} {
		if replyStalledOnAPromise(done) {
			t.Errorf("a handover to the user is a finished turn: %q", done)
		}
	}
}

func TestPresentingIsNotPromising(t *testing.T) {
	// The split that let this guard stand on its own. It used to share
	// firstPersonIntentRe with endsWithCallAnnouncement, which also matches
	// "here's" — fine there, because that guard additionally demands a trailing
	// colon, and "Here's the update_agent call:" is unmistakable. Here there is
	// no colon to lean on, and re-prompting every "Here's the answer" would be
	// far worse than missing the occasional promise.
	for _, presenting := range []string{
		"Here's what I think.",
		"Here's the answer: 42.",
		"Here is the summary you asked for.",
	} {
		if replyStalledOnAPromise(presenting) {
			t.Errorf("presenting something now is a finished turn: %q", presenting)
		}
	}
	// The colon shape stays covered by the guard that owns it.
	if !endsWithCallAnnouncement("Here's the update_agent call to implement these changes:") {
		t.Error("endsWithCallAnnouncement still owns the colon shape")
	}
}

func TestAnAnswerIsNotAStall(t *testing.T) {
	if replyStalledOnAPromise("") {
		t.Error("empty content is the other branch's business, not this one")
	}
	// No first-person commitment anywhere: a plain report is finished.
	if replyStalledOnAPromise("That backend needs two source photos and only one was in the workspace.") {
		t.Error("a plain report is not a stall")
	}
	// Length is the lead-in cutoff. A real answer that happens to say "I'll"
	// somewhere in it is an answer, and re-prompting it produces restated noise.
	long := "Here is the full rundown. " + strings.Repeat("The backend composes two source photos. ", 20) + "I'll note that for next time."
	if len(long) <= replyStalledOnAPromiseMaxLen {
		t.Fatalf("fixture too short to exercise the cutoff: %d", len(long))
	}
	if replyStalledOnAPromise(long) {
		t.Error("a long answer is an answer, not a lead-in")
	}
}
