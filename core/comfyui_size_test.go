package core

import (
	"encoding/json"
	"testing"
)

// One requested size is often several nodes in the graph. Flux 2 carries
// width/height on BOTH the empty latent and the scheduler, and the scheduler
// derives its sigmas from the image area — so moving only the latent leaves it
// solving for a resolution that is no longer being rendered. That does not
// fail. It quietly degrades the picture, which is worse: the backend looks like
// it is working and the size control looks like it took effect.

func sizeGraph(t *testing.T, raw string) map[string]map[string]any {
	t.Helper()
	var g map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestOnlyTheSamplersAncestorsCount — a node DOWNSTREAM of the sampler with a
// width and height is doing something else with it (an upscale, a crop), and
// rewriting that to the requested size would break a deliberate step.
func TestOnlyTheSamplersAncestorsCount(t *testing.T) {
	g := sizeGraph(t, `{
	  "s":  {"inputs": {"latent_image": ["lat", 0]}, "class_type": "KSampler"},
	  "lat":{"inputs": {"width": 512, "height": 512}, "class_type": "EmptyLatentImage"},
	  "up": {"inputs": {"samples": ["s", 0], "width": 2048, "height": 2048}, "class_type": "LatentUpscale"}
	}`)
	got := comfySizeNodesFeeding(g, "s")
	for _, id := range got {
		if id == "up" {
			t.Error("an upscale downstream of the sampler was treated as the render size — " +
				"the requested size would overwrite a deliberate step")
		}
	}
	if len(got) != 1 || got[0] != "lat" {
		t.Errorf("found %v, want just the latent", got)
	}
}

// TestALinkedSizeIsLeftAlone — a width driven by a link is computed by the
// graph, and writing over it either loses the wiring or is silently recomputed.
// Same reasoning that leaves a linked `steps` unmapped rather than offering a
// knob that can only do harm.
func TestALinkedSizeIsLeftAlone(t *testing.T) {
	g := sizeGraph(t, `{
	  "s":   {"inputs": {"latent_image": ["lat", 0]}, "class_type": "KSampler"},
	  "lat": {"inputs": {"width": ["prim", 0], "height": ["prim", 0]}, "class_type": "EmptyLatentImage"},
	  "prim":{"inputs": {"value": 768}, "class_type": "PrimitiveInt"}
	}`)
	if got := comfySizeNodesFeeding(g, "s"); len(got) != 0 {
		t.Errorf("found %v — a graph-computed size must stay unmapped", got)
	}
}

// TestAPlainGraphIsUnchanged — the single-node case must keep working exactly
// as it did. This began as a fix for a two-node graph and must not disturb the
// ordinary one.
func TestAPlainGraphIsUnchanged(t *testing.T) {
	g := sizeGraph(t, `{
	  "3":{"inputs": {"latent_image": ["5", 0]}, "class_type": "KSampler"},
	  "5":{"inputs": {"width": 512, "height": 512, "batch_size": 1}, "class_type": "EmptyLatentImage"}
	}`)
	got := comfySizeNodesFeeding(g, "3")
	if len(got) != 1 || got[0] != "5" {
		t.Errorf("found %v, want just [5]", got)
	}
}

// TestACycleDoesNotHang — a hand-edited graph can point back at itself, and a
// walk with no visited set would spin forever inside a save.
func TestACycleDoesNotHang(t *testing.T) {
	g := sizeGraph(t, `{
	  "a":{"inputs": {"x": ["b", 0], "width": 64, "height": 64}, "class_type": "A"},
	  "b":{"inputs": {"y": ["a", 0]}, "class_type": "B"}
	}`)
	if got := comfySizeNodesFeeding(g, "a"); len(got) != 1 {
		t.Errorf("found %v, want the one sized node", got)
	}
}

// TestOrderIsStable — the mapping is stored on the backend record and shown to
// an operator; a set that reorders between saves reads as a change nobody made.
func TestOrderIsStable(t *testing.T) {
	g := sizeGraph(t, `{
	  "s":  {"inputs": {"a": ["z", 0], "b": ["m", 0], "c": ["lat", 0]}, "class_type": "KSampler"},
	  "z":  {"inputs": {"width": 8, "height": 8}, "class_type": "Z"},
	  "m":  {"inputs": {"width": 8, "height": 8}, "class_type": "M"},
	  "lat":{"inputs": {"width": 8, "height": 8}, "class_type": "EmptyLatentImage"}
	}`)
	first := comfySizeNodesFeeding(g, "s")
	for i := 0; i < 20; i++ {
		again := comfySizeNodesFeeding(g, "s")
		if len(again) != len(first) {
			t.Fatalf("unstable length: %v vs %v", first, again)
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("order is unstable across calls: %v vs %v — map iteration is leaking out", first, again)
			}
		}
	}
}
