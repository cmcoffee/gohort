package admin

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestNestAndStringifyComfyWorkflow(t *testing.T) {
	// A spec whose comfy_workflow is a string holding pretty JSON.
	spec, _ := json.Marshal(RestImageSpec{
		SubmitURL:     "http://x/prompt",
		ComfyWorkflow: "{\n  \"6\": {\n    \"class_type\": \"CLIPTextEncode\"\n  }\n}",
	})

	// Edit-spec view: comfy_workflow becomes a NESTED object, not an escaped string.
	nested, ok := nestComfyWorkflow(spec)
	if !ok {
		t.Fatal("expected nesting to apply")
	}
	if strings.Contains(string(nested), `\"6\"`) || strings.Contains(string(nested), `\n`) {
		t.Errorf("workflow still escaped in Edit-spec view:\n%s", nested)
	}
	if !strings.Contains(string(nested), "\"comfy_workflow\": {") {
		t.Errorf("comfy_workflow not rendered as a nested object:\n%s", nested)
	}

	// Save path: an edited nested object folds back to a string, and the workflow
	// content survives the round-trip.
	backToString := stringifyComfyWorkflow(nested)
	var out RestImageSpec
	if err := json.Unmarshal(backToString, &out); err != nil {
		t.Fatalf("stringified spec invalid: %v\n%s", err, backToString)
	}
	if !strings.Contains(out.ComfyWorkflow, "CLIPTextEncode") {
		t.Errorf("workflow content lost on round-trip: %q", out.ComfyWorkflow)
	}
	if !strings.Contains(out.ComfyWorkflow, "\n") {
		t.Errorf("workflow should be pretty (multi-line) after round-trip: %q", out.ComfyWorkflow)
	}

	// A non-image spec (no comfy_workflow) is passed through untouched.
	if _, ok := nestComfyWorkflow([]byte(`{"credential":"no_auth"}`)); ok {
		t.Error("nesting should not apply to a spec without comfy_workflow")
	}
}

func TestComfyCfgRoundTrip(t *testing.T) {
	spec := RestImageSpec{
		SubmitURL:     "http://localhost:8188/prompt",
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
