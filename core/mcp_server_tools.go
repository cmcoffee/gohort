package core

import (
	"context"
	"sort"
	"sync"
)

// MCPToolHandler runs a registered MCP tool for an authenticated owner and
// returns the text result (the MCP tools/call content). The MCP server resolves
// owner from the caller's bridge key, so a handler operates strictly on that
// user's data — resolve the user's store with UserDB(RootDB, owner).
type MCPToolHandler func(ctx context.Context, owner string, args map[string]any) (string, error)

// MCPToolSpec is an app-contributed tool exposed on gohort's INBOUND MCP server
// (apps/mcpserver, served at /mcp/). Apps register these so an external MCP
// client (e.g. Claude Desktop) can DRIVE the app — list/read/create/edit its
// records — not just talk to agents. This is the registry seam that keeps the
// MCP server domain-agnostic: it knows nothing about guides/notes/etc., it just
// aggregates whatever apps register. An app calls RegisterMCPTool from its
// init() or Routes(); see apps/guides/mcp.go for the canonical example.
type MCPToolSpec struct {
	Name        string         // unique, snake_case, app-namespaced (e.g. "guides_list")
	Description string         // shown to the MCP client; say what it does + when to use it
	InputSchema map[string]any // JSON Schema object for the arguments (nil ⇒ no args)
	Handler     MCPToolHandler
}

var (
	mcpToolsMu sync.RWMutex
	mcpTools   = map[string]MCPToolSpec{}
)

// RegisterMCPTool adds (or replaces) an app MCP tool. Last registration of a
// name wins. Ignores a spec with no name or handler.
func RegisterMCPTool(spec MCPToolSpec) {
	if spec.Name == "" || spec.Handler == nil {
		return
	}
	mcpToolsMu.Lock()
	mcpTools[spec.Name] = spec
	mcpToolsMu.Unlock()
}

// RegisteredMCPTools returns the app MCP tools, name-sorted for a stable
// tools/list order (a byte-stable list keeps MCP clients from re-prompting).
func RegisteredMCPTools() []MCPToolSpec {
	mcpToolsMu.RLock()
	out := make([]MCPToolSpec, 0, len(mcpTools))
	for _, s := range mcpTools {
		out = append(out, s)
	}
	mcpToolsMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LookupMCPTool resolves a registered tool by name.
func LookupMCPTool(name string) (MCPToolSpec, bool) {
	mcpToolsMu.RLock()
	s, ok := mcpTools[name]
	mcpToolsMu.RUnlock()
	return s, ok
}

// --- exposure policy (admin governs what's reachable over MCP) ---------------

// mcpToolPolicyTable — bumped to a fresh name after a bool→struct value-type
// change left undecodable rows in the original "mcp_tool_policy" table (DBase.Get
// FATALs on a gob mismatch). Starting clean sidesteps any mix of old bool/struct
// rows. The value type here is a bare bool and MUST NOT change again.
const mcpToolPolicyTable = "mcp_tool_exposed"

// MCPAppToolExposed reports whether an app-contributed MCP tool is reachable on
// the inbound MCP server. Default OFF: the admin opts each tool in, so adding a
// new app tool never silently widens the external surface. (The built-in
// ask_agent / recent_results tools are not governed here — they're always on.)
func MCPAppToolExposed(name string) bool {
	if RootDB == nil {
		return false
	}
	// Stored as a bare bool. NOTE: do not change this value type — kvlite uses
	// gob, and DBase.Get FATALs (Critical) on a decode mismatch, so a bool-vs-
	// struct change crashes the server on the old keys. (We learned this the hard
	// way; the table already holds bools.)
	var on bool
	if RootDB.Get(mcpToolPolicyTable, name, &on) {
		return on
	}
	return false
}

// SetMCPAppToolExposed persists the admin's expose/hide choice for an app tool.
func SetMCPAppToolExposed(name string, on bool) {
	if RootDB != nil {
		RootDB.Set(mcpToolPolicyTable, name, on)
	}
}

// MCPToolStatus is one registered app tool plus whether it's currently exposed —
// the row the admin UI lists with a toggle.
type MCPToolStatus struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Exposed     bool   `json:"exposed"`
}

// MCPAppToolStatuses returns every registered app MCP tool with its exposure
// state, name-sorted — the feed behind the admin MCP-tools governance table.
func MCPAppToolStatuses() []MCPToolStatus {
	specs := RegisteredMCPTools()
	out := make([]MCPToolStatus, 0, len(specs))
	for _, s := range specs {
		out = append(out, MCPToolStatus{Name: s.Name, Description: s.Description, Exposed: MCPAppToolExposed(s.Name)})
	}
	return out
}

// --- agent exposure gate (which agents ask_agent may reach) ------------------

// MCPAgentGateFunc reports whether (owner, agentID) is reachable over MCP — i.e.
// the agent's MCPExposed flag. core can't import AgentRecord/loadAgent, so the
// agent-aware package (orchestrate) registers the closure, mirroring the
// channel-runner / standing-runner seams.
type MCPAgentGateFunc func(owner, agentID string) bool

var (
	mcpAgentGate   MCPAgentGateFunc
	mcpAgentGateMu sync.RWMutex
)

// RegisterMCPAgentGate installs the agent-exposure check. Call once at startup
// from orchestrate.
func RegisterMCPAgentGate(fn MCPAgentGateFunc) {
	mcpAgentGateMu.Lock()
	mcpAgentGate = fn
	mcpAgentGateMu.Unlock()
}

// MCPAgentExposed reports whether ask_agent may dispatch to (owner, agentID).
// Fails CLOSED: when no gate is registered (orchestrate not loaded) nothing is
// reachable, so the external surface can't be wider than the policy.
func MCPAgentExposed(owner, agentID string) bool {
	mcpAgentGateMu.RLock()
	fn := mcpAgentGate
	mcpAgentGateMu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(owner, agentID)
}
