// macOS confinement, via Seatbelt (`sandbox-exec`).
//
// The Linux backend hides the filesystem: outside its bind mounts nothing
// EXISTS, because bubblewrap builds a mount namespace. Seatbelt cannot do that.
// It is a policy evaluated against the real filesystem, so the shape here is
// "deny by default, then allow named paths", and a denied path fails with a
// permission error rather than vanishing. The security outcome for the thing
// that matters — an LLM-issued command reading ~/.ssh or writing outside its
// workspace — is the same. What differs is spelled out under KNOWN GAPS below,
// because a backend that quietly confines less than the one it stands in for is
// worse than no backend at all.
//
// PATHS ARE NOT REMAPPED, which is why seatbeltSandbox.remapsPaths() is false.
// The gohort helper library and the managed python deps are allowed at their
// real host locations, and sandboxPythonPath already points PYTHONPATH there
// when the backend does not remap. That is the whole reason remapsPaths()
// exists rather than a bwrap check.
//
// ON THE DEPRECATION. `sandbox-exec` has carried a deprecation notice since
// 10.10 and its profile language has never been publicly documented. It is
// still shipping, still used inside macOS, and it is the only mechanism that
// confines an arbitrary child process without entitlements and a signed bundle.
// So it is used, and it is used defensively: seatbeltUsable() runs a trivial
// command under a real profile at startup, and a failure demotes this host to
// the none backend with a loud log rather than breaking every shell tool on the
// machine. If a future macOS drops it, that is what happens — degraded, not
// broken, and said out loud.
//
// KNOWN GAPS versus bubblewrap, none of which are silent:
//   - No PID/UTS/IPC namespaces. Seatbelt governs file, network and mach
//     access; it does not give the child its own process table.
//   - No tmpfs. /tmp is the real /tmp, writable, shared with the host.
//   - Metadata is readable filesystem-wide (see seatbeltProfile). Contents are
//     not. A command can learn that a path exists without reading it.
package core

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// seatbeltProbeTimeout bounds the startup probe. Generous for spawning
// /usr/bin/true, short enough that a wedged sandbox-exec cannot hold up the
// first shell tool of the process.
const seatbeltProbeTimeout = 10 * time.Second

// seatbeltBinary is Apple's profile-driven sandbox launcher.
const seatbeltBinary = "/usr/bin/sandbox-exec"

// seatbeltReadPaths are the system locations a command must be able to read and
// execute for ordinary utilities to work.
//
// /opt/homebrew and /usr/local are here because on macOS the interpreters
// actually in use — python3, bash 5, coreutils — are usually Homebrew's, not
// Apple's. Omitting them yields "command not found" for the tooling every
// script assumes, which reads as a broken sandbox rather than a missing path.
var seatbeltReadPaths = []string{
	"/usr", "/bin", "/sbin", "/System", "/Library",
	"/opt/homebrew", "/opt/local", "/usr/local",
	"/etc", "/private/etc", "/var/select", "/private/var/select",
	"/dev",
}

// seatbeltWritePaths are writable regardless of the run shape. The real /tmp,
// because Seatbelt has no tmpfs to substitute — noted in the file header as a
// gap rather than pretended away.
var seatbeltWritePaths = []string{
	"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp", "/dev/null",
}

// seatbeltSandbox confines with sandbox-exec.
type seatbeltSandbox struct{ path string }

func (s seatbeltSandbox) name() string   { return "seatbelt" }
func (s seatbeltSandbox) confines() bool { return true }

// remapsPaths is false: Seatbelt constrains the REAL filesystem rather than
// presenting a rearranged one. Answering true here would put PYTHONPATH and the
// shim bin dir at bubblewrap's mount points, which exist nowhere on macOS, and
// every hook-using script would die on its first line with ModuleNotFoundError.
func (s seatbeltSandbox) remapsPaths() bool { return false }

func (s seatbeltSandbox) build(ctx context.Context, run sandboxRun) *exec.Cmd {
	spec := seatbeltSpec{
		Workspace:    run.WorkspaceDir,
		AllowNetwork: run.AllowNetwork,
		HookSocket:   run.Env["GOHORT_HOOK_PATH"],
	}
	// The pipe and script shapes are pure data transformation: no workspace, no
	// outbound. Mirrors bwrapPipeArgv / bwrapScriptArgv, which both pass
	// --unshare-net unconditionally.
	if run.Kind != sandboxShellRun {
		spec.Workspace = ""
		spec.AllowNetwork = false
		spec.HookSocket = ""
	}
	if lib := EnsureGohortLibDir(); lib != "" {
		spec.ReadOnly = append(spec.ReadOnly, lib)
	}
	if py := EnsurePyDepsDir(); py != "" {
		spec.ReadOnly = append(spec.ReadOnly, py)
	}

	profile := seatbeltProfile(spec)
	var argv []string
	switch run.Kind {
	case sandboxScriptRun:
		argv = []string{"-p", profile, run.Interpreter, "-c", run.Command}
	default:
		argv = []string{"-p", profile, "/bin/sh", "-c", run.Command}
	}
	c := exec.CommandContext(ctx, s.path, argv...)
	if run.Kind == sandboxShellRun {
		c.Dir = run.WorkspaceDir
	} else {
		c.Dir = "/tmp"
	}
	return c
}

// seatbeltSpec is what a profile is generated from.
type seatbeltSpec struct {
	Workspace    string   // the single writable working directory, or "" for none
	ReadOnly     []string // extra host dirs readable but not writable
	HookSocket   string   // unix socket the hook listens on, or ""
	AllowNetwork bool
}

// seatbeltProfile renders an SBPL profile.
//
// Pure text in, text out, with no filesystem or exec of its own, so every rule
// this produces is checkable from a Linux test run. That matters more here than
// anywhere else in the sandbox code: the profile is the entire security
// boundary on macOS, it is written in a language with no public specification,
// and it executes on a machine the test suite never runs on.
func seatbeltProfile(spec seatbeltSpec) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")

	// Metadata filesystem-wide. A deliberate widening over bubblewrap, which
	// hides unlisted paths entirely: macOS resolves a path by stat-ing every
	// ancestor, and denying that makes even permitted paths unreachable.
	// Contents stay denied, so this leaks existence and not content.
	b.WriteString("(allow file-read-metadata)\n")

	// Process and runtime basics. Without mach-lookup almost nothing on macOS
	// starts — the dynamic linker and libSystem both need it.
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow signal (target self))\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")

	// Two rules rather than one combined "(allow file-read* process-exec …)".
	// Both forms are meant to be legal, but the profile language has no public
	// specification and a rejected profile aborts with no useful message, so the
	// plainest construct that expresses the intent is worth preferring on
	// principle — there is nowhere to look up which of two spellings a given
	// macOS accepts.
	read := append([]string{}, seatbeltReadPaths...)
	read = append(read, spec.ReadOnly...)
	b.WriteString("(allow file-read*\n")
	for _, p := range read {
		b.WriteString("  " + sbSubpath(p) + "\n")
	}
	b.WriteString(")\n")
	b.WriteString("(allow process-exec\n")
	for _, p := range read {
		b.WriteString("  " + sbSubpath(p) + "\n")
	}
	b.WriteString(")\n")

	write := append([]string{}, seatbeltWritePaths...)
	if spec.Workspace != "" {
		write = append(write, spec.Workspace)
	}
	b.WriteString("(allow file-read* file-write*\n")
	for _, p := range write {
		b.WriteString("  " + sbSubpath(p) + "\n")
	}
	b.WriteString(")\n")

	if spec.AllowNetwork {
		b.WriteString("(allow network*)\n")
	} else if spec.HookSocket != "" {
		// Even with outbound denied, the hook has to stay reachable — it is how
		// a sandboxed script performs the network calls it is NOT allowed to
		// make directly, under capability checks on the host side. A unix
		// socket connect is network-outbound in SBPL, so it needs naming even
		// though nothing leaves the machine.
		b.WriteString("(allow network-outbound " + sbLiteral(spec.HookSocket) + ")\n")
	}
	if spec.HookSocket != "" {
		b.WriteString("(allow file-read* file-write* " + sbLiteral(spec.HookSocket) + ")\n")
	}
	return b.String()
}

// sbSubpath renders a subpath rule.
func sbSubpath(p string) string { return `(subpath ` + sbString(p) + `)` }

// sbLiteral renders an exact-path rule.
func sbLiteral(p string) string { return `(literal ` + sbString(p) + `)` }

// sbString quotes a path for SBPL.
//
// THE injection point. A profile is one argv string, and a workspace path
// containing a quote would end the string and let the rest of the path be
// parsed as policy — a directory named `"))(allow default)(deny nothing` would
// otherwise switch the sandbox off. Workspace paths are gohort-generated today
// and this must keep holding when they stop being.
func sbString(p string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n', '\r':
			// Cannot be escaped into an SBPL string; dropped rather than
			// emitted raw, which would split the rule across lines.
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var (
	seatbeltProbeOnce sync.Once
	seatbeltProbeOK   bool
)

// seatbeltUsable reports whether sandbox-exec on this host actually accepts a
// profile of the shape used here and runs a command under it.
//
// A real end-to-end probe rather than a stat of the binary. The profile
// language is undocumented and the tool has been deprecated for a decade, so
// "the binary exists" is not evidence it works — and the failure mode of
// assuming it does is that every shell tool on the Mac breaks at once, with an
// error from a sandbox nobody knew had been switched on.
func seatbeltUsable(binary string) bool {
	seatbeltProbeOnce.Do(func() {
		seatbeltProbeOK = seatbeltProbe(binary, seatbeltProbeRunner(binary))
	})
	return seatbeltProbeOK
}

// seatbeltProbe is the probe itself, taking the runner so a test can exercise
// both outcomes. Memoization lives in seatbeltUsable rather than here, or the
// first test to run would decide the answer for every test after it.
func seatbeltProbe(binary string, runner func(profile string) error) bool {
	if strings.TrimSpace(binary) == "" {
		return false
	}
	if err := runner(seatbeltProfile(seatbeltSpec{})); err != nil {
		Log("[sandbox] WARNING: %s is present but refused a probe profile (%v) — "+
			"falling back to unconfined execution. Shell tools run with gohort user permissions.",
			binary, err)
		return false
	}
	Debug("[sandbox] seatbelt probe passed — shell tools are OS-sandboxed")
	return true
}

// seatbeltProbeRunner runs /usr/bin/true under the given profile.
//
// sandbox-exec's own complaint is captured and returned. Without it a rejected
// profile reports only the process result — "signal: abort trap" — which says
// that the profile was refused and nothing whatever about which rule did it.
// The profile language has no public specification, so that message is the only
// documentation there is.
func seatbeltProbeRunner(binary string) func(string) error {
	return func(profile string) error {
		ctx, cancel := context.WithTimeout(context.Background(), seatbeltProbeTimeout)
		defer cancel()
		c := exec.CommandContext(ctx, binary, "-p", profile, "/usr/bin/true")
		var stderr bytes.Buffer
		c.Stderr = &stderr
		err := c.Run()
		if err == nil {
			return nil
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%v: %s", err, firstLines(msg, 3))
		}
		return err
	}
}

// firstLines trims a multi-line tool complaint to its opening lines, which is
// where a parser puts the thing it choked on.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "; ")
}
