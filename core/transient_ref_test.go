// A handle that does not survive, written into something that does.
//
// The reported note: "Reference images confirmed: Craig headset edit (image#9),
// Amsterdam selfie (media#10), and reference photo of man with beard". Every
// handle in it is transient — image#N is a POSITION that comes to mean a
// different picture as new ones arrive, media#N is gone when the turn ends —
// and a pinned fact is forever. It reads as authoritative for as long as it is
// wrong, which is from a few turns after it was written until something evicts
// it, and nothing does.
package core

import "testing"

func TestPositionalAndTurnScopedRefsAreTransient(t *testing.T) {
	got := TransientImageRefs("Reference images confirmed: Craig headset edit (image#9), Amsterdam selfie (media#10)")
	if len(got) != 2 || got[0] != "image#9" || got[1] != "media#10" {
		t.Errorf("both handle kinds should be caught in order, got %v", got)
	}
}

// A KEPT image is the durable form and the whole point of the fix — it must not
// be flagged, or the guidance would point at something also refused.
func TestKeptNamesAreNotTransient(t *testing.T) {
	for _, s := range []string{
		"the brand mark is image#brand_mark",
		"use image#wren for the dog",
		"image#craig_headset is the reference",
	} {
		if got := TransientImageRefs(s); len(got) != 0 {
			t.Errorf("%q names a kept image and should be durable, got %v", s, got)
		}
	}
}

func TestOrdinaryNotesAreUntouched(t *testing.T) {
	for _, s := range []string{
		"User prefers metric units",
		"the issue is #9 in the tracker",
		"image quality matters more than speed",
		"",
	} {
		if got := TransientImageRefs(s); len(got) != 0 {
			t.Errorf("%q should not be flagged, got %v", s, got)
		}
	}
}

// Repeats collapse: the refusal names what is wrong, and naming image#9 three
// times says nothing extra.
func TestRepeatedRefsAreDeduplicated(t *testing.T) {
	got := TransientImageRefs("image#9 and IMAGE#9 and image#9 again, plus media#1")
	if len(got) != 2 {
		t.Errorf("repeats should collapse case-insensitively, got %v", got)
	}
}
