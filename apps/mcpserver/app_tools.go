package mcpserver

// The key owner's own apps, over MCP: each action, data source or pipeline an
// app marks expose (apps/customapps/exposed_tools.go) is a tool here too, so
// an MCP client can use the app with no page open. A call is the owner using
// the app, as a click would be.
//
// An action that asks to confirm before it runs is not offered: nobody here
// is asked over MCP, and running it without asking would skip the question
// the app put on it. The key's own scope narrows these like any tool.
//
// A key may be limited to apps (AppToolsTarget targets): it then reaches
// those apps' tools and no other app's, and a key that names apps and no
// agent reaches nothing else at all, no agent and no built-in tool. A key
// that names no app reaches every app its owner has, as keys always did.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
)

// offeringApps is customapps.OfferingApps, a variable so tests can stand in.
var offeringApps = customapps.OfferingApps

// keyApps are the apps a key is limited to, by slug: nil for a key that
// names none.
func keyApps(tok *AccountToken) []string {
	if tok == nil || tok.Scope == nil {
		return nil
	}
	var out []string
	for _, t := range tok.Scope.Targets {
		if slug := strings.TrimPrefix(strings.TrimSpace(t), AppToolsTarget); slug != strings.TrimSpace(t) && slug != "" {
			out = append(out, slug)
		}
	}
	return out
}

// appOnlyKey reports a key made for apps: it names apps and no agent, so it
// reaches their tools and nothing else here.
func appOnlyKey(tok *AccountToken) bool {
	if len(keyApps(tok)) == 0 {
		return false
	}
	for _, t := range tok.Scope.Targets {
		if strings.HasPrefix(strings.TrimSpace(t), "agent:") {
			return false
		}
	}
	return true
}

// keyReachesApp reports whether tok may use the app slug: it names that app,
// or names none.
func keyReachesApp(tok *AccountToken, slug string) bool {
	apps := keyApps(tok)
	if apps == nil {
		return true
	}
	for _, a := range apps {
		if strings.EqualFold(a, slug) {
			return true
		}
	}
	return false
}

// reachedApps are the owner's offering apps tok reaches; only, when set, is
// the one app asked for.
func reachedApps(tok *AccountToken, only string) []customapps.OfferingApp {
	if tok == nil || tok.Owner == "" {
		return nil
	}
	var out []customapps.OfferingApp
	for _, a := range offeringApps(tok.Owner) {
		if !keyReachesApp(tok, a.Slug) || (only != "" && !strings.EqualFold(a.Slug, only)) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// appToolDefs are the app tools tok reaches, as tools/list entries; only,
// when set, is the one app asked for.
func appToolDefs(tok *AccountToken, only string) []map[string]any {
	var out []map[string]any
	for _, a := range reachedApps(tok, only) {
		for _, d := range a.Tools {
			if d.Confirmation != nil {
				continue
			}
			out = append(out, map[string]any{
				"name":        d.Tool.Name,
				"description": appToolDescription(d),
				"inputSchema": appToolSchema(d),
			})
		}
	}
	return out
}

// callAppTool runs name when it is one of the app tools tok reaches (only,
// when set, the one app asked for); handled is false when it is not one, so
// the caller goes on to say what is here.
func callAppTool(ctx context.Context, tok *AccountToken, only, name string, args map[string]any) (text string, handled bool, err error) {
	for _, a := range reachedApps(tok, only) {
		for _, d := range a.Tools {
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
	}
	return "", false, nil
}

// withAppTools is a tools/list for tok on the general endpoint: defs (the
// built-in and registered tools) with the app tools tok reaches added, or
// those app tools alone for a key made for apps.
func withAppTools(defs []map[string]any, tok *AccountToken) []map[string]any {
	if appOnlyKey(tok) {
		defs = nil
	}
	return append(defs, appToolDefs(tok, "")...)
}

// appOnlyRefusal refuses a call a key made for apps may not make: anything
// but one of its apps' tools.
func appOnlyRefusal(tok *AccountToken, name string) error {
	if !appOnlyKey(tok) {
		return nil
	}
	if findAppTool(tok, name) {
		return nil
	}
	return fmt.Errorf("this key is for %s only and reaches nothing else here: %q is not one of its tools", strings.Join(keyApps(tok), ", "), name)
}

// findAppTool reports whether name is one of the app tools tok reaches.
func findAppTool(tok *AccountToken, name string) bool {
	for _, a := range reachedApps(tok, "") {
		for _, d := range a.Tools {
			if d.Tool.Name == name {
				return true
			}
		}
	}
	return false
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
