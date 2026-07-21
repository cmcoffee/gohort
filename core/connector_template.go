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

// ConnectorTemplate is the declaration. BuildSpec/ReadValues are the value↔spec
// mapping ("how to map the values"); Detect is the optional auto-fill.
type ConnectorTemplate struct {
	Name        string // stable id, stored on the connector for provenance (Stage 2)
	Label       string
	Category    string // groups the Add menu / catalog (e.g. "Image generation")
	Description string
	Kind        string // base connector kind it materializes

	Fields []TemplateField

	// BuildSpec maps collected field values → the connector Spec. Returns non-fatal
	// warnings + a fatal error. The SAVE path MERGES this onto the existing raw Spec
	// (MergeSpec) so unknown fields from a newer version survive — see the doc.
	BuildSpec func(vals map[string]any) (json.RawMessage, []string, error)
	// ReadValues is the inverse: prefill the Configure panel from an existing spec.
	ReadValues func(spec json.RawMessage) map[string]any
	// Detect (optional) auto-fills fields from others (ComfyUI: parse workflow →
	// node map). nil = no Detect button.
	Detect func(vals map[string]any) (map[string]any, []string, error)
}

// HasDetect reports whether the template exposes a Detect action.
func (t ConnectorTemplate) HasDetect() bool { return t.Detect != nil }

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
