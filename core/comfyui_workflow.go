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

	// 2. Sampler → positive/negative conditioning → text nodes.
	sampler := findComfyNode(graph, func(class string) bool { return strings.Contains(class, "KSampler") })
	if sampler == "" {
		// Fallback: any node exposing a `positive` input behaves like a sampler.
		for id := range graph {
			if _, ok := comfyInputs(graph, id)["positive"]; ok {
				sampler = id
				break
			}
		}
	}
	if sampler == "" {
		return nil, fmt.Errorf("no KSampler (or node with a positive input) found — can't locate the prompt node")
	}
	sIn := comfyInputs(graph, sampler)

	if pid := traceComfyText(graph, sIn["positive"]); pid != "" {
		m.PromptNodes = []string{pid}
		m.TextKeys = comfyTextKeys(comfyInputs(graph, pid))
	} else {
		return nil, fmt.Errorf("couldn't trace the sampler's positive conditioning to a text node — set the prompt node in the config panel")
	}
	if nid := traceComfyText(graph, sIn["negative"]); nid != "" {
		m.NegativeNodes = []string{nid}
	} else if _, ok := sIn["negative"]; ok {
		warnings = append(warnings, "negative conditioning didn't lead to a text node; the negative prompt won't apply")
	}

	// 3. Seed + steps on the sampler.
	switch {
	case hasKey(sIn, "seed"):
		m.SeedNodes, m.SeedKey = []string{sampler}, "seed"
	case hasKey(sIn, "noise_seed"):
		m.SeedNodes, m.SeedKey = []string{sampler}, "noise_seed"
	default:
		warnings = append(warnings, "no seed input on the sampler; generated images may not vary")
	}
	if hasKey(sIn, "steps") {
		m.StepsNodes = []string{sampler}
	}

	// 4. Size: the latent node the sampler draws from (EmptyLatentImage or variant).
	latent := ""
	if lid, ok := comfyLinkTarget(sIn["latent_image"]); ok && hasKey(comfyInputs(graph, lid), "width") {
		latent = lid
	} else {
		latent = findComfyNode(graph, func(class string) bool { return strings.Contains(class, "EmptyLatent") })
	}
	if lin := comfyInputs(graph, latent); hasKey(lin, "width") && hasKey(lin, "height") {
		m.WidthNodes, m.HeightNodes = []string{latent}, []string{latent}
		if w := comfyInt(lin["width"]); w > 0 {
			s.DefaultWidth = w
		}
		if h := comfyInt(lin["height"]); h > 0 {
			s.DefaultHeight = h
		}
	} else {
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

// traceComfyText resolves a sampler conditioning link (positive/negative) to the
// text node it leads to, following indirection (ControlNetApply, etc.).
func traceComfyText(graph map[string]map[string]any, linkVal any) string {
	tid, ok := comfyLinkTarget(linkVal)
	if !ok {
		return ""
	}
	return findComfyTextNode(graph, tid, 4, map[string]bool{})
}

// comfyTextKeys returns the prompt input key(s) present on a text node: ["text"]
// for a standard CLIPTextEncode, ["text_g","text_l"] for an SDXL encoder.
func comfyTextKeys(in map[string]any) []string {
	var keys []string
	for _, k := range []string{"text", "text_g", "text_l"} {
		if hasKey(in, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		keys = []string{"text"}
	}
	return keys
}

// BuildComfyBody parses the stored workflow and injects each generation value
// into the nodes named by the map, returning the /prompt request body. This is
// the mapping-model counterpart to token substitution — the wiring lives in the
// (editable) map, not baked into the graph. seed is the already-resolved value.
func BuildComfyBody(workflow string, m ComfyNodeMap, prompt, negative string, width, height, steps, seed int) (string, error) {
	graph, err := parseComfyGraph(workflow)
	if err != nil {
		return "", fmt.Errorf("stored workflow is invalid: %w", err)
	}
	setStr := func(nodes, keys []string, val string) {
		for _, id := range nodes {
			if in := comfyInputs(graph, id); in != nil {
				for _, k := range keys {
					if hasKey(in, k) {
						in[k] = val
					}
				}
			}
		}
	}
	setNum := func(nodes []string, key string, val int) {
		for _, id := range nodes {
			if in := comfyInputs(graph, id); in != nil {
				if hasKey(in, key) {
					in[key] = val
				}
			}
		}
	}
	keys := m.TextKeys
	if len(keys) == 0 {
		keys = []string{"text"}
	}
	setStr(m.PromptNodes, keys, prompt)
	setStr(m.NegativeNodes, keys, negative)
	setNum(m.WidthNodes, "width", width)
	setNum(m.HeightNodes, "height", height)
	seedKey := m.SeedKey
	if seedKey == "" {
		seedKey = "seed"
	}
	setNum(m.SeedNodes, seedKey, seed)
	if steps > 0 {
		setNum(m.StepsNodes, "steps", steps)
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		return "", err
	}
	return `{"prompt":` + string(raw) + `}`, nil
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
	if strings.Contains(class, "CLIPTextEncode") || hasKey(in, "text") || hasKey(in, "text_g") || hasKey(in, "text_l") {
		return start
	}
	for _, v := range in {
		if tid, ok := comfyLinkTarget(v); ok {
			if r := findComfyTextNode(graph, tid, depth-1, seen); r != "" {
				return r
			}
		}
	}
	return ""
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
