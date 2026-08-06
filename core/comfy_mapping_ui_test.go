// Mapping a workflow by hand, when auto-wiring cannot read it.
//
// Every ComfyUI import failure so far has been a DETECTION failure against a
// correctly wired graph — a sampler behind a selector, conditioning behind a
// guider — and each new architecture will do it again, because detection
// encodes what architectures looked like when it was written. Returning an
// error filled nothing: the admin was told their workflow was wrong and left
// with an empty form and a JSON file to read.
package core

import (
	"strings"
	"testing"
)

func TestGraphNodesAreLabelledForAHuman(t *testing.T) {
	nodes, err := ComfyGraphNodes(flux2Graph(t))
	if err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("a real workflow should offer candidates")
	}
	byValue := map[string]string{}
	for _, n := range nodes {
		byValue[n.Value] = n.Label
	}
	// The author's own title is the most recognizable part, so it has to be
	// there — "98:12" alone is what made these fields unusable.
	if got := byValue["98:12"]; !strings.Contains(got, "Load Diffusion Model") || !strings.Contains(got, "UNETLoader") {
		t.Errorf("label for 98:12 = %q, want the title and the class", got)
	}
	if got := byValue["98:6"]; !strings.Contains(got, "CLIP Text Encode") {
		t.Errorf("label for 98:6 = %q, want its title", got)
	}
	if !strings.HasPrefix(byValue["9"], "9 ") && byValue["9"] != "9" {
		t.Errorf("label should lead with the id that goes in the field, got %q", byValue["9"])
	}
}

// Stable ordering: a list that reshuffles between openings makes the form look
// like it changed when nothing did.
func TestGraphNodesAreStablyOrdered(t *testing.T) {
	first, err := ComfyGraphNodes(flux2Graph(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := ComfyGraphNodes(flux2Graph(t))
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if first[j].Value != again[j].Value {
				t.Fatalf("node order changed between calls at %d: %q vs %q", j, first[j].Value, again[j].Value)
			}
		}
	}
}

// An unreadable graph has nothing to offer, and must say so rather than
// pretending to a candidate list.
func TestGraphNodesRejectsUnparseableJSON(t *testing.T) {
	if _, err := ComfyGraphNodes("{not json"); err == nil {
		t.Error("unparseable JSON should error, not return an empty list")
	}
}

// The fallback fills what can be read WITHOUT understanding the wiring.
func TestPartialMapReadsWhatItCan(t *testing.T) {
	m := comfyPartialMap(flux2Graph(t))
	if m.OutputNode != "9" {
		t.Errorf("output node = %q, want 9 — a SaveImage is identifiable without tracing anything", m.OutputNode)
	}
	if !eqStrs(m.PromptNodes, []string{"98:6"}) {
		t.Errorf("prompt nodes = %v, want the node holding literal text", m.PromptNodes)
	}
	if len(m.ImageNodes) != 0 {
		t.Errorf("a txt2img graph has no LoadImage, got %v", m.ImageNodes)
	}
}

// Every node-mapping field must offer the graph's nodes. A field left out is
// the one someone has to type an id into from memory.
func TestEveryNodeFieldOffersSuggestions(t *testing.T) {
	tpl := comfyuiTemplate()
	for _, f := range tpl.Fields {
		if !strings.HasSuffix(f.Key, "_nodes") && f.Key != "output_node" {
			continue
		}
		if f.SuggestFrom == "" {
			t.Errorf("field %q maps a node but offers no candidates", f.Key)
		}
	}
}
