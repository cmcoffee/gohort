package core

// Asking a COMPOSE workflow for a text-only picture. It has image inputs and
// nothing to put in them, so the graph runs against the filenames it was
// exported with — a photo from whenever that export happened — and returns the
// prompt composited with it. The result looks like a poor render, not a wiring
// error, which is why it publishes.

import (
	"strings"
	"testing"
)

func TestAComposeBackendRefusesATextOnlyRender(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, qwenBlendGraph(t), ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	_, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{Prompt: "a dramatic blog header"})
	if err == nil {
		t.Fatal("a blend backend must refuse a text-only render rather than draw against its placeholders")
	}
	for _, want := range []string{"SOURCE PHOTOS", "text-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should say what is wrong (%q missing): %v", want, err)
		}
	}
	// With its sources supplied it builds normally.
	if _, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: "blend them",
		Images: []ComfyUploadedImage{{Name: "a.png"}, {Name: "b.png"}},
	}); err != nil {
		t.Errorf("a compose backend with its images must build: %v", err)
	}
}

func TestATextToImageBackendIsUnaffected(t *testing.T) {
	// The guard keys on the graph HAVING image inputs, so a txt2img workflow —
	// which has none — is untouched.
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, ComfyStarterGraph("generate"), ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(s.ComfyMap.ImageNodes) != 0 {
		t.Skipf("starter graph has image nodes (%v) — not the shape under test", s.ComfyMap.ImageNodes)
	}
	if _, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{Prompt: "a cat"}); err != nil {
		t.Errorf("a text-to-image backend must still render from text alone: %v", err)
	}
}
