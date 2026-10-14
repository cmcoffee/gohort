// mcp: Builder's view of the MCP servers this deployment is connected to.
//
// MCP server tools (<server>_<tool>) were out of Builder's reach entirely. It
// had no way to list the servers, see their tools, check a connection, or call
// a tool to learn what it returns. Asked to fix an agent that uses one, its
// only route was dispatching to that agent, which can refuse, and it ended up
// rewriting the agent's rules without ever having seen the tool work.
//
// list, get and test read; run calls one tool, as the person running Builder,
// so a server signed in per person (Atlassian, say) answers for them. A tool
// the server's own name marks as changing something asks first, in the chat;
// with nobody to ask it is not run. A Builder run another agent sent can look
// but not run, or any agent able to reach Builder would reach every MCP tool
// through it. Servers themselves are an administrator's, set up in Admin > MCP
// Servers: Builder sees and tests them, it does not change them.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const mcpToolName = "mcp"

// mcpToolOutputMax bounds what a run hands back.
const mcpToolOutputMax = 8000

func mcpToolDef(sess *ToolSession) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: mcpToolName,
			Description: "The MCP servers this deployment is connected to, and their tools (named <server>_<tool>). " +
				"action=list: every server, whether it is on and connected for you, and its tool names. " +
				"action=get (server): each tool's description and parameters, and which ones ask before they run. " +
				"action=test (server): check the connection now. " +
				"action=run (tool, args): call one tool and see what it returns, to learn its real behaviour before you build on it or tell an agent how to use it; " +
				"a tool that changes something asks the person first. " +
				"An agent uses these tools by having them on its tool list, like any other tool. Servers are set up by an administrator in Admin > MCP Servers: you can see and test them, not change them.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Enum: []string{"list", "get", "test", "run"}, Description: "list | get | test | run."},
				"server": {Type: "string", Description: "(get, test) The server's name, as list shows it."},
				"tool":   {Type: "string", Description: "(run) The tool's name, <server>_<tool>, as get shows it."},
				"args":   {Type: "object", Description: "(run) The tool's arguments, as get describes them."},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead, CapNetwork},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			user := ""
			if sess != nil {
				user = sess.Username
			}
			switch strings.ToLower(strings.TrimSpace(stringArg(args, "action"))) {
			case "list":
				return mcpListServers(user), nil
			case "get":
				return mcpGetServer(user, stringArg(args, "server"))
			case "test":
				return mcpTestServer(ctx, user, stringArg(args, "server"))
			case "run":
				return mcpRunTool(ctx, sess, stringArg(args, "tool"), mcpArgs(args["args"]))
			}
			return "", fmt.Errorf("action must be list, get, test or run")
		},
	}
}

// mcpServerTools is the registered tools of one server that are not switched
// off, by name.
func mcpServerTools(server string) []ChatTool {
	var out []ChatTool
	for _, ct := range FilterChatTools(BlockedTools) {
		if c, ok := ct.(CategorizedTool); ok && c.Category() == MCPToolCategory(server) {
			out = append(out, ct)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// mcpToolByName finds a registered MCP tool that is not switched off. By its
// category rather than through the server list, so a tool registered from a
// server's cached tool list is found while the server reconnects.
func mcpToolByName(name string) ChatTool {
	for _, ct := range FilterChatTools(BlockedTools) {
		if ct.Name() != name {
			continue
		}
		if c, ok := ct.(CategorizedTool); ok && strings.HasPrefix(c.Category(), MCPToolCategory("")+": ") {
			return ct
		}
	}
	return nil
}

func mcpServer(name string) (MCPServerConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return MCPServerConfig{}, fmt.Errorf("name the server: mcp(action=\"list\") shows them")
	}
	cfg, ok := MCP().Load(name)
	if !ok {
		return MCPServerConfig{}, fmt.Errorf("no MCP server named %q: mcp(action=\"list\") shows them", name)
	}
	return cfg, nil
}

// mcpServerState says whether a server's tools can be used by user now.
func mcpServerState(user string, cfg MCPServerConfig) string {
	switch {
	case !cfg.Enabled:
		return "switched off"
	case !cfg.ExposeTools:
		return "on, but its tools are not offered to agents"
	case MCP().Connected(user, cfg.Name):
		return "connected"
	case cfg.AuthMode == MCPAuthOAuth:
		return "not connected for you: it signs in per person, under your account's connections"
	}
	return "not connected right now"
}

func mcpListServers(user string) string {
	servers := MCP().List()
	if len(servers) == 0 {
		return "No MCP servers are set up on this deployment. An administrator adds them in Admin > MCP Servers."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d MCP server(s):\n", len(servers))
	for _, cfg := range servers {
		tools := mcpServerTools(cfg.Name)
		var names []string
		for _, t := range tools {
			names = append(names, t.Name())
		}
		fmt.Fprintf(&b, "\n- %s: %s; %d tool(s)", cfg.Name, mcpServerState(user, cfg), len(tools))
		if len(names) > 0 {
			b.WriteString(": " + strings.Join(names, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nmcp(action=\"get\", server=...) shows a server's tools in full.")
	return b.String()
}

func mcpGetServer(user, name string) (string, error) {
	cfg, err := mcpServer(name)
	if err != nil {
		return "", err
	}
	tools := mcpServerTools(cfg.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "MCP server %s: %s.\n", cfg.Name, mcpServerState(user, cfg))
	if len(tools) == 0 {
		b.WriteString("It offers no tools right now: it may not have connected yet, or offers them only once connected.")
		return b.String(), nil
	}
	for _, ct := range tools {
		fmt.Fprintf(&b, "\n## %s", ct.Name())
		if c, ok := ct.(interface{ NeedsConfirm() bool }); ok && c.NeedsConfirm() {
			b.WriteString(" (changes something: asks before it runs)")
		}
		b.WriteString("\n" + strings.TrimSpace(ct.Desc()) + "\n")
		params := ct.Params()
		var names []string
		for n := range params {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := params[n]
			line := "- " + n + " (" + firstNonBlank(p.Type, "any") + ")"
			if d := strings.TrimSpace(p.Description); d != "" {
				line += ": " + d
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String(), nil
}

func mcpTestServer(ctx context.Context, user, name string) (string, error) {
	cfg, err := mcpServer(name)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return fmt.Sprintf("%s is switched off. An administrator turns it on in Admin > MCP Servers.", cfg.Name), nil
	}
	if cfg.AuthMode == MCPAuthOAuth {
		// Signed in per person: the connection that matters is this user's.
		if MCP().Connected(user, cfg.Name) {
			return fmt.Sprintf("%s is connected for you, with %d tool(s).", cfg.Name, len(mcpServerTools(cfg.Name))), nil
		}
		return fmt.Sprintf("%s signs in per person and is not connected for you. Connect it under your account's connections, then test again.", cfg.Name), nil
	}
	msg, err := MCP().Test(ctx, cfg, "")
	if err != nil {
		return "", fmt.Errorf("%s did not answer: %v", cfg.Name, err)
	}
	return msg, nil
}

func mcpRunTool(ctx context.Context, sess *ToolSession, name string, args map[string]any) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name the tool, <server>_<tool>: mcp(action=\"get\", server=...) lists them")
	}
	if sess != nil && strings.TrimSpace(sess.DispatchParentAgentID) != "" {
		return "", fmt.Errorf("not run: this Builder run was sent by another agent, and running an MCP tool from here would reach past what that agent may use. Look with list and get, or try it from Builder's own chat")
	}
	ct := mcpToolByName(name)
	if ct == nil {
		return "", fmt.Errorf("no MCP tool named %q is available: mcp(action=\"list\") shows the tools each server offers", name)
	}
	td := ChatToolToAgentToolDefWithSession(ct, sess)
	if td.NeedsConfirm {
		if sess == nil || sess.AskInChat == nil {
			return "", fmt.Errorf("not run: %s changes something, and nobody is watching this run to approve it", name)
		}
		brief, _ := json.Marshal(args)
		if sess.AskInChat(fmt.Sprintf("Builder wants to run %s to see what it does. It changes something. Run it?", name), truncate(string(brief), 600), []string{"Run it"}) == "" {
			return fmt.Sprintf("Not run: %s was not approved.", name), nil
		}
	}
	out, err := td.Handler(ctx, args)
	if err != nil {
		return "", fmt.Errorf("%s: %v", name, err)
	}
	return truncate(out, mcpToolOutputMax), nil
}

// mcpArgs reads the run's arguments, given as an object or as JSON text.
func mcpArgs(v any) map[string]any {
	switch a := v.(type) {
	case map[string]any:
		return a
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(a), &m) == nil {
			return m
		}
	}
	return map[string]any{}
}
