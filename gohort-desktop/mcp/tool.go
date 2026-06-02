package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// mcpTool adapts one MCP server tool to the core.Tool interface, so it
// registers, announces, and invokes exactly like a native local tool.
type mcpTool struct {
	srv      *server
	rawName  string // MCP tool name, used in tools/call
	name     string // namespaced "<server>.<tool>" for the registry + approval UI
	desc     string
	params   map[string]core.ToolParam
	required []string
}

func (t *mcpTool) Name() string                      { return t.name }
func (t *mcpTool) Desc() string                      { return t.desc }
func (t *mcpTool) Params() map[string]core.ToolParam { return t.params }
func (t *mcpTool) Required() []string                { return t.required }
func (t *mcpTool) Enabled() bool                     { return t.srv.alive() } // drops from the catalog if the server dies
func (t *mcpTool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		return t.srv.callTool(t.rawName, args)
	}
}

// newTool wraps a tools/list entry. The name is namespaced by server so
// servers can't collide and the approval dialog reads clearly
// ("Allow tool: github.create_issue?").
func newTool(srv *server, def toolDef) *mcpTool {
	params, required := mapSchema(def.InputSchema)
	desc := def.Description
	if desc == "" {
		desc = def.Name
	}
	return &mcpTool{
		srv:      srv,
		rawName:  def.Name,
		name:     srv.name + "." + def.Name,
		desc:     desc,
		params:   params,
		required: required,
	}
}

// mapSchema flattens an MCP JSON-Schema inputSchema into the flat
// per-property shape core.Tool uses. Nested object/array properties keep
// their top-level type and get a compact JSON of their sub-schema folded
// into the description (core.ToolParam can't nest), so the model still
// sees the expected shape.
func mapSchema(schema map[string]any) (map[string]core.ToolParam, []string) {
	out := map[string]core.ToolParam{}
	if schema == nil {
		return out, nil
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		p, _ := raw.(map[string]any)
		typ, _ := p["type"].(string)
		if typ == "" {
			typ = "string"
		}
		desc, _ := p["description"].(string)
		if typ == "object" || typ == "array" {
			if shape := compactSchema(p); shape != "" {
				if desc != "" {
					desc += " "
				}
				desc += "(shape: " + shape + ")"
			}
		}
		out[name] = core.ToolParam{Type: typ, Description: desc}
	}
	var required []string
	if reqs, ok := schema["required"].([]any); ok {
		for _, r := range reqs {
			if s, ok := r.(string); ok {
				required = append(required, s)
			}
		}
	}
	return out, required
}

// compactSchema renders a sub-schema as compact JSON for the param
// description. Best-effort; empty on failure or if oversized.
func compactSchema(p map[string]any) string {
	b, err := json.Marshal(p)
	if err != nil || len(b) > 400 {
		return ""
	}
	return string(b)
}

// callResult mirrors the MCP tools/call result.
type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// extractContent concatenates the text blocks of a tools/call result.
// Non-text blocks (image/resource) are noted as placeholders for now —
// the bridge's existing attach path could carry them later.
func extractContent(raw json.RawMessage) (string, error) {
	var r callResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
			continue
		}
		b.WriteString("[" + c.Type + " content omitted]")
	}
	if r.IsError {
		return "", fmt.Errorf("tool reported error: %s", b.String())
	}
	return b.String(), nil
}
