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
	"encoding/json"
	"fmt"
	"strings"
)

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
			return nil, fmt.Errorf("no SaveImage node found — add one in ComfyUI, or pass the output node id explicitly")
		}
	}

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

	if !wireComfyPrompt(graph, sIn, "positive", "{prompt}") {
		return nil, fmt.Errorf("couldn't trace the sampler's positive conditioning to a text node — wire {prompt} manually via Edit spec, or let the assistant handle this graph")
	}
	if _, ok := sIn["negative"]; ok {
		if !wireComfyPrompt(graph, sIn, "negative", "{negative}") {
			warnings = append(warnings, "negative conditioning didn't lead to a text node; {negative} not wired")
		}
	}

	// 3. Seed → {seed} for image variety. Sentinel is stripped of its quotes
	//    after marshal so the token lands unquoted (a JSON number position).
	const seedSentinel = "__GOHORT_SEED__"
	switch {
	case hasKey(sIn, "seed"):
		sIn["seed"] = seedSentinel
	case hasKey(sIn, "noise_seed"):
		sIn["noise_seed"] = seedSentinel
	default:
		warnings = append(warnings, "no seed input on the sampler; generated images may not vary")
	}

	// 4. Serialize + wrap as the /prompt request body.
	marshaled, err := json.Marshal(graph)
	if err != nil {
		return nil, fmt.Errorf("re-serializing workflow: %w", err)
	}
	body := strings.ReplaceAll(string(marshaled), `"`+seedSentinel+`"`, "{seed}")
	s.SubmitBody = `{"prompt":` + body + `}`

	// 5. Poll paths for the detected output node.
	s.PollReadyPath = "{id}.outputs." + save + ".images.0.filename"
	s.PollFields = map[string]string{
		"filename":  "{id}.outputs." + save + ".images.0.filename",
		"subfolder": "{id}.outputs." + save + ".images.0.subfolder",
		"type":      "{id}.outputs." + save + ".images.0.type",
	}
	return warnings, nil
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

// wireComfyPrompt follows sampInputs[key]'s link to a text node and sets its
// prompt text to token. Returns whether it injected anything.
func wireComfyPrompt(graph map[string]map[string]any, sampInputs map[string]any, key, token string) bool {
	tid, ok := comfyLinkTarget(sampInputs[key])
	if !ok {
		return false
	}
	node := findComfyTextNode(graph, tid, 4, map[string]bool{})
	if node == "" {
		return false
	}
	return injectComfyPrompt(comfyInputs(graph, node), token)
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

// injectComfyPrompt sets the prompt token on whichever text fields the node has
// (SDXL nodes carry text_g/text_l instead of a single text). Only replaces a
// STRING value — a linked (list) text input is left alone. Returns success.
func injectComfyPrompt(in map[string]any, token string) bool {
	hit := false
	for _, k := range []string{"text", "text_g", "text_l"} {
		if v, ok := in[k]; ok {
			if _, isStr := v.(string); isStr || v == nil {
				in[k] = token
				hit = true
			}
		}
	}
	return hit
}

// hasKey reports whether m has key k.
func hasKey(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	_, ok := m[k]
	return ok
}
