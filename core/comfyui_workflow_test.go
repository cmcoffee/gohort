package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An underfilled compose graph must not render somebody else's photo.
//
// Only as many image nodes are written as there are images, so a three-input
// blend given one picture ran its other two against the filenames the workflow
// was SAVED with. The unfilled inputs are now taken out of the graph.
// threeInputGraph is a minimal compose workflow: three LoadImage nodes, each
// with the placeholder filename an export would bake in.
const threeInputGraph = `{
  "1": {"class_type": "LoadImage", "inputs": {"image": "stale-one.png"}},
  "2": {"class_type": "LoadImage", "inputs": {"image": "stale-two.png"}},
  "3": {"class_type": "LoadImage", "inputs": {"image": "stale-three.png"}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["1", 0]}}
}`

func threeInputMap() ComfyNodeMap {
	return ComfyNodeMap{ImageNodes: []string{"1", "2", "3"}, ImageKey: "image", OutputNode: "9"}
}

func uploaded(names ...string) []ComfyUploadedImage {
	out := make([]ComfyUploadedImage, 0, len(names))
	for _, n := range names {
		out = append(out, ComfyUploadedImage{Name: n})
	}
	return out
}

func TestPartialFillTakesTheUnusedInputsOut(t *testing.T) {
	body, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "blend these", Images: uploaded("mine.png"),
	})
	if err != nil {
		t.Fatalf("one image into a three-input graph should render with the other inputs removed: %v", err)
	}
	if strings.Contains(body, "stale-two.png") || strings.Contains(body, "stale-three.png") || !strings.Contains(body, "mine.png") {
		t.Errorf("an unfilled input stayed in the graph on its placeholder:\n%s", body)
	}
}

// The shape these workflows actually have: each loader feeds a resize, and
// the resizes feed one edit node's image1..image3. Two pictures in a
// three-input graph take the third loader, its resize, and the edit node's
// image3 out, and leave the rest of the edit node as it was.
func TestTwoPicturesInAThreeInputEditGraph(t *testing.T) {
	const graph = `{
  "1": {"class_type": "LoadImage", "inputs": {"image": "stale-one.png"}},
  "2": {"class_type": "LoadImage", "inputs": {"image": "stale-two.png"}},
  "3": {"class_type": "LoadImage", "inputs": {"image": "stale-three.png"}},
  "11": {"class_type": "ImageScaleToTotalPixels", "inputs": {"image": ["1", 0], "megapixels": 1}},
  "12": {"class_type": "ImageScaleToTotalPixels", "inputs": {"image": ["2", 0], "megapixels": 1}},
  "13": {"class_type": "ImageScaleToTotalPixels", "inputs": {"image": ["3", 0], "megapixels": 1}},
  "20": {"class_type": "CLIPLoader", "inputs": {"clip_name": "qwen.safetensors"}},
  "21": {"class_type": "TextEncodeQwenImageEditPlus", "inputs": {"clip": ["20", 0], "prompt": "x", "image1": ["11", 0], "image2": ["12", 0], "image3": ["13", 0]}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["21", 0]}}
}`
	body, err := BuildComfyBody(graph, threeInputMap(), ComfyBuildInput{Prompt: "put the logo in", Images: uploaded("header.png", "logo.png")})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Prompt map[string]map[string]any `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"3", "13"} {
		if _, ok := out.Prompt[gone]; ok {
			t.Errorf("node %s fed only the unused input and should be gone", gone)
		}
	}
	edit := out.Prompt["21"]["inputs"].(map[string]any)
	if _, ok := edit["image3"]; ok {
		t.Error("the edit node still links the removed image3")
	}
	for _, kept := range []string{"clip", "prompt", "image1", "image2"} {
		if _, ok := edit[kept]; !ok {
			t.Errorf("the edit node lost %s", kept)
		}
	}
	if out.Prompt["1"]["inputs"].(map[string]any)["image"] != "header.png" || out.Prompt["2"]["inputs"].(map[string]any)["image"] != "logo.png" {
		t.Error("the two pictures did not go to the first two inputs, in order")
	}
	if strings.Contains(body, "stale-") {
		t.Errorf("a placeholder is still in the graph:\n%s", body)
	}
}

// Zero pictures is still refused: an edit graph has nothing to work from.
func TestNoPicturesStillRefused(t *testing.T) {
	if _, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{Prompt: "anything"}); err == nil {
		t.Error("a source-photo backend asked for a text-only render must refuse")
	}
}

// The full set renders, and every node gets ITS image — order is what puts the
// subject and the background in the right places.
func TestFullFillWritesEveryNodeInOrder(t *testing.T) {
	body, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "blend these", Images: uploaded("a.png", "b.png", "c.png"),
	})
	if err != nil {
		t.Fatalf("a complete set should render: %v", err)
	}
	for _, want := range []string{"a.png", "b.png", "c.png"} {
		if !strings.Contains(body, want) {
			t.Errorf("body should carry %q", want)
		}
	}
	for _, stale := range []string{"stale-one.png", "stale-two.png", "stale-three.png"} {
		if strings.Contains(body, stale) {
			t.Errorf("saved placeholder %q survived into the render", stale)
		}
	}
}

// A backend deliberately capped below its node count is a config the operator
// chose; the guard must not turn it into a permanent error.
func TestExplicitCapBelowNodeCountStillRenders(t *testing.T) {
	if _, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "change this", Images: uploaded("mine.png"), ExpectedImages: 1,
	}); err != nil {
		t.Fatalf("a backend that asks for one image should accept one: %v", err)
	}
}

// An ExpectedImages larger than the mapped nodes cannot be satisfied and must
// fall back to the real node count rather than demanding the impossible.
func TestExpectedAboveNodeCountFallsBackToNodes(t *testing.T) {
	if _, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{
		Prompt: "blend", Images: uploaded("a.png", "b.png", "c.png"), ExpectedImages: 9,
	}); err != nil {
		t.Fatalf("filling every mapped node should be enough: %v", err)
	}
}

// The zero case keeps its own message — it is a different mistake (a text-only
// render aimed at a compose backend) and says so.
func TestNoImagesKeepsItsOwnDiagnosis(t *testing.T) {
	_, err := BuildComfyBody(threeInputGraph, threeInputMap(), ComfyBuildInput{Prompt: "a dragon"})
	if err == nil {
		t.Fatal("a compose backend asked for a text-only render must refuse")
	}
	if !strings.Contains(err.Error(), "text-only render") {
		t.Errorf("the zero case should keep its specific message, got %q", err)
	}
}

// What a wired backend is called on re-edit. Picking "blend" and reopening the
// form showed "edit" — the classifier tested for the ABSENCE of a prompt, so
// any composite that also takes text (most of them, and the shape the framework
// now asks for) was filed as an edit.
func TestWorkflowTypeCountsPhotosNotPrompts(t *testing.T) {
	twoWithPrompt := ComfyNodeMap{ImageNodes: []string{"1", "2"}, PromptNodes: []string{"6"}}
	if got := ComfyWorkflowTypeOf(twoWithPrompt); got != ComfyTypeBlend {
		t.Errorf("a two-photo composite that takes a prompt is a blend, got %q", got)
	}
	twoNoPrompt := ComfyNodeMap{ImageNodes: []string{"1", "2"}}
	if got := ComfyWorkflowTypeOf(twoNoPrompt); got != ComfyTypeBlend {
		t.Errorf("a two-photo composite with no prompt is still a blend, got %q", got)
	}
	oneWithPrompt := ComfyNodeMap{ImageNodes: []string{"1"}, PromptNodes: []string{"6"}}
	if got := ComfyWorkflowTypeOf(oneWithPrompt); got != ComfyTypeEdit {
		t.Errorf("one photo plus text is an edit, got %q", got)
	}
	// A promptless single-image graph (an upscale) is nearer an edit than a
	// blend — it changes one picture.
	if got := ComfyWorkflowTypeOf(ComfyNodeMap{ImageNodes: []string{"1"}}); got != ComfyTypeEdit {
		t.Errorf("a single-image processing graph should read as edit, got %q", got)
	}
	if got := ComfyWorkflowTypeOf(ComfyNodeMap{PromptNodes: []string{"6"}}); got != ComfyTypeGenerate {
		t.Errorf("no image input is a generate, got %q", got)
	}
}

// The built-in starters must round-trip to the type that produced them, or the
// form contradicts itself the first time anyone reopens it.
func TestBuiltinStartersRoundTrip(t *testing.T) {
	for _, c := range []struct{ kind, graph string }{
		{ComfyTypeGenerate, ComfyStarterGraph(ComfyTypeGenerate)},
		{ComfyTypeEdit, ComfyStarterGraph(ComfyTypeEdit)},
		{ComfyTypeBlend, ComfyStarterGraph(ComfyTypeBlend)},
	} {
		var spec RestImageSpec
		if _, err := ApplyComfyWorkflow(&spec, c.graph, ""); err != nil {
			t.Fatalf("%s starter should wire: %v", c.kind, err)
		}
		if got := ComfyWorkflowTypeOf(spec.ComfyMap); got != c.kind {
			t.Errorf("%s starter reads back as %q", c.kind, got)
		}
	}
}

// A cap below the mapped node count is permitted but hazardous: the unwritten
// nodes render the placeholder the workflow was exported with. It has to say so
// where the person configuring it will read it.
func TestLowCapWarnsAboutPlaceholders(t *testing.T) {
	spec := RestImageSpec{MaxInputImages: 2}
	warns, err := ApplyComfyWorkflow(&spec, threeInputGraph, "")
	if err != nil {
		t.Fatalf("a low cap is permitted, not fatal: %v", err)
	}
	var found bool
	for _, w := range warns {
		if strings.Contains(w, "max_input_images is 2 but 3 image node(s) are mapped") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a placeholder warning, got %v", warns)
	}
}

// No cap set is the ordinary case and must stay quiet.
func TestNoCapDoesNotWarn(t *testing.T) {
	var spec RestImageSpec
	warns, err := ApplyComfyWorkflow(&spec, threeInputGraph, "")
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	for _, w := range warns {
		if strings.Contains(w, "max_input_images") {
			t.Errorf("unexpected cap warning with no cap set: %q", w)
		}
	}
}

// A correctly wired graph that the importer could not read.
//
// Flux2 fails three assumptions at once: the node whose class says KSampler is
// a SELECTOR holding a sampler name, the prompt reaches the sampler through a
// BasicGuider rather than a positive input, and the seed lives on a separate
// RandomNoise node. Each on its own was enough to reject the import — with an
// error naming the graph as the problem, when nothing was wrong with it.
func flux2Graph(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "flux2_klein_workflow.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestFlux2WorkflowImports(t *testing.T) {
	spec, warns, err := NewComfyImageSpec("http://localhost:8188", "no_auth", flux2Graph(t), "")
	if err != nil {
		t.Fatalf("a wired Flux2 graph must import: %v", err)
	}

	// The prompt is three hops out: sampler → guider → FluxGuidance → encoder.
	if !eqStrs(spec.ComfyMap.PromptNodes, []string{"98:6"}) {
		t.Errorf("prompt nodes = %v, want the CLIPTextEncode at 98:6", spec.ComfyMap.PromptNodes)
	}
	// The seed is on RandomNoise, not the sampler. Unmapped, every render
	// reuses the exported number and the backend returns one picture forever.
	if !eqStrs(spec.ComfyMap.SeedNodes, []string{"98:25"}) || spec.ComfyMap.SeedKey != "noise_seed" {
		t.Errorf("seed = %v key %q, want [98:25] noise_seed", spec.ComfyMap.SeedNodes, spec.ComfyMap.SeedKey)
	}
	// BOTH size-bearing nodes, not just the latent. Flux2Scheduler carries its
	// own width/height and derives sigmas from the image area, so a render that
	// moves the latent alone leaves the scheduler solving for a resolution
	// nobody is rendering. That does not fail — it quietly degrades the picture,
	// which is why this assertion used to pass while the backend was wrong.
	if !eqStrs(spec.ComfyMap.WidthNodes, []string{"98:47", "98:48"}) {
		t.Errorf("width nodes = %v, want the Flux2 latent AND the scheduler", spec.ComfyMap.WidthNodes)
	}
	if !eqStrs(spec.ComfyMap.HeightNodes, []string{"98:47", "98:48"}) {
		t.Errorf("height nodes = %v — height must move with width or the two disagree", spec.ComfyMap.HeightNodes)
	}
	if spec.ComfyMap.OutputNode != "9" {
		t.Errorf("output node = %q, want 9", spec.ComfyMap.OutputNode)
	}
	// No image input: this is a text-to-image backend.
	if got := ComfyWorkflowTypeOf(spec.ComfyMap); got != ComfyTypeGenerate {
		t.Errorf("type = %q, want generate", got)
	}
	if len(warns) != 0 {
		t.Errorf("a fully mapped graph should import clean, got %v", warns)
	}
}

// KSamplerSelect holds a sampler NAME. Mapping it as the sampler leaves the
// prompt trace starting at a node with no links to follow.
func TestSamplerDetectionSkipsSelectors(t *testing.T) {
	graph := map[string]map[string]any{
		"1": {"class_type": "KSamplerSelect", "inputs": map[string]any{"sampler_name": "euler"}},
		"2": {"class_type": "SamplerCustomAdvanced", "inputs": map[string]any{"guider": []any{"3", 0}}},
		"3": {"class_type": "BasicGuider", "inputs": map[string]any{"conditioning": []any{"4", 0}}},
		"4": {"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "hello"}},
	}
	if got := findComfySampler(graph); got != "2" {
		t.Errorf("sampler = %q, want the node that consumes a guider", got)
	}
}

// A plain KSampler graph must be unaffected — positive is still tried first.
func TestClassicSamplerStillWins(t *testing.T) {
	graph := map[string]map[string]any{
		"1": {"class_type": "KSampler", "inputs": map[string]any{
			"positive": []any{"2", 0}, "negative": []any{"3", 0}, "seed": 42, "latent_image": []any{"4", 0}}},
		"2": {"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "a cat"}},
		"3": {"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "blurry"}},
		"4": {"class_type": "EmptyLatentImage", "inputs": map[string]any{"width": 512, "height": 512}},
	}
	sampler := findComfySampler(graph)
	if sampler != "1" {
		t.Fatalf("sampler = %q, want 1", sampler)
	}
	// The POSITIVE encoder, never the negative — the fallback walk skips it.
	if got := traceSamplerPrompt(graph, sampler, comfyInputs(graph, sampler)); got != "2" {
		t.Errorf("prompt node = %q, want the positive encoder 2", got)
	}
}

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

// The setup form's "what this backend does" picker. It exists so adding a photo
// editor is a dropdown rather than a JSON paste — but the pasted export has to
// stay authoritative, and a saved backend has to survive re-editing.

func buildComfy(t *testing.T, vals map[string]any) RestImageSpec {
	t.Helper()
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	if vals["base_url"] == nil {
		vals["base_url"] = "http://localhost:8188"
	}
	raw, _, err := comfyBuildSpec(tpl, vals)
	if err != nil {
		t.Fatalf("comfyBuildSpec: %v", err)
	}
	var s RestImageSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	return s
}

func TestTypePicksTheStartingGraph(t *testing.T) {
	gen := buildComfy(t, map[string]any{"workflow_type": ComfyTypeGenerate})
	if gen.SupportsImageInput() {
		t.Error("a generate backend must not take source photos")
	}
	if len(gen.ComfyMap.PromptNodes) == 0 {
		t.Error("a generate backend needs a prompt node")
	}

	edit := buildComfy(t, map[string]any{"workflow_type": ComfyTypeEdit})
	if !edit.SupportsImageInput() {
		t.Error("an edit backend must take a source photo")
	}
	if edit.MaxImages() != 1 {
		t.Errorf("edit MaxImages = %d, want 1", edit.MaxImages())
	}
	if len(edit.ComfyMap.PromptNodes) == 0 {
		t.Error("an img2img backend still takes a prompt")
	}

	blend := buildComfy(t, map[string]any{"workflow_type": ComfyTypeBlend})
	if !blend.SupportsImageInput() {
		t.Error("a blend backend must take source photos")
	}
	if blend.MaxImages() != 2 {
		t.Errorf("blend MaxImages = %d, want 2", blend.MaxImages())
	}
	if len(blend.ComfyMap.PromptNodes) != 0 {
		t.Error("a blend graph has no text node")
	}
}

func TestEmptyTypeStaysGenerate(t *testing.T) {
	// Every connector authored before the picker existed has no workflow_type.
	// It must keep meaning what it meant.
	s := buildComfy(t, map[string]any{})
	if s.SupportsImageInput() {
		t.Error("a blank type must stay text-to-image, as it was before the picker")
	}
}

func TestPastedWorkflowBeatsTheType(t *testing.T) {
	// A dropdown that could silently replace a pasted graph would be worse than
	// no dropdown.
	s := buildComfy(t, map[string]any{
		"workflow_type": ComfyTypeGenerate, // says generate…
		"workflow":      ComfyBlendDefaultGraph(),
	})
	if !s.SupportsImageInput() {
		t.Error("the pasted graph must win over the type picker")
	}
	if s.MaxImages() != 2 {
		t.Errorf("MaxImages = %d, want the pasted blend graph's 2", s.MaxImages())
	}
}

func TestTypeIsReadBackFromTheWiring(t *testing.T) {
	// On re-edit the panel should show what the backend IS, not what was picked
	// — someone can select "edit" and paste an upscale graph, and the map is
	// what decides which action it serves.
	tpl, _ := GetConnectorTemplate("comfyui")
	for _, tc := range []struct{ picked, want string }{
		{ComfyTypeGenerate, ComfyTypeGenerate},
		{ComfyTypeEdit, ComfyTypeEdit},
		{ComfyTypeBlend, ComfyTypeBlend},
	} {
		spec := buildComfy(t, map[string]any{"workflow_type": tc.picked})
		raw, _ := json.Marshal(spec)
		got := comfyReadValues(tpl, raw)["workflow_type"]
		if got != tc.want {
			t.Errorf("picked %q, read back %v, want %q", tc.picked, got, tc.want)
		}
	}
}

func TestDetectReportsTheType(t *testing.T) {
	tpl, _ := GetConnectorTemplate("comfyui")
	out, _, err := comfyDetect(tpl, map[string]any{"workflow": ComfyBlendDefaultGraph()})
	if err != nil {
		t.Fatalf("comfyDetect: %v", err)
	}
	if out["workflow_type"] != ComfyTypeBlend {
		t.Errorf("detect reported %v, want blend", out["workflow_type"])
	}
	if out["image_nodes"] != JoinCSV([]string{"1", "2"}) {
		t.Errorf("detect image nodes = %v, want both LoadImage ids", out["image_nodes"])
	}
}

func TestEditedImageNodeOrderSurvivesResave(t *testing.T) {
	// Node-id order is arbitrary relative to "the first photo", so a compose
	// backend's order is THE field an admin hand-corrects. Re-saving used to
	// discard it on a blend, because the "has a map" check looked only at
	// prompt_nodes and a blend graph has none.
	tpl, _ := GetConnectorTemplate("comfyui")
	spec := buildComfy(t, map[string]any{"workflow_type": ComfyTypeBlend})
	raw, _ := json.Marshal(spec)
	vals := comfyReadValues(tpl, raw)

	// The admin swaps subject and background.
	vals["image_nodes"] = "2,1"
	resaved, _, err := comfyBuildSpec(tpl, vals)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	var out RestImageSpec
	if err := json.Unmarshal(resaved, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !eqStrs(out.ComfyMap.ImageNodes, []string{"2", "1"}) {
		t.Errorf("image nodes = %v, want the admin's [2 1] — the correction was undone", out.ComfyMap.ImageNodes)
	}
}

func TestTypeOfClassifiesByWiringNotClass(t *testing.T) {
	cases := []struct {
		name string
		m    ComfyNodeMap
		want string
	}{
		{"text only", ComfyNodeMap{PromptNodes: []string{"6"}}, ComfyTypeGenerate},
		{"photo plus text", ComfyNodeMap{PromptNodes: []string{"6"}, ImageNodes: []string{"11"}}, ComfyTypeEdit},
		{"photos, no text", ComfyNodeMap{ImageNodes: []string{"1", "2"}}, ComfyTypeBlend},
		{"nothing wired", ComfyNodeMap{}, ComfyTypeGenerate},
	}
	for _, c := range cases {
		if got := ComfyWorkflowTypeOf(c.m); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStarterGraphFallsBackToGenerate(t *testing.T) {
	for _, bad := range []string{"", "nonsense", "CUSTOM"} {
		if ComfyStarterGraph(bad) != comfyDefaultGraph {
			t.Errorf("ComfyStarterGraph(%q) should fall back to the text-to-image graph", bad)
		}
	}
	if ComfyStarterGraph("EDIT") != comfyEditDefaultGraph {
		t.Error("the type should be case-insensitive")
	}
}

func TestTemplateOffersTheTypeField(t *testing.T) {
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	var found bool
	for _, f := range tpl.Fields {
		if f.Key != "workflow_type" {
			continue
		}
		found = true
		if f.Type != "select" {
			t.Errorf("workflow_type type = %q, want select", f.Type)
		}
		if len(f.Options) != 3 {
			t.Errorf("options = %v, want all three starting points", f.Options)
		}
		if f.Default != ComfyTypeGenerate {
			t.Errorf("default = %v, want generate", f.Default)
		}
	}
	if !found {
		t.Error("the comfyui template must offer workflow_type")
	}
}

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
	body, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: prompt, Negative: neg, Width: w, Height: h, Steps: steps, Seed: seed,
	})
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

// Which photo is "the first one". The caller's first image goes to
// ImageNodes[0], so that list's order decides what "the base/subject" means —
// and ComfyUI numbers nodes by creation, not by role.

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

// Asking a COMPOSE workflow for a text-only picture. It has image inputs and
// nothing to put in them, so the graph runs against the filenames it was
// exported with — a photo from whenever that export happened — and returns the
// prompt composited with it. The result looks like a poor render, not a wiring
// error, which is why it publishes.

func TestAComposeBackendRefusesATextOnlyRender(t *testing.T) {
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, qwenBlendGraph(t), ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	_, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{Prompt: "a dramatic blog header"})
	if err == nil {
		t.Fatal("a blend backend must refuse a text-only render rather than draw against its placeholders")
	}
	for _, want := range []string{"SOURCE PHOTOS", "text-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should say what is wrong (%q missing): %v", want, err)
		}
	}
	// With its sources supplied it builds normally.
	if _, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{
		Prompt: "blend them",
		Images: []ComfyUploadedImage{{Name: "a.png"}, {Name: "b.png"}},
	}); err != nil {
		t.Errorf("a compose backend with its images must build: %v", err)
	}
}

func TestATextToImageBackendIsUnaffected(t *testing.T) {
	// The guard keys on the graph HAVING image inputs, so a txt2img workflow —
	// which has none — is untouched.
	var s RestImageSpec
	if _, err := ApplyComfyWorkflow(&s, ComfyStarterGraph("generate"), ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(s.ComfyMap.ImageNodes) != 0 {
		t.Skipf("starter graph has image nodes (%v) — not the shape under test", s.ComfyMap.ImageNodes)
	}
	if _, err := BuildComfyBody(s.ComfyWorkflow, s.ComfyMap, ComfyBuildInput{Prompt: "a cat"}); err != nil {
		t.Errorf("a text-to-image backend must still render from text alone: %v", err)
	}
}

// Auto-wiring for the image-input graphs: img2img, multi-image compose, blend.
// The failure these guard against is specific and nasty — a graph that wires
// "successfully" but drops the caller's photo produces a plausible picture of
// the wrong thing, which reads as a bad model rather than bad config.

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
