// Confinement by container, via podman or docker.
//
// The third backend, and the first one that is NOT about a platform. bubblewrap
// and Seatbelt exist because Linux and macOS confine differently; this exists
// because a container answers a question neither of them can.
//
// WHAT IT BUYS, WHICH IS NOT SECURITY. On Linux bwrap already isolates well and
// starts in single-digit milliseconds, where a container costs a few hundred.
// Judged only on confinement this is a downgrade. What it changes is the
// USERSPACE: bwrap ro-binds the host's /usr, so a sandboxed command sees exactly
// the toolchain the operator installed globally, as root, for every user on the
// box. gohort currently needs python3, pandoc, pdftotext, node, yt-dlp, ffmpeg
// and aws that way, and core/deps exists to work around the same problem for
// Python packages. An image supplies those without touching the host.
//
// So it is OPT-IN and never auto-selected. Swapping the entire filesystem a
// command sees is a deployment decision, not a "strongest available" one, and a
// host with bwrap installed should keep using it unless somebody says otherwise.
// GOHORT_SANDBOX_BACKEND is that somebody.
//
// PODMAN FIRST, DELIBERATELY. Reaching /var/run/docker.sock is root-equivalent:
// anything that can make the daemon run a container can mount / and own the
// host. A backend that confines the CHILD better while handing the PARENT root
// is a bad trade, and it is not hypothetical — the gohort daemon is the process
// holding that socket. Rootless podman has no daemon and no such socket, so it
// is preferred whenever both are present, and docker is accepted because some
// deployments have only it.
//
// KNOWN GAP, and it is the one that will bite. The managed Python deps
// (core/deps) are pip-installed on the HOST against the host interpreter and
// mounted read-only into the sandbox. Under bwrap that is the same interpreter,
// because /usr is the host's. Under a container it is the image's, and a
// compiled wheel built for one CPython ABI will not import into another. Pure
// Python survives; numpy, lxml and pillow do not. The probe compares the two
// versions and says so at startup rather than leaving it to be discovered on
// the first import.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/deps"
	"github.com/cmcoffee/snugforge/nfo"
)

const (
	// containerProbeTimeout bounds the startup probe. Generous, because a cold
	// runtime may have to pull the image, and a pull over a slow link is a
	// legitimate reason to wait once rather than a reason to declare the
	// backend broken.
	containerProbeTimeout = 120 * time.Second

	// defaultContainerImage is what runs when nobody names one.
	//
	// Debian-based rather than Alpine on purpose: Alpine is musl, and the
	// managed pip wheels mounted in from the host are glibc. An image that
	// cannot load them at all is a worse default than one that merely might
	// have the wrong CPython minor.
	defaultContainerImage = "docker.io/library/python:3-slim"
)

// containerSandbox runs each command in a throwaway container.
type containerSandbox struct {
	path  string // absolute path to the podman or docker binary
	kind  string // "podman" | "docker" — they differ in how uid mapping is asked for
	image string
}

func (c containerSandbox) name() string   { return c.kind }
func (c containerSandbox) confines() bool { return true }

// remapsPaths is true for the same reason bubblewrap's is: the gohort helper
// and the managed deps appear at their mount points, not their host paths. The
// WORKSPACE is deliberately mounted at the same path inside as out, so an
// absolute path in a tool argument means the same thing on both sides — but
// that is one mount, and remapsPaths asks about the helper paths PYTHONPATH and
// the shim bin dir have to name.
func (c containerSandbox) remapsPaths() bool { return true }

// scopesReads is true: a container's filesystem is the image plus what was
// mounted into it, so a path nobody mounted does not exist. Same property
// bubblewrap gets from a mount namespace, reached the same way.
func (c containerSandbox) scopesReads() bool { return true }

func (c containerSandbox) build(ctx context.Context, run sandboxRun) *exec.Cmd {
	args := []string{"run", "--rm", "--interactive"}

	// Identity. Rootless podman maps the host user into the container, and
	// keep-id makes that mapping the IDENTITY one so a file written into the
	// bind-mounted workspace comes back owned by the daemon rather than by a
	// subordinate uid nothing else can read. docker has no such mapping and
	// defaults the container to root, which would leave root-owned droppings in
	// a workspace the daemon has to clean up, so it is told the uid outright.
	switch c.kind {
	case "podman":
		args = append(args, "--userns=keep-id")
	default:
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}

	// Hardening that costs nothing here. bwrap gets most of this from the
	// namespaces it unshares; a container has to ask.
	args = append(args,
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		// The image's own filesystem is throwaway, but read-only says so: the
		// only writable paths are the workspace and the tmpfs below, which is
		// the same promise bwrap makes.
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev",
		"--tmpfs", "/var/tmp:rw,nosuid,nodev",
		"--tmpfs", "/run:rw,nosuid,nodev",
	)

	if !run.AllowNetwork {
		args = append(args, "--network", "none")
	}

	// Mounts. Same set bwrapArgv binds, minus the system directories: those
	// come from the image, which is the entire point.
	if run.Kind == sandboxShellRun && run.WorkspaceDir != "" {
		args = append(args, "--volume", run.WorkspaceDir+":"+run.WorkspaceDir+":rw", "--workdir", run.WorkspaceDir)
	} else {
		args = append(args, "--workdir", "/tmp")
	}
	if libDir := gohortLibDir(); libDir != "" {
		args = append(args, "--volume", libDir+":"+GohortLibMountPath+":ro")
	}
	if pyDir := deps.EnsurePyDepsDir(); pyDir != "" {
		args = append(args, "--volume", pyDir+":"+deps.SandboxPyDepsMountPath+":ro")
	}
	// The hook socket, by FILE rather than by directory, so a sandboxed script
	// gets its own and cannot list anyone else's. Same path inside as out, so
	// GOHORT_HOOK_PATH needs no translation.
	if p := run.Env["GOHORT_HOOK_PATH"]; p != "" && !withinDir(p, run.WorkspaceDir) {
		args = append(args, "--volume", p+":"+p)
	}
	for _, p := range run.ReadOnly {
		p = strings.TrimSpace(p)
		if p == "" || withinDir(p, run.WorkspaceDir) {
			continue
		}
		args = append(args, "--volume", p+":"+p+":ro")
	}

	// Environment. A container does NOT inherit the parent's env, so unlike the
	// other backends the caller's later `c.Env = ...` would set the env of the
	// podman CLIENT and reach nothing inside. Every variable has to be passed
	// as a flag, which means assembling here what exec.go assembles for the
	// others.
	for _, kv := range containerEnv(run.Env) {
		args = append(args, "--env", kv)
	}

	args = append(args, c.image)
	switch run.Kind {
	case sandboxScriptRun:
		args = append(args, run.Interpreter, "-c", run.Command)
	default:
		args = append(args, "sh", "-c", run.Command)
	}
	return exec.CommandContext(ctx, c.path, args...)
}

// containerEnv is the environment for a containerized run, as KEY=VALUE.
//
// It starts from sandboxEnv — the same allowlist every other backend gets, so
// the daemon's secrets are dropped here too — and then replaces PATH. The
// inherited PATH describes the HOST's filesystem, and inside a container those
// directories are the image's or absent; carrying it in would mean a PATH whose
// entries mostly do not exist, with the shim dir buried among them.
func containerEnv(extra map[string]string) []string {
	out := make([]string, 0, 8+len(extra))
	for _, kv := range sandboxEnv(true) {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PATH="+GohortBinMountPath+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// --- probe ---------------------------------------------------------------------

var (
	containerProbeOnce sync.Once
	containerProbeOK   bool
)

// containerUsable reports whether this runtime can actually run a container.
//
// Probed rather than merely located, and the reason is on this very host: the
// docker BINARY is on PATH while the daemon socket refuses the connection, so
// `exec.LookPath("docker")` succeeds and every command would then fail. Rootless
// podman has its own version of this — a missing /etc/subuid entry makes
// --userns=keep-id fail at run time, not at install time. Neither is visible
// without running something.
func containerUsable(c containerSandbox) bool {
	containerProbeOnce.Do(func() {
		ok, why := containerProbe(c, containerProbeRunner)
		if !ok {
			nfo.Log("[sandbox] WARNING: %s — falling back to unconfined, which under the default "+
				"fail-closed policy means shell tools are REFUSED.", why)
		}
		containerProbeOK = ok
	})
	return containerProbeOK
}

// containerProbe is the decision, with the runner injected so a test can drive
// every outcome on a host that has no working container runtime — which is most
// of them, including the one this suite runs on.
//
// The reason is RETURNED rather than logged here, so a test can assert on the
// sentence an operator will read. That is not ceremony: the first version
// logged the output of the `--version` call in the message about the RUN
// failing, so a docker whose daemon socket refuses the connection reported
// "Docker version 26.1.3" as its diagnosis. It said the one thing that was
// working.
func containerProbe(c containerSandbox, run func(containerSandbox, []string) (string, error)) (bool, string) {
	if strings.TrimSpace(c.path) == "" {
		return false, "no container runtime was located"
	}
	if out, err := run(c, []string{"--version"}); err != nil {
		return false, fmt.Sprintf("%s was requested but is not usable (%v: %s). "+
			"Check that the runtime is installed and that this account may use it.",
			c.kind, err, firstLine(out))
	}
	// A real container, with the same identity flags a real run uses: keep-id
	// fails on a host with no subuid delegation, and finding that out here beats
	// finding it out on the first tool call.
	out, err := run(c, containerProbeArgs(c))
	if err != nil {
		return false, fmt.Sprintf("%s cannot run a container (%v: %s). "+
			"The image is %q; check that it can be pulled and that this account may run containers.",
			c.kind, err, firstLine(out), c.image)
	}
	warnPythonSkew(out)
	nfo.Log("[sandbox] %s probe passed — shell tools run in %s", c.kind, c.image)
	return true, ""
}

// containerProbeArgs runs the image's python3 and prints its version. Chosen
// over `true` because it proves three things at once: the runtime works, the
// image resolves, and the interpreter the managed deps will be loaded into is
// the one named in the output.
func containerProbeArgs(c containerSandbox) []string {
	args := []string{"run", "--rm"}
	if c.kind == "podman" {
		args = append(args, "--userns=keep-id")
	} else {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	return append(args, "--network", "none", c.image, "python3", "--version")
}

func containerProbeRunner(c containerSandbox, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), containerProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.path, args...).CombinedOutput()
	return string(out), err
}

// warnPythonSkew compares the image's interpreter to the host's.
//
// The managed pip wheels are built on the host and mounted in, so a mismatched
// CPython minor means `import numpy` fails with a message about the module,
// naming nothing about why. deps.SandboxPythonVersion probes the HOST directly
// and its comment says that is safe because /usr is bind-mounted — true for
// bubblewrap and false here, which is exactly the assumption worth naming out
// loud rather than fixing silently in a place nobody reads.
func warnPythonSkew(probeOutput string) {
	hostMaj, hostMin, ok := deps.SandboxPythonVersion()
	if !ok {
		return
	}
	imgMaj, imgMin, ok := parsePythonVersion(probeOutput)
	if !ok {
		return
	}
	if imgMaj == hostMaj && imgMin == hostMin {
		return
	}
	nfo.Log("[sandbox] WARNING: the container image runs Python %d.%d and this host runs %d.%d. "+
		"The managed Python packages are built on the host and mounted in, so PURE-python ones still "+
		"import and COMPILED ones (numpy, lxml, pillow) will not. Pin an image whose Python is %d.%d, "+
		"via GOHORT_SANDBOX_IMAGE.",
		imgMaj, imgMin, hostMaj, hostMin, hostMaj, hostMin)
}

// parsePythonVersion reads "Python 3.11.2" into 3, 11.
func parsePythonVersion(s string) (major, minor int, ok bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) < 2 {
		return 0, 0, false
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// firstLine trims a runtime's complaint to something that fits in a log line.
// Their errors run to several paragraphs of suggestion, and the first line is
// invariably the sentence that says what went wrong.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// containerImage is the image to run, overridable per deployment.
func containerImage() string {
	if s := strings.TrimSpace(os.Getenv("GOHORT_SANDBOX_IMAGE")); s != "" {
		return s
	}
	return defaultContainerImage
}
