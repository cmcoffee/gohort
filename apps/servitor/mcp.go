// MCP controls for Servitor: READ-ONLY tools exposed on gohort's inbound MCP
// server (/mcp/) so an external MCP client (e.g. Claude Desktop) can query the
// user's accumulated system knowledge — list their systems and search the facts
// Servitor has gathered about them. Each handler is scoped to the bridge-key
// owner and reads the SAME per-user store the web UI uses
// (UserDB(RootDB.Bucket("servitor"), owner)).
//
// DELIBERATELY read-only and credential-free: these tools never run a command on
// an appliance and never surface a Password/User — only the knowledge layer. Like
// every app MCP tool they default OFF (admin opts in via Admin → MCP Tools), so
// nothing about a user's systems leaves the deployment unless explicitly exposed.
package servitor

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func registerServitorMCPTools() {
	RegisterMCPTool(MCPToolSpec{
		Name:        "servitor_list_systems",
		Description: "List the user's Servitor systems (id, name, host, type) — the appliances Servitor has knowledge about. Read-only; never returns credentials.",
		InputSchema: map[string]any{"type": "object"},
		Handler:     servitorMCPListSystems,
	})
	RegisterMCPTool(MCPToolSpec{
		Name:        "servitor_search_facts",
		Description: "Search the facts Servitor has gathered about the user's systems (keys, values, tags). This is Servitor's accumulated knowledge — use it to answer questions about how a system is configured. Optionally narrow to one system by name or id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":  map[string]any{"type": "string", "description": "Text to match against fact keys, values, and tags."},
				"system": map[string]any{"type": "string", "description": "Optional: limit to a system by name or id (from servitor_list_systems)."},
			},
			"required": []string{"query"},
		},
		Handler: servitorMCPSearchFacts,
	})
	RegisterMCPTool(MCPToolSpec{
		Name:        "servitor_system_facts",
		Description: "Return every fact Servitor knows about one system. Pass the system id from servitor_list_systems.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"system_id": map[string]any{"type": "string", "description": "The system id (from servitor_list_systems)."},
			},
			"required": []string{"system_id"},
		},
		Handler: servitorMCPSystemFacts,
	})
}

// servitorUserDB resolves the owner's Servitor store — the SAME store the web UI
// reads. Servitor data lives in the app's own bucket (RootDB.Bucket("servitor"),
// the framework's T.DB), NOT RootDB itself.
func servitorUserDB(owner string) Database {
	if RootDB == nil || owner == "" {
		return nil
	}
	return UserDB(RootDB.Bucket("servitor"), owner)
}

func servitorMCPListSystems(_ context.Context, owner string, _ map[string]any) (string, error) {
	udb := servitorUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	var b strings.Builder
	n := 0
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if !udb.Get(applianceTable, k, &a) {
			continue
		}
		n++
		typ := a.Type
		if typ == "" {
			typ = "ssh"
		}
		// Only safe, non-credential fields.
		if typ == "command" {
			fmt.Fprintf(&b, "- %s — %q (command)\n", a.ID, a.Name)
		} else {
			fmt.Fprintf(&b, "- %s — %q (%s @ %s)\n", a.ID, a.Name, typ, a.Host)
		}
	}
	if n == 0 {
		return "You have no Servitor systems yet.", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func servitorMCPSearchFacts(_ context.Context, owner string, args map[string]any) (string, error) {
	udb := servitorUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	query := strings.TrimSpace(svcMCPStr(args, "query"))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	facts := searchFacts(udb, query, strings.TrimSpace(svcMCPStr(args, "system")))
	if len(facts) == 0 {
		return fmt.Sprintf("No facts match %q.", query), nil
	}
	return formatFacts(facts), nil
}

func servitorMCPSystemFacts(_ context.Context, owner string, args map[string]any) (string, error) {
	udb := servitorUserDB(owner)
	if udb == nil {
		return "", fmt.Errorf("no store for user")
	}
	id := strings.TrimSpace(svcMCPStr(args, "system_id"))
	if id == "" {
		return "", fmt.Errorf("system_id is required")
	}
	facts := factsForAppliance(udb, id)
	if len(facts) == 0 {
		return fmt.Sprintf("No facts for system %q (use servitor_list_systems for ids).", id), nil
	}
	return formatFacts(facts), nil
}

func svcMCPStr(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}
