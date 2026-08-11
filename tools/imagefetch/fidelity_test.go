// Checking that the subject survived the render.
//
// showToModel deliberately refuses to let the model judge WHO someone is —
// handed a stranger's photo, an agent that does not recognize the face has
// learned nothing, and treating that as a failure threw away correct results.
// That reasoning holds for a found picture and stops holding the moment a
// SOURCE is in hand: resemblance between two pictures is answerable from
// pixels, and it is the failure an edit model actually has.
package imagefetch

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func fidelitySession(t *testing.T) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	return &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), LLM: &Session{}}
}

func TestFidelityCheckAsksTheAnswerableQuestion(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	if !queueSourceForComparison(sess, []string{"image#1"}) {
		t.Fatal("an edit with a resolvable source should carry a comparison")
	}
	note := fidelityNote("image#1")
	// The distinction the whole thing rests on.
	if !strings.Contains(note, "not who the person is") {
		t.Errorf("must not ask the model to identify anyone, got %q", note)
	}
	if !strings.Contains(note, "SAME ONE as in the first") {
		t.Errorf("should ask about resemblance to the source, got %q", note)
	}
	// The order it states must match the order the images are queued in, or the
	// comparison is made with the two pictures swapped.
	if !strings.Contains(note, "FIRST the source") || !strings.Contains(note, "SECOND the edited result") {
		t.Errorf("the note must name which picture is which, got %q", note)
	}
	// A finding has to lead somewhere, and a non-finding has to stop.
	if !strings.Contains(note, "try once more") {
		t.Error("a visible mismatch should have a next step")
	}
	if !strings.Contains(note, "do not raise this again") {
		t.Error("a survived likeness must not loop")
	}
}

// The source is put in front of the model, not just described.
func TestFidelityCheckAttachesTheSource(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	before := len(sess.PendingViewImages)
	queueSourceForComparison(sess, []string{"image#1"})
	if len(sess.PendingViewImages) != before+1 {
		t.Error("the source should be attached for the comparison")
	}
}

// Only the first: the ordering this tool documents makes it the subject, and
// attaching all three would triple the cost of every edit.
func TestFidelityCheckShowsOnlyTheFirstSource(t *testing.T) {
	sess := fidelitySession(t)
	for i := 0; i < 3; i++ {
		if RecordRecentImage(sess, []byte{byte('A' + i)}, "received from craig", ImageFromUser) == "" {
			t.Fatal("record failed")
		}
	}
	before := len(sess.PendingViewImages)
	queueSourceForComparison(sess, []string{"image#1", "image#2", "image#3"})
	if got := len(sess.PendingViewImages) - before; got != 1 {
		t.Errorf("exactly one source should be attached, got %d", got)
	}
}

// No source to compare against means no question — silence beats asking
// something unanswerable.
func TestFidelityCheckIsSilentWithoutAResolvableSource(t *testing.T) {
	sess := fidelitySession(t)
	if queueSourceForComparison(sess, []string{"some-file.png"}) {
		t.Error("an unresolvable ref should produce no comparison")
	}
	if queueSourceForComparison(sess, nil) {
		t.Error("a render with no sources has nothing to compare")
	}
}

// A detached render has nobody looking this round, so attaching a source would
// spend tokens on a question no one will answer.
func TestFidelityCheckSkipsDetachedRuns(t *testing.T) {
	sess := fidelitySession(t)
	sess.Detached = true
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	if queueSourceForComparison(sess, []string{"image#1"}) {
		t.Error("a detached run should skip the comparison")
	}
}

// A reference is a likeness to draw FROM, not an object to place IN the frame.
// Given two pictures and no statement of their roles, the renderer sometimes
// reads one literally: a face swap that lands the right face and then puts a
// thumbnail of the original on the forehead, or a result carrying the source
// inset in a corner. The guard names those artifacts because the failure is a
// literal reading of the input, not a stylistic preference.
func TestEditPromptForbidsInliningTheSources(t *testing.T) {
	g := editCompositingGuard()
	for _, want := range []string{
		"ONE finished image",
		"references for likeness",
		"must not appear as objects inside the result",
		"inset", "thumbnail", "corner overlay", "picture-in-picture",
		"duplicate of a face", // the forehead artifact, named directly
	} {
		if !strings.Contains(g, want) {
			t.Errorf("guard missing %q:\n%s", want, g)
		}
	}
}

// The guard rides on the prompt the backend actually receives, after scrubbing
// — a guard that only exists in a helper protects nothing.
func TestEditPlanCarriesTheGuardIntoThePrompt(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	got, _, err := buildEditPrompt(sess, "put a hat on the person in the first image", []string{"image#1"})
	if err != nil {
		t.Fatalf("buildEditPrompt: %v", err)
	}
	if !strings.Contains(got, "must not appear as objects inside the result") {
		t.Errorf("the guard did not reach the backend prompt:\n%s", got)
	}
	if !strings.Contains(got, "put a hat on") {
		t.Errorf("the caller's own prompt must survive:\n%s", got)
	}
}

// An empty prompt must stay empty. A backend that requires a prompt validates
// by checking for one, so padding it with the guard would let a promptless call
// through to render from the guard text alone — and a blend backend has no text
// node for it in the first place.
func TestEditGuardNeverPadsAnEmptyPrompt(t *testing.T) {
	sess := fidelitySession(t)
	got, _, err := buildEditPrompt(sess, "", []string{"image#1", "image#2"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("an empty prompt must survive as empty, got %q", got)
	}
	if strings.TrimSpace("   ") != "" { // guard against a whitespace-only prompt too
		t.Fatal("unreachable")
	}
	if got, _, _ = buildEditPrompt(sess, "   ", []string{"image#1"}); strings.Contains(got, "must not appear") {
		t.Errorf("a whitespace-only prompt must not be padded, got %q", got)
	}
}

// image#N is positional — the code says so at the definition: "image#1 is
// whatever is newest right now". Saving a render pushes the RESULT to image#1
// and slides the user's original to image#2, so a number the model noted
// earlier now points at a different picture. Telling it the number was a
// "lasting handle" and to "use it to edit later" is what produced edits landing
// on unrelated images.
func TestRenderResultDoesNotPromiseAPositionalRefIsDurable(t *testing.T) {
	sess := fidelitySession(t)
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "gen", "generated: a cat", ImageFromGenerated)
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}
	if strings.Contains(msg, "lasting handle") {
		t.Errorf("a positional ref must not be described as lasting:\n%s", msg)
	}
	// It has to say what IS true, and hand over something durable to use
	// instead — an accurate warning with no alternative just leaves the model
	// with the number it was warned about.
	for _, want := range []string{"POSITION", "moves down", "image#r."} {
		if !strings.Contains(msg, want) {
			t.Errorf("result text missing %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "always means THIS picture") {
		t.Errorf("the stable ref must be offered as the thing to carry:\n%s", msg)
	}
}

// The ring genuinely renumbers: this is the mechanism behind the symptom, and
// pinning it stops a future change from quietly making refs look stable.
func TestSavingAnImageRenumbersEveryEarlierRef(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("ORIGINAL"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	first, ok := ResolveRecentImage(sess, "image#1")
	if !ok || string(first) != "ORIGINAL" {
		t.Fatalf("image#1 should be the original, got %q", first)
	}
	if RecordRecentImage(sess, []byte("RESULT"), "generated", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	now, _ := ResolveRecentImage(sess, "image#1")
	moved, _ := ResolveRecentImage(sess, "image#2")
	if string(now) != "RESULT" || string(moved) != "ORIGINAL" {
		t.Errorf("after a save, image#1=%q image#2=%q — the ref the model held now means something else", now, moved)
	}
}
