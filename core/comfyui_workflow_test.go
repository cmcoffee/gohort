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

// mustBody substitutes the numeric {seed} token (unquoted in the template, so
// the raw body isn't valid JSON until filled — same as the a1111 preset's
// {width}) and parses the result. String tokens like {prompt} stay as literals
// inside their quotes and survive the parse unchanged.
func mustBody(t *testing.T, s RestImageSpec) map[string]any {
	t.Helper()
	filled := s.SubmitBody
	for _, tok := range []string{"{seed}", "{width}", "{height}", "{steps}"} {
		filled = strings.ReplaceAll(filled, tok, "0")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(filled), &m); err != nil {
		t.Fatalf("SubmitBody is not valid JSON after wiring: %v\n%s", err, s.SubmitBody)
	}
	return m
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
	// Body must be valid JSON wrapped as {"prompt": {graph}}.
	m := mustBody(t, s)
	graph, ok := m["prompt"].(map[string]any)
	if !ok {
		t.Fatalf("body not wrapped as {\"prompt\":...}: %s", s.SubmitBody)
	}
	// Positive text node "6" → {prompt}; negative "7" → {negative}.
	if txt := graph["6"].(map[string]any)["inputs"].(map[string]any)["text"]; txt != "{prompt}" {
		t.Errorf("positive text not tokenized: %v", txt)
	}
	if txt := graph["7"].(map[string]any)["inputs"].(map[string]any)["text"]; txt != "{negative}" {
		t.Errorf("negative text not tokenized: %v", txt)
	}
	// Poll paths point at the SaveImage node "9".
	if s.PollReadyPath != "{id}.outputs.9.images.0.filename" {
		t.Errorf("poll_ready_path = %q", s.PollReadyPath)
	}
	if s.PollFields["filename"] != "{id}.outputs.9.images.0.filename" {
		t.Errorf("poll_fields = %v", s.PollFields)
	}
	// EmptyLatentImage "5" width/height tokenized (unquoted number position) and
	// the graph's own size captured as the spec defaults.
	if !strings.Contains(s.SubmitBody, `"width":{width}`) || !strings.Contains(s.SubmitBody, `"height":{height}`) {
		t.Errorf("size not tokenized: %s", s.SubmitBody)
	}
	if s.DefaultWidth != 512 || s.DefaultHeight != 512 {
		t.Errorf("size defaults not captured from graph: %dx%d", s.DefaultWidth, s.DefaultHeight)
	}
}

func TestNormImageDim(t *testing.T) {
	cases := map[int]int{0: 512, -5: 512, 1: 64, 100: 104, 512: 512, 1000: 1000, 1023: 1024, 767: 768}
	for in, want := range cases {
		if got := normImageDim(in); got != want {
			t.Errorf("normImageDim(%d) = %d, want %d", in, got, want)
		}
	}
	// Every result is a valid SD dimension (multiple of 8, >= 64).
	for _, in := range []int{64, 333, 900, 2049} {
		if got := normImageDim(in); got%8 != 0 || got < 64 {
			t.Errorf("normImageDim(%d) = %d is not a valid SD dim", in, got)
		}
	}
}

func TestApplyComfyWorkflowSeedTokenizedUnquoted(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, comfyGraphSD15, ""); err != nil {
		t.Fatal(err)
	}
	// {seed} must land in a NUMBER position (unquoted), so a numeric seed
	// substitutes cleanly. It reads `"seed":{seed}` in the template.
	if !strings.Contains(s.SubmitBody, `"seed":{seed}`) {
		t.Errorf("seed not tokenized unquoted; body: %s", s.SubmitBody)
	}
	if strings.Contains(s.SubmitBody, `"{seed}"`) {
		t.Errorf("seed token is quoted (would send a string): %s", s.SubmitBody)
	}
	// After substituting the tokens the body is valid JSON with a numeric seed.
	final := strings.ReplaceAll(s.SubmitBody, "{seed}", "42")
	final = strings.ReplaceAll(final, "{width}", "512")
	final = strings.ReplaceAll(final, "{height}", "512")
	final = strings.ReplaceAll(final, "{prompt}", "x")
	final = strings.ReplaceAll(final, "{negative}", "y")
	var m map[string]any
	if err := json.Unmarshal([]byte(final), &m); err != nil {
		t.Fatalf("substituted body invalid: %v\n%s", err, final)
	}
}

func TestApplyComfyWorkflowAlreadyWrapped(t *testing.T) {
	// A body already shaped as {"prompt": {graph}} must not be double-wrapped.
	var s RestImageSpec
	wrapped := `{"prompt":` + comfyGraphSD15 + `}`
	if _, err := ApplyComfyWorkflow(&s, wrapped, ""); err != nil {
		t.Fatal(err)
	}
	m := mustBody(t, s)
	inner, ok := m["prompt"].(map[string]any)
	if !ok {
		t.Fatalf("not wrapped: %s", s.SubmitBody)
	}
	if _, doubled := inner["prompt"]; doubled {
		t.Errorf("double-wrapped {\"prompt\":{\"prompt\":...}}")
	}
}

func TestApplyComfyWorkflowNodeOverride(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, comfyGraphSD15, "8"); err != nil {
		t.Fatal(err)
	}
	if s.PollReadyPath != "{id}.outputs.8.images.0.filename" {
		t.Errorf("override ignored: %q", s.PollReadyPath)
	}
	// A bad override id is a hard error.
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
	// SDXL-style positive node carries text_g/text_l instead of text.
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
	m := mustBody(t, s)
	g := m["prompt"].(map[string]any)
	posIn := g["6"].(map[string]any)["inputs"].(map[string]any)
	if posIn["text_g"] != "{prompt}" || posIn["text_l"] != "{prompt}" {
		t.Errorf("SDXL dual-encoder not tokenized: %v", posIn)
	}
	// noise_seed (KSamplerAdvanced-style) tokenized.
	if !strings.Contains(s.SubmitBody, `"noise_seed":{seed}`) {
		t.Errorf("noise_seed not tokenized: %s", s.SubmitBody)
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
	m := mustBody(t, s)
	g := m["prompt"].(map[string]any)
	if g["6"].(map[string]any)["inputs"].(map[string]any)["text"] != "{prompt}" {
		t.Errorf("prompt not traced through ControlNetApply")
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
