package sandbox

// Confinement is about blast radius; limits are about blast duration.
//
// The interesting cases are the ones where a wrong answer is invisible: a
// prefix that a shell silently ignores, a default that quietly breaks ffmpeg,
// and a typo in a limit that resolves to "unlimited". The last test actually
// runs a command against a real limit, because everything above it only proves
// we generated a string.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimitsDefaultToWhatNoRealWorkloadNeeds(t *testing.T) {
	for _, k := range []string{
		"GOHORT_SANDBOX_MAX_FILE_MB", "GOHORT_SANDBOX_MAX_OPEN_FILES",
		"GOHORT_SANDBOX_MAX_CPU_SEC", "GOHORT_SANDBOX_MAX_MEM_MB", "GOHORT_SANDBOX_MAX_PROCS",
	} {
		t.Setenv(k, "")
	}
	l := resourceLimits()
	if l.FileSizeMB != defaultFileSizeMB || l.OpenFiles != defaultOpenFiles {
		t.Errorf("file size and fd table are capped by default, got %+v", l)
	}
	// CPU, memory and process count stay OFF, and each has a specific reason
	// recorded in limits.go. tools/video/transcode.go runs ffmpeg through
	// RunSandboxedShell on a five-minute wall clock, which on a many-core host
	// is a large number of CPU-seconds; RLIMIT_AS kills runtimes that reserve
	// address space they never commit; RLIMIT_NPROC is counted per-UID and so
	// a low value starves the DAEMON rather than the command. A default that
	// broke any of those would surface weeks later as "video tools stopped
	// working", with no reason to suspect a sandbox setting.
	if l.CPUSeconds != 0 || l.MemoryMB != 0 || l.MaxProcs != 0 {
		t.Errorf("cpu/mem/procs must stay opt-in, got %+v", l)
	}
}

func TestAnUnparseableLimitTakesTheDefaultRatherThanUnlimited(t *testing.T) {
	// Same rule bypassPolicy applies to a typo in a security switch: a mistake
	// must not resolve to the permissive answer.
	t.Setenv("GOHORT_SANDBOX_MAX_FILE_MB", "1gb")
	if got := resourceLimits().FileSizeMB; got != defaultFileSizeMB {
		t.Errorf("a malformed limit should fall back to the default, got %d", got)
	}
	t.Setenv("GOHORT_SANDBOX_MAX_FILE_MB", "-5")
	if got := resourceLimits().FileSizeMB; got != defaultFileSizeMB {
		t.Errorf("a negative limit should fall back to the default, got %d", got)
	}
	// But an explicit switch-off is a legitimate thing to want, and must be
	// distinguishable from a typo. A deployment writing a file bigger than the
	// default needs a way to say so without disabling the other four.
	for _, off := range []string{"0", "none", "unlimited", "off"} {
		t.Setenv("GOHORT_SANDBOX_MAX_FILE_MB", off)
		if got := resourceLimits().FileSizeMB; got != 0 {
			t.Errorf("%q should switch the limit off, got %d", off, got)
		}
	}
}

func TestTheLimitPrefixIsAbsentWhenNothingIsLimited(t *testing.T) {
	// A no-op prefix would show up in the argv of every command on a
	// deployment that caps nothing, which is noise in exactly the logs
	// somebody reads while debugging something else.
	if got := (Limits{}).apply("ls -la"); got != "ls -la" {
		t.Errorf("an empty ceiling must not touch the command, got %q", got)
	}
}

func TestTheLimitPrefixDoesNotChangeWhatACompoundCommandMeans(t *testing.T) {
	got := Limits{FileSizeMB: 1}.apply("build && test")
	// No `exec`: `exec build && test` would replace the shell with build and
	// never run test. The command is appended verbatim.
	if strings.Contains(got, "exec ") {
		t.Errorf("exec would break a compound command: %q", got)
	}
	if !strings.HasSuffix(got, "build && test") {
		t.Errorf("the command must be appended unchanged, got %q", got)
	}
	// stderr discarded so a shell that rejects one option does not print a
	// diagnostic into output the LLM reads back as the tool's result.
	if !strings.Contains(got, "2>/dev/null") {
		t.Errorf("ulimit noise must not reach the command's output: %q", got)
	}
}

// A script run has no shell to run ulimit in — its Command is the script BODY.
// Prefixing it would be a syntax error inside the interpreter, which would
// break every export generator and event-monitor evaluator at once.
func TestAScriptRunIsNeverPrefixed(t *testing.T) {
	t.Setenv("GOHORT_SANDBOX_MAX_FILE_MB", "64")
	script := "import sys\nprint('hi')\n"
	c := buildRun(context.Background(), noSandbox{}, sandboxRun{
		Kind: sandboxScriptRun, Interpreter: "python3", Command: script,
	})
	for _, a := range c.Args {
		if strings.Contains(a, "ulimit") {
			t.Fatalf("a script body must not be prefixed: %q", a)
		}
	}
}

// The one test that proves the mechanism rather than the string: run a real
// command against a real limit and check the kernel stopped it.
func TestAFileSizeLimitActuallyStopsARunawayWrite(t *testing.T) {
	sb := activeSandbox()
	if !sb.confines() {
		// Deliberately not running this unconfined: it would need the bypass
		// switched on, and a test that flips a security default to pass is a
		// test that will one day flip it for everyone.
		t.Skip("no confining backend on this host")
	}
	t.Setenv("GOHORT_SANDBOX_MAX_FILE_MB", "1")

	ws := limitTestWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*1e9)
	defer cancel()
	// Ask for 8 MiB against a 1 MB ceiling. `yes` would work too but bounds
	// nothing if the limit fails to apply, and a test that hangs on failure
	// tells you less than one that fails.
	res := RunSandboxedShell(ctx, "dd if=/dev/zero of=big.bin bs=1M count=8 2>&1; echo rc=$?", ws)
	if res.TimedOut {
		t.Fatal("the write should have been stopped by the limit, not the deadline")
	}
	if strings.Contains(res.Output, "rc=0") {
		t.Errorf("an 8MiB write must not succeed under a 1MB cap:\n%s", res.Output)
	}
	if fi, err := os.Stat(filepath.Join(ws, "big.bin")); err == nil && fi.Size() > 4<<20 {
		t.Errorf("the file grew past the cap: %d bytes", fi.Size())
	}
}

// limitTestWorkspace makes a workspace OUTSIDE /tmp.
//
// t.TempDir() is the obvious choice and it is wrong here: bwrapArgv mounts
// `--tmpfs /tmp` after binding the workspace, so a workspace under /tmp is
// covered by the tmpfs and bwrap dies with "Can't chdir". That failure looks
// like the limit working — the command never runs, so it never writes 8MiB and
// never prints rc=0 — which is how the first version of this test passed while
// proving nothing. Production workspaces live under WorkspacesDir in the gohort
// data directory, not /tmp, so this is a property of the test environment
// rather than a bug in the argv.
func limitTestWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "gohort-limits-")
	if err != nil {
		t.Skipf("no writable dir outside /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
