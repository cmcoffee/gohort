package core

// A Bedrock credential failure has to say enough to tell "AWS refused" apart
// from "this process is not looking where you logged in". Those two produce
// the same non-zero exit and lead to opposite actions: go fix an SSO
// assignment, versus go look at which user the service runs as.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAnOldCLIIsRecognizedAsTooOldForExportCredentials(t *testing.T) {
	for _, tc := range []struct {
		ver string
		old bool
		why string
	}{
		{"aws-cli/2.4.18", true, "2.4 predates export-credentials"},
		{"aws-cli/1.29.0", true, "every v1 predates it"},
		{"aws-cli/2.12.9", true, "2.12 is the last release without it"},
		{"aws-cli/2.13.0", false, "2.13 is where it landed"},
		{"aws-cli/2.30.1", false, "anything newer has it"},
		{"aws-cli/3.0.0", false, "a future major is not older"},
		// An unparseable version must NOT read as too old. Guessing wrong here
		// sends somebody to upgrade a CLI that was never the problem, which is
		// the failure this whole diagnosis exists to prevent.
		{"aws-cli/experimental", false, "an unparseable version is not a verdict"},
		{"", false, "no version is not a verdict"},
	} {
		if got := awsCLITooOld(tc.ver); got != tc.old {
			t.Errorf("awsCLITooOld(%q) = %v, want %v: %s", tc.ver, got, tc.old, tc.why)
		}
	}
}

// The version is read off the banner, which v2 prints to stdout and some v1
// builds print to stderr. Missing it on the exact CLI whose age IS the problem
// would make the diagnosis worse than silence.
func TestTheCLIVersionIsReadFromEitherStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	for _, stream := range []string{"1", "2"} {
		bin := fakeAWS(t, "echo 'aws-cli/2.4.18 Python/3.8.8 Linux/x86_64' >&"+stream)
		got, ok := awsCLIVersion(bin)
		if !ok || got != "aws-cli/2.4.18" {
			t.Errorf("version off fd %s = %q (ok=%v), want aws-cli/2.4.18", stream, got, ok)
		}
	}
}

// A binary that prints no banner leaves the version unknown rather than
// inventing one, and the diagnosis still carries the facts that do not need it.
func TestAnUnreadableVersionStillDiagnoses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	bin := fakeAWS(t, "echo 'no banner here'")
	if _, ok := awsCLIVersion(bin); ok {
		t.Error("a binary with no banner reported a version")
	}
	d := awsCLIDiagnosis(bin)
	if !strings.Contains(d, "version unknown") {
		t.Errorf("the diagnosis hides that the version could not be read: %s", d)
	}
	if !strings.Contains(d, bin) {
		t.Errorf("the diagnosis does not name which binary was run: %s", d)
	}
}

// The three facts, and the right closing advice for each case.
func TestTheDiagnosisNamesBinaryUserAndHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("HOME", "/home/placeholder")

	old := awsCLIDiagnosis(fakeAWS(t, "echo 'aws-cli/2.4.18 Python/3.8.8 Linux/x86_64'"))
	if !strings.Contains(old, "aws-cli/2.4.18") {
		t.Errorf("the version is missing: %s", old)
	}
	if !strings.Contains(old, "/home/placeholder") {
		t.Errorf("HOME is missing, so nobody can see whose token cache was read: %s", old)
	}
	if !strings.Contains(old, "running as ") {
		t.Errorf("the effective user is missing: %s", old)
	}
	// Too old is a DIFFERENT instruction. Telling somebody to re-run
	// `aws sso login` when the subcommand does not exist in their CLI sends
	// them around a loop that cannot terminate.
	if !strings.Contains(old, "upgrade the CLI") {
		t.Errorf("an ancient CLI is not called out: %s", old)
	}
	if strings.Contains(old, "aws sso login") {
		t.Errorf("an ancient CLI is told to log in again, which cannot help: %s", old)
	}

	cur := awsCLIDiagnosis(fakeAWS(t, "echo 'aws-cli/2.31.0 Python/3.12.0 Linux/x86_64'"))
	if strings.Contains(cur, "upgrade the CLI") {
		t.Errorf("a current CLI is blamed for its age: %s", cur)
	}
	// The per-user token cache is the most common cause and the hardest to
	// see, because the operator's own login is genuinely fine.
	if !strings.Contains(cur, "per-user") || !strings.Contains(cur, "aws sso login") {
		t.Errorf("a current CLI does not get pointed at the per-user token cache: %s", cur)
	}
}

// fakeAWS writes an executable stub and returns its path. t.TempDir is removed
// with the test, so nothing lands in the tree.
func fakeAWS(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "aws")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
