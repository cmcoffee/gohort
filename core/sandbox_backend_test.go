package core

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

// TestTheRefusalNamesSomethingActionable — GOHORT_SANDBOX_REQUIRED is the
// fail-closed switch, and on a platform with no backend it does not harden
// anything; it makes shell tools unavailable. The error has to say so, and say
// how to get back, rather than repeating an impossible instruction.
func TestTheRefusalNamesSomethingActionable(t *testing.T) {
	msg := sandboxUnavailableErr().Error()
	if !strings.Contains(msg, "GOHORT_SANDBOX_REQUIRED") {
		t.Error("the refusal never names the switch that caused it")
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
func TestDarwinNeverSelectsBubblewrap(t *testing.T) {
	found := func(string) (string, error) { return "/opt/homebrew/bin/bwrap", nil }
	if sb := detectSandboxFor("darwin", found); sb.name() == "bubblewrap" {
		t.Error("darwin adopted a bwrap that happened to be on PATH")
	}
	if sb := detectSandboxFor("darwin", found); sb.confines() {
		t.Error("darwin reports itself confined")
	}
}

// TestSelectionPerPlatform — Linux takes bwrap when present and nothing when
// absent; an unknown platform takes nothing rather than guessing.
func TestSelectionPerPlatform(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/bwrap", nil }
	missing := func(string) (string, error) { return "", os.ErrNotExist }

	if sb := detectSandboxFor("linux", found); sb.name() != "bubblewrap" {
		t.Errorf("linux with bwrap chose %q", sb.name())
	}
	if sb := detectSandboxFor("linux", missing); sb.confines() {
		t.Error("linux without bwrap reports itself confined")
	}
	if sb := detectSandboxFor("windows", found); sb.confines() {
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
	if !strings.Contains(mac, "GOHORT_SANDBOX_REQUIRED") {
		t.Errorf("macOS advice offers no way to fail closed: %q", mac)
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
	if got, want := st.Required, sandboxRequired(); got != want {
		t.Errorf("status Required=%v, env says %v", got, want)
	}
}

// TestRequiredIsReadFromTheEnvironment — the fail-closed switch has to be
// honored in both directions, since one direction refuses work.
func TestRequiredIsReadFromTheEnvironment(t *testing.T) {
	prev, had := os.LookupEnv("GOHORT_SANDBOX_REQUIRED")
	t.Cleanup(func() {
		if had {
			os.Setenv("GOHORT_SANDBOX_REQUIRED", prev)
			return
		}
		os.Unsetenv("GOHORT_SANDBOX_REQUIRED")
	})
	for _, on := range []string{"1", "true", "yes", "on", "TRUE"} {
		os.Setenv("GOHORT_SANDBOX_REQUIRED", on)
		if !sandboxRequired() {
			t.Errorf("%q did not enable fail-closed sandboxing", on)
		}
	}
	for _, off := range []string{"", "0", "false", "no"} {
		os.Setenv("GOHORT_SANDBOX_REQUIRED", off)
		if sandboxRequired() {
			t.Errorf("%q enabled fail-closed sandboxing", off)
		}
	}
}
