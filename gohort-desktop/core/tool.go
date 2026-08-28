// Tool is the interface every local-capability plugin satisfies.
// Plugins live in tools/<category>/ subpackages and self-register
// via init() — same pattern kitebroker uses for tasks and gohort
// uses for its own tools.
//
// The desktop's Wails bridge exposes the registry to JS for direct
// local calls. The WebSocket client (Phase 2) will additionally
// announce the catalog to a connected remote gohort server so the
// server's LLMs can invoke them.
//
// To add a new tool:
//
//   1. Create tools/<category>/<tool>.go in its own package.
//   2. Define a struct that implements Tool.
//   3. In an init() block: core.RegisterTool(new(MyTool)).
//   4. Add a blank import to tools.go to pull the package in.
//
// No central switch statement, no registration in the loader — every
// tool stays self-contained.

package core

import "strings"

// ToolParam describes one parameter on a Tool. Matches gohort's own
// ToolParam shape so catalogs interop without translation.
type ToolParam struct {
	Type        string `json:"type"`        // "string", "number", "boolean", "object", "array"
	Description string `json:"description"` // human-readable; surfaced to the LLM
}

// ToolHandler is the per-tool execution function. Returns the
// result as a string (typically JSON-encoded for structured results,
// plain text otherwise) and an error for the LLM to see on failure.
type ToolHandler func(args map[string]any) (string, error)

// Tool is the contract every local capability implements.
//
// Name is the LLM-facing identifier; should be snake_case and
// prefixed with the category (e.g. "filesystem_read_local_file",
// "apps_open", "contacts_lookup") so the registry stays scannable
// as the catalog grows. It MUST satisfy ValidToolName — see the note
// there for why a separator other than "_" silently breaks the tool.
//
// Desc is what the LLM reads when deciding whether to call this
// tool — be descriptive about WHEN to use it, not just what it does.
//
// Params is the JSON-schema-ish parameter set. Required is the list
// of param names that must be supplied; everything else is optional.
//
// Handler is the actual execution. Receives the LLM-supplied args
// map; returns result-or-error.
//
// Enabled lets a tool opt out of registration at runtime (e.g. an
// apps_open tool that's disabled on systems where the open command
// isn't available). The registry skips disabled tools entirely —
// they don't appear in the catalog, can't be called.
type Tool interface {
	Name() string
	Desc() string
	Params() map[string]ToolParam
	Required() []string
	Handler() ToolHandler
	Enabled() bool
}

// maxToolNameBytes is the longest tool name the model APIs accept.
const maxToolNameBytes = 128

// ValidToolName reports whether name is in the character class every model
// API accepts: ^[a-zA-Z0-9_-]{1,128}$.
//
// This matters here, two hops from any model, because the server publishes
// this catalog straight into the LLM tool list. The names were once written
// "filesystem.read_local_file", and a DOT is outside that class — so the
// server's name guard dropped the entire desktop surface from every catalog
// it built. Nothing errored on either side; the tools were simply never
// offered. Hence the check at the point names are minted rather than a
// convention nobody can enforce.
func ValidToolName(name string) bool {
	if len(name) == 0 || len(name) > maxToolNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ToolName normalizes an externally-supplied name (an MCP server's own tool
// names, a declared command) into that class: everything outside it collapses
// to a single underscore, and the result is capped to length. Returns "" when
// nothing usable survives.
//
// Only for names we did NOT author. A native tool's name is a compile-time
// constant and should be written correctly, not laundered — RegisterTool
// panics rather than sanitizing.
func ToolName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prev_underscore := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
			prev_underscore = false
		default:
			if !prev_underscore && b.Len() > 0 {
				b.WriteByte('_')
				prev_underscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > maxToolNameBytes {
		out = strings.Trim(out[:maxToolNameBytes], "_")
	}
	return out
}
