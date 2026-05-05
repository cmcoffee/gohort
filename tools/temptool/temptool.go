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
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// commandTimeout caps wall-clock time per temp-tool invocation. Same as
// run_local — long-running commands get killed.
const commandTimeout = 90 * time.Second

// maxOutput is the per-call output cap, same as run_local.
const maxOutput = 10000

// Individual tools (CreateTempToolTool, ListTempToolsTool,
// DeleteTempToolTool, CreateAPIToolTool) are no longer registered —
// the consolidated tool_def grouped tool (registered in tool_def.go)
// covers all four. Their implementations remain so tool_def.go's
// dispatchers can call them; just dropped from the catalog.
func init() {}

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
	// Optional StatePath captures "this subdir of the workspace
	// persists across invocations." Most tools don't need it — they
	// run their script and produce output, no state. Stateful tools
	// (counters, accumulating logs, lookup DBs) opt in.
	if sp := strings.TrimSpace(StringArg(args, "state_path")); sp != "" {
		tool.StatePath = sp
	}
	// Allow in-session overwrite: if the LLM is recreating a tool by
	// the same name (typically because v1 had a schema mistake), drop
	// the old entry first so AppendTempTool doesn't reject as a
	// duplicate. Cheaper than forcing the LLM to come up with a v2
	// name and littering the catalog with deprecated copies.
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	// Session-scoped persistence: save the tool keyed by chat session
	// ID so it survives across messages within the same chat. Only
	// applies when persist=false (the persist=true path queues for
	// admin approval, which lives in a separate pool).
	saveSessionScoped := func() {
		if sess.DB != nil && sess.ChatSessionID != "" {
			if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, *tool); err != nil {
				Debug("[temptool] session-scoped save failed for %s/%s: %v", sess.ChatSessionID, name, err)
			}
		}
	}

	// Persist request: queue for human approval. The tool is already
	// usable in this session (just registered above); persistence is
	// what makes it survive into future sessions, and that requires
	// human review of the command_template.
	persist := BoolArg(args, "persist")
	if persist {
		if sess.DB == nil || sess.Username == "" {
			saveSessionScoped()
			return fmt.Sprintf("Created temp tool %q for this session. Persistence was requested but is not available in this app/session, so the tool will be discarded when the session ends. Treat it as session-scoped.", name), nil
		}
		// Pack the session's current workspace into a tar.gz archive
		// IFF the command_template references {workspace_dir} (so the
		// tool actually needs the workspace files). Pure shell tools
		// like "uname -a" skip this — empty ArchivePath = self-
		// contained.
		archiveNote := ""
		if strings.Contains(cmd, "{workspace_dir}") {
			if sess.WorkspaceDir == "" {
				return "", fmt.Errorf("command_template references {workspace_dir} but session has no active workspace — call workspace(action=create) first, write your scripts, then create the persistent tool")
			}
			path, size, hash, err := PackToolArchive(sess.WorkspaceDir, sess.Username, name)
			if err != nil {
				return "", fmt.Errorf("pack tool archive: %w", err)
			}
			tool.ArchivePath = path
			tool.ArchiveSize = size
			tool.ArchiveHash = hash
			archiveNote = fmt.Sprintf(" Workspace packed into %s (%d bytes, sha256=%s).", path, size, hash[:12])
		}
		if err := QueuePendingTempTool(sess.DB, sess.Username, *tool, ""); err != nil {
			saveSessionScoped()
			return fmt.Sprintf("Created temp tool %q for this session. Persistence requested but queueing failed: %v. Tool will be discarded at session end.", name, err), nil
		}
		return fmt.Sprintf("Created temp tool %q for this session.%s Persistence requested — queued for user approval via the admin UI. The user will review the command template (and the packed archive contents) before it becomes available in future sessions.", name, archiveNote), nil
	}
	saveSessionScoped()
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

	// Also clear from the per-chat-session pool so the deletion sticks
	// across messages within this same chat. Independent of the
	// persistent (admin-approved) pool — a tool can live in either,
	// both, or neither.
	if sess.DB != nil && sess.ChatSessionID != "" {
		RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
	}

	// Also clear from the persistent pool if applicable. The LLM may
	// be deleting a tool it loaded from persistence at session start,
	// expecting the deletion to stick across sessions.
	persistRemoved := false
	if sess.DB != nil && sess.Username != "" {
		if err := DeletePersistentTempTool(sess.DB, sess.Username, name); err == nil {
			persistRemoved = true
		}
	}

	// Also clear from the pending-approval queue. If the LLM created
	// a tool with persist=true earlier in this session and now wants
	// it gone (typo, supersession by a v2, or a flat-out abandonment
	// of the original idea), the deletion should yank it from the
	// admin's review queue too — otherwise the user sees a tool the
	// LLM no longer wants and approving it just adds dead weight.
	pendingRemoved := false
	if sess.DB != nil && sess.Username != "" {
		if err := RejectPendingTempTool(sess.DB, sess.Username, name); err == nil {
			pendingRemoved = true
		}
	}

	parts := []string{}
	if removed {
		parts = append(parts, "this session")
	}
	if persistRemoved {
		parts = append(parts, "your persistent pool")
	}
	if pendingRemoved {
		parts = append(parts, "the pending-approval queue")
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no temp tool named %q in this session, your persistent pool, or the pending-approval queue", name)
	}
	var phrase string
	switch len(parts) {
	case 1:
		phrase = parts[0]
	case 2:
		phrase = parts[0] + " and " + parts[1]
	default:
		phrase = strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
	return fmt.Sprintf("Removed temp tool %q from %s.", name, phrase), nil
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
		Debug("[temptool] BuildAgentToolDefs called with nil session")
		return nil
	}
	tools := sess.CopyTempTools()
	if len(tools) == 0 {
		Debug("[temptool] BuildAgentToolDefs: sess.TempTools is empty (no dynamic temp tools to expose to the LLM this round)")
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, tt := range tools {
		names = append(names, tt.Name)
	}
	Debug("[temptool] BuildAgentToolDefs: producing %d AgentToolDef(s) — %v", len(tools), names)
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
		// Response pipe runs sandboxed shell on the API result, so the
		// wrapper needs CapExecute as well as CapNetwork.
		if tt.ResponsePipe != "" {
			caps = append(caps, CapExecute)
		}
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

	// Shell-mode dispatch path. Three shapes depending on the tool:
	//
	//  (a) ArchivePath set: tool packs its own code. Extract into a
	//      per-invocation tmpdir, optionally restore state subdir,
	//      run, save state back, tear tmpdir down. {workspace_dir}
	//      resolves to the tmpdir.
	//
	//  (b) ArchivePath empty + command references {workspace_dir}:
	//      ad-hoc tool created mid-session (persist=false). Use the
	//      session's current WorkspaceDir.
	//
	//  (c) ArchivePath empty + no {workspace_dir} reference: pure
	//      shell command (e.g. "uname -a"). Use sess.WorkspaceDir
	//      as the bwrap bind so the command has SOME cwd, but the
	//      command itself doesn't depend on workspace contents.
	var workspaceDir string
	if tt.ArchivePath != "" {
		tmp, err := os.MkdirTemp("", "tooldispatch-")
		if err != nil {
			return "", fmt.Errorf("mkdtemp: %w", err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if err := UnpackToolArchive(tt.ArchivePath, tmp); err != nil {
			return "", fmt.Errorf("unpack archive: %w", err)
		}
		// Restore persistent state for stateful tools.
		if tt.StatePath != "" {
			stateTarget := filepath.Join(tmp, tt.StatePath)
			if err := os.MkdirAll(stateTarget, 0700); err != nil {
				return "", fmt.Errorf("mkdir state target: %w", err)
			}
			if err := CopyToolStateInto(sess.Username, tt.Name, stateTarget); err != nil {
				return "", fmt.Errorf("restore state: %w", err)
			}
		}
		workspaceDir = tmp
	} else {
		workspaceDir = sess.WorkspaceDir
	}
	if workspaceDir == "" {
		// Last resort: command doesn't reference {workspace_dir} and
		// the session has no provisioned workspace (e.g. workspace
		// setup failed at handleSend, or a non-chat caller didn't set
		// one). The bwrap bind still needs a path, but the command
		// doesn't need workspace contents — provision an ephemeral
		// tmpdir for this invocation only. Without this, stateless
		// shell tools like "echo '{text}' | rev" can't dispatch even
		// when correctly registered, persisted, and approved.
		if strings.Contains(tt.CommandTemplate, "{workspace_dir}") {
			return "", fmt.Errorf("temp tool %q references {workspace_dir} but session has no provisioned workspace", tt.Name)
		}
		tmp, err := os.MkdirTemp("", "tooldispatch-stateless-")
		if err != nil {
			return "", fmt.Errorf("mkdtemp for stateless dispatch: %w", err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		workspaceDir = tmp
	}
	cmdTemplate := strings.ReplaceAll(tt.CommandTemplate, "{workspace_dir}", shellQuote(workspaceDir))
	cmd, err := substitute(cmdTemplate, tt.Params, args)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	res := RunSandboxedShell(ctx, cmd, workspaceDir)
	output := strings.TrimSpace(res.Output)

	// Save state back AFTER successful (or failed — preserve state
	// either way so state changes during partial runs aren't lost)
	// dispatch. Best-effort: state-save errors are logged but don't
	// fail the dispatch itself.
	if tt.ArchivePath != "" && tt.StatePath != "" {
		stateTarget := filepath.Join(workspaceDir, tt.StatePath)
		if err := CopyToolStateBack(sess.Username, tt.Name, stateTarget); err != nil {
			Debug("[temptool] state save failed for %s: %v", tt.Name, err)
		}
	}

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
//
// Tolerant of literal braces in JSON / shell expressions: only treats
// `{...}` as a placeholder when the contents look like an identifier
// (letters, digits, underscores; must start with a letter or
// underscore). Anything else — `{"key": "value"}`, `${VAR}`, `${1:-x}`,
// `${array[@]}`, brace expansion `{a,b,c}`, etc. — passes through
// silently the same way `substitute` does, so api-mode body templates
// containing literal JSON object braces don't get rejected at
// validation time.
func validateTemplate(cmd string, params map[string]ToolParam) error {
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '{' {
			continue
		}
		end := strings.IndexByte(cmd[i+1:], '}')
		if end < 0 {
			// Unclosed brace is more likely a literal in shell or JSON
			// than a forgotten placeholder. Don't error — let it
			// through. (The original strict behavior caused false
			// positives on api-mode body templates with nested
			// objects.)
			return nil
		}
		name := cmd[i+1 : i+1+end]
		if !isPlaceholderIdent(name) {
			// Not identifier-shaped — treat as a literal brace
			// expression (JSON, shell parameter expansion, brace
			// expansion). Skip past the opening brace only; the
			// closing brace and contents stay in scope so a
			// genuine placeholder later in the same string still
			// gets validated.
			continue
		}
		if _, ok := params[name]; !ok {
			return fmt.Errorf("placeholder {%s} not in params", name)
		}
		i = i + 1 + end
	}
	return nil
}

// isPlaceholderIdent reports whether s is shaped like a Go-style
// identifier — letters/digits/underscores, must start with a letter
// or underscore. Mirrors the implicit shape `substitute` uses when
// it decides whether to honor a `{...}` as a placeholder. Empty
// strings and JSON-shaped strings (containing spaces, quotes, colons,
// commas, brackets) return false.
func isPlaceholderIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			// always allowed
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
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
			Description: "Optional JSON body template with {param} placeholders. Placeholders are JSON-encoded at call time — strings get wrapped in quotes automatically, numbers/booleans pass through, objects/arrays serialize structurally. DO NOT wrap placeholders in quotation marks yourself: write {prompt} not \"{prompt}\". The substitution layer handles the quoting and any escaping (newlines, embedded quotes) of the runtime value, so a long multi-line system prompt or any unsafe-for-JSON string passed as an arg comes out correctly. Put long literal content (system prompts, instructions) as runtime PARAMS, not baked into the template — that way you don't have to escape it inside the template string itself. Example: '{\"system_prompt\": {prompt}, \"user_message\": {msg}}'. Leave empty for GET requests.",
		},
		"params": {
			Type:        "object",
			Description: "Object describing the tool's parameters. Same shape as create_temp_tool. Each key matches a {placeholder} in url_template or body_template.",
		},
		"required": {
			Type:        "array",
			Description: "Optional list of param names that must be provided. Defaults to all of them.",
		},
		"response_pipe": {
			Type:        "string",
			Description: "Optional shell command (sh -c) that receives the API response BODY on stdin and emits the LLM-visible result on stdout. The HTTP status line is stripped before piping and re-prepended to the output, so the pipe should target only the response body (no `tail -n +2` needed). Pipe is skipped on non-2xx responses so the LLM sees the raw error. Use to pre-filter noisy responses before they reach the LLM context — e.g. \"jq -c '[.items[] | {id, name, status}]'\" or \"jq -c '.[:20]'\". Runs in a tight sandbox (no network, no filesystem, /tmp tmpfs only) so it can use jq, awk, sed, grep, head, tr, etc. Leave empty if the LLM should see the raw response. Adds an exec dependency to the tool — sessions without execute capability won't be able to dispatch it.",
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
	respPipe := strings.TrimSpace(StringArg(args, "response_pipe"))

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
		ResponsePipe:    respPipe,
	}
	// Allow in-session overwrite — see CreateTempToolTool for rationale.
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	// Session-scoped persistence so the tool survives across messages
	// in the same chat session (see CreateTempToolTool for rationale).
	saveSessionScoped := func() {
		if sess.DB != nil && sess.ChatSessionID != "" {
			if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, *tool); err != nil {
				Debug("[temptool] session-scoped save failed for %s/%s: %v", sess.ChatSessionID, name, err)
			}
		}
	}

	persist := BoolArg(args, "persist")
	if persist {
		if sess.Username == "" {
			saveSessionScoped()
			return fmt.Sprintf("Created api tool %q for this session. Persistence not available without an authenticated user.", name), nil
		}
		if err := QueuePendingTempTool(sess.DB, sess.Username, *tool, ""); err != nil {
			saveSessionScoped()
			return fmt.Sprintf("Created api tool %q for this session. Persistence requested but queueing failed: %v.", name, err), nil
		}
		return fmt.Sprintf("Created api tool %q (wraps credential %q). Persistence requested — queued for user approval via admin UI. The credential's allowed-URL pattern is %s; the LLM still cannot see its value.", name, credName, cred.AllowedURLPattern), nil
	}
	saveSessionScoped()
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
		// Validate the substituted body is valid JSON before sending.
		// Catches template-shape mistakes (mismatched braces, missing
		// commas, an LLM-baked literal that didn't escape correctly)
		// before the remote API rejects them with a generic "Expected
		// ',' or '}' at position N" — which is hard to act on without
		// seeing the actual body. The error returned to the LLM
		// includes the produced body so it can inspect what it built
		// and re-create the tool with a corrected template.
		var probe any
		if jerr := json.Unmarshal([]byte(body), &probe); jerr != nil {
			Debug("[temptool] api tool %q produced invalid JSON body: %s\nTEMPLATE: %s\nBODY: %s", tt.Name, jerr, tt.BodyTemplate, body)
			return "", fmt.Errorf("body template substitution produced invalid JSON: %w. Template: %s. Substituted body: %s", jerr, tt.BodyTemplate, body)
		}
		Debug("[temptool] api tool %q body validated (%d bytes)", tt.Name, len(body))
	}
	if sess.DB != nil && sess.Username != "" {
		TouchPersistentTempTool(sess.DB, sess.Username, tt.Name)
	}
	raw, err := Secure().DispatchToolCall(sess, tt.Credential, urlStr, method, body)
	if err != nil {
		return raw, err
	}
	if tt.ResponsePipe == "" {
		return raw, nil
	}
	// The raw response from Secure().DispatchToolCall is shaped as:
	//   HTTP <code> <text>\n<body>
	// — the status line is there so the LLM can see HTTP errors when
	// reading the response directly. For the pipe path it's noise: jq
	// chokes on it, every pipe would need a `tail -n +2` prefix, and
	// running a filter against an error response (different shape than
	// a success body) usually produces garbage. Split the line off,
	// pipe only the body on 2xx, and skip the pipe entirely on non-2xx
	// so the LLM gets the unfiltered error to act on.
	statusLine, body := splitStatusLine(raw)
	if !isStatus2xx(statusLine) {
		return raw, nil
	}
	pipeCtx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pres := RunSandboxedShellPipe(pipeCtx, tt.ResponsePipe, body)
	piped := strings.TrimSpace(pres.Output)
	if pres.TimedOut {
		notice := fmt.Sprintf("\n[response_pipe TIMED OUT after %s — command killed.]", commandTimeout)
		if piped == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return piped + notice, nil
	}
	if pres.Err != nil {
		// Pipe failed (bad jq expression, missing binary, etc.). Surface
		// the error plus whatever output landed so the LLM can fix the
		// pipe via delete + recreate. Don't fall through to raw — the
		// whole point is to keep the raw response out of the LLM context.
		if piped == "" {
			return fmt.Sprintf("[response_pipe failed: %v — no output. Pipe: %s]", pres.Err, tt.ResponsePipe), nil
		}
		return piped + fmt.Sprintf("\n[response_pipe exit: %v]", pres.Err), nil
	}
	if len(piped) > maxOutput {
		totalLines := strings.Count(piped, "\n") + 1
		truncated := piped[:maxOutput]
		shown := strings.Count(truncated, "\n") + 1
		piped = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars).]",
			shown, totalLines, len(piped))
	}
	// Re-prepend the status line so the LLM still sees the HTTP code.
	if statusLine != "" {
		return statusLine + "\n" + piped, nil
	}
	return piped, nil
}

// splitStatusLine separates the leading "HTTP <code> <text>" line from
// the response body. Returns ("", raw) when the response doesn't start
// with an HTTP status line (defensive — should always match for output
// produced by Secure().dispatch).
func splitStatusLine(raw string) (status, body string) {
	if !strings.HasPrefix(raw, "HTTP ") {
		return "", raw
	}
	nl := strings.IndexByte(raw, '\n')
	if nl < 0 {
		return raw, ""
	}
	return raw[:nl], raw[nl+1:]
}

// isStatus2xx parses the numeric code out of an "HTTP <code> ..." line
// and reports whether it's in the 2xx range. Defensive — unparseable
// status lines fail closed (return false) so we don't pipe what looks
// like an error response.
func isStatus2xx(statusLine string) bool {
	if !strings.HasPrefix(statusLine, "HTTP ") {
		return false
	}
	rest := statusLine[len("HTTP "):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		sp = len(rest)
	}
	codeStr := rest[:sp]
	if len(codeStr) != 3 {
		return false
	}
	return codeStr[0] == '2'
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
//
// Quote-wrap tolerance: if the LLM writes the placeholder wrapped in
// quotes (`"prompt": "{prompt}"`) — the natural JSON shape — strip
// the surrounding quotes from the output, since json.Marshal will
// re-add them for string values. Otherwise the result becomes
// `"prompt": ""hello""` which breaks the JSON. For non-string
// values (numbers, booleans, objects, arrays) the wrap is also
// incorrect on the input side, so we treat the strip as the
// authoritative shape and let json.Marshal produce the right form.
func substituteJSON(tmpl string, params map[string]ToolParam, args map[string]any) (string, error) {
	var b strings.Builder
	out := []byte{}
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			out = append(out, tmpl[i])
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			out = append(out, tmpl[i])
			continue
		}
		name := tmpl[i+1 : i+1+end]
		if _, known := params[name]; !known {
			out = append(out, tmpl[i])
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
		// Quote-wrap detection: if `"{name}"` is the surrounding
		// shape, strip the leading `"` from already-emitted output
		// and skip the trailing `"` in the template input. The
		// substituted JSON value (which may itself start with `"`
		// for strings, or with a non-quote char for numbers/bools/
		// objects) replaces the wrapped placeholder cleanly either
		// way.
		closingQuoteAhead := i+1+end+1 < len(tmpl) && tmpl[i+1+end+1] == '"'
		openingQuoteBehind := len(out) > 0 && out[len(out)-1] == '"'
		if openingQuoteBehind && closingQuoteAhead {
			out = out[:len(out)-1] // drop the leading quote
			out = append(out, j...)
			i = i + 1 + end + 1 // skip placeholder + trailing quote
			continue
		}
		out = append(out, j...)
		i = i + 1 + end
	}
	b.Write(out)
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
