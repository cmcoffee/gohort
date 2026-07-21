package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// a standard SD1.5 txt2img API-format graph (SaveImage node "9", KSampler "3").
const comfyGraphSD15 = `{
  "3": {"class_type":"KSampler","inputs":{"seed":156680208700286,"steps":20,"cfg":8,"sampler_name":"euler","scheduler":"normal","denoise":1,"model":["4",0],"positive":["6",0],"negative":["7",0],"latent_image":["5",0]}},
  "4": {"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"v1-5-pruned-emaonly.safetensors"}},
  "5": {"class_type":"EmptyLatentImage","inputs":{"width":512,"height":512,"batch_size":1}},
  "6": {"class_type":"CLIPTextEncode","inputs":{"text":"a photo of a cat","clip":["4",1]}},
  "7": {"class_type":"CLIPTextEncode","inputs":{"text":"blurry, low quality","clip":["4",1]}},
  "8": {"class_type":"VAEDecode","inputs":{"samples":["3",0],"vae":["4",2]}},
  "9": {"class_type":"SaveImage","inputs":{"filename_prefix":"ComfyUI","images":["8",0]}}
}`

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

// buildGraph runs BuildComfyBody for a wired spec and returns the parsed graph.
func buildGraph(t *testing.T, s RestImageSpec, prompt, neg string, w, h, steps, seed int) map[string]any {
	t.Helper()
	body, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, prompt, neg, w, h, steps, seed)
	if err != nil {
		t.Fatalf("BuildComfyBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("built body is not valid JSON: %v\n%s", err, body)
	}
	g, ok := m["prompt"].(map[string]any)
	if !ok {
		t.Fatalf("body not wrapped {\"prompt\":...}: %s", body)
	}
	return g
}

func nodeInput(t *testing.T, g map[string]any, node, key string) any {
	t.Helper()
	n, ok := g[node].(map[string]any)
	if !ok {
		t.Fatalf("node %q missing", node)
	}
	in, ok := n["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("node %q has no inputs", node)
	}
	return in[key]
}

func TestApplyComfyWorkflowStandard(t *testing.T) {
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, comfyGraphSD15, "")
	if err != nil {
		t.Fatalf("wiring failed: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	m := s.ComfyMap
	if !eqStrs(m.PromptNodes, []string{"6"}) {
		t.Errorf("prompt nodes = %v, want [6]", m.PromptNodes)
	}
	if !eqStrs(m.NegativeNodes, []string{"7"}) {
		t.Errorf("negative nodes = %v, want [7]", m.NegativeNodes)
	}
	if !eqStrs(m.TextKeys, []string{"text"}) {
		t.Errorf("text keys = %v, want [text]", m.TextKeys)
	}
	if !eqStrs(m.WidthNodes, []string{"5"}) || !eqStrs(m.HeightNodes, []string{"5"}) {
		t.Errorf("size nodes = %v / %v, want [5] / [5]", m.WidthNodes, m.HeightNodes)
	}
	if !eqStrs(m.SeedNodes, []string{"3"}) || m.SeedKey != "seed" {
		t.Errorf("seed = %v key %q", m.SeedNodes, m.SeedKey)
	}
	if !eqStrs(m.StepsNodes, []string{"3"}) {
		t.Errorf("steps nodes = %v, want [3]", m.StepsNodes)
	}
	if m.OutputNode != "9" {
		t.Errorf("output node = %q, want 9", m.OutputNode)
	}
	if s.DefaultWidth != 512 || s.DefaultHeight != 512 {
		t.Errorf("size defaults = %dx%d, want 512x512", s.DefaultWidth, s.DefaultHeight)
	}
	if s.ComfyWorkflow == "" {
		t.Error("raw workflow not stored")
	}
	// Stored pretty-indented, not collapsed to one line, and still parses to the
	// same nodes (content preserved).
	if !strings.Contains(s.ComfyWorkflow, "\n") {
		t.Errorf("workflow should be pretty-indented, got one line: %s", s.ComfyWorkflow)
	}
	if !strings.Contains(s.ComfyWorkflow, "\"class_type\": \"KSampler\"") &&
		!strings.Contains(s.ComfyWorkflow, "\"class_type\":\"KSampler\"") {
		t.Errorf("workflow content not preserved: %s", s.ComfyWorkflow)
	}
	// Legacy token fields are cleared — the mapping model owns body + poll paths.
	if s.SubmitBody != "" || s.PollReadyPath != "" {
		t.Errorf("legacy fields not cleared: body=%q ready=%q", s.SubmitBody, s.PollReadyPath)
	}
}

func TestBuildComfyBodyInjects(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, comfyGraphSD15, ""); err != nil {
		t.Fatal(err)
	}
	g := buildGraph(t, s, "a red fox", "blurry", 768, 512, 25, 12345)
	if got := nodeInput(t, g, "6", "text"); got != "a red fox" {
		t.Errorf("prompt not injected: %v", got)
	}
	if got := nodeInput(t, g, "7", "text"); got != "blurry" {
		t.Errorf("negative not injected: %v", got)
	}
	// JSON numbers round-trip back as float64.
	if got := nodeInput(t, g, "5", "width"); got != float64(768) {
		t.Errorf("width = %v, want 768", got)
	}
	if got := nodeInput(t, g, "5", "height"); got != float64(512) {
		t.Errorf("height = %v, want 512", got)
	}
	if got := nodeInput(t, g, "3", "seed"); got != float64(12345) {
		t.Errorf("seed = %v, want 12345", got)
	}
	if got := nodeInput(t, g, "3", "steps"); got != float64(25) {
		t.Errorf("steps = %v, want 25", got)
	}
	// A quote/newline in the prompt can't break the graph JSON.
	g2 := buildGraph(t, s, "a \"cat\"\nwith text", "", 512, 512, 20, 1)
	if got := nodeInput(t, g2, "6", "text"); got != "a \"cat\"\nwith text" {
		t.Errorf("escaped prompt round-trip: %v", got)
	}
}

func TestApplyComfyWorkflowAlreadyWrapped(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, `{"prompt":`+comfyGraphSD15+`}`, ""); err != nil {
		t.Fatal(err)
	}
	// A wrapped input is preserved as-pasted, but BuildComfyBody still produces a
	// SINGLE {"prompt": {graph}} body (not double-wrapped) with the prompt injected.
	g := buildGraph(t, s, "a red fox", "", 512, 512, 20, 1)
	if _, doubled := g["prompt"]; doubled {
		t.Error("double-wrapped: body is {\"prompt\":{\"prompt\":...}}")
	}
	if got := nodeInput(t, g, "6", "text"); got != "a red fox" {
		t.Errorf("prompt not injected from wrapped workflow: %v", got)
	}
}

func TestApplyComfyWorkflowNodeOverride(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, comfyGraphSD15, "8"); err != nil {
		t.Fatal(err)
	}
	if s.ComfyMap.OutputNode != "8" {
		t.Errorf("override ignored: %q", s.ComfyMap.OutputNode)
	}
	if _, err := ApplyComfyWorkflow(&RestImageSpec{}, comfyGraphSD15, "999"); err == nil {
		t.Error("expected error for unknown override node id")
	}
}

func TestApplyComfyWorkflowNoSaveImage(t *testing.T) {
	graph := `{"3":{"class_type":"KSampler","inputs":{"seed":1,"positive":["6",0],"negative":["7",0]}},"6":{"class_type":"CLIPTextEncode","inputs":{"text":"x"}},"7":{"class_type":"CLIPTextEncode","inputs":{"text":"y"}}}`
	if _, err := ApplyComfyWorkflow(&RestImageSpec{}, graph, ""); err == nil || !strings.Contains(err.Error(), "SaveImage") {
		t.Errorf("expected SaveImage error, got %v", err)
	}
}

func TestApplyComfyWorkflowSDXLDualEncoder(t *testing.T) {
	graph := `{
	  "3":{"class_type":"KSampler","inputs":{"noise_seed":7,"positive":["6",0],"negative":["7",0]}},
	  "6":{"class_type":"CLIPTextEncodeSDXL","inputs":{"text_g":"a castle","text_l":"a castle","clip":["4",1]}},
	  "7":{"class_type":"CLIPTextEncodeSDXL","inputs":{"text_g":"ugly","text_l":"ugly","clip":["4",1]}},
	  "9":{"class_type":"SaveImage","inputs":{"images":["8",0]}}
	}`
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, graph, ""); err != nil {
		t.Fatalf("SDXL wiring failed: %v", err)
	}
	if !eqStrs(s.ComfyMap.TextKeys, []string{"text_g", "text_l"}) {
		t.Errorf("SDXL text keys = %v", s.ComfyMap.TextKeys)
	}
	if s.ComfyMap.SeedKey != "noise_seed" {
		t.Errorf("seed key = %q, want noise_seed", s.ComfyMap.SeedKey)
	}
	g := buildGraph(t, s, "a dragon", "blurry", 1024, 1024, 20, 42)
	if nodeInput(t, g, "6", "text_g") != "a dragon" || nodeInput(t, g, "6", "text_l") != "a dragon" {
		t.Errorf("SDXL dual-encoder prompt not injected")
	}
	if got := nodeInput(t, g, "3", "noise_seed"); got != float64(42) {
		t.Errorf("noise_seed = %v, want 42", got)
	}
}

func TestApplyComfyWorkflowIndirectConditioning(t *testing.T) {
	// Positive routes through a ControlNetApply before reaching the text node.
	graph := `{
	  "3":{"class_type":"KSampler","inputs":{"seed":1,"positive":["10",0],"negative":["7",0]}},
	  "10":{"class_type":"ControlNetApply","inputs":{"conditioning":["6",0],"control_net":["11",0],"image":["12",0]}},
	  "6":{"class_type":"CLIPTextEncode","inputs":{"text":"a dog"}},
	  "7":{"class_type":"CLIPTextEncode","inputs":{"text":"bad"}},
	  "9":{"class_type":"SaveImage","inputs":{"images":["8",0]}}
	}`
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, graph, ""); err != nil {
		t.Fatalf("indirect conditioning failed: %v", err)
	}
	if !eqStrs(s.ComfyMap.PromptNodes, []string{"6"}) {
		t.Errorf("prompt node via ControlNetApply = %v, want [6]", s.ComfyMap.PromptNodes)
	}
}

func TestPrettyComfyJSON(t *testing.T) {
	// Compact input becomes pretty (multi-line, indented).
	compact := `{"3":{"class_type":"KSampler","inputs":{"seed":1}}}`
	out := PrettyComfyJSON(compact)
	if !strings.Contains(out, "\n") || !strings.Contains(out, "  ") {
		t.Errorf("compact not pretty-printed: %q", out)
	}
	// Already-pretty input stays valid + parses to the same value (idempotent-ish).
	again := PrettyComfyJSON(out)
	if again != out {
		t.Errorf("pretty not stable: %q vs %q", again, out)
	}
	// Invalid JSON is returned trimmed, not mangled.
	if got := PrettyComfyJSON("  not json  "); got != "not json" {
		t.Errorf("invalid input = %q", got)
	}
}

func TestApplyComfyWorkflowInvalid(t *testing.T) {
	if _, err := ApplyComfyWorkflow(&RestImageSpec{}, "not json", ""); err == nil {
		t.Error("expected error for non-JSON workflow")
	}
	if _, err := ApplyComfyWorkflow(&RestImageSpec{}, "", ""); err == nil {
		t.Error("expected error for empty workflow")
	}
}
