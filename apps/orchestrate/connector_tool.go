// The "connector" authoring tool — Builder's front-end for declaring a new
// BRIDGE TYPE at runtime, no code change. A connector points gohort at an
// external capability (today: a remote MCP server — many services publish one:
// calendars, ticketing, CRMs) so its tools become available to agents once an
// admin approves it. Pure composition over core.Connector + the registered kind
// handlers; Builder never handles a secret (auth is referenced by credential
// name, or is per-user oauth).
//
// Builder-exclusive (added to builderAuthoringTools). Distinct from the
// "bridge" tool: bridge polls ONE authenticated URL and wakes an agent on
// change; connector registers a whole tool surface (a capability type) for
// every agent.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// connectorDefTool builds the grouped "connector" authoring tool. Mirrors
// bridge / skill_def: one tool, action-discriminated, single-fire-per-batch
// (you author one connector at a time).
func connectorDefTool() ChatTool {
	gt := NewGroupedTool("connector",
		"Declare a new BRIDGE TYPE with no code change: point gohort at an external capability (e.g. a calendar or CRM via its MCP server) so its tools become available to agents. YOU author the config; an admin APPROVES it in Admin > Connectors before it goes live.")
	gt.SetSingleFirePerBatch(true)
	gt.SetHelpPreamble(strings.TrimSpace(`
A connector is a reusable "bridge type": a declared external capability that
lives in one governed surface (Admin > Connectors). Two kinds ship:

  remote_mcp — a remote Model Context Protocol server. Many services publish one
  (calendars, ticketing, CRMs). Its tools register as <name>.<tool>. Created
  UNAPPROVED (it adds new external reach) — an admin approves it before it goes
  live.

  rest_poll — poll ONE authenticated URL every N minutes and wake an agent when
  the response changes (same as the bridge tool). It uses an already-approved
  credential, so it goes LIVE on create; an admin can still unapprove/delete it.

  desktop_mcp — run a LOCAL MCP server (a subprocess: command + args) on the
  user's OWN machine via their gohort desktop app; its tools register as
  <name>.<tool>. Created UNAPPROVED (it runs code on the user's machine) — an
  admin approves it, and the user's desktop then asks the user to confirm before
  it applies. The user's desktop app must be running.

  desktop_command — run a FIXED local command as one tool on the user's machine
  (command + args, with optional {placeholder} params). The lightweight option
  when a full MCP server is overkill. Same UNAPPROVED + user-consent gates as
  desktop_mcp; the user's desktop app must be running.

Governance for remote_mcp: create leaves it UNAPPROVED and inert. Tell the user
an admin must approve it in Admin > Connectors (there they confirm the endpoint +
auth). You NEVER handle a secret:
  - auth_mode="secure_api" references a SecureAPI credential by NAME (draft it
    first with draft_oauth_credential; the admin pastes the secret in Admin >
    APIs);
  - auth_mode="oauth" is per-user hosted login (each user connects their own
    account);
  - auth_mode="none" for a public server.
A static bearer token is a secret — those servers are added by the admin
directly in Admin > MCP Servers, not via a connector.

Typical flow for a calendar:
  1. (if the service needs an app credential) draft_oauth_credential for it.
  2. connector(action="create", kind="remote_mcp", name="gcal",
     url="https://<calendar-mcp-endpoint>", auth_mode="oauth").
  3. Tell the user to approve it in Admin > Connectors. Once approved its tools
     (gcal.list_events, gcal.create_event, …) are available to agents.`))

	gt.AddAction("create", &GroupedToolAction{
		Description: "Declare a new connector (bridge type). remote_mcp is created UNAPPROVED (admin approves in Admin > Connectors); rest_poll goes live immediately (it uses an already-approved credential).",
		Params: map[string]ToolParam{
			"kind":             {Type: "string", Enum: []string{RemoteMCPConnectorKind, RestPollConnectorKind, DesktopMCPConnectorKind, DesktopCommandConnectorKind}, Description: "The bridge type. remote_mcp = a remote MCP server whose tools register as <name>.<tool>. rest_poll = poll one authenticated URL every N minutes and wake an agent when it changes. desktop_mcp = run a LOCAL MCP server (subprocess) on the user's OWN machine. desktop_command = run a fixed local command (with {placeholder} args) as one tool on the user's machine — the lightweight option."},
			"name":             {Type: "string", Description: "Short unique id (letters/digits/underscore/dash), e.g. \"gcal\". Namespaces the capability's tools."},
			"url":              {Type: "string", Description: "(remote_mcp) the MCP server's https endpoint. (rest_poll) the full URL to poll each interval."},
			"auth_mode":        {Type: "string", Enum: []string{"none", "secure_api", "oauth"}, Description: "(remote_mcp) How the server authenticates. none = public; secure_api = mint a bearer from a registered SecureAPI credential (set secure_cred); oauth = per-user hosted login. NEVER pass a static token."},
			"secure_cred":      {Type: "string", Description: "(remote_mcp, auth_mode=secure_api) Name of a registered SecureAPI OAuth2 credential to mint the bearer from. Draft it first with draft_oauth_credential."},
			"credential":       {Type: "string", Description: "(rest_poll) Name of a registered SecureAPI credential to call the URL through (becomes call_<name>). Draft it first if needed."},
			"wake_agent":       {Type: "string", Description: "(rest_poll) Name or id of the agent to wake when the polled response changes."},
			"interval_minutes": {Type: "number", Description: "(rest_poll) How often to poll, in minutes (minimum 1)."},
			"wake_brief":       {Type: "string", Description: "(rest_poll, optional) Guidance handed to the woken agent on each change — what the data means and what to do about it."},
			"method":           {Type: "string", Description: "(rest_poll, optional) HTTP method; defaults to GET."},
			"body":             {Type: "string", Description: "(rest_poll, optional) request body for POST/PUT."},
			"command":          {Type: "string", Description: "(desktop_mcp / desktop_command) the executable the desktop runs, e.g. \"npx\" or an absolute path."},
			"args":             {Type: "array", Description: "(desktop_mcp / desktop_command, optional) command arguments as a list of strings. For desktop_command, an arg may contain a {placeholder} filled from the tool call, e.g. [\"--query\", \"{q}\"]."},
			"params":           {Type: "object", Description: "(desktop_command, optional) the tool's parameters as {name: description} — each becomes a required string arg the caller supplies and can be referenced as {name} in args. Omit for a fixed command with no inputs."},
			"description":      {Type: "string", Description: "(optional) What this connector is for. For desktop_command it is also the tool's description shown to callers."},
		},
		Required: []string{"kind", "name"},
		Handler:  connectorCreate,
	})
	gt.AddAction("list", &GroupedToolAction{
		Description: "List declared connectors and whether each is approved + live.",
		Handler:     connectorList,
	})
	gt.AddAction("get", &GroupedToolAction{
		Description: "Show one connector's configuration and status.",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "The connector name."}},
		Required:    []string{"name"},
		Handler:     connectorGet,
	})
	gt.AddAction("test", &GroupedToolAction{
		Description: "Validate a connector's config WITHOUT approving it (checks shape + referenced credential).",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "The connector name."}},
		Required:    []string{"name"},
		Handler:     connectorTest,
	})
	gt.AddAction("delete", &GroupedToolAction{
		Description: "Delete a connector and tear down its live capability.",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "The connector name."}},
		Required:    []string{"name"},
		Handler:     connectorDelete,
	})
	return gt
}

// stringSliceArg reads a list-of-strings tool argument, tolerating a JSON array
// (the normal case), a []string, or a lone string.
func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if strings.TrimSpace(t) != "" {
			return []string{t}
		}
	}
	return nil
}

// stringMapArg reads an object tool argument as a map[string]string (values
// coerced to their string form). Used for desktop_command's {name: description}
// params. Returns nil when absent or not an object.
func stringMapArg(args map[string]any, key string) map[string]string {
	m, ok := args[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

// normalizeConnectorAuth maps the LLM-facing "none" to the empty auth mode the
// underlying subsystems use.
func normalizeConnectorAuth(v string) string {
	v = strings.TrimSpace(v)
	if v == "none" {
		return ""
	}
	return v
}

func connectorCreate(args map[string]any, sess *ToolSession) (string, error) {
	owner := bridgeOwner(sess)
	name := strings.TrimSpace(stringArg(args, "name"))
	kind := strings.TrimSpace(stringArg(args, "kind"))
	if kind == "" {
		kind = RemoteMCPConnectorKind
	}
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if _, exists := GetConnector(RootDB, name); exists {
		return "", fmt.Errorf("a connector named %q already exists — pick another name or delete it first", name)
	}

	c := Connector{
		Name:  name,
		Kind:  kind,
		Owner: owner,
		Desc:  strings.TrimSpace(stringArg(args, "description")),
	}

	switch kind {
	case RemoteMCPConnectorKind:
		// Don't stomp an MCP server an admin authored under the same name —
		// the connector materializes into a server of the same name.
		if _, ok := MCP().Load(name); ok {
			return "", fmt.Errorf("an MCP server named %q already exists (Admin > MCP Servers) — choose a different connector name", name)
		}
		spec := RemoteMCPSpec{
			URL:        strings.TrimSpace(stringArg(args, "url")),
			AuthMode:   normalizeConnectorAuth(stringArg(args, "auth_mode")),
			SecureCred: strings.TrimSpace(stringArg(args, "secure_cred")),
		}
		raw, _ := json.Marshal(spec)
		c.Spec = raw
	case RestPollConnectorKind:
		if owner == "" {
			return "", fmt.Errorf("rest_poll requires an authenticated session")
		}
		wantAgent := strings.TrimSpace(stringArg(args, "wake_agent"))
		wakeAgent := resolveCheckAgent(sess, owner, wantAgent, "")
		if wakeAgent == "" {
			return "", fmt.Errorf("no agent named %q to wake — pass a real agent name or id for wake_agent", wantAgent)
		}
		// Don't clobber an existing bridge/monitor of the same name (the
		// connector materializes into an owner-scoped EventMonitor).
		if _, exists := GetEventMonitor(RootDB, owner, name); exists {
			return "", fmt.Errorf("a bridge or monitor named %q already exists for you — pick another name", name)
		}
		spec := RestPollSpec{
			Credential:      strings.TrimSpace(stringArg(args, "credential")),
			URL:             strings.TrimSpace(stringArg(args, "url")),
			Method:          strings.TrimSpace(stringArg(args, "method")),
			Body:            stringArg(args, "body"),
			WakeAgent:       wakeAgent,
			WakeBrief:       strings.TrimSpace(stringArg(args, "wake_brief")),
			IntervalMinutes: oArgInt(args, "interval_minutes"),
		}
		raw, _ := json.Marshal(spec)
		c.Spec = raw
	case DesktopMCPConnectorKind:
		if owner == "" {
			return "", fmt.Errorf("desktop_mcp requires an authenticated session (it installs on the user's own machine)")
		}
		spec := DesktopMCPSpec{
			Command: strings.TrimSpace(stringArg(args, "command")),
			Args:    stringSliceArg(args, "args"),
		}
		raw, _ := json.Marshal(spec)
		c.Spec = raw
	case DesktopCommandConnectorKind:
		if owner == "" {
			return "", fmt.Errorf("desktop_command requires an authenticated session (it runs on the user's own machine)")
		}
		// Params: {name: description}, all treated as required string args and
		// referenceable as {name} in the command args.
		var params map[string]ToolParam
		var required []string
		for pn, pd := range stringMapArg(args, "params") {
			if params == nil {
				params = map[string]ToolParam{}
			}
			params[pn] = ToolParam{Type: "string", Description: pd}
			required = append(required, pn)
		}
		spec := DesktopCommandSpec{
			Command:  strings.TrimSpace(stringArg(args, "command")),
			Args:     stringSliceArg(args, "args"),
			Desc:     strings.TrimSpace(stringArg(args, "description")),
			Params:   params,
			Required: required,
		}
		raw, _ := json.Marshal(spec)
		c.Spec = raw
	default:
		return "", fmt.Errorf("unknown connector kind %q", kind)
	}

	if err := SaveConnector(RootDB, c); err != nil {
		return "", err
	}
	// rest_poll (and any auto-approving kind) goes live on create; remote_mcp
	// waits for an admin. Reflect the real state back.
	saved, _ := GetConnector(RootDB, name)
	if saved.Approved {
		return fmt.Sprintf("Connector %q (kind=%s) created and LIVE. %s\nManage it in Admin > Connectors.", name, kind, ConnectorSummary(saved)), nil
	}
	return fmt.Sprintf(
		"Drafted connector %q (kind=%s), created UNAPPROVED. %s\nTo go live: an admin opens Admin > Connectors, reviews it, and clicks Approve — then its tools register for agents. Nothing runs until then.",
		name, kind, ConnectorSummary(saved)), nil
}

func connectorList(args map[string]any, sess *ToolSession) (string, error) {
	cs := ListConnectors(RootDB)
	if len(cs) == 0 {
		return "No connectors declared yet.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d connector(s):\n", len(cs))
	for _, c := range cs {
		state := "UNAPPROVED (inert)"
		if c.Approved {
			state = "approved (live)"
		}
		fmt.Fprintf(&b, "- %s [%s] %s: %s", c.Name, c.Kind, state, ConnectorSummary(c))
		if c.LastError != "" {
			fmt.Fprintf(&b, " ⚠️ last error: %s", c.LastError)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func connectorGet(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return "", fmt.Errorf("no connector named %q", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Connector %q:\n", c.Name)
	fmt.Fprintf(&b, "  kind:     %s\n", c.Kind)
	fmt.Fprintf(&b, "  summary:  %s\n", ConnectorSummary(c))
	if c.Desc != "" {
		fmt.Fprintf(&b, "  purpose:  %s\n", c.Desc)
	}
	if c.Approved {
		b.WriteString("  status:   approved (live)\n")
	} else {
		b.WriteString("  status:   UNAPPROVED — an admin must approve it in Admin > Connectors\n")
	}
	if c.Owner != "" {
		fmt.Fprintf(&b, "  drafted by: %s\n", c.Owner)
	}
	if c.LastError != "" {
		fmt.Fprintf(&b, "  last error: %s\n", c.LastError)
	}
	return strings.TrimSpace(b.String()), nil
}

func connectorTest(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return "", fmt.Errorf("no connector named %q", name)
	}
	if err := ValidateConnector(c); err != nil {
		return "", err
	}
	return fmt.Sprintf("Connector %q validates. %s\nIt still needs admin approval in Admin > Connectors before it goes live.", name, ConnectorSummary(c)), nil
}

func connectorDelete(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if _, ok := GetConnector(RootDB, name); !ok {
		return "", fmt.Errorf("no connector named %q", name)
	}
	if err := DeleteConnector(RootDB, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted connector %q and tore down its capability.", name), nil
}
