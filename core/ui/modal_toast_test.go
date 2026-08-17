package ui

// A toast raised while a dialog is open.
//
// uiOpenModal's overlay sits at z-index 1000 and the form-chip modal's
// at 1100; the toast was at 50. So every "Saved ✓" and every "Save
// failed: …" raised from inside a modal rendered BEHIND the backdrop.
// The visible result of a failed submit was a button that went back to
// normal and said nothing — which is the same thing a button that does
// nothing looks like.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestAToastOutranksEveryModalLayer(t *testing.T) {
	prelude := readRuntimeFile(t, "00_prelude.js")

	toast := regexp.MustCompile(`z-index:(\d+);[^']*box-shadow`).FindStringSubmatch(prelude)
	if toast == nil {
		t.Fatal("could not find the toast's z-index")
	}
	tz, _ := strconv.Atoi(toast[1])

	// The layers that can be OPEN while a form submits underneath them.
	// Deliberately not "every z-index in the app": the image lightbox
	// sits at 10000 and nothing submits a form under it, so demanding
	// the toast clear that too would be a rule about nothing.
	layers := map[string]int{}
	if m := regexp.MustCompile(`justify-content:center;z-index:(\d+)`).FindStringSubmatch(prelude); m != nil {
		layers["uiOpenModal overlay"], _ = strconv.Atoi(m[1])
	}
	css := readRuntimeCSSForTest(t)
	if at := strings.Index(css, ".ui-form-modal-overlay"); at >= 0 {
		if m := regexp.MustCompile(`z-index:\s*(\d+)`).FindStringSubmatch(css[at : at+200]); m != nil {
			layers["ui-form-modal-overlay"], _ = strconv.Atoi(m[1])
		}
	}
	if len(layers) != 2 {
		t.Fatalf("could not read both modal layers, got %v — the selectors moved", layers)
	}
	for name, z := range layers {
		if tz <= z {
			t.Errorf("a toast at z-index %d sits under %s at %d — it would be invisible over a dialog", tz, name, z)
		}
	}
}

// And the toast is not the only place a failure lands. It disappears
// after three seconds, and a server's reason for refusing is often a
// paragraph; the form says it too, next to the fields somebody would
// change.
func TestASubmitFailureIsWrittenIntoTheFormItFailedIn(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	// The SUBMIT path specifically — that string appears in a dozen
	// handlers, and the one under test is the one next to the submit
	// button's own note element.
	anchor := strings.Index(src, "// Post-submit note:")
	if anchor < 0 {
		t.Fatal("the submit button's block moved")
	}
	rest := src[anchor:]
	at := strings.Index(rest, "showToast('Save failed: ' + err.message)")
	if at < 0 {
		t.Fatal("the submit failure path moved")
	}
	window := rest[max0(at-700) : at]
	if !strings.Contains(window, "msgEl.textContent = String((err && err.message) || err)") {
		t.Errorf("a failed submit should say so in the form:\n%s", window)
	}
	if !strings.Contains(window, "var(--danger") {
		t.Error("and it should read as a failure, not as a note")
	}
	// A slow submit says it is still working. An endpoint that asks a
	// model to draft something runs for a minute, and a button reading
	// "Saving…" for that long is indistinguishable from a hang.
	if !strings.Contains(src, "Still working. This one runs a model") {
		t.Error("a long submit gives no sign it is still alive")
	}
	// And the note is cleared on success, or a form that just saved
	// keeps showing why it once failed.
	if !strings.Contains(src, "msgEl.style.display = 'none';\n            if (!cfg.redirect_url)") {
		t.Error("a successful submit should clear the previous failure")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
