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
	if u := TemplateStr(vals, "upload_url"); u != "" {
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
	return vals
}

func comfyDetect(_ ConnectorTemplate, vals map[string]any) (map[string]any, []string, error) {
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, TemplateStr(vals, "workflow"), TemplateStr(vals, "output_node"))
	if err != nil {
		return nil, warns, err
	}
	out := map[string]any{
		"workflow_type":  ComfyWorkflowTypeOf(s.ComfyMap),
		"default_width":  s.DefaultWidth,
		"default_height": s.DefaultHeight,
	}
	comfyMapToVals(s.ComfyMap, out)
	return out, warns, nil
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
			{Key: "workflow", Label: "Workflow (ComfyUI “Save (API Format)” JSON)", Type: "textarea", Group: "Connection", Help: "Leave blank to start from the graph the type above selects. Enable Dev Mode in ComfyUI to get the API-format export. After saving, this shows the graph actually in use."},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Connection", Advanced: true, Help: "no_auth for a local LAN box; a SecureAPI credential name for a hosted/authenticated server."},
			{Key: "prompt_nodes", Label: "Prompt node(s)", Type: "text", Group: "Node mapping", Help: "node id(s) the prompt is written into"},
			{Key: "negative_nodes", Label: "Negative node(s)", Type: "text", Group: "Node mapping"},
			{Key: "text_keys", Label: "Text input key(s)", Type: "text", Group: "Node mapping", Help: "usually \"text\"; SDXL \"text_g, text_l\""},
			{Key: "width_nodes", Label: "Width node(s)", Type: "text", Group: "Node mapping"},
			{Key: "height_nodes", Label: "Height node(s)", Type: "text", Group: "Node mapping"},
			{Key: "steps_nodes", Label: "Steps node(s)", Type: "text", Group: "Node mapping"},
			{Key: "seed_nodes", Label: "Seed node(s)", Type: "text", Group: "Node mapping"},
			{Key: "seed_key", Label: "Seed key", Type: "text", Group: "Node mapping", Help: "\"seed\" or \"noise_seed\""},
			{Key: "output_node", Label: "Output (SaveImage) node", Type: "text", Group: "Node mapping", Help: "the image is read from this node"},
			{Key: "image_nodes", Label: "Input image node(s)", Type: "text", Group: "Image input", Help: "LoadImage node id(s) a source photo is written into — this is what makes the backend able to EDIT a photo rather than only generate one. ORDER MATTERS for a multi-image compose: the first id gets the caller's first image."},
			{Key: "image_key", Label: "Image input key", Type: "text", Group: "Image input", Advanced: true, Help: "usually \"image\""},
			{Key: "mask_nodes", Label: "Mask node(s)", Type: "text", Group: "Image input", Advanced: true, Help: "LoadImageMask node id(s), for inpainting a selected region"},
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
