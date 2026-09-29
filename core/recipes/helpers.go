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
// with gohort; a template only names it, so importing a template never runs
// code that came with the file.

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/cmcoffee/gohort/core"
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
