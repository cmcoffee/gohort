// Package mcpclient is a minimal, transport-pluggable Model Context
// Protocol CLIENT for the gohort SERVER. It speaks JSON-RPC 2.0 to a
// remote MCP server and exposes just what gohort needs — initialize,
// tools/list, tools/call — over a Transport interface so the wire
// (Streamable HTTP today, legacy HTTP+SSE later) is swappable.
//
// This package is deliberately DEPENDENCY-FREE of gohort/core: it deals
// in its own MCP types (ToolDef, raw JSON schema) and never imports the
// tool registry. The adapter in core/mcp_manager.go bridges these types
// into core.ChatTool / core.ReferenceSource. Keeping it pure makes it
// unit-testable against an httptest server with no gohort wiring.
//
// It mirrors the stdio host in gohort-desktop/mcp (a separate Go module,
// so it can't be imported) — same JSON-RPC core, different transport.
package mcpclient

import "context"

// Transport carries already-marshaled JSON-RPC frames to a server and
// returns the response frame. The Client owns request/response shaping
// and id assignment; the Transport only moves bytes for one logical
// exchange. This split lets a streamable-HTTP transport (one POST per
// message) and a future SSE transport (a persistent stream) share the
// same Client.
type Transport interface {
	// Call sends one JSON-RPC request frame and returns the matching
	// response frame. Implementations must respect ctx for cancellation
	// and deadlines.
	Call(ctx context.Context, frame []byte) (response []byte, err error)
	// Notify sends a fire-and-forget notification frame (no id, no
	// response expected).
	Notify(ctx context.Context, frame []byte) error
	// Close releases any transport resources (open streams, idle conns).
	Close() error
}
