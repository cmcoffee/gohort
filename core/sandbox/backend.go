// Which OS confinement mechanism this host actually has, and saying so.
//
// Sandboxing used to be spelled "bubblewrap": the code looked up `bwrap`, and
// treated its absence as a degraded edge case on an otherwise-Linux host. That
// is wrong on macOS in a way that is invisible from the Mac. bwrap cannot be
// installed there at all, so every LLM-issued shell command — run_local, every
// temp tool, persistent shells, export scripts, event-monitor evaluators — ran
// at the daemon's full privilege, while the one-time warning advised
// `apt install bubblewrap`. The fail-closed switch was worse: it did not harden
// anything on darwin, it just made shell tools permanently unavailable with an
// error telling the operator to install a Linux package.
//
// So the mechanism becomes a BACKEND, chosen per platform, and every message
// about it names something the reader can act on. There are two backends today
// — bubblewrap and none — and the seam exists because there will be a third:
// macOS confinement (Seatbelt) plugs in here without touching a caller.
//
// THE PROPERTY THAT GENERALIZES. Callers used to branch on "is bwrap present",
// but almost none of them cared about bubblewrap; they cared whether host
// directories appear at DIFFERENT paths inside the sandbox. bubblewrap remaps
// (it builds a mount namespace); a policy-based sandbox like Seatbelt does not
// (it constrains access to the real paths). That distinction is what decides
// where PYTHONPATH and the shim bin dir must point, and getting it wrong is a
// ModuleNotFoundError on the first line of every hook-using script. It is now
// asked as remapsPaths(), so the next backend answers it correctly by
// construction rather than by remembering.
package sandbox

import (
	"context"
	"os/exec"
	"runtime"
	"sync"

	"github.com/cmcoffee/snugforge/nfo"
)

// sandboxRunKind distinguishes the three shapes a sandboxed run takes. They
// differ in what the command is allowed to reach, so they cannot share one
// argv: a workspace run needs a writable directory, while a pipe or an
// evaluator script is pure data transformation that should reach no filesystem
// at all.
type sandboxRunKind int

const (
	// sandboxShellRun is a shell command with workspaceDir writable.
	sandboxShellRun sandboxRunKind = iota
	// sandboxPipeRun is a shell command fed on stdin, no workspace.
	sandboxPipeRun
	// sandboxScriptRun is an interpreter running a script body, no workspace.
	sandboxScriptRun
)

// sandboxRun describes one run. A struct rather than a parameter list because
// the shapes share most fields and differ in a couple, and threading five
// positional arguments through three backends is how they drift apart.
type sandboxRun struct {
	Kind         sandboxRunKind
	Command      string            // shell command, or the script body
	Interpreter  string            // sandboxScriptRun only
	WorkspaceDir string            // sandboxShellRun only; the single writable path
	Env          map[string]string // extras merged into the sandbox env
	AllowNetwork bool
	// ReadOnly are host paths to expose READ-ONLY inside the sandbox, at
	// the same path they have outside.
	//
	// It exists for a tool whose parameter is scoped to a registered root
	// (core/path_scope.go): the scope check proves the path, and then the
	// script cannot open it, because a sandbox binds nothing it was not
	// told about. A check that passes and a tool that fails is worse than
	// either alone — it reads as the check being wrong.
	//
	// Same path inside as out, so a resolved absolute path is correct on
	// both sides with no translation, and read-only because these are
	// somebody's registered corpus rather than the tool's scratch space.
	//
	// Only a backend whose scopesReads() is true honors this. The others
	// cannot narrow a read at all, so a run that sets this field is refused
	// before it starts (scopedRunRefusal) rather than running with the field
	// quietly ignored.
	ReadOnly []string
}

// sandboxBackend is one OS confinement mechanism.
type sandboxBackend interface {
	// name identifies the backend in logs and in the admin status panel.
	name() string
	// confines reports whether this backend actually isolates anything. False
	// for the none backend, which exists so callers have something uniform to
	// call rather than a nil check.
	confines() bool
	// remapsPaths reports whether host directories appear at different paths
	// inside the sandbox. See the file comment — this is the question callers
	// actually have.
	remapsPaths() bool
	// scopesReads reports whether this backend can restrict READS to a named
	// set of paths — i.e. whether sandboxRun.ReadOnly means anything.
	//
	// A SECOND property, distinct from confines(), because a backend can
	// genuinely confine and still be unable to keep this particular promise.
	// bubblewrap builds a mount namespace, so a path it was not told about
	// does not exist; Seatbelt evaluates a policy against the real filesystem
	// and its profile aborts on load if file-read* is scoped by subpath, so it
	// allows reads filesystem-wide and constrains writes and network instead.
	//
	// Asked separately because scopedRunRefusal used to gate on confines(),
	// which reads as the right question and is not: on macOS the guard passed
	// and the command then ran able to read the scoped folder AND everything
	// else — the exact state the guard exists to refuse. Whether a backend
	// confines and whether it can scope a read are two facts, so the next
	// backend answers both by construction rather than by remembering.
	scopesReads() bool
	// build returns the command to execute for one run.
	build(ctx context.Context, run sandboxRun) *exec.Cmd
}

// --- bubblewrap ---------------------------------------------------------------

type bwrapSandbox struct{ path string }

func (b bwrapSandbox) name() string      { return "bubblewrap" }
func (b bwrapSandbox) confines() bool    { return true }
func (b bwrapSandbox) remapsPaths() bool { return true } // it builds a mount namespace
func (b bwrapSandbox) scopesReads() bool { return true } // --ro-bind: unlisted paths do not exist

func (b bwrapSandbox) build(ctx context.Context, run sandboxRun) *exec.Cmd {
	var args []string
	switch run.Kind {
	case sandboxPipeRun:
		args = bwrapPipeArgv(run.Command)
	case sandboxScriptRun:
		args = bwrapScriptArgv(run.Interpreter, run.Command)
	default:
		args = bwrapArgvWithEnv(run.WorkspaceDir, run.Command, run.Env, run.AllowNetwork)
		args = withReadOnlyBinds(args, run.ReadOnly, run.WorkspaceDir)
	}
	return exec.CommandContext(ctx, b.path, args...)
}

// --- none ---------------------------------------------------------------------

// noSandbox runs the command directly. Kept as a real backend rather than a nil
// case so the three call sites have no branch of their own to get wrong, and so
// "unconfined" is a state the status surface can report rather than the absence
// of one.
type noSandbox struct{}

func (noSandbox) name() string      { return "none" }
func (noSandbox) confines() bool    { return false }
func (noSandbox) remapsPaths() bool { return false }
func (noSandbox) scopesReads() bool { return false }

func (noSandbox) build(ctx context.Context, run sandboxRun) *exec.Cmd {
	switch run.Kind {
	case sandboxScriptRun:
		return exec.CommandContext(ctx, run.Interpreter, "-c", run.Command)
	case sandboxPipeRun:
		c := exec.CommandContext(ctx, "sh", "-c", run.Command)
		// /tmp rather than the process's cwd: a pipe command has no workspace,
		// and leaving it in gohort's own directory points relative paths at the
		// install tree.
		c.Dir = "/tmp"
		return c
	default:
		c := exec.CommandContext(ctx, "sh", "-c", run.Command)
		c.Dir = run.WorkspaceDir
		return c
	}
}

// --- selection ------------------------------------------------------------------

var (
	sandboxOnce   sync.Once
	sandboxActive sandboxBackend
)

// activeSandbox returns the strongest backend available on this host, resolved
// once.
//
// Resolved once because the answer cannot change while the process runs — a
// binary does not appear on PATH mid-run in any way worth honoring — and
// because the alternative is a PATH lookup on every shell command.
func activeSandbox() sandboxBackend {
	sandboxOnce.Do(func() {
		sandboxActive = detectSandbox()
	})
	return sandboxActive
}

// detectSandbox picks a backend for this platform.
//
// Written as a per-GOOS switch rather than "try everything and take what
// works", so that an unimplemented platform is a stated fact rather than the
// residue of a failed lookup. That distinction is the whole reason this file
// exists: on macOS the old code's silence was indistinguishable from a Linux
// host that had simply not installed a package.
func detectSandbox() sandboxBackend {
	return detectSandboxFor(runtime.GOOS, exec.LookPath, seatbeltUsable)
}

// detectSandboxFor takes the platform, the PATH lookup and the Seatbelt probe as
// arguments so the selection is testable on any host — including the darwin
// branch, which is the one that was silently wrong and the one that runs where
// this suite does not.
func detectSandboxFor(goos string, look func(string) (string, error), probe func(string) bool) sandboxBackend {
	switch goos {
	case "linux":
		if p, err := look("bwrap"); err == nil {
			nfo.Debug("[sandbox] bubblewrap at %s — shell tools are OS-sandboxed", p)
			return bwrapSandbox{path: p}
		}
		nfo.Debug("[sandbox] bwrap not found on PATH — shell tools will run unconfined")
	case "darwin":
		// Deliberately NOT falling through to a bwrap lookup: it can never
		// succeed on darwin, and pretending to try is what produced the
		// misleading "not installed" reading this file is about.
		//
		// Probed rather than merely located. sandbox-exec has been deprecated
		// for a decade and its profile language is undocumented, so the binary
		// being present is not evidence it accepts what we generate — and
		// assuming it does would break every shell tool on the Mac at once.
		if p, err := look(seatbeltBinary); err == nil {
			if probe(p) {
				nfo.Debug("[sandbox] seatbelt at %s — shell tools are OS-sandboxed", p)
				return seatbeltSandbox{path: p}
			}
		}
		nfo.Debug("[sandbox] no usable sandbox backend on macOS — shell tools will run unconfined")
	default:
		nfo.Debug("[sandbox] no sandbox backend for %s — shell tools will run unconfined", goos)
	}
	return noSandbox{}
}

// SandboxStatus describes this host's confinement, for the admin panel.
//
// Exported because a log line at first use is not a surface anyone checks. The
// state that matters — every shell command on this machine runs unconfined —
// was previously discoverable only by reading a warning that may have scrolled
// past days ago, on a platform where the advice it gave was impossible to
// follow.
type SandboxStatus struct {
	Backend  string `json:"backend"`  // "bubblewrap" | "none"
	Confined bool   `json:"confined"` // false means shell tools run at the daemon's privilege
	Required bool   `json:"required"` // GOHORT_SANDBOX_REQUIRED is set
	Advice   string `json:"advice"`   // what to do about it, or "" when confined
}

// GetSandboxStatus reports what is confining shell execution on this host.
func GetSandboxStatus() SandboxStatus {
	sb := activeSandbox()
	st := SandboxStatus{Backend: sb.name(), Confined: sb.confines(), Required: sandboxRequired()}
	if !st.Confined {
		st.Advice = unsandboxedAdvice()
	}
	return st
}

// unsandboxedAdvice is the platform-appropriate next step for an operator whose
// host has no sandbox. Never "install bubblewrap" on a platform that cannot
// install bubblewrap.
func unsandboxedAdvice() string { return unsandboxedAdviceFor(runtime.GOOS) }

// unsandboxedAdviceFor takes the platform as an argument so every branch is
// reachable from a test.
//
// The darwin branch is the reason this whole file exists and it runs on a
// machine the Linux test suite never touches. Gating it behind runtime.GOOS
// alone would leave the one message that matters most verified by nothing but a
// cross-compile — which proves it parses, not that it says anything useful.
func unsandboxedAdviceFor(goos string) string {
	switch goos {
	case "linux":
		return "Install bubblewrap (apt install bubblewrap / dnf install bubblewrap)."
	case "darwin":
		return "macOS confinement uses sandbox-exec (Seatbelt), and this host either lacks it or it refused the probe profile — " +
			"check the log for the refusal. Nothing can be installed to fix that; sandbox-exec ships with macOS or not at all. " +
			"Either accept that shell tools run at this account's privilege, run them on a Linux peer, or set GOHORT_SANDBOX_REQUIRED=1 to refuse them."
	default:
		return "gohort has no sandbox backend for " + goos + "."
	}
}

// sandboxUnavailableErr is the fail-closed refusal, phrased for this platform.
//
// A function rather than the constant it replaced: the old text told every
// operator to install bubblewrap, which on macOS is an instruction to do
// something impossible in order to fix something that was never going to work.
func sandboxUnavailableErr() error {
	return Error("sandbox required (GOHORT_SANDBOX_REQUIRED) but this host has none — refusing to run the tool unsandboxed. " +
		unsandboxedAdvice() + " Or clear GOHORT_SANDBOX_REQUIRED to run unconfined.")
}

var sandboxWarnOnce sync.Once

// warnUnsandboxed logs the degraded state once per process, naming what is
// running unconfined.
func warnUnsandboxed(what string) {
	sandboxWarnOnce.Do(func() {
		nfo.Log("[sandbox] WARNING: no OS sandbox on this host (%s) — %s run with gohort user permissions. %s Set GOHORT_SANDBOX_REQUIRED=1 to refuse unsandboxed execution instead.",
			runtime.GOOS, what, unsandboxedAdvice())
	})
}
