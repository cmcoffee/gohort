package admin

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestComfyCfgRoundTrip(t *testing.T) {
	spec := RestImageSpec{
		SubmitURL:     "http://localhost:8188/prompt",
		ComfyWorkflow: `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":""}}}`,
		DefaultWidth:  768,
		DefaultHeight: 512,
		DefaultSteps:  25,
		PromptSuffix:  "crisp, high-contrast",
		ComfyMap: ComfyNodeMap{
			PromptNodes:   []string{"6"},
			NegativeNodes: []string{"7"},
			TextKeys:      []string{"text_g", "text_l"},
			WidthNodes:    []string{"5"},
			HeightNodes:   []string{"5"},
			StepsNodes:    []string{"3"},
			SeedNodes:     []string{"3"},
			SeedKey:       "noise_seed",
			OutputNode:    "9",
		},
	}

	cfg := comfyCfgFromSpec("comfyui", spec)
	if cfg.BaseURL != "http://localhost:8188" {
		t.Errorf("base_url = %q (should strip /prompt)", cfg.BaseURL)
	}
	if cfg.PromptNodes != "6" || cfg.NegativeNodes != "7" || cfg.TextKeys != "text_g, text_l" {
		t.Errorf("map projection wrong: %+v", cfg)
	}
	if cfg.SeedKey != "noise_seed" || cfg.OutputNode != "9" {
		t.Errorf("seed/output wrong: %+v", cfg)
	}
	if cfg.PromptSuffix != "crisp, high-contrast" {
		t.Errorf("suffix lost: %q", cfg.PromptSuffix)
	}

	// The comma-string form parses back to the same node lists.
	m := comfyMapFromCfg(cfg)
	if !eqStrs(m.PromptNodes, []string{"6"}) || !eqStrs(m.TextKeys, []string{"text_g", "text_l"}) {
		t.Errorf("round-trip lost nodes: %+v", m)
	}
	if m.SeedKey != "noise_seed" || m.OutputNode != "9" {
		t.Errorf("round-trip lost seed/output: %+v", m)
	}
}

func TestSplitNodes(t *testing.T) {
	got := splitNodes(" 6 , 7 ,, 8 ")
	if !eqStrs(got, []string{"6", "7", "8"}) {
		t.Errorf("splitNodes trims/drops empties wrong: %v", got)
	}
	if got := splitNodes(""); len(got) != 0 {
		t.Errorf("empty → nil, got %v", got)
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
