package core

// Auto-wiring for the image-input graphs: img2img, multi-image compose, blend.
// The failure these guard against is specific and nasty — a graph that wires
// "successfully" but drops the caller's photo produces a plausible picture of
// the wrong thing, which reads as a bad model rather than bad config.

import (
	"encoding/json"
	"strings"
	"testing"
)

// parseBody unwraps a built {"prompt": graph} request body. The sibling
// buildGraph helper does this for the text-only path, but these tests need to
// pass images through ComfyBuildInput, so they call BuildComfyBody directly.
func parseBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	g, ok := m["prompt"].(map[string]any)
	if !ok {
		t.Fatalf("body not wrapped in {\"prompt\":...}: %s", body)
	}
	return g
}

const img2imgGraph = `{
  "3":{"class_type":"KSampler","inputs":{"seed":0,"steps":20,"cfg":7,"denoise":0.5,"model":["4",0],"positive":["6",0],"negative":["7",0],"latent_image":["10",0]}},
  "4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"sd15.safetensors"}},
  "6":{"class_type":"CLIPTextEncode","inputs":{"text":"x","clip":["4",1]}},
  "7":{"class_type":"CLIPTextEncode","inputs":{"text":"","clip":["4",1]}},
  "8":{"class_type":"VAEDecode","inputs":{"samples":["3",0],"vae":["4",2]}},
  "9":{"class_type":"SaveImage","inputs":{"filename_prefix":"g","images":["8",0]}},
  "10":{"class_type":"VAEEncode","inputs":{"pixels":["11",0],"vae":["4",2]}},
  "11":{"class_type":"LoadImage","inputs":{"image":"example.png"}}
}`

func TestImg2ImgWiresTheLoadImageNode(t *testing.T) {
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, img2imgGraph, "")
	if err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	if !eqStrs(s.ComfyMap.ImageNodes, []string{"11"}) {
		t.Errorf("image nodes = %v, want [11]", s.ComfyMap.ImageNodes)
	}
	if s.ComfyMap.ImageKey != "image" {
		t.Errorf("image key = %q, want \"image\"", s.ComfyMap.ImageKey)
	}
	// The size warning is for txt2img graphs. An img2img graph takes its size
	// from the source photo — warning here told every edit backend it was broken.
	for _, w := range warns {
		if strings.Contains(w, "EmptyLatentImage") {
			t.Errorf("spurious size warning on an img2img graph: %q", w)
		}
	}
}

func TestComposeGraphKeepsBothImagesInNodeOrder(t *testing.T) {
	// Two LoadImage nodes: caller image[0] must land in the lower node id, so
	// "subject then background" stays "subject then background".
	graph := `{
      "1":{"class_type":"LoadImage","inputs":{"image":"a.png"}},
      "2":{"class_type":"LoadImage","inputs":{"image":"b.png"}},
      "3":{"class_type":"KSampler","inputs":{"seed":0,"steps":20,"denoise":0.6,"positive":["6",0],"negative":["7",0],"latent_image":["10",0]}},
      "6":{"class_type":"CLIPTextEncode","inputs":{"text":"x"}},
      "7":{"class_type":"CLIPTextEncode","inputs":{"text":""}},
      "10":{"class_type":"VAEEncode","inputs":{"pixels":["1",0]}},
      "9":{"class_type":"SaveImage","inputs":{"images":["3",0]}}
    }`
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, graph, ""); err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	if !eqStrs(s.ComfyMap.ImageNodes, []string{"1", "2"}) {
		t.Fatalf("image nodes = %v, want [1 2] in id order", s.ComfyMap.ImageNodes)
	}
	body, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: "p",
		Images: []ComfyUploadedImage{{Name: "first.png"}, {Name: "second.png", Subfolder: "gohort"}},
	})
	if err != nil {
		t.Fatalf("BuildComfyBody: %v", err)
	}
	g := parseBody(t, body)
	if got := nodeInput(t, g, "1", "image"); got != "first.png" {
		t.Errorf("node 1 image = %v, want the caller's FIRST image", got)
	}
	// A subfolder has to be joined on, or the backend looks in the wrong place.
	if got := nodeInput(t, g, "2", "image"); got != "gohort/second.png" {
		t.Errorf("node 2 image = %v, want \"gohort/second.png\"", got)
	}
}

func TestMaskLoaderIsNotTreatedAsASourcePhoto(t *testing.T) {
	// A LoadImageMask in ImageNodes would swallow one of the caller's images.
	graph := `{
      "1":{"class_type":"LoadImage","inputs":{"image":"a.png"}},
      "2":{"class_type":"LoadImageMask","inputs":{"image":"m.png","channel":"red"}},
      "3":{"class_type":"KSampler","inputs":{"seed":0,"denoise":0.5,"positive":["6",0],"negative":["7",0],"latent_image":["10",0]}},
      "6":{"class_type":"CLIPTextEncode","inputs":{"text":"x"}},
      "7":{"class_type":"CLIPTextEncode","inputs":{"text":""}},
      "10":{"class_type":"VAEEncode","inputs":{"pixels":["1",0]}},
      "9":{"class_type":"SaveImage","inputs":{"images":["3",0]}}
    }`
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, graph, ""); err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	if !eqStrs(s.ComfyMap.ImageNodes, []string{"1"}) {
		t.Errorf("image nodes = %v, want only [1]", s.ComfyMap.ImageNodes)
	}
	if !eqStrs(s.ComfyMap.MaskNodes, []string{"2"}) {
		t.Errorf("mask nodes = %v, want [2]", s.ComfyMap.MaskNodes)
	}
}

func TestBlendGraphNeedsNoSamplerOrPrompt(t *testing.T) {
	// A two-photo blend is pure pixel work: no checkpoint, no diffusion, no text
	// anywhere. Requiring a KSampler rejected this working workflow at import.
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, ComfyBlendDefaultGraph(), "")
	if err != nil {
		t.Fatalf("a sampler-less blend graph must wire: %v", err)
	}
	if !eqStrs(s.ComfyMap.ImageNodes, []string{"1", "2"}) {
		t.Errorf("image nodes = %v, want [1 2]", s.ComfyMap.ImageNodes)
	}
	if len(s.ComfyMap.PromptNodes) != 0 {
		t.Errorf("prompt nodes = %v, want none — this graph has no text node", s.ComfyMap.PromptNodes)
	}
	if s.ComfyMap.OutputNode != "9" {
		t.Errorf("output node = %q, want 9", s.ComfyMap.OutputNode)
	}
	// The user is told what they've got, rather than left guessing why there's
	// no prompt field.
	var explained bool
	for _, w := range warns {
		if strings.Contains(w, "no sampler") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("a promptless graph should say so; warnings = %v", warns)
	}
}

func TestBlendGraphValidatesAndRuns(t *testing.T) {
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", ComfyBlendDefaultGraph(), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	// prompt_nodes is empty — Validate must not insist on it for an image graph.
	c := Connector{Name: "blend", Kind: RestImageConnectorKind}
	c.Spec, _ = json.Marshal(spec)
	if err := (restImageHandler{}).Validate(c); err != nil {
		t.Fatalf("a promptless blend backend must validate: %v", err)
	}
	if !spec.SupportsImageInput() {
		t.Error("a blend backend must report image input support")
	}
	if spec.MaxImages() != 2 {
		t.Errorf("MaxImages = %d, want 2", spec.MaxImages())
	}
	body, err := BuildComfyBody(spec.ComfyWorkflow, spec.ComfyMap, ComfyBuildInput{
		Images: []ComfyUploadedImage{{Name: "left.png"}, {Name: "right.png"}},
	})
	if err != nil {
		t.Fatalf("BuildComfyBody: %v", err)
	}
	g := parseBody(t, body)
	if nodeInput(t, g, "1", "image") != "left.png" || nodeInput(t, g, "2", "image") != "right.png" {
		t.Errorf("blend inputs not wired: %s", body)
	}
	// The blend amount stays baked in the graph — no knob, by design.
	if bf := nodeInput(t, g, "3", "blend_factor"); bf == nil {
		t.Error("blend_factor must survive from the workflow")
	}
}

func TestUnmappedImageNodeIsAnErrorNotASilentSkip(t *testing.T) {
	// THE failure mode this whole path is built around: writing to a node that
	// isn't there used to no-op, the graph ran against its placeholder image,
	// and the caller got a confident picture of the wrong thing.
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, img2imgGraph, ""); err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	bad := s.ComfyMap
	bad.ImageNodes = []string{"404"}
	_, err := BuildComfyBody(s.ComfyWorkflow, bad, ComfyBuildInput{
		Prompt: "p",
		Images: []ComfyUploadedImage{{Name: "a.png"}},
	})
	if err == nil {
		t.Fatal("writing to an unmapped image node must fail loudly")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should name the bad node: %v", err)
	}
}

func TestTooManyImagesIsRejected(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, img2imgGraph, ""); err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	_, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: "p",
		Images: []ComfyUploadedImage{{Name: "a.png"}, {Name: "b.png"}},
	})
	if err == nil {
		t.Fatal("more images than nodes must fail rather than drop one")
	}
}

func TestTxt2ImgStillWarnsAboutMissingSize(t *testing.T) {
	// The suppression is scoped to graphs with image input — a genuine txt2img
	// defect must still be reported.
	graph := `{
      "3":{"class_type":"KSampler","inputs":{"seed":0,"positive":["6",0],"negative":["7",0],"latent_image":["10",0]}},
      "6":{"class_type":"CLIPTextEncode","inputs":{"text":"x"}},
      "7":{"class_type":"CLIPTextEncode","inputs":{"text":""}},
      "10":{"class_type":"VAEEncode","inputs":{"pixels":["3",0]}},
      "9":{"class_type":"SaveImage","inputs":{"images":["3",0]}}
    }`
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, graph, "")
	if err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	var warned bool
	for _, w := range warns {
		if strings.Contains(w, "EmptyLatentImage") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a txt2img graph with no size node should still warn; got %v", warns)
	}
}

func TestGraphWithNeitherPromptNorImageIsRejected(t *testing.T) {
	graph := `{"9":{"class_type":"SaveImage","inputs":{"images":["8",0]}}}`
	if _, err := ApplyComfyWorkflow(&RestImageSpec{}, graph, ""); err == nil {
		t.Fatal("a graph with no sampler and no LoadImage has nothing to work from")
	}
}
