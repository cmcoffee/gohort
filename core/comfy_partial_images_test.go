// An underfilled compose graph renders somebody else's photo.
//
// Only as many image nodes were written as there were images, so a three-input
// blend given one picture ran its other two against the filenames the workflow
// was SAVED with. The render succeeded and came back as one supplied picture
// composited with two from whenever that workflow was exported — which reads as
// "it only blended one of my three images", not as a wiring error.
package core

import (
	"strings"
	"testing"
)

// threeInputGraph is a minimal compose workflow: three LoadImage nodes, each
// with the placeholder filename an export would bake in.
const threeInputGraph = `{
  "1": {"class_type": "LoadImage", "inputs": {"image": "stale-one.png"}},
  "2": {"class_type": "LoadImage", "inputs": {"image": "stale-two.png"}},
  "3": {"class_type": "LoadImage", "inputs": {"image": "stale-three.png"}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["1", 0]}}
}`

func threeInputMap() ComfyNodeMap {
	return ComfyNodeMap{ImageNodes: []string{"1", "2", "3"}, ImageKey: "image", OutputNode: "9"}
}

func uploaded(names ...string) []ComfyUploadedImage {
	out := make([]ComfyUploadedImage, 0, len(names))
	for _, n := range names {
		out = append(out, ComfyUploadedImage{Name: n})
	}
	return out
}

func TestPartialFillIsRefused(t *testing.T) {
	_, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "blend these", Images: uploaded("mine.png"),
	})
	if err == nil {
		t.Fatal("one image into a three-input compose graph must not render — the other two would be placeholders")
	}
	if !strings.Contains(err.Error(), "composes 3 source photos and got 1") {
		t.Errorf("the error should name both counts, got %q", err)
	}
}

// The full set renders, and every node gets ITS image — order is what puts the
// subject and the background in the right places.
func TestFullFillWritesEveryNodeInOrder(t *testing.T) {
	body, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "blend these", Images: uploaded("a.png", "b.png", "c.png"),
	})
	if err != nil {
		t.Fatalf("a complete set should render: %v", err)
	}
	for _, want := range []string{"a.png", "b.png", "c.png"} {
		if !strings.Contains(body, want) {
			t.Errorf("body should carry %q", want)
		}
	}
	for _, stale := range []string{"stale-one.png", "stale-two.png", "stale-three.png"} {
		if strings.Contains(body, stale) {
			t.Errorf("saved placeholder %q survived into the render", stale)
		}
	}
}

// A backend deliberately capped below its node count is a config the operator
// chose; the guard must not turn it into a permanent error.
func TestExplicitCapBelowNodeCountStillRenders(t *testing.T) {
	if _, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "change this", Images: uploaded("mine.png"), ExpectedImages: 1,
	}); err != nil {
		t.Fatalf("a backend that asks for one image should accept one: %v", err)
	}
}

// An ExpectedImages larger than the mapped nodes cannot be satisfied and must
// fall back to the real node count rather than demanding the impossible.
func TestExpectedAboveNodeCountFallsBackToNodes(t *testing.T) {
	if _, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "blend", Images: uploaded("a.png", "b.png", "c.png"), ExpectedImages: 9,
	}); err != nil {
		t.Fatalf("filling every mapped node should be enough: %v", err)
	}
}

// The zero case keeps its own message — it is a different mistake (a text-only
// render aimed at a compose backend) and says so.
func TestNoImagesKeepsItsOwnDiagnosis(t *testing.T) {
	_, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{Prompt: "a dragon"})
	if err == nil {
		t.Fatal("a compose backend asked for a text-only render must refuse")
	}
	if !strings.Contains(err.Error(), "text-only render") {
		t.Errorf("the zero case should keep its specific message, got %q", err)
	}
}
