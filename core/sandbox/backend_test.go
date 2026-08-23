package sandbox

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestNoBackendIsEverSilent — the defect this split exists for. On macOS bwrap
// can never be installed, so the old code's "not installed" fallback was
// indistinguishable from a Linux host missing a package, and it advised
// `apt install bubblewrap` on a platform where that is impossible. Whatever
// this host is, the advice has to be something its operator can act on.
func TestNoBackendIsEverSilent(t *testing.T) {
	advice := unsandboxedAdvice()
	if strings.TrimSpace(advice) == "" {
		t.Fatal("an unsandboxed host is given no advice at all")
	}
	if runtime.GOOS == "darwin" && strings.Contains(strings.ToLower(advice), "install bubblewrap") {
		t.Error("macOS is told to install bubblewrap, which cannot be installed there")
	}
	if runtime.GOOS != "linux" && strings.Contains(advice, "apt install") {
		t.Errorf("a non-Linux host is given a Linux package command: %q", advice)
	}
}

// TestTheRefusalNamesSomethingActionable — on a platform with no backend the
// fail-closed default does not harden anything; it makes shell tools
// unavailable. The error has to say so, and say how to get back, rather than
// repeating an impossible instruction.
//
// It names the OPT-OUT, not the old opt-in: an operator reading this is being
// refused BY the default, so the actionable sentence is how to permit what was
// refused. Pointing at GOHORT_SANDBOX_REQUIRED would name a switch they never
// set and cannot usefully unset.
func TestTheRefusalNamesSomethingActionable(t *testing.T) {
	msg := sandboxUnavailableErr().Error()
	if !strings.Contains(msg, "GOHORT_ALLOW_UNSANDBOXED") {
		t.Error("the refusal never names the way out of it")
	}
	if !strings.Contains(msg, unsandboxedAdvice()) {
		t.Errorf("the refusal does not carry this platform's advice: %q", msg)
	}
	if runtime.GOOS == "darwin" && strings.Contains(strings.ToLower(msg), "install bubblewrap") {
		t.Errorf("macOS refusal tells the operator to install bubblewrap: %q", msg)
	}
}

// TestDarwinNeverSelectsBubblewrap — a bwrap on PATH under darwin (a cross
// build, a stray shell script, a wrapper that shells out to a VM) must not be
// adopted. Selection is per-platform on purpose, and this runs on the Linux
// suite rather than only on a Mac nobody is testing.
// probeFails stands in for a host where Seatbelt is unusable.
func probeFails(string) bool { return false }

func TestDarwinNeverSelectsBubblewrap(t *testing.T) {
	found := func(string) (string, error) { return "/opt/homebrew/bin/bwrap", nil }
	if sb := detectSandboxFor("darwin", found, probeFails); sb.name() == "bubblewrap" {
		t.Error("darwin adopted a bwrap that happened to be on PATH")
	}
	if sb := detectSandboxFor("darwin", found, probeFails); sb.confines() {
		t.Error("darwin reports itself confined")
	}
}

// TestSelectionPerPlatform — Linux takes bwrap when present and nothing when
// absent; an unknown platform takes nothing rather than guessing.
func TestSelectionPerPlatform(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/bwrap", nil }
	missing := func(string) (string, error) { return "", os.ErrNotExist }

	if sb := detectSandboxFor("linux", found, probeFails); sb.name() != "bubblewrap" {
		t.Errorf("linux with bwrap chose %q", sb.name())
	}
	if sb := detectSandboxFor("linux", missing, probeFails); sb.confines() {
		t.Error("linux without bwrap reports itself confined")
	}
	if sb := detectSandboxFor("windows", found, probeFails); sb.confines() {
		t.Error("an unsupported platform adopted a backend")
	}
}

// TestEveryPlatformGetsUsableAdvice — the message an operator reads when their
// host is unconfined. Checked for all three branches here, because the darwin
// one runs where this suite does not.
func TestEveryPlatformGetsUsableAdvice(t *testing.T) {
	linux := unsandboxedAdviceFor("linux")
	if !strings.Contains(linux, "bubblewrap") {
		t.Errorf("linux advice does not name bubblewrap: %q", linux)
	}

	mac := unsandboxedAdviceFor("darwin")
	if strings.Contains(strings.ToLower(mac), "install bubblewrap") {
		t.Errorf("macOS is told to install bubblewrap: %q", mac)
	}
	if !strings.Contains(mac, "macOS") {
		t.Errorf("macOS advice never says which platform it is about: %q", mac)
	}
	// It has to leave the operator somewhere to go, not just state a lack.
	// On macOS that road cannot be "install a package", so it is the opt-out:
	// this is the one platform where accepting the risk may be the only move.
	if !strings.Contains(mac, "GOHORT_ALLOW_UNSANDBOXED") {
		t.Errorf("macOS advice offers no way forward at all: %q", mac)
	}

	other := unsandboxedAdviceFor("windows")
	if !strings.Contains(other, "windows") {
		t.Errorf("an unknown platform is not named in its own advice: %q", other)
	}
	if strings.Contains(other, "apt install") {
		t.Errorf("an unknown platform is given a Linux package command: %q", other)
	}
}

// TestRemapsPathsTracksTheBackend — the property every path helper keys on.
// A backend that does NOT remap must report so, or PYTHONPATH and the shim bin
// dir point at mount paths that exist nowhere and every hook-using script dies
// on its first line.
func TestRemapsPathsTracksTheBackend(t *testing.T) {
	if !(bwrapSandbox{path: "/usr/bin/bwrap"}).remapsPaths() {
		t.Error("bubblewrap builds a mount namespace but reports no remapping")
	}
	if (noSandbox{}).remapsPaths() {
		t.Error("the none backend reports remapping — PYTHONPATH would point at unmounted paths")
	}
	if (noSandbox{}).confines() {
		t.Error("the none backend claims to confine")
	}
	if !(bwrapSandbox{}).confines() {
		t.Error("bubblewrap does not claim to confine")
	}
}

// TestScopesReadsIsAskedSeparately — the three backends' answers, asserted here
// so a fourth cannot inherit a default.
//
// It is a SEPARATE property from confines() because they diverge: Seatbelt
// confines and cannot scope a read (see sandbox_seatbelt.go). Anything that
// gates a read-only path list on confines() is asking the wrong question, and
// the cost of that mistake is a check that passes while constraining nothing.
func TestScopesReadsIsAskedSeparately(t *testing.T) {
	if !(bwrapSandbox{path: "/usr/bin/bwrap"}).scopesReads() {
		t.Error("bubblewrap binds read-only paths into a mount namespace but reports it cannot scope reads")
	}
	if (noSandbox{}).scopesReads() {
		t.Error("the none backend claims to scope reads — a path_scope would be checked and then ignored")
	}
	if (seatbeltSandbox{path: seatbeltBinary}).scopesReads() {
		t.Error("seatbelt claims to scope reads, but its profile allows file-read* filesystem-wide")
	}
	// The divergence itself, stated as an assertion rather than left in a
	// comment: this pairing is the whole reason the predicate exists.
	if sb := (seatbeltSandbox{path: seatbeltBinary}); !sb.confines() || sb.scopesReads() {
		t.Errorf("seatbelt must be confines=true scopesReads=false, got %v/%v", sb.confines(), sb.scopesReads())
	}
}

// TestTheNoneBackendKeepsTheOldShapes — behavior on an unsandboxed host must be
// exactly what it was before the split: a shell run in the workspace, a pipe in
// /tmp, a script through its interpreter.
func TestTheNoneBackendKeepsTheOldShapes(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	c := noSandbox{}.build(ctx, sandboxRun{Kind: sandboxShellRun, Command: "echo hi", WorkspaceDir: ws})
	if c.Dir != ws {
		t.Errorf("shell run cwd = %q, want the workspace", c.Dir)
	}
	if len(c.Args) < 3 || c.Args[1] != "-c" || c.Args[2] != "echo hi" {
		t.Errorf("shell argv = %v", c.Args)
	}

	c = noSandbox{}.build(ctx, sandboxRun{Kind: sandboxPipeRun, Command: "cat"})
	if c.Dir != "/tmp" {
		t.Errorf("pipe run cwd = %q, want /tmp — a pipe has no workspace and must not "+
			"inherit gohort's own directory", c.Dir)
	}

	c = noSandbox{}.build(ctx, sandboxRun{Kind: sandboxScriptRun, Interpreter: "python3", Command: "print(1)"})
	if len(c.Args) < 3 || !strings.HasSuffix(c.Args[0], "python3") || c.Args[1] != "-c" || c.Args[2] != "print(1)" {
		t.Errorf("script argv = %v", c.Args)
	}
}

// TestBwrapBackendDelegatesToTheRightArgv — each run shape reaches its own argv
// builder. They differ in what the command may touch, so a shape wired to the
// wrong builder would hand a data-transform pipe a writable workspace.
func TestBwrapBackendDelegatesToTheRightArgv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bwrap argv builders are Linux-shaped")
	}
	ctx := context.Background()
	sb := bwrapSandbox{path: "/usr/bin/bwrap"}
	ws := t.TempDir()

	shell := sb.build(ctx, sandboxRun{Kind: sandboxShellRun, Command: "true", WorkspaceDir: ws, AllowNetwork: true})
	if !strings.Contains(strings.Join(shell.Args, " "), ws) {
		t.Error("the shell shape did not bind the workspace")
	}
	pipe := sb.build(ctx, sandboxRun{Kind: sandboxPipeRun, Command: "cat"})
	if strings.Contains(strings.Join(pipe.Args, " "), ws) {
		t.Error("the pipe shape bound a workspace it should never see")
	}
}

// TestStatusReportsTheTruth — a log line at first use is not a surface anyone
// checks, and "every shell command here runs unconfined" is exactly the fact an
// operator needs without having to go looking.
func TestStatusReportsTheTruth(t *testing.T) {
	st := GetSandboxStatus()
	if st.Backend == "" {
		t.Error("status names no backend")
	}
	if st.Confined && st.Advice != "" {
		t.Errorf("a confined host is still being given remediation advice: %q", st.Advice)
	}
	if !st.Confined && st.Advice == "" {
		t.Error("an unconfined host is given no advice")
	}
	if st.Confined != (st.Backend != "none") {
		t.Errorf("backend %q and confined=%v disagree", st.Backend, st.Confined)
	}
	if got, want := st.Required, sandboxRequired(context.Background()); got != want {
		t.Errorf("status Required=%v, env says %v", got, want)
	}
}

// restoreEnv snapshots the two sandbox switches so a case can set either one
// without leaking into the next.
func restoreEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		name := name
		prev, had := os.LookupEnv(name)
		t.Cleanup(func() {
			if had {
				os.Setenv(name, prev)
				return
			}
			os.Unsetenv(name)
		})
		os.Unsetenv(name)
	}
}

// TestConfinementIsRequiredByDefault is the inversion itself, and the single
// most important assertion in this file. The old default ran every LLM-issued
// shell command unconfined on a host with no backend, and an operator who never
// heard of the env var got the dangerous answer silently.
func TestConfinementIsRequiredByDefault(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	if !sandboxRequired(context.Background()) {
		t.Fatal("with nothing set, an unconfinable run is permitted — the default must fail closed")
	}
}

// TestUnsandboxedExecutionIsOptIn — the escape hatch has to work, or the flip
// is a wall rather than a default. Both spellings, since a deployment carrying
// the old variable must keep the behavior it asked for.
func TestUnsandboxedExecutionIsOptIn(t *testing.T) {
	for _, on := range []string{"1", "true", "yes", "on", "TRUE"} {
		restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
		os.Setenv("GOHORT_ALLOW_UNSANDBOXED", on)
		if sandboxRequired(context.Background()) {
			t.Errorf("GOHORT_ALLOW_UNSANDBOXED=%q did not permit unconfined execution", on)
		}
	}
	for _, off := range []string{"0", "false", "no", "off"} {
		restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
		os.Setenv("GOHORT_SANDBOX_REQUIRED", off)
		if sandboxRequired(context.Background()) {
			t.Errorf("GOHORT_SANDBOX_REQUIRED=%q did not permit unconfined execution", off)
		}
	}
}

// TestExplicitlyRequiredStillWins — a deployment that set the old opt-in keeps
// meaning it, and a contradictory pair resolves the safe way. A config saying
// both things is a mistake; the safe reading of a mistake is the confining one.
func TestExplicitlyRequiredStillWins(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	os.Setenv("GOHORT_SANDBOX_REQUIRED", "1")
	if !sandboxRequired(context.Background()) {
		t.Fatal("GOHORT_SANDBOX_REQUIRED=1 stopped meaning what it always meant")
	}
	os.Setenv("GOHORT_ALLOW_UNSANDBOXED", "1")
	if !sandboxRequired(context.Background()) {
		t.Fatal("a contradictory pair resolved to the permissive reading")
	}
}

// TestRefusingSeparatesTwoOppositeDiagnoses — since the default flipped,
// "not confined" no longer tells an operator whether anything runs. Refusing
// does, and the two states demand opposite fixes.
func TestRefusingSeparatesTwoOppositeDiagnoses(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	st := GetSandboxStatus()
	if st.Confined && st.Refusing {
		t.Error("a confined host reports that it is refusing tools")
	}
	if !st.Confined && !st.Refusing {
		t.Error("an unconfined host with the default policy should be refusing")
	}

	os.Setenv("GOHORT_ALLOW_UNSANDBOXED", "1")
	if GetSandboxStatus().Refusing {
		t.Error("a host that opted into unconfined execution reports refusing")
	}
}

// TestAdminOnlyBypassSplitsCallerFromDeployment is the reason the bypass is a
// tri-state rather than a boolean. On a host that cannot confine, the operator
// wants their own shell tools back WITHOUT handing unconfined execution to
// every agent, schedule and channel wake on the box.
func TestAdminOnlyBypassSplitsCallerFromDeployment(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	os.Setenv("GOHORT_ALLOW_UNSANDBOXED", "admin")

	if sandboxRequired(WithAdminCaller(context.Background(), true)) {
		t.Error("an admin's own run is still refused under the admin bypass")
	}
	if !sandboxRequired(context.Background()) {
		t.Error("an unstamped run (schedule, wake, monitor) was allowed to run unconfined")
	}
	if !sandboxRequired(WithAdminCaller(context.Background(), false)) {
		t.Error("a non-admin caller was allowed to run unconfined")
	}
}

// TestAdminStampIsInertUnlessTheDeploymentAsked — stamping a caller as admin
// must not, by itself, weaken anything. The deployment decides first.
func TestAdminStampIsInertUnlessTheDeploymentAsked(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	admin := WithAdminCaller(context.Background(), true)
	if !sandboxRequired(admin) {
		t.Fatal("an admin bypassed confinement on a deployment that never enabled a bypass")
	}
	os.Setenv("GOHORT_ALLOW_UNSANDBOXED", "off")
	if !sandboxRequired(admin) {
		t.Fatal("an admin bypassed confinement with the bypass explicitly off")
	}
}

// TestATypoIsNotThePermissiveAnswer — an unrecognized value in a security
// switch has exactly one safe reading.
func TestATypoIsNotThePermissiveAnswer(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	for _, junk := range []string{"yes-please", "ADMINS-ONLY-PLZ", "true(ish)", " "} {
		os.Setenv("GOHORT_ALLOW_UNSANDBOXED", junk)
		if !sandboxRequired(WithAdminCaller(context.Background(), true)) {
			t.Errorf("%q was read as permission to run unconfined", junk)
		}
	}
}

// TestBypassIsReportedToTheOperator — the panel has to name which of the three
// settings is live, since "required: false" alone cannot distinguish "everyone
// may" from "admins may".
func TestBypassIsReportedToTheOperator(t *testing.T) {
	restoreEnv(t, "GOHORT_SANDBOX_REQUIRED", "GOHORT_ALLOW_UNSANDBOXED")
	for env, want := range map[string]string{"": "off", "on": "on", "admin": "admin", "off": "off"} {
		if env == "" {
			os.Unsetenv("GOHORT_ALLOW_UNSANDBOXED")
		} else {
			os.Setenv("GOHORT_ALLOW_UNSANDBOXED", env)
		}
		if got := GetSandboxStatus().Bypass; got != want {
			t.Errorf("GOHORT_ALLOW_UNSANDBOXED=%q reported as %q, want %q", env, got, want)
		}
	}
}
