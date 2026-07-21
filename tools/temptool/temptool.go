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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// commandTimeout caps wall-clock time per temp-tool invocation. Same as
// run_local — long-running commands get killed.
const commandTimeout = 90 * time.Second

// Pipeline-mode custom tools run a nested sub-agent, which — unlike the shell
// (commandTimeout) and api (SecureAPI 30s) paths — had NO wall-clock ceiling
// (it ran on context.Background(), bounded only by max_rounds). A stalled or
// looping nested agent then hangs the parent turn indefinitely, since the agent
// loop invokes tool handlers without a timeout. This tunable caps it.
func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_pipeline_tool_timeout",
		Category: "Timeouts",
		Label:    "Pipeline tool wall-clock timeout",
		Help:     "Max wall-clock time a pipeline-mode custom tool's nested sub-agent may run before it is cancelled. Bounds a runaway or stalled pipeline so it fails cleanly instead of hanging the turn.",
		Kind:     KindSeconds,
		Default:  300,
		Min:      30,
		Max:      1800,
	})
}

// maxOutput is the per-call output cap for shell-mode temp tools and
// for response_pipe filtered output on api-mode temp tools. Bumped
// from 10000 (run_local-compatible) to 50000 (~12K tokens) to give
// pipe projections enough headroom for richer results — a list of
// 50 records with several fields each would clip at 10K but fits
// comfortably at 50K, while still being well within a 200K context
// window. Shell-mode tools also benefit when their output is
// genuinely structured. run_local stays at 10000 — that's plain
// shell output where the readability ceiling is lower.
const maxOutput = 50000

// Individual tools (CreateTempToolTool, ListTempToolsTool,
// DeleteTempToolTool, CreateAPIToolTool) are no longer registered —
// the consolidated tool_def grouped tool (registered in tool_def.go)
// covers all four. Their implementations remain so tool_def.go's
// dispatchers can call them; just dropped from the catalog.
func init() {
	// Backfill a legacy tool's script into its exported bundle. New tools
	// capture the script into the record at authoring time (see the
	// create path), but tools authored before that — via local(write) + a
	// {workspace_dir} command_template reference — have an empty ScriptBody
	// and their script lives only in the owner's workspace on disk. Read it
	// back here so exports carry it. Best-effort: single on-disk script,
	// simple filename, owner's user-root workspace.
	ResolveToolScriptForExport = captureExportScript
}

// captureExportScript populates t.ScriptBody (+ ScriptName/CanonicalScriptName)
// from the owner's on-disk workspace when the tool references exactly one
// existing {workspace_dir} script but carries no captured body. Mutates t in
// place; leaves it untouched on any miss (no workspace, multi-file, sub-path,
// unreadable). Wired into core.ResolveToolScriptForExport from init().
func captureExportScript(t *TempTool, owner string) {
	if t == nil || t.ScriptBody != "" {
		return
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}
	dir, err := EnsureWorkspaceDir(owner)
	if err != nil {
		return
	}
	present := presentWorkspaceScriptRefs(t.CommandTemplate, dir)
	if len(present) != 1 || strings.ContainsAny(present[0], "/\\") {
		return
	}
	content, err := os.ReadFile(filepath.Join(dir, present[0]))
	if err != nil || len(content) == 0 {
		return
	}
	t.ScriptBody = string(content)
	t.ScriptName = present[0]
	t.CanonicalScriptName = canonicalScriptName(t.Name, present[0], string(content))
	// A legacy multi-file tool's helpers live on disk too — pull them in
	// so the whole tool travels, not just the entry script.
	if len(t.WorkspaceFiles) == 0 {
		t.WorkspaceFiles = gatherWorkspaceHelpers(present[0], string(content), dir)
	}
}

// formatTempToolSpec renders a just-registered TempTool the same way
// BuildToolPrompt would describe a static tool: name, description,
// param list with types and required-flags. Appended to the result
// of every "Created temp tool" return so the LLM has the full schema
// in-band the moment it asked to create the tool — no waiting for
// the next round's catalog to discover the shape, no guessing param
// names from its own create_temp_tool args. Important when the
// model creates a tool and immediately wants to use it in the same
// reply ("created and now calling…").
func formatTempToolSpec(tt *TempTool) string {
	if tt == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nTool spec:\n\n")
	b.WriteString(fmt.Sprintf("### %s\n%s\n", tt.Name, tt.Description))
	if len(tt.Params) > 0 {
		b.WriteString("Parameters:\n")
		// Required-set lookup, single pass, preserves the order the
		// LLM declared them in (map iteration is randomized but the
		// info is stable enough — the names matter, not the order).
		reqSet := make(map[string]bool, len(tt.Required))
		for _, r := range tt.Required {
			reqSet[r] = true
		}
		for name, p := range tt.Params {
			req := ""
			if reqSet[name] {
				req = " (required)"
			}
			b.WriteString(fmt.Sprintf("  - %s (%s%s): %s\n", name, p.Type, req, p.Description))
		}
	}
	return b.String()
}

// ----------------------------------------------------------------------
// create_temp_tool
// ----------------------------------------------------------------------

type CreateTempToolTool struct{}

func (t *CreateTempToolTool) Name() string       { return "create_temp_tool" }
func (t *CreateTempToolTool) Caps() []Capability { return []Capability{CapExecute} }
func (t *CreateTempToolTool) NeedsConfirm() bool { return true }

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
			Description: "Object describing the tool's parameters. Each key is a param name and its value is an object {type, description, [required]}. Type must be \"string\", \"integer\", \"number\", or \"boolean\". E.g. {\"input\": {\"type\": \"string\", \"description\": \"Input file path\"}, \"size\": {\"type\": \"string\", \"description\": \"Target dimensions like 800x600\"}}. OPTIONAL — omit for a tool that takes no params; don't invent a dummy placeholder.",
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
		"script_body": {
			Type:        "string",
			Description: "Optional. The full source of a script to ship with the tool — Python, Bash, awk, jq, whatever. Written into the tool's sandbox at registration time as `script_name` (default \"script.py\"). Use this for any tool whose logic is more than a one-liner. Reference it from command_template as {workspace_dir}/<script_name>. Auto-mints a sandbox if none exists; no need to set up a workspace first.",
		},
		"script_name": {
			Type:        "string",
			Description: "Optional. Filename to write script_body as. Defaults to \"script.py\". Pick a name that matches the script's language (e.g. \"run.sh\" for Bash) so command_template reads naturally.",
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
	// Also reject DYNAMIC per-agent built-ins (channel/operator tools) that aren't
	// in the static catalog — a temp tool named e.g. send_message would otherwise
	// shadow the real, delivering tool with a stub that fakes success.
	if IsReservedToolName(name) {
		return "", fmt.Errorf("name %q is a built-in tool (channel/operator) — pick another; don't recreate it", name)
	}

	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", fmt.Errorf("description is required")
	}

	cmd := strings.TrimSpace(StringArg(args, "command_template"))
	scriptBody := StringArg(args, "script_body")
	scriptName := strings.TrimSpace(StringArg(args, "script_name"))

	// script_body shortcut: infer command_template when omitted, auto-mint a
	// sandbox, and write the script into it so command_template can reference
	// it via {workspace_dir}/<script_name> — workspace-create + file-write +
	// tool-create collapsed into one call, so the LLM never has to think about
	// workspaces. Shared with orchestrate's add_tool via PrepareScriptBody:
	// both authoring surfaces MUST behave identically here, and they didn't —
	// add_tool silently dropped script_body entirely. No-op when scriptBody is
	// blank, which leaves the else-branch below to handle the local(write) case.
	cmd, scriptName, canonicalName, err := PrepareScriptBody(sess, name, cmd, scriptBody, scriptName, args["params"])
	if err != nil {
		return "", err
	}
	if cmd == "" {
		return "", fmt.Errorf("command_template is required (or supply script_body for a recognized extension — .py/.sh/.bash/.js/.jq/.rb — and the framework will infer python3 {workspace_dir}/script.py; declared params reach the script as ENVIRONMENT VARIABLES, not positional argv — read them with os.environ['name'])")
	}
	if scriptBody == "" && strings.Contains(cmd, "{workspace_dir}") {
		// command_template references {workspace_dir} but no script_body
		// was supplied. Auto-mint a sandbox so the placeholder resolves;
		// the LLM is presumably writing the script itself via local(write).
		if _, err := EnsureSessionWorkspace(sess); err != nil {
			return "", fmt.Errorf("auto-mint workspace: %w", err)
		}
		// Catch the most common authoring mismatch: LLM wrote the script
		// via local(action="write", path="X.py") under one name and
		// gave command_template a DIFFERENT filename
		// ({workspace_dir}/script.py) — no script_body to redeploy,
		// so dispatch fails on the first call with "no such file" and
		// the user sees an invisible deployment. Refuse at create
		// time so the LLM gets a clear, immediate error instead of a
		// silently-broken tool. Only checks recognized script
		// extensions (.py / .sh / .jq / etc.) so command_templates
		// that reference workspace_dir as a scratch path for output
		// files (data.json, screenshot.png) aren't false-positives.
		if missing := missingWorkspaceScriptRefs(cmd, sess.WorkspaceDir); len(missing) > 0 {
			return "", fmt.Errorf("command_template references script file(s) %v in {workspace_dir} that don't exist on disk. Either (a) pass script_body so the framework ships the script with the tool record (preferred — survives workspace wipes), OR (b) call local(action=\"write\", path=\"<exact-filename>\", content=\"...\") BEFORE this tool_def call, with the path matching what command_template expects", missing)
		}
		// CAPTURE-INTO-RECORD: the LLM authored via local(write) + a
		// command_template reference rather than the script_body param.
		// The script exists on disk but NOT in the tool record — so it
		// works this session yet silently fails to travel: a bundle
		// export carries an empty script_body, and a workspace wipe
		// breaks the tool. Read the referenced script back into the
		// record here so option (b) gets the SAME portability as option
		// (a). We handle the single-file case (by far the common one — a
		// lone Python/bash script); multi-file or sub-path references
		// stay disk-resident (the single ScriptBody slot can't represent
		// them, and {workspace_dir} refs rule out the Recipe tmpdir).
		if present := presentWorkspaceScriptRefs(cmd, sess.WorkspaceDir); len(present) == 1 && !strings.ContainsAny(present[0], "/\\") {
			rel := present[0]
			if content, rerr := os.ReadFile(filepath.Join(sess.WorkspaceDir, rel)); rerr == nil && len(content) > 0 {
				// Feed the standard capture block below (script body != "")
				// so the record is populated identically to the script_body
				// path: LLM-facing name = the referenced filename, canonical
				// on-disk name = collision-proof hash. Dispatch redeploys the
				// body under the canonical name and translates the reference.
				scriptBody = string(content)
				scriptName = rel
				canonicalName = canonicalScriptName(name, scriptName, scriptBody)
			} else if rerr != nil {
				Debug("[temptool] %q: could not capture on-disk script %q into record: %v", name, rel, rerr)
			}
		}
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
	// Omitted required → default all params required; an EXPLICIT [] → make
	// all optional. Distinguish by presence (see the toolbox path).
	if raw, present := args["required"]; !present || raw == nil {
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
	// Persist the script content into the tool record so it
	// survives workspace wipes. At dispatch the framework will
	// idempotently redeploy this to {workspace_dir}/<CanonicalScriptName>
	// if the file is missing. Without this, deleting the workspace
	// dir silently breaks any tool that depends on a script_body
	// file (the dispatch fails with "no such file or directory").
	//
	// ScriptName is the LLM-facing name (what appears in CommandTemplate);
	// CanonicalScriptName is the framework's collision-proof on-disk
	// filename ("<tool_name>_<content_hash>.<ext>"). Dispatch translates
	// references from one to the other.
	if scriptBody != "" {
		tool.ScriptBody = scriptBody
		tool.ScriptName = scriptName
		tool.CanonicalScriptName = canonicalName
		// Bundle the helper files the entry script pulls in (imported
		// Python modules, sourced bash files) so the tool is
		// self-contained — it exports whole and survives a workspace
		// wipe, not just the entry script. Best-effort: only on-disk
		// siblings are captured; a missed helper is a no-op (it still
		// sits in the shared workspace at runtime, it just wouldn't
		// travel). See gatherWorkspaceHelpers.
		tool.WorkspaceFiles = gatherWorkspaceHelpers(scriptName, scriptBody, sess.WorkspaceDir)
	}
	// Optional StatePath captures "this subdir of the workspace
	// persists across invocations." Most tools don't need it — they
	// run their script and produce output, no state. Stateful tools
	// (counters, accumulating logs, lookup DBs) opt in.
	if sp := strings.TrimSpace(StringArg(args, "state_path")); sp != "" {
		tool.StatePath = sp
	}
	// Optional RawNetwork: opt-in escape hatch that keeps the bwrap
	// sandbox joined to the host network namespace. Default false
	// (the sandbox runs with --unshare-net regardless of session
	// connector). Reserve for persistent-mode REPLs and the small
	// set of legacy tools that haven't been migrated to the hook
	// (hook_capabilities=["fetch"] + gohort.fetch(...) is the
	// preferred path for everything else).
	if BoolArg(args, "raw_network") {
		tool.RawNetwork = true
	}
	// Optional HookCapabilities: opens the per-dispatch UDS callback
	// channel for the listed methods. Empty / unset = no hook, no
	// extra env, zero surface area. Validate each entry — bare
	// methods ("fetch", "log") and qualified secret entries
	// ("secret:<credential_name>") are accepted; bare "secret"
	// without a name is rejected so the tool record always names
	// every credential it can read. Other typos fail authoring
	// rather than silently never being granted at dispatch.
	if caps := stringSliceArg(args["hook_capabilities"]); len(caps) > 0 {
		bareKnown := map[string]bool{"fetch": true, "log": true, "browse_page": true}
		var bad []string
		var clean []string
		seen := map[string]bool{}
		for _, c := range caps {
			// Preserve credential name casing in the suffix — credential
			// names are case-sensitive on the registry side — but
			// normalize the method prefix to lower.
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if idx := strings.IndexByte(c, ':'); idx >= 0 {
				method := strings.ToLower(c[:idx])
				name := strings.TrimSpace(c[idx+1:])
				if name == "" {
					bad = append(bad, c)
					continue
				}
				// Qualified forms: per-credential grants. secret:<name>
				// returns the decrypted secret to the script;
				// fetch_via:<name> routes an HTTP call through that
				// credential's Secure.Dispatch (allowlist + audit +
				// auth applied server-side, script never sees the
				// secret).
				if method != "secret" && method != "fetch_via" {
					bad = append(bad, c)
					continue
				}
				// SECURED credential binding is AUTO-RESOLVED: a tool that declares
				// fetch_via:<cred> is bound to it — no approval step. Access follows
				// the tool's own scope. Two exceptions: secret:<cred> (raw key to a
				// script) is never a binding and stays hard-blocked; and an admin's
				// explicit REVOKE of this tool is a durable deny. See
				// docs/secured-credential-tool-binding.md.
				if cr, ok := Secure().Load(name); ok && cr.Secured {
					switch {
					case method == "secret":
						return "", fmt.Errorf("credential %q is SECURED — a tool cannot take its raw secret (secret:%s). Route the call through the credential with fetch_via:%s instead — the secret stays server-side and the binding is automatic", name, name, name)
					case Secure().ToolBindingRevoked(name, tool.Name):
						return "", fmt.Errorf("credential %q is SECURED and tool %q's binding was REVOKED by an admin — ask them to restore it in Admin > APIs, or use a different tool name", name, tool.Name)
					default:
						// Auto-resolve: record the binding so it shows in the admin
						// effective-access view; access is governed by the tool's scope.
						_ = Secure().ApproveToolBinding(name, tool.Name)
					}
				}
				c = method + ":" + name
			} else {
				c = strings.ToLower(c)
				if c == "secret" || c == "fetch_via" {
					// Bare qualified-form methods are intentionally
					// not honored — every credential grant must be
					// explicit.
					return "", fmt.Errorf("hook_capabilities entry %q is too broad — declare specific credentials as %q instead", c, c+":<credential_name>")
				}
				if !bareKnown[c] {
					bad = append(bad, c)
					continue
				}
			}
			if seen[c] {
				continue
			}
			seen[c] = true
			clean = append(clean, c)
		}
		if len(bad) > 0 {
			return "", fmt.Errorf("hook_capabilities lists unknown entry/entries %v — known forms: \"fetch\", \"log\", \"secret:<credential_name>\", \"fetch_via:<credential_name>\"", bad)
		}
		tool.HookCapabilities = clean
	}
	// Default-on the bare hook capabilities for any shell-mode tool
	// with script_body. Builder doesn't have to remember to declare
	// hook_capabilities=["fetch"] — the framework adds them
	// automatically. Same security posture: the bwrap sandbox is still
	// --unshare-net, the connector still gates Private mode, every
	// call still goes through gohort's HTTP client with audit. Just
	// removes the "tool exits 1 on first dispatch because GOHORT_HOOK_PATH
	// wasn't set" footgun.
	//
	// Parameterized capabilities (secret:<name> / fetch_via:<name>)
	// stay explicit. Those bind to specific credentials the framework
	// can't safely guess, so they still need declaration. Script that
	// calls gohort.secret("openweather") without "secret:openweather"
	// declared fails authoring with a directive error.
	if scriptBody != "" {
		existing := map[string]bool{}
		for _, c := range tool.HookCapabilities {
			existing[c] = true
		}
		for _, def := range []string{"fetch", "log", "browse_page"} {
			if !existing[def] {
				existing[def] = true
				tool.HookCapabilities = append(tool.HookCapabilities, def)
			}
		}
		// Credentialed-call guard: script that needs secret:<name> or
		// fetch_via:<name> must declare it explicitly. We refuse to
		// guess credential identifiers.
		if missing := findUngrantedCredentialCalls(scriptBody, tool.HookCapabilities); missing.calls != "" {
			return "", fmt.Errorf(
				"script_body uses %s but hook_capabilities doesn't grant the credential(s). "+
					"Add %s to hook_capabilities — the framework won't auto-grant credential names. "+
					"Register the credential via the admin UI first if it doesn't exist yet.",
				missing.calls, missing.suggest)
		}
		// Refuse network primitives. Builder repeatedly rewrites
		// fetch-failing tools to urllib/requests/curl/wget when
		// fetch_url returns 4xx, but those libraries are BLOCKED in
		// the script sandbox. Refuse at authoring time so the
		// rewrite path is closed off entirely.
		if forbidden := detectForbiddenNetworkPatterns(scriptBody); forbidden != "" {
			return "", fmt.Errorf(
				"script_body uses %s — that's BLOCKED. Any network-doing standard library (urllib / requests / curl / wget / http.client / socket) is blocked in the script sandbox. All HTTP goes through gohort: `from gohort import fetch_url; data = fetch_url(url)`. If gohort.fetch_url is returning a 4xx, the fix is NOT a different HTTP client — diagnose the URL itself, escalate to gohort.browse_page for JS-heavy / anti-bot hosts, or add hook_capabilities=[\"fetch_via:<credential_name>\"] for authenticated endpoints.",
				forbidden)
		}
	}
	// Optional cache spec — opt-in memoization keyed by rendered
	// {param} template (default = hash of all args). Validated here
	// so a bad spec fails authoring instead of silently never
	// caching at dispatch.
	if cacheSpec, err := parseCacheArg(args["cache"]); err != nil {
		return "", fmt.Errorf("cache: %w", err)
	} else if cacheSpec != nil {
		tool.Cache = cacheSpec
	}
	// Network-grant lint: if the script_body uses a raw-network Python
	// or shell API (urllib / requests / socket / http.client / curl /
	// wget) AND the tool has neither a hook grant ("fetch" or
	// "fetch_via:..." in HookCapabilities) nor RawNetwork=true, the
	// tool will fail at first dispatch with a DNS-resolution error
	// (--unshare-net cuts the namespace). Refuse at create time with
	// directive guidance so the LLM re-authors with the right shape
	// instead of producing a tool that test_args would catch but
	// silent re-runs wouldn't. Same pattern as missingWorkspaceScriptRefs.
	if mismatch := networkGrantMismatch(tool); mismatch != "" {
		return "", fmt.Errorf("network-grant mismatch: %s", mismatch)
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
	spec := formatTempToolSpec(tool)

	// Persistence is no longer LLM-driven — the `persist` flag is
	// silently ignored (kept readable here as a comment instead of a
	// noisy error so old prompts don't break). Tools always land in
	// the session-scoped pool; the admin promotes them out of there
	// via the Tools modal in the chat surface.
	_ = BoolArg(args, "persist")
	saveSessionScoped()
	return fmt.Sprintf("Created temp tool %q. It is available in your tool catalog for the rest of this conversation. The user (admin) can promote it to keep past the session via the Tools modal.%s", name, spec), nil
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

	// Capture the tool's on-disk script filenames BEFORE removing
	// the record. We check all three pools (session / persistent /
	// pending) for the tool by name; the first match wins. The
	// captured names get unlinked after the registry removal so the
	// workspace doesn't accumulate orphan scripts when tools get
	// deleted.
	var scriptFiles []string
	collectScriptFiles := func(tt *TempTool) {
		if tt == nil {
			return
		}
		if tt.CanonicalScriptName != "" {
			scriptFiles = append(scriptFiles, tt.CanonicalScriptName)
		}
		if tt.ScriptName != "" && tt.ScriptName != tt.CanonicalScriptName {
			// Legacy tools may have only ScriptName populated (no
			// canonical) — try unlinking that too.
			scriptFiles = append(scriptFiles, tt.ScriptName)
		}
	}
	// Check the session pool first.
	for _, tt := range sess.TempTools {
		if tt != nil && tt.Name == name {
			collectScriptFiles(tt)
			break
		}
	}
	// Then the persistent pool.
	if len(scriptFiles) == 0 && sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			if p.Tool.Name == name {
				collectScriptFiles(&p.Tool)
				break
			}
		}
	}
	// Pending pool last.
	if len(scriptFiles) == 0 && sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			if p.Tool.Name == name {
				collectScriptFiles(&p.Tool)
				break
			}
		}
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

	// Clean up on-disk script files captured before the registry
	// removal. Best-effort: an unlink failure logs but doesn't fail
	// the delete (the tool's already gone from the catalog; a stale
	// script file is cruft, not a correctness issue). Idempotent
	// for missing files.
	if sess.WorkspaceDir != "" {
		seen := map[string]bool{}
		for _, f := range scriptFiles {
			if seen[f] {
				continue
			}
			seen[f] = true
			abs := filepath.Join(sess.WorkspaceDir, f)
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				Debug("[temptool] script cleanup: failed to unlink %s: %v", abs, err)
			} else if err == nil {
				Debug("[temptool] script cleanup: removed %s for tool %q", abs, name)
			}
		}
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
		out = append(out, agentToolDefsFromTemp(sess, tt)...)
	}
	return out
}

// agentToolDefsFromTemp renders one temp tool into the AgentToolDefs it
// contributes to the catalog. Almost every tool yields exactly one def;
// an EXPANDED toolbox (tt.Expand) yields one `<toolbox>_<action>` def per
// live action instead of a single collapsed action-dispatch entry. The
// single-entity boundary is untouched — one record, one credential, one
// artifact; expansion is purely presentation, decided here at build time.
func agentToolDefsFromTemp(sess *ToolSession, tt *TempTool) []AgentToolDef {
	// Drop a temp tool that collides with a dynamic built-in (e.g. a stale
	// send_message authored before the create-time guard existed). Leaving it in
	// would shadow the real, delivering tool with a stub. Dropping it here lets
	// the built-in (assembled separately at dispatch) take the name back.
	if IsReservedToolName(tt.Name) {
		return nil
	}
	if tt.Mode == TempToolModeToolbox && tt.Expand {
		return expandedToolboxDefs(sess, tt)
	}
	return []AgentToolDef{agentToolFromTemp(sess, tt)}
}

// expandedToolboxDefs surfaces each non-disabled toolbox action as its own
// top-level tool named `<toolbox>_<action>`, with the action's own params
// as its schema (no action="<sub>" indirection). Each handler pins the
// action and reuses the shared toolbox dispatcher, so credential, allow-
// list, audit, and response_pipe behavior are identical to the collapsed
// path. Disabled actions are quarantined — dropped from the catalog.
func expandedToolboxDefs(sess *ToolSession, tt *TempTool) []AgentToolDef {
	out := make([]AgentToolDef, 0, len(tt.Actions))
	for i := range tt.Actions {
		if tt.Actions[i].Disabled {
			continue
		}
		out = append(out, perActionToolDef(sess, tt, tt.Actions[i]))
	}
	return out
}

// perActionToolDef builds the standalone AgentToolDef for one expanded
// toolbox action. Mirrors the collapsed group's per-action handler (pin
// action, hand off to dispatchTempTool → dispatchToolboxModeTempTool) but
// promotes the action to a first-class catalog name with its own schema.
func perActionToolDef(sess *ToolSession, tt *TempTool, act TempToolAction) AgentToolDef {
	writes := isMutatingMethod(act.Method)
	kind := "read"
	if writes {
		kind = "write"
	}
	desc := act.Description + fmt.Sprintf(" (%s action of toolbox %q, credential %q; defined via tool_def)", kind, tt.Name, tt.Credential)
	return AgentToolDef{
		Tool: Tool{
			Name:        tt.Name + "_" + act.Name,
			Description: desc,
			Parameters:  act.Params,
			Required:    act.Required,
			Caps:        []Capability{CapNetwork, CapExecute},
		},
		NeedsConfirm: true,
		Handler: func(args map[string]any) (string, error) {
			a2 := make(map[string]any, len(args)+1)
			for k, v := range args {
				a2[k] = v
			}
			a2["action"] = act.Name
			// Resolve against the live record so a mid-turn edit to this
			// action (url/body/params) dispatches the current version, not
			// the turn-start snapshot (same staleness fix as the collapsed
			// path — agent-owned toolboxes are pinned static and skip the
			// dynamic refresh feed).
			live := sess.LookupTempTool(tt.Name)
			if live == nil {
				live = tt
			}
			return dispatchTempTool(sess, live, a2)
		},
	}
}

// isMutatingMethod reports whether an HTTP method has side effects — used
// to classify a toolbox action as read vs write for the LLM description
// and the admin audit view. Empty defaults to GET (read).
func isMutatingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", "GET", "HEAD", "OPTIONS":
		return false
	default:
		return true
	}
}

// newToolboxGroupedTool builds the framework GroupedTool for a toolbox-mode
// TempTool: one catalog name, action="<sub>" dispatch, disabled actions left
// out (quarantined). Each action handler synthesizes a single-endpoint api-
// mode TempTool at dispatch time via dispatchTempTool. Built from whatever
// record is passed — the caller passes the LIVE session record so a group
// rebuilt mid-call reflects mutations (see agentToolFromTemp's live-resolve
// handler).
func newToolboxGroupedTool(tt *TempTool) *GroupedTool {
	live := 0
	for i := range tt.Actions {
		if !tt.Actions[i].Disabled {
			live++
		}
	}
	gtDesc := tt.Description + fmt.Sprintf(" (toolbox — wraps credential %q with %d action(s), defined this session via tool_def)", tt.Credential, live)
	gt := NewGroupedTool(tt.Name, gtDesc)
	for i := range tt.Actions {
		if tt.Actions[i].Disabled {
			continue // quarantined — not offered
		}
		act := tt.Actions[i] // capture by value for the closure
		gt.AddAction(act.Name, &GroupedToolAction{
			Description: act.Description,
			Params:      act.Params,
			Required:    act.Required,
			Caps:        []Capability{CapNetwork, CapExecute}, // api-mode + response_pipe
			Handler: func(args map[string]any, s *ToolSession) (string, error) {
				// Re-attach the action key so the toolbox dispatcher
				// finds its routing handle. The framework's grouped
				// tool stripped it during routing; we put it back
				// because dispatchToolboxModeTempTool reads
				// args["action"] to look up the sub-action.
				a2 := make(map[string]any, len(args)+1)
				for k, v := range args {
					a2[k] = v
				}
				a2["action"] = act.Name
				return dispatchTempTool(s, tt, a2)
			},
		})
	}
	return gt
}

// tempToolCaps returns the capability tier a non-toolbox temp tool needs at
// dispatch. API mode needs CapNetwork (HTTP via the stored credential), plus
// CapExecute when it carries a response_pipe (sandboxed shell over the
// response). Shell / pipeline / everything else runs sandboxed shell →
// CapExecute. Shared by the def builder (declared caps, cap-gated by the
// loop) and the live-resolve guard (so a mid-turn edit can't dispatch a
// version that needs more than the loop gated on).
func tempToolCaps(tt *TempTool) []Capability {
	if tt.Mode == TempToolModeAPI {
		if tt.ResponsePipe != "" {
			return []Capability{CapNetwork, CapExecute}
		}
		return []Capability{CapNetwork}
	}
	return []Capability{CapExecute}
}

// capsSubset reports whether every capability in want is present in have —
// i.e. want does not exceed the granted set.
func capsSubset(want, have []Capability) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// tempToolNeedsConfirm reports whether a temp tool is consequential enough to
// require per-call approval. Credential/api tools reach a real endpoint;
// RawNetwork leaves the sandbox; a hook capability outside the read-only set
// (secret:<name>, fetch_via:<name>, …) grants more than a benign fetch. A plain
// shell tool that at most does an audited read-only fetch/log/browse is not
// consequential and runs freely.
func tempToolNeedsConfirm(tt *TempTool) bool {
	if tt == nil {
		return true
	}
	if tt.Mode == TempToolModeAPI || strings.TrimSpace(tt.Credential) != "" {
		return true
	}
	if tt.RawNetwork {
		return true
	}
	for _, c := range tt.HookCapabilities {
		switch c {
		case "fetch", "log", "browse_page":
			// read-only audited hooks — benign
		default:
			return true // secret:<name>, fetch_via:<name>, or any other capability
		}
	}
	return false
}

func agentToolFromTemp(sess *ToolSession, tt *TempTool) AgentToolDef {
	// Toolbox mode is structurally a GroupedTool — bundle of action-
	// dispatched sub-endpoints. The LLM-facing schema is identical to
	// built-in grouped tools (tool_def / agents / workspace): one tool
	// name in the catalog, action="<sub>" routes to the right endpoint.
	if tt.Mode == TempToolModeToolbox {
		snapshot := newToolboxGroupedTool(tt)
		def := ChatToolToAgentToolDefWithSession(snapshot, sess)
		// Live-resolve on dispatch. An agent-OWNED toolbox is pinned into
		// the static catalog (staticTempToolNames) and the per-round
		// dynamic-tool feed deliberately skips static names, so the group
		// built at turn start is re-registered every round and never
		// refreshed. Without this, a mid-turn tool_def update / delete /
		// recreate mutates the session record but dispatch keeps hitting
		// the frozen snapshot — a renamed action 404s as "unknown action"
		// and help lists stale names (observed: a whole moltbook rename
		// that never took effect). Rebuilding the group from the LIVE
		// record on each call makes mutations take effect immediately.
		// The schema shown to the LLM still reflects the snapshot until the
		// next turn (cosmetic — the model calls the name it just authored,
		// and both dispatch and the help action resolve against live).
		def.Handler = func(args map[string]any) (string, error) {
			live := sess.LookupTempTool(tt.Name)
			if live == nil || live.Mode != TempToolModeToolbox {
				return snapshot.RunWithSession(args, sess)
			}
			return newToolboxGroupedTool(live).RunWithSession(args, sess)
		}
		return def
	}

	// Caps depend on execution mode (see tempToolCaps). The AllowedCaps
	// filter then hides the tool from sessions that don't grant the tier.
	caps := tempToolCaps(tt)
	descSuffix := " (temp tool — defined this session via create_temp_tool)"
	if tt.Mode == TempToolModeAPI {
		descSuffix = fmt.Sprintf(" (api tool — wraps credential %q, defined this session via create_api_tool)", tt.Credential)
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        tt.Name,
			Description: tt.Description + descSuffix,
			Parameters:  tt.Params,
			Required:    tt.Required,
			Caps:        caps,
		},
		// Confirm only for CONSEQUENTIAL temp tools — ones that reach a real
		// endpoint (api mode / a credential), leave the sandbox (RawNetwork),
		// or hold a capability beyond the read-only audited hooks. A cred-less
		// shell tool whose only external effect is a fetch/log/browse_page hook
		// is read-only + low-consequence, so it runs WITHOUT an approval prompt
		// — matching how the interactive orchestrate path already treats it
		// (its confirm hook only gates credentialed tools), so an unattended
		// scheduled fire doesn't queue an approval for every benign tool call.
		NeedsConfirm: tempToolNeedsConfirm(tt),
		// Live-resolve on dispatch — same staleness fix as the toolbox path
		// above, generalized to api/shell/pipeline tools. An agent-OWNED
		// temp tool is pinned into the static catalog and skipped by the
		// per-round dynamic-tool refresh, so a mid-turn tool_def/add_tool
		// update would otherwise keep dispatching the frozen turn-start
		// snapshot (an edited url_template / body_template / command /
		// script_body never takes effect). Dispatch the LIVE record instead.
		// GUARD: only when the live tool needs no MORE capabilities than the
		// snapshot the loop already cap-gated on. A mid-turn edit that GROWS
		// the cap profile (e.g. an api tool gaining a response_pipe →
		// CapExecute) must wait for the next turn's fresh build + gate, so it
		// can't run an un-gated shell pipe this turn; until then the snapshot
		// dispatches. Non-cap edits — the common case — apply immediately.
		Handler: func(args map[string]any) (string, error) {
			live := sess.LookupTempTool(tt.Name)
			if live == nil {
				return dispatchTempTool(sess, tt, args)
			}
			if capsSubset(tempToolCaps(live), caps) {
				return dispatchTempTool(sess, live, args)
			}
			return dispatchTempTool(sess, tt, args)
		},
	}
}

// DispatchTempToolDirect dispatches a TempTool directly without
// requiring it to be registered in sess.tempTools. Used by authoring
// flows (e.g. orchestrate.add_tool) that want to immediately verify a
// freshly-authored tool with example args without round-tripping
// through "register, end turn, re-load on next round, dispatch by
// name". Same dispatch surface dispatchTempTool uses internally —
// shell vs api vs pipeline routing, sandbox, response_pipe, secure-api
// allow-list, the works.
func DispatchTempToolDirect(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if tt == nil {
		return "", fmt.Errorf("nil temp tool")
	}
	return dispatchTempTool(sess, tt, args)
}

// lookupArgCI looks up an arg by name with case-insensitive matching.
// First tries exact match (preserves intent when params have
// case-sensitive distinctions); falls back to case-folded lookup so
// "URL"/"url" don't trip required-arg validation against each other.
func lookupArgCI(args map[string]any, key string) (any, bool) {
	if v, ok := args[key]; ok {
		return v, true
	}
	keyLower := strings.ToLower(key)
	for k, v := range args {
		if strings.ToLower(k) == keyLower {
			return v, true
		}
	}
	return nil, false
}

// canonicalizeArgKeys rewrites case-variant keys onto the tool's
// declared parameter names. So if Required = ["url"] and the LLM
// emitted args["URL"], the returned map has args["url"]. Downstream
// template substitution and handler code see the canonical key
// regardless of what casing the LLM used.
func canonicalizeArgKeys(args map[string]any, required []string, params map[string]ToolParam) map[string]any {
	// Build the set of canonical names from required + declared params.
	canonical := map[string]string{} // lowercase → canonical
	for _, r := range required {
		canonical[strings.ToLower(r)] = r
	}
	for name := range params {
		canonical[strings.ToLower(name)] = name
	}
	if len(canonical) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if can, ok := canonical[strings.ToLower(k)]; ok {
			out[can] = v
		} else {
			out[k] = v
		}
	}
	return out
}

// buildEnvArgs converts the temp tool's args map to a string env map
// suitable for shell pass-through. Scalar values (string/number/bool)
// stringify naturally; arrays/objects get JSON-encoded so the script
// can json.loads() them back. Keys with characters illegal in env
// var names (spaces, hyphens) are sanitized; unhelpful but unlikely
// since temp tools use snake_case params by convention.
func buildEnvArgs(args map[string]any) map[string]string {
	// Always return a writable map, never nil: the dispatcher writes
	// GOHORT_HOOK_PATH into this map when the tool has a sandbox hook, and
	// a param-less tool (no args) with a hook — e.g. a poll-an-endpoint
	// script that takes no arguments — would otherwise panic with
	// "assignment to entry in nil map" at that write. len(args)==0 is the
	// COMMON case for such tools, not an edge one.
	out := make(map[string]string, len(args))
	if len(args) == 0 {
		return out
	}
	for k, v := range args {
		if k == "" || !isValidEnvVarName(k) {
			continue
		}
		switch val := v.(type) {
		case nil:
			out[k] = ""
		case string:
			out[k] = val
		case bool:
			if val {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
		case float64, float32, int, int64:
			out[k] = fmt.Sprint(val)
		default:
			// Arrays / objects — encode as JSON so the script can
			// parse if it wants structured data; degenerates to
			// "null" for unrepresentable values.
			if b, err := json.Marshal(val); err == nil {
				out[k] = string(b)
			} else {
				out[k] = fmt.Sprint(val)
			}
		}
	}
	return out
}

// isValidEnvVarName reports whether the string is a legal POSIX env
// var name (alpha/underscore start, then alphanumerics/underscores).
// LLM-authored params follow snake_case so this almost always passes.
func isValidEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
				return false
			}
			continue
		}
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// mapKeys returns the keys of an args map sorted for stable diag
// output. Used in error messages so the operator can see exactly
// what the LLM sent vs. what the tool declared.
func mapKeys(args map[string]any) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// dispatchTempTool routes to the right execution path based on the
// tool's Mode. Shell mode runs through RunSandboxedShell. API mode
// substitutes URL/body templates and dispatches through the secure-
// API call path against the named credential.
func dispatchTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("temp tool %q requires a session", tt.Name)
	}
	// Required-arg check (applies to both modes). Case-insensitive
	// lookup: LLMs sometimes emit "URL" when the tool defines "url"
	// (or vice versa), and we'd rather accept the call than block
	// over a casing mismatch. Empty-string and nil are treated as
	// missing — those would fail at the template-substitution step
	// anyway, so reject here with a clearer error.
	for _, r := range tt.Required {
		v, ok := lookupArgCI(args, r)
		if !ok || v == nil {
			Log("[temptool] %q rejecting call: required %q missing. provided keys: %v", tt.Name, r, mapKeys(args))
			return "", fmt.Errorf("missing required arg %q (provided: %v)", r, mapKeys(args))
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			Log("[temptool] %q rejecting call: required %q is empty string", tt.Name, r)
			return "", fmt.Errorf("required arg %q is empty (provide a value, not an empty string)", r)
		}
	}
	// Canonicalize keys onto the tool's declared names so case
	// variants from the LLM ("URL" → "url") feed through to
	// template substitution + handler code without surprises.
	args = canonicalizeArgKeys(args, tt.Required, tt.Params)

	// Cache lookup, when the tool spec opted into memoization. Wraps
	// every mode (shell / api / pipeline / persistent) uniformly —
	// caching is about input→output equivalence, not the backend.
	if tt.Cache != nil {
		if hit, ok := lookupTempToolCache(sess, tt, args); ok {
			Log("[temptool] %q cache hit — skipping exec", tt.Name)
			return hit, nil
		}
	}

	result, err := dispatchTempToolUncached(sess, tt, args)
	if err == nil && tt.Cache != nil {
		storeTempToolCache(sess, tt, args, result)
	}
	return result, err
}

// dispatchTempToolUncached is the per-mode dispatch core that
// dispatchTempTool wraps with cache lookup/store. Required-arg
// validation and key canonicalization have already happened above.
func dispatchTempToolUncached(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	// Secured-credential binding enforcement for the credential-dispatching modes
	// (api / toolbox dispatch through tt.Credential). Only APPROVED tools may reach
	// a secured cred; a legacy declaring tool is grandfathered, a revoked one
	// refused; open creds pass. Shell-mode fetch_via is enforced in the sandbox
	// hook (it dispatches per-call, not here). See secured-credential-tool-binding.md.
	if (tt.Mode == TempToolModeAPI || tt.Mode == TempToolModeToolbox) && strings.TrimSpace(tt.Credential) != "" {
		securedUser := ""
		if sess != nil {
			securedUser = sess.Username
		}
		if err := Secure().EnforceSecuredBinding(tt.Credential, tt.Name, securedUser); err != nil {
			return "", err
		}
	}
	if tt.Mode == TempToolModeAPI {
		return dispatchAPIModeTempTool(sess, tt, args)
	}
	if tt.Mode == TempToolModePipeline {
		return dispatchPipelineModeTempTool(sess, tt, args)
	}
	if tt.Mode == TempToolModePersistent {
		return dispatchPersistentShellTempTool(sess, tt, args)
	}
	if tt.Mode == TempToolModeToolbox {
		return dispatchToolboxModeTempTool(sess, tt, args)
	}

	// Shell-mode dispatch path. Three shapes depending on the tool:
	//
	//  (a) Recipe non-empty: persistent tool with packaged content.
	//      Mint a fresh per-invocation sandbox, deploy the recipe
	//      into it, optionally restore state subdir, run, save state
	//      back, tear down. Each dispatch starts from the same
	//      declarative manifest — no drift between runs.
	//
	//  (b) Recipe empty + command references {workspace_dir}:
	//      ad-hoc tool created mid-session (persist=false). Use the
	//      session's current WorkspaceDir as the deploy target so the
	//      LLM's just-written script is accessible.
	//
	//  (c) Recipe empty + no {workspace_dir} reference: pure shell
	//      command (e.g. "uname -a"). Provision an ephemeral tmpdir
	//      so bwrap has SOME bind target; the command doesn't depend
	//      on its contents.
	var workspaceDir string
	var ephemeralDir bool
	if len(tt.Recipe) > 0 {
		tmp, err := MintToolDispatchDir("tooldispatch-")
		if err != nil {
			return "", fmt.Errorf("mkdtemp: %w", err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		ephemeralDir = true
		if err := DeployRecipe(tt.Recipe, tmp); err != nil {
			return "", fmt.Errorf("deploy recipe: %w", err)
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
		// Recipe-less, no session workspace. The bwrap bind still
		// needs a path, but the command doesn't depend on its
		// contents — provision an ephemeral tmpdir for this
		// invocation only. Without this, stateless shell tools like
		// "echo '{text}' | rev" can't dispatch when the session has
		// no workspace.
		if strings.Contains(tt.CommandTemplate, "{workspace_dir}") {
			return "", fmt.Errorf("temp tool %q references {workspace_dir} but has no recipe and the session has no sandbox", tt.Name)
		}
		tmp, err := MintToolDispatchDir("tooldispatch-stateless-")
		if err != nil {
			return "", fmt.Errorf("mkdtemp for stateless dispatch: %w", err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		workspaceDir = tmp
	}
	_ = ephemeralDir
	cmdTemplate := strings.ReplaceAll(tt.CommandTemplate, "{workspace_dir}", shellQuote(workspaceDir))
	cmd, err := substitute(cmdTemplate, tt.Params, args)
	if err != nil {
		Log("[temptool] %q substitute failed: %v (template=%q args=%v)", tt.Name, err, cmdTemplate, mapKeys(args))
		return "", err
	}
	// Log the rendered command so authors can see whether args
	// actually made it into the command line. If the LLM passed
	// first_name but the template lacks {first_name}, the rendered
	// command will be obviously missing the value — and the script
	// will (correctly) reject it as "first_name is required".
	Log("[temptool] %q rendered command: %s", tt.Name, cmd)
	// Deployment state at dispatch time — answers "did the LLM author
	// this with script_body?" and "does the workspace have the file
	// the command_template expects?" before we even look at exec.
	// Critical when a tool hangs / errors and we need to know if the
	// redeploy block (next) will fire and where it'll write.
	Debug("[temptool] %q deploy state: script_body=%dB script_name=%q canonical=%q workspace=%s",
		tt.Name, len(tt.ScriptBody), tt.ScriptName, tt.CanonicalScriptName, workspaceDir)

	// Redeploy ScriptBody to {workspace_dir}/<CanonicalScriptName>
	// if missing or stale. Survives workspace wipes — the script
	// content lives on the tool's DB record, so the very first
	// dispatch into a fresh workspace rewrites it. Idempotent: if
	// the file already exists with matching content, no-op.
	//
	// We deploy under CanonicalScriptName (the framework's collision-
	// proof filename) but translate the command's references from
	// ScriptName (LLM-facing) to CanonicalScriptName afterward, so
	// the actual shell command resolves to the right file.
	if tt.ScriptBody != "" {
		onDiskName := tt.CanonicalScriptName
		if onDiskName == "" {
			// Legacy tools authored before canonicalization fall back
			// to ScriptName as the on-disk filename. Backfill will
			// populate CanonicalScriptName for them eventually.
			onDiskName = tt.ScriptName
		}
		if onDiskName == "" {
			return "", fmt.Errorf("tool %q has script_body but no script_name — record is malformed", tt.Name)
		}
		if strings.ContainsAny(onDiskName, "/\\") {
			return "", fmt.Errorf("invalid script filename %q on tool %q (no path separators allowed)", onDiskName, tt.Name)
		}
		scriptPath := filepath.Join(workspaceDir, onDiskName)
		needWrite := true
		if existing, err := os.ReadFile(scriptPath); err == nil {
			if string(existing) == tt.ScriptBody {
				needWrite = false
			}
		}
		Debug("[temptool] %q redeploy check: path=%s needWrite=%v", tt.Name, scriptPath, needWrite)
		if needWrite {
			if err := os.MkdirAll(filepath.Dir(scriptPath), 0700); err != nil {
				return "", fmt.Errorf("create parent dir for script %q on tool %q: %w", onDiskName, tt.Name, err)
			}
			if err := os.WriteFile(scriptPath, []byte(tt.ScriptBody), 0700); err != nil {
				return "", fmt.Errorf("redeploy script %q for tool %q: %w", onDiskName, tt.Name, err)
			}
			Debug("[temptool] redeployed script_body to %s for tool %q (%dB)", scriptPath, tt.Name, len(tt.ScriptBody))
		}
		// Translate every LLM-facing script_name reference in the
		// final command to the canonical on-disk filename. Last-
		// mile rewrite so the shell sees the right path while the
		// LLM's view of the tool record stays clean.
		if tt.CanonicalScriptName != "" && tt.ScriptName != "" && tt.CanonicalScriptName != tt.ScriptName {
			cmd = strings.ReplaceAll(cmd, tt.ScriptName, tt.CanonicalScriptName)
		}
	}

	// Redeploy bundled helper files (imported modules, sourced scripts)
	// alongside the entry script — same survives-a-wipe idempotent write
	// as ScriptBody, but under each file's LITERAL path because the entry
	// script pulls them in by that exact name (`import helper` -> helper.py,
	// `source lib.sh`). No canonical rename, no command translation. Skips
	// any path that isn't a plain in-workspace filename (defense against a
	// crafted imported record).
	for _, wf := range tt.WorkspaceFiles {
		rel := strings.TrimSpace(wf.Path)
		if rel == "" || strings.ContainsAny(rel, "/\\") || rel == ".." {
			Debug("[temptool] %q: skipping workspace_file with unsafe path %q", tt.Name, wf.Path)
			continue
		}
		mode := os.FileMode(wf.Mode)
		if mode == 0 {
			mode = 0700
		}
		filePath := filepath.Join(workspaceDir, rel)
		if existing, err := os.ReadFile(filePath); err == nil && string(existing) == wf.Content {
			continue // already present with matching content — no-op
		}
		if err := os.WriteFile(filePath, []byte(wf.Content), mode); err != nil {
			return "", fmt.Errorf("redeploy workspace file %q for tool %q: %w", rel, tt.Name, err)
		}
		Debug("[temptool] redeployed workspace file %s for tool %q (%dB)", filePath, tt.Name, len(wf.Content))
	}

	// Pre-exec validation for legacy tools: when ScriptBody is empty,
	// the redeploy block above doesn't fire — so a command_template
	// that references {workspace_dir}/<script>.py relies on the file
	// already being on disk (either left over from a prior local(write)
	// or… missing). Without this check the dispatch runs `python3
	// .../foo.py`, python exits with "can't open file" within ms, and
	// the agent-loop's retry behavior + SSE buffering make it look
	// like a hang. Surface the missing script as a directive error
	// the LLM (or admin) can act on. ScriptBody-bearing tools skip
	// this — they just redeployed the canonical script above and the
	// final cmd is translated to its name at line ~957.
	if tt.ScriptBody == "" {
		missing := missingWorkspaceScriptRefs(tt.CommandTemplate, workspaceDir)
		Debug("[temptool] %q missing-script validation: missing=%v", tt.Name, missing)
		if len(missing) > 0 {
			// State the CONDITION, not a guess at the cause. This used to assert
			// "a legacy tool authored before deploy-time validation existed",
			// which is a claim the check can't support: it fires for any record
			// with no script_body, including one authored seconds ago (an
			// authoring surface that dropped script_body) and the framework's own
			// documented local(write) path after a workspace wipe. A confidently
			// wrong diagnosis is worse than none — it sent an authoring model
			// chasing the wrong fix instead of the real one.
			return "", fmt.Errorf("tool %q references script(s) %v under {workspace_dir} that aren't on disk, and the tool record carries no script_body to redeploy them. Either the script was written into the workspace separately and the workspace has since been wiped, or the tool was authored without shipping its script. Fix: re-author with script_body=\"...\" so the script travels WITH the tool record (preferred — survives workspace wipes and export/import), or call local(action=\"write\", path=\"<exact-filename>\", content=\"...\") with a filename matching what command_template expects before each dispatch", tt.Name, missing)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Wrap with the session's network connector so the sandbox
	// applies --unshare-net when the calling turn is in private
	// mode. No-op when sess has no connector.
	ctx = sess.ContextWithNetworkConnector(ctx)
	// Tool-level network policy: shell-mode tools default to
	// --unshare-net regardless of session policy. Raw network is
	// granted only when the tool record explicitly opts in with
	// RawNetwork=true (the documented escape hatch for persistent-
	// mode REPLs and legacy tools). For everything else, the
	// authoring contract is: declare hook_capabilities=["fetch"]
	// and call gohort.fetch(...). Override layers DOWNWARD only —
	// if the session connector is already blocking, this can't
	// undo that.
	if !tt.RawNetwork {
		ctx = WithNetworkConnector(ctx, NewNetworkConnector(true))
	}

	// Pass every declared arg as an env var so scripts can read them
	// via $name (shell) or os.environ.get("name") (Python) — second
	// path alongside the {name} command-template substitution. Many
	// LLM-authored tools use shell-var conventions (`$first_name`),
	// which used to silently fail because the framework only did
	// {name} substitution. Both paths now coexist; tools authored
	// either way work.
	envArgs := buildEnvArgs(args)

	// SandboxHook: when the tool declared HookCapabilities, start a
	// per-dispatch UDS server inside the workspace, deploy the Python
	// helper module (`gohort.py`) so scripts can `from gohort import
	// fetch`, and expose the socket path via GOHORT_HOOK_PATH in the
	// sandbox env. The hook lets the script call back into gohort
	// for narrow capabilities (HTTP fetch, log, secret, fetch_via)
	// WITHOUT opening the sandbox's network namespace — gohort
	// proxies on its behalf. Empty HookCapabilities ⇒ no hook
	// started, no env var set, zero extra surface area.
	hook, hookErr := NewSandboxHook(workspaceDir, tt.HookCapabilities, sess)
	if hookErr != nil {
		return "", fmt.Errorf("start sandbox hook: %w", hookErr)
	}
	if hook != nil {
		defer hook.Close()
		// Identify the tool to the hook for secured-credential binding
		// enforcement on fetch_via. Set before the sandbox runs (below).
		hook.ToolName = tt.Name
		envArgs["GOHORT_HOOK_PATH"] = hook.SocketPath
		// The gohort helper package is bind-mounted RO into the
		// sandbox from a host-side library dir (see
		// EnsureGohortLibDir, wired in bwrapArgv). Nothing to deploy
		// into the workspace.
		Debug("[temptool] %q hook attached: %s caps=%v", tt.Name, hook.SocketPath, tt.HookCapabilities)
	}

	// Entry/exit breadcrumb around the sandbox call. The inner
	// [sandbox] spawn/exit lines bracket the actual exec; the outer
	// pair here catches anything wedged in argv setup / network
	// connector application / etc. between them. If you see this
	// "enter" but never the next "exit", the hang is inside the
	// sandbox call itself; if you don't see this "enter", the hang
	// is upstream (redeploy or missing-script validation).
	Debug("[temptool] %q sandbox enter (envArgs=%d hook=%v)", tt.Name, len(envArgs), hook != nil)
	tExec := time.Now()
	res := RunSandboxedShellWithEnv(ctx, cmd, workspaceDir, envArgs)
	Debug("[temptool] %q sandbox exit: dur=%s err=%v timedOut=%v outBytes=%d",
		tt.Name, time.Since(tExec), res.Err, res.TimedOut, len(res.Output))
	output := strings.TrimSpace(res.Output)

	// Extract attachment markers from stdout and route them to the
	// session's image/video channels. Lets shell-mode tools emit
	// binary attachments (e.g. fetch + convert an image, return it
	// as an attachment) — without the marker, shell tools can only
	// emit text, which makes use cases like "fetch a meme + convert
	// to PNG" impossible to express. See extractAttachmentMarkers.
	output = extractAttachmentMarkers(output, sess)

	// Save state back AFTER successful (or failed — preserve state
	// either way so state changes during partial runs aren't lost)
	// dispatch. Best-effort: state-save errors are logged but don't
	// fail the dispatch itself.
	if len(tt.Recipe) > 0 && tt.StatePath != "" {
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

// extractAttachmentMarkers scans the tool's stdout for attachment-
// emit markers and routes each found block to the appropriate session
// channel (Images / Videos). The markers are stripped from the
// returned text so the LLM doesn't see (and try to repeat) the raw
// base64 in its context. Returns the cleaned stdout.
//
// Marker format (designed to survive base64 / non-binary stdout):
//
//	<<<ATTACH:image/png
//	<base64 data, can span multiple lines>
//	ATTACH_END>>>
//
// Supported mime prefixes: image/*, video/*, audio/*. Anything else
// is left in the output as-is (the LLM sees it as text). Multiple
// markers per stdout are supported; each becomes one attachment.
//
// The marker is intentionally verbose to avoid colliding with normal
// tool output. A script that wanted to LITERALLY print
// "<<<ATTACH:..." (e.g. discussing the marker syntax in its own
// stdout) would have to avoid the exact opening sequence — rare
// enough that we don't bother with escaping.
func extractAttachmentMarkers(output string, sess *ToolSession) string {
	const openMarker = "<<<ATTACH:"
	const closeMarker = "ATTACH_END>>>"
	if !strings.Contains(output, openMarker) || !strings.Contains(output, closeMarker) {
		return output
	}
	var result strings.Builder
	remaining := output
	for {
		openIdx := strings.Index(remaining, openMarker)
		if openIdx < 0 {
			result.WriteString(remaining)
			break
		}
		// Emit everything before the marker as-is.
		result.WriteString(remaining[:openIdx])
		// Find the close marker.
		afterOpen := remaining[openIdx+len(openMarker):]
		closeIdx := strings.Index(afterOpen, closeMarker)
		if closeIdx < 0 {
			// Unterminated marker — emit the rest as-is, don't try
			// to interpret. Bad authoring, but don't silently swallow.
			result.WriteString(remaining[openIdx:])
			break
		}
		block := afterOpen[:closeIdx]
		// Block format: "mime/type\n<base64>\n" (mime ends at first newline).
		nl := strings.IndexByte(block, '\n')
		if nl < 0 {
			// No body — skip silently.
			remaining = afterOpen[closeIdx+len(closeMarker):]
			continue
		}
		mime := strings.TrimSpace(block[:nl])
		b64 := strings.TrimSpace(block[nl+1:])
		// Strip whitespace inside the base64 (multi-line OK).
		var clean strings.Builder
		for _, r := range b64 {
			if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
				continue
			}
			clean.WriteRune(r)
		}
		b64 = clean.String()
		if b64 != "" && sess != nil {
			switch {
			case strings.HasPrefix(mime, "image/"):
				sess.AppendImage(b64)
				Log("[temptool.attach] image attached via marker (mime=%s, b64_chars=%d)", mime, len(b64))
			case strings.HasPrefix(mime, "video/"), strings.HasPrefix(mime, "audio/"):
				sess.AppendVideo(b64)
				Log("[temptool.attach] media attached via marker (mime=%s, b64_chars=%d)", mime, len(b64))
			default:
				Log("[temptool.attach] unsupported marker mime %q — discarding block", mime)
			}
		}
		remaining = afterOpen[closeIdx+len(closeMarker):]
	}
	return strings.TrimSpace(result.String())
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
	out := map[string]ToolParam{}
	// A no-param tool/action is legitimate — a GET with no query string, a
	// shell command like "date", or an endpoint like /home or /list_submolts.
	// Treat absent OR empty params as a valid empty set instead of forcing the
	// author to invent a dummy "_ (Unused, required by API)" placeholder just
	// to pass validation. buildToolParamsSchema emits a valid {"type":"object"}
	// for an empty set and the dispatcher then requires nothing, so the tool is
	// callable with no args (a toolbox sub-action with just action="home").
	if v == nil {
		return out, nil
	}
	// Re-marshal whatever we got and unmarshal into our typed map. This
	// handles both the native object form and the stringified form the
	// LLM might produce when it emits a JSON blob.
	var raw any
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return out, nil
		}
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
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("must be an object of {name: {type, description}}: %w", err)
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
		// Reserved placeholders the dispatcher fills in (not user
		// params): {workspace_dir} resolves to the deployed sandbox
		// path. Skip param-list lookup for these.
		if name == "workspace_dir" {
			i = i + 1 + end
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
		// Type-aware substitution: numeric and boolean values skip
		// shell quoting and emit bare values, since their value
		// space is constrained enough that they can't contain
		// shell metacharacters by definition. String values still
		// go through shellQuote so injection-safety is preserved.
		//
		// Two paths fire as "skip quoting":
		//   1. Declared type is integer/number/boolean — the
		//      author signaled this is constrained data.
		//   2. The runtime VALUE is a number or bool — even if the
		//      author declared the param as "string", a value like
		//      float64(1) or true is safe to emit bare. This
		//      defends against LLMs that author tools with sloppy
		//      type declarations (everything typed as "string"),
		//      where a `count` param then fails the downstream
		//      script's int() / atoi() because it receives `'1'`
		//      instead of `1`.
		skipQuote := false
		switch params[name].Type {
		case "integer", "number", "boolean":
			skipQuote = true
		}
		if !skipQuote {
			switch val.(type) {
			case float64, float32, int, int64, int32, bool:
				skipQuote = true
			}
		}
		// Third defense: the value is a STRING but it parses as a
		// pure number or boolean literal. Worker LLMs often pass
		// numeric args as JSON strings ("1" instead of 1) when the
		// param's declared type is "string". Pure number / boolean
		// strings have no shell metacharacters by definition — safe
		// to emit bare, and necessary to avoid the int("1") → quoted
		// → script-side parse failure pattern.
		if !skipQuote {
			if s, ok := val.(string); ok {
				if looksLikeNumberLiteral(s) || looksLikeBoolLiteral(s) {
					skipQuote = true
				}
			}
		}
		if skipQuote {
			b.WriteString(stringify(val))
		} else {
			b.WriteString(shellQuote(stringify(val)))
		}
		i = i + 1 + end
	}
	return b.String(), nil
}

// canonicalScriptName builds the on-disk filename for a tool's
// script_body. Format: "<tool_name>_<short_content_hash>.<ext>".
// The hash is a sha256 prefix (8 hex chars = ~32 bits, 4B unique
// values — collisions are astronomically unlikely in a single-
// operator workspace). Combined with the tool name, this gives:
//   - Readable filename (debug: "see get_meme_a4f2b8e1.py in
//     workspace, that's the get_meme tool's current script")
//   - Deterministic per content (same body → same filename, so
//     redeploy is idempotent: no file = write; matching file =
//     no-op; different file = different name, no collision)
//   - Drift detection (if a tool's record shows hash "a4f2b8e1"
//     but the file on disk hashes differently, content drifted —
//     redeploy from the record overwrites)
//   - Re-author safety (delete + recreate a tool with same name
//     but different script body gets a different filename; old
//     file isn't touched, no stale-content risk)
//
// Extension is derived from (in priority order):
//  1. The LLM's script_name suffix, if a recognized language ext.
//  2. A shebang line in script_body ("#!/bin/sh" → .sh, etc.).
//  3. Default .py (Python is the dominant pattern).
func canonicalScriptName(toolName, llmHint, body string) string {
	ext := ".py"
	// First try the LLM's hint suffix.
	if i := strings.LastIndexByte(llmHint, '.'); i >= 0 {
		suffix := strings.ToLower(llmHint[i:])
		switch suffix {
		case ".py", ".sh", ".bash", ".jq", ".awk", ".sed", ".pl", ".rb", ".js", ".ts":
			ext = suffix
		}
	}
	// Shebang as fallback / override when LLM gave no hint.
	if firstLine := body; firstLine != "" {
		if nl := strings.IndexByte(firstLine, '\n'); nl >= 0 {
			firstLine = firstLine[:nl]
		}
		switch {
		case strings.Contains(firstLine, "python"):
			ext = ".py"
		case strings.Contains(firstLine, "/sh"), strings.Contains(firstLine, "/bash"):
			ext = ".sh"
		case strings.Contains(firstLine, "/jq"):
			ext = ".jq"
		}
	}
	// Sanitize the tool name for filesystem use. Tool names should
	// already be snake_case validated, but defense in depth.
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return -1
	}, toolName)
	if safe == "" {
		safe = "tool"
	}
	// Short content hash — sha256 first 8 hex chars. Deterministic
	// per body content so identical scripts always map to the same
	// filename; different content forces a different filename and
	// avoids any chance of a stale on-disk file masking a real
	// update.
	sum := sha256.Sum256([]byte(body))
	hashSuffix := hex.EncodeToString(sum[:4])
	return safe + "_" + hashSuffix + ext
}

// scriptExtensions lists the file suffixes the framework treats as
// "this is a script" for command_template validation purposes. Used
// by missingWorkspaceScriptRefs to distinguish script references
// (where missing → tool will be silently broken) from arbitrary
// workspace_dir output paths like data.json or screenshot.png
// (which the command itself produces and shouldn't be expected to
// pre-exist).
var scriptExtensions = map[string]bool{
	".py": true, ".sh": true, ".bash": true, ".jq": true,
	".awk": true, ".sed": true, ".pl": true, ".rb": true,
	".js": true, ".ts": true,
}

// workspaceScriptRefRe captures {workspace_dir}/<path>.<ext>
// references. The path may contain word chars, hyphens, dots, and
// slashes (subdirectories allowed). Extension match is checked
// against scriptExtensions; anything else is treated as a non-script
// reference and skipped (false-positive avoidance).
var workspaceScriptRefRe = regexp.MustCompile(`\{workspace_dir\}/([A-Za-z0-9_./\-]+\.[A-Za-z0-9]+)`)

// missingWorkspaceScriptRefs scans cmd for {workspace_dir}/<script>
// patterns and returns the filenames whose extension marks them as
// scripts AND which do not exist on disk under workspaceDir. Empty
// workspaceDir or empty cmd returns nil (no validation possible).
// De-duplicates so a script referenced twice surfaces once in the
// error message.
func missingWorkspaceScriptRefs(cmd, workspaceDir string) []string {
	if cmd == "" || workspaceDir == "" {
		return nil
	}
	matches := workspaceScriptRefRe.FindAllStringSubmatch(cmd, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var missing []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rel := m[1]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		ext := strings.ToLower(filepath.Ext(rel))
		if !scriptExtensions[ext] {
			continue // arbitrary output path, not a script invocation
		}
		full := filepath.Join(workspaceDir, rel)
		if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, rel)
		}
	}
	return missing
}

// presentWorkspaceScriptRefs is the complement of missingWorkspaceScriptRefs:
// it returns the {workspace_dir}/<script> references whose extension marks
// them as scripts AND which DO exist on disk under workspaceDir. Used at
// authoring time to capture a local(write)-authored script back into the
// tool record (so it travels with export and survives workspace wipes).
// De-duplicated; preserves first-seen order.
func presentWorkspaceScriptRefs(cmd, workspaceDir string) []string {
	if cmd == "" || workspaceDir == "" {
		return nil
	}
	matches := workspaceScriptRefRe.FindAllStringSubmatch(cmd, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var present []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rel := m[1]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		ext := strings.ToLower(filepath.Ext(rel))
		if !scriptExtensions[ext] {
			continue // arbitrary output path, not a script invocation
		}
		full := filepath.Join(workspaceDir, rel)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			present = append(present, rel)
		}
	}
	return present
}

// maxWorkspaceHelpers caps the transitive helper walk so a pathological
// import graph can't stuff an unbounded pile of files into a tool record.
const maxWorkspaceHelpers = 24

// pyImportRe matches Python `import foo` / `import foo, bar` /
// `from foo import x` / `from .foo import x` at the start of a (possibly
// indented) line. It captures the FIRST module token; comma-lists and
// dotted packages are handled by the caller splitting on the module name.
var pyImportRe = regexp.MustCompile(`(?m)^[ \t]*(?:from[ \t]+\.?([A-Za-z_][A-Za-z0-9_]*)|import[ \t]+([A-Za-z_][A-Za-z0-9_]*(?:[ \t]*,[ \t]*[A-Za-z_][A-Za-z0-9_]*)*))`)

// shSourceRe matches bash `source foo.sh` / `. foo.sh` (optionally
// ./-prefixed or quoted), capturing the referenced filename.
var shSourceRe = regexp.MustCompile(`(?m)^[ \t]*(?:source|\.)[ \t]+["']?(?:\./)?([A-Za-z0-9_./\-]+\.sh)["']?`)

// scriptHelperRefs returns the LITERAL sibling filenames a script body pulls
// in that could resolve to a helper file next to it — Python module imports
// (foo -> foo.py) and bash sources (foo.sh). Best-effort and language-scoped
// to Python/bash (where env-var params + gohort helpers already steer
// authoring); anything it doesn't recognize simply isn't followed, which is a
// no-op (the helper still sits in the shared workspace at runtime, it just
// doesn't travel). ext picks which import grammar to scan.
func scriptHelperRefs(body, ext string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	switch ext {
	case ".py":
		for _, m := range pyImportRe.FindAllStringSubmatch(body, -1) {
			// m[1] = from-import module; m[2] = import list (comma-separated).
			if m[1] != "" {
				add(m[1] + ".py")
			}
			if m[2] != "" {
				for _, mod := range strings.Split(m[2], ",") {
					if mod = strings.TrimSpace(mod); mod != "" {
						add(mod + ".py")
					}
				}
			}
		}
	case ".sh", ".bash":
		for _, m := range shSourceRe.FindAllStringSubmatch(body, -1) {
			if len(m) >= 2 {
				add(m[1])
			}
		}
	}
	return out
}

// gatherWorkspaceHelpers walks the dependency graph of a primary script and
// returns the helper files (as RecipeFile{Path,Content}) it pulls in that
// EXIST on disk under workspaceDir. Starting from primaryBody it follows
// Python imports / bash sources transitively (bounded), reading each resolved
// sibling and scanning IT for further helpers. primaryName is excluded so the
// entry script (which travels as ScriptBody) isn't duplicated. Best-effort:
// a helper that can't be resolved or read is simply skipped.
func gatherWorkspaceHelpers(primaryName, primaryBody, workspaceDir string) []RecipeFile {
	if workspaceDir == "" || primaryBody == "" {
		return nil
	}
	collected := map[string]bool{primaryName: true}
	var out []RecipeFile
	queue := scriptHelperRefs(primaryBody, strings.ToLower(filepath.Ext(primaryName)))
	for len(queue) > 0 && len(out) < maxWorkspaceHelpers {
		rel := queue[0]
		queue = queue[1:]
		if collected[rel] || strings.ContainsAny(rel, "/\\") {
			// Already have it, or a sub-path we won't chase (helpers live
			// beside the entry script in the flat workspace root).
			continue
		}
		collected[rel] = true
		full := filepath.Join(workspaceDir, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue // unresolved import (stdlib / third-party / typo) — skip
		}
		content, err := os.ReadFile(full)
		if err != nil || len(content) == 0 {
			continue
		}
		out = append(out, RecipeFile{Path: rel, Content: string(content), Mode: 0700})
		queue = append(queue, scriptHelperRefs(string(content), strings.ToLower(filepath.Ext(rel)))...)
	}
	return out
}

// rawNetworkPatterns names script-side APIs that bypass the hook and
// reach the network directly. Tools that use any of these MUST
// declare raw_network=true (to leave the bwrap namespace networked)
// or migrate to the hook (gohort.fetch / gohort.fetch_via). Detection
// is substring-match against the script_body — coarse but
// catches the common authoring mistakes (mostly Python urllib and
// shell curl/wget) without false positives in normal prose.
var rawNetworkPatterns = []string{
	"urllib.request",
	"urlopen(",
	"http.client",
	"requests.get",
	"requests.post",
	"requests.put",
	"requests.delete",
	"requests.request",
	"requests.Session",
	"socket.create_connection",
	"socket.connect",
	"curl ",
	"wget ",
}

// networkGrantMismatch returns a directive error string when the
// tool's script_body uses a raw-network API but the tool record
// doesn't declare a network grant (HookCapabilities including
// "fetch" or "fetch_via:..." OR RawNetwork=true). Returns "" when
// the grant matches the script's usage. The post-strict-network
// guard: --unshare-net would cause urllib / curl / etc. to fail
// with "Name or service not known" on every dispatch; catch at
// authoring time instead.
//
// Hook-only tools (HookCapabilities=["log"] with no fetch) still
// trigger the lint if the script tries to do raw HTTP — log alone
// doesn't grant network reach.
func networkGrantMismatch(tt *TempTool) string {
	if tt == nil || tt.ScriptBody == "" {
		return ""
	}
	if tt.RawNetwork {
		return ""
	}
	// Hook grants "fetch" or any "fetch_via:..." count as a network
	// grant via the proxy path — the script SHOULD use gohort.fetch,
	// but we can't easily tell whether it does without parsing.
	// Accept the grant and move on; the actual mismatch (script uses
	// urllib AND has fetch capability) is a possible authoring smell
	// but valid in principle (mixed-use tools).
	for _, c := range tt.HookCapabilities {
		if c == "fetch" || strings.HasPrefix(c, "fetch_via:") {
			return ""
		}
	}
	// No grant — check whether the script needs one.
	var found []string
	seen := map[string]bool{}
	for _, pat := range rawNetworkPatterns {
		if strings.Contains(tt.ScriptBody, pat) {
			if !seen[pat] {
				found = append(found, pat)
				seen[pat] = true
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("script_body uses raw-network API(s) %v but the tool has no network grant. Either (a) re-author with the hook: `from gohort import fetch` then `fetch(url)` instead of urllib.request.urlopen(url) — and declare hook_capabilities=[\"fetch\"]; or (b) declare raw_network=true (escape hatch for persistent-mode REPLs and non-HTTP TCP). Without one of these the sandbox runs --unshare-net and every outbound call fails with a DNS-resolution error", found)
}

// looksLikeNumberLiteral returns true when s parses cleanly as a
// JSON-style number (integer or decimal, optional leading sign). No
// shell metacharacters by definition; safe to emit bare. Used by
// substitute() to detect numeric strings the LLM passes in instead
// of native JSON numbers — common when the tool's param schema
// declares type="string" but the value is logically numeric.
func looksLikeNumberLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// strconv.ParseFloat accepts the JSON number grammar plus a bit
	// more (exponent forms, etc.) — all are safe to emit bare.
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// looksLikeBoolLiteral matches the canonical true/false literals.
// Case-sensitive: matches JSON / Python / Go convention.
func looksLikeBoolLiteral(s string) bool {
	s = strings.TrimSpace(s)
	return s == "true" || s == "false" || s == "True" || s == "False"
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
// quotes via the standard `'\”` POSIX trick. Suitable for any sh
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
			Description: "Object describing the tool's parameters. Same shape as create_temp_tool. Each key matches a {placeholder} in url_template or body_template. OPTIONAL — omit for a no-param endpoint (a GET with no query string); don't invent a dummy placeholder.",
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
		// Default to the always-bootstrapped no_auth credential when
		// the LLM omitted credential — public APIs are the common case
		// and forcing the author to specify credential="no_auth"
		// explicitly was friction without security benefit (the
		// no_auth credential is the deployment's open-by-default
		// pattern anyway). Admin tighten via no_auth's
		// AllowedURLPattern if scope-limiting matters.
		credName = "no_auth"
	}
	cr, ok := Secure().Load(credName)
	if !ok {
		return "", fmt.Errorf("credential %q is not registered — register it via the admin UI first", credName)
	}
	// A secured credential auto-binds to any tool that declares it (api-mode
	// dispatches server-side; secret never exposed) — no approval step, access
	// follows the tool's scope. Exception: an admin's explicit REVOKE is a durable
	// deny. See docs/secured-credential-tool-binding.md.
	if cr.Secured {
		if Secure().ToolBindingRevoked(credName, name) {
			return "", fmt.Errorf("credential %q is SECURED and tool %q's binding was REVOKED by an admin — ask them to restore it in Admin > APIs", credName, name)
		}
		_ = Secure().ApproveToolBinding(credName, name)
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
	// Omitted required → default all params required; an EXPLICIT [] → make
	// all optional. Distinguish by presence (see the toolbox path).
	if raw, present := args["required"]; !present || raw == nil {
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
	// Hard authoring gate (mirrors the toolbox path): a write action whose
	// required param appears in neither the url_template nor the body_template
	// sends it nowhere → a live 400 the author can't diagnose. Reject here.
	if unsent := unsentWriteParams(method, urlTpl, bodyTpl, required); len(unsent) > 0 {
		return "", fmt.Errorf("required param(s) %v are sent NOWHERE — this %s tool references them in neither url_template nor body_template, so the API never receives them (the cause of a 400 like \"content must be a string\"). Add a body_template that carries them, e.g. body_template: {\"content\": {content}}", unsent, method)
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

	spec := formatTempToolSpec(tool)

	// Persistence is admin-driven now — silently ignore the `persist`
	// flag if the LLM still sends it. Tools always land in the
	// session-scoped pool; the admin promotes via the Tools modal.
	_ = BoolArg(args, "persist")
	saveSessionScoped()
	return fmt.Sprintf("Created api tool %q (wraps credential %q). Available in your tool catalog for the rest of this conversation. The user (admin) can promote it via the Tools modal.%s", name, credName, spec), nil
}

// dispatchAPIModeTempTool handles a TempTool whose Mode is api. The
// URL template gets URL-path-encoded args; the optional body template
// gets JSON-encoded args. The resolved request is then dispatched
// through DispatchSecureAPICredentialCall, which validates the URL
// against the credential's allowlist and injects the encrypted secret.
// dispatchPipelineModeTempTool runs a pipeline-mode TempTool by
// spawning a sub-agent loop via the host session's SubAgentRunner.
// Args are substituted into PipelinePrompt as plain-text
// `{arg_name}` placeholders (it's a system prompt, not a shell or
// URL — no quoting/encoding rules). The user message handed to the
// sub-agent is a compact JSON dump of the args, so the sub-agent
// can also read arg values directly without re-parsing the prompt.
//
// Sub-agent's tool catalog is PipelineTools (subset of the parent
// session's). MaxRounds defaults to 6 when unset.
func dispatchPipelineModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	// Structured-steps path takes precedence — when the author set
	// pipeline_steps, run deterministically; no sub-agent, no LLM
	// per step. Cheaper, faster, predictable.
	if len(tt.PipelineSteps) > 0 {
		return dispatchPipelineStepsTempTool(sess, tt, args)
	}
	if sess.SubAgentRunner == nil {
		return "", fmt.Errorf("pipeline tool %q: host app didn't wire SubAgentRunner", tt.Name)
	}
	sys := tt.PipelinePrompt
	for k, v := range args {
		sys = strings.ReplaceAll(sys, "{"+k+"}", fmt.Sprint(v))
	}
	// The substituted system prompt already carries every arg value
	// in its intended position. Passing the same args AGAIN as a JSON
	// blob in the user message ("Inputs:\n{...}") was a recurring
	// foot-gun: smaller sub-agents pattern-matched the JSON-quoted
	// string values (e.g. "AI 2026") and emitted their downstream
	// tool calls with the quotes still on, which then read as a
	// shell-quoted literal by web_search etc. The kickoff message is
	// just a trigger now — the sub-agent works from the system prompt.
	userMsg := "Begin."
	maxRounds := tt.PipelineMaxRounds
	if maxRounds <= 0 {
		maxRounds = 6
	}
	argsJSON, _ := json.Marshal(args)
	Log("[temptool.pipeline] dispatch tool=%q inner_tools=%v max_rounds=%d args=%s",
		tt.Name, tt.PipelineTools, maxRounds, string(argsJSON))
	// Dump the substituted system prompt for verification when an
	// author reports "the framework is quoting my values" — the
	// substitution is plain text (fmt.Sprint + ReplaceAll above) so
	// any quotes the sub-agent sees come from the author's prompt.
	debugSys := sys
	if len(debugSys) > 600 {
		debugSys = debugSys[:600] + "...[truncated]"
	}
	Debug("[temptool.pipeline] tool=%q substituted system prompt: %s", tt.Name, debugSys)
	// Wall-clock ceiling: without this the nested agent ran on
	// context.Background() (bounded only by max_rounds), so a stalled or looping
	// pipeline hung the parent turn with no recovery. The deadline propagates
	// into the sub-agent's LLM calls, which cancel at the boundary.
	pctx, cancel := context.WithTimeout(context.Background(), TuneDuration("tune_pipeline_tool_timeout"))
	defer cancel()
	out, err := sess.SubAgentRunner(pctx, sys, userMsg, tt.PipelineTools, maxRounds)
	if err != nil {
		Log("[temptool.pipeline] tool=%q FAILED: %v", tt.Name, err)
		return "", fmt.Errorf("pipeline tool %q: %v", tt.Name, err)
	}
	Log("[temptool.pipeline] tool=%q OK (output=%d chars)", tt.Name, len(out))
	return out, nil
}

// dispatchPipelineStepsTempTool executes a structured pipeline step
// by step, with no inner LLM. Each step's args undergo template
// substitution against (caller args, prior step outputs) before the
// step's tool fires. The final step's output is the pipeline's
// return value.
//
// Substitution patterns inside string args:
//   - {param_name}    → caller's arg value
//   - $N              → entire string output of step N (1-indexed)
//   - $N.field.path   → JSON field path into step N's output;
//     returns empty string when the output isn't
//     JSON or the path doesn't resolve
//   - $name / $name.field — same as above but referencing a step's
//     Name (set by the author for readability)
//
// Aborts on the first error (no retry / no on_error policy in V1).
func dispatchPipelineStepsTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	allowed := map[string]bool{}
	for _, n := range tt.PipelineTools {
		allowed[n] = true
	}
	// Outputs is indexed two ways: by integer step number (1-based)
	// stored as the stringified int, AND by the optional step Name.
	// Both maps point into the same slice via parallel keys.
	rawOutputs := make([]string, 0, len(tt.PipelineSteps))
	jsonOutputs := make([]any, 0, len(tt.PipelineSteps))
	nameIndex := map[string]int{}

	Log("[temptool.pipeline_steps] dispatch tool=%q steps=%d", tt.Name, len(tt.PipelineSteps))
	for i, step := range tt.PipelineSteps {
		stepNum := i + 1
		toolName := strings.TrimSpace(step.Tool)
		if toolName == "" {
			return "", fmt.Errorf("step %d: tool name is required", stepNum)
		}
		if !allowed[toolName] {
			return "", fmt.Errorf("step %d: tool %q is not in this pipeline's allowed_tools list %v", stepNum, toolName, tt.PipelineTools)
		}
		// Resolve args — walk every value, substitute templates in
		// strings. Non-string values pass through unchanged so the
		// LLM can still pass numbers / bools / arrays.
		resolved := map[string]any{}
		for k, v := range step.Args {
			resolved[k] = resolvePipelineArg(v, args, rawOutputs, jsonOutputs, nameIndex)
		}
		// Dispatch the tool. The lookup goes through the session-
		// aware helper so session-scoped temp tools (drafts, inner
		// pipelines) are reachable.
		defs, err := GetAgentToolsWithSession(sess, toolName)
		if err != nil || len(defs) == 0 {
			return "", fmt.Errorf("step %d: tool %q not found in catalog: %v", stepNum, toolName, err)
		}
		out, err := defs[0].Handler(resolved)
		if err != nil {
			Log("[temptool.pipeline_steps] tool=%q step %d (%s) FAILED: %v", tt.Name, stepNum, toolName, err)
			return "", fmt.Errorf("step %d (%s): %v", stepNum, toolName, err)
		}
		Log("[temptool.pipeline_steps] tool=%q step %d (%s) OK (output=%d chars)", tt.Name, stepNum, toolName, len(out))
		rawOutputs = append(rawOutputs, out)
		// Best-effort JSON parse for later field-path resolution.
		// nil on parse failure is fine — accessor falls back to raw.
		var parsed any
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			parsed = nil
		}
		jsonOutputs = append(jsonOutputs, parsed)
		if name := strings.TrimSpace(step.Name); name != "" {
			nameIndex[name] = i
		}
	}
	if len(rawOutputs) == 0 {
		return "", fmt.Errorf("pipeline tool %q: no steps defined", tt.Name)
	}
	return rawOutputs[len(rawOutputs)-1], nil
}

// resolvePipelineArg walks a single arg value applying template
// substitution rules. Non-string values pass through unchanged so
// numbers, bools, arrays etc. survive the round trip.
func resolvePipelineArg(v any, callerArgs map[string]any, rawOutputs []string, jsonOutputs []any, nameIndex map[string]int) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	return substitutePipelineTemplate(s, callerArgs, rawOutputs, jsonOutputs, nameIndex)
}

// substitutePipelineTemplate applies the three substitution patterns
// in order: {param}, $N.field, $name.field, $N, $name. Returns the
// resulting string. Unknown references render as empty string rather
// than the literal token so a typo doesn't leak template syntax into
// the next tool's input.
func substitutePipelineTemplate(s string, callerArgs map[string]any, rawOutputs []string, jsonOutputs []any, nameIndex map[string]int) string {
	// {param} substitution.
	for k, v := range callerArgs {
		s = strings.ReplaceAll(s, "{"+k+"}", fmt.Sprint(v))
	}
	// $N / $N.field / $name / $name.field — walk char-by-char to
	// avoid regex complexity and gracefully handle adjacency.
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '$' {
			out.WriteByte(s[i])
			i++
			continue
		}
		// Read the identifier (digits or letters/underscores).
		j := i + 1
		for j < len(s) && (isIdentChar(s[j])) {
			j++
		}
		ident := s[i+1 : j]
		if ident == "" {
			// Lone '$' — emit literally.
			out.WriteByte('$')
			i++
			continue
		}
		// Optional dotted path.
		path := ""
		k := j
		if k < len(s) && s[k] == '.' {
			pathStart := k + 1
			for k < len(s) && (s[k] == '.' || isIdentChar(s[k])) {
				k++
			}
			path = s[pathStart:k]
		}
		// Resolve the identifier to a step index.
		var stepIdx int = -1
		if n, err := strconv.Atoi(ident); err == nil {
			stepIdx = n - 1 // 1-indexed → 0-indexed
		} else if idx, ok := nameIndex[ident]; ok {
			stepIdx = idx
		}
		if stepIdx < 0 || stepIdx >= len(rawOutputs) {
			// Unresolved reference — emit empty (silent miss).
			i = k
			continue
		}
		if path == "" {
			out.WriteString(rawOutputs[stepIdx])
		} else {
			out.WriteString(extractJSONPath(jsonOutputs[stepIdx], path))
		}
		i = k
	}
	return out.String()
}

func isIdentChar(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

// extractJSONPath walks a dotted path into a parsed JSON value.
// Returns the resolved value as a string (json-encoded for objects /
// arrays, stringified for scalars). Empty string on miss.
func extractJSONPath(root any, path string) string {
	if root == nil || path == "" {
		return ""
	}
	cur := root
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		next, ok := m[part]
		if !ok {
			return ""
		}
		cur = next
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// dispatchToolboxModeTempTool routes a toolbox-mode TempTool call to
// the named sub-action. The caller passes action="<name>" plus the
// action's own args; we look up the action, synthesize a single-
// endpoint api-mode TempTool that shares the parent's Credential,
// and reuse dispatchAPIModeTempTool. This keeps every api-mode
// concern (allow-list, audit log, response_pipe sandbox, etc.) in
// one place — toolbox is a packaging primitive, not a separate
// execution path.
func dispatchToolboxModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if len(tt.Actions) == 0 {
		return "", fmt.Errorf("toolbox tool %q has no actions defined", tt.Name)
	}
	actionName := strings.TrimSpace(StringArg(args, "action"))
	if actionName == "" {
		names := make([]string, 0, len(tt.Actions))
		for _, a := range tt.Actions {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("toolbox tool %q requires action=<name>. available: %v", tt.Name, names)
	}
	var act *TempToolAction
	for i := range tt.Actions {
		if tt.Actions[i].Name == actionName {
			act = &tt.Actions[i]
			break
		}
	}
	if act == nil {
		names := make([]string, 0, len(tt.Actions))
		for _, a := range tt.Actions {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("toolbox %q: no action named %q. available: %v", tt.Name, actionName, names)
	}
	if act.Disabled {
		return "", fmt.Errorf("toolbox %q action %q is disabled (quarantined). Fix it and re-enable via tool_def(action=\"update\", name=%q, actions=[{name:%q, disabled:false, ...}])", tt.Name, act.Name, tt.Name, act.Name)
	}
	// Drop the routing key so the synthetic api-mode tool doesn't see
	// it (action isn't one of its declared params, and the substitute
	// step would treat it as noise).
	inner := make(map[string]any, len(args))
	for k, v := range args {
		if k == "action" {
			continue
		}
		inner[k] = v
	}
	// Synthesize a single-endpoint api-mode tool. Carries the parent's
	// Credential + the action's URL/method/body/pipe + the action's
	// declared params + required list. Name encodes both layers so the
	// inner machinery's logs (rendered URL, response pipe failures)
	// stay attributable to the toolbox+action pair.
	synthetic := TempTool{
		Name:            tt.Name + "." + act.Name,
		Description:     act.Description,
		Params:          act.Params,
		Required:        act.Required,
		Mode:            TempToolModeAPI,
		CommandTemplate: act.URLTemplate,
		Credential:      tt.Credential,
		Method:          act.Method,
		BodyTemplate:    act.BodyTemplate,
		ResponsePipe:    act.ResponsePipe,
	}
	// Required-arg check (mirrors the top-level dispatchTempTool guard
	// but scoped to the action's params, since the outer toolbox tool
	// only enforces "action" was provided).
	for _, r := range synthetic.Required {
		v, ok := lookupArgCI(inner, r)
		if !ok || v == nil {
			return "", fmt.Errorf("toolbox %q action %q: missing required arg %q", tt.Name, act.Name, r)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("toolbox %q action %q: required arg %q is empty", tt.Name, act.Name, r)
		}
	}
	inner = canonicalizeArgKeys(inner, synthetic.Required, synthetic.Params)
	return dispatchAPIModeTempTool(sess, &synthetic, inner)
}

func dispatchAPIModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if sess.DB == nil {
		return "", fmt.Errorf("api tool %q requires a session with DB access", tt.Name)
	}
	// Network connector — API-mode is intrinsically network. Refuse
	// pre-dispatch when the connector blocks rather than letting the
	// HTTP layer fail with a confusing error. Same framework-floor
	// guarantee that gates web_search / fetch_url / the sandbox.
	if !sess.NetworkAllowed() {
		return "", fmt.Errorf("api tool %q refused: network is blocked for this turn (private mode is on)", tt.Name)
	}
	urlStr, err := substituteURL(tt.CommandTemplate, tt.Params, tt.Required, args)
	if err != nil {
		return "", fmt.Errorf("url template: %w", err)
	}
	method := strings.ToUpper(strings.TrimSpace(tt.Method))
	if method == "" {
		method = "GET"
	}
	var body string
	if tt.BodyTemplate != "" {
		body, err = substituteJSON(tt.BodyTemplate, tt.Params, tt.Required, args)
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
	// When a response_pipe is configured, signal the dispatch layer to
	// read with a higher byte cap and skip the truncation marker —
	// the pipe will project the body down to a small output that fits
	// in context. Without this hint, large list-style endpoints get
	// cut mid-string and jq fails with "Unfinished string at EOF".
	var raw string
	if tt.Credential == "" {
		// Public-API path: no credential → plain HTTP. Same risk
		// profile as fetch_url (which the LLM can already call
		// directly). Used for public JSON endpoints (Reddit,
		// Wikipedia, public data feeds) where requiring a fake
		// credential just to satisfy the dispatcher would be silly.
		raw, err = dispatchPublicAPICall(urlStr, method, body)
	} else if tt.ResponsePipe != "" {
		raw, err = Secure().DispatchToolCallForPipe(sess, tt.Credential, urlStr, method, body)
	} else {
		raw, err = Secure().DispatchToolCall(sess, tt.Credential, urlStr, method, body)
	}
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
		hint := ""
		// jq prints a red-herring "Unix shell quoting issues?" on a
		// compile error; the real culprit is almost always the `//`
		// alternative operator used bare in object construction, which
		// jq requires parenthesized. Say so directly.
		if strings.Contains(tt.ResponsePipe, "//") && strings.Contains(fmt.Sprint(pres.Err), "//") {
			hint = " HINT: jq requires the `//` alternative operator to be PARENTHESIZED inside object construction — write `{k: (.a // .b)}`, not `{k: .a // .b}`. Fix the response_pipe (delete + recreate the tool)."
		}
		if piped == "" {
			// The pipe produced nothing usable — but the HTTP call ALREADY
			// SUCCEEDED here (2xx; non-2xx returned raw above). Returning only
			// the pipe error HIDES that success: for a mutating call (POST) the
			// model reads "failed", retries, and double-submits — observed live,
			// a successful moltbook post reported as failed, retried into a 429,
			// then the model flailed reading the feed. Fall back to the raw body
			// (truncated) with an explicit do-not-retry directive so the model
			// sees the call worked and can read the result despite the broken
			// projection. The truncation bounds the context cost the pipe was
			// meant to avoid.
			rawBody := strings.TrimSpace(body)
			if len(rawBody) > maxOutput {
				rawBody = rawBody[:maxOutput] + "\n... [truncated]"
			}
			header := statusLine
			if header == "" {
				header = "HTTP 2xx"
			}
			return fmt.Sprintf("%s\n[response_pipe failed: %v — the HTTP call SUCCEEDED; showing the RAW response below. Do NOT retry the call (a repeat POST would double-submit). Fix the pipe later via delete + recreate. Pipe: %s]%s\n%s", header, pres.Err, tt.ResponsePipe, hint, rawBody), nil
		}
		return piped + fmt.Sprintf("\n[response_pipe exit: %v]%s", pres.Err, hint), nil
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

// dispatchPublicAPICall makes a plain HTTP call for api-mode tools
// authored without a credential. Used for public APIs (Reddit JSON,
// Wikipedia, etc.) where requiring a registered credential just to
// satisfy the dispatcher would force an awkward pipeline+fetch_url
// indirection. Same risk profile as fetch_url which the LLM can
// already call directly.
//
// Returns the same "HTTP <code> <text>\n<body>" shape as
// Secure().DispatchToolCall so downstream response_pipe logic and
// status-line handling keep working unchanged.
func dispatchPublicAPICall(urlStr, method, body string) (string, error) {
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequest(method, urlStr, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", publicAPIUserAgent)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: publicAPITimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http %s %s: %w", method, urlStr, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, publicAPIMaxResponseBytes)
	bodyBytes, readErr := io.ReadAll(limited)
	if readErr != nil {
		return "", fmt.Errorf("read response: %w", readErr)
	}
	statusLine := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	out := statusLine + "\n" + string(bodyBytes)
	// Mark truncation explicitly when the body hit the cap so the LLM
	// can see whether it received the full payload.
	if int64(len(bodyBytes)) == publicAPIMaxResponseBytes {
		out += fmt.Sprintf("\n\n[response truncated at %d bytes]", publicAPIMaxResponseBytes)
	}
	return out, nil
}

const (
	publicAPITimeout          = 30 * time.Second
	publicAPIMaxResponseBytes = 1 * 1024 * 1024 // 1 MB
	publicAPIUserAgent        = "gohort-public-api-tool/1.0"
)

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
func substituteURL(tmpl string, params map[string]ToolParam, required []string, args map[string]any) (string, error) {
	// An OPTIONAL query param whose value wasn't provided should drop out of
	// the URL entirely — otherwise a template like "?sort={sort}&limit={limit}"
	// can never be called without every param, defeating the point of making
	// them optional (observed live: feed's limit/sort couldn't be omitted).
	// Strip whole "[?&]key={name}" segments for a known, NON-required param
	// with no arg; a required or path-position placeholder still errors below.
	reqSet := make(map[string]bool, len(required))
	for _, r := range required {
		reqSet[r] = true
	}
	tmpl = dropAbsentOptionalQuery(tmpl, params, reqSet, args)
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

// dropAbsentOptionalQuery removes "key={name}" query segments whose {name} is a
// known, non-required param with no provided arg, so an omitted optional query
// param yields a clean URL rather than a "missing arg" error or a literal
// "?limit={limit}". Only touches the query string (after the first "?"), and
// only segments whose value is a single bare "{placeholder}"; anything mixed or
// required is left for the normal substitution/validation to handle.
func dropAbsentOptionalQuery(tmpl string, params map[string]ToolParam, reqSet map[string]bool, args map[string]any) string {
	q := strings.IndexByte(tmpl, '?')
	if q < 0 {
		return tmpl
	}
	path, query := tmpl[:q], tmpl[q+1:]
	segs := strings.Split(query, "&")
	kept := segs[:0]
	for _, seg := range segs {
		if eq := strings.IndexByte(seg, '='); eq >= 0 {
			val := seg[eq+1:]
			if len(val) >= 2 && val[0] == '{' && val[len(val)-1] == '}' &&
				strings.IndexByte(val, '}') == len(val)-1 {
				name := val[1 : len(val)-1]
				if _, known := params[name]; known && !reqSet[name] {
					if _, provided := args[name]; !provided {
						continue // drop this optional, unprovided segment
					}
				}
			}
		}
		kept = append(kept, seg)
	}
	if len(kept) == 0 {
		return path
	}
	return path + "?" + strings.Join(kept, "&")
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
func substituteJSON(tmpl string, params map[string]ToolParam, required []string, args map[string]any) (string, error) {
	// An OPTIONAL body field whose value wasn't provided should drop out of
	// the JSON entirely — the same guarantee substituteURL gives query params.
	// Without this, a body template like {"title":{title},"url":{url}} can
	// never be posted without a url, so a plain text post fails with a cryptic
	// "body template: missing arg url" (observed live: moltbook's post action,
	// where url is only for link posts). Strip whole "key":{name} properties
	// for a known, NON-required param with no arg; required placeholders still
	// error below.
	reqSet := make(map[string]bool, len(required))
	for _, r := range required {
		reqSet[r] = true
	}
	tmpl = dropAbsentOptionalJSONFields(tmpl, params, reqSet, args)
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

// dropAbsentOptionalJSONFields removes whole "key": {name} object properties
// from a JSON body template when {name} is a known, non-required param with no
// provided arg — so an omitted optional body field yields clean, valid JSON
// rather than a "missing arg" error. It handles the property's adjacent comma
// (whether the field sits first, middle, or last in the object) and tolerates
// a quote-wrapped placeholder ("key": "{name}"). Required params and provided
// optionals are left for normal substitution/validation. Only exact
// "key": {name} shapes are touched; anything else is left intact.
func dropAbsentOptionalJSONFields(tmpl string, params map[string]ToolParam, reqSet map[string]bool, args map[string]any) string {
	for name := range params {
		if reqSet[name] {
			continue
		}
		if _, provided := args[name]; provided {
			continue
		}
		ph := regexp.QuoteMeta("{" + name + "}")
		// The property value is the bare placeholder or a quote-wrapped one.
		prop := `"[A-Za-z0-9_]+"\s*:\s*"?` + ph + `"?`
		// Order matters: consume a trailing comma first (field is first or
		// middle), then a leading comma (field is last), then the lone field
		// (only property in the object).
		tmpl = regexp.MustCompile(prop+`\s*,\s*`).ReplaceAllString(tmpl, "")
		tmpl = regexp.MustCompile(`\s*,\s*`+prop).ReplaceAllString(tmpl, "")
		tmpl = regexp.MustCompile(prop).ReplaceAllString(tmpl, "")
	}
	return tmpl
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

// inferCommandTemplate produces a sensible default command_template
// for a script_body when the caller forgot to specify one. Returns
// empty string when no interpreter can be guessed — caller falls
// back to a directive error.
//
// Strategy:
//   - Shebang on line 1 → use it directly (script must be executable
//     at dispatch; the framework writes script files with 0700 so a
//     shebang line resolves naturally).
//   - Recognized extension → known interpreter:
//     .py → python3, .sh/.bash → bash, .jq → jq -f, .js → node,
//     .rb → ruby, .pl → perl.
//   - Otherwise empty (no inference).
//
// Params are NOT inlined as positional placeholders. The framework
// already passes every declared param to the script as an environment
// variable (see RunSandboxedShellWithEnv) — adding positional args on
// top forces the script to choose between sys.argv and os.environ,
// and the ordering of those positional args (alphabetical?
// insertion-order?) becomes a footgun the LLM keeps tripping on.
// Standardizing on env-vars-only means: scripts read params with
// `os.environ['name']` (Python) / `$name` (bash); ordering is a
// non-question; insertion + retrieval are both by NAME.
//
// If the caller really wants positional args (third-party scripts
// that take strict argv shapes), they supply an explicit
// command_template — the inference only fires when command_template
// is omitted.
// PrepareScriptBody is the shared implementation of the script_body authoring
// shortcut: infer command_template when the author omitted it, materialize the
// script into the session workspace under a collision-proof canonical name, and
// hand back the fields to stamp onto the TempTool record.
//
// It exists so the two authoring surfaces can't drift. tool_def grew this
// behavior inline; add_tool never had it, and silently DROPPED a script_body it
// was handed — the tool record kept a command_template pointing at a file that
// was never written, so the first dispatch failed with a "script doesn't exist"
// error that blamed a "legacy tool". Any surface that accepts script_body must
// route through here.
//
// cmd is the caller's command_template ("" to infer). Returns the effective
// template plus the LLM-facing and canonical script names. A blank scriptBody
// is a no-op that echoes cmd back, so callers can call it unconditionally.
func PrepareScriptBody(sess *ToolSession, toolName, cmd, scriptBody, scriptName string, params any) (outCmd, outScriptName, outCanonical string, err error) {
	if strings.TrimSpace(scriptBody) == "" {
		return cmd, "", "", nil
	}
	if scriptName == "" {
		scriptName = "script.py"
	}
	if strings.ContainsAny(scriptName, "/\\") {
		return "", "", "", fmt.Errorf("script_name must be a single filename (no path separators)")
	}
	if strings.TrimSpace(cmd) == "" {
		paramOrder, perr := paramNamesInDefinitionOrder(params)
		if perr == nil {
			cmd = inferCommandTemplate(scriptName, scriptBody, paramOrder)
			if cmd != "" {
				Log("[temptool] auto-inferred command_template=%q (script=%s, params=%v) — supply command_template explicitly for kwargs / stdin / non-positional shapes",
					cmd, scriptName, paramOrder)
			}
		}
	}
	if strings.TrimSpace(cmd) == "" {
		return "", "", "", fmt.Errorf("command_template is required (or supply script_body with a recognized extension — .py/.sh/.bash/.js/.jq/.rb — and the framework will infer it; declared params reach the script as ENVIRONMENT VARIABLES, not positional argv — read them with os.environ['name'])")
	}
	canonical := canonicalScriptName(toolName, scriptName, scriptBody)
	if _, werr := EnsureSessionWorkspace(sess); werr != nil {
		return "", "", "", fmt.Errorf("auto-mint workspace: %w", werr)
	}
	scriptPath := filepath.Join(sess.WorkspaceDir, canonical)
	if mkerr := os.MkdirAll(filepath.Dir(scriptPath), 0700); mkerr != nil {
		return "", "", "", fmt.Errorf("create parent dir for script %q: %w", canonical, mkerr)
	}
	if werr := os.WriteFile(scriptPath, []byte(scriptBody), 0700); werr != nil {
		return "", "", "", fmt.Errorf("write script %q: %w", canonical, werr)
	}
	// The template must actually reference the script, or the tool is born
	// broken — the exact failure this helper exists to prevent.
	if !strings.Contains(cmd, scriptName) && !strings.Contains(cmd, "{workspace_dir}") {
		return "", "", "", fmt.Errorf("script_body was written to %s (canonical %s) but command_template %q doesn't reference it — add {workspace_dir}/%s to the template", scriptPath, canonical, cmd, scriptName)
	}
	return cmd, scriptName, canonical, nil
}

func inferCommandTemplate(scriptName, scriptBody string, paramOrder []string) string {
	if scriptName == "" {
		return ""
	}
	interpreter := ""
	if shebang := firstShebang(scriptBody); shebang != "" {
		// Use the file directly; the kernel resolves the shebang.
		interpreter = ""
	} else {
		ext := strings.ToLower(filepath.Ext(scriptName))
		switch ext {
		case ".py":
			interpreter = "python3"
		case ".sh", ".bash":
			interpreter = "bash"
		case ".jq":
			interpreter = "jq -f"
		case ".js":
			interpreter = "node"
		case ".rb":
			interpreter = "ruby"
		case ".pl":
			interpreter = "perl"
		default:
			return "" // unknown — caller must supply command_template
		}
	}
	// paramOrder is intentionally unused — see the doc comment above.
	_ = paramOrder
	var b strings.Builder
	if interpreter != "" {
		b.WriteString(interpreter)
		b.WriteByte(' ')
	}
	b.WriteString("{workspace_dir}/")
	b.WriteString(scriptName)
	return b.String()
}

// detectForbiddenNetworkPatterns returns a human-readable description
// of any sandbox-incompatible network-library use found in script_body.
// Empty string means clean.
//
// The patterns we refuse are the ones that DEFINITELY cannot work
// inside the bwrap sandbox (--unshare-net). False positives here have
// real cost — refusing a legitimate use breaks authoring — so we keep
// the list focused on the calls that are network-doing (not just
// `import socket` since AF_UNIX sockets work, not just `import urllib`
// since urllib.parse is useful for URL encoding).
//
// Patterns:
//
//	import requests / from requests import …      → requests library
//	import urllib2                                → Py2 legacy net
//	urllib.request.urlopen / urllib.urlopen       → urllib network
//	from urllib.request import …                  → urllib network
//	from urllib import urlopen                    → urllib network
//	import http.client / from http.client …       → http.client
//	socket.create_connection / socket.connect(    → raw socket dialing
//	curl <url> / wget <url>                       → shell HTTP
//	subprocess.* with "curl" or "wget"            → wrapped shell HTTP
func detectForbiddenNetworkPatterns(script string) string {
	patterns := []struct {
		needle string
		label  string
	}{
		// Python network libs.
		{"import requests", "import requests"},
		{"from requests import", "from requests import …"},
		{"import urllib2", "import urllib2"},
		{"urllib.request", "urllib.request"},
		{"from urllib.request import", "from urllib.request import …"},
		{"from urllib import urlopen", "from urllib import urlopen"},
		{"urllib.urlopen", "urllib.urlopen"},
		{"import http.client", "import http.client"},
		{"from http.client import", "from http.client import …"},
		{"socket.create_connection", "socket.create_connection"},
		{"socket.connect(", "socket.connect()"},
		// Shell HTTP. Match with leading space / line-start so a
		// substring inside a longer word doesn't false-positive.
		{"\ncurl ", "curl"},
		{" curl ", "curl"},
		{"\nwget ", "wget"},
		{" wget ", "wget"},
		// Catch curl/wget wrapped in subprocess too.
		{"subprocess.run([\"curl", "subprocess.run([\"curl …\"])"},
		{"subprocess.run(['curl", "subprocess.run(['curl …'])"},
		{"subprocess.run([\"wget", "subprocess.run([\"wget …\"])"},
		{"subprocess.run(['wget", "subprocess.run(['wget …'])"},
		{"os.system(\"curl", "os.system(\"curl …\")"},
		{"os.system('curl", "os.system('curl …')"},
		{"os.system(\"wget", "os.system(\"wget …\")"},
		{"os.system('wget", "os.system('wget …')"},
	}
	for _, p := range patterns {
		if strings.Contains(script, p.needle) {
			return p.label
		}
	}
	return ""
}

// ungrantedCalls represents one or more credential-bearing calls
// found in script_body that aren't covered by HookCapabilities.
// calls and suggest are pre-formatted for the directive error.
type ungrantedCalls struct {
	calls   string
	suggest string
}

// findUngrantedCredentialCalls scans script_body for gohort.secret(...)
// and gohort.fetch_via(...) calls, extracts the credential name from
// the first string-literal arg, and returns any names that aren't
// covered by the existing HookCapabilities. Used to produce a
// directive error at authoring time instead of letting the tool fail
// at dispatch with a confusing "HookError: secret %q not granted".
//
// Best-effort parse — only catches the common shape with a string
// literal as the first arg (`gohort.secret("openweather")`,
// `gohort.fetch_via("github", url)`). Dynamic args (variable, f-string,
// dict lookup) slip through silently — they'll surface at dispatch
// where the hook denies the unknown credential.
//
// Returns the zero value when nothing's missing (calls == "" means
// no error to raise).
func findUngrantedCredentialCalls(script string, granted []string) ungrantedCalls {
	grantedSet := map[string]bool{}
	for _, c := range granted {
		grantedSet[c] = true
	}
	var missingCalls []string
	var suggestParts []string
	scan := func(prefix, capKind string) {
		idx := 0
		for {
			pos := strings.Index(script[idx:], prefix)
			if pos < 0 {
				return
			}
			start := idx + pos + len(prefix)
			// Skip whitespace.
			for start < len(script) && (script[start] == ' ' || script[start] == '\t') {
				start++
			}
			if start >= len(script) {
				return
			}
			quote := script[start]
			if quote != '"' && quote != '\'' {
				// Not a string literal — can't extract the name.
				idx = idx + pos + len(prefix)
				continue
			}
			end := strings.IndexByte(script[start+1:], quote)
			if end < 0 {
				return
			}
			name := script[start+1 : start+1+end]
			needed := capKind + ":" + name
			if !grantedSet[needed] {
				missingCalls = append(missingCalls, fmt.Sprintf("gohort.%s(%q)", capKind, name))
				suggestParts = append(suggestParts, fmt.Sprintf("%q", needed))
				grantedSet[needed] = true // dedupe across multiple call sites
			}
			idx = idx + pos + len(prefix)
		}
	}
	scan("gohort.secret(", "secret")
	scan("gohort.fetch_via(", "fetch_via")
	if len(missingCalls) == 0 {
		return ungrantedCalls{}
	}
	return ungrantedCalls{
		calls:   strings.Join(missingCalls, ", "),
		suggest: strings.Join(suggestParts, ", "),
	}
}

// firstShebang returns the shebang line of scriptBody (without the
// leading "#!") when the body starts with one. Empty otherwise.
func firstShebang(scriptBody string) string {
	if !strings.HasPrefix(scriptBody, "#!") {
		return ""
	}
	end := strings.IndexByte(scriptBody, '\n')
	if end < 0 {
		end = len(scriptBody)
	}
	return strings.TrimSpace(scriptBody[2:end])
}

// paramNamesInDefinitionOrder extracts param names from the raw
// `params` argument value while preserving the order the LLM
// specified. parseParamsArg returns a map[string]ToolParam which
// loses order; for inferring positional command_template we want the
// order the LLM listed them in. Uses encoding/json's Decoder Token
// stream to walk an object's keys in insertion order.
//
// Falls back to alphabetical when the input isn't a parseable JSON
// object (the typed-map path takes over via parseParamsArg downstream,
// so this is a best-effort ordering for the auto-inference shortcut).
func paramNamesInDefinitionOrder(v any) ([]string, error) {
	if v == nil {
		return nil, fmt.Errorf("params is nil")
	}
	var raw []byte
	switch s := v.(type) {
	case string:
		raw = []byte(s)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("params is not a JSON object")
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch d := t.(type) {
		case json.Delim:
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		case string:
			if depth == 0 {
				keys = append(keys, d)
			}
		}
	}
	return keys, nil
}
