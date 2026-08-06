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

// What a wired backend is called on re-edit. Picking "blend" and reopening the
// form showed "edit" — the classifier tested for the ABSENCE of a prompt, so
// any composite that also takes text (most of them, and the shape the framework
// now asks for) was filed as an edit.
func TestWorkflowTypeCountsPhotosNotPrompts(t *testing.T) {
	twoWithPrompt := ComfyNodeMap{ImageNodes: []string{"1", "2"}, PromptNodes: []string{"6"}}
	if got := ComfyWorkflowTypeOf(twoWithPrompt); got != ComfyTypeBlend {
		t.Errorf("a two-photo composite that takes a prompt is a blend, got %q", got)
	}
	twoNoPrompt := ComfyNodeMap{ImageNodes: []string{"1", "2"}}
	if got := ComfyWorkflowTypeOf(twoNoPrompt); got != ComfyTypeBlend {
		t.Errorf("a two-photo composite with no prompt is still a blend, got %q", got)
	}
	oneWithPrompt := ComfyNodeMap{ImageNodes: []string{"1"}, PromptNodes: []string{"6"}}
	if got := ComfyWorkflowTypeOf(oneWithPrompt); got != ComfyTypeEdit {
		t.Errorf("one photo plus text is an edit, got %q", got)
	}
	// A promptless single-image graph (an upscale) is nearer an edit than a
	// blend — it changes one picture.
	if got := ComfyWorkflowTypeOf(ComfyNodeMap{ImageNodes: []string{"1"}}); got != ComfyTypeEdit {
		t.Errorf("a single-image processing graph should read as edit, got %q", got)
	}
	if got := ComfyWorkflowTypeOf(ComfyNodeMap{PromptNodes: []string{"6"}}); got != ComfyTypeGenerate {
		t.Errorf("no image input is a generate, got %q", got)
	}
}

// The built-in starters must round-trip to the type that produced them, or the
// form contradicts itself the first time anyone reopens it.
func TestBuiltinStartersRoundTrip(t *testing.T) {
	for _, c := range []struct{ kind, graph string }{
		{ComfyTypeGenerate, ComfyStarterGraph(ComfyTypeGenerate)},
		{ComfyTypeEdit, ComfyStarterGraph(ComfyTypeEdit)},
		{ComfyTypeBlend, ComfyStarterGraph(ComfyTypeBlend)},
	} {
		var spec RestImageSpec
		if _, err := ApplyComfyWorkflow(&spec, c.graph, ""); err != nil {
			t.Fatalf("%s starter should wire: %v", c.kind, err)
		}
		if got := ComfyWorkflowTypeOf(spec.ComfyMap); got != c.kind {
			t.Errorf("%s starter reads back as %q", c.kind, got)
		}
	}
}

// A cap below the mapped node count is permitted but hazardous: the unwritten
// nodes render the placeholder the workflow was exported with. It has to say so
// where the person configuring it will read it.
func TestLowCapWarnsAboutPlaceholders(t *testing.T) {
	spec := RestImageSpec{MaxInputImages: 2}
	warns, err := ApplyComfyWorkflow(&spec, threeInputGraph, "")
	if err != nil {
		t.Fatalf("a low cap is permitted, not fatal: %v", err)
	}
	var found bool
	for _, w := range warns {
		if strings.Contains(w, "max_input_images is 2 but 3 image node(s) are mapped") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a placeholder warning, got %v", warns)
	}
}

// No cap set is the ordinary case and must stay quiet.
func TestNoCapDoesNotWarn(t *testing.T) {
	var spec RestImageSpec
	warns, err := ApplyComfyWorkflow(&spec, threeInputGraph, "")
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	for _, w := range warns {
		if strings.Contains(w, "max_input_images") {
			t.Errorf("unexpected cap warning with no cap set: %q", w)
		}
	}
}
