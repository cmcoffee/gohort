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
	body, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{Prompt: "x"})
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
