package temptool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

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

func (t *CreateTempToolTool) Name() string { return "create_temp_tool" }

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
	// And reject a name an existing toolbox action already publishes.
	if err := CheckCatalogNameCollision(sess, name, nil); err != nil {
		return "", err
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
		Category:        strings.TrimSpace(StringArg(args, "category")),
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
		Debug("[temptool] create %q: gathering workspace helpers from %s", name, sess.WorkspaceDir)
		tool.WorkspaceFiles = gatherWorkspaceHelpers(scriptName, scriptBody, sess.WorkspaceDir)
		Debug("[temptool] create %q: gathered %d helper file(s)", name, len(tool.WorkspaceFiles))
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
	saveSessionScoped := func() { persistUnapprovedTool(sess, tool) }

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
	return fmt.Sprintf("Created temp tool %q. It is now in your tool catalog.%s", name, spec), nil
}
