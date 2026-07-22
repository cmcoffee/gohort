// Connector templates: a declarative integration built ON a connector kind, that
// owns its whole config surface — the fields, how they map to the Spec, and
// (optionally) how to auto-detect them. A GENERIC renderer builds the Add and
// Configure panels from the declaration, so a new backend is a template, not a
// hand-written admin panel.
//
// A template is NOT a new kind. Kinds (rest_image, rest_messaging) are the runtime
// mechanism (a ConnectorHandler); templates are the authored, user-facing
// integration with its config surface. Many templates ride one kind (comfyui,
// a1111, flux are all rest_image). See docs/connector-templates.md.
package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// TemplateField is one config input the renderer shows. Types are deliberately
// small (text | textarea | number | bool | select | credential); richer surfaces
// like ComfyUI's node map are just a GROUP of text fields auto-filled by Detect,
// not a special widget.
type TemplateField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Type     string   `json:"type"`  // text | textarea | number | bool | select | credential
	Group    string   `json:"group"` // section heading in the panel
	Options  []string `json:"options,omitempty"`
	Default  any      `json:"default,omitempty"`
	Advanced bool     `json:"advanced,omitempty"`
}

// ConnectorTemplate is a DECLARATION — pure data (no funcs), so it can be a Go
// value today and a DB record / shared bundle later with no engine change. The
// logic lives in a named ConnectorStrategy it points at; Params carry the few
// per-declaration knobs the strategy reads (e.g. which rest_image preset). A new
// INSTANCE of a known shape is just a declaration; only a new SHAPE needs a
// strategy. See docs/templates.md.
type Template struct {
	Name        string            `json:"name"` // stable id (per target); stored on the connector for provenance
	Label       string            `json:"label"`
	Category    string            `json:"category"` // groups the Add menu / catalog
	Description string            `json:"description,omitempty"`
	Target      string            `json:"target,omitempty"` // "connector" | "tool"; empty = connector
	Kind        string            `json:"kind"`             // connector kind (connector target); tool mode/type (tool target)
	Strategy    string            `json:"strategy"`         // names a registered Strategy (the code) for this Target
	Params      map[string]string `json:"params,omitempty"` // per-declaration knobs the strategy reads
	Fields      []TemplateField   `json:"fields"`           // what options are needed (the declaration)
}

// ConnectorTemplate is the back-compat alias — connector templates are just
// Template values with Target "connector".
type ConnectorTemplate = Template

// Target constants.
const (
	TargetConnector = "connector"
	TargetTool      = "tool"
)

func (t Template) target() string {
	if t.Target == "" {
		return TargetConnector
	}
	return t.Target
}

// Strategy is the CODE half — the value↔artifact mapping + optional auto-fill,
// written once and shared by every declaration (of a target) that names it. The
// template is passed in so the strategy can read Params. For a connector target
// the artifact is a connector Spec; for a tool target it's a tool definition.
type Strategy struct {
	BuildSpec  func(t Template, vals map[string]any) (json.RawMessage, []string, error)
	ReadValues func(t Template, artifact json.RawMessage) map[string]any
	// Detect (optional) auto-fills fields from others (ComfyUI: workflow → node
	// map; later: OpenAPI spec → tool actions). nil = no Detect button.
	Detect func(t Template, vals map[string]any) (map[string]any, []string, error)
}

// ConnectorStrategy is the back-compat alias.
type ConnectorStrategy = Strategy

// --- strategy registry (keyed by target + name) ------------------------------

var (
	templateStrategies = map[string]Strategy{}
	templateStrategyMu sync.RWMutex
)

func strategyKey(target, name string) string {
	if target == "" {
		target = TargetConnector
	}
	return target + "/" + strings.TrimSpace(name)
}

// RegisterStrategy installs a strategy for a target by name.
func RegisterStrategy(target, name string, s Strategy) {
	templateStrategyMu.Lock()
	templateStrategies[strategyKey(target, name)] = s
	templateStrategyMu.Unlock()
}

// GetStrategy returns a target's strategy by name.
func GetStrategy(target, name string) (Strategy, bool) {
	templateStrategyMu.RLock()
	defer templateStrategyMu.RUnlock()
	s, ok := templateStrategies[strategyKey(target, name)]
	return s, ok
}

// RegisterConnectorStrategy / GetConnectorStrategy — connector-target wrappers.
func RegisterConnectorStrategy(name string, s Strategy) { RegisterStrategy(TargetConnector, name, s) }
func GetConnectorStrategy(name string) (Strategy, bool) { return GetStrategy(TargetConnector, name) }

// BuildSpec resolves the template's strategy (for its target) and runs it.
func (t Template) BuildSpec(vals map[string]any) (json.RawMessage, []string, error) {
	s, ok := GetStrategy(t.target(), t.Strategy)
	if !ok || s.BuildSpec == nil {
		return nil, nil, fmt.Errorf("template %q references unknown %s strategy %q", t.Name, t.target(), t.Strategy)
	}
	return s.BuildSpec(t, vals)
}

// ReadValues resolves the strategy and prefills the Configure panel.
func (t Template) ReadValues(artifact json.RawMessage) map[string]any {
	s, ok := GetStrategy(t.target(), t.Strategy)
	if !ok || s.ReadValues == nil {
		return map[string]any{}
	}
	return s.ReadValues(t, artifact)
}

// Detect resolves the strategy and runs its auto-fill.
func (t Template) Detect(vals map[string]any) (map[string]any, []string, error) {
	s, ok := GetStrategy(t.target(), t.Strategy)
	if !ok || s.Detect == nil {
		return nil, nil, fmt.Errorf("template %q has no detect", t.Name)
	}
	return s.Detect(t, vals)
}

// HasDetect reports whether the template's strategy exposes a Detect action.
func (t Template) HasDetect() bool {
	s, ok := GetStrategy(t.target(), t.Strategy)
	return ok && s.Detect != nil
}

// --- template registry (keyed by target + name) ------------------------------

var (
	allTemplates  = map[string]Template{}
	allTemplateMu sync.RWMutex
)

// RegisterTemplate installs a declaration (any target). Call once at startup.
func RegisterTemplate(t Template) {
	if t.Target == "" {
		t.Target = TargetConnector
	}
	allTemplateMu.Lock()
	allTemplates[strategyKey(t.Target, t.Name)] = t
	allTemplateMu.Unlock()
}

// GetTemplate returns a target's template by name.
func GetTemplate(target, name string) (Template, bool) {
	allTemplateMu.RLock()
	defer allTemplateMu.RUnlock()
	t, ok := allTemplates[strategyKey(target, name)]
	return t, ok
}

// Templates lists a target's templates, sorted by category then label.
func Templates(target string) []Template {
	if target == "" {
		target = TargetConnector
	}
	allTemplateMu.RLock()
	out := make([]Template, 0, len(allTemplates))
	for _, t := range allTemplates {
		if t.target() == target {
			out = append(out, t)
		}
	}
	allTemplateMu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// RegisterConnectorTemplate / GetConnectorTemplate / ConnectorTemplates —
// connector-target wrappers so existing call sites are unchanged.
func RegisterConnectorTemplate(t Template)              { t.Target = TargetConnector; RegisterTemplate(t) }
func GetConnectorTemplate(name string) (Template, bool) { return GetTemplate(TargetConnector, name) }
func ConnectorTemplates() []Template                    { return Templates(TargetConnector) }

// TemplateRef is the uniform provenance handle for an installed extension: the
// Target + Name of the template that authored an artifact. The Name is STORED on
// the artifact (Connector.Template / TempTool.Template); the Target is fixed by
// which registry the artifact belongs to, so it need not be stored redundantly.
// Resolving through this one handle is what lets a connector and a tool be
// re-opened in the SAME generic template form regardless of where each landed.
// Empty Name = hand-authored (no template).
type TemplateRef struct {
	Target string `json:"target"`
	Name   string `json:"name"`
}

// Resolve returns the referenced template, or ok=false when the ref is empty or
// its template isn't registered in this deployment (e.g. an imported artifact
// whose authoring template this build doesn't ship).
func (r TemplateRef) Resolve() (Template, bool) {
	if strings.TrimSpace(r.Name) == "" {
		return Template{}, false
	}
	return GetTemplate(r.Target, r.Name)
}

// AllTemplates lists every target's templates — the umbrella "Extensions" view —
// sorted by Category then Label, with each row's Target intact so a catalog can
// facet on it (Connector vs Tool). It's a read over the same registry, NOT a new
// one: the domains stay split by Target; only discovery is unified.
func AllTemplates() []Template {
	allTemplateMu.RLock()
	out := make([]Template, 0, len(allTemplates))
	for _, t := range allTemplates {
		if t.Target == "" {
			t.Target = TargetConnector
		}
		out = append(out, t)
	}
	allTemplateMu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// MergeSpec overlays `updates` onto `existing` at the JSON-object level, so keys
// present only in `existing` — including fields a newer gohort added that this
// version doesn't know — survive the round-trip (forward compatibility). Values in
// `updates` win. Falls back to `updates` if either side isn't a JSON object.
func MergeSpec(existing, updates json.RawMessage) json.RawMessage {
	var um map[string]json.RawMessage
	if json.Unmarshal(updates, &um) != nil {
		return updates
	}
	var em map[string]json.RawMessage
	if len(existing) == 0 || json.Unmarshal(existing, &em) != nil {
		em = map[string]json.RawMessage{}
	}
	for k, v := range um {
		em[k] = v
	}
	out, err := json.Marshal(em)
	if err != nil {
		return updates
	}
	return out
}

// --- value coercion helpers (the values map comes from JSON, so numbers arrive as
// float64 / json.Number and everything may be absent) ------------------------

// TemplateStr reads a string field value (trimmed).
func TemplateStr(vals map[string]any, key string) string {
	switch v := vals[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// TemplateInt reads a numeric field value, 0 when absent/unparseable.
func TemplateInt(vals map[string]any, key string) int {
	switch v := vals[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return 0
}

// TemplateBool reads a bool field value.
func TemplateBool(vals map[string]any, key string) bool {
	switch v := vals[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

// TemplateCSV splits a comma-separated field value into trimmed, non-empty parts.
func TemplateCSV(vals map[string]any, key string) []string {
	var out []string
	for _, p := range strings.Split(TemplateStr(vals, key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinCSV renders a node-id list as the comma-separated form the fields use.
func JoinCSV(a []string) string { return strings.Join(a, ", ") }
