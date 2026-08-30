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

package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/deps"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/snugforge/nfo"
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
// sandboxPythonPath builds the PYTHONPATH for a run, given the bwrap binary
// that will (or will not) carry it.
//
// A remapping sandbox reaches the helpers through bind mounts at fixed paths.
// Without remapping there are no mounts, so the only paths that resolve are the
// host directories the mounts would have pointed at. Naming the mount paths in
// both cases is the bug this exists to prevent: on such a host it puts two
// directories on PYTHONPATH that do not exist, and the first line of every
// hook-using script dies with ModuleNotFoundError.
//
// Passing remaps in rather than asking activeSandbox() keeps the decision
// testable — the whole point is what happens on a machine whose sandbox does
// not remap paths, which is not the machine the tests run on.
func sandboxPythonPath(remaps bool, existing string) string {
	libPath, depsPath := GohortLibMountPath, deps.SandboxPyDepsMountPath
	if !remaps {
		// Ensure* both deploys the helper and reports where it landed. On this
		// path it is also the only thing that deploys it at all: it used to run
		// solely as a side effect of building the bwrap argv.
		libPath, depsPath = gohortLibDir(), deps.EnsurePyDepsDir()
	}
	// Prepend rather than clobber so a caller-supplied PYTHONPATH stays
	// searchable; empty entries are dropped.
	return deps.PrependPythonPath(existing, libPath, depsPath)
}

// sandboxShimBinDir returns the directory holding the fetch_url / fetch_via /
// browse_page shims for a run, or "" when there is none to offer.
//
// Under a remapping sandbox that is the mount point; otherwise the host dir the
// mount would have pointed at. Same shape as sandboxPythonPath, and
// deliberately so: these two are the entire bridge between a sandboxed script
// and the hook, and they went wrong the same way for the same reason.
func sandboxShimBinDir(remaps bool) string {
	if remaps {
		return GohortBinMountPath
	}
	libDir := gohortLibDir()
	if libDir == "" {
		return ""
	}
	return filepath.Join(libDir, "bin")
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
// dispatch). nil sess is fine — fetch/log/browse_page don't need it. It is typed
// `any` because this package hands it straight to the broker and never reads it;
// the broker knows what a session is, and keeping that knowledge out of here is
// what lets confinement and credential enforcement live apart.
//
// Returns the same shape as RunSandboxedShell. On hook-start failure
// (rare — would mean the workspace dir is unwritable), falls back to
// the no-hook path so the run still completes; the script will then
// see the HookError on attempt and the operator gets a Log line.
func RunSandboxedShellWithHook(ctx context.Context, command, workspaceDir string, sess any, capabilities []string) SandboxedShellResult {
	return RunSandboxedShellWithHookEnv(ctx, command, workspaceDir, sess, capabilities, nil)
}

// RunSandboxedShellWithHookEnv is RunSandboxedShellWithHook plus caller-supplied
// env: extraEnv is merged with the gohort hook path and exposed to the script
// (shell $VAR / Python os.environ). The hook path always wins over a colliding
// caller key. Used by workspace(action="run", env={...}) so a manual debug run
// can pass variables — the same way a registered shell tool receives its params.
func RunSandboxedShellWithHookEnv(ctx context.Context, command, workspaceDir string, sess any, capabilities []string, extraEnv map[string]string) SandboxedShellResult {
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
	hook, err := newHook(workspaceDir, capabilities, sess)
	if err != nil || hook == nil {
		if err != nil {
			nfo.Log("[sandbox] hook init failed for iterate-and-test run (%v) — running without hook; gohort.fetch in this script will raise HookError", err)
		}
		if len(extraEnv) == 0 {
			return RunSandboxedShell(ctx, command, workspaceDir)
		}
		return RunSandboxedShellWithEnv(ctx, command, workspaceDir, merge(""))
	}
	defer hook.Close()
	return RunSandboxedShellWithEnv(ctx, command, workspaceDir, merge(hook.Path()))
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
	return runSandboxedShellWithBinds(ctx, command, workspaceDir, extraEnv, nil)
}

// SandboxedCmd is a confined command that has been BUILT but not started.
//
// It exists for a caller whose process OUTLIVES the call that creates it — a
// persistent shell holding a psql or ssh session across many LLM turns. Such a
// caller cannot use RunSandboxedShell, which owns the command start to finish
// and hands back only the bytes it printed; it needs the *exec.Cmd itself so it
// can attach its own pipes and keep the process alive.
//
// What it must NOT do is build that command itself, which is what
// tools/temptool did: its own exec.LookPath("bwrap"), its own copy of the argv,
// and its own idea of what to do when there was no bwrap. See
// buildSandboxedShellCmd for what that cost.
//
// Backend and Confined are reported rather than left to be re-derived, because
// the caller that logs "running unconfined" must be naming the same decision
// this package made, not a second guess at it.
type SandboxedCmd struct {
	Cmd      *exec.Cmd
	Backend  string
	Confined bool
	// Remaps is the backend's remapsPaths(), carried out so a caller that
	// resolves its own paths asks the question rather than assuming bwrap.
	Remaps bool
}

// buildSandboxedShellCmd assembles one shell run: the PYTHONPATH the helper
// package needs, the fail-closed policy gate, the backend's argv, and the
// scrubbed environment. It does not start anything.
//
// This is a single copy on purpose. Persistent shells used to hand-roll a
// parallel bwrap argv (persistentBwrapArgv) behind their own PATH lookup, and
// the duplicate drifted exactly where it mattered: it never consulted
// bypassPolicy, so a host with no bwrap logged a warning and dropped a
// long-lived LLM-driven shell to `sh -c` at the service account's full
// privilege. That is the precise state sandboxRequired refuses for every
// one-shot command, reached by a caller that had simply never been told. A
// second copy of this logic is a second copy of that hole, so both shapes call
// here.
func buildSandboxedShellCmd(ctx context.Context, command, workspaceDir string, extraEnv map[string]string, readOnly []string) (SandboxedCmd, error) {
	sb := activeSandbox()
	allowNetwork := netgate.NetworkAllowedFromContext(ctx)

	// PYTHONPATH := GohortLibMountPath so `from gohort import
	// fetch` resolves against the bind-mounted gohort helper package
	// (which lives OUTSIDE the workspace — see EnsureGohortLibDir).
	// Without this, a script at any depth under workspaceDir can't
	// find the helper because the workspace doesn't contain it.
	// Prepend rather than clobber so a caller-supplied PYTHONPATH
	// also stays in the search list.
	if extraEnv == nil {
		extraEnv = map[string]string{}
	}
	// PYTHONPATH must include both the gohort helper (so `from gohort import
	// fetch` resolves) and the managed python-deps (so `import openpyxl` and
	// friends resolve). WHERE those are depends on whether we are about to run
	// under bwrap.
	//
	// The mount paths are real only INSIDE the sandbox — they are where bwrap
	// binds the host directories. Without bwrap nothing is mounted anywhere and
	// the only real location is the host directory itself, so pointing
	// PYTHONPATH at /opt/gohort-lib there names a path that does not exist.
	//
	// This is every macOS deployment, where bwrap does not exist at all, plus
	// any Linux host without bubblewrap installed. The symptom is that
	// `from gohort import fetch_url` raises ModuleNotFoundError on the FIRST
	// line of every hook-using script, while the hook socket itself is present
	// and working — so it reads as a broken install rather than a wrong path,
	// and the obvious next move (hunting for the module) fails too: the helper
	// really is on disk, just somewhere PYTHONPATH never mentions.
	//
	// Calling Ensure* here also makes the no-bwrap path deploy the helper at
	// all. It used to be written only as a side effect of building the bwrap
	// argv, so on a host with no bwrap the package was never even created.
	extraEnv["PYTHONPATH"] = sandboxPythonPath(sb.remapsPaths(), extraEnv["PYTHONPATH"])

	if !sb.confines() {
		if sandboxRequired(ctx) {
			warnRefusing("shell tools")
			return SandboxedCmd{}, sandboxUnavailableErr()
		}
		warnUnsandboxed("shell tools")
	}
	c := sb.build(ctx, sandboxRun{
		Kind: sandboxShellRun, Command: command, WorkspaceDir: workspaceDir,
		Env: extraEnv, AllowNetwork: allowNetwork, ReadOnly: readOnly,
	})
	env := sandboxEnv(sb.remapsPaths())
	// Append extras AFTER sandboxEnv so a tool arg "PATH" (rare but
	// possible) wins over the inherited PATH inside the subshell.
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	c.Env = env
	// Spawn breadcrumb: when a dispatch hangs, the gap between this and the
	// caller's exit line tells us exec is wedged versus the wrapper code
	// upstream. argv-count distinguishes "tiny argv → exec failed early" from
	// "fat argv → bind-mount setup stuck".
	nfo.Debug("[sandbox] spawn: backend=%s argv=%d allowNet=%v workspace=%s", sb.name(), len(c.Args), allowNetwork, workspaceDir)
	return SandboxedCmd{Cmd: c, Backend: sb.name(), Confined: sb.confines(), Remaps: sb.remapsPaths()}, nil
}

// NewSandboxedShellCmd builds a confined shell command and hands it back
// unstarted, for a caller that owns the process lifetime itself.
//
// The refusal is returned as an error rather than signalled by a nil Cmd: a
// caller that forgets to check gets a nil-pointer panic at Start rather than an
// unconfined shell, which is the direction that mistake should fall.
//
// Scoped read-only binds are deliberately NOT offered here. They are a promise
// about a path (RunSandboxedShellScoped), and a long-lived shell outlives the
// resolution that proved it.
func NewSandboxedShellCmd(ctx context.Context, command, workspaceDir string, extraEnv map[string]string) (SandboxedCmd, error) {
	return buildSandboxedShellCmd(ctx, command, workspaceDir, extraEnv, nil)
}

// runSandboxedShellWithBinds is the body both one-shot variants share. readOnly
// is empty for every caller that does not use a path scope, which is all of
// them but one.
func runSandboxedShellWithBinds(ctx context.Context, command, workspaceDir string, extraEnv map[string]string, readOnly []string) SandboxedShellResult {
	built, err := buildSandboxedShellCmd(ctx, command, workspaceDir, extraEnv, readOnly)
	if err != nil {
		return SandboxedShellResult{Err: err}
	}
	c := built.Cmd

	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	c.WaitDelay = sandboxWaitDelay
	t0 := time.Now()
	runErr := c.Run()
	dur := time.Since(t0)
	timedOut := ctx.Err() == context.DeadlineExceeded
	nfo.Debug("[sandbox] exit: err=%v timedOut=%v bytes=%d dur=%s", runErr, timedOut, buf.Len(), dur)
	return SandboxedShellResult{
		Output:   explainMissingGohortModule(buf.String(), built.Remaps),
		Err:      runErr,
		Sandbox:  built.Confined,
		TimedOut: timedOut,
	}
}

// explainMissingGohortModule appends the real cause when a script died
// because the helper package was not there.
//
// ModuleNotFoundError names the script's IMPORT, never the deployment
// that failed, and the model reading it cannot tell those apart. What it
// does instead is report an unspecified "environmental fault" and route
// around the tool — which is the expensive half: an investigation that
// stops one step short, or worse, an answer assembled from guesses
// because one door was shut for a reason nobody could state.
//
// The bwrap argv already calls this outcome "the right shape" when it
// skips the bind mount. It is the right shape for a human reading a
// stack trace and the wrong one for the only reader it actually has.
func explainMissingGohortModule(out string, remaps bool) string {
	if !strings.Contains(out, "No module named 'gohort'") &&
		!strings.Contains(out, "No module named \"gohort\"") {
		return out
	}
	libDir := gohortLibDir()
	note := "\n[gohort] The `gohort` helper package could not be deployed on this host, so it is " +
		"not present in the sandbox. This is a DEPLOYMENT fault, not a problem with the arguments " +
		"you passed, and no retry or different argument will get around it — say so plainly and do " +
		"not work around it by guessing at what the tool would have returned. "
	if libDir == "" {
		note += "Nothing was written: the server log carries the reason under [hook/helpers]."
	} else {
		note += "The package is on disk at " + libDir + "/gohort/__init__.py"
		if remaps {
			note += " and should be mounted at " + GohortLibMountPath +
				"; it is not, so the bind mount is the thing to check."
		} else {
			note += "; this host does not remap paths, so PYTHONPATH should name that directory directly."
		}
	}
	return out + note + "\n"
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
	out := make([]string, 0, sepIdx+len(extraEnv)*3+len(tail)+3)
	out = append(out, args[:sepIdx]...)
	// The hook socket no longer lives in the workspace — a per-agent
	// workspace path is long enough to blow the 107-byte unix socket
	// limit, so it binds under a short host dir instead (see
	// hookSocketPath). Which means the sandbox can no longer reach it
	// for free off the workspace mount, and it needs its own bind.
	//
	// The FILE, not its directory: a sandboxed script gets its own
	// socket and cannot list anyone else's. Same path inside as out, so
	// GOHORT_HOOK_PATH is correct on both sides with no translation.
	if p := extraEnv["GOHORT_HOOK_PATH"]; p != "" && !withinDir(p, workspaceDir) {
		out = append(out, "--bind", p, p)
	}
	for k, v := range extraEnv {
		out = append(out, "--setenv", k, v)
	}
	out = append(out, tail...)
	return out
}

// withinDir reports whether path sits under dir, so a socket that IS in
// the workspace is not bind-mounted twice — bwrap would reject the
// second mount over an existing one, taking down every hook-using tool
// on deployments where the old path still fits.
func withinDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	// a fixed mount point (GohortLibMountPath). PYTHONPATH is
	// set to this path in the env so `from gohort import fetch`
	// resolves regardless of the running script's location. The mount
	// is RO: no shell escape inside the sandbox can modify the helper
	// source, and the LLM can't see it from the workspace at all.
	// Best-effort: if EnsureGohortLibDir failed (e.g., WorkspacesDir
	// unset), skip the bind — the script's gohort import will fail
	// loudly with ModuleNotFoundError, which is the right shape.
	if libDir := gohortLibDir(); libDir != "" {
		args = append(args, "--ro-bind", libDir, GohortLibMountPath)
	}
	// Managed python deps (openpyxl, python-docx, ...) live in a host
	// dir populated by EnsurePyDeps; bind RO so `import openpyxl`
	// resolves. PYTHONPATH is extended to include deps.SandboxPyDepsMountPath
	// by the caller (RunSandboxedShellWithEnv).
	if pyDir := deps.EnsurePyDepsDir(); pyDir != "" {
		args = append(args, "--ro-bind", pyDir, deps.SandboxPyDepsMountPath)
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
	sb := activeSandbox()
	if !sb.confines() {
		if sandboxRequired(ctx) {
			warnRefusing("response pipes")
			return SandboxedShellResult{Err: sandboxUnavailableErr()}
		}
		warnUnsandboxed("response pipes")
	}
	c := sb.build(ctx, sandboxRun{Kind: sandboxPipeRun, Command: command})
	sandbox := sb.confines()
	c.Env = sandboxEnv(sb.remapsPaths())
	// A pipe got NO PYTHONPATH at all, so `from gohort import ...` and
	// `import openpyxl` both died with ModuleNotFoundError in a pipe while
	// working in every other sandboxed context. Same value the shell path
	// computes, so the two agree about where the helpers are.
	//
	// This does not make a pipe able to FETCH — it has no hook socket and
	// --unshare-net — and it is not meant to. What it buys is that the
	// import resolves and the failure becomes the helper's own sentence
	// ("GOHORT_HOOK_PATH not set"), which says what is wrong, instead of a
	// missing-module error that says the tool is broken.
	c.Env = append(c.Env, "PYTHONPATH="+sandboxPythonPath(sb.remapsPaths(), ""))
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
//
// The helper mounts are here for the same reason they are on the shell
// path: without them PYTHONPATH names two directories that exist only
// inside a sandbox that never mounted them, and a pipe cannot import
// anything the deployment provides. A pipe is the one place a missing
// mount is completely silent — it has no workspace to look in and no
// author sitting next to it.
func bwrapPipeArgv(shellCmd string) []string {
	args := []string{
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
	}
	// Same two RO binds the shell path gets, and the same best-effort
	// posture: a deployment that could not write them still runs, the
	// imports just fail.
	if libDir := gohortLibDir(); libDir != "" {
		args = append(args, "--ro-bind", libDir, GohortLibMountPath)
	}
	if pyDir := deps.EnsurePyDepsDir(); pyDir != "" {
		args = append(args, "--ro-bind", pyDir, deps.SandboxPyDepsMountPath)
	}
	return append(args, "--", "sh", "-c", shellCmd)
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
	sb := activeSandbox()
	if !sb.confines() {
		if sandboxRequired(ctx) {
			warnRefusing("evaluator scripts")
			return SandboxedScriptResult{Err: sandboxUnavailableErr()}
		}
		warnUnsandboxed("evaluator scripts")
	}
	c := sb.build(ctx, sandboxRun{Kind: sandboxScriptRun, Interpreter: interpreter, Command: script})
	sandbox := sb.confines()
	c.Env = sandboxEnv(sb.remapsPaths())
	// The managed python deps reach a remapped run through --setenv on the
	// mount path (see bwrapScriptArgv). Without remapping there is no mount and
	// no --setenv, so this ran with no PYTHONPATH at all and every generator
	// that imports openpyxl / python-docx / python-pptx failed on a host where
	// the packages were provisioned and present.
	if !sb.remapsPaths() {
		if pyDir := deps.EnsurePyDepsDir(); pyDir != "" {
			c.Env = append(c.Env, "PYTHONPATH="+deps.PrependPythonPath("", pyDir))
		}
	}
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
	if pyDir := deps.EnsurePyDepsDir(); pyDir != "" {
		args = append(args,
			"--ro-bind", pyDir, deps.SandboxPyDepsMountPath,
			"--setenv", "PYTHONPATH", deps.SandboxPyDepsMountPath,
		)
	}
	args = append(args, "--", interpreter, "-c", script)
	return args
}

// sandboxEnv returns the environment for sandboxed commands. PATH must
// survive so common utilities resolve; secrets the gohort process holds
// must NOT survive — env vars like API keys, AWS creds, etc. would
// otherwise leak straight into LLM-controlled shell scope.
func sandboxEnv(remaps bool) []string {
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
	// Prepend the gohort shim bin dir so a script can invoke fetch_url /
	// fetch_via / browse_page as ordinary commands (they proxy to the hook,
	// which still enforces capabilities).
	//
	// Which directory that is depends on whether the sandbox remaps paths, for
	// the same reason PYTHONPATH does: the mount path is a subpath of the RO
	// lib mount and exists only inside one. This used to prepend it and
	// call the result harmless — "a dead PATH entry, which the PATH search
	// simply skips". It is not harmless. Skipping it means that on any host
	// without a remapping sandbox the documented shell interface is silently
	// absent, and
	// `fetch_url https://…` fails as command-not-found on a system whose hook
	// is present, granted, and answering.
	if binDir := sandboxShimBinDir(remaps); binDir != "" {
		for i, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				rest := kv[len("PATH="):]
				if !strings.HasPrefix(rest, binDir+":") {
					env[i] = "PATH=" + binDir + ":" + rest
				}
				break
			}
		}
	}
	return env
}

// withReadOnlyBinds inserts --ro-bind flags for caller-supplied paths,
// before the "--" that separates bwrap's flags from the command.
//
// Skips anything already inside the workspace (it is bound writable
// there, and a later read-only bind of the same path would take the
// write away) and anything empty. Uses --ro-bind-try rather than
// --ro-bind so a path that has been removed since it resolved leaves the
// script reporting "no such file" instead of bwrap refusing to start,
// which is the difference between an error a model can act on and one it
// cannot see past.
func withReadOnlyBinds(args []string, readOnly []string, workspaceDir string) []string {
	if len(readOnly) == 0 {
		return args
	}
	sepIdx := len(args)
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	binds := make([]string, 0, len(readOnly)*3)
	seen := map[string]bool{}
	for _, p := range readOnly {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] || withinDir(p, workspaceDir) {
			continue
		}
		seen[p] = true
		binds = append(binds, "--ro-bind-try", p, p)
	}
	if len(binds) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+len(binds))
	out = append(out, args[:sepIdx]...)
	out = append(out, binds...)
	out = append(out, args[sepIdx:]...)
	return out
}

// RunSandboxedShellScoped is RunSandboxedShellWithEnv plus read-only
// access to paths a scope check has already proved.
//
// It REFUSES when the sandbox cannot SCOPE A READ, rather than running the
// command anyway. Everywhere else in this file an absent sandbox is a
// warning and the command still runs, because the alternative is a
// deployment where no tool works at all. Here the caller has been
// promised something specific — this path, read-only, and nothing else —
// and without a mount namespace that promise is not kept: the command
// would run with the daemon's own view of the filesystem, where the
// resolved path is readable and so is everything around it. A constraint
// that silently does not apply is worse than a refusal that says so.
//
// "Cannot scope a read" and "does not confine" are not the same condition,
// which is what this used to test for. See scopedRunRefusal.
func RunSandboxedShellScoped(ctx context.Context, command, workspaceDir string, extraEnv map[string]string, readOnly []string) SandboxedShellResult {
	if err := scopedRunRefusal(activeSandbox(), readOnly); err != nil {
		return SandboxedShellResult{Err: err}
	}
	return runSandboxedShellWithBinds(ctx, command, workspaceDir, extraEnv, readOnly)
}

// scopedRunRefusal is the decision, separated from the run so it can be
// exercised against a backend that does not confine — on a host with
// bubblewrap installed, a test of the refusal would otherwise skip, and a
// skipped test proves nothing about the case it names.
//
// nil when there is nothing to promise (no scoped paths) or the backend
// can keep the promise.
//
// The gate is scopesReads(), NOT confines(). The two came apart when the
// second confining backend arrived: Seatbelt confines writes and network
// but allows reads filesystem-wide, so on macOS this returned nil and the
// command then ran able to read the scoped path AND everything around it
// — the precise outcome the refusal exists to prevent, reached through the
// check meant to prevent it. A guard has to ask for the property it
// promises rather than a stronger-sounding one that seems to imply it.
func scopedRunRefusal(sb sandboxBackend, readOnly []string) error {
	if len(readOnly) == 0 || sb.scopesReads() {
		return nil
	}
	paths := strings.Join(readOnly, ", ")
	// Two different situations, and an operator can act on only one of them,
	// so they get different sentences. "No sandbox at all" is a host somebody
	// can fix by installing a package. "A sandbox that cannot scope a read" is
	// a property of the mechanism, where the only moves are a different host
	// or a different tool — telling that reader to install bubblewrap would be
	// advice they cannot follow, which is the mistake sandbox_backend.go was
	// written to stop repeating.
	if !sb.confines() {
		return Error("this tool reads a registered path (" + paths +
			") and this host has no sandbox to confine it to (" + sb.name() + "). Refusing rather " +
			"than running it with the daemon's own view of the filesystem, where that constraint " +
			"would not apply. " + unsandboxedAdvice() + " Or drop the path_scope from the tool's " +
			"parameter and accept that it is unconstrained.")
	}
	return Error("this tool reads a registered path (" + paths + ") and the " + sb.name() +
		" sandbox on this host cannot restrict reads to it: it confines writes and network, but its " +
		"policy allows reads filesystem-wide, so the path_scope narrows nothing. Refusing rather " +
		"than running a check that does not apply — the path would be readable and so would " +
		"everything around it. Run this tool on a Linux host with bubblewrap, or drop the " +
		"path_scope from the tool's parameter and accept that it is unconstrained.")
}
