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
lives in one governed surface (Admin > Connectors). Six kinds ship:

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

  messaging_bridge — turn on a BUILT-IN messaging relay on the user's OWN device
  (today: iMessage, macOS-only) so their conversations route to agents through
  channels. Two-sided: it ensures the server-side routing key AND enables the
  device relay. Created UNAPPROVED (it enables a capability on the user's
  machine) — an admin approves it, and the user's desktop then confirms. No
  secret travels; the daemon auto-negotiates its own key. The user's desktop app
  must be running for the device half to apply.

  rest_messaging — a SERVER-SIDE, two-sided messaging bridge for any REST-pollable
  chat service (Microsoft Teams, Slack, Discord). It polls the service's API
  through a SecureAPI credential, routes each new message to the bound channel
  agent, and delivers replies back — no user device, no public webhook. Use a
  preset so you supply only the credential + a couple of vars:
    - preset="teams" — Graph channel-message delta; needs an oauth2 credential
      (draft_oauth_credential) and vars {team_id, channel_id}. Corporate Teams
      requires the tenant admin to grant the credential the Graph channel-read
      permission (a Microsoft-side gate).
    - preset="slack" — Slack Web API; needs a BEARER credential holding a bot
      token (xoxb-…) with channels:history + chat:write, the bot added to the
      channel, and var {channel_id}.
  Or author poll_url + map (dot-paths) + cursor + send_url by hand for any other
  service. Created UNAPPROVED (it routes external messages to agents and sends
  replies) — an admin approves it in Admin > Connectors. Run
  connector(action="test") to preview the mapping before approval.

  REAL-TIME instead of polling: add webhook_provider="slack" to receive Slack
  events the instant they happen (no poll interval). The bridge exposes a public
  route at /bridges/api/webhook/<connector-name>: paste that as the Request URL in
  the Slack app's Event Subscriptions, and the admin sets the app's Signing Secret
  via POST /bridges/api/webhook-secret {"connector","secret"} (kept encrypted,
  never in the connector's exported spec). Replies still go out via send_url +
  credential. webhook_provider="graph" gives Microsoft Teams the same real-time
  path (pair it with preset="teams"): on approval the bridge creates a Graph
  change-notification subscription against that route (clientState auto-generated),
  auto-renews it before the ~60-min expiry, and deletes it on unapprove/delete. It
  needs this deployment's public URL reachable by Graph (validated at creation)
  and the tenant's Teams change-notification licensing.

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
			"kind":             {Type: "string", Enum: []string{RemoteMCPConnectorKind, RestPollConnectorKind, DesktopMCPConnectorKind, DesktopCommandConnectorKind, MessagingBridgeConnectorKind, RestMessagingConnectorKind, RestImageConnectorKind}, Description: "The bridge type. remote_mcp = a remote MCP server whose tools register as <name>.<tool>. rest_poll = poll one authenticated URL every N minutes and wake an agent when it changes. desktop_mcp = run a LOCAL MCP server (subprocess) on the user's OWN machine. desktop_command = run a fixed local command (with {placeholder} args) as one tool on the user's machine — the lightweight option. messaging_bridge = enable a built-in messaging relay (iMessage) on the user's device so their chats route to agents. rest_messaging = a server-side two-sided messaging bridge for a REST-pollable service (Teams/Slack/Discord) via a SecureAPI credential — use preset=\"teams\" for the canned Graph mapping. rest_image = an image-GENERATION backend (ComfyUI / Automatic1111 / hosted diffusion) declared from a spec — use preset=\"a1111\" (turnkey) or preset=\"comfyui\" with vars={\"base_url\":\"http://localhost:7860\"}; materializes a generate_image_<name> tool. Created UNAPPROVED."},
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
			"service":          {Type: "string", Description: "(messaging_bridge) the built-in service to bridge — only \"imessage\" today. (rest_messaging) a name that namespaces the bridge, e.g. \"teams\" (the preset sets it)."},
			"poll_secs":        {Type: "number", Description: "(messaging_bridge, optional) how often the device relay polls for new messages, in seconds (default 5)."},
			"preset":           {Type: "string", Description: "(rest_messaging, optional) a canned service template that fills poll_url/map/cursor/send_url. \"teams\" (Graph, needs an oauth2 credential) or \"slack\" (Web API, needs a bearer credential holding a bot token). Explicit fields still override the preset."},
			"vars":             {Type: "object", Description: "(rest_messaging) values substituted into {token}s in the preset's URLs/chat id. teams: {\"team_id\":\"...\",\"channel_id\":\"...\"}. slack: {\"channel_id\":\"C...\"}."},
			"poll_url":         {Type: "string", Description: "(rest_messaging) absolute list endpoint polled each interval (the preset provides this; omit when using a preset+vars)."},
			"interval_secs":    {Type: "number", Description: "(rest_messaging, optional) poll cadence in seconds (default 30, min 5)."},
			"list_path":        {Type: "string", Description: "(rest_messaging) dot-path to the message array in the response (e.g. \"value\"). Empty = the response root is the array."},
			"map":              {Type: "object", Description: "(rest_messaging) element-relative dot-paths pulling fields from each message: chat_id (required), text (required), msg_id, sender, sender_name, conv_name, timestamp. E.g. {\"chat_id\":\"channelIdentity.channelId\",\"text\":\"body.content\"}."},
			"next_url_path":    {Type: "string", Description: "(rest_messaging, cursor mode A) response dot-path whose value replaces poll_url next tick (Graph \"@odata.deltaLink\")."},
			"cursor_path":      {Type: "string", Description: "(rest_messaging, cursor mode B) response dot-path whose value is injected as a query param (cursor_param) next tick."},
			"cursor_param":     {Type: "string", Description: "(rest_messaging, cursor mode B) query-param name carrying cursor_path's value on the next poll (e.g. \"cursor\", \"after\")."},
			"send_url":         {Type: "string", Description: "(rest_messaging) absolute send endpoint for replies; may contain {chat_id}. Omit for an inbound-only bridge."},
			"send_method":      {Type: "string", Description: "(rest_messaging, optional) send HTTP method (default POST)."},
			"send_body":        {Type: "string", Description: "(rest_messaging) JSON body template for a reply; {text} and {chat_id} are substituted and JSON-escaped."},
			"chat_id_const":    {Type: "string", Description: "(rest_messaging, optional) a FIXED chat id for every message, for services whose messages omit their conversation id (Slack). Takes precedence over map.chat_id."},
			"more_url_path":    {Type: "string", Description: "(rest_messaging, optional) response dot-path to a complete next-page URL followed within a tick until absent (Graph \"@odata.nextLink\")."},
			"webhook_provider": {Type: "string", Enum: []string{"slack", "graph"}, Description: "(rest_messaging, optional) switch inbound from POLL to real-time PUSH. \"slack\" (Slack Events API — turnkey: paste the webhook URL into the Slack app, admin sets the signing secret) or \"graph\". The poll fields become unused; send_url/credential still deliver replies."},
			"image_spec":       {Type: "object", Description: "(rest_image, optional) explicit backend fields overriding/extending the preset: submit_url, submit_method, submit_body (a JSON template with {prompt}/{negative}/{width}/{height}/{steps}/{seed} tokens), image_b64_path or image_url_path (synchronous result), or the poll set submit_id_path/poll_url/poll_ready_path/poll_b64_path/poll_url_path/poll_url_template/poll_fields (async). Omit when a preset + vars is enough. For rest_image, `credential` names the SecureAPI credential (or \"no_auth\" for a local endpoint) and `vars` fills preset tokens like {\"base_url\":\"http://localhost:7860\"}."},
			"description":      {Type: "string", Description: "(optional) What this connector is for. For desktop_command it is also the tool's description shown to callers."},
		},
		Required: []string{"kind", "name"},
		Handler:  connectorCreate,
	})
	gt.AddAction("update", &GroupedToolAction{
		Description: "Change an EXISTING connector's fields WITHOUT recreating it — use when a preset value or mapping needs fixing later. Only the fields you pass change; the rest are kept. The kind can't change. If the connector is already approved (live), it re-materializes immediately (a rest_messaging poller restarts with the new spec, resuming from its cursor); an unapproved one just updates its draft. To CLEAR a field or switch preset/kind, delete and recreate instead.",
		Params: map[string]ToolParam{
			"name":             {Type: "string", Description: "The connector to update."},
			"description":      {Type: "string", Description: "(optional) New description."},
			"url":              {Type: "string", Description: "(remote_mcp / rest_poll) new URL."},
			"auth_mode":        {Type: "string", Enum: []string{"none", "secure_api", "oauth"}, Description: "(remote_mcp) new auth mode."},
			"secure_cred":      {Type: "string", Description: "(remote_mcp) new SecureAPI credential name."},
			"credential":       {Type: "string", Description: "(rest_poll / rest_messaging) new credential name."},
			"wake_agent":       {Type: "string", Description: "(rest_poll) new agent to wake."},
			"interval_minutes": {Type: "number", Description: "(rest_poll) new poll interval, minutes."},
			"wake_brief":       {Type: "string", Description: "(rest_poll) new wake guidance."},
			"method":           {Type: "string", Description: "(rest_poll / rest_messaging) new poll HTTP method."},
			"body":             {Type: "string", Description: "(rest_poll / rest_messaging) new poll request body."},
			"command":          {Type: "string", Description: "(desktop_mcp / desktop_command) new command."},
			"args":             {Type: "array", Description: "(desktop_mcp / desktop_command) new args list (replaces the whole list)."},
			"params":           {Type: "object", Description: "(desktop_command) new params {name: description} (replaces the whole set)."},
			"service":          {Type: "string", Description: "(messaging_bridge / rest_messaging) new service name."},
			"poll_secs":        {Type: "number", Description: "(messaging_bridge) new device relay poll seconds."},
			"poll_url":         {Type: "string", Description: "(rest_messaging) new poll endpoint."},
			"interval_secs":    {Type: "number", Description: "(rest_messaging) new poll cadence, seconds."},
			"list_path":        {Type: "string", Description: "(rest_messaging) new message-array dot-path."},
			"map":              {Type: "object", Description: "(rest_messaging) message field dot-paths to change (merged per-field into the existing map)."},
			"next_url_path":    {Type: "string", Description: "(rest_messaging) new cross-tick cursor dot-path."},
			"cursor_path":      {Type: "string", Description: "(rest_messaging) new cursor-value dot-path."},
			"cursor_param":     {Type: "string", Description: "(rest_messaging) new cursor query-param name."},
			"more_url_path":    {Type: "string", Description: "(rest_messaging) new within-tick next-page dot-path."},
			"chat_id_const":    {Type: "string", Description: "(rest_messaging) new fixed chat id."},
			"send_url":         {Type: "string", Description: "(rest_messaging) new reply send endpoint."},
			"send_method":      {Type: "string", Description: "(rest_messaging) new send HTTP method."},
			"send_body":        {Type: "string", Description: "(rest_messaging) new reply body template."},
			"webhook_provider": {Type: "string", Enum: []string{"slack", "graph"}, Description: "(rest_messaging) switch inbound to a real-time webhook provider."},
		},
		Required: []string{"name"},
		Handler:  connectorUpdate,
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
	gt.AddAction("export", &GroupedToolAction{
		Description: "Export connector(s) as a portable, SECRET-FREE JSON pack the user can save, back up, or share. Omit name to export ALL connectors as one pack; pass name for a single one. Auth travels by credential NAME only — no secret is included.",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "(optional) A single connector to export. Omit to export every connector as one pack."}},
		Handler:     connectorExport,
	})
	gt.AddAction("import", &GroupedToolAction{
		Description: "Import a connector pack (the JSON produced by export) as new DRAFT connectors owned by the user. Governance still applies: remote_mcp / desktop_* land UNAPPROVED (an admin must approve them); rest_poll goes live if its credential exists. A name that already exists is SKIPPED, never overwritten. Referenced credentials must exist (or be drafted) on this install.",
		Params:      map[string]ToolParam{"pack": {Type: "string", Description: "The connector pack JSON — a full pack {\"bundle\":...,\"connectors\":[...]}, a single connector object, or an array of connectors."}},
		Required:    []string{"pack"},
		Handler:     connectorImport,
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

// imageSpecArg maps the `image_spec` object arg onto a RestImageSpec by
// marshaling the raw map and unmarshaling into the typed struct — so every
// declared field (submit_url, submit_body, the poll set, defaults) flows through
// by its JSON tag without per-field wiring. Returns the zero spec when absent.
func imageSpecArg(args map[string]any, key string) RestImageSpec {
	var s RestImageSpec
	m, ok := args[key].(map[string]any)
	if !ok {
		return s
	}
	if b, err := json.Marshal(m); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
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
	case MessagingBridgeConnectorKind:
		if owner == "" {
			return "", fmt.Errorf("messaging_bridge requires an authenticated session (it enables a relay on your own device)")
		}
		spec := MessagingBridgeSpec{
			Service:  strings.ToLower(strings.TrimSpace(stringArg(args, "service"))),
			PollSecs: oArgInt(args, "poll_secs"),
		}
		raw, _ := json.Marshal(spec)
		c.Spec = raw
	case RestMessagingConnectorKind:
		if owner == "" {
			return "", fmt.Errorf("rest_messaging requires an authenticated session (its channel agents run as you)")
		}
		fm := stringMapArg(args, "map")
		over := RestMessagingSpec{
			Service:      strings.TrimSpace(stringArg(args, "service")),
			Credential:   strings.TrimSpace(stringArg(args, "credential")),
			PollURL:      strings.TrimSpace(stringArg(args, "poll_url")),
			Method:       strings.TrimSpace(stringArg(args, "method")),
			Body:         stringArg(args, "body"),
			IntervalSecs: oArgInt(args, "interval_secs"),
			ListPath:     strings.TrimSpace(stringArg(args, "list_path")),
			NextURLPath:  strings.TrimSpace(stringArg(args, "next_url_path")),
			CursorPath:   strings.TrimSpace(stringArg(args, "cursor_path")),
			CursorParam:  strings.TrimSpace(stringArg(args, "cursor_param")),
			SendURL:         strings.TrimSpace(stringArg(args, "send_url")),
			SendMethod:      strings.TrimSpace(stringArg(args, "send_method")),
			SendBody:        stringArg(args, "send_body"),
			ChatIDConst:     strings.TrimSpace(stringArg(args, "chat_id_const")),
			MoreURLPath:     strings.TrimSpace(stringArg(args, "more_url_path")),
			WebhookProvider: strings.TrimSpace(stringArg(args, "webhook_provider")),
			Map: RestMessagingFieldMap{
				ChatID:     strings.TrimSpace(fm["chat_id"]),
				MsgID:      strings.TrimSpace(fm["msg_id"]),
				Sender:     strings.TrimSpace(fm["sender"]),
				SenderName: strings.TrimSpace(fm["sender_name"]),
				Text:       strings.TrimSpace(fm["text"]),
				ConvName:   strings.TrimSpace(fm["conv_name"]),
				Timestamp:  strings.TrimSpace(fm["timestamp"]),
			},
		}
		spec, err := ApplyRestMessagingPreset(stringArg(args, "preset"), over, stringMapArg(args, "vars"))
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(spec)
		c.Spec = raw
	case RestImageConnectorKind:
		over := imageSpecArg(args, "image_spec")
		if cred := strings.TrimSpace(stringArg(args, "credential")); cred != "" {
			over.Credential = cred
		}
		spec, err := ApplyRestImagePreset(stringArg(args, "preset"), over, stringMapArg(args, "vars"))
		if err != nil {
			return "", err
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

// argSet reports whether the caller explicitly passed a key (present but empty
// still counts as "provided" — distinct from omitted).
func argSet(args map[string]any, key string) bool {
	_, ok := args[key]
	return ok
}

// patchStr returns the provided arg (trimmed) when set, else the current value.
func patchStr(cur string, args map[string]any, key string) string {
	if argSet(args, key) {
		return strings.TrimSpace(stringArg(args, key))
	}
	return cur
}

// patchInt returns the provided int arg when set, else the current value.
func patchInt(cur int, args map[string]any, key string) int {
	if argSet(args, key) {
		return oArgInt(args, key)
	}
	return cur
}

// connectorUpdate patches an existing connector's fields in place. Only provided
// fields change; the kind is fixed. SaveConnector re-validates and, if the
// connector is approved, re-materializes so the change takes effect immediately.
func connectorUpdate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	prev, ok := GetConnector(RootDB, name)
	if !ok {
		return "", fmt.Errorf("no connector named %q — create it first", name)
	}
	owner := bridgeOwner(sess)
	c := prev
	if argSet(args, "description") {
		c.Desc = strings.TrimSpace(stringArg(args, "description"))
	}

	switch prev.Kind {
	case RemoteMCPConnectorKind:
		var s RemoteMCPSpec
		_ = json.Unmarshal(prev.Spec, &s)
		s.URL = patchStr(s.URL, args, "url")
		if argSet(args, "auth_mode") {
			s.AuthMode = normalizeConnectorAuth(stringArg(args, "auth_mode"))
		}
		s.SecureCred = patchStr(s.SecureCred, args, "secure_cred")
		c.Spec, _ = json.Marshal(s)
	case RestPollConnectorKind:
		var s RestPollSpec
		_ = json.Unmarshal(prev.Spec, &s)
		s.Credential = patchStr(s.Credential, args, "credential")
		s.URL = patchStr(s.URL, args, "url")
		s.Method = patchStr(s.Method, args, "method")
		s.Body = patchStr(s.Body, args, "body")
		s.WakeBrief = patchStr(s.WakeBrief, args, "wake_brief")
		s.IntervalMinutes = patchInt(s.IntervalMinutes, args, "interval_minutes")
		if argSet(args, "wake_agent") {
			want := strings.TrimSpace(stringArg(args, "wake_agent"))
			wa := resolveCheckAgent(sess, owner, want, "")
			if wa == "" {
				return "", fmt.Errorf("no agent named %q to wake", want)
			}
			s.WakeAgent = wa
		}
		c.Spec, _ = json.Marshal(s)
	case DesktopMCPConnectorKind:
		var s DesktopMCPSpec
		_ = json.Unmarshal(prev.Spec, &s)
		s.Command = patchStr(s.Command, args, "command")
		if argSet(args, "args") {
			s.Args = stringSliceArg(args, "args")
		}
		c.Spec, _ = json.Marshal(s)
	case DesktopCommandConnectorKind:
		var s DesktopCommandSpec
		_ = json.Unmarshal(prev.Spec, &s)
		s.Command = patchStr(s.Command, args, "command")
		if argSet(args, "args") {
			s.Args = stringSliceArg(args, "args")
		}
		if argSet(args, "description") {
			s.Desc = c.Desc // desktop_command shows Desc as the tool's description
		}
		if argSet(args, "params") {
			var params map[string]ToolParam
			var required []string
			for pn, pd := range stringMapArg(args, "params") {
				if params == nil {
					params = map[string]ToolParam{}
				}
				params[pn] = ToolParam{Type: "string", Description: pd}
				required = append(required, pn)
			}
			s.Params, s.Required = params, required
		}
		c.Spec, _ = json.Marshal(s)
	case MessagingBridgeConnectorKind:
		var s MessagingBridgeSpec
		_ = json.Unmarshal(prev.Spec, &s)
		if argSet(args, "service") {
			s.Service = strings.ToLower(strings.TrimSpace(stringArg(args, "service")))
		}
		s.PollSecs = patchInt(s.PollSecs, args, "poll_secs")
		c.Spec, _ = json.Marshal(s)
	case RestMessagingConnectorKind:
		var existing RestMessagingSpec
		_ = json.Unmarshal(prev.Spec, &existing)
		fm := stringMapArg(args, "map")
		// Build an overlay from the provided args; MergeRestMessagingSpec keeps the
		// existing value for every field left empty, giving per-field partial patch.
		over := RestMessagingSpec{
			Service:      strings.TrimSpace(stringArg(args, "service")),
			Credential:   strings.TrimSpace(stringArg(args, "credential")),
			PollURL:      strings.TrimSpace(stringArg(args, "poll_url")),
			Method:       strings.TrimSpace(stringArg(args, "method")),
			Body:         stringArg(args, "body"),
			IntervalSecs: oArgInt(args, "interval_secs"),
			ListPath:     strings.TrimSpace(stringArg(args, "list_path")),
			ChatIDConst:  strings.TrimSpace(stringArg(args, "chat_id_const")),
			MoreURLPath:  strings.TrimSpace(stringArg(args, "more_url_path")),
			NextURLPath:  strings.TrimSpace(stringArg(args, "next_url_path")),
			CursorPath:   strings.TrimSpace(stringArg(args, "cursor_path")),
			CursorParam:  strings.TrimSpace(stringArg(args, "cursor_param")),
			SendURL:         strings.TrimSpace(stringArg(args, "send_url")),
			SendMethod:      strings.TrimSpace(stringArg(args, "send_method")),
			SendBody:        stringArg(args, "send_body"),
			WebhookProvider: strings.TrimSpace(stringArg(args, "webhook_provider")),
			Map: RestMessagingFieldMap{
				ChatID:     strings.TrimSpace(fm["chat_id"]),
				MsgID:      strings.TrimSpace(fm["msg_id"]),
				Sender:     strings.TrimSpace(fm["sender"]),
				SenderName: strings.TrimSpace(fm["sender_name"]),
				Text:       strings.TrimSpace(fm["text"]),
				ConvName:   strings.TrimSpace(fm["conv_name"]),
				Timestamp:  strings.TrimSpace(fm["timestamp"]),
			},
		}
		c.Spec, _ = json.Marshal(MergeRestMessagingSpec(existing, over))
	case RestImageConnectorKind:
		var existing RestImageSpec
		_ = json.Unmarshal(prev.Spec, &existing)
		// Overlay: image_spec fields + credential win; empty fields keep existing.
		over := imageSpecArg(args, "image_spec")
		if cred := strings.TrimSpace(stringArg(args, "credential")); cred != "" {
			over.Credential = cred
		}
		merged := MergeRestImageSpec(existing, over)
		// Re-apply any vars (e.g. a changed base_url) across the URL/body/path fields.
		if vars := stringMapArg(args, "vars"); len(vars) > 0 {
			merged, _ = ApplyRestImagePreset("", merged, vars)
		}
		c.Spec, _ = json.Marshal(merged)
	default:
		return "", fmt.Errorf("update not supported for kind %q — delete and recreate instead", prev.Kind)
	}

	if err := SaveConnector(RootDB, c); err != nil {
		return "", err
	}
	saved, _ := GetConnector(RootDB, name)
	state := "draft updated (still UNAPPROVED)"
	if saved.Approved {
		state = "updated and RE-MATERIALIZED (live now)"
	}
	return fmt.Sprintf("Connector %q %s. %s", name, state, ConnectorSummary(saved)), nil
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
	// rest_messaging goes a step past shape validation: do one live poll and show
	// the mapped first message, so the author can confirm the dot-paths before
	// approval. Needs the credential enabled by an admin.
	if c.Kind == RestMessagingConnectorKind {
		preview, err := ProbeMessagingConnector(c)
		if err != nil {
			return "", fmt.Errorf("%q validates, but the live poll failed: %w\n(If the credential isn't enabled yet, an admin must add its secret and enable it in Admin > APIs first.)", name, err)
		}
		return fmt.Sprintf("Connector %q validates. %s\n%s\nApprove it in Admin > Connectors to go live.", name, ConnectorSummary(c), preview), nil
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

func connectorExport(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	var names []string
	if name != "" {
		if _, ok := GetConnector(RootDB, name); !ok {
			return "", fmt.Errorf("no connector named %q", name)
		}
		names = []string{name}
	}
	pack, err := ExportConnectorPack(RootDB, names...)
	if err != nil {
		return "", err
	}
	if len(pack.Connectors) == 0 {
		return "No connectors to export yet.", nil
	}
	raw, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Exported %d connector(s) as a portable, secret-free pack (auth is referenced by credential name only — no secrets included). Save this JSON, or import it on another install with connector(action=\"import\"):\n\n```json\n%s\n```",
		len(pack.Connectors), string(raw)), nil
}

func connectorImport(args map[string]any, sess *ToolSession) (string, error) {
	owner := bridgeOwner(sess)
	if owner == "" {
		return "", fmt.Errorf("import requires an authenticated session (imported connectors are owned by you)")
	}
	raw := strings.TrimSpace(stringArg(args, "pack"))
	if raw == "" {
		return "", fmt.Errorf("pack is required — paste the JSON produced by connector(action=\"export\")")
	}
	res, err := ImportConnectorPack(RootDB, []byte(raw), owner)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(res.Imported) == 0 {
		b.WriteString("No connectors imported.")
	} else {
		fmt.Fprintf(&b, "Imported %d connector(s): %s.\n", len(res.Imported), strings.Join(res.Imported, ", "))
		b.WriteString("remote_mcp / desktop_* land UNAPPROVED — an admin approves them in Admin > Connectors before their tools go live. rest_poll goes live if its credential is already registered.")
	}
	if len(res.Skipped) > 0 {
		b.WriteString("\nSkipped:")
		for _, s := range res.Skipped {
			fmt.Fprintf(&b, "\n  - %s: %s", s.Name, s.Reason)
		}
	}
	return strings.TrimSpace(b.String()), nil
}
