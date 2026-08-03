package core

// Which photo is "the first one". The caller's first image goes to
// ImageNodes[0], so that list's order decides what "the base/subject" means —
// and ComfyUI numbers nodes by creation, not by role.

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseGraph(t *testing.T, raw string) map[string]map[string]any {
	t.Helper()
	var g map[string]map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&g); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return g
}

func TestImageNodesFollowTheEncoderNotTheNodeIds(t *testing.T) {
	// Node "2" feeds image1 and node "1" feeds image2 — the reverse of id
	// order. Before this, the caller's FIRST photo went to node "1", which the
	// graph treats as the second source: a blend composited backwards, with no
	// error and a plausible-looking result.
	g := parseGraph(t, `{
	  "1": {"class_type": "LoadImage", "inputs": {"image": "b.png"}},
	  "2": {"class_type": "LoadImage", "inputs": {"image": "a.png"}},
	  "3": {"class_type": "TextEncodeQwenImageEditPlus", "inputs": {
	          "image1": ["2", 0], "image2": ["1", 0], "prompt": "blend them"}}
	}`)
	got := orderImageNodesByConsumer(g, []string{"1", "2"})
	if len(got) != 2 || got[0] != "2" || got[1] != "1" {
		t.Errorf("order = %v, want [2 1] — the loader feeding image1 comes first", got)
	}
}

func TestThreeSourcesOrderByTheirSlots(t *testing.T) {
	g := parseGraph(t, `{
	  "10": {"class_type": "LoadImage", "inputs": {"image": "c.png"}},
	  "11": {"class_type": "LoadImage", "inputs": {"image": "a.png"}},
	  "12": {"class_type": "LoadImage", "inputs": {"image": "b.png"}},
	  "20": {"class_type": "TextEncodeQwenImageEditPlus", "inputs": {
	          "image1": ["11", 0], "image2": ["12", 0], "image3": ["10", 0]}}
	}`)
	got := orderImageNodesByConsumer(g, []string{"10", "11", "12"})
	want := []string{"11", "12", "10"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestAGraphThatExpressesNoOrderIsLeftAlone(t *testing.T) {
	// Plain "image"/"images" inputs say nothing about which source is first;
	// inventing an order from them would be worse than keeping id order.
	g := parseGraph(t, `{
	  "1": {"class_type": "LoadImage", "inputs": {"image": "a.png"}},
	  "2": {"class_type": "LoadImage", "inputs": {"image": "b.png"}},
	  "3": {"class_type": "PreviewImage", "inputs": {"images": ["1", 0]}},
	  "4": {"class_type": "VAEEncode", "inputs": {"pixels": ["2", 0]}}
	}`)
	got := orderImageNodesByConsumer(g, []string{"1", "2"})
	if got[0] != "1" || got[1] != "2" {
		t.Errorf("order = %v, want the id order preserved", got)
	}
	// One loader needs no ordering at all.
	if got := orderImageNodesByConsumer(g, []string{"1"}); len(got) != 1 || got[0] != "1" {
		t.Errorf("single loader = %v", got)
	}
}

func TestAnUnwiredLoaderSortsAfterTheWiredOnes(t *testing.T) {
	// A spare loader nothing numbered references must not displace a real
	// source — it goes last, where an extra image would be ignored anyway.
	g := parseGraph(t, `{
	  "1": {"class_type": "LoadImage", "inputs": {"image": "spare.png"}},
	  "5": {"class_type": "LoadImage", "inputs": {"image": "real.png"}},
	  "9": {"class_type": "ImageBlend", "inputs": {"image1": ["5", 0]}}
	}`)
	got := orderImageNodesByConsumer(g, []string{"1", "5"})
	if got[0] != "5" {
		t.Errorf("order = %v, want the wired loader first", got)
	}
}

func TestNumberedImageInputsOnly(t *testing.T) {
	for _, c := range []struct {
		key  string
		n    int
		want bool
	}{
		{"image1", 1, true}, {"image2", 2, true}, {"image10", 10, true},
		{"image", 0, false}, {"images", 0, false}, {"image0", 0, false},
		{"mask1", 0, false}, {"", 0, false},
	} {
		n, ok := numberedImageInput(c.key)
		if ok != c.want || (ok && n != c.n) {
			t.Errorf("%q → (%d,%v), want (%d,%v)", c.key, n, ok, c.n, c.want)
		}
	}
}

func TestOrderingFollowsThroughIntermediateNodes(t *testing.T) {
	// The shape a real Qwen edit graph actually has: each source runs through a
	// scaler before the encoder, so image1 names the SCALER and the scaler names
	// the loader. Ranking on direct links alone found nothing here and fell
	// back to node id — in precisely the workflows this ordering exists for.
	g := parseGraph(t, `{
	  "41": {"class_type": "LoadImage", "inputs": {"image": "second.png"}},
	  "42": {"class_type": "LoadImage", "inputs": {"image": "first.png"}},
	  "160": {"class_type": "FluxKontextImageScale", "inputs": {"image": ["42", 0]}},
	  "170": {"class_type": "FluxKontextImageScale", "inputs": {"image": ["41", 0]}},
	  "149": {"class_type": "TextEncodeQwenImageEditPlus", "inputs": {
	            "image1": ["160", 0], "image2": ["170", 0]}}
	}`)
	got := orderImageNodesByConsumer(g, []string{"41", "42"})
	if got[0] != "42" || got[1] != "41" {
		t.Errorf("order = %v, want [42 41] — 42 reaches image1 through the scaler", got)
	}
}

func TestTheRealBlendWorkflowOrdersByItsEncoder(t *testing.T) {
	// The user's own two-LoadImage export. Its encoder reads image1 from the
	// scaler fed by 41 and image2 from the scaler fed by 42, so id order and
	// consumer order agree here — the assertion is that the walk RESOLVES them
	// rather than falling through to the id fallback.
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", qwenBlendGraph(t), "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	nodes := spec.ComfyMap.ImageNodes
	if len(nodes) != 2 {
		t.Fatalf("image nodes = %v, want two", nodes)
	}
	if nodes[0] != "41" || nodes[1] != "42" {
		t.Errorf("image nodes = %v, want [41 42] in encoder order", nodes)
	}
}
