// Grouped tool helper. Wraps several related operations into a single
// LLM-facing tool with an "action" discriminator + per-action handlers
// + lazy-loaded help docs. Cuts catalog noise — instead of registering
// list_X, create_X, delete_X separately (each with its own description
// pulling prompt budget every round), there's just one X tool whose
// brief catalog description points the LLM at action="help" for the
// full usage spec.
//
// Usage:
//
//   gt := NewGroupedTool("tool_def", "Manage runtime-defined tools.")
//   gt.AddAction("list", &GroupedToolAction{
//       Description: "List all session + persistent tools.",
//       Params:      map[string]ToolParam{},
//       Handler:     func(args map[string]any, sess *ToolSession) (string, error) { ... },
//   })
//   gt.AddAction("create", &GroupedToolAction{
//       Description: "Define a new tool.",
//       Params:      map[string]ToolParam{...},
//       Required:    []string{"name", "description"},
//       Handler:     func(args, sess) (string, error) { ... },
//   })
//   RegisterChatTool(gt)
//
// The catalog description is auto-built; help text is auto-generated
// from the action list + per-action params. Caps default to the union
// of all action caps (most permissive). NeedsConfirm defaults to true
// when ANY action declares it.

package core

import (
	"fmt"
	"sort"
	"strings"
)

// GroupedToolAction is one sub-action of a GroupedTool. The Handler
// receives the full args map (not just this action's params) so it
// can pick fields freely; required-param validation runs before the
// handler is called.
type GroupedToolAction struct {
	Description  string
	Params       map[string]ToolParam
	Required     []string
	Caps         []Capability
	NeedsConfirm bool
	Handler      func(args map[string]any, sess *ToolSession) (string, error)
}

// GroupedTool implements ChatTool + SessionChatTool. Build via
// NewGroupedTool + AddAction; register normally via RegisterChatTool.
type GroupedTool struct {
	name     string
	brief    string
	preamble string
	actions  map[string]*GroupedToolAction
}

// SetHelpPreamble attaches a rich text block that is prepended to the
// auto-generated action listing in formatHelp. Use it when the tool
// has cross-action concepts (workspace flows, decision matrices,
// common pitfalls) that don't fit in any single action description.
// Empty by default.
func (g *GroupedTool) SetHelpPreamble(text string) {
	g.preamble = text
}

// NewGroupedTool creates a new grouped tool with the given catalog name
// and brief description (one-liner; the full per-action docs live in
// the help action).
func NewGroupedTool(name, brief string) *GroupedTool {
	return &GroupedTool{
		name:    name,
		brief:   brief,
		actions: map[string]*GroupedToolAction{},
	}
}

// AddAction registers a sub-action. Action name must be lowercase
// snake_case. "help" is reserved.
func (g *GroupedTool) AddAction(action string, def *GroupedToolAction) {
	if action == "help" {
		panic("'help' is a reserved action name")
	}
	g.actions[action] = def
}

// --- ChatTool interface ---

func (g *GroupedTool) Name() string { return g.name }

func (g *GroupedTool) Desc() string {
	actions := g.sortedActionNames()
	return g.brief + ` Call with action="help" to see the full usage spec for each sub-action. Available actions: ` + strings.Join(actions, ", ") + ", help."
}

func (g *GroupedTool) Caps() []Capability {
	// Union of all action caps. The agent loop's AllowedCaps filter
	// uses these to decide whether the tool appears at all; if any
	// action requires CapExecute and the session doesn't grant it,
	// the whole grouped tool is hidden. Coarse but safe — mixing
	// CapExecute and CapRead actions in one grouped tool means the
	// less-restricted session has to opt into the more-restricted
	// tier to use any action.
	seen := map[Capability]bool{}
	var out []Capability
	for _, a := range g.actions {
		for _, c := range a.Caps {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// NeedsConfirm returns true if any action in the group requires
// confirmation. The per-action handler can re-check and skip the
// prompt when the specific action doesn't need it; this is the
// catalog-level signal.
func (g *GroupedTool) NeedsConfirm() bool {
	for _, a := range g.actions {
		if a.NeedsConfirm {
			return true
		}
	}
	return false
}

func (g *GroupedTool) Params() map[string]ToolParam {
	out := map[string]ToolParam{
		"action": {
			Type:        "string",
			Description: `Which sub-action to invoke. Call with "help" first if you don't know which action you need or what its params look like.`,
		},
	}
	// Union of every action's params. JSON-schema-wise, all are
	// optional at the top level — per-action required validation
	// happens in the dispatcher.
	for _, a := range g.actions {
		for k, v := range a.Params {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out
}

// Run is the no-session fallback. Some grouped tools (the help action
// in particular) work without a session; others don't. We invoke the
// handler unconditionally and let it fail if it needs the session.
func (g *GroupedTool) Run(args map[string]any) (string, error) {
	return g.RunWithSession(args, nil)
}

// RunWithSession dispatches by action. The "help" action is auto-handled.
func (g *GroupedTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	action := strings.TrimSpace(StringArg(args, "action"))
	if action == "" || action == "help" {
		return g.formatHelp(), nil
	}
	def, ok := g.actions[action]
	if !ok {
		return "", fmt.Errorf("unknown action %q for tool %q. Available: %s. Call with action=\"help\" for the full usage spec", action, g.name, strings.Join(g.sortedActionNames(), ", "))
	}
	for _, r := range def.Required {
		v, ok := args[r]
		if !ok || v == nil {
			return "", fmt.Errorf("action %q requires param %q (call %q with action=\"help\" for the full param list)", action, r, g.name)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("action %q requires non-empty %q", action, r)
		}
	}
	return def.Handler(args, sess)
}

// --- internals ---

func (g *GroupedTool) sortedActionNames() []string {
	names := make([]string, 0, len(g.actions))
	for n := range g.actions {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// formatHelp renders the per-action documentation as a single
// readable block. Returned by action="help" or when no action given.
func (g *GroupedTool) formatHelp() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — usage:\n\n", g.name)
	if g.preamble != "" {
		b.WriteString(strings.TrimSpace(g.preamble))
		b.WriteString("\n\n")
	}
	for _, name := range g.sortedActionNames() {
		def := g.actions[name]
		fmt.Fprintf(&b, "  action=%q — %s\n", name, def.Description)
		if len(def.Params) > 0 {
			// Sort params alphabetically for consistency.
			keys := make([]string, 0, len(def.Params))
			for k := range def.Params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			required := map[string]bool{}
			for _, r := range def.Required {
				required[r] = true
			}
			for _, k := range keys {
				p := def.Params[k]
				req := ""
				if required[k] {
					req = " (required)"
				}
				fmt.Fprintf(&b, "    %s (%s)%s — %s\n", k, p.Type, req, p.Description)
			}
		} else {
			b.WriteString("    (no params)\n")
		}
		if def.NeedsConfirm {
			b.WriteString("    note: requires per-call user confirmation.\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(`  action="help" — show this usage spec.` + "\n")
	return b.String()
}
