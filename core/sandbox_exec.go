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
	"sync"
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
// Network is allowed inside the sandbox — most legitimate uses (curl,
// pip install, ffmpeg pulling fonts, etc.) need it. Block at a higher
// layer if a session shouldn't have network.
func RunSandboxedShell(ctx context.Context, command, workspaceDir string) SandboxedShellResult {
	bwrap := detectBwrap()

	var c *exec.Cmd
	sandbox := false
	if bwrap != "" {
		args := bwrapArgv(workspaceDir, command)
		c = exec.CommandContext(ctx, bwrap, args...)
		sandbox = true
	} else {
		bwrapWarnedOnce.Do(func() {
			Log("[sandbox] WARNING: bwrap not installed — shell tools run with gohort user permissions. Install bubblewrap to enable real sandboxing (apt install bubblewrap / dnf install bubblewrap).")
		})
		c = exec.CommandContext(ctx, "sh", "-c", command)
		c.Dir = workspaceDir
	}
	c.Env = sandboxEnv()

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

// bwrapArgv builds the bwrap invocation. Workspace is bind-mounted as
// the only writable path; system dirs are read-only; /tmp is a fresh
// tmpfs per invocation; namespaces are unshared so the command can't
// see the host's processes, IPC, or hostname.
func bwrapArgv(workspaceDir, shellCmd string) []string {
	args := []string{
		// Lifecycle.
		"--die-with-parent",   // kill child when bwrap dies
		"--new-session",       // detach controlling tty
		// Namespace isolation. PID/UTS/IPC are uncontroversial. We do
		// NOT --unshare-net so curl / pip / etc. keep working; gate at
		// a higher layer if a session shouldn't have network.
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
