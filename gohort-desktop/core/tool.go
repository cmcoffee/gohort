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
// prefixed with the category (e.g. "filesystem.read_local_file",
// "apps.open", "screenshot.capture") so the registry stays scannable
// as the catalog grows.
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
// apps.open tool that's disabled on systems where the open command
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
