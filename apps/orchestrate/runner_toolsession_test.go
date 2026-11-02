package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An uploaded picture used to reach the LLM as vision content and nothing else:
// the model could see it but had no id for it. Asked to blend that photo with
// one it had generated, the only handle it held was the generated image#1 — so
// it passed that, got a picture back, and reported success while the user's
// upload was never involved.

func TestUploadedImagesAreNameable(t *testing.T) {
	one := uploadedImageHandles(1)
	if !strings.Contains(one, "media#1") {
		t.Errorf("a single upload must be addressable:\n%s", one)
	}
	two := uploadedImageHandles(2)
	if !strings.Contains(two, "media#1") || !strings.Contains(two, "media#2") {
		t.Errorf("both uploads must be addressable:\n%s", two)
	}
	// Order is the user's, not ours — media#1 is the first one they attached.
	if !strings.Contains(two, "order they were sent") {
		t.Errorf("ordering must be stated, it decides subject vs background:\n%s", two)
	}
	if uploadedImageHandles(0) != "" {
		t.Error("no attachments must add no note")
	}
}

func TestUploadNoteForbidsTheSubstitution(t *testing.T) {
	// The actual failure: it reached for a picture it had made instead.
	note := uploadedImageHandles(1)
	for _, want := range []string{"do NOT invent a filename", "do NOT substitute a picture you made earlier"} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
}

func TestUploadNoteIsNotTheChannelManifest(t *testing.T) {
	// The channel manifest tells the model NOT to send a photo back, because in
	// a group thread everyone already received it. Here that would suppress the
	// delivery the user is asking for.
	note := uploadedImageHandles(1)
	if strings.Contains(note, "do NOT send one back") || strings.Contains(note, "already received") {
		t.Errorf("channel-thread advice must not leak into the web surface:\n%s", note)
	}
}

// The fallback hands the loop the tool's whole def, so a confirm tool called
// by name is confirmed (TestAToolCalledByNameIsConfirmedAndJudged in core).
// It used to hand back the handler alone.
func TestALazyToolCalledByNameKeepsItsConfirm(t *testing.T) {
	turn := &chatTurn{
		lazyCustomToolDefs: map[string]AgentToolDef{"pay": {Tool: Tool{Name: "pay"}, NeedsConfirm: true,
			Handler: func(context.Context, map[string]any) (string, error) { return "", nil }}},
		loadedCustomTools:       map[string]bool{},
		deferredAuthoringDefs:   map[string]AgentToolDef{},
		deferredAuthoringLoaded: map[string]bool{},
	}
	td, ok := turn.lazyToolDef("pay")
	if !ok || !td.NeedsConfirm || td.Handler == nil {
		t.Errorf("the fallback lost what the loop decides from: ok=%v %+v", ok, td)
	}
	if !turn.loadedCustomTools["pay"] {
		t.Error("the tool was not marked loaded for the next round")
	}
}
