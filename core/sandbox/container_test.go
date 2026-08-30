package sandbox

// The container backend, driven without a container runtime.
//
// Every assertion here is about a DECISION — which backend gets picked, what
// ends up in the argv, what happens when the runtime is present but broken —
// and every one of those is reachable with the probe injected. That matters
// more than usual: the host this suite runs on has the docker binary on PATH
// and a daemon socket that refuses the connection, which is precisely the state
// the probe exists for and precisely the state a "does docker exist" check
// would get wrong.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

var errNotFound = errors.New("executable file not found in $PATH")

func neverUsable(containerSandbox) bool  { return false }
func alwaysUsable(containerSandbox) bool { return true }

func found(name string) (string, error) { return "/usr/bin/" + name, nil }

// A container backend is never chosen on its own. It swaps the entire
// filesystem a command sees, which is a deployment decision — and on Linux it
// is a downgrade on both isolation and start-up cost against the bwrap that is
// already there.
func TestAContainerBackendIsNeverAutoSelected(t *testing.T) {
	for _, pick := range []string{"", "auto"} {
		sb := detectSandboxFor(sandboxSelection{
			GOOS: "linux", Pick: pick, Look: found,
			Seatbelt: func(string) bool { return true }, Container: alwaysUsable,
		})
		if sb.name() != "bubblewrap" {
			t.Errorf("Pick=%q selected %q; a host with bwrap must keep it", pick, sb.name())
		}
	}
}

// Asked for by name, it is chosen — and podman wins a bare "container" request,
// because reaching docker's socket is root-equivalent for the daemon doing the
// reaching.
func TestPodmanIsPreferredOverDocker(t *testing.T) {
	sb := detectSandboxFor(sandboxSelection{
		GOOS: "linux", Pick: "container", Image: "img", Look: found, Container: alwaysUsable,
	})
	if sb.name() != "podman" {
		t.Errorf("a bare container request selected %q, want podman", sb.name())
	}
	// Only docker installed: taken, because some deployments have only it.
	onlyDocker := func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errNotFound
	}
	sb = detectSandboxFor(sandboxSelection{
		GOOS: "linux", Pick: "container", Image: "img", Look: onlyDocker, Container: alwaysUsable,
	})
	if sb.name() != "docker" {
		t.Errorf("with only docker present, got %q", sb.name())
	}
	// Asked for docker by name, podman present: the operator's choice wins.
	sb = detectSandboxFor(sandboxSelection{
		GOOS: "linux", Pick: "docker", Image: "img", Look: found, Container: alwaysUsable,
	})
	if sb.name() != "docker" {
		t.Errorf("an explicit docker request selected %q", sb.name())
	}
}

// The state this host is actually in: the binary is on PATH and the runtime
// cannot run anything. It must NOT fall back to bwrap — an operator who asked
// for containers and silently got something else has no way to notice.
func TestAnInstalledButUnusableRuntimeDoesNotSilentlyFallBackToBwrap(t *testing.T) {
	sb := detectSandboxFor(sandboxSelection{
		GOOS: "linux", Pick: "podman", Image: "img", Look: found, Container: neverUsable,
	})
	if sb.confines() {
		t.Fatalf("an unusable runtime must not confine; got %q", sb.name())
	}
	if sb.name() != "none" {
		t.Errorf("expected the none backend, got %q", sb.name())
	}
}

// A typo must not read as "use the default and say nothing", but it also must
// not refuse: an unrecognized name is a request nobody can act on, not a request
// for less confinement.
func TestAnUnknownBackendNameFallsBackToThePlatformDefault(t *testing.T) {
	sb := detectSandboxFor(sandboxSelection{
		GOOS: "linux", Pick: "podmn", Look: found,
		Seatbelt: func(string) bool { return true }, Container: alwaysUsable,
	})
	if sb.name() != "bubblewrap" {
		t.Errorf("a typo should land on the platform default, got %q", sb.name())
	}
}

// The container backend answers the two properties callers actually branch on.
// Both are true for the same reason bubblewrap's are, reached differently: the
// helper paths are mount points, and a path nobody mounted does not exist.
func TestAContainerRemapsPathsAndScopesReads(t *testing.T) {
	c := containerSandbox{path: "/usr/bin/podman", kind: "podman", image: "img"}
	if !c.remapsPaths() {
		t.Error("the helper and deps live at mount points, so paths are remapped")
	}
	if !c.scopesReads() {
		t.Error("a container's filesystem is the image plus its mounts, so reads are scoped")
	}
}

// The argv is where every promise in this backend actually lands, so it is
// asserted rather than assumed.
func TestTheContainerArgvCarriesTheConfinementPromises(t *testing.T) {
	c := containerSandbox{path: "/usr/bin/podman", kind: "podman", image: "testimg"}
	ws := t.TempDir()
	argv := strings.Join(c.build(context.Background(), sandboxRun{
		Kind: sandboxShellRun, Command: "ls", WorkspaceDir: ws,
		Env: map[string]string{"GOHORT_HOOK_PATH": "/run/gohort/h.sock"},
	}).Args, " ")

	for _, want := range []string{
		"--rm",                             // no container survives its command
		"--userns=keep-id",                 // podman: files come back owned by the daemon
		"--cap-drop ALL",                   // nothing the command could escalate with
		"--security-opt no-new-privileges", //
		"--read-only",                      // only the workspace and the tmpfs are writable
		"--network none",                   // AllowNetwork was false
		ws + ":" + ws + ":rw",              // workspace, same path inside as out
		"--workdir " + ws,
		"/run/gohort/h.sock:/run/gohort/h.sock", // the hook socket, by file
		"testimg sh -c ls",                      // image, then the command
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv is missing %q:\n%s", want, argv)
		}
	}
}

// Network is a promise in the other direction too: when the connector allows
// it, --network none must NOT be there, or every fetch inside a permitted run
// fails for a reason the model cannot see.
func TestNetworkIsOnlyCutWhenTheConnectorSaysSo(t *testing.T) {
	c := containerSandbox{path: "/usr/bin/podman", kind: "podman", image: "img"}
	argv := strings.Join(c.build(context.Background(), sandboxRun{
		Kind: sandboxShellRun, Command: "curl x", WorkspaceDir: t.TempDir(), AllowNetwork: true,
	}).Args, " ")
	if strings.Contains(argv, "--network none") {
		t.Errorf("a permitted run must keep its network:\n%s", argv)
	}
}

// docker has no keep-id, so it is told the uid outright. Getting this wrong
// leaves root-owned files in a workspace the daemon then cannot clean up.
func TestDockerIsGivenAUidBecauseItCannotMapOne(t *testing.T) {
	c := containerSandbox{path: "/usr/bin/docker", kind: "docker", image: "img"}
	argv := strings.Join(c.build(context.Background(), sandboxRun{
		Kind: sandboxShellRun, Command: "ls", WorkspaceDir: t.TempDir(),
	}).Args, " ")
	if strings.Contains(argv, "keep-id") {
		t.Error("keep-id is podman-only")
	}
	if !strings.Contains(argv, "--user ") {
		t.Errorf("docker must be given a uid:gid:\n%s", argv)
	}
}

// A container inherits nothing from the parent's environment, so the scrub that
// every other backend gets from sandboxEnv has to be re-applied as flags. The
// failure this guards is a daemon secret reaching an LLM-controlled shell.
func TestContainerEnvDropsTheDaemonsSecretsAndReplacesPATH(t *testing.T) {
	t.Setenv("SOME_PROVIDER_API_KEY", "sk-do-not-leak-this")
	env := containerEnv(map[string]string{"TOOL_ARG": "x"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "sk-do-not-leak-this") {
		t.Error("a daemon secret reached the container environment")
	}
	if !strings.Contains(joined, "TOOL_ARG=x") {
		t.Error("the caller's own variables must still arrive")
	}
	// The host's PATH describes the host's filesystem; inside the image those
	// directories are somebody else's or absent.
	var paths int
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			paths++
			if !strings.HasPrefix(kv, "PATH="+GohortBinMountPath+":") {
				t.Errorf("the shim dir must lead PATH, got %q", kv)
			}
		}
	}
	if paths != 1 {
		t.Errorf("expected exactly one PATH, got %d", paths)
	}
}

// The probe is the difference between "the binary is there" and "this works".
func TestTheProbeRejectsARuntimeThatCannotRun(t *testing.T) {
	c := containerSandbox{path: "/usr/bin/docker", kind: "docker", image: "img"}
	// Version works, running a container does not — the shape of a docker
	// install whose daemon socket refuses the connection.
	calls := 0
	runner := func(_ containerSandbox, args []string) (string, error) {
		calls++
		if len(args) > 0 && args[0] == "--version" {
			return "Docker version 26.1.3", nil
		}
		return "permission denied while trying to connect to the Docker daemon socket", errNotFound
	}
	ok, why := containerProbe(c, runner)
	if ok {
		t.Error("a runtime that cannot run a container must not be accepted")
	}
	if calls != 2 {
		t.Errorf("the probe should try both version and run, got %d calls", calls)
	}
	// The reason must name what FAILED, not what worked. The first version of
	// this logged the --version output in the message about the run failing, so
	// a docker whose socket refuses the connection diagnosed itself as
	// "Docker version 26.1.3" — the one thing that was fine.
	if !strings.Contains(why, "permission denied") {
		t.Errorf("the reason should carry the run failure, got: %s", why)
	}
	if strings.Contains(why, "26.1.3") {
		t.Errorf("the reason reported the version instead of the failure: %s", why)
	}
	// And an empty path is refused without running anything.
	if ok, _ := containerProbe(containerSandbox{kind: "podman"}, runner); ok {
		t.Error("an unlocated runtime must not be accepted")
	}
}

func TestTheProbeAcceptsAWorkingRuntime(t *testing.T) {
	c := containerSandbox{path: "/usr/bin/podman", kind: "podman", image: "img"}
	ok, why := containerProbe(c, func(_ containerSandbox, args []string) (string, error) {
		if len(args) > 0 && args[0] == "--version" {
			return "podman version 4.9.4", nil
		}
		return "Python 3.11.2", nil
	})
	if !ok {
		t.Errorf("a runtime that runs a container must be accepted: %s", why)
	}
}

// The Python skew is the known gap this backend ships with: the managed wheels
// are built against the HOST interpreter and mounted in, so a mismatched image
// breaks every compiled one. Parsing is asserted because the warning depends on
// it and a silent parse failure would mean no warning at all.
func TestPythonVersionParsing(t *testing.T) {
	for in, want := range map[string][2]int{
		"Python 3.11.2":   {3, 11},
		"Python 3.6.8\n":  {3, 6},
		"  Python 3.13.0": {3, 13},
	} {
		maj, min, ok := parsePythonVersion(in)
		if !ok || maj != want[0] || min != want[1] {
			t.Errorf("parsePythonVersion(%q) = %d.%d ok=%v, want %d.%d", in, maj, min, ok, want[0], want[1])
		}
	}
	for _, bad := range []string{"", "Python", "not a version", "Python three"} {
		if _, _, ok := parsePythonVersion(bad); ok {
			t.Errorf("parsePythonVersion(%q) should fail", bad)
		}
	}
}

// --- SELinux -----------------------------------------------------------------

// The bug this backend shipped with. Under SELinux enforcing a container runs
// as container_t and cannot read a bind mount carrying the host's own label, so
// the workspace reads as empty or permission-denied with nothing naming
// SELinux. It was invisible on the dev box (SELinux Disabled) and would have
// appeared on the first RHEL deployment, which is where gohort mostly runs.
func TestMountsCarryTheRelabelOptionWhenSELinuxEnforces(t *testing.T) {
	t.Setenv("GOHORT_SANDBOX_SELINUX_RELABEL", "on")
	resetRelabel(t)

	got := mount("/host/ws", "/host/ws", "rw")
	if len(got) != 2 || got[1] != "/host/ws:/host/ws:rw,z" {
		t.Errorf("a writable mount should relabel: %v", got)
	}
	// A read-only mount keeps ro AND gains the label; podman takes them
	// comma-separated in one option field.
	got = mount("/corpus", "/corpus", "ro")
	if got[1] != "/corpus:/corpus:ro,z" {
		t.Errorf("a read-only mount should relabel too: %v", got)
	}
	// The hook socket has no options of its own, so `z` must not arrive with a
	// leading comma — podman rejects ":,z".
	got = mount("/run/h.sock", "/run/h.sock", "")
	if got[1] != "/run/h.sock:/run/h.sock:z" {
		t.Errorf("an option-less mount should get a bare label: %v", got)
	}
}

// Lowercase z (shared), never uppercase Z (private to one container). These
// mounts outlive any single run — several containers touch one workspace in
// sequence — and a private label would have each one relabel the tree away from
// the last.
func TestRelabelIsSharedNotPrivate(t *testing.T) {
	t.Setenv("GOHORT_SANDBOX_SELINUX_RELABEL", "on")
	resetRelabel(t)
	if spec := mount("/ws", "/ws", "rw")[1]; strings.Contains(spec, ",Z") {
		t.Errorf("private relabeling would fight between runs: %q", spec)
	}
}

// Off is a real requirement, not decoration: relabeling REWRITES the host
// directory's label, and a scoped read-only corpus may be labeled for another
// service. An operator whose web root is httpd_sys_content_t needs to be able
// to refuse.
func TestRelabelCanBeRefused(t *testing.T) {
	t.Setenv("GOHORT_SANDBOX_SELINUX_RELABEL", "off")
	resetRelabel(t)
	if spec := mount("/ws", "/ws", "rw")[1]; strings.Contains(spec, "z") {
		t.Errorf("relabeling was disabled but happened anyway: %q", spec)
	}
}

// On a host with no SELinux, nothing is added — the flag would be a no-op on
// most kernels and an error on some.
func TestNoRelabelWithoutSELinux(t *testing.T) {
	t.Setenv("GOHORT_SANDBOX_SELINUX_RELABEL", "")
	resetRelabel(t)
	spec := mount("/ws", "/ws", "rw")[1]
	if SELinuxState() == "enforcing" {
		if !strings.HasSuffix(spec, ",z") {
			t.Errorf("an enforcing host must relabel: %q", spec)
		}
		return
	}
	if strings.Contains(spec, ",z") {
		t.Errorf("a host without enforcing SELinux must not relabel: %q", spec)
	}
}

// resetRelabel clears the sync.Once so each test decides for itself. Without
// it the first test to run would fix the answer for every one after it — the
// same trap seatbeltUsable documents.
func resetRelabel(t *testing.T) {
	t.Helper()
	relabelOnce = sync.Once{}
	relabelOK = false
	t.Cleanup(func() {
		relabelOnce = sync.Once{}
		relabelOK = false
	})
}
