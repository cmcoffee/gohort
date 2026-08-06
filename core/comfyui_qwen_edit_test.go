package core

// A real Qwen-Image-Edit workflow, exported from a live ComfyUI. It was
// REJECTED at import — "couldn't trace the sampler's positive conditioning to a
// text node" — and it exercises three things the hand-written fixtures don't:
//
//   - its text encoder names the prompt input `prompt`, not `text`
//   - it drives steps and cfg from switch nodes, so those inputs are LINKS
//   - node ids contain colons, the output node is SaveImageAdvanced, and every
//     node carries a _meta block
//
// Keeping the actual export as the fixture is the point: the failure was in the
// gap between what ComfyUI emits and what the fixtures assumed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func qwenEditGraph(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "qwen_edit_workflow.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestQwenEditWorkflowImports(t *testing.T) {
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, qwenEditGraph(t), "")
	if err != nil {
		t.Fatalf("a real edit workflow must import, got: %v", err)
	}
	m := s.ComfyMap

	// The prompt lives on TextEncodeQwenImageEditPlus under `prompt`. Looking
	// only for `text` is what rejected this graph.
	if !eqStrs(m.PromptNodes, []string{"170:151"}) {
		t.Errorf("prompt nodes = %v, want the positive encoder [170:151]", m.PromptNodes)
	}
	if !eqStrs(m.TextKeys, []string{"prompt"}) {
		t.Errorf("text keys = %v, want [prompt]", m.TextKeys)
	}
	if !eqStrs(m.NegativeNodes, []string{"170:149"}) {
		t.Errorf("negative nodes = %v, want [170:149]", m.NegativeNodes)
	}
	// Both sides reach their encoder through a FluxKontextMultiReferenceLatent
	// hop, so the indirection walk has to survive an unfamiliar node class.
	if !eqStrs(m.ImageNodes, []string{"41"}) {
		t.Errorf("image nodes = %v, want [41]", m.ImageNodes)
	}
	if m.OutputNode != "195" {
		t.Errorf("output node = %q, want 195 (SaveImageAdvanced)", m.OutputNode)
	}
	if !eqStrs(m.SeedNodes, []string{"170:169"}) || m.SeedKey != "seed" {
		t.Errorf("seed = %v/%q, want the KSampler's literal seed", m.SeedNodes, m.SeedKey)
	}
	for _, w := range warns {
		if strings.Contains(w, "no text node") {
			t.Errorf("prompt was not found: %q", w)
		}
	}
}

func TestQwenEditIsAnEditor(t *testing.T) {
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", qwenEditGraph(t), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	if !spec.SupportsImageInput() {
		t.Error("a workflow with a LoadImage and an upload endpoint is an editor")
	}
	if got := ComfyWorkflowTypeOf(spec.ComfyMap); got != ComfyTypeEdit {
		t.Errorf("workflow type = %q, want edit — it takes a photo AND a prompt", got)
	}
	c := Connector{Name: "qwen_edit", Kind: RestImageConnectorKind}
	c.Spec, _ = json.Marshal(spec)
	if err := (restImageHandler{}).Validate(c); err != nil {
		t.Fatalf("a real edit backend must validate: %v", err)
	}
}

func TestLinkedInputsAreNeverOverwritten(t *testing.T) {
	// steps and cfg come from ComfySwitchNode. Writing a plain number over
	// ["170:167",0] severs the switch — ComfyUI then runs a graph the author
	// never built, and it reads as a bad render, not a broken import.
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, qwenEditGraph(t), ""); err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	if len(s.ComfyMap.StepsNodes) != 0 {
		t.Errorf("steps nodes = %v, want none — the graph drives steps from a switch", s.ComfyMap.StepsNodes)
	}

	body, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: "make the hands correct",
		Steps:  99,
		Seed:   123,
		Images: []ComfyUploadedImage{{Name: "photo.png", Subfolder: "gohort"}},
	})
	if err != nil {
		t.Fatalf("BuildComfyBody: %v", err)
	}
	g := parseBody(t, body)

	// The switch wiring survives...
	steps, ok := nodeInput(t, g, "170:169", "steps").([]any)
	if !ok || len(steps) == 0 || steps[0] != "170:167" {
		t.Errorf("steps = %v, want the untouched link to the switch node", nodeInput(t, g, "170:169", "steps"))
	}
	if _, ok := nodeInput(t, g, "170:169", "cfg").([]any); !ok {
		t.Error("cfg link was overwritten")
	}
	// ...while the literals we DO own are written.
	if got := nodeInput(t, g, "170:151", "prompt"); got != "make the hands correct" {
		t.Errorf("prompt = %v, want the caller's text", got)
	}
	if got := nodeInput(t, g, "41", "image"); got != "gohort/photo.png" {
		t.Errorf("image = %v, want the uploaded reference", got)
	}
	if got := nodeInput(t, g, "170:169", "seed"); got == nil {
		t.Error("seed was not written")
	}
}

func TestMetaAndColonIdsSurviveTheRoundTrip(t *testing.T) {
	// _meta is ComfyUI's own bookkeeping and colon ids are how it namespaces
	// subgraph nodes. Both have to come back out byte-for-byte or the graph we
	// submit isn't the graph that was exported.
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, qwenEditGraph(t), ""); err != nil {
		t.Fatalf("ApplyComfyWorkflow: %v", err)
	}
	// An edit graph gets its source photo: a text-only render against one is
	// refused now, because it would draw against the placeholder filename the
	// workflow was exported with.
	body, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: "x",
		Images: []ComfyUploadedImage{{Name: "src.png"}},
	})
	if err != nil {
		t.Fatalf("BuildComfyBody: %v", err)
	}
	g := parseBody(t, body)
	if _, ok := g["170:169"]; !ok {
		t.Error("a colon-namespaced node id was lost")
	}
	node, _ := g["41"].(map[string]any)
	if _, ok := node["_meta"]; !ok {
		t.Error("_meta was dropped from the submitted graph")
	}
	if len(g) != 24 {
		t.Errorf("graph has %d nodes, want all 24 preserved", len(g))
	}
}

func TestMappingOntoADrivenInputIsRejected(t *testing.T) {
	// Filling in steps_nodes for this workflow looks entirely reasonable — the
	// node id is right, the input name is right — but steps comes from a switch
	// node, so the value can never be applied. Silently ignoring it leaves an
	// admin staring at a field they populated correctly that does nothing.
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", qwenEditGraph(t), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	if len(spec.ComfyMap.StepsNodes) != 0 {
		t.Fatalf("auto-wiring should leave steps unmapped, got %v", spec.ComfyMap.StepsNodes)
	}

	spec.ComfyMap.StepsNodes = []string{"170:169"} // the KSampler, by hand
	c := Connector{Name: "qwen_edit", Kind: RestImageConnectorKind}
	c.Spec, _ = json.Marshal(spec)
	err = (restImageHandler{}).Validate(c)
	if err == nil {
		t.Fatal("mapping onto a graph-driven input must be rejected, not silently ignored")
	}
	for _, want := range []string{"steps_nodes", "170:169", "driven by another node", "170:167"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so it's actionable:\n%v", want, err)
		}
	}
}

func TestMappingOntoALiteralInputIsFine(t *testing.T) {
	// The guard must not fire on a normal graph, where every mapped input is a
	// plain value.
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", ComfyStarterGraph(ComfyTypeGenerate), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	c := Connector{Name: "txt", Kind: RestImageConnectorKind}
	c.Spec, _ = json.Marshal(spec)
	if err := (restImageHandler{}).Validate(c); err != nil {
		t.Fatalf("a plain txt2img graph must validate: %v", err)
	}
}

func qwenBlendGraph(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "qwen_blend_workflow.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestQwenBlendWorkflowTakesTwoImages(t *testing.T) {
	// The same workflow with a second LoadImage wired into the encoders'
	// image2. One LoadImage caps the backend at a single source photo, so
	// "combine these two" had nowhere to put the second one.
	spec, warns, err := NewComfyImageSpec("http://localhost:8188", "no_auth", qwenBlendGraph(t), "")
	if err != nil {
		t.Fatalf("the two-image variant must import: %v", err)
	}
	if !eqStrs(spec.ComfyMap.ImageNodes, []string{"41", "42"}) {
		t.Fatalf("image nodes = %v, want [41 42]", spec.ComfyMap.ImageNodes)
	}
	if spec.MaxImages() != 2 {
		t.Errorf("MaxImages = %d, want 2", spec.MaxImages())
	}
	// REVERSED from "edit". This graph takes TWO photos and a prompt, and the
	// prompt used to decide the label — so it read as an edit, and someone who
	// picked "blend" in the form saw their choice apparently revert every time
	// they reopened it. The count is what the form's own wording promises
	// ("blend = combine two photos"), and a composite that can be told HOW to
	// combine is the shape the framework now asks for, so a prompt no longer
	// disqualifies it.
	if got := ComfyWorkflowTypeOf(spec.ComfyMap); got != ComfyTypeBlend {
		t.Errorf("type = %q, want blend — two source photos is a blend, prompt or not", got)
	}
	// The prompt wiring must be unchanged by the addition.
	if !eqStrs(spec.ComfyMap.PromptNodes, []string{"170:151"}) {
		t.Errorf("prompt nodes = %v, want the positive encoder", spec.ComfyMap.PromptNodes)
	}
	for _, w := range warns {
		if strings.Contains(w, "no text node") || strings.Contains(w, "no sampler") {
			t.Errorf("unexpected warning: %q", w)
		}
	}

	c := Connector{Name: "qwen_blend", Kind: RestImageConnectorKind}
	c.Spec, _ = json.Marshal(spec)
	if err := (restImageHandler{}).Validate(c); err != nil {
		t.Fatalf("must validate: %v", err)
	}
}

func TestQwenBlendPlacesCallerImagesInOrder(t *testing.T) {
	// image#1 is the base — it also drives the VAEEncode latent, so it defines
	// the output canvas. image#2 composites onto it. That has to match what the
	// images param promises: "the first is the base/subject".
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", qwenBlendGraph(t), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	body, err := BuildComfyBody(spec.ComfyWorkflow, spec.ComfyMap, ComfyBuildInput{
		Prompt: "a clown terminator",
		Images: []ComfyUploadedImage{
			{Name: "terminator.png", Subfolder: "gohort"},
			{Name: "clown.png", Subfolder: "gohort"},
		},
	})
	if err != nil {
		t.Fatalf("BuildComfyBody: %v", err)
	}
	g := parseBody(t, body)
	if got := nodeInput(t, g, "41", "image"); got != "gohort/terminator.png" {
		t.Errorf("node 41 = %v, want the caller's FIRST image", got)
	}
	if got := nodeInput(t, g, "42", "image"); got != "gohort/clown.png" {
		t.Errorf("node 42 = %v, want the caller's SECOND image", got)
	}
	// The base image is what the latent is encoded from — swapping that would
	// silently change which picture defines the canvas.
	pixels, ok := nodeInput(t, g, "170:156", "pixels").([]any)
	if !ok || pixels[0] != "170:160" {
		t.Errorf("VAEEncode pixels = %v, want the FIRST image's scale node", nodeInput(t, g, "170:156", "pixels"))
	}
}

func TestQwenBlendConditionsBothSidesOnBothImages(t *testing.T) {
	// Positive and negative conditioning must reference the SAME image set. If
	// only the positive encoder gets image2, the negative side is conditioned
	// on a different picture and the guidance fights itself.
	g, err := parseComfyGraph(qwenBlendGraph(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, node := range []string{"170:149", "170:151"} {
		in := comfyInputs(g, node)
		for _, key := range []string{"image1", "image2"} {
			if _, ok := in[key]; !ok {
				t.Errorf("encoder %s is missing %s", node, key)
			}
		}
	}
}
