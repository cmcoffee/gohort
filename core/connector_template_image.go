// The image-generation strategies + declarations. Two STRATEGIES (code) —
// rest_image_preset (a generic preset-driven backend) and comfyui (the graph +
// node-map + Detect one) — plus two DECLARATIONS (data) that name them. a1111 is
// a PURE DECLARATION: it writes no code, just its options + Strategy:
// "rest_image_preset". Adding another preset-shaped backend (Flux-api, a
// DALL·E-style endpoint) is likewise one declaration. Only a genuinely new shape
// (ComfyUI) needs a strategy. See docs/templates.md.
package core

import (
	"encoding/json"
	"net/url"
	"strings"
)

func init() {
	RegisterConnectorStrategy("rest_image_preset", ConnectorStrategy{
		BuildSpec:  restImagePresetBuildSpec,
		ReadValues: restImagePresetReadValues,
	})
	RegisterConnectorStrategy("comfyui", ConnectorStrategy{
		BuildSpec:  comfyBuildSpec,
		ReadValues: comfyReadValues,
		Detect:     comfyDetect,
	})
	RegisterConnectorTemplate(comfyuiTemplate())
	RegisterConnectorTemplate(a1111Template())
	RegisterConnectorTemplate(a1111Img2ImgTemplate())
}

// --- rest_image_preset strategy (generic; reused by a1111 and future presets) --

func restImagePresetBuildSpec(t ConnectorTemplate, vals map[string]any) (json.RawMessage, []string, error) {
	cred := TemplateStr(vals, "credential")
	if cred == "" {
		cred = "no_auth"
	}
	spec, err := ApplyRestImagePreset(t.Params["preset"], RestImageSpec{Credential: cred}, map[string]string{"base_url": TemplateStr(vals, "base_url")})
	if err != nil {
		return nil, nil, err
	}
	if w := TemplateInt(vals, "default_width"); w > 0 {
		spec.DefaultWidth = w
	}
	if h := TemplateInt(vals, "default_height"); h > 0 {
		spec.DefaultHeight = h
	}
	if s := TemplateInt(vals, "default_steps"); s > 0 {
		spec.DefaultSteps = s
	}
	spec.PromptSuffix = TemplateStr(vals, "prompt_suffix")
	spec.PromptGuidance = TemplateStr(vals, "prompt_guidance")
	raw, err := json.Marshal(spec)
	return raw, nil, err
}

func restImagePresetReadValues(t ConnectorTemplate, spec json.RawMessage) map[string]any {
	var s RestImageSpec
	_ = json.Unmarshal(spec, &s)
	return map[string]any{
		"base_url":        restImagePresetBaseURL(t.Params["preset"], s.SubmitURL),
		"credential":      s.Credential,
		"default_width":   s.DefaultWidth,
		"default_height":  s.DefaultHeight,
		"default_steps":   s.DefaultSteps,
		"prompt_suffix":   s.PromptSuffix,
		"prompt_guidance": s.PromptGuidance,
	}
}

// restImagePresetBaseURL recovers base_url by stripping the preset's own endpoint
// suffix (derived by applying the preset to an empty base_url), so ReadValues
// needs no per-backend knowledge of the path.
func restImagePresetBaseURL(preset, submitURL string) string {
	probe, err := ApplyRestImagePreset(preset, RestImageSpec{}, map[string]string{"base_url": ""})
	if err != nil || probe.SubmitURL == "" {
		return submitURL
	}
	return strings.TrimSuffix(submitURL, probe.SubmitURL)
}

// --- comfyui strategy (the new shape: workflow + node map + Detect) -----------

func comfyBuildSpec(t ConnectorTemplate, vals map[string]any) (json.RawMessage, []string, error) {
	preset := t.Params["preset"]
	if preset == "" {
		preset = "comfyui"
	}
	cred := TemplateStr(vals, "credential")
	if cred == "" {
		cred = "no_auth"
	}
	spec, err := ApplyRestImagePreset(preset, RestImageSpec{Credential: cred}, map[string]string{"base_url": TemplateStr(vals, "base_url")})
	if err != nil {
		return nil, nil, err
	}
	// The workflow box wins when it has anything in it; the type picker only
	// chooses which built-in graph a BLANK box starts from. That ordering keeps
	// "paste my export" the primary path — a dropdown that could silently
	// replace a pasted graph would be worse than no dropdown.
	wf := TemplateStr(vals, "workflow")
	if wf == "" {
		wf = ComfyStarterGraph(TemplateStr(vals, "workflow_type"))
	}
	var warns []string
	// A provided node map (Configure edits, or a Detect the user ran) is used
	// as-is; a bare workflow with no map is auto-wired so Save always works.
	//
	// "Has a map" means prompt nodes OR image nodes — a blend graph has no text
	// node at all, so keying only on prompt_nodes sent every re-save of one down
	// the auto-wire path and discarded the admin's edits. Image-node ORDER is
	// exactly what gets hand-corrected on a compose backend (it decides subject
	// vs background), so that was the edit most likely to be silently undone.
	mapped := len(TemplateCSV(vals, "prompt_nodes")) > 0 || len(TemplateCSV(vals, "image_nodes")) > 0
	if TemplateStr(vals, "output_node") != "" && mapped {
		spec.ComfyWorkflow = PrettyComfyJSON(wf)
		spec.ComfyMap = comfyMapFromVals(vals)
		spec.SubmitBody, spec.PollReadyPath, spec.PollFields = "", "", nil
	} else {
		warns, err = ApplyComfyWorkflow(&spec, wf, TemplateStr(vals, "output_node"))
		if err != nil {
			return nil, warns, err
		}
	}
	if w := TemplateInt(vals, "default_width"); w > 0 {
		spec.DefaultWidth = w
	}
	if h := TemplateInt(vals, "default_height"); h > 0 {
		spec.DefaultHeight = h
	}
	if s := TemplateInt(vals, "default_steps"); s > 0 {
		spec.DefaultSteps = s
	}
	spec.PromptSuffix = TemplateStr(vals, "prompt_suffix")
	spec.PromptGuidance = TemplateStr(vals, "prompt_guidance")
	// ApplyRestImagePreset above already derived UploadURL from the CURRENT
	// base_url. Only let the submitted field override that when it is a
	// genuinely custom endpoint.
	//
	// The Configure panel prefills upload_url from the stored spec, so once it
	// held a concrete host, re-saving wrote that old host straight back over
	// the freshly-derived one — and sameImageHost then refused the save for
	// disagreeing with the new base_url. Changing the ComfyUI server became
	// impossible, from a field marked Advanced that the admin never sees.
	// Same shape as the frozen poll defaults MigrateFrozenImageDefaults
	// unfroze: preset residue stored as though it were a choice.
	if u := TemplateStr(vals, "upload_url"); u != "" && !isDerivedUploadURL(u) {
		spec.UploadURL = u
	}
	// 0 means "use the deadline tunable for this backend's kind", which is the
	// right default for almost everyone — the per-connector value is for the one
	// backend that's slower than the rest.
	spec.PollMaxSecs = TemplateInt(vals, "poll_max_secs")
	spec.PollIntervalSecs = TemplateInt(vals, "poll_interval_secs")
	raw, err := json.Marshal(spec)
	return raw, warns, err
}

// comfyNodeChoicesKey is the values-map key carrying the pasted graph's nodes
// as mapping candidates. Underscored because it is not a field: nothing renders
// or saves it, and Configure ignores it on the way back in.
const comfyNodeChoicesKey = "__nodes"

// addComfyNodeChoices attaches the graph's nodes to a values map, when there is
// a graph to read. Silent on a parse failure: the form is still usable with the
// workflow box and free-text ids, and the parse error surfaces where the admin
// acts on it (Detect / Save) rather than as a missing dropdown.
func addComfyNodeChoices(vals map[string]any, workflow string) {
	if strings.TrimSpace(workflow) == "" {
		return
	}
	if nodes, err := ComfyGraphNodes(workflow); err == nil && len(nodes) > 0 {
		vals[comfyNodeChoicesKey] = nodes
	}
}

// isDerivedUploadURL reports whether an upload endpoint is just the preset's
// default shape for some host — "<host>/upload/image", or the unsubstituted
// "{base_url}/upload/image" template. Those follow the ComfyUI URL and are
// re-derived on every build; only a different PATH is a real choice worth
// preserving. Host is deliberately ignored: a differing host is precisely the
// stale case, and sameImageHost forbids it anyway.
func isDerivedUploadURL(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "{base_url}") {
		return strings.TrimPrefix(s, "{base_url}") == "/upload/image"
	}
	u, err := url.Parse(s)
	if err != nil {
		return false // unparseable: leave it alone and let validation speak
	}
	return strings.TrimSuffix(u.Path, "/") == "/upload/image"
}

func comfyReadValues(_ ConnectorTemplate, spec json.RawMessage) map[string]any {
	var s RestImageSpec
	_ = json.Unmarshal(spec, &s)
	vals := map[string]any{
		"base_url":           strings.TrimSuffix(s.SubmitURL, "/prompt"),
		"workflow_type":      ComfyWorkflowTypeOf(s.ComfyMap),
		"workflow":           s.ComfyWorkflow,
		"credential":         s.Credential,
		"default_width":      s.DefaultWidth,
		"default_height":     s.DefaultHeight,
		"default_steps":      s.DefaultSteps,
		"prompt_suffix":      s.PromptSuffix,
		"prompt_guidance":    s.PromptGuidance,
		"upload_url":         s.UploadURL,
		"poll_max_secs":      s.PollMaxSecs,
		"poll_interval_secs": s.PollIntervalSecs,
	}
	comfyMapToVals(s.ComfyMap, vals)
	addComfyNodeChoices(vals, s.ComfyWorkflow)
	return vals
}

func comfyDetect(_ ConnectorTemplate, vals map[string]any) (map[string]any, []string, error) {
	var s RestImageSpec
	workflow := TemplateStr(vals, "workflow")
	warns, err := ApplyComfyWorkflow(&s, workflow, TemplateStr(vals, "output_node"))
	if err != nil {
		// A graph auto-wiring cannot read is not a graph nobody can use.
		//
		// Returning the error filled NOTHING: every field stayed blank, the
		// admin was told their workflow was wrong, and the only way forward was
		// to read the JSON and type node ids from memory. Every ComfyUI import
		// failure seen so far has been a DETECTION failure against a correctly
		// wired graph — a sampler behind a selector, conditioning behind a
		// guider — and each new model architecture will do it again, because
		// detection encodes what architectures looked like when it was written.
		//
		// So detection degrades to a first guess instead of a verdict: the
		// obvious nodes are filled in, the graph's own nodes come back as
		// candidates, and the failure becomes a note rather than a wall. Save
		// still validates properly — a half-mapped backend must not persist.
		out := map[string]any{}
		comfyValsFound(comfyPartialMap(workflow), out)
		addComfyNodeChoices(out, workflow)
		if len(out) == 0 {
			return nil, warns, err // nothing to offer: the JSON itself is unreadable
		}
		return out, append(warns, "couldn't wire this graph automatically ("+err.Error()+
			") — the fields below are BEST GUESSES. Check each against the workflow and pick the right node from the suggestions."), nil
	}
	out := map[string]any{
		"workflow_type":  ComfyWorkflowTypeOf(s.ComfyMap),
		"default_width":  s.DefaultWidth,
		"default_height": s.DefaultHeight,
	}
	// Only report what was actually FOUND. comfyMapToVals writes every key,
	// empty ones included, and the panel applies whatever Detect returns — so a
	// field auto-wiring has no opinion about came back as "" and wiped what the
	// admin had typed there. Steps is the case that bites: a graph can drive it
	// from a switch, or carry several nodes plausibly named "Steps", and the
	// honest answer is "no opinion", not "".
	comfyValsFound(s.ComfyMap, out)
	addComfyNodeChoices(out, workflow)
	return out, warns, nil
}

// comfyValsFound writes only the mappings that were actually FOUND. comfyMapToVals
// writes every key, empty ones included, and the panel applies whatever Detect
// returns — so a field auto-wiring has no opinion about would come back as ""
// and wipe what the admin had typed there.
func comfyValsFound(m ComfyNodeMap, out map[string]any) {
	detected := map[string]any{}
	comfyMapToVals(m, detected)
	for k, v := range detected {
		if str, ok := v.(string); ok && strings.TrimSpace(str) == "" {
			continue
		}
		out[k] = v
	}
}

// comfyPartialMap is the fallback when full auto-wiring fails: the mappings that
// can be read off a graph without understanding how it is wired.
//
// Deliberately shallow. It reports what a node IS (a SaveImage, a LoadImage, a
// node holding literal text) and never what a node MEANS in this graph, because
// the meaning is exactly what full detection failed to work out. Two text
// encoders, and the first is offered — a guess, labelled as one, next to the
// list to correct it from.
func comfyPartialMap(apiJSON string) ComfyNodeMap {
	graph, err := parseComfyGraph(apiJSON)
	if err != nil {
		return ComfyNodeMap{}
	}
	var m ComfyNodeMap
	m.OutputNode = findComfyNode(graph, func(class string) bool { return strings.Contains(class, "SaveImage") })
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
	}
	for _, id := range sortedComfyNodes(graph) {
		if in := comfyInputs(graph, id); hasComfyTextKey(in) {
			m.PromptNodes, m.TextKeys = []string{id}, comfyTextKeys(in)
			break
		}
	}
	if l := findComfyNode(graph, func(class string) bool { return strings.Contains(class, "EmptyLatent") }); l != "" {
		if in := comfyInputs(graph, l); hasKey(in, "width") && hasKey(in, "height") {
			m.WidthNodes, m.HeightNodes = []string{l}, []string{l}
		}
	}
	return m
}

// comfyMapFromVals and comfyMapToVals are two halves of one round trip. Wire
// every new field into BOTH: a field read but never written (or the reverse)
// makes Configure silently drop it on save, which is how image wiring would
// vanish the first time an admin edited an unrelated field.
func comfyMapFromVals(vals map[string]any) ComfyNodeMap {
	return ComfyNodeMap{
		PromptNodes:   TemplateCSV(vals, "prompt_nodes"),
		NegativeNodes: TemplateCSV(vals, "negative_nodes"),
		TextKeys:      TemplateCSV(vals, "text_keys"),
		WidthNodes:    TemplateCSV(vals, "width_nodes"),
		HeightNodes:   TemplateCSV(vals, "height_nodes"),
		StepsNodes:    TemplateCSV(vals, "steps_nodes"),
		SeedNodes:     TemplateCSV(vals, "seed_nodes"),
		SeedKey:       TemplateStr(vals, "seed_key"),
		OutputNode:    TemplateStr(vals, "output_node"),
		ImageNodes:    TemplateCSV(vals, "image_nodes"),
		ImageKey:      TemplateStr(vals, "image_key"),
		MaskNodes:     TemplateCSV(vals, "mask_nodes"),
	}
}

func comfyMapToVals(m ComfyNodeMap, into map[string]any) {
	into["prompt_nodes"] = JoinCSV(m.PromptNodes)
	into["negative_nodes"] = JoinCSV(m.NegativeNodes)
	into["text_keys"] = JoinCSV(m.TextKeys)
	into["width_nodes"] = JoinCSV(m.WidthNodes)
	into["height_nodes"] = JoinCSV(m.HeightNodes)
	into["steps_nodes"] = JoinCSV(m.StepsNodes)
	into["seed_nodes"] = JoinCSV(m.SeedNodes)
	into["seed_key"] = m.SeedKey
	into["output_node"] = m.OutputNode
	into["image_nodes"] = JoinCSV(m.ImageNodes)
	into["image_key"] = m.ImageKey
	into["mask_nodes"] = JoinCSV(m.MaskNodes)
}

// --- declarations (pure data) -------------------------------------------------

func comfyuiTemplate() ConnectorTemplate {
	return ConnectorTemplate{
		Name:        "comfyui",
		Label:       "ComfyUI",
		Category:    "Image generation",
		Description: "A local or self-hosted ComfyUI server. Paste your workflow; the node map is auto-detected and editable.",
		Kind:        RestImageConnectorKind,
		Strategy:    "comfyui",
		Params:      map[string]string{"preset": "comfyui"},
		Fields: []TemplateField{
			{Key: "base_url", Label: "ComfyUI URL", Type: "text", Group: "Connection", Help: "e.g. http://localhost:8188"},
			{Key: "workflow_type", Label: "What this backend does", Type: "select", Group: "Connection", Options: ComfyWorkflowTypes(), Default: ComfyTypeGenerate, Help: "generate = text to image. edit = change a photo you give it (img2img). blend = combine two photos, with no model loaded at all. This picks the STARTING graph; paste your own below to override it. One backend does one of these — add a second connector, pointed at the same ComfyUI, for another."},
			{Key: "workflow_file", Label: "Load workflow from a file", Type: "file", Group: "Connection", Accept: ".json,application/json", Into: "workflow", Help: "Pick the .json ComfyUI wrote with “Save (API Format)”. It is read here in your browser and dropped into the box below for review — nothing is sent until you Save."},
			{Key: "workflow", Label: "Workflow (ComfyUI “Save (API Format)” JSON)", Type: "textarea", Group: "Connection", Help: "Leave blank to start from the graph the type above selects. Enable Dev Mode in ComfyUI to get the API-format export. After saving, this shows the graph actually in use."},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Connection", Advanced: true, Help: "no_auth for a local LAN box; a SecureAPI credential name for a hosted/authenticated server."},
			{Key: "prompt_nodes", Label: "Prompt node(s)", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey, Help: "node id(s) the prompt is written into"},
			{Key: "negative_nodes", Label: "Negative node(s)", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey},
			{Key: "text_keys", Label: "Text input key(s)", Type: "text", Group: "Node mapping", Help: "usually \"text\"; SDXL \"text_g, text_l\""},
			{Key: "width_nodes", Label: "Width node(s)", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey},
			{Key: "height_nodes", Label: "Height node(s)", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey},
			{Key: "steps_nodes", Label: "Steps node(s)", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey},
			{Key: "seed_nodes", Label: "Seed node(s)", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey},
			{Key: "seed_key", Label: "Seed key", Type: "text", Group: "Node mapping", Help: "\"seed\" or \"noise_seed\""},
			{Key: "output_node", Label: "Output (SaveImage) node", Type: "text", Group: "Node mapping", SuggestFrom: comfyNodeChoicesKey, Help: "the image is read from this node"},
			{Key: "image_nodes", Label: "Input image node(s)", Type: "text", Group: "Image input", SuggestFrom: comfyNodeChoicesKey, Help: "LoadImage node id(s) a source photo is written into — this is what makes the backend able to EDIT a photo rather than only generate one. ORDER MATTERS for a multi-image compose: the first id gets the caller's first image."},
			{Key: "image_key", Label: "Image input key", Type: "text", Group: "Image input", Advanced: true, Help: "usually \"image\""},
			{Key: "mask_nodes", Label: "Mask node(s)", Type: "text", Group: "Image input", SuggestFrom: comfyNodeChoicesKey, Advanced: true, Help: "LoadImageMask node id(s), for inpainting a selected region"},
			{Key: "upload_url", Label: "Upload endpoint", Type: "text", Group: "Image input", Advanced: true, Help: "where source photos are POSTed before the graph runs; defaults to <ComfyUI URL>/upload/image. Must be the same host as the ComfyUI URL."},
			{Key: "default_width", Label: "Default width", Type: "number", Group: "Defaults"},
			{Key: "default_height", Label: "Default height", Type: "number", Group: "Defaults"},
			{Key: "default_steps", Label: "Default steps", Type: "number", Group: "Defaults"},
			{Key: "poll_interval_secs", Label: "Poll interval (seconds)", Type: "number", Group: "Defaults", Advanced: true, Help: "How often to ask this backend whether it has finished. Blank uses the value in Admin > Tunables > Timeouts."},
			{Key: "poll_max_secs", Label: "Render timeout (seconds)", Type: "number", Group: "Defaults", Help: "How long to wait for this backend to finish. Leave blank to use the deadline in Admin > Tunables > Timeouts (higher for backends that edit photos, since a large edit model has to load first). Raise it here only if THIS workflow is slower than the rest."},
			{Key: "prompt_suffix", Label: "Append to every prompt", Type: "textarea", Group: "House style", Help: "e.g. crisp, high-contrast, sharp typography"},
			{Key: "prompt_guidance", Label: "Prompt guidance for the model", Type: "textarea", Group: "House style", Help: "Added to the generate_image tool's description the model reads (NOT the prompt). Teach it this backend's quirks, e.g. 'put any words you want rendered as text inside \"double quotes\"'."},
		},
	}
}

func a1111Template() ConnectorTemplate {
	// Pure declaration — NO strategy code of its own; it reuses rest_image_preset.
	return ConnectorTemplate{
		Name:        "a1111",
		Label:       "Automatic1111",
		Category:    "Image generation",
		Description: "A local or self-hosted Automatic1111 (stable-diffusion-webui) server. Synchronous txt2img — no workflow needed.",
		Kind:        RestImageConnectorKind,
		Strategy:    "rest_image_preset",
		Params:      map[string]string{"preset": "a1111"},
		Fields: []TemplateField{
			{Key: "base_url", Label: "Automatic1111 URL", Type: "text", Group: "Connection", Help: "e.g. http://localhost:7860"},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Connection", Advanced: true, Help: "no_auth for a local box; a SecureAPI credential name for an authenticated server."},
			{Key: "default_width", Label: "Default width", Type: "number", Group: "Defaults"},
			{Key: "default_height", Label: "Default height", Type: "number", Group: "Defaults"},
			{Key: "default_steps", Label: "Default steps", Type: "number", Group: "Defaults"},
			{Key: "poll_interval_secs", Label: "Poll interval (seconds)", Type: "number", Group: "Defaults", Advanced: true, Help: "How often to ask this backend whether it has finished. Blank uses the value in Admin > Tunables > Timeouts."},
			{Key: "poll_max_secs", Label: "Render timeout (seconds)", Type: "number", Group: "Defaults", Help: "How long to wait for this backend to finish. Leave blank to use the deadline in Admin > Tunables > Timeouts (higher for backends that edit photos, since a large edit model has to load first). Raise it here only if THIS workflow is slower than the rest."},
			{Key: "prompt_suffix", Label: "Append to every prompt", Type: "textarea", Group: "House style", Help: "e.g. crisp, high-contrast, sharp typography"},
			{Key: "prompt_guidance", Label: "Prompt guidance for the model", Type: "textarea", Group: "House style", Help: "Added to the generate_image tool's description the model reads (NOT the prompt). Teach it this backend's quirks, e.g. 'put any words you want rendered as text inside \"double quotes\"'."},
		},
	}
}

// ConnectorTemplateRef is the provenance handle for a connector: Target is always
// "connector"; Name is its stored Connector.Template.
func ConnectorTemplateRef(c Connector) TemplateRef {
	return TemplateRef{Target: TargetConnector, Name: strings.TrimSpace(c.Template)}
}

// TemplateForConnector resolves which template owns a connector. It PREFERS the
// stored provenance (Connector.Template), falling back to shape inference for
// connectors authored before provenance: rest_image with a workflow → comfyui;
// other rest_image → a1111. Returns ("", false) when no template applies.
func TemplateForConnector(c Connector) (ConnectorTemplate, bool) {
	if tpl, ok := ConnectorTemplateRef(c).Resolve(); ok {
		return tpl, true
	}
	if c.Kind != RestImageConnectorKind {
		return ConnectorTemplate{}, false
	}
	var s RestImageSpec
	_ = json.Unmarshal(c.Spec, &s)
	name := "a1111"
	if s.ComfyWorkflow != "" {
		name = "comfyui"
	}
	return GetConnectorTemplate(name)
}

// a1111Img2ImgTemplate is a PURE DECLARATION, like a1111 — no strategy code of
// its own, just a different preset. That it takes one line to add an EDITING
// backend is the point of the split: image input is a property of the preset's
// request body, not of the machinery.
func a1111Img2ImgTemplate() ConnectorTemplate {
	return ConnectorTemplate{
		Name:        "a1111_img2img",
		Label:       "Automatic1111 (edit a photo)",
		Category:    "Image generation",
		Description: "A local or self-hosted Automatic1111, wired for img2img: it takes a source photo and a prompt and returns a changed version. Add the plain Automatic1111 template as well if you also want text-to-image.",
		Kind:        RestImageConnectorKind,
		Strategy:    "rest_image_preset",
		Params:      map[string]string{"preset": "a1111_img2img"},
		Fields: []TemplateField{
			{Key: "base_url", Label: "Automatic1111 URL", Type: "text", Group: "Connection", Help: "e.g. http://localhost:7860 — the same server as your text-to-image backend, if you have one."},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Connection", Advanced: true, Help: "no_auth for a local box; a SecureAPI credential name for an authenticated server."},
			{Key: "default_width", Label: "Default width", Type: "number", Group: "Defaults"},
			{Key: "default_height", Label: "Default height", Type: "number", Group: "Defaults"},
			{Key: "default_steps", Label: "Default steps", Type: "number", Group: "Defaults"},
			{Key: "prompt_suffix", Label: "Append to every prompt", Type: "textarea", Group: "House style", Help: "e.g. crisp, high-contrast, sharp typography"},
			{Key: "prompt_guidance", Label: "Prompt guidance for the model", Type: "textarea", Group: "House style", Help: "Added to the image tool's description the model reads (NOT the prompt). For an editing backend, say what it is good at changing."},
		},
	}
}
