package imagefetch

// The blend case is where the "keep the subject out of the prompt" rule and the
// "say which part comes from where" requirement meet, and the first version of
// this guidance got it wrong in both directions: it forbade describing anything
// already in the pictures (which is the whole of a blend instruction) while
// shipping a worked example that named the subjects. An example outweighs a
// rule when a model is copying a pattern.

import (
	"strings"
	"testing"
)

func editPromptDesc(t *testing.T) string {
	t.Helper()
	d := imageSchemaFor(imageActions{generate: true, edit: true}).params["prompt"].Description
	if d == "" {
		t.Fatal("no prompt parameter description")
	}
	return d
}

func TestBlendGuidanceAsksForPartAndSource(t *testing.T) {
	d := editPromptDesc(t)
	// The routing half: which part, which picture.
	if !strings.Contains(d, "the face from the first picture on the body in the second") {
		t.Errorf("the blend example must be positional:\n%s", d)
	}
	// The identity half: never the name, never the look.
	for _, want := range []string{"no names", "never by name and never by description"} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q:\n%s", want, d)
		}
	}
	// And the example that taught the wrong pattern is gone. Naming the
	// subjects is exactly what the rule above forbids, so an example doing it
	// is the model's permission slip.
	if strings.Contains(d, "bear's body") || strings.Contains(d, "pig's snout") {
		t.Errorf("the subject-naming blend example must not survive:\n%s", d)
	}
	// The over-broad rule is gone too: a blend HAS to talk about what is in
	// the pictures, and forbidding that outright made the two halves conflict.
	if strings.Contains(d, "Do not describe who or what is already in the pictures") {
		t.Errorf("that rule forbids the blend instruction itself:\n%s", d)
	}
}
