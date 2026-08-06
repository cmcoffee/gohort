// ComfyUI workflow auto-wiring: turn a raw "Save (API Format)" graph into a
// working rest_image spec WITHOUT the user hand-editing JSON. ComfyUI's API
// graph is regular enough to derive the wiring deterministically:
//
//   - the OUTPUT node is the SaveImage node → drives the poll paths;
//   - the PROMPT node is the CLIPTextEncode feeding the sampler's `positive`
//     input → its text becomes {prompt} (and the `negative` side → {negative});
//   - the sampler's seed becomes {seed} so images vary.
//
// Everything else in the graph is preserved verbatim. This is a SMART PRESET,
// not a new runtime — the generic rest_image connector still executes it; we
// only automate the fiddly authoring the user was doing by hand. Non-standard
// graphs (SDXL dual-encoders, custom text nodes) that this can't trace are
// reported back so the assistant / Edit spec can finish them.
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// comfyDefaultGraph is a minimal SD1.5 txt2img graph (RAW API format, no tokens)
// used when a ComfyUI backend is set up without a custom workflow. It's wired by
// ApplyComfyWorkflow like any pasted workflow, so the default path and the custom
// path both run the mapping model. Edit the checkpoint via the config panel.
const comfyDefaultGraph = `{
  "3":{"class_type":"KSampler","inputs":{"seed":0,"steps":20,"cfg":7,"sampler_name":"euler","scheduler":"normal","denoise":1,"model":["4",0],"positive":["6",0],"negative":["7",0],"latent_image":["5",0]}},
  "4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"v1-5-pruned-emaonly.safetensors"}},
  "5":{"class_type":"EmptyLatentImage","inputs":{"width":512,"height":512,"batch_size":1}},
  "6":{"class_type":"CLIPTextEncode","inputs":{"text":"a scenic landscape","clip":["4",1]}},
  "7":{"class_type":"CLIPTextEncode","inputs":{"text":"","clip":["4",1]}},
  "8":{"class_type":"VAEDecode","inputs":{"samples":["3",0],"vae":["4",2]}},
  "9":{"class_type":"SaveImage","inputs":{"filename_prefix":"gohort","images":["8",0]}}
}`

// comfyEditDefaultGraph is a minimal SD1.5 IMG2IMG graph: LoadImage → VAEEncode
// → KSampler(denoise 0.55) → SaveImage. It's the edit counterpart to
// comfyDefaultGraph, so "add a ComfyUI backend that edits photos" works from the
// config panel with the workflow box left blank, exactly as generate does.
const comfyEditDefaultGraph = `{
  "3":{"class_type":"KSampler","inputs":{"seed":0,"steps":20,"cfg":7,"sampler_name":"euler","scheduler":"normal","denoise":0.55,"model":["4",0],"positive":["6",0],"negative":["7",0],"latent_image":["10",0]}},
  "4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"v1-5-pruned-emaonly.safetensors"}},
  "6":{"class_type":"CLIPTextEncode","inputs":{"text":"a scenic landscape","clip":["4",1]}},
  "7":{"class_type":"CLIPTextEncode","inputs":{"text":"","clip":["4",1]}},
  "8":{"class_type":"VAEDecode","inputs":{"samples":["3",0],"vae":["4",2]}},
  "9":{"class_type":"SaveImage","inputs":{"filename_prefix":"gohort","images":["8",0]}},
  "10":{"class_type":"VAEEncode","inputs":{"pixels":["11",0],"vae":["4",2]}},
  "11":{"class_type":"LoadImage","inputs":{"image":"example.png"}}
}`

// comfyBlendDefaultGraph is a two-photo BLEND: two LoadImage nodes into
// ImageBlend into SaveImage. No checkpoint, no sampler, no prompt — it's pure
// pixel work, so it runs in a fraction of a second and needs no model loaded.
//
// It's also the reason auto-wiring tolerates a sampler-less graph: this is a
// perfectly good backend that the old "no KSampler found" check rejected.
const comfyBlendDefaultGraph = `{
  "1":{"class_type":"LoadImage","inputs":{"image":"example.png"}},
  "2":{"class_type":"LoadImage","inputs":{"image":"example.png"}},
  "3":{"class_type":"ImageBlend","inputs":{"blend_factor":0.5,"blend_mode":"normal","image1":["1",0],"image2":["2",0]}},
  "9":{"class_type":"SaveImage","inputs":{"filename_prefix":"gohort","images":["3",0]}}
}`

// Workflow types the setup form offers. A ComfyUI backend IS its workflow, so
// this is really "which starting graph" — it only applies when the workflow box
// is left blank, and pasting an export overrides it entirely.
const (
	ComfyTypeGenerate = "generate" // text → image
	ComfyTypeEdit     = "edit"     // photo + text → image
	ComfyTypeBlend    = "blend"    // two photos → image, no model
	ComfyTypeCustom   = "custom"   // whatever was pasted
)

// ComfyWorkflowTypes lists the pickable starting points, in the order the form
// should show them.
func ComfyWorkflowTypes() []string {
	return []string{ComfyTypeGenerate, ComfyTypeEdit, ComfyTypeBlend}
}

// ComfyStarterGraph returns the built-in graph for a workflow type. Unknown or
// empty types fall back to the text-to-image graph, which is what a ComfyUI
// backend meant before edit existed.
func ComfyStarterGraph(workflowType string) string {
	switch strings.ToLower(strings.TrimSpace(workflowType)) {
	case ComfyTypeEdit:
		return comfyEditDefaultGraph
	case ComfyTypeBlend:
		return comfyBlendDefaultGraph
	default:
		return comfyDefaultGraph
	}
}

// ComfyEditDefaultGraph exposes the built-in img2img starting graph so the admin
// form and the Builder can offer "edit photos" without the user pasting one.
func ComfyEditDefaultGraph() string { return comfyEditDefaultGraph }

// ComfyBlendDefaultGraph exposes the built-in two-photo blend graph, for
// "combine these two pictures" with nothing to paste and no model to load.
func ComfyBlendDefaultGraph() string { return comfyBlendDefaultGraph }

// ComfyWorkflowTypeOf names what a WIRED backend actually turned out to be,
// read back from its node map rather than from what was picked at setup. The
// two can differ — someone selects "edit" and then pastes an upscale graph —
// and the map is the thing that decides which action the backend serves, so
// it's the honest thing to show on re-edit.
func ComfyWorkflowTypeOf(m ComfyNodeMap) string {
	// HOW MANY PHOTOS IT TAKES, not whether it also takes text.
	//
	// Prompt-absence was the old test for a blend, and it misfiled every
	// composite that accepts a prompt — which is most real ones, and which the
	// framework now actively asks for ("say HOW they should combine"). A
	// two-photo graph with a text node came back as "edit", so someone who
	// picked blend, saved, and reopened the form saw their choice apparently
	// revert, over and over, with nothing wrong but the label.
	//
	// Counting inputs also matches what the words mean to the person choosing:
	// edit changes A photo, blend combines SEVERAL. A promptless single-image
	// graph (an upscale) reads as edit under this, which is nearer the truth
	// than calling it a blend.
	switch {
	case len(m.ImageNodes) == 0:
		return ComfyTypeGenerate
	case len(m.ImageNodes) >= 2:
		return ComfyTypeBlend
	default:
		return ComfyTypeEdit
	}
}

// NewComfyImageSpec builds a ready-to-save ComfyUI rest_image spec: it applies the
// comfyui preset (endpoints, from base_url) then auto-wires the workflow into the
// mapping model. A blank workflow uses the built-in default graph. This is the one
// path both the admin form and the Builder's import_comfyui action go through, so
// every ComfyUI backend gets a visible/editable node map.
func NewComfyImageSpec(baseURL, credential, workflow, saveNodeOverride string) (RestImageSpec, []string, error) {
	cred := strings.TrimSpace(credential)
	if cred == "" {
		cred = "no_auth"
	}
	spec, err := ApplyRestImagePreset("comfyui", RestImageSpec{Credential: cred}, map[string]string{"base_url": strings.TrimSpace(baseURL)})
	if err != nil {
		return spec, nil, err
	}
	wf := strings.TrimSpace(workflow)
	if wf == "" {
		wf = comfyDefaultGraph
	}
	warns, err := ApplyComfyWorkflow(&spec, wf, saveNodeOverride)
	return spec, warns, err
}

// ApplyComfyWorkflow parses a ComfyUI API-format workflow and rewrites s's
// SubmitBody / PollReadyPath / PollFields to run it: {prompt}/{negative}/{seed}
// are injected into the graph and the poll paths point at the SaveImage node.
// saveNodeOverride forces a specific output node id (empty = auto-detect).
// Returns non-fatal warnings; a fatal problem (no output node, can't find the
// prompt node) returns an error.
func ApplyComfyWorkflow(s *RestImageSpec, apiJSON, saveNodeOverride string) ([]string, error) {
	graph, err := parseComfyGraph(apiJSON)
	if err != nil {
		return nil, err
	}
	var warnings []string
	var m ComfyNodeMap

	// 1. Output (SaveImage) node — drives the poll paths.
	save := strings.TrimSpace(saveNodeOverride)
	if save != "" {
		if _, ok := graph[save]; !ok {
			return nil, fmt.Errorf("node id %q is not in the workflow", save)
		}
	} else {
		save = findComfyNode(graph, func(class string) bool { return class == "SaveImage" })
		if save == "" {
			save = findComfyNode(graph, func(class string) bool { return strings.Contains(class, "SaveImage") })
		}
		if save == "" {
			return nil, fmt.Errorf("no SaveImage node found — add one in ComfyUI, or set the output node in the config panel")
		}
	}
	m.OutputNode = save

	// 2. Image input: LoadImage nodes a source photo is written into. Detected
	//    BEFORE the sampler, because whether a missing sampler is fatal depends
	//    on it — see step 3. Ordered by node id for determinism; that order is
	//    arbitrary relative to what a user means by "the first photo", so a
	//    compose/blend graph's order is worth checking in the config panel.
	//    Mask loaders are a separate list: they take a mask, not a photo, and
	//    one sitting in ImageNodes would silently eat a source image.
	for _, id := range sortedComfyNodes(graph) {
		switch class := comfyClass(graph, id); {
		case strings.Contains(class, "LoadImageMask"):
			m.MaskNodes = append(m.MaskNodes, id)
		case strings.Contains(class, "LoadImage"):
			m.ImageNodes = append(m.ImageNodes, id)
		}
	}
	if len(m.ImageNodes) > 0 {
		m.ImageKey = "image"
		m.ImageNodes = orderImageNodesByConsumer(graph, m.ImageNodes)
	}

	// 3. Sampler → positive/negative conditioning → text nodes.
	//
	// A graph need not HAVE a sampler. A pure image-processing workflow — blend
	// two photos, upscale, composite — is nodes in, image out, with no
	// diffusion and no text anywhere. Those are legitimate backends, and
	// demanding a KSampler rejected them at import with a message about prompt
	// nodes that made no sense for what the user had built. So: a sampler is
	// required only when there's no image input to work from, because a graph
	// with neither has no way to produce anything.
	sampler := findComfySampler(graph)
	if sampler == "" && len(m.ImageNodes) == 0 {
		return nil, fmt.Errorf("no KSampler (or node with a positive input) found, and no LoadImage node either — this graph has no prompt and no image to work from")
	}

	var sIn map[string]any
	if sampler != "" {
		sIn = comfyInputs(graph, sampler)
		if pid := traceSamplerPrompt(graph, sampler, sIn); pid != "" {
			m.PromptNodes = []string{pid}
			m.TextKeys = comfyTextKeys(comfyInputs(graph, pid))
		} else if len(m.ImageNodes) == 0 {
			return nil, fmt.Errorf("couldn't trace the sampler's conditioning to a text node — set the prompt node in the config panel")
		} else {
			warnings = append(warnings, "no text node reached from the sampler; this backend takes images but no prompt")
		}
		if nid := traceComfyText(graph, sIn["negative"]); nid != "" {
			m.NegativeNodes = []string{nid}
		} else if _, ok := sIn["negative"]; ok {
			warnings = append(warnings, "negative conditioning didn't lead to a text node; the negative prompt won't apply")
		}
	} else {
		warnings = append(warnings, "no sampler in this graph — it processes the input image(s) directly and takes no prompt")
	}

	// 4. Seed + steps on the sampler (absent on a promptless processing graph).
	switch {
	case sampler == "":
		// nothing to seed — the graph is deterministic
	case hasKey(sIn, "seed"):
		m.SeedNodes, m.SeedKey = []string{sampler}, "seed"
	case hasKey(sIn, "noise_seed"):
		m.SeedNodes, m.SeedKey = []string{sampler}, "noise_seed"
	default:
		// The seed may live one node away. Flux drives SamplerCustomAdvanced from
		// a RandomNoise node that holds noise_seed, so the sampler itself has no
		// seed input at all — and left unmapped, every render reuses whatever
		// number the workflow was exported with and the backend returns the same
		// picture forever.
		if nid, key := findComfySeedNode(graph, sIn); nid != "" {
			m.SeedNodes, m.SeedKey = []string{nid}, key
		} else {
			warnings = append(warnings, "no seed input on the sampler; generated images may not vary")
		}
	}
	if v, ok := sIn["steps"]; ok {
		if comfyIsLink(v) {
			// Computed by the graph — a switch, a primitive, a preset chain.
			// Mapping it would advertise a knob that can only damage the wiring,
			// so leave it unmapped and say why: an empty steps field otherwise
			// looks like a detection failure worth "fixing" by hand.
			warnings = append(warnings, fmt.Sprintf("steps is driven by node %v in this workflow, so it stays unmapped and the graph keeps deciding it", firstComfyLinkID(v)))
		} else {
			m.StepsNodes = []string{sampler}
		}
	}

	// 5. Locate the latent node the sampler draws from (used for size, below).
	latent := ""
	if lid, ok := comfyLinkTarget(sIn["latent_image"]); ok && hasKey(comfyInputs(graph, lid), "width") {
		latent = lid
	} else {
		latent = findComfyNode(graph, func(class string) bool { return strings.Contains(class, "EmptyLatent") })
	}
	// 7. Size: the latent node the sampler draws from (EmptyLatentImage or variant).
	if lin := comfyInputs(graph, latent); hasKey(lin, "width") && hasKey(lin, "height") {
		m.WidthNodes, m.HeightNodes = []string{latent}, []string{latent}
		if w := comfyInt(lin["width"]); w > 0 {
			s.DefaultWidth = w
		}
		if h := comfyInt(lin["height"]); h > 0 {
			s.DefaultHeight = h
		}
	} else if len(m.ImageNodes) == 0 {
		// Only a defect on a txt2img graph. An img2img graph takes its size from
		// the source photo (the latent comes from VAEEncode, not
		// EmptyLatentImage), so warning here told every edit backend it was
		// broken when it was working exactly as intended.
		warnings = append(warnings, "no EmptyLatentImage width/height found; image size is fixed to the workflow")
	}

	// 5. Store the workflow as the user gave it, pretty-indented (json.Indent
	//    preserves their content + key order, unlike re-marshaling the parsed map);
	//    the mapping model owns the body and poll paths, so clear the legacy fields.
	s.ComfyWorkflow = PrettyComfyJSON(apiJSON)
	s.ComfyMap = m
	s.SubmitBody = ""
	s.PollReadyPath = ""
	s.PollFields = nil
	// A cap BELOW the mapped node count leaves nodes nobody ever writes, and an
	// unwritten LoadImage renders the filename the workflow was exported with —
	// usually example.png. The render succeeds and quietly contains a picture
	// from somebody else's session, which is not a failure anyone goes looking
	// for. Permitted (a stray loader the output branch never consumes is a real
	// case, and every LoadImage gets mapped whether consumed or not), so it
	// warns where the person configuring it will read it rather than failing.
	if s.MaxInputImages > 0 && s.MaxInputImages < len(m.ImageNodes) {
		warnings = append(warnings, fmt.Sprintf(
			"max_input_images is %d but %d image node(s) are mapped — the other %d will render whatever placeholder the workflow was saved with. Trim image_nodes to the ones this graph actually uses, or raise the cap",
			s.MaxInputImages, len(m.ImageNodes), len(m.ImageNodes)-s.MaxInputImages))
	}
	return warnings, nil
}

// PrettyComfyJSON indents a workflow so it's human-readable in the config panel;
// it preserves content + key order (only adds whitespace). Input that isn't valid
// JSON is returned trimmed but otherwise unchanged.
func PrettyComfyJSON(s string) string {
	s = strings.TrimSpace(s)
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(s), "", "  ") == nil {
		return buf.String()
	}
	return s
}

// findComfySampler picks the node that actually DENOISES, which is not always
// the one whose class name says KSampler.
//
// A Flux graph carries a KSamplerSelect — a picker holding a sampler NAME and
// nothing else: no conditioning, no latent, no seed. Matching on the substring
// found it first and mapped it as the sampler, so the prompt trace started at a
// node with no links to follow and the import failed with "couldn't trace the
// sampler's conditioning" against a graph that was wired correctly.
//
// Structure decides it: a sampler consumes a latent, or conditioning, or a
// guider that carries conditioning. The class name is the tiebreak, not the test.
func findComfySampler(graph map[string]map[string]any) string {
	var byClass string
	for _, id := range sortedComfyNodes(graph) {
		class := comfyClass(graph, id)
		if strings.Contains(class, "Select") {
			continue // a picker FOR a sampler, not a sampler
		}
		in := comfyInputs(graph, id)
		if hasKey(in, "latent_image") || hasKey(in, "positive") || hasKey(in, "guider") {
			return id
		}
		if byClass == "" && strings.Contains(class, "KSampler") {
			byClass = id
		}
	}
	return byClass
}

// samplerSeedInputs are the sampler inputs worth following to find a seed,
// in a fixed order for the usual reason: a map walk would pick a different node
// between imports.
var samplerSeedInputs = []string{"noise", "sampler", "sigmas", "guider"}

// findComfySeedNode looks one hop out from the sampler for the node that holds
// the seed, returning it and the key it lives under. Only a LITERAL counts: a
// linked seed is computed by the graph, and writing over it would sever the
// wiring the author built.
func findComfySeedNode(graph map[string]map[string]any, sIn map[string]any) (string, string) {
	for _, in := range samplerSeedInputs {
		tid, ok := comfyLinkTarget(sIn[in])
		if !ok {
			continue
		}
		nin := comfyInputs(graph, tid)
		for _, key := range []string{"noise_seed", "seed"} {
			if v, has := nin[key]; has && !comfyIsLink(v) {
				return tid, key
			}
		}
	}
	return "", ""
}

// samplerConditioningInputs are the inputs a prompt can arrive on, in the order
// worth trying. "positive" first, so a graph that has one behaves exactly as
// before; "guider" and "conditioning" cover the Flux shape, where the text
// reaches the sampler through a BasicGuider instead.
var samplerConditioningInputs = []string{"positive", "guider", "conditioning"}

// traceSamplerPrompt finds the text node feeding a sampler.
//
// Tries the conditioning inputs in a FIXED order, then the sampler's remaining
// inputs sorted by name. Both orderings are deliberate: a map walk is
// nondeterministic, and on a graph with two text chains it would map the
// positive encoder on one import and the negative on the next.
//
// "negative" is skipped in the fallback for that reason — reaching it by
// accident makes the negative prompt the backend's prompt, which is worse than
// finding nothing at all.
func traceSamplerPrompt(graph map[string]map[string]any, sampler string, sIn map[string]any) string {
	for _, key := range samplerConditioningInputs {
		if id := traceComfyText(graph, sIn[key]); id != "" {
			return id
		}
	}
	keys := make([]string, 0, len(sIn))
	for k := range sIn {
		if k != "negative" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if id := traceComfyText(graph, sIn[k]); id != "" {
			return id
		}
	}
	return ""
}

// traceComfyText resolves a sampler conditioning link (positive/negative) to the
// text node it leads to, following indirection (ControlNetApply, etc.).
func traceComfyText(graph map[string]map[string]any, linkVal any) string {
	tid, ok := comfyLinkTarget(linkVal)
	if !ok {
		return ""
	}
	return findComfyTextNode(graph, tid, 4, map[string]bool{})
}

// comfyTextKeyNames are the input names a prompt can arrive under, in priority
// order. "text" covers CLIPTextEncode, "text_g"/"text_l" the SDXL dual encoder,
// and "prompt" the newer per-model encoders — TextEncodeQwenImageEditPlus and
// friends name their input `prompt`, and looking only for `text` left those
// graphs with no prompt node at all: a working edit workflow imported as a
// promptless one, misread as a blend.
var comfyTextKeyNames = []string{"text", "text_g", "text_l", "prompt"}

// comfyTextKeys returns the prompt input key(s) present on a text node: ["text"]
// for a standard CLIPTextEncode, ["text_g","text_l"] for an SDXL encoder,
// ["prompt"] for a Qwen-style edit encoder.
func comfyTextKeys(in map[string]any) []string {
	var keys []string
	for _, k := range comfyTextKeyNames {
		if hasKey(in, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		keys = []string{"text"}
	}
	return keys
}

// ComfyBuildInput is one generation request's resolved values, ready to inject
// into a graph. A struct rather than a parameter list: with images, a mask and
// denoise added, the positional form reached eleven arguments of mostly
// interchangeable ints.
type ComfyBuildInput struct {
	Prompt   string
	Negative string
	Width    int
	Height   int
	Steps    int
	Seed     int // already resolved — never negative
	// Images are the uploaded source photos in CALLER order: Images[0] goes to
	// ComfyMap.ImageNodes[0], and so on.
	Images []ComfyUploadedImage
	Mask   *ComfyUploadedImage
	// ExpectedImages is how many source photos this backend actually asks a
	// caller for — MaxInputImages when the operator set one, otherwise the
	// number of mapped image nodes. Supplied so the partial-fill check below
	// does not fire on a backend deliberately capped below its node count.
	// Zero means "as many nodes as are mapped".
	ExpectedImages int
}

// ComfyUploadedImage is an input image already stored on the ComfyUI server —
// the value a LoadImage node references.
type ComfyUploadedImage struct {
	Name      string // filename returned by /upload/image
	Subfolder string // optional; joined onto Name as "subfolder/name"
}

// Ref is the value written into a LoadImage node's image input.
func (u ComfyUploadedImage) Ref() string {
	if s := strings.TrimSpace(u.Subfolder); s != "" {
		return s + "/" + u.Name
	}
	return u.Name
}

// BuildComfyBody parses the stored workflow and injects each generation value
// into the nodes named by the map, returning the /prompt request body. This is
// the mapping-model counterpart to token substitution — the wiring lives in the
// (editable) map, not baked into the graph.
func BuildComfyBody(workflow string, m ComfyNodeMap, in ComfyBuildInput) (string, error) {
	graph, err := parseComfyGraph(workflow)
	if err != nil {
		return "", fmt.Errorf("stored workflow is invalid: %w", err)
	}
	// Both setters refuse to overwrite a LINKED input. A workflow can drive any
	// parameter from another node — this graph feeds steps and cfg from switch
	// nodes — and writing a plain value over ["<id>",slot] severs that wiring
	// silently: ComfyUI runs a graph the author never built and the output looks
	// like a bad render rather than a broken import. The graph wins; it was
	// built that way on purpose.
	setStr := func(nodes, keys []string, val string) {
		for _, id := range nodes {
			if inputs := comfyInputs(graph, id); inputs != nil {
				for _, k := range keys {
					if v, ok := inputs[k]; ok && !comfyIsLink(v) {
						inputs[k] = val
					}
				}
			}
		}
	}
	setNum := func(nodes []string, key string, val any) {
		for _, id := range nodes {
			if inputs := comfyInputs(graph, id); inputs != nil {
				if v, ok := inputs[key]; ok && !comfyIsLink(v) {
					inputs[key] = val
				}
			}
		}
	}
	keys := m.TextKeys
	if len(keys) == 0 {
		keys = []string{"text"}
	}
	setStr(m.PromptNodes, keys, in.Prompt)
	setStr(m.NegativeNodes, keys, in.Negative)
	setNum(m.WidthNodes, "width", in.Width)
	setNum(m.HeightNodes, "height", in.Height)
	seedKey := m.SeedKey
	if seedKey == "" {
		seedKey = "seed"
	}
	setNum(m.SeedNodes, seedKey, in.Seed)
	if in.Steps > 0 {
		setNum(m.StepsNodes, "steps", in.Steps)
	}

	// Input images. Unlike every value above, a missing target here fails
	// SILENTLY in the worst possible way: setStr no-ops on an unknown node id,
	// the graph runs against whatever placeholder the workflow was saved with,
	// and the caller gets a plausible image that ignored their photo entirely.
	// So this path errors instead of skipping.
	imageKey := m.ImageKey
	if imageKey == "" {
		imageKey = "image"
	}
	if len(in.Images) > len(m.ImageNodes) {
		return "", fmt.Errorf("this backend takes %d input image(s), got %d", len(m.ImageNodes), len(in.Images))
	}
	// The same silent failure from the other direction, and the one that
	// actually shipped: a workflow with image inputs, asked for a text-only
	// render. Nothing writes those nodes, so the graph runs against the
	// filenames it was SAVED with — whatever photo happened to be in ComfyUI's
	// input folder when the workflow was exported — and returns a composite of
	// the prompt and a picture from some earlier session. It looks like a bad
	// render rather than a wiring error, so it publishes.
	//
	// Observed: a blog header rendered through an image-BLEND backend, coming
	// back as the article's subject composited with a leftover source photo,
	// once a day, for as long as that backend was the configured provider.
	if len(m.ImageNodes) > 0 && len(in.Images) == 0 {
		return "", fmt.Errorf("this backend composes SOURCE PHOTOS (%d image input%s) and was asked for a text-only render — it would draw against whatever placeholder its workflow was saved with. Use a text-to-image backend for this, or supply the source image(s)", len(m.ImageNodes), map[bool]string{true: "s", false: ""}[len(m.ImageNodes) != 1])
	}
	// PARTIAL fill is the same failure as none, and it shipped for the same
	// reason: only as many nodes are written as there are images, so a graph
	// with three inputs given one photo runs its other two against the
	// filenames the workflow was SAVED with. The render succeeds and returns a
	// composite of one picture the caller supplied and two from whenever the
	// workflow was exported — which reads as "it only blended one of my
	// images", not as a wiring error, so nothing about it looks like a bug.
	//
	// Erroring is the same choice made directly above for the zero case: the
	// caller can supply the missing photos or the backend can map fewer nodes,
	// and either beats a picture nobody can account for.
	want := in.ExpectedImages
	if want <= 0 || want > len(m.ImageNodes) {
		want = len(m.ImageNodes)
	}
	if len(in.Images) < want {
		return "", fmt.Errorf("this backend composes %d source photos and got %d — the unfilled input(s) would render against whatever placeholder the workflow was saved with, so this would silently blend a picture from some earlier session. Supply %d image(s), or map fewer image_nodes in the backend config",
			want, len(in.Images), want)
	}
	for i, img := range in.Images {
		if err := setComfyImage(graph, m.ImageNodes[i], imageKey, img); err != nil {
			return "", err
		}
	}
	if in.Mask != nil {
		if len(m.MaskNodes) == 0 {
			return "", fmt.Errorf("this backend has no mask node — remove the mask, or map one in the config panel")
		}
		if err := setComfyImage(graph, m.MaskNodes[0], imageKey, *in.Mask); err != nil {
			return "", err
		}
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		return "", err
	}
	return `{"prompt":` + string(raw) + `}`, nil
}

// setComfyImage writes an uploaded image's reference into one node, erroring if
// the mapped node or its input key isn't in the graph. See BuildComfyBody for
// why this can't be a silent skip.
func setComfyImage(graph map[string]map[string]any, node, key string, img ComfyUploadedImage) error {
	inputs := comfyInputs(graph, node)
	if inputs == nil {
		return fmt.Errorf("image node %q is not in the workflow — fix the image node mapping in the config panel", node)
	}
	if !hasKey(inputs, key) {
		return fmt.Errorf("image node %q has no %q input — set the image key in the config panel", node, key)
	}
	inputs[key] = img.Ref()
	return nil
}

// parseComfyGraph decodes an API-format workflow into a node map, unwrapping a
// {"prompt": {...}} envelope if present. UseNumber keeps large seeds/ids exact
// through the round-trip (float64 would corrupt a 64-bit seed).
func parseComfyGraph(apiJSON string) (map[string]map[string]any, error) {
	apiJSON = strings.TrimSpace(apiJSON)
	if apiJSON == "" {
		return nil, fmt.Errorf("empty workflow")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(apiJSON), &top); err != nil {
		return nil, fmt.Errorf("workflow is not valid JSON: %w", err)
	}
	graphRaw := apiJSON
	if pr, ok := top["prompt"]; ok {
		graphRaw = string(pr) // already-wrapped {"prompt": {...}}
	}
	dec := json.NewDecoder(strings.NewReader(graphRaw))
	dec.UseNumber()
	var graph map[string]map[string]any
	if err := dec.Decode(&graph); err != nil {
		return nil, fmt.Errorf("workflow graph is not a node map (paste ComfyUI's “Save (API Format)” output): %w", err)
	}
	if len(graph) == 0 {
		return nil, fmt.Errorf("workflow has no nodes")
	}
	return graph, nil
}

// comfyClass returns a node's class_type.
func comfyClass(graph map[string]map[string]any, id string) string {
	if n := graph[id]; n != nil {
		if c, ok := n["class_type"].(string); ok {
			return c
		}
	}
	return ""
}

// comfyInputs returns a node's inputs map (nil if absent).
func comfyInputs(graph map[string]map[string]any, id string) map[string]any {
	if n := graph[id]; n != nil {
		if in, ok := n["inputs"].(map[string]any); ok {
			return in
		}
	}
	return nil
}

// sortedComfyNodes returns every node id in sorted order, so any list derived
// from the graph is stable across runs (Go map iteration is not).
func sortedComfyNodes(graph map[string]map[string]any) []string {
	ids := make([]string, 0, len(graph))
	for id := range graph {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// comfyFloat coerces a graph value to a float64; 0 if it can't.
func comfyFloat(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	case float64:
		return n
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f
		}
	}
	return 0
}

// findComfyNode returns the first node id (sorted for determinism) whose class
// matches pred.
func findComfyNode(graph map[string]map[string]any, pred func(class string) bool) string {
	best := ""
	for id := range graph {
		if pred(comfyClass(graph, id)) {
			if best == "" || id < best {
				best = id
			}
		}
	}
	return best
}

// comfyLinkTarget reads a ComfyUI link value ["<node_id>", <slot>] and returns
// the source node id.
func comfyLinkTarget(v any) (string, bool) {
	if arr, ok := v.([]any); ok && len(arr) >= 1 {
		return fmt.Sprint(arr[0]), true
	}
	return "", false
}

// findComfyTextNode walks up conditioning links from start until it finds a
// node we can inject a prompt into (a CLIPTextEncode variant, or any node with a
// text / text_g / text_l input). Bounded by depth to avoid cycles.
func findComfyTextNode(graph map[string]map[string]any, start string, depth int, seen map[string]bool) string {
	if start == "" || depth < 0 || seen[start] {
		return ""
	}
	seen[start] = true
	class := comfyClass(graph, start)
	in := comfyInputs(graph, start)
	if strings.Contains(class, "CLIPTextEncode") || hasComfyTextKey(in) {
		return start
	}
	// Sorted, not a map walk: two reachable text chains would otherwise resolve
	// differently from one import to the next, and "which encoder became the
	// prompt" is not something anyone would think to re-check.
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if tid, ok := comfyLinkTarget(in[k]); ok {
			if r := findComfyTextNode(graph, tid, depth-1, seen); r != "" {
				return r
			}
		}
	}
	return ""
}

// hasComfyTextKey reports whether a node exposes any prompt-carrying input. The
// value has to be a LITERAL: on these encoders `prompt` is the text, but the
// same name can arrive as a link from an upstream node, and that is wiring, not
// a field we can write into.
func hasComfyTextKey(in map[string]any) bool {
	for _, k := range comfyTextKeyNames {
		if v, ok := in[k]; ok && !comfyIsLink(v) {
			return true
		}
	}
	return false
}

// comfyIsLink reports whether a graph value is a NODE REFERENCE — ["<id>",slot]
// — rather than a literal the caller may replace.
//
// This is the guard that keeps a parameter-driving graph intact. A workflow can
// feed steps / cfg / a prompt from another node (a switch, a primitive, a
// preset chain), and writing a plain value over that link silently severs the
// wiring: ComfyUI then runs a graph the author never built, and the result
// looks like a bad render rather than a broken import.
// orderImageNodesByConsumer sorts the detected LoadImage nodes by the input
// they FEED — image1, then image2, then image3 — rather than by node id.
//
// Node id order is arbitrary relative to what a person means by "the first
// photo": ComfyUI numbers nodes by creation, so the loader wired to image2 can
// easily carry the lower id. With two sources that is a coin flip you can spot
// by eye; at three it is one you cannot, and getting it wrong does not error —
// it composites in the wrong order and returns a plausible picture of the wrong
// thing. The caller's first image goes to ImageNodes[0], so the consumer's
// order is the only one that makes "the first is the base/subject" mean
// anything.
//
// Loaders no numbered input references keep their relative id order, after the
// ranked ones: a graph without this shape behaves exactly as it did before.
func orderImageNodesByConsumer(graph map[string]map[string]any, nodes []string) []string {
	if len(nodes) < 2 {
		return nodes
	}
	isLoader := make(map[string]bool, len(nodes))
	for _, id := range nodes {
		isLoader[id] = true
	}
	rank := make(map[string]int, len(nodes))
	for _, id := range sortedComfyNodes(graph) {
		for key, val := range comfyInputs(graph, id) {
			n, ok := numberedImageInput(key)
			if !ok {
				continue
			}
			src, ok := comfyLinkSource(val)
			if !ok {
				continue
			}
			loader, ok := loaderBehind(graph, src, isLoader, 0)
			if !ok {
				continue
			}
			// Lowest wins: a loader reaching both image1 and a later slot is
			// still the first source.
			if cur, seen := rank[loader]; !seen || n < cur {
				rank[loader] = n
			}
		}
	}
	if len(rank) == 0 {
		return nodes
	}
	out := append([]string(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := rank[out[i]]
		rj, okj := rank[out[j]]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		default:
			return false
		}
	})
	return out
}

// loaderBehind walks back from a node to the LoadImage that ultimately feeds
// it, through however many intermediate nodes sit between them.
//
// A direct wire is the exception, not the rule: a real Qwen edit graph runs
// each source through a FluxKontextImageScale before the encoder, so an encoder
// input names the scaler and the scaler names the loader. Ranking only on
// direct links found nothing in exactly the workflows this ordering exists for,
// and silently fell back to node id — the behaviour being fixed.
//
// Depth-bounded and single-path: it follows the first image-shaped link it
// finds at each hop. A node that merges two sources (a real composite) has no
// single loader behind it, and guessing which one is "first" from a merge is
// the invention this function is meant to avoid.
func loaderBehind(graph map[string]map[string]any, id string, isLoader map[string]bool, depth int) (string, bool) {
	if depth > 8 || strings.TrimSpace(id) == "" {
		return "", false
	}
	if isLoader[id] {
		return id, true
	}
	inputs := comfyInputs(graph, id)
	// Deterministic hop order: a node with several image inputs must resolve the
	// same way every import.
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "image") && k != "pixels" && k != "samples" {
			continue
		}
		src, ok := comfyLinkSource(inputs[k])
		if !ok {
			continue
		}
		if loader, found := loaderBehind(graph, src, isLoader, depth+1); found {
			return loader, true
		}
	}
	return "", false
}

// numberedImageInput reads the index out of an "image1" / "image2" input name.
// Only NUMBERED names count: a plain "image" or "images" says nothing about
// order, and reading one as first would invent an ordering the graph never
// expressed.
func numberedImageInput(key string) (int, bool) {
	const prefix = "image"
	if !strings.HasPrefix(key, prefix) || len(key) == len(prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(key[len(prefix):])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// comfyLinkSource returns the node id a link points at. A link is
// ["<node id>", <slot>]; anything else is a literal value, not a wire.
func comfyLinkSource(v any) (string, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return "", false
	}
	id, ok := arr[0].(string)
	return id, ok && strings.TrimSpace(id) != ""
}

func comfyIsLink(v any) bool {
	arr, ok := v.([]any)
	return ok && len(arr) >= 1
}

// comfyInt coerces a graph value (json.Number under UseNumber, or a stray
// float64/string) to an int; 0 if it can't.
func comfyInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return 0
}

// hasKey reports whether m has key k.
func hasKey(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	_, ok := m[k]
	return ok
}
