// A correctly wired graph that the importer could not read.
//
// Flux2 fails three assumptions at once: the node whose class says KSampler is
// a SELECTOR holding a sampler name, the prompt reaches the sampler through a
// BasicGuider rather than a positive input, and the seed lives on a separate
// RandomNoise node. Each on its own was enough to reject the import — with an
// error naming the graph as the problem, when nothing was wrong with it.
package core

import (
	"os"
	"path/filepath"
	"testing"
)

func flux2Graph(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "flux2_klein_workflow.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestFlux2WorkflowImports(t *testing.T) {
	spec, warns, err := NewComfyImageSpec("http://localhost:8188", "no_auth", flux2Graph(t), "")
	if err != nil {
		t.Fatalf("a wired Flux2 graph must import: %v", err)
	}

	// The prompt is three hops out: sampler → guider → FluxGuidance → encoder.
	if !eqStrs(spec.ComfyMap.PromptNodes, []string{"98:6"}) {
		t.Errorf("prompt nodes = %v, want the CLIPTextEncode at 98:6", spec.ComfyMap.PromptNodes)
	}
	// The seed is on RandomNoise, not the sampler. Unmapped, every render
	// reuses the exported number and the backend returns one picture forever.
	if !eqStrs(spec.ComfyMap.SeedNodes, []string{"98:25"}) || spec.ComfyMap.SeedKey != "noise_seed" {
		t.Errorf("seed = %v key %q, want [98:25] noise_seed", spec.ComfyMap.SeedNodes, spec.ComfyMap.SeedKey)
	}
	// BOTH size-bearing nodes, not just the latent. Flux2Scheduler carries its
	// own width/height and derives sigmas from the image area, so a render that
	// moves the latent alone leaves the scheduler solving for a resolution
	// nobody is rendering. That does not fail — it quietly degrades the picture,
	// which is why this assertion used to pass while the backend was wrong.
	if !eqStrs(spec.ComfyMap.WidthNodes, []string{"98:47", "98:48"}) {
		t.Errorf("width nodes = %v, want the Flux2 latent AND the scheduler", spec.ComfyMap.WidthNodes)
	}
	if !eqStrs(spec.ComfyMap.HeightNodes, []string{"98:47", "98:48"}) {
		t.Errorf("height nodes = %v — height must move with width or the two disagree", spec.ComfyMap.HeightNodes)
	}
	if spec.ComfyMap.OutputNode != "9" {
		t.Errorf("output node = %q, want 9", spec.ComfyMap.OutputNode)
	}
	// No image input: this is a text-to-image backend.
	if got := ComfyWorkflowTypeOf(spec.ComfyMap); got != ComfyTypeGenerate {
		t.Errorf("type = %q, want generate", got)
	}
	if len(warns) != 0 {
		t.Errorf("a fully mapped graph should import clean, got %v", warns)
	}
}

// KSamplerSelect holds a sampler NAME. Mapping it as the sampler leaves the
// prompt trace starting at a node with no links to follow.
func TestSamplerDetectionSkipsSelectors(t *testing.T) {
	graph := map[string]map[string]any{
		"1": {"class_type": "KSamplerSelect", "inputs": map[string]any{"sampler_name": "euler"}},
		"2": {"class_type": "SamplerCustomAdvanced", "inputs": map[string]any{"guider": []any{"3", 0}}},
		"3": {"class_type": "BasicGuider", "inputs": map[string]any{"conditioning": []any{"4", 0}}},
		"4": {"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "hello"}},
	}
	if got := findComfySampler(graph); got != "2" {
		t.Errorf("sampler = %q, want the node that consumes a guider", got)
	}
}

// A plain KSampler graph must be unaffected — positive is still tried first.
func TestClassicSamplerStillWins(t *testing.T) {
	graph := map[string]map[string]any{
		"1": {"class_type": "KSampler", "inputs": map[string]any{
			"positive": []any{"2", 0}, "negative": []any{"3", 0}, "seed": 42, "latent_image": []any{"4", 0}}},
		"2": {"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "a cat"}},
		"3": {"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "blurry"}},
		"4": {"class_type": "EmptyLatentImage", "inputs": map[string]any{"width": 512, "height": 512}},
	}
	sampler := findComfySampler(graph)
	if sampler != "1" {
		t.Fatalf("sampler = %q, want 1", sampler)
	}
	// The POSITIVE encoder, never the negative — the fallback walk skips it.
	if got := traceSamplerPrompt(graph, sampler, comfyInputs(graph, sampler)); got != "2" {
		t.Errorf("prompt node = %q, want the positive encoder 2", got)
	}
}
