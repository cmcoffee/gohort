package orchestrate

// A person's own MCP servers (core/mcppersonal) reach their own agents, in
// the runs they make: a provider rather than the registry, so they appear in
// every path an agent runs on (a chat, a channel message, a wake, a schedule)
// and in nobody else's. An agent with a tool list of its own takes only the
// names on it, as it does every other tool; Private mode takes them away, as
// it takes every tool that reaches the network.

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/mcppersonal"
)

func init() { RegisterAgentToolProvider("mcp.personal", personalMCPTools) }

func personalMCPTools(sess *ToolSession, owner, agentID string) []AgentToolDef {
	if sess == nil || owner == "" {
		return nil
	}
	// Someone else running an agent shared with them does not get its
	// owner's servers: those answer as the owner.
	if user := sess.Username; user != "" && user != owner {
		return nil
	}
	servers := mcppersonal.List(owner)
	if len(servers) == 0 {
		return nil
	}
	if db := AuthDB(); db != nil && AuthGetPrivateModeForAgent(db, owner, agentID) {
		return nil
	}
	taken := registeredToolNames()
	defs := mcppersonal.Tools(sess, owner, taken)
	if agentID == "" || orchestrateBaseDB == nil {
		return defs
	}
	a, ok := loadAgent(UserDB(orchestrateBaseDB, owner), agentID)
	if !ok || len(a.AllowedTools) == 0 {
		return defs
	}
	listed := map[string]bool{}
	for _, n := range a.AllowedTools {
		listed[n] = true
	}
	// A name listed in the other shape (the server's one tool, or one of its
	// tools) reaches the shape the server is in now.
	aliases := mcppersonal.Aliases(owner, registeredToolNames())
	var out []AgentToolDef
	for _, d := range defs {
		keep := listed[d.Tool.Name]
		for _, alt := range aliases[d.Tool.Name] {
			keep = keep || listed[alt]
		}
		if keep {
			out = append(out, d)
		}
	}
	return out
}

// registeredToolNames are the names a person's own MCP tools must not take:
// every registered tool's.
func registeredToolNames() map[string]bool {
	taken := map[string]bool{}
	for _, ct := range RegisteredChatTools() {
		taken[ct.Name()] = true
	}
	return taken
}
