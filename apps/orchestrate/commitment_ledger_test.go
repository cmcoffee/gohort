// Carrying an unkept promise into the next turn.
//
// The in-turn guards stop a promise shipping unbacked; this is for the one that
// gets through. Observed in a group chat: WiWee said "On it — let me grab some
// reference photos and composite them in", called no tool, and ended the turn.
// Craig asked "Wiwee, are you really ?" — three words with no antecedent — and
// the model, having no memory of promising anything, bound the question to the
// wrong thread and answered something nobody had asked. Told plainly what it
// had done, it apologized and made the identical promise again.
package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

const promiseSaid = "On it — let me grab some reference photos of the actual guys and composite them into that scene."

func commitDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

func TestAnUnkeptPromiseReachesTheNextTurn(t *testing.T) {
	db := commitDB(t)
	recordTurnCommitment(db, "s1", promiseSaid, false)

	note := commitmentTurnNote(db, "s1")
	if note == "" {
		t.Fatal("the next turn must be told what was promised")
	}
	// In the model's OWN words: a framework paraphrase is something it can
	// argue with, its own sentence is not.
	if !strings.Contains(note, "reference photos") {
		t.Errorf("the note must quote the promise:\n%s", note)
	}
	// The antecedent for the jab that follows. "are you really?" has nothing in
	// it to bind to, which is how the model answered a different question.
	if !strings.Contains(note, "are you really") {
		t.Errorf("the note must name the challenge it answers:\n%s", note)
	}
	// And the two things it actually did when challenged, both ruled out.
	for _, must := range []string{"Do NOT apologize", "do NOT promise it again"} {
		if !strings.Contains(note, must) {
			t.Errorf("the note must rule out %q:\n%s", must, note)
		}
	}
}

func TestDoingTheWorkRetiresIt(t *testing.T) {
	db := commitDB(t)
	recordTurnCommitment(db, "s1", promiseSaid, false)
	if commitmentTurnNote(db, "s1") == "" {
		t.Fatal("precondition: something is owed")
	}
	// A turn that called a tool is a turn where the conversation moved. Retiring
	// early costs a missed reminder; retiring late costs an agent apologizing
	// for something it already handled.
	recordTurnCommitment(db, "s1", "Done — that's posted.", true)
	if note := commitmentTurnNote(db, "s1"); note != "" {
		t.Errorf("work must retire the promise, still owed:\n%s", note)
	}
}

func TestItStopsNaggingOnItsOwn(t *testing.T) {
	// The opposite failure, and the more damaging one: an agent reminded every
	// turn that it owes someone something starts apologizing every turn.
	db := commitDB(t)
	recordTurnCommitment(db, "s1", promiseSaid, false)
	for i := 0; i < commitmentMaxTurns; i++ {
		recordTurnCommitment(db, "s1", "Sure, the weather's fine.", false) // chat, no promise, no tools
	}
	if note := commitmentTurnNote(db, "s1"); note != "" {
		t.Errorf("an aged-out promise must be dropped, still owed:\n%s", note)
	}
}

func TestAFreshPromiseRenewsIt(t *testing.T) {
	// "Let me actually fix this now instead of making promises I don't keep" —
	// the second promise, also unbacked. A persistent problem renews itself for
	// as long as it lasts, so nothing has to nag to keep up with it.
	db := commitDB(t)
	recordTurnCommitment(db, "s1", promiseSaid, false)
	recordTurnCommitment(db, "s1", "You're right. Let me actually fix this now.", false)
	note := commitmentTurnNote(db, "s1")
	if !strings.Contains(note, "actually fix this now") {
		t.Errorf("the newest promise is the one outstanding:\n%s", note)
	}
	if strings.Contains(note, "reference photos") {
		t.Errorf("the superseded promise must not linger:\n%s", note)
	}
}

func TestOrdinaryTalkOwesNothing(t *testing.T) {
	db := commitDB(t)
	for _, plain := range []string{
		"Here's the answer: 42.",
		"That backend needs two source photos and only one was in the workspace.",
		"",
	} {
		recordTurnCommitment(db, "s2", plain, false)
		if note := commitmentTurnNote(db, "s2"); note != "" {
			t.Errorf("nothing was promised in %q, got:\n%s", plain, note)
		}
	}
	// A promise BACKED by real work is not a debt — this is the reply the
	// detached-task notice explicitly asks for.
	recordTurnCommitment(db, "s2", "I'll get that going and let you know when it's done.", true)
	if note := commitmentTurnNote(db, "s2"); note != "" {
		t.Errorf("a backed promise owes nothing:\n%s", note)
	}
}

func TestLedgerIsPerConversation(t *testing.T) {
	// A debt in one thread must not surface in another — the same reason the
	// image space is scoped per agent.
	db := commitDB(t)
	recordTurnCommitment(db, "s1", promiseSaid, false)
	if note := commitmentTurnNote(db, "s2"); note != "" {
		t.Errorf("another conversation owes nothing:\n%s", note)
	}
	// And no session id at all is a no-op rather than a shared bucket.
	recordTurnCommitment(db, "", promiseSaid, false)
	if note := commitmentTurnNote(db, ""); note != "" {
		t.Errorf("a session-less turn keeps no ledger:\n%s", note)
	}
}

func TestNotesComposeInActingOrder(t *testing.T) {
	// Reference material first, the thing to act on last, so the instruction
	// sits closest to where the model starts writing.
	sess := noteSession(t, "edited: me wasting away in the garage")
	db := commitDB(t)
	recordTurnCommitment(db, "s1", promiseSaid, false)

	got := turnNotes(sess, db, "s1", "Wiwee, are you really going to fix that picture?")
	// Anchored on the manifest's closing instruction rather than its heading:
	// the heading now varies with provenance (pictures you were GIVEN vs ones
	// YOU MADE), and this test is about ORDER, not about how the manifest
	// labels its groups.
	img := strings.Index(got, "images list of an image call")
	com := strings.Index(got, "OUTSTANDING FROM YOUR LAST TURN")
	if img < 0 || com < 0 {
		t.Fatalf("both notes must appear:\n%s", got)
	}
	if img > com {
		t.Errorf("the manifest is reference and must come first:\n%s", got)
	}
	// Nothing owed and nothing pictured means nothing appended at all.
	if n := turnNotes(nil, commitDB(t), "s3", "what's for dinner"); n != "" {
		t.Errorf("a turn with nothing to say must add nothing: %q", n)
	}
}
