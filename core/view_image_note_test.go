package core

import (
	"strings"
	"testing"
)

// Several image producers can run in ONE round — deliberately, since a user
// asking for three renders gets three — and they append to a single queue from
// separate goroutines as each backend answers. So queue order tracks completion
// order, not the order the tool calls are listed in.
//
// The old note told the model the opposite: images arrive "in order" and "the
// preceding tool result says what they are". With three parallel renders there
// are three preceding results and no way to tell which is which, so the model
// re-derived an alignment the code never preserved. It reads exactly like a
// correct answer when it is wrong.
func TestViewImageNoteNamesEachImageInsteadOfImplyingOrder(t *testing.T) {
	note := viewImageNote([]ViewImage{
		{Data: []byte("a"), Label: "the SOURCE photo, BEFORE the edit (image#r.7a3f)"},
		{Data: []byte("b"), Label: "the finished render (the AFTER picture)"},
	})

	for _, want := range []string{"Image 1 of 2", "Image 2 of 2", "SOURCE photo", "finished render"} {
		if !strings.Contains(note, want) {
			t.Errorf("note should carry %q:\n%s", want, note)
		}
	}
	// The specific false claim that motivated this.
	if strings.Contains(note, "in order — the preceding tool result") {
		t.Errorf("note still asserts the positional alignment that parallel producers break:\n%s", note)
	}
	// Labels must appear in attachment order, since that is how they are matched.
	if strings.Index(note, "SOURCE photo") > strings.Index(note, "finished render") {
		t.Errorf("labels are out of attachment order:\n%s", note)
	}
}

// A lone image needs no scaffolding — there is nothing to confuse it with — so
// the plain wording stays. Verbosity on every single-image turn would be a real
// cost paid for an ambiguity that cannot occur.
func TestViewImageNoteStaysPlainForASingleUnlabeledImage(t *testing.T) {
	note := viewImageNote([]ViewImage{{Data: []byte("a")}})
	if !strings.Contains(note, "Here is 1 image") {
		t.Errorf("single-image note should stay plain:\n%s", note)
	}
	if strings.Contains(note, "Image 1 of 1") {
		t.Errorf("single image should not get the enumerated treatment:\n%s", note)
	}
}

// Multiple images with NO labels is the case the old wording handled worst: it
// asserted an order for exactly the situation where order means least. Say the
// order is not guaranteed rather than implying it is.
func TestViewImageNoteWarnsWhenNothingIsLabeled(t *testing.T) {
	note := viewImageNote([]ViewImage{{Data: []byte("a")}, {Data: []byte("b")}})
	if !strings.Contains(note, "NOT necessarily the order") {
		t.Errorf("unlabeled multi-image note must not imply an alignment:\n%s", note)
	}
}

// A producer that labels its image must not have that label silently dropped
// because a different producer in the same round did not.
func TestViewImageNoteKeepsLabelsWhenOnlySomeAreSet(t *testing.T) {
	note := viewImageNote([]ViewImage{
		{Data: []byte("a")},
		{Data: []byte("b"), Label: "video frame 2 of 2, in time order"},
	})
	if !strings.Contains(note, "video frame 2 of 2") {
		t.Errorf("a set label must survive an unlabeled sibling:\n%s", note)
	}
	if !strings.Contains(note, "unlabeled") {
		t.Errorf("the unlabeled one should be marked as such, not left to be guessed:\n%s", note)
	}
}

// The bytes handed to the model must stay in the same order as the labels that
// describe them — the labels are matched by position within the message, so a
// reordering here would reintroduce the bug in a harder-to-see form.
func TestViewImageBytesTrackLabelOrder(t *testing.T) {
	imgs := []ViewImage{
		{Data: []byte("first"), Label: "one"},
		{Data: []byte("second"), Label: "two"},
	}
	got := viewImageBytes(imgs)
	if len(got) != 2 || string(got[0]) != "first" || string(got[1]) != "second" {
		t.Fatalf("bytes reordered relative to labels: %q", got)
	}
}
