// Package temptool provides three meta-tools the LLM can use to define,
// list, and remove session-scoped tools at runtime:
//
//   - create_temp_tool: register a new tool whose body is a shell command
//     template. Visible to the LLM on the next round and from then on.
//   - list_temp_tools:   inspect what's currently defined.
//   - delete_temp_tool:  remove one by name.
//
// Temp tools execute through the same sandbox as run_local
// (RunSandboxedShell), so they inherit the bubblewrap mount-namespace
// isolation when bwrap is available. They cannot escape the workspace
// or read files outside it. They CAN make network calls (curl an API,
// download a font) — gate at the AllowedCaps tier if that's not desired.
//
// All three tools require CapExecute. The temp tool a session defines
// also runs at CapExecute. Runtime tool registration cannot grant the
// LLM capabilities it didn't already have.
package temptool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// commandTimeout caps wall-clock time per temp-tool invocation. Same as
// run_local — long-running commands get killed.
const commandTimeout = 90 * time.Second

// maxOutput is the per-call output cap, same as run_local.
const maxOutput = 10000

func init() {
	RegisterChatTool(&CreateTempToolTool{})
	RegisterChatTool(&ListTempToolsTool{})
	RegisterChatTool(&DeleteTempToolTool{})
	RegisterChatTool(&CreateAPIToolTool{})
}

// ----------------------------------------------------------------------
// create_temp_tool
// ----------------------------------------------------------------------

type CreateTempToolTool struct{}

func (t *CreateTempToolTool) Name() string         { return "create_temp_tool" }
func (t *CreateTempToolTool) Caps() []Capability   { return []Capability{CapExecute} }
func (t *CreateTempToolTool) NeedsConfirm() bool   { return true }

func (t *CreateTempToolTool) Desc() string {
	return "Define a new tool for this session. The tool runs a shell command template you supply; placeholders like {arg_name} are filled with the caller's arguments (shell-quoted to prevent injection). The tool appears in your catalog on the next round and stays available for the rest of this session. Use this when you find yourself re-issuing the same shell command pattern with different inputs (e.g. resizing many images, batch-converting files, scraping a series of URLs). Runs in the same workspace sandbox as run_local — cannot reach files outside the workspace. Requires user confirmation."
}

func (t *CreateTempToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {
			Type:        "string",
			Description: "Tool name (snake_case, must not match an existing tool). E.g. \"resize_image\".",
		},
		"description": {
			Type:        "string",
			Description: "What the tool does. Shown to you in your future tool catalog so make it clear when to call this tool vs. another.",
		},
		"params": {
			Type:        "object",
			Description: "Object describing the tool's parameters. Each key is a param name and its value is an object {type, description, [required]}. Type must be \"string\", \"integer\", \"number\", or \"boolean\". E.g. {\"input\": {\"type\": \"string\", \"description\": \"Input file path\"}, \"size\": {\"type\": \"string\", \"description\": \"Target dimensions like 800x600\"}}.",
		},
		"command_template": {
			Type:        "string",
			Description: "Shell command to run. Use {param_name} placeholders that match keys in `params`. Each placeholder is replaced with the shell-quoted arg value at call time. Standard sh -c semantics. Example: \"convert {input} -resize {size} {input}.resized.png\".",
		},
		"required": {
			Type:        "array",
			Description: "Optional list of param names that must be provided. Defaults to all of them.",
		},
		"persist": {
			Type:        "boolean",
			Description: "If true, request that this tool be saved across future sessions. The tool is registered for the current session immediately, but will only appear in subsequent sessions after the user approves it via the admin UI. Default false (session-only). Use only for tools you expect to reuse next time.",
		},
	}
}

func (t *CreateTempToolTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("create_temp_tool requires a session")
}

func (t *CreateTempToolTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("create_temp_tool requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !validToolName(name) {
		return "", fmt.Errorf("name must be lowercase letters / digits / underscores only (got %q)", name)
	}
	// Reject collisions with the static catalog so the LLM can't
	// shadow a real tool with a temp one and confuse later dispatch.
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == name {
			return "", fmt.Errorf("name %q collides with a registered tool — pick another", name)
		}
	}

	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", fmt.Errorf("description is required")
	}

	cmd := strings.TrimSpace(StringArg(args, "command_template"))
	if cmd == "" {
		return "", fmt.Errorf("command_template is required")
	}

	params, err := parseParamsArg(args["params"])
	if err != nil {
		return "", fmt.Errorf("params: %w", err)
	}

	// Validate that every {placeholder} in the template names a real
	// param. Forgotten placeholders would silently leak literal `{x}`
	// into the shell, which is a footgun.
	if err := validateTemplate(cmd, params); err != nil {
		return "", fmt.Errorf("command_template: %w", err)
	}

	required := stringSliceArg(args["required"])
	if len(required) == 0 {
		// Default to all params required.
		for k := range params {
			required = append(required, k)
		}
	} else {
		// Validate all listed required keys exist in params.
		for _, r := range required {
			if _, ok := params[r]; !ok {
				return "", fmt.Errorf("required lists %q which is not in params", r)
			}
		}
	}

	tool := &TempTool{
		Name:            name,
		Description:     desc,
		Params:          params,
		Required:        required,
		CommandTemplate: cmd,
	}
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}

	// Persist request: queue for human approval. The tool is already
	// usable in this session (just registered above); persistence is
	// what makes it survive into future sessions, and that requires
	// human review of the command_template.
	persist := BoolArg(args, "persist")
	if persist {
		if sess.DB == nil || sess.Username == "" {
			// Persistence not supported in this app/session (e.g.
			// phantom, or unauthenticated chat). Tell the LLM clearly
			// so it doesn't assume the tool will survive — it won't.
			return fmt.Sprintf("Created temp tool %q for this session. Persistence was requested but is not available in this app/session, so the tool will be discarded when the session ends. Treat it as session-scoped.", name), nil
		}
		if err := QueuePendingTempTool(sess.DB, sess.Username, *tool, ""); err != nil {
			// Queue failure isn't fatal — the in-session tool still
			// works; the LLM just learns persistence didn't take.
			return fmt.Sprintf("Created temp tool %q for this session. Persistence requested but queueing failed: %v. Tool will be discarded at session end.", name, err), nil
		}
		return fmt.Sprintf("Created temp tool %q for this session. Persistence requested — queued for user approval via the admin UI. The user will review the command template before it becomes available in future sessions.", name), nil
	}
	return fmt.Sprintf("Created temp tool %q. It is available in your tool catalog on the next round of this session only.", name), nil
}

// ----------------------------------------------------------------------
// list_temp_tools
// ----------------------------------------------------------------------

type ListTempToolsTool struct{}

func (t *ListTempToolsTool) Name() string       { return "list_temp_tools" }
func (t *ListTempToolsTool) Caps() []Capability { return []Capability{CapExecute} }

func (t *ListTempToolsTool) Desc() string {
	return "List the temp tools currently defined for this session. Returns each tool's name, description, parameters, and command template — useful for reviewing what you've built before deciding whether to add another."
}

func (t *ListTempToolsTool) Params() map[string]ToolParam { return map[string]ToolParam{} }

func (t *ListTempToolsTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("list_temp_tools requires a session")
}

func (t *ListTempToolsTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("list_temp_tools requires a session")
	}
	// Categorize: each in-session tool is either loaded-from-persistent,
	// pending-approval, or session-only. Look up persistence state by
	// name once.
	persistentByName := make(map[string]bool)
	pendingByName := make(map[string]bool)
	if sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			persistentByName[p.Tool.Name] = true
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			pendingByName[p.Tool.Name] = true
		}
	}

	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 {
		return "No temp tools defined in this session.", nil
	}
	var b strings.Builder
	for i, t := range tools {
		var tag string
		switch {
		case persistentByName[t.Name]:
			tag = " [persistent]"
		case pendingByName[t.Name]:
			tag = " [pending approval]"
		default:
			tag = " [session-only]"
		}
		fmt.Fprintf(&b, "%d. %s%s — %s\n", i+1, t.Name, tag, t.Description)
		fmt.Fprintf(&b, "   command: %s\n", t.CommandTemplate)
		if len(t.Params) > 0 {
			b.WriteString("   params: ")
			first := true
			for k, p := range t.Params {
				if !first {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s (%s)", k, p.Type)
				first = false
			}
			b.WriteString("\n")
		}
	}
	// Also list pending tools that aren't currently in the session
	// (e.g. requested in a prior session, still waiting on approval).
	var orphanPending []string
	inSession := make(map[string]bool, len(tools))
	for _, t := range tools {
		inSession[t.Name] = true
	}
	for name := range pendingByName {
		if !inSession[name] {
			orphanPending = append(orphanPending, name)
		}
	}
	if len(orphanPending) > 0 {
		b.WriteString("\nPending approval (not yet visible to the LLM until approved): ")
		b.WriteString(strings.Join(orphanPending, ", "))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ----------------------------------------------------------------------
// delete_temp_tool
// ----------------------------------------------------------------------

type DeleteTempToolTool struct{}

func (t *DeleteTempToolTool) Name() string       { return "delete_temp_tool" }
func (t *DeleteTempToolTool) Caps() []Capability { return []Capability{CapExecute} }

func (t *DeleteTempToolTool) Desc() string {
	return "Remove a temp tool from this session. Use when a tool you defined is no longer needed or you want to redefine it (delete then create_temp_tool again with the new shape)."
}

func (t *DeleteTempToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {Type: "string", Description: "Name of the temp tool to remove."},
	}
}

func (t *DeleteTempToolTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("delete_temp_tool requires a session")
}

func (t *DeleteTempToolTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("delete_temp_tool requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	removed := sess.RemoveTempTool(name)

	// Also clear from the persistent pool if applicable. The LLM may
	// be deleting a tool it loaded from persistence at session start,
	// expecting the deletion to stick across sessions.
	persistRemoved := false
	if sess.DB != nil && sess.Username != "" {
		if err := DeletePersistentTempTool(sess.DB, sess.Username, name); err == nil {
			persistRemoved = true
		}
	}

	switch {
	case removed && persistRemoved:
		return fmt.Sprintf("Removed temp tool %q from this session and from your persistent pool.", name), nil
	case removed:
		return fmt.Sprintf("Removed temp tool %q from this session.", name), nil
	case persistRemoved:
		return fmt.Sprintf("Removed temp tool %q from your persistent pool. (It wasn't in the current session.)", name), nil
	default:
		return "", fmt.Errorf("no temp tool named %q in this session or your persistent pool", name)
	}
}

// ----------------------------------------------------------------------
// Conversion + dispatch
// ----------------------------------------------------------------------

// BuildAgentToolDefs converts a session's temp tools into AgentToolDefs
// suitable for AgentLoopConfig.DynamicTools. Each tool's handler
// substitutes the caller's args into the command template (shell-quoted
// to prevent injection) and runs through RunSandboxedShell.
func BuildAgentToolDefs(sess *ToolSession) []AgentToolDef {
	if sess == nil {
		return nil
	}
	tools := sess.CopyTempTools()
	if len(tools) == 0 {
		return nil
	}
	out := make([]AgentToolDef, 0, len(tools))
	for _, tt := range tools {
		out = append(out, agentToolFromTemp(sess, tt))
	}
	return out
}

func agentToolFromTemp(sess *ToolSession, tt *TempTool) AgentToolDef {
	// Caps depend on execution mode. Shell mode requires CapExecute
	// (sandboxed shell), API mode requires CapNetwork (HTTP call via
	// stored credential). The AllowedCaps filter then hides the tool
	// from sessions that don't grant the required tier.
	var caps []Capability
	descSuffix := " (temp tool — defined this session via create_temp_tool)"
	switch tt.Mode {
	case TempToolModeAPI:
		caps = []Capability{CapNetwork}
		descSuffix = fmt.Sprintf(" (api tool — wraps credential %q, defined this session via create_api_tool)", tt.Credential)
	default:
		caps = []Capability{CapExecute}
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        tt.Name,
			Description: tt.Description + descSuffix,
			Parameters:  tt.Params,
			Required:    tt.Required,
			Caps:        caps,
		},
		// Confirm so each temp-tool invocation goes through the same
		// approval prompt run_local does. The LLM defined the tool but
		// the user still sees each call.
		NeedsConfirm: true,
		Handler: func(args map[string]any) (string, error) {
			return dispatchTempTool(sess, tt, args)
		},
	}
}

// dispatchTempTool routes to the right execution path based on the
// tool's Mode. Shell mode runs through RunSandboxedShell. API mode
// substitutes URL/body templates and dispatches through the secure-
// API call path against the named credential.
func dispatchTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("temp tool %q requires a session", tt.Name)
	}
	// Required-arg check (applies to both modes).
	for _, r := range tt.Required {
		if _, ok := args[r]; !ok {
			return "", fmt.Errorf("missing required arg %q", r)
		}
	}

	if tt.Mode == TempToolModeAPI {
		return dispatchAPIModeTempTool(sess, tt, args)
	}

	if sess.WorkspaceDir == "" {
		return "", fmt.Errorf("temp tool %q requires a session with WorkspaceDir set", tt.Name)
	}
	// Shell mode: substitute with shell-quoted args.
	cmd, err := substitute(tt.CommandTemplate, tt.Params, args)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	// Network access defaults to off — temp tools that touch the wire
	// must be opted in via per-tool flag (future). For now, all temp
	// tools run with --unshare-net... actually leave network on so
	// existing tools keep working; the per-tool network flag is the
	// next pass.
	res := RunSandboxedShell(ctx, cmd, sess.WorkspaceDir)
	output := strings.TrimSpace(res.Output)

	// Telemetry: bump LastUsedAt on the persistent record (if this is
	// a persistent tool). Best-effort — no-op when the tool isn't
	// persisted or the session lacks DB/Username.
	if sess.DB != nil && sess.Username != "" {
		TouchPersistentTempTool(sess.DB, sess.Username, tt.Name)
	}

	if len(output) > maxOutput {
		totalLines := strings.Count(output, "\n") + 1
		truncated := output[:maxOutput]
		shown := strings.Count(truncated, "\n") + 1
		output = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars).]",
			shown, totalLines, len(output))
	}

	if res.TimedOut {
		notice := fmt.Sprintf("\n[TIMED OUT after %s — command killed.]", commandTimeout)
		if output == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return output + notice, nil
	}
	if res.Err != nil {
		if output == "" {
			return fmt.Sprintf("[exit: %v — no output]", res.Err), nil
		}
		return output + fmt.Sprintf("\n[exit: %v]", res.Err), nil
	}
	return output, nil
}

// ----------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------

// validToolName: lowercase letters, digits, underscores only. Length
// 1-64. Mirrors the snake_case convention in the rest of the codebase.
func validToolName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

// parseParamsArg accepts the LLM's `params` object and converts it to
// our typed ToolParam map. Tolerates two shapes the LLM commonly emits:
// a real JSON object, or a JSON-encoded string of one.
func parseParamsArg(v any) (map[string]ToolParam, error) {
	if v == nil {
		return nil, fmt.Errorf("required")
	}
	// Re-marshal whatever we got and unmarshal into our typed map. This
	// handles both the native object form and the stringified form the
	// LLM might produce when it emits a JSON blob.
	var raw any
	if s, ok := v.(string); ok {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, fmt.Errorf("could not parse params JSON: %w", err)
		}
	} else {
		raw = v
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-marshal failed: %w", err)
	}
	out := map[string]ToolParam{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("must be an object of {name: {type, description}}: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one param is required")
	}
	for k, p := range out {
		if !validToolName(k) {
			return nil, fmt.Errorf("param name %q must be lowercase letters/digits/underscores only", k)
		}
		switch p.Type {
		case "string", "integer", "number", "boolean":
		case "":
			return nil, fmt.Errorf("param %q has no type — set type to string/integer/number/boolean", k)
		default:
			return nil, fmt.Errorf("param %q type %q not supported (use string/integer/number/boolean)", k, p.Type)
		}
	}
	return out, nil
}

func stringSliceArg(v any) []string {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		var out []string
		for _, e := range arr {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// validateTemplate scans cmd for `{name}` placeholders and ensures each
// one names a known param. Catches the obvious "I forgot to add this
// to params" mistake before the tool gets registered.
func validateTemplate(cmd string, params map[string]ToolParam) error {
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '{' {
			continue
		}
		end := strings.IndexByte(cmd[i+1:], '}')
		if end < 0 {
			return fmt.Errorf("unclosed placeholder at offset %d", i)
		}
		name := cmd[i+1 : i+1+end]
		if name == "" {
			return fmt.Errorf("empty placeholder at offset %d", i)
		}
		if _, ok := params[name]; !ok {
			return fmt.Errorf("placeholder {%s} not in params", name)
		}
		i = i + 1 + end
	}
	return nil
}

// substitute replaces {name} placeholders in cmd with shell-quoted arg
// values. Unknown args (placeholders without a corresponding key in
// args) result in an error rather than silent dropping.
func substitute(cmd string, params map[string]ToolParam, args map[string]any) (string, error) {
	var b strings.Builder
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '{' {
			b.WriteByte(cmd[i])
			continue
		}
		end := strings.IndexByte(cmd[i+1:], '}')
		if end < 0 {
			b.WriteByte(cmd[i])
			continue
		}
		name := cmd[i+1 : i+1+end]
		if _, known := params[name]; !known {
			// Not a placeholder we recognize — emit verbatim so the
			// LLM can use literal braces in its command if needed.
			b.WriteByte(cmd[i])
			continue
		}
		val, ok := args[name]
		if !ok {
			return "", fmt.Errorf("missing arg %q", name)
		}
		b.WriteString(shellQuote(stringify(val)))
		i = i + 1 + end
	}
	return b.String(), nil
}

// stringify renders any JSON-decoded value as a string suitable for
// shell substitution. Numbers come back as float64 from json — we
// format them as integers when they're whole, decimals otherwise.
func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case nil:
		return ""
	default:
		// Last resort — let json represent it.
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// shellQuote wraps s in single quotes, escaping any embedded single
// quotes via the standard `'\''` POSIX trick. Suitable for any sh
// shell. Critical for safety — without this, a placeholder value
// containing `; rm -rf $HOME` would execute as a command.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ----------------------------------------------------------------------
// create_api_tool
// ----------------------------------------------------------------------

// CreateAPIToolTool wraps a registered secure-API credential into a
// focused, structured tool. Sister of create_temp_tool but the body is
// a URL template against a credential rather than a shell command —
// the LLM never sees the credential value.
type CreateAPIToolTool struct{}

func (t *CreateAPIToolTool) Name() string       { return "create_api_tool" }
func (t *CreateAPIToolTool) Caps() []Capability { return []Capability{CapNetwork} }
func (t *CreateAPIToolTool) NeedsConfirm() bool { return true }

func (t *CreateAPIToolTool) Desc() string {
	return "Define a focused tool that calls a registered API credential. The body is a URL template (with {param} placeholders) targeting a specific credential — the credential's auth is injected server-side, you never see the secret. Use when you've discovered a useful endpoint pattern via call_<credname> and want a structured, reusable shape (e.g. get_github_issue(owner, repo, number) wrapping /repos/{owner}/{repo}/issues/{number}). Set persist=true to queue the tool for human approval and reuse across sessions."
}

func (t *CreateAPIToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {
			Type:        "string",
			Description: "Tool name (snake_case, must not match an existing tool). E.g. \"get_github_issue\".",
		},
		"description": {
			Type:        "string",
			Description: "What the tool does. Include enough detail that you'll know when to call it in future rounds.",
		},
		"credential": {
			Type:        "string",
			Description: "Name of the registered secure-API credential to use (e.g. \"github_api\"). The credential's allowed-URL pattern is enforced — your URL template must resolve to a URL that matches.",
		},
		"url_template": {
			Type:        "string",
			Description: "URL template with {param} placeholders. Placeholders are URL-path-encoded at call time. Example: \"https://api.github.com/repos/{owner}/{repo}/issues/{number}\".",
		},
		"method": {
			Type:        "string",
			Description: "HTTP method. Defaults to GET. Use POST/PUT/PATCH/DELETE for write operations.",
		},
		"body_template": {
			Type:        "string",
			Description: "Optional JSON body template with {param} placeholders. Placeholders are JSON-encoded at call time. Example: '{\"title\": {title}, \"labels\": {labels}}'. Leave empty for GET requests.",
		},
		"params": {
			Type:        "object",
			Description: "Object describing the tool's parameters. Same shape as create_temp_tool. Each key matches a {placeholder} in url_template or body_template.",
		},
		"required": {
			Type:        "array",
			Description: "Optional list of param names that must be provided. Defaults to all of them.",
		},
		"persist": {
			Type:        "boolean",
			Description: "If true, request that this tool be saved across future sessions. Same approval flow as create_temp_tool — the tool works in this session immediately but persists only after human review.",
		},
	}
}

func (t *CreateAPIToolTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("create_api_tool requires a session")
}

func (t *CreateAPIToolTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("create_api_tool requires a session")
	}
	if sess.DB == nil {
		return "", fmt.Errorf("create_api_tool requires a session with DB access")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !validToolName(name) {
		return "", fmt.Errorf("name must be lowercase letters / digits / underscores only (got %q)", name)
	}
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == name {
			return "", fmt.Errorf("name %q collides with a registered tool", name)
		}
	}
	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", fmt.Errorf("description is required")
	}
	credName := strings.TrimSpace(StringArg(args, "credential"))
	if credName == "" {
		return "", fmt.Errorf("credential is required")
	}
	cred, ok := Secure().Load(credName)
	if !ok {
		return "", fmt.Errorf("credential %q is not registered — register it via the admin UI first", credName)
	}
	urlTpl := strings.TrimSpace(StringArg(args, "url_template"))
	if urlTpl == "" {
		return "", fmt.Errorf("url_template is required")
	}
	method := strings.ToUpper(strings.TrimSpace(StringArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	bodyTpl := strings.TrimSpace(StringArg(args, "body_template"))

	params, err := parseParamsArg(args["params"])
	if err != nil {
		return "", fmt.Errorf("params: %w", err)
	}
	if err := validateTemplate(urlTpl, params); err != nil {
		return "", fmt.Errorf("url_template: %w", err)
	}
	if bodyTpl != "" {
		if err := validateTemplate(bodyTpl, params); err != nil {
			return "", fmt.Errorf("body_template: %w", err)
		}
	}

	required := stringSliceArg(args["required"])
	if len(required) == 0 {
		for k := range params {
			required = append(required, k)
		}
	} else {
		for _, r := range required {
			if _, ok := params[r]; !ok {
				return "", fmt.Errorf("required lists %q which is not in params", r)
			}
		}
	}

	tool := &TempTool{
		Name:            name,
		Description:     desc,
		Params:          params,
		Required:        required,
		CommandTemplate: urlTpl,
		Mode:            TempToolModeAPI,
		Credential:      credName,
		Method:          method,
		BodyTemplate:    bodyTpl,
	}
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}

	persist := BoolArg(args, "persist")
	if persist {
		if sess.Username == "" {
			return fmt.Sprintf("Created api tool %q for this session. Persistence not available without an authenticated user.", name), nil
		}
		if err := QueuePendingTempTool(sess.DB, sess.Username, *tool, ""); err != nil {
			return fmt.Sprintf("Created api tool %q for this session. Persistence requested but queueing failed: %v.", name, err), nil
		}
		return fmt.Sprintf("Created api tool %q (wraps credential %q). Persistence requested — queued for user approval via admin UI. The credential's allowed-URL pattern is %s; the LLM still cannot see its value.", name, credName, cred.AllowedURLPattern), nil
	}
	return fmt.Sprintf("Created api tool %q (wraps credential %q) for this session. Available on the next round; will be discarded at session end.", name, credName), nil
}

// dispatchAPIModeTempTool handles a TempTool whose Mode is api. The
// URL template gets URL-path-encoded args; the optional body template
// gets JSON-encoded args. The resolved request is then dispatched
// through DispatchSecureAPICredentialCall, which validates the URL
// against the credential's allowlist and injects the encrypted secret.
func dispatchAPIModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if sess.DB == nil {
		return "", fmt.Errorf("api tool %q requires a session with DB access", tt.Name)
	}
	if tt.Credential == "" {
		return "", fmt.Errorf("api tool %q has no credential configured", tt.Name)
	}
	urlStr, err := substituteURL(tt.CommandTemplate, tt.Params, args)
	if err != nil {
		return "", fmt.Errorf("url template: %w", err)
	}
	method := strings.ToUpper(strings.TrimSpace(tt.Method))
	if method == "" {
		method = "GET"
	}
	var body string
	if tt.BodyTemplate != "" {
		body, err = substituteJSON(tt.BodyTemplate, tt.Params, args)
		if err != nil {
			return "", fmt.Errorf("body template: %w", err)
		}
	}
	if sess.DB != nil && sess.Username != "" {
		TouchPersistentTempTool(sess.DB, sess.Username, tt.Name)
	}
	return Secure().DispatchToolCall(sess, tt.Credential, urlStr, method, body)
}

// substituteURL replaces {param} placeholders in a URL template with
// URL-path-encoded arg values. Different from shell quoting —
// placeholders inside path segments must be %-encoded.
func substituteURL(tmpl string, params map[string]ToolParam, args map[string]any) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			b.WriteByte(tmpl[i])
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			b.WriteByte(tmpl[i])
			continue
		}
		name := tmpl[i+1 : i+1+end]
		if _, known := params[name]; !known {
			b.WriteByte(tmpl[i])
			continue
		}
		val, ok := args[name]
		if !ok {
			return "", fmt.Errorf("missing arg %q", name)
		}
		b.WriteString(urlEscape(stringify(val)))
		i = i + 1 + end
	}
	return b.String(), nil
}

// urlEscape percent-encodes a value for safe inclusion in a URL path
// or query. Encodes everything not in the unreserved set (RFC 3986)
// — conservative, won't double-encode legitimately-encoded values
// because callers should pass raw param values, not pre-encoded ones.
func urlEscape(s string) string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// substituteJSON replaces {param} placeholders in a body template
// with JSON-encoded arg values. Strings get JSON-quoted; numbers/
// bools pass through as-is; objects/arrays serialize structurally.
// This lets the LLM write JSON body templates like
// `{"title": {title}, "labels": {labels}}` and have them serialize
// correctly regardless of the underlying arg type.
func substituteJSON(tmpl string, params map[string]ToolParam, args map[string]any) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			b.WriteByte(tmpl[i])
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			b.WriteByte(tmpl[i])
			continue
		}
		name := tmpl[i+1 : i+1+end]
		if _, known := params[name]; !known {
			b.WriteByte(tmpl[i])
			continue
		}
		val, ok := args[name]
		if !ok {
			return "", fmt.Errorf("missing arg %q", name)
		}
		j, err := jsonMarshal(val)
		if err != nil {
			return "", err
		}
		b.WriteString(j)
		i = i + 1 + end
	}
	return b.String(), nil
}

// jsonMarshal is a wrapper around json.Marshal that returns the
// string form. Inlined to avoid pulling json into substituteJSON's
// signature.
func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
