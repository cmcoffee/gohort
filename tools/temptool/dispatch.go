package temptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

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
	// Path scopes, BEFORE substitution. A parameter declaring
	// path_scope: "files:<store>" has to be proved to land inside that
	// registered root, and the value substituted is the absolute path it
	// resolved to.
	//
	// This ran nowhere on this path until v0.6.241 — only servitor's
	// appliance dispatch checked it — so the declaration was decoration
	// on a value that had been shell-quoted and nothing more. Quoting
	// stops a value contributing SYNTAX and says nothing about it
	// pointing somewhere else, which core/path_scope.go opens by warning
	// about: "../../var/lib/something" is a perfectly well-formed single
	// argument.
	scopedArgs, scopedPaths, serr := ResolveScopedArgs(sess.Username, sess.AgentID, tt.Params, args)
	if serr != nil {
		// Refuse. A scope that fails open is not a scope, and the model
		// gets a message it can act on: the root it may name, and what
		// it asked for.
		Log("[temptool] %q refused: %v", tt.Name, serr)
		return "", fmt.Errorf("%s: %w", tt.Name, serr)
	}
	args = scopedArgs
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
	ctx = sess.ContextWithSandboxCaller(ctx)
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
	// Scoped paths are bound READ-ONLY into the sandbox, at the same path
	// they have outside. Without this the check passes and the script
	// still cannot open the file, which reads as the check being wrong.
	// RunSandboxedShellScoped REFUSES when the host has no sandbox rather
	// than running with the daemon's own view of the filesystem, where
	// "this path only" would not apply.
	res := RunSandboxedShellScoped(ctx, cmd, workspaceDir, envArgs, scopedPaths)
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
