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
	"bytes"
	"image/jpeg"
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
	if len(queueSourceForComparison(sess, []string{"image#1"})) != 1 {
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

// A one-photo edit shows its one source: it is the subject, and the question is
// whether it survived.
func TestAOnePhotoEditShowsItsSource(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("A"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	before := len(sess.PendingViewImages)
	if got := queueSourceForComparison(sess, []string{"image#1"}); len(got) != 1 {
		t.Errorf("shown = %v, want the one source", got)
	}
	if got := len(sess.PendingViewImages) - before; got != 1 {
		t.Errorf("exactly one source should be attached, got %d", got)
	}
}

// A step from several photos is shown as ONE contact sheet in place of the
// result: every source and the result in one picture, which fits under a
// model's per-request picture limit where separate ones did not, and the older
// ones arrived as somebody else's paragraph.
func TestABlendIsOneContactSheet(t *testing.T) {
	sess := fidelitySession(t)
	RecordRecentImage(sess, pngOf(t, 64, 64, red), "received from craig", ImageFromUser)
	RecordRecentImage(sess, pngOf(t, 64, 64, green), "received from craig", ImageFromUser)
	refs := []string{"image#2", "image#1"}

	if got := queueSourceForComparison(sess, refs); got != nil {
		t.Errorf("a blend queued its sources separately: %v", got)
	}
	sources := checkSources(sess, refs)
	if len(sources) != 2 || sources[0].ref != "image#2" || sources[1].ref != "image#1" {
		t.Fatalf("sources = %+v, want both in the order passed", sources)
	}
	before := len(sess.PendingViewImages)
	sess.AppendViewImageAs(pngOf(t, 64, 64, blue), "the finished render (the AFTER picture)") // as saveImageResult does
	shown, ok := sheetInPlaceOfResult(sess, before, sources)
	if !ok || strings.Join(shown, ",") != "image#2,image#1" {
		t.Fatalf("sheet not built: %v %v", shown, ok)
	}
	if n := len(sess.PendingViewImages) - before; n != 1 {
		t.Fatalf("%d pictures queued for the step, want the one sheet", n)
	}
	v := sess.PendingViewImages[before]
	if !strings.Contains(v.Label, "contact sheet") {
		t.Errorf("label %q does not say it is a contact sheet", v.Label)
	}
	img, err := jpeg.Decode(bytes.NewReader(v.Data))
	if err != nil {
		t.Fatalf("the queued picture is not the sheet: %v", err)
	}
	if w := img.Bounds().Dx(); w != sheetGap+3*(sheetTileHeight+sheetGap) {
		t.Errorf("sheet width %d does not hold three tiles", w)
	}
}

// The swap is made only on the result this step queued: a picture another
// producer queued in between is never replaced.
func TestTheSheetNeverReplacesSomethingElse(t *testing.T) {
	sess := fidelitySession(t)
	RecordRecentImage(sess, pngOf(t, 64, 64, red), "received from craig", ImageFromUser)
	RecordRecentImage(sess, pngOf(t, 64, 64, green), "received from craig", ImageFromUser)
	sources := checkSources(sess, []string{"image#1", "image#2"})
	before := len(sess.PendingViewImages)
	sess.AppendViewImageAs([]byte("someone else's"), "a screenshot")
	sess.AppendViewImageAs(pngOf(t, 64, 64, blue), "the finished render (the AFTER picture)")
	if _, ok := sheetInPlaceOfResult(sess, before, sources); ok {
		t.Error("swapped with two pictures queued since, one of them not this step's")
	}
	if string(sess.PendingViewImages[before].Data) != "someone else's" {
		t.Error("another producer's picture was changed")
	}
}

// A sheet that cannot be built leaves the result as it was queued.
func TestAnUnbuildableSheetLeavesTheResult(t *testing.T) {
	sess := fidelitySession(t)
	sources := []sourceImage{{ref: "image#1", data: []byte("not a picture")}}
	before := len(sess.PendingViewImages)
	result := pngOf(t, 64, 64, blue)
	sess.AppendViewImageAs(result, "the finished render (the AFTER picture)")
	if _, ok := sheetInPlaceOfResult(sess, before, sources); ok {
		t.Error("reported a sheet that could not be built")
	}
	if !bytes.Equal(sess.PendingViewImages[before].Data, result) {
		t.Error("the result was replaced by a failed sheet")
	}
}

// The blend's question is about each numbered source on the sheet, and a held
// step is told not to be built on when one went missing.
func TestTheBlendNoteChecksEachSource(t *testing.T) {
	note := blendNote([]string{"image#1", "image#2"}, false)
	for _, want := range []string{"ONE contact sheet", "1 = image#1, 2 = image#2", "RESULT", "Check each numbered source", "say which number", "do not raise this again"} {
		if !strings.Contains(note, want) {
			t.Errorf("blend note lacks %q: %q", want, note)
		}
	}
	if strings.Contains(note, "next pass") {
		t.Errorf("a delivered blend is told about a next pass: %q", note)
	}
	held := blendNote([]string{"image#1", "image#2"}, true)
	if !strings.Contains(held, "Do NOT build the next pass on a step that lost a subject") || !strings.Contains(held, "go on to the next pass") {
		t.Errorf("a held step is not told to stop on a lost subject: %q", held)
	}
}

// No source to compare against means no question — silence beats asking
// something unanswerable.
func TestFidelityCheckIsSilentWithoutAResolvableSource(t *testing.T) {
	sess := fidelitySession(t)
	if len(queueSourceForComparison(sess, []string{"some-file.png"})) != 0 {
		t.Error("an unresolvable ref should produce no comparison")
	}
	if len(queueSourceForComparison(sess, nil)) != 0 {
		t.Error("a render with no sources has nothing to compare")
	}
}

// A detached render shows its sources too. Its result is shown on the round
// that reports it (saveImageResult), and that round used to compare an AFTER
// with no BEFORE: the source was skipped on the reasoning that nobody was
// looking, which stopped being true when the result started being shown.
func TestADetachedRenderStillComparesItsSources(t *testing.T) {
	sess := fidelitySession(t)
	sess.Detached = true
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	if len(queueSourceForComparison(sess, []string{"image#1"})) != 1 {
		t.Error("a detached render should show its source for the comparison")
	}
}

// Without a model to look with there is nothing to queue.
func TestNoModelNoComparison(t *testing.T) {
	sess := fidelitySession(t)
	sess.LLM = nil
	RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser)
	if len(queueSourceForComparison(sess, []string{"image#1"})) != 0 {
		t.Error("queued a comparison for a session with no model")
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
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "gen", "generated: a cat", ImageFromGenerated, false)
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
