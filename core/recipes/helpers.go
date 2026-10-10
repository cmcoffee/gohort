package recipes

// Named helpers: the one place a template reaches Go. Most integrations are
// data (a credential, api tools, a connector spec with its answers filled in),
// but a few need a genuinely smart step no placeholder can do, like reading a
// ComfyUI workflow and wiring the prompt, seed and output nodes into it. That
// step lives here once, under a name, and any template can ask for it:
//
//	{"name": "workflow", "helper": "comfyui_workflow", "with": {"base_url": "{{base_url}}"}}
//
// and use what it made as {{workflow.spec}}. A helper is reviewed Go shipped
// with oddjob; a template only names it, so importing a template never runs
// code that came with the file.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/cmcoffee/oddjob/core"
)

// Helper turns one answer (and its With inputs) into named outputs. Run
// returns each output as a JSON value, plus warnings for the person adding
// the template to read.
type Helper struct {
	Desc    string
	Outputs []string
	Run     func(answer string, with map[string]string) (map[string]any, []string, error)
}

func (h Helper) hasOutput(name string) bool {
	for _, o := range h.Outputs {
		if o == name {
			return true
		}
	}
	return false
}

var (
	helpersMu sync.RWMutex
	helpers   = map[string]Helper{}
)

// RegisterHelper adds a named helper templates can use.
func RegisterHelper(name string, h Helper) {
	helpersMu.Lock()
	defer helpersMu.Unlock()
	helpers[name] = h
}

// LookupHelper finds a helper by name.
func LookupHelper(name string) (Helper, bool) {
	helpersMu.RLock()
	defer helpersMu.RUnlock()
	h, ok := helpers[name]
	return h, ok
}

// Helpers lists the helper names, sorted.
func Helpers() []string {
	helpersMu.RLock()
	defer helpersMu.RUnlock()
	var out []string
	for n := range helpers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// asJSONValue turns a Go value into the plain JSON value a bundle holds.
func asJSONValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(raw, &out)
}

func init() {
	RegisterHelper("comfyui_workflow", Helper{
		Desc:    "Wires a ComfyUI workflow (API format) into an image connector's spec: the prompt, negative, seed and output nodes are found and mapped. A blank answer uses a basic SD1.5 graph. With: base_url (required), credential, output_node.",
		Outputs: []string{"spec"},
		Run: func(answer string, with map[string]string) (map[string]any, []string, error) {
			if with["base_url"] == "" {
				return nil, nil, fmt.Errorf("the ComfyUI address is needed to wire the workflow")
			}
			spec, warns, err := core.NewComfyImageSpec(with["base_url"], with["credential"], answer, with["output_node"])
			if err != nil {
				return nil, nil, fmt.Errorf("the workflow could not be wired: %v", err)
			}
			v, err := asJSONValue(spec)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"spec": v}, warns, nil
		},
	})
}

// The form helper runs a built-in form's own strategy, for the two forms whose
// output depends on what the user types and so cannot be written down as
// data: a REST call's arguments come from the {placeholders} in the address
// it is given, and an OpenAPI toolbox's actions come from the document pasted
// in. The form stays in Go as the editor behind Configure; this lets a
// template reach it, so adding goes through templates like everything else.
//
// With: form ("tool/<name>" or "connector/<name>", required), answer (the
// form field this question's own answer fills), name (stamped on a tool),
// and any other form field by its key. Output "piece": the tool definition
// (with its name and the form recorded, so Configure opens that form) or the
// connector's spec.
func init() {
	RegisterHelper("form", Helper{
		Desc:    "Builds a tool or connector spec with a built-in form's strategy. With: form (tool/<name> or connector/<name>), answer (the form field this answer fills), name, and any form field.",
		Outputs: []string{"piece"},
		Run: func(answer string, with map[string]string) (map[string]any, []string, error) {
			target, name, ok := strings.Cut(with["form"], "/")
			if !ok || name == "" {
				return nil, nil, fmt.Errorf("the form helper needs form as tool/<name> or connector/<name>")
			}
			tpl, found := core.GetTemplate(target, name)
			if !found {
				return nil, nil, fmt.Errorf("this oddjob has no %s form %q", target, name)
			}
			vals := map[string]any{}
			for k, v := range with {
				switch k {
				case "form", "answer", "name":
					continue
				}
				if strings.TrimSpace(v) != "" {
					vals[k] = v
				}
			}
			if f := strings.TrimSpace(with["answer"]); f != "" {
				vals[f] = answer
			}
			raw, warns, err := tpl.BuildSpec(vals)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %v", tpl.Label, err)
			}
			var piece map[string]any
			if err := json.Unmarshal(raw, &piece); err != nil {
				return nil, nil, err
			}
			if target == core.TargetTool {
				if n := strings.TrimSpace(with["name"]); n != "" {
					piece["name"] = n
				}
				piece["template"] = name
			}
			return map[string]any{"piece": piece}, warns, nil
		},
	})
}
