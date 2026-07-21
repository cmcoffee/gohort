// The comfyui + a1111 connector templates — the first two, both on the rest_image
// kind. They prove the template shape: comfyui carries the hard case (a node map
// auto-filled by Detect); a1111 falls out as a handful of fields. Adding Flux or a
// hosted SD endpoint later is another declaration here, no new admin panel.
package core

import (
	"encoding/json"
	"strings"
)

func init() {
	RegisterConnectorTemplate(comfyuiTemplate())
	RegisterConnectorTemplate(a1111Template())
}

// --- ComfyUI -----------------------------------------------------------------

func comfyuiTemplate() ConnectorTemplate {
	return ConnectorTemplate{
		Name:        "comfyui",
		Label:       "ComfyUI",
		Category:    "Image generation",
		Description: "A local or self-hosted ComfyUI server. Paste your workflow; the node map is auto-detected and editable.",
		Kind:        RestImageConnectorKind,
		Fields: []TemplateField{
			{Key: "base_url", Label: "ComfyUI URL", Type: "text", Group: "Connection", Help: "e.g. http://localhost:8188"},
			{Key: "workflow", Label: "Workflow (ComfyUI “Save (API Format)” JSON)", Type: "textarea", Group: "Connection", Help: "Leave blank for a default SD1.5 graph. Enable Dev Mode in ComfyUI to get the API-format export."},
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
			{Key: "default_width", Label: "Default width", Type: "number", Group: "Defaults"},
			{Key: "default_height", Label: "Default height", Type: "number", Group: "Defaults"},
			{Key: "default_steps", Label: "Default steps", Type: "number", Group: "Defaults"},
			{Key: "prompt_suffix", Label: "Append to every prompt", Type: "textarea", Group: "House style", Help: "e.g. crisp, high-contrast, sharp typography"},
		},
		BuildSpec:  comfyBuildSpec,
		ReadValues: comfyReadValues,
		Detect:     comfyDetect,
	}
}

func comfyBuildSpec(vals map[string]any) (json.RawMessage, []string, error) {
	cred := TemplateStr(vals, "credential")
	if cred == "" {
		cred = "no_auth"
	}
	spec, err := ApplyRestImagePreset("comfyui", RestImageSpec{Credential: cred}, map[string]string{"base_url": TemplateStr(vals, "base_url")})
	if err != nil {
		return nil, nil, err
	}
	wf := TemplateStr(vals, "workflow")
	if wf == "" {
		wf = comfyDefaultGraph
	}
	var warns []string
	// A provided node map (Configure edits, or a Detect the user ran) is used
	// as-is; a bare workflow with no map is auto-wired so Save always works.
	if TemplateStr(vals, "output_node") != "" && len(TemplateCSV(vals, "prompt_nodes")) > 0 {
		spec.ComfyWorkflow = PrettyComfyJSON(wf)
		spec.ComfyMap = comfyMapFromVals(vals)
		spec.SubmitBody, spec.PollReadyPath, spec.PollFields = "", "", nil
	} else {
		warns, err = ApplyComfyWorkflow(&spec, wf, TemplateStr(vals, "output_node"))
		if err != nil {
			return nil, warns, err
		}
	}
	// Defaults + suffix (an explicit value overrides what auto-wire captured).
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
	raw, err := json.Marshal(spec)
	return raw, warns, err
}

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
}

func comfyReadValues(spec json.RawMessage) map[string]any {
	var s RestImageSpec
	_ = json.Unmarshal(spec, &s)
	vals := map[string]any{
		"base_url":       strings.TrimSuffix(s.SubmitURL, "/prompt"),
		"workflow":       s.ComfyWorkflow,
		"credential":     s.Credential,
		"default_width":  s.DefaultWidth,
		"default_height": s.DefaultHeight,
		"default_steps":  s.DefaultSteps,
		"prompt_suffix":  s.PromptSuffix,
	}
	comfyMapToVals(s.ComfyMap, vals)
	return vals
}

func comfyDetect(vals map[string]any) (map[string]any, []string, error) {
	var s RestImageSpec
	warns, err := ApplyComfyWorkflow(&s, TemplateStr(vals, "workflow"), TemplateStr(vals, "output_node"))
	if err != nil {
		return nil, warns, err
	}
	out := map[string]any{
		"default_width":  s.DefaultWidth,
		"default_height": s.DefaultHeight,
	}
	comfyMapToVals(s.ComfyMap, out)
	return out, warns, nil
}

// --- Automatic1111 -----------------------------------------------------------

func a1111Template() ConnectorTemplate {
	return ConnectorTemplate{
		Name:        "a1111",
		Label:       "Automatic1111",
		Category:    "Image generation",
		Description: "A local or self-hosted Automatic1111 (stable-diffusion-webui) server. Synchronous txt2img — no workflow needed.",
		Kind:        RestImageConnectorKind,
		Fields: []TemplateField{
			{Key: "base_url", Label: "Automatic1111 URL", Type: "text", Group: "Connection", Help: "e.g. http://localhost:7860"},
			{Key: "credential", Label: "Credential", Type: "credential", Group: "Connection", Advanced: true, Help: "no_auth for a local box; a SecureAPI credential name for an authenticated server."},
			{Key: "default_width", Label: "Default width", Type: "number", Group: "Defaults"},
			{Key: "default_height", Label: "Default height", Type: "number", Group: "Defaults"},
			{Key: "default_steps", Label: "Default steps", Type: "number", Group: "Defaults"},
			{Key: "prompt_suffix", Label: "Append to every prompt", Type: "textarea", Group: "House style", Help: "e.g. crisp, high-contrast, sharp typography"},
		},
		BuildSpec:  a1111BuildSpec,
		ReadValues: a1111ReadValues,
	}
}

func a1111BuildSpec(vals map[string]any) (json.RawMessage, []string, error) {
	cred := TemplateStr(vals, "credential")
	if cred == "" {
		cred = "no_auth"
	}
	spec, err := ApplyRestImagePreset("a1111", RestImageSpec{Credential: cred}, map[string]string{"base_url": TemplateStr(vals, "base_url")})
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
	raw, err := json.Marshal(spec)
	return raw, nil, err
}

func a1111ReadValues(spec json.RawMessage) map[string]any {
	var s RestImageSpec
	_ = json.Unmarshal(spec, &s)
	return map[string]any{
		"base_url":       strings.TrimSuffix(s.SubmitURL, "/sdapi/v1/txt2img"),
		"credential":     s.Credential,
		"default_width":  s.DefaultWidth,
		"default_height": s.DefaultHeight,
		"default_steps":  s.DefaultSteps,
		"prompt_suffix":  s.PromptSuffix,
	}
}

// TemplateForConnector infers which template owns a connector, until Stage 2 adds
// explicit provenance. rest_image with a workflow → comfyui; other rest_image →
// a1111. Returns ("", false) when no template applies.
func TemplateForConnector(c Connector) (ConnectorTemplate, bool) {
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
