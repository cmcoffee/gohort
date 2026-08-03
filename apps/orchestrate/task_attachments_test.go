// Delivering what a background task produced. The text half always worked; the
// files it made are the half that went missing, and these pin the staging that
// carries them from the finished task to the turn that announces it.
package orchestrate

import (
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
