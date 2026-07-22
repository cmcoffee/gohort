// Sandboxed shell execution. Used by run_local and temp tools to run
// LLM-issued shell commands without giving the LLM the gohort process's
// full filesystem and resource access.
//
// Mechanism: when the `bwrap` (bubblewrap) binary is available, the
// command runs inside a Linux mount namespace that bind-mounts only the
// workspace as writable plus a minimal read-only set of system dirs
// needed for common utilities to function (`/usr`, `/lib`, `/etc/ssl`,
// etc.). Network is allowed by default so curl / API access keeps
// working. Outside the bind mounts the sandbox sees nothing — `rm -rf
// ~`, `cat ~/.ssh/...`, or anything else outside the workspace silently
// hits a "no such file" wall.
//
// Fallback: if bwrap isn't installed, exec falls back to plain `sh -c`
// with `cwd = workspace` (the original behavior). A warning is logged
// at first use so the operator knows the sandbox is degraded. Tools
// keep working — they just aren't OS-sandboxed.

package core

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	bwrapDetectOnce sync.Once
	bwrapPath       string
	bwrapWarnedOnce sync.Once
)

// sandboxWaitDelay bounds how long Run() will keep blocking AFTER ctx is
// cancelled, waiting on stdout/stderr pipes the child left open. Once it
// elapses the pipes are force-closed and Run() returns. Guards the
// fallback (non-bwrap) path: a command that backgrounds a process still
// holding the pipe ("sleep 300 &") would otherwise wedge Run() past the
// deadline even though the direct child was killed. The bwrap path can't
// hit this (--die-with-parent reaps the whole tree), so this is harmless
// belt-and-suspenders there.
const sandboxWaitDelay = 5 * time.Second

// detectBwrap finds the bwrap binary on PATH once and caches the result.
// Empty path means no sandbox — caller falls back to plain sh -c.
// sandboxRequired reports whether the operator has demanded fail-closed
// sandboxing via the GOHORT_SANDBOX_REQUIRED env var. When set and bubblewrap
// is unavailable, shell/script execution is refused rather than silently
// dropping to an unsandboxed subshell running at the service account's full
// privilege. Default (unset) preserves the historical fail-open behavior so
// hosts without bubblewrap keep working; a hardened deployment sets it.
func sandboxRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOHORT_SANDBOX_REQUIRED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// errSandboxUnavailable is returned when the sandbox is required but bwrap is
// missing — the fail-closed alternative to running unsandboxed.
const errSandboxUnavailable = Error("sandbox required (GOHORT_SANDBOX_REQUIRED) but bubblewrap (bwrap) is not installed — refusing to run the tool unsandboxed; install bubblewrap or clear GOHORT_SANDBOX_REQUIRED")

func detectBwrap() string {
	bwrapDetectOnce.Do(func() {
		if p, err := exec.LookPath("bwrap"); err == nil {
			bwrapPath = p
			Debug("[sandbox] bwrap detected at %s — run_local and temp tools will be OS-sandboxed", p)
		} else {
			Debug("[sandbox] bwrap not found on PATH — run_local will fall back to plain sh -c (workspace cwd only, no namespace isolation)")
		}
	})
	return bwrapPath
}

// SandboxedShellResult is what RunSandboxedShell returns. Combined
// stdout+stderr in Output, exec error (if any) in Err. Sandbox=true
// means bwrap actually wrapped the command.
type SandboxedShellResult struct {
	Output   string
	Err      error
	Sandbox  bool // true when the command ran inside bwrap
	TimedOut bool // true when ctx.Err() == context.DeadlineExceeded
}

// RunSandboxedShell runs `sh -c command` with the strongest sandbox
// available on this host, scoped to workspaceDir as the only writable
// path. Stdout and stderr are combined and returned together.
//
// workspaceDir must be an absolute path that already exists. The caller
// is responsible for path validation; this function trusts the value.
//
// Network access is governed by the NetworkConnector on ctx: connector
// allowed → sandbox keeps the host network namespace; connector blocked
// → --unshare-net cuts the sandbox off from outbound. Missing connector
// in ctx defaults to allowed (back-compat for callers that don't yet
// propagate one).
func RunSandboxedShell(ctx context.Context, command, workspaceDir string) SandboxedShellResult {
	return RunSandboxedShellWithEnv(ctx, command, workspaceDir, nil)
}

// RunSandboxedShellWithHook is the iterate-and-test variant: starts a
// SandboxHook with the given capabilities, threads GOHORT_HOOK_PATH
// into the sandbox env so `from gohort import fetch` works exactly
// the way it would inside a registered shell-mode tool, runs the
// command, then closes the hook.
//
// Solves an asymmetry that bit Builder repeatedly: a registered
// shell tool's sandbox got a hook (via temptool dispatch), but the
// iterate-and-test path (workspace action="run") didn't. The same
// script that worked when dispatched as a tool raised
// `HookError: GOHORT_HOOK_PATH not set` when iterated via shell,
// teaching Builder that "fetch doesn't work" and sending it down a
// wrong-direction rewrite spiral. Wiring the hook here equalizes
// the two contexts — what works in iterate-and-test works in
// dispatch and vice versa.
//
// Pass capabilities=["fetch", "log", "browse_page"] for the typical
// iterate-and-test default. Don't auto-grant secret:* or fetch_via:*
// — those should still surface as missing during iteration so the
// author registers credentials properly before shipping the tool.
//
// sess is forwarded to the hook (needed for fetch_via session-aware
// dispatch). nil sess is fine — fetch/log/browse_page don't need it.
//
// Returns the same shape as RunSandboxedShell. On hook-start failure
// (rare — would mean the workspace dir is unwritable), falls back to
// the no-hook path so the run still completes; the script will then
// see the HookError on attempt and the operator gets a Log line.
func RunSandboxedShellWithHook(ctx context.Context, command, workspaceDir string, sess *ToolSession, capabilities []string) SandboxedShellResult {
	return RunSandboxedShellWithHookEnv(ctx, command, workspaceDir, sess, capabilities, nil)
}

// RunSandboxedShellWithHookEnv is RunSandboxedShellWithHook plus caller-supplied
// env: extraEnv is merged with the gohort hook path and exposed to the script
// (shell $VAR / Python os.environ). The hook path always wins over a colliding
// caller key. Used by workspace(action="run", env={...}) so a manual debug run
// can pass variables — the same way a registered shell tool receives its params.
func RunSandboxedShellWithHookEnv(ctx context.Context, command, workspaceDir string, sess *ToolSession, capabilities []string, extraEnv map[string]string) SandboxedShellResult {
	merge := func(hookPath string) map[string]string {
		env := map[string]string{}
		for k, v := range extraEnv {
			env[k] = v
		}
		if hookPath != "" {
			env["GOHORT_HOOK_PATH"] = hookPath // hook path is authoritative
		}
		return env
	}
	if len(capabilities) == 0 || workspaceDir == "" {
		if len(extraEnv) == 0 {
			return RunSandboxedShell(ctx, command, workspaceDir)
		}
		return RunSandboxedShellWithEnv(ctx, command, workspaceDir, merge(""))
	}
	hook, err := NewSandboxHook(workspaceDir, capabilities, sess)
	if err != nil || hook == nil {
		if err != nil {
			Log("[sandbox] hook init failed for iterate-and-test run (%v) — running without hook; gohort.fetch in this script will raise HookError", err)
		}
		if len(extraEnv) == 0 {
			return RunSandboxedShell(ctx, command, workspaceDir)
		}
		return RunSandboxedShellWithEnv(ctx, command, workspaceDir, merge(""))
	}
	defer hook.Close()
	return RunSandboxedShellWithEnv(ctx, command, workspaceDir, merge(hook.SocketPath))
}

// RunSandboxedShellWithEnv is the env-extended variant: extraEnv maps
// key→value are appended to the sandbox's standard env (sandboxEnv())
// so shell commands can reference them as `$key`, and Python scripts
// can read them via `os.environ.get("key")`. Used by temp tools to
// pass declared args without the author having to remember the
// {placeholder} substitution syntax — `python3 ./tool.py $first_name`
// just works.
//
// Keys are NOT prefixed: tools commonly use natural names (first_name,
// url, etc.) and adding a prefix would surprise authors. Collision
// with sandboxEnv's allowlist (PATH/HOME/LANG/etc.) is theoretically
// possible but vanishingly rare — a tool arg named "PATH" would
// overwrite, and that's the author's problem.
//
// Network access is governed by the NetworkConnector on ctx: when the
// connector is blocked, --unshare-net is appended and outbound calls
// from the command silently fail. Missing connector = network allowed
// (back-compat for callers not yet plumbing one through).
func RunSandboxedShellWithEnv(ctx context.Context, command, workspaceDir string, extraEnv map[string]string) SandboxedShellResult {
	bwrap := detectBwrap()
	allowNetwork := NetworkAllowedFromContext(ctx)

	// PYTHONPATH := SandboxGohortLibMountPath so `from gohort import
	// fetch` resolves against the bind-mounted gohort helper package
	// (which lives OUTSIDE the workspace — see EnsureGohortLibDir).
	// Without this, a script at any depth under workspaceDir can't
	// find the helper because the workspace doesn't contain it.
	// Prepend rather than clobber so a caller-supplied PYTHONPATH
	// also stays in the search list.
	if extraEnv == nil {
		extraEnv = map[string]string{}
	}
	// PYTHONPATH must include both the gohort helper mount (so `from
	// gohort import fetch` resolves) and the managed python-deps mount
	// (so `import openpyxl` and friends resolve). Prepend rather than
	// clobber so a caller-supplied PYTHONPATH also stays searchable.
	extraEnv["PYTHONPATH"] = PrependPythonPath(extraEnv["PYTHONPATH"],
		SandboxGohortLibMountPath, SandboxPyDepsMountPath)

	var c *exec.Cmd
	sandbox := false
	if bwrap != "" {
		args := bwrapArgvWithEnv(workspaceDir, command, extraEnv, allowNetwork)
		c = exec.CommandContext(ctx, bwrap, args...)
		sandbox = true
	} else if sandboxRequired() {
		return SandboxedShellResult{Err: errSandboxUnavailable}
	} else {
		bwrapWarnedOnce.Do(func() {
			Log("[sandbox] WARNING: bwrap not installed — shell tools run with gohort user permissions. Install bubblewrap to enable real sandboxing (apt install bubblewrap / dnf install bubblewrap). Set GOHORT_SANDBOX_REQUIRED=1 to refuse unsandboxed execution instead.")
		})
		c = exec.CommandContext(ctx, "sh", "-c", command)
		c.Dir = workspaceDir
	}
	env := sandboxEnv()
	// Append extras AFTER sandboxEnv so a tool arg "PATH" (rare but
	// possible) wins over the inherited PATH inside the subshell.
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	c.Env = env

	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	c.WaitDelay = sandboxWaitDelay
	// Spawn/exit breadcrumbs: when a dispatch hangs, the gap between
	// these two lines tells us exec is wedged versus the wrapper code
	// upstream. argv-count distinguishes "tiny argv → exec failed
	// early" from "fat argv → bind-mount setup stuck".
	Debug("[sandbox] spawn: bwrap=%q argv=%d allowNet=%v workspace=%s", bwrap, len(c.Args), allowNetwork, workspaceDir)
	t0 := time.Now()
	err := c.Run()
	dur := time.Since(t0)
	timedOut := ctx.Err() == context.DeadlineExceeded
	Debug("[sandbox] exit: err=%v timedOut=%v bytes=%d dur=%s", err, timedOut, buf.Len(), dur)
	return SandboxedShellResult{
		Output:   buf.String(),
		Err:      err,
		Sandbox:  sandbox,
		TimedOut: timedOut,
	}
}

// bwrapArgv builds the bwrap invocation. Workspace is bind-mounted as
// the only writable path; system dirs are read-only; /tmp is a fresh
// tmpfs per invocation; namespaces are unshared so the command can't
// see the host's processes, IPC, or hostname.
// bwrapArgvWithEnv is the env-extended variant. Splices --setenv KEY
// VALUE triples into the bwrap argv BEFORE the "--" separator. Past
// the separator, bwrap treats every token as part of the command to
// exec — so inserting --setenv after "--" gets bwrap to try to exec
// "--setenv" as a binary ("no such file or directory"). allowNetwork
// controls whether --unshare-net is appended (false = no network).
func bwrapArgvWithEnv(workspaceDir, shellCmd string, extraEnv map[string]string, allowNetwork bool) []string {
	args := bwrapArgv(workspaceDir, shellCmd, allowNetwork)
	if len(extraEnv) == 0 {
		return args
	}
	// Find the "--" separator. Everything before it is bwrap flags;
	// everything after is the command. --setenv must land before "--".
	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 {
		// Defensive: bwrapArgv lost the separator somehow. Return
		// the unmodified argv rather than silently corrupting the
		// invocation.
		return args
	}
	// Copy tail into its own slice BEFORE mutating head — both
	// args[:sepIdx] and args[sepIdx:] share the same backing array,
	// so `append(head, ...)` would otherwise overwrite tail's
	// "--", "sh", "-c", shellCmd entries in place. Symptom of the
	// aliasing bug was bwrap trying to execvp the whole shell
	// command string directly (because "sh" and "-c" had been
	// silently clobbered by the setenv tokens).
	tail := append([]string{}, args[sepIdx:]...)
	out := make([]string, 0, sepIdx+len(extraEnv)*3+len(tail))
	out = append(out, args[:sepIdx]...)
	for k, v := range extraEnv {
		out = append(out, "--setenv", k, v)
	}
	out = append(out, tail...)
	return out
}

func bwrapArgv(workspaceDir, shellCmd string, allowNetwork bool) []string {
	args := []string{
		// Lifecycle.
		"--die-with-parent", // kill child when bwrap dies
		"--new-session",     // detach controlling tty
		// Namespace isolation. PID/UTS/IPC are uncontroversial. Network
		// is governed by the NetworkConnector — when the connector is
		// blocked, --unshare-net is appended below to cut outbound off
		// at the kernel level. When allowed, we keep the host's net
		// namespace so curl / pip / etc. work normally.
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
		"--unshare-cgroup-try", // own cgroup when kernel supports it
	}
	// Network namespace isolation: when allowNetwork=false (privacy
	// mode session OR tool didn't declare raw_network=true), --unshare-net
	// creates a brand-new network namespace with no interfaces, blocking
	// every outbound call at the kernel. When true, we keep the host's
	// net namespace so curl / urllib / etc. work normally. This MUST
	// appear before the "--" separator so bwrap interprets it as a flag
	// rather than part of the command to exec.
	if !allowNetwork {
		args = append(args, "--unshare-net")
	}
	args = append(args,
		// Filesystem: workspace writable, system dirs read-only,
		// everything else not visible.
		"--bind", workspaceDir, workspaceDir,
		"--chdir", workspaceDir,
	)
	// Bind the host-side gohort helper library RO into the sandbox at
	// a fixed mount point (SandboxGohortLibMountPath). PYTHONPATH is
	// set to this path in the env so `from gohort import fetch`
	// resolves regardless of the running script's location. The mount
	// is RO: no shell escape inside the sandbox can modify the helper
	// source, and the LLM can't see it from the workspace at all.
	// Best-effort: if EnsureGohortLibDir failed (e.g., WorkspacesDir
	// unset), skip the bind — the script's gohort import will fail
	// loudly with ModuleNotFoundError, which is the right shape.
	if libDir := EnsureGohortLibDir(); libDir != "" {
		args = append(args, "--ro-bind", libDir, SandboxGohortLibMountPath)
	}
	// Managed python deps (openpyxl, python-docx, ...) live in a host
	// dir populated by EnsurePyDeps; bind RO so `import openpyxl`
	// resolves. PYTHONPATH is extended to include SandboxPyDepsMountPath
	// by the caller (RunSandboxedShellWithEnv).
	if pyDir := EnsurePyDepsDir(); pyDir != "" {
		args = append(args, "--ro-bind", pyDir, SandboxPyDepsMountPath)
	}
	args = append(args,
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib32", "/lib32",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind-try", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		"--ro-bind-try", "/etc/pki", "/etc/pki",
		// /etc/alternatives is RHEL/Rocky/Fedora's symlink-routing dir
		// that binaries like python3, java, vim, podman, editor route
		// through. Without this bind, a `python3` symlink at
		// /usr/bin/python3 points at /etc/alternatives/python3 which
		// dangles inside the namespace, and the sandbox sees "command
		// not found" for everything that uses the alternatives system.
		// On Debian/Ubuntu this is /etc/alternatives too — same fix
		// works across distros.
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		// Synthesized filesystems.
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--",
		"sh", "-c", shellCmd,
	)
	return args
}

// RunSandboxedShellPipe runs `sh -c command` in a tight sandbox with
// stdinData piped to the command's stdin and combined stdout+stderr
// returned. Tighter than RunSandboxedShell: no writable bind, no
// network, /tmp tmpfs only. Designed for response-pipe use cases where
// the command (jq, awk, sed, grep, etc.) transforms data in-flight
// without needing filesystem or network access. Falls back to plain
// `sh -c` (cwd = /tmp) when bwrap isn't installed.
func RunSandboxedShellPipe(ctx context.Context, command, stdinData string) SandboxedShellResult {
	bwrap := detectBwrap()

	var c *exec.Cmd
	sandbox := false
	if bwrap != "" {
		args := bwrapPipeArgv(command)
		c = exec.CommandContext(ctx, bwrap, args...)
		sandbox = true
	} else if sandboxRequired() {
		return SandboxedShellResult{Err: errSandboxUnavailable}
	} else {
		bwrapWarnedOnce.Do(func() {
			Log("[sandbox] WARNING: bwrap not installed — response pipes run with gohort user permissions. Install bubblewrap to enable real sandboxing.")
		})
		c = exec.CommandContext(ctx, "sh", "-c", command)
		c.Dir = "/tmp"
	}
	c.Env = sandboxEnv()
	c.Stdin = strings.NewReader(stdinData)

	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	c.WaitDelay = sandboxWaitDelay
	err := c.Run()
	timedOut := ctx.Err() == context.DeadlineExceeded
	return SandboxedShellResult{
		Output:   buf.String(),
		Err:      err,
		Sandbox:  sandbox,
		TimedOut: timedOut,
	}
}

// bwrapPipeArgv builds bwrap args for a response-pipe invocation.
// Same shape as bwrapScriptArgv (no writable bind, no network) but
// runs `sh -c command` so the LLM can chain pipes (jq | head, etc.).
func bwrapPipeArgv(shellCmd string) []string {
	return []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
		"--unshare-cgroup-try",
		"--unshare-net",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib32", "/lib32",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--chdir", "/tmp",
		"--",
		"sh", "-c", shellCmd,
	}
}

// SandboxedScriptResult is what RunSandboxedScript returns.
type SandboxedScriptResult struct {
	Stdout   string
	Stderr   string
	Err      error
	Sandbox  bool
	TimedOut bool
}

// RunSandboxedScript runs an interpreter (e.g. "python3", "node") with
// the given script body via `-c`, piping stdinData to its stdin and
// capturing stdout / stderr separately. Stricter than RunSandboxedShell:
//
//   - No writable bind: there's no workspace; the sandbox's only
//     writable surface is the per-invocation /tmp tmpfs.
//   - No network: --unshare-net cuts the script off from outbound,
//     since evaluator scripts already receive the data they need.
//   - Same read-only system binds (libs, /etc/alternatives, etc.) so
//     standard interpreters and stdlib work normally.
//
// Designed for watcher evaluator scripts: deterministic, fast,
// stdin-in / stdout-out. Falls back to plain exec when bwrap isn't
// installed (logs a warning once).
func RunSandboxedScript(ctx context.Context, interpreter, script, stdinData string) SandboxedScriptResult {
	bwrap := detectBwrap()

	var c *exec.Cmd
	sandbox := false
	if bwrap != "" {
		args := bwrapScriptArgv(interpreter, script)
		c = exec.CommandContext(ctx, bwrap, args...)
		sandbox = true
	} else if sandboxRequired() {
		return SandboxedScriptResult{Err: errSandboxUnavailable}
	} else {
		bwrapWarnedOnce.Do(func() {
			Log("[sandbox] WARNING: bwrap not installed — evaluator scripts run with gohort user permissions. Install bubblewrap to enable real sandboxing.")
		})
		c = exec.CommandContext(ctx, interpreter, "-c", script)
	}
	c.Env = sandboxEnv()
	c.Stdin = strings.NewReader(stdinData)

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	c.WaitDelay = sandboxWaitDelay
	err := c.Run()
	timedOut := ctx.Err() == context.DeadlineExceeded
	return SandboxedScriptResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      err,
		Sandbox:  sandbox,
		TimedOut: timedOut,
	}
}

// bwrapScriptArgv builds bwrap args for a script invocation. Tighter
// than bwrapArgv (no writable bind, no network).
func bwrapScriptArgv(interpreter, script string) []string {
	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
		"--unshare-cgroup-try",
		"--unshare-net", // evaluator scripts don't need outbound; tighter security
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib32", "/lib32",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--chdir", "/tmp",
	}
	// Managed python deps: bind RO and point PYTHONPATH at the mount so
	// generator scripts (xlsx/docx/pptx) can import their libraries even
	// though this sandbox keeps --unshare-net and a read-only /usr. The
	// packages were provisioned host-side by EnsurePyDeps.
	if pyDir := EnsurePyDepsDir(); pyDir != "" {
		args = append(args,
			"--ro-bind", pyDir, SandboxPyDepsMountPath,
			"--setenv", "PYTHONPATH", SandboxPyDepsMountPath,
		)
	}
	args = append(args, "--", interpreter, "-c", script)
	return args
}

// sandboxEnv returns the environment for sandboxed commands. PATH must
// survive so common utilities resolve; secrets the gohort process holds
// must NOT survive — env vars like API keys, AWS creds, etc. would
// otherwise leak straight into LLM-controlled shell scope.
func sandboxEnv() []string {
	// Minimal allowlist. Anything not in this list is dropped.
	keep := map[string]bool{
		"PATH":     true,
		"LANG":     true,
		"LC_ALL":   true,
		"LC_CTYPE": true,
		"TERM":     true,
		"HOME":     true,
	}
	env := []string{}
	for _, kv := range os.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				if keep[kv[:i]] {
					env = append(env, kv)
				}
				break
			}
		}
	}
	// Always set a sensible PATH if the host didn't have one.
	hasPath := false
	for _, kv := range env {
		if len(kv) >= 5 && kv[:5] == "PATH=" {
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}
