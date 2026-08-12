package deps

import (
	"runtime"
	"strings"
	"testing"
)

// A dependency row that can never be satisfied on this platform is worse than
// no row: it is a permanent failure against a package the operator cannot
// obtain, and it crowds out the thing that actually does the job.

// TestNoRowAsksForAnImpossiblePackage — every row must either be satisfiable
// here or say plainly that nothing needs installing.
func TestNoRowAsksForAnImpossiblePackage(t *testing.T) {
	for _, d := range knownDependencies {
		hint := d.hint
		if hint == "" {
			hint = dependencyInstallHint(d.apt, d.brew)
		}
		if runtime.GOOS == "darwin" && strings.Contains(hint, "apt install") {
			t.Errorf("row %q tells macOS to run apt: %q", d.name, hint)
		}
		if runtime.GOOS != "darwin" && strings.Contains(hint, "brew install") {
			t.Errorf("row %q tells %s to run brew: %q", d.name, runtime.GOOS, hint)
		}
	}
}

// TestTheSandboxRowNamesThisPlatformsSandbox — bubblewrap is Linux-only and
// Seatbelt is macOS-only; neither can be installed to satisfy the other. A
// single bwrap row showed every Mac a permanent red mark for a package that
// does not exist there, while /usr/bin/sandbox-exec went unmentioned.
func TestTheSandboxRowNamesThisPlatformsSandbox(t *testing.T) {
	var names []string
	for _, d := range knownDependencies {
		names = append(names, d.name)
	}
	joined := strings.Join(names, ",")
	if runtime.GOOS == "darwin" {
		if strings.Contains(joined, "bwrap") {
			t.Error("macOS still lists bwrap, which can never be installed there")
		}
		if !strings.Contains(joined, "sandbox-exec") {
			t.Error("macOS does not list sandbox-exec, the tool that actually confines there")
		}
		return
	}
	if !strings.Contains(joined, "bwrap") {
		t.Errorf("%s does not list bwrap", runtime.GOOS)
	}
	if strings.Contains(joined, "sandbox-exec") {
		t.Errorf("%s lists sandbox-exec, which is macOS-only", runtime.GOOS)
	}
}

// TestTheLegacyDocRowNamesThisPlatformsConverter — same rule, the row that
// prompted it: Homebrew dropped antiword, and macOS has textutil built in.
func TestTheLegacyDocRowNamesThisPlatformsConverter(t *testing.T) {
	var names []string
	for _, d := range knownDependencies {
		names = append(names, d.name)
	}
	joined := strings.Join(names, ",")
	if runtime.GOOS == "darwin" {
		if strings.Contains(joined, "antiword") {
			t.Error("macOS still lists antiword, which Homebrew no longer ships")
		}
		if !strings.Contains(joined, "textutil") {
			t.Error("macOS does not list textutil, which reads .doc there with no install")
		}
		return
	}
	if !strings.Contains(joined, "antiword") {
		t.Errorf("%s does not list antiword", runtime.GOOS)
	}
}

// TestEveryRowSaysWhatItEnables — a name with no explanation is a row nobody
// can act on.
func TestEveryRowSaysWhatItEnables(t *testing.T) {
	for _, d := range knownDependencies {
		if strings.TrimSpace(d.name) == "" {
			t.Error("a dependency row has no name")
		}
		if strings.TrimSpace(d.enables) == "" {
			t.Errorf("row %q does not say what it enables", d.name)
		}
	}
}
