package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// credentialTools returns every tool that declares cred — scanning the shared
// deployment pool, then each user's agents and per-user global pool. Wired into
// core.CredentialToolsResolver at startup so the admin credential UI (which
// can't reach agent records itself) can list a credential's bound tools and warn
// on a secured-but-unused one. A tool declares a credential via its api-mode
// Credential or a fetch_via:/secret: hook capability.
func credentialTools(cred string) []CredentialToolRef {
	cred = strings.TrimSpace(cred)
	if cred == "" || RootDB == nil {
		return nil
	}
	// via returns HOW the tool uses the credential ("" = not at all).
	via := func(tt TempTool) string {
		if strings.TrimSpace(tt.Credential) == cred {
			return "api"
		}
		for _, c := range tt.HookCapabilities {
			if c == "fetch_via:"+cred {
				return "fetch_via"
			}
			if c == "secret:"+cred {
				return "secret"
			}
		}
		return ""
	}
	// Group by (tool, via): one row per tool listing every agent/scope it lives
	// on, so a tool scoped to several agents shows once with all of them — not
	// once per agent.
	type toolGroup struct {
		tool   string
		via    string
		agents []string
		seenAg map[string]bool
	}
	groups := map[string]*toolGroup{}
	var order []string
	add := func(agent, tool, v string) {
		if v == "" {
			return
		}
		k := tool + "\x00" + v
		g := groups[k]
		if g == nil {
			g = &toolGroup{tool: tool, via: v, seenAg: map[string]bool{}}
			groups[k] = g
			order = append(order, k)
		}
		if agent != "" && !g.seenAg[agent] {
			g.seenAg[agent] = true
			g.agents = append(g.agents, agent)
		}
	}
	for _, pt := range LoadSharedPersistentTempTools(RootDB) {
		add("deployment pool", pt.Tool.Name, via(pt.Tool))
	}
	for _, u := range AuthListUsers(RootDB) {
		udb := agentUserDB(RootDB, u.Username)
		for _, a := range listAgents(udb, u.Username) {
			for _, tt := range a.Tools {
				add(a.Name, tt.Name, via(tt))
			}
		}
		for _, pt := range LoadPersistentTempTools(RootDB, u.Username) {
			add(u.Username+" pool", pt.Tool.Name, via(pt.Tool))
		}
	}
	// Connectors bind a credential too, but the tool they generate
	// (rest_poll's call_<cred>, an MCP server's <name>.<tool>) is materialized
	// from the connector spec — it never lands in a pool or agent record, so the
	// TempTool scan above can't see it. Enumerate connectors directly.
	for _, c := range ListConnectors(RootDB) {
		for _, ref := range ConnectorCredentialRefs(c.Spec) {
			if strings.TrimSpace(ref) != cred {
				continue
			}
			tool := c.Name + ".*"
			if c.Kind == RestPollConnectorKind {
				tool = "call_" + cred // rest_poll's generated watch tool
			}
			add(c.Name, tool, "connector")
		}
	}
	out := make([]CredentialToolRef, 0, len(order))
	for _, k := range order {
		g := groups[k]
		out = append(out, CredentialToolRef{Agent: strings.Join(g.agents, ", "), Tool: g.tool, Via: g.via})
	}
	return out
}
