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
	bwrapDetectOnce  sync.Once
	bwrapPath        string
	bwrapWarnedOnce  sync.Once
)

// detectBwrap finds the bwrap binary on PATH once and caches the result.
// Empty path means no sandbox — caller falls back to plain sh -c.
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

	var c *exec.Cmd
	sandbox := false
	if bwrap != "" {
		args := bwrapArgvWithEnv(workspaceDir, command, extraEnv, allowNetwork)
		c = exec.CommandContext(ctx, bwrap, args...)
		sandbox = true
	} else {
		bwrapWarnedOnce.Do(func() {
			Log("[sandbox] WARNING: bwrap not installed — shell tools run with gohort user permissions. Install bubblewrap to enable real sandboxing (apt install bubblewrap / dnf install bubblewrap).")
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
		"--die-with-parent",   // kill child when bwrap dies
		"--new-session",       // detach controlling tty
		// Namespace isolation. PID/UTS/IPC are uncontroversial. Network
		// is governed by the NetworkConnector — when the connector is
		// blocked, --unshare-net is appended below to cut outbound off
		// at the kernel level. When allowed, we keep the host's net
		// namespace so curl / pip / etc. work normally.
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
		"--unshare-cgroup-try", // own cgroup when kernel supports it
		// Filesystem: workspace writable, system dirs read-only,
		// everything else not visible.
		"--bind", workspaceDir, workspaceDir,
		"--chdir", workspaceDir,
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
	}
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
	return []string{
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
		"--",
		interpreter, "-c", script,
	}
}

// sandboxEnv returns the environment for sandboxed commands. PATH must
// survive so common utilities resolve; secrets the gohort process holds
// must NOT survive — env vars like API keys, AWS creds, etc. would
// otherwise leak straight into LLM-controlled shell scope.
func sandboxEnv() []string {
	// Minimal allowlist. Anything not in this list is dropped.
	keep := map[string]bool{
		"PATH": true,
		"LANG": true,
		"LC_ALL": true,
		"LC_CTYPE": true,
		"TERM": true,
		"HOME": true,
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
