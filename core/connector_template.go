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
type ConnectorTemplate struct {
	Name        string            `json:"name"` // stable id, stored on the connector for provenance
	Label       string            `json:"label"`
	Category    string            `json:"category"` // groups the Add menu / catalog
	Description string            `json:"description,omitempty"`
	Kind        string            `json:"kind"`             // base connector kind it materializes
	Strategy    string            `json:"strategy"`         // names a registered ConnectorStrategy (the code)
	Params      map[string]string `json:"params,omitempty"` // per-declaration knobs the strategy reads
	Fields      []TemplateField   `json:"fields"`           // what options are needed (the declaration)
}

// ConnectorStrategy is the CODE half — the value↔spec mapping + optional
// auto-fill, written once and shared by every declaration that names it. The
// template is passed in so the strategy can read Params.
type ConnectorStrategy struct {
	// BuildSpec maps collected field values → the connector Spec. The SAVE path
	// MERGES this onto the existing raw Spec (MergeSpec) so unknown fields from a
	// newer version survive.
	BuildSpec func(t ConnectorTemplate, vals map[string]any) (json.RawMessage, []string, error)
	// ReadValues is the inverse: prefill the Configure panel from an existing spec.
	ReadValues func(t ConnectorTemplate, spec json.RawMessage) map[string]any
	// Detect (optional) auto-fills fields from others (ComfyUI: workflow → node
	// map; later: OpenAPI spec → tool actions). nil = no Detect button.
	Detect func(t ConnectorTemplate, vals map[string]any) (map[string]any, []string, error)
}

var (
	connectorStrategies = map[string]ConnectorStrategy{}
	connectorStrategyMu sync.RWMutex
)

// RegisterConnectorStrategy installs a strategy by name. Call once at startup.
func RegisterConnectorStrategy(name string, s ConnectorStrategy) {
	connectorStrategyMu.Lock()
	connectorStrategies[name] = s
	connectorStrategyMu.Unlock()
}

// GetConnectorStrategy returns a strategy by name.
func GetConnectorStrategy(name string) (ConnectorStrategy, bool) {
	connectorStrategyMu.RLock()
	defer connectorStrategyMu.RUnlock()
	s, ok := connectorStrategies[strings.TrimSpace(name)]
	return s, ok
}

// BuildSpec resolves the template's strategy and runs it.
func (t ConnectorTemplate) BuildSpec(vals map[string]any) (json.RawMessage, []string, error) {
	s, ok := GetConnectorStrategy(t.Strategy)
	if !ok || s.BuildSpec == nil {
		return nil, nil, fmt.Errorf("template %q references unknown strategy %q", t.Name, t.Strategy)
	}
	return s.BuildSpec(t, vals)
}

// ReadValues resolves the strategy and prefills the Configure panel.
func (t ConnectorTemplate) ReadValues(spec json.RawMessage) map[string]any {
	s, ok := GetConnectorStrategy(t.Strategy)
	if !ok || s.ReadValues == nil {
		return map[string]any{}
	}
	return s.ReadValues(t, spec)
}

// Detect resolves the strategy and runs its auto-fill.
func (t ConnectorTemplate) Detect(vals map[string]any) (map[string]any, []string, error) {
	s, ok := GetConnectorStrategy(t.Strategy)
	if !ok || s.Detect == nil {
		return nil, nil, fmt.Errorf("template %q has no detect", t.Name)
	}
	return s.Detect(t, vals)
}

// HasDetect reports whether the template's strategy exposes a Detect action.
func (t ConnectorTemplate) HasDetect() bool {
	s, ok := GetConnectorStrategy(t.Strategy)
	return ok && s.Detect != nil
}

var (
	connectorTemplates  = map[string]ConnectorTemplate{}
	connectorTemplateMu sync.RWMutex
)

// RegisterConnectorTemplate installs a template. Call once at startup.
func RegisterConnectorTemplate(t ConnectorTemplate) {
	connectorTemplateMu.Lock()
	connectorTemplates[t.Name] = t
	connectorTemplateMu.Unlock()
}

// GetConnectorTemplate returns a template by name.
func GetConnectorTemplate(name string) (ConnectorTemplate, bool) {
	connectorTemplateMu.RLock()
	defer connectorTemplateMu.RUnlock()
	t, ok := connectorTemplates[strings.TrimSpace(name)]
	return t, ok
}

// ConnectorTemplates lists registered templates, sorted by category then label.
func ConnectorTemplates() []ConnectorTemplate {
	connectorTemplateMu.RLock()
	out := make([]ConnectorTemplate, 0, len(connectorTemplates))
	for _, t := range connectorTemplates {
		out = append(out, t)
	}
	connectorTemplateMu.RUnlock()
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
