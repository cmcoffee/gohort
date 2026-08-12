package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The profile is the ENTIRE security boundary on macOS, it is written in a
// language with no public specification, and it executes on a machine this
// suite never runs on. Generation is therefore a pure function, and these tests
// are the only place its rules get checked before they are law on somebody's
// laptop.

// TestAPathCannotEscapeTheProfile — the injection point.
//
// A profile is passed to sandbox-exec as ONE argv string. A workspace path
// holding a quote would terminate the string and let the remainder parse as
// policy, so a directory named `"))(allow default)(deny nothing` would switch
// the sandbox off from inside the thing it is meant to confine. Paths are
// gohort-generated today; this has to keep holding when they stop being.
func TestAPathCannotEscapeTheProfile(t *testing.T) {
	evil := `/ws/"))(allow default)(deny nothing`
	profile := seatbeltProfile(seatbeltSpec{Workspace: evil})

	// Checked against the profile with every quoted STRING removed. The crafted
	// text does appear verbatim — inside a string literal, where it is inert —
	// so searching the raw profile would flag correct escaping as a breakout.
	// What must not exist is a rule outside any string.
	if code := stripSBStrings(profile); strings.Contains(code, "(allow default)") {
		t.Fatalf("a crafted path turned the sandbox off; profile outside string literals:\n%s", code)
	}
	// The quote must arrive escaped, not dropped — dropping it would silently
	// grant a DIFFERENT directory than the one asked for.
	if !strings.Contains(profile, `\"`) {
		t.Errorf("the embedded quote was not escaped:\n%s", profile)
	}
	// Every rule must still be one line: a newline in a path would split a rule
	// and leave a fragment parsing as its own.
	for _, in := range []string{"/ws/a\nb", "/ws/a\rb"} {
		got := sbString(in)
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("sbString(%q) emitted a raw newline: %q", in, got)
		}
	}
	// Backslashes escape too, or a trailing one would escape the closing quote.
	if got := sbString(`/ws/back\`); got != `"/ws/back\\"` {
		t.Errorf("sbString did not escape a trailing backslash: %s", got)
	}
}

// TestTheProfileDeniesByDefault — an allowlist that forgot to deny is not an
// allowlist. Cheap to check, catastrophic to omit.
func TestTheProfileDeniesByDefault(t *testing.T) {
	p := seatbeltProfile(seatbeltSpec{Workspace: "/ws"})
	if !strings.HasPrefix(p, "(version 1)\n(deny default)\n") {
		t.Fatalf("the profile does not open by denying everything:\n%s", p)
	}
}

// TestOnlyTheWorkspaceIsWritable — the property the whole sandbox exists for.
// A command may write its working directory and scratch space, and nothing
// else; the user's home, their keys and the gohort install stay read-only at
// best.
func TestOnlyTheWorkspaceIsWritable(t *testing.T) {
	p := seatbeltProfile(seatbeltSpec{Workspace: "/ws/agent-1"})
	writeBlock := sectionAfter(t, p, "(allow file-read* file-write*")

	if !strings.Contains(writeBlock, `"/ws/agent-1"`) {
		t.Errorf("the workspace is not writable:\n%s", writeBlock)
	}
	for _, forbidden := range []string{`"/usr"`, `"/System"`, `"/Library"`, `"/etc"`, `"/opt/homebrew"`} {
		if strings.Contains(writeBlock, forbidden) {
			t.Errorf("%s appears in the WRITABLE set:\n%s", forbidden, writeBlock)
		}
	}
	// And a home directory is never granted, by either rule.
	if strings.Contains(p, `"/Users"`) {
		t.Errorf("the profile grants access under /Users:\n%s", p)
	}
}

// TestSystemPathsAreReadableAndExecutable — including the Homebrew prefixes.
// On macOS the interpreters actually in use (python3, bash 5, coreutils) are
// usually Homebrew's; omitting them gives "command not found" for the tooling
// every script assumes, which reads as a broken sandbox rather than a missing
// path.
func TestSystemPathsAreReadableAndExecutable(t *testing.T) {
	p := seatbeltProfile(seatbeltSpec{Workspace: "/ws"})
	readBlock := sectionAfter(t, p, "(allow file-read*\n")
	execBlock := sectionAfter(t, p, "(allow process-exec")
	for _, want := range []string{"/usr", "/bin", "/System", "/Library", "/opt/homebrew", "/usr/local"} {
		if !strings.Contains(readBlock, `"`+want+`"`) {
			t.Errorf("%s is not readable:\n%s", want, readBlock)
		}
		// Readable but not executable would break every interpreter on the box,
		// which reads as a broken sandbox rather than a missing rule.
		if !strings.Contains(execBlock, `"`+want+`"`) {
			t.Errorf("%s is readable but not executable:\n%s", want, execBlock)
		}
	}
}

// TestNetworkFollowsTheCaller — the privacy-mode guarantee. A blocked
// connector means outbound must not be granted, or a private turn's data
// leaves the machine from inside the sandbox meant to prevent exactly that.
func TestNetworkFollowsTheCaller(t *testing.T) {
	if on := seatbeltProfile(seatbeltSpec{Workspace: "/ws", AllowNetwork: true}); !strings.Contains(on, "(allow network*)") {
		t.Error("an allowed connector did not grant network")
	}
	off := seatbeltProfile(seatbeltSpec{Workspace: "/ws", AllowNetwork: false})
	if strings.Contains(off, "(allow network*)") {
		t.Errorf("a blocked connector still granted network:\n%s", off)
	}
}

// TestTheHookStaysReachableWithNetworkDenied — the hook is HOW a sandboxed
// script performs the calls it may not make itself, under capability checks on
// the host side. A unix socket connect is network-outbound in SBPL, so denying
// outbound wholesale would sever it and every `from gohort import fetch` would
// fail on a run that is supposed to support it.
func TestTheHookStaysReachableWithNetworkDenied(t *testing.T) {
	sock := "/var/run/gohort/h1.sock"
	p := seatbeltProfile(seatbeltSpec{Workspace: "/ws", AllowNetwork: false, HookSocket: sock})

	if !strings.Contains(p, `(allow network-outbound (literal "`+sock+`"))`) {
		t.Errorf("the hook socket is not connectable with network denied:\n%s", p)
	}
	if !strings.Contains(p, `(allow file-read* file-write* (literal "`+sock+`"))`) {
		t.Errorf("the hook socket file is not accessible:\n%s", p)
	}
	// Named as a literal, never a subpath — a subpath rule on its directory
	// would expose every OTHER agent's socket in the same directory.
	if strings.Contains(p, `(subpath "/var/run/gohort")`) {
		t.Errorf("the hook's DIRECTORY was granted, exposing other agents' sockets:\n%s", p)
	}
}

// TestDataShapesGetNoWorkspaceAndNoNetwork — pipe and script runs are pure
// transformation. bwrapPipeArgv and bwrapScriptArgv both pass --unshare-net
// unconditionally and bind no workspace; the macOS backend has to match, or the
// same tool is meaningfully less confined depending on which machine ran it.
func TestDataShapesGetNoWorkspaceAndNoNetwork(t *testing.T) {
	sb := seatbeltSandbox{path: "/usr/bin/sandbox-exec"}
	ctx := context.Background()

	for _, kind := range []sandboxRunKind{sandboxPipeRun, sandboxScriptRun} {
		c := sb.build(ctx, sandboxRun{
			Kind: kind, Command: "cat", Interpreter: "python3",
			WorkspaceDir: "/ws/secret", AllowNetwork: true,
			Env: map[string]string{"GOHORT_HOOK_PATH": "/var/run/h.sock"},
		})
		profile := profileArg(t, c.Args)
		if strings.Contains(profile, "/ws/secret") {
			t.Errorf("kind %v was given a writable workspace:\n%s", kind, profile)
		}
		if strings.Contains(profile, "(allow network*)") {
			t.Errorf("kind %v was granted network despite being a data transform", kind)
		}
		if strings.Contains(profile, "/var/run/h.sock") {
			t.Errorf("kind %v was given the hook socket", kind)
		}
		if c.Dir != "/tmp" {
			t.Errorf("kind %v runs in %q, want /tmp", kind, c.Dir)
		}
	}
}

// TestTheShellShapeCarriesItsWorkspace — and runs in it.
func TestTheShellShapeCarriesItsWorkspace(t *testing.T) {
	sb := seatbeltSandbox{path: "/usr/bin/sandbox-exec"}
	c := sb.build(context.Background(), sandboxRun{
		Kind: sandboxShellRun, Command: "ls", WorkspaceDir: "/ws/agent-9", AllowNetwork: true,
	})
	if c.Dir != "/ws/agent-9" {
		t.Errorf("cwd = %q, want the workspace", c.Dir)
	}
	profile := profileArg(t, c.Args)
	if !strings.Contains(profile, `"/ws/agent-9"`) {
		t.Error("the workspace is missing from the profile")
	}
	// The command must be the last argv element, after the profile.
	if c.Args[len(c.Args)-1] != "ls" {
		t.Errorf("argv does not end with the command: %v", c.Args)
	}
}

// TestSeatbeltDoesNotClaimToRemapPaths — the property every path helper keys
// on. Claiming true here would point PYTHONPATH and the shim bin dir at
// bubblewrap's mount points, which exist nowhere on macOS, and every hook-using
// script would die on its first line with ModuleNotFoundError.
func TestSeatbeltDoesNotClaimToRemapPaths(t *testing.T) {
	sb := seatbeltSandbox{}
	if sb.remapsPaths() {
		t.Error("seatbelt claims to remap paths")
	}
	if !sb.confines() {
		t.Error("seatbelt does not claim to confine")
	}
	// And the helpers must then hand back HOST paths, not mount points.
	if got := sandboxPythonPath(sb.remapsPaths(), ""); strings.Contains(got, SandboxGohortLibMountPath) {
		t.Errorf("PYTHONPATH points at a bubblewrap mount that does not exist on macOS: %q", got)
	}
}

// TestAFailedProbeDemotesRatherThanBreaks — sandbox-exec has been deprecated
// for a decade and its profile language is undocumented, so the binary being
// present is not evidence it accepts what we generate. Assuming it does would
// break every shell tool on the Mac at once, from a sandbox nobody knew had
// been switched on.
func TestAFailedProbeDemotesRatherThanBreaks(t *testing.T) {
	refuses := func(string) error { return errors.New("profile: unknown operation") }
	accepts := func(string) error { return nil }

	if seatbeltProbe("/usr/bin/sandbox-exec", refuses) {
		t.Error("a refused probe still reported the backend usable")
	}
	if !seatbeltProbe("/usr/bin/sandbox-exec", accepts) {
		t.Error("an accepted probe reported the backend unusable")
	}
	if seatbeltProbe("", accepts) {
		t.Error("an absent binary reported usable")
	}

	// And selection honors it: probe fails → unconfined, probe passes →
	// seatbelt. Checked from Linux, where it would otherwise never run.
	found := func(string) (string, error) { return "/usr/bin/sandbox-exec", nil }
	if sb := detectSandboxFor("darwin", found, func(string) bool { return false }); sb.confines() {
		t.Error("a failed probe still produced a confining backend")
	}
	sb := detectSandboxFor("darwin", found, func(string) bool { return true })
	if sb.name() != "seatbelt" || !sb.confines() {
		t.Errorf("a passing probe produced %q (confines=%v)", sb.name(), sb.confines())
	}
}

// TestTheProbeProfileIsTheRealShape — probing with a permissive throwaway would
// prove nothing about the profiles actually used. It is generated by the same
// function, so a syntax error in the real shape is what the probe catches.
func TestTheProbeProfileIsTheRealShape(t *testing.T) {
	var seen string
	seatbeltProbe("/usr/bin/sandbox-exec", func(p string) error { seen = p; return nil })
	if !strings.HasPrefix(seen, "(version 1)\n(deny default)\n") {
		t.Errorf("the probe used something other than a real generated profile:\n%s", seen)
	}
	if strings.Contains(seen, "(allow default)") {
		t.Error("the probe used a permissive profile, so it proves nothing")
	}
}

// --- helpers ---

// stripSBStrings removes every quoted string from an SBPL profile, honoring
// backslash escapes, leaving only the policy structure. What remains is what
// sandbox-exec will act on; anything a path smuggled in has been erased with
// the string that contained it.
func stripSBStrings(profile string) string {
	var out strings.Builder
	inStr := false
	for i := 0; i < len(profile); i++ {
		c := profile[i]
		if inStr {
			if c == '\\' && i+1 < len(profile) {
				i++ // skip the escaped character, quote included
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

// profileArg returns the argument following -p.
func profileArg(t *testing.T, argv []string) string {
	t.Helper()
	for i, a := range argv {
		if a == "-p" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	t.Fatalf("no -p profile in argv: %v", argv)
	return ""
}

// sectionAfter returns the text from a rule's opening line to its closing
// paren, so a test can assert about one rule rather than the whole profile.
func sectionAfter(t *testing.T, profile, opener string) string {
	t.Helper()
	i := strings.Index(profile, opener)
	if i < 0 {
		t.Fatalf("profile has no %q rule:\n%s", opener, profile)
	}
	rest := profile[i:]
	if j := strings.Index(rest, "\n)\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}
