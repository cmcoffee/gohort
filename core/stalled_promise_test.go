// A turn that ends by saying it is ABOUT to do the work. Observed with two
// image backends erroring and four rounds still on the clock: "Got it — let me
// create this. I'll blend Alex onto the picture of me wasting away in the
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
		"Got it — let me create this. I'll blend Alex onto the picture of me wasting away in the garage. 🏚️👔",
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

// A promise about future CONDUCT is not work left undone.
//
// Observed 2026-08-29 in a casual conversation: the user asked why the agent
// used em-dashes, it answered "I'll keep it straight going forward", and the
// give-up guard re-prompted it to "do it NOW with a real tool call". There is
// no tool for not using a punctuation mark. It fired three times in one chat,
// concatenated its retries into a single bubble, and ended with the agent
// inventing work nobody had asked for so that it would have a tool call to
// make. These are the exact replies from that session.
func TestABehavioralPromiseIsNotStalledWork(t *testing.T) {
	for _, conduct := range []string{
		"You're right, I missed that one. The rule is clear and I bent it without thinking. I'll keep it straight going forward.",
		"You caught me, the same thing happened right there too. It's a pattern, not a one-off slip. I'll watch for it this time.",
		"I'll be more careful with that from now on.",
		"My mistake. Next time I'll get it right.",
		"Noted, I won't do that again.",
	} {
		if replyStalledOnAPromise(conduct) {
			t.Errorf("re-prompted a promise no tool can keep:\n  %s", conduct)
		}
	}
}

// The tightening must not blunt the guard on real work. A promise to ACT still
// has to be caught, including when the same reply apologises first, which is
// the shape that makes the two easy to conflate.
func TestAPromiseToActIsStillCaughtAfterTheTightening(t *testing.T) {
	for _, stall := range []string{
		"Sorry about that. Let me look up the current price.",
		"You're right, I should have checked. I'll search for it now.",
		"I'm going to fetch that page and pull the number out.",
	} {
		if !replyStalledOnAPromise(stall) {
			t.Errorf("stopped catching a real stall:\n  %s", stall)
		}
	}
}
