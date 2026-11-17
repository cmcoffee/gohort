package mcpserver

// The key owner's own apps, over MCP: each action, data source or pipeline an
// app marks expose (core.AppToolsFor, apps/customapps/exposed_tools.go) is a
// tool here too, so an MCP client can use the app with no page open. A call
// is the owner using the app, as a click would be.
//
// An action that asks to confirm before it runs is not offered: nobody here
// is asked over MCP, and running it without asking would skip the question
// the app put on it. The key's own scope narrows these like any tool.

import (
	"context"
	"fmt"
	"sort"

	. "github.com/cmcoffee/gohort/core"
)

// appToolDefs are owner's exposed app tools, as tools/list entries.
func appToolDefs(owner string) []map[string]any {
	if AppToolsFor == nil || owner == "" {
		return nil
	}
	var out []map[string]any
	for _, d := range AppToolsFor(nil, owner) {
		if d.Confirmation != nil {
			continue
		}
		out = append(out, map[string]any{
			"name":        d.Tool.Name,
			"description": appToolDescription(d),
			"inputSchema": appToolSchema(d),
		})
	}
	return out
}

// callAppTool runs name when it is one of owner's exposed app tools; handled
// is false when it is not one, so the caller goes on to say what is here.
func callAppTool(ctx context.Context, owner, name string, args map[string]any) (text string, handled bool, err error) {
	if AppToolsFor == nil || owner == "" {
		return "", false, nil
	}
	for _, d := range AppToolsFor(nil, owner) {
		if d.Tool.Name != name {
			continue
		}
		if d.Confirmation != nil {
			return "", true, fmt.Errorf("%q asks to confirm before it runs (%q), and nobody is asked over MCP: use it from the app's page or from one of your agents in gohort", name, d.Confirmation.Prompt)
		}
		if args == nil {
			args = map[string]any{}
		}
		text, err := d.Handler(ctx, args)
		return text, true, err
	}
	return "", false, nil
}

// appToolDescription is a tool's description led by its app's name, which an
// MCP client lists beside every other server's tools.
func appToolDescription(d AgentToolDef) string {
	if d.Tool.Category == "" {
		return d.Tool.Description
	}
	return d.Tool.Category + ". " + d.Tool.Description
}

// appToolSchema is a tool's parameters as the JSON Schema MCP takes. An app
// tool's inputs are strings, each described by its app.
func appToolSchema(d AgentToolDef) map[string]any {
	props := map[string]any{}
	names := make([]string, 0, len(d.Tool.Parameters))
	for n := range d.Tool.Parameters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := d.Tool.Parameters[n]
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		props[n] = map[string]any{"type": typ, "description": p.Description}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(d.Tool.Required) > 0 {
		schema["required"] = d.Tool.Required
	}
	return schema
}
