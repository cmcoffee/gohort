// Delivering what a background task produced. The text half always worked; the
// files it made are the half that went missing, and these pin the staging that
// carries them from the finished task to the turn that announces it.
package orchestrate

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestStagedAttachmentsRideTheTurnThatAnnouncesThem(t *testing.T) {
	const session = "sess-stage-1"
	t.Cleanup(func() { claimTaskAttachments(session, &ToolSession{}) })

	if n := stageTaskAttachments(session, TaskProduct{Text: "done", Images: []string{"PICTURE"}}); n != 1 {
		t.Fatalf("staged %d, want 1", n)
	}
	if !hasStagedAttachments(session) {
		t.Fatal("a staged delivery must be visible to the wake router — otherwise it joins a live turn and strands the file")
	}

	sess := &ToolSession{}
	if n := claimTaskAttachments(session, sess); n != 1 {
		t.Fatalf("claimed %d, want 1", n)
	}
	// Folded into the session, not handed back as a separate list: that is the
	// one place every surface already collects outbound attachments from.
	if len(sess.Images) != 1 || sess.Images[0] != "PICTURE" {
		t.Fatalf("attachment did not reach the turn's session: %#v", sess.Images)
	}
	// Claimed once. A second turn must not re-send the same picture.
	if hasStagedAttachments(session) {
		t.Error("staged attachments must be consumed by the claim")
	}
	if n := claimTaskAttachments(session, &ToolSession{}); n != 0 {
		t.Errorf("second claim returned %d, want 0", n)
	}
}

func TestSeveralFinishedTasksCoalesceTheirFiles(t *testing.T) {
	// Their notes already merge into one turn (joinWakeNotes); their files have
	// to merge with them, or the second render is announced and never sent.
	const session = "sess-stage-2"
	t.Cleanup(func() { claimTaskAttachments(session, &ToolSession{}) })

	stageTaskAttachments(session, TaskProduct{Images: []string{"FIRST"}})
	stageTaskAttachments(session, TaskProduct{Images: []string{"SECOND"}})

	sess := &ToolSession{}
	if n := claimTaskAttachments(session, sess); n != 2 {
		t.Fatalf("claimed %d, want both", n)
	}
	if len(sess.Images) != 2 || sess.Images[0] != "FIRST" || sess.Images[1] != "SECOND" {
		t.Fatalf("order or contents wrong: %#v", sess.Images)
	}
}

func TestOnlyAWakeTurnClaimsStagedFiles(t *testing.T) {
	// The gate on the claim. A recurring fire — or any other turn on the same
	// session — must not pick up a delivery meant for the wake, or a picture
	// goes out attached to something unrelated the user said.
	if !isTaskWake(taskWakeMarker + " The work you started earlier is done: image edit.") {
		t.Error("a wake note must be recognized as one")
	}
	if isTaskWake("[SCHEDULED UPDATE — fire 3, every 30m] check the feed") {
		t.Error("a scheduled fire must not claim a background task's files")
	}
	if isTaskWake("") {
		t.Error("an empty prompt is not a wake")
	}
}

func TestNothingStagedStagesNothing(t *testing.T) {
	const session = "sess-stage-3"
	if n := stageTaskAttachments(session, TaskProduct{Text: "just text"}); n != 0 {
		t.Errorf("staged %d for a text-only result, want 0", n)
	}
	if hasStagedAttachments(session) {
		t.Error("a text-only result must leave the wake free to join a live turn")
	}
}

func TestAWakeKeepsTheFactAndNotTheInstruction(t *testing.T) {
	// The thread has to keep what came back — the handle the user's next
	// message ("make it brighter") refers to. It must NOT keep "deliver this
	// now": an instruction left in history is one the model can read three
	// turns later and obey a second time.
	note := buildWakeNote("sess-note-1", "image edit: a cat", TaskProduct{
		Text: "The finished picture IS ATTACHED to this result. image#1 is its lasting handle.",
	}, nil)

	if !strings.Contains(note.history, "image#1") {
		t.Errorf("the handle naming the result must survive into history:\n%s", note.history)
	}
	if !strings.Contains(note.history, "image edit: a cat") {
		t.Errorf("history must say WHICH request this answers:\n%s", note.history)
	}
	if strings.Contains(note.history, "Deliver this to the user now") {
		t.Errorf("a one-turn instruction must not be persisted:\n%s", note.history)
	}
	// The delivering turn still gets told what to do.
	if !strings.Contains(note.prompt, "Deliver this to the user now") {
		t.Errorf("the prompt must carry the instruction:\n%s", note.prompt)
	}
	if !isTaskWake(note.prompt) || !isTaskWake(note.history) {
		t.Error("both halves must be recognizable as a task wake")
	}
}

func TestAFailedTaskSaysSoWithoutRetryingItself(t *testing.T) {
	note := buildWakeNote("sess-note-2", "image edit", TaskProduct{}, errTestTaskFailed)
	if !strings.Contains(note.prompt, errTestTaskFailed.Error()) {
		t.Errorf("the failure reason must reach the model:\n%s", note.prompt)
	}
	if !strings.Contains(note.prompt, "Do not silently retry") {
		t.Errorf("a failed render must not be quietly re-run:\n%s", note.prompt)
	}
}

var errTestTaskFailed = errors.New("the backend never returned an image")

func TestJoinedWakesMergeBothHalves(t *testing.T) {
	got := joinWakeNotes([]wakeNote{
		{prompt: "first prompt", history: "first fact"},
		{prompt: "second prompt", history: "second fact"},
	})
	for _, want := range []string{"first prompt", "second prompt"} {
		if !strings.Contains(got.prompt, want) {
			t.Errorf("merged prompt missing %q: %s", want, got.prompt)
		}
	}
	for _, want := range []string{"first fact", "second fact"} {
		if !strings.Contains(got.history, want) {
			t.Errorf("merged history missing %q: %s", want, got.history)
		}
	}
	// One result stays exactly itself — no numbering, no restructuring.
	solo := joinWakeNotes([]wakeNote{{prompt: "only", history: "fact"}})
	if solo.prompt != "only" || solo.history != "fact" {
		t.Errorf("a single note must pass through unchanged: %+v", solo)
	}
}

func TestOneFinishedTaskIsDeliveredOnce(t *testing.T) {
	// The double-announcement bug: the first result opened a coalescing window
	// AND was buffered in it, so its caller sent it, the window closed, and it
	// went out a second time fifteen seconds later — the second message
	// promising a picture the first had already carried off.
	const session = "sess-window-1"
	t.Cleanup(func() { takeBufferedWakes(session) })

	first := wakeNote{prompt: "first", history: "first fact"}
	if !claimWakeWindow(session, first) {
		t.Fatal("the first result must own delivery and send its own note")
	}
	if got := takeBufferedWakes(session); len(got) != 0 {
		t.Fatalf("the owner's note must NOT be buffered — it is being sent now (%d queued)", len(got))
	}
}

func TestResultsLandingInTheWindowGoOutTogether(t *testing.T) {
	const session = "sess-window-2"
	t.Cleanup(func() { takeBufferedWakes(session) })

	if !claimWakeWindow(session, wakeNote{prompt: "owner"}) {
		t.Fatal("first caller should own the window")
	}
	for _, n := range []string{"second", "third"} {
		if claimWakeWindow(session, wakeNote{prompt: n}) {
			t.Fatalf("%q arrived inside an open window — it must not open its own", n)
		}
	}
	notes := takeBufferedWakes(session)
	if len(notes) != 2 || notes[0].prompt != "second" || notes[1].prompt != "third" {
		t.Fatalf("window collected %+v, want the two siblings in order", notes)
	}
	// Closed: the next result opens a fresh window.
	if !claimWakeWindow(session, wakeNote{prompt: "later"}) {
		t.Error("a result after the window closed must own its own delivery")
	}
}

// A set still running says so to the turn that delivers this piece, and to
// nobody afterwards. Persisted, "start the next one" is an instruction a later
// turn can read again and obey — which is how a set of three acquires a fourth.
func TestAContinuationDrivesTheDeliveringTurnOnly(t *testing.T) {
	session := "sess-note-series"
	t.Cleanup(func() { claimTaskAttachments(session, &ToolSession{}) })
	note := buildWakeNote(session, "image generate: a red bicycle", TaskProduct{
		Text:         "Stored. image#1 is its lasting handle.",
		Images:       []string{"AAAA"},
		Continuation: SeriesContinuation(1, 3, "another take on: a red bicycle"),
	}, nil)

	if !strings.Contains(note.prompt, "PIECE 1 OF 3") {
		t.Errorf("the delivering turn must be told to start the next piece:\n%s", note.prompt)
	}
	if strings.Contains(note.history, "PIECE 1 OF 3") {
		t.Errorf("a one-turn instruction must not be persisted:\n%s", note.history)
	}
	// The fact half is unchanged: the handle still has to survive.
	if !strings.Contains(note.history, "image#1") {
		t.Errorf("the handle must still reach history:\n%s", note.history)
	}
}

// A failed piece ends the set. The wake for a failure already says not to retry
// silently, and carrying a continuation past a visible break keeps sending
// pictures for a request that went wrong.
func TestAFailedPieceCarriesNoContinuation(t *testing.T) {
	note := buildWakeNote("sess-note-series-failed", "image generate", TaskProduct{
		Continuation: SeriesContinuation(1, 3, "another take"),
	}, errTestTaskFailed)
	if strings.Contains(note.prompt, "PIECE 1 OF 3") {
		t.Errorf("a failure must not start the next piece:\n%s", note.prompt)
	}
}
