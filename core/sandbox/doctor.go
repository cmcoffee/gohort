// What is confining shell commands on this host, why it is not the thing you
// asked for, and the exact commands that would fix it.
//
// A DOCTOR, NOT AN INSTALLER, and the distinction is the whole design. It never
// runs sudo: installing packages and delegating subuid ranges are the
// operator's to authorize, and a tool that quietly acquires root to fix its own
// configuration is a worse problem than the one it solves. What it does instead
// is compute the commands FOR THIS HOST — the right package manager, a subuid
// range that does not collide with what is already delegated — so the operator
// pastes two lines rather than reading a distribution's documentation.
//
// It also verifies through the REAL probe rather than a reimplementation. That
// is not tidiness: a shell script that re-checks "can podman run" would drift
// from the check the daemon actually makes, which is exactly how
// persistent_shell.go ended up with its own copy of the bwrap argv and its own
// answer to what happens when bwrap is missing. "Verified" here means the
// production code path returned true.
package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"

	"github.com/cmcoffee/gohort/core/deps"
)

// Doctor renders the report. Returns the text and whether the host is in a
// state where shell tools will actually run.
func Doctor() (string, bool) {
	var b strings.Builder
	st := GetSandboxStatus()

	fmt.Fprintf(&b, "Sandbox\n")
	fmt.Fprintf(&b, "  backend        %s%s\n", st.Backend, requestedSuffix())
	fmt.Fprintf(&b, "  confined       %v\n", st.Confined)
	fmt.Fprintf(&b, "  bypass         %s\n", st.Bypass)
	fmt.Fprintf(&b, "  shell tools    %s\n", map[bool]string{
		true:  "REFUSED (no confinement, and the policy is fail-closed)",
		false: "running",
	}[st.Refusing])
	fmt.Fprintf(&b, "  limits         %s\n", st.LimitSummary)

	fmt.Fprintf(&b, "\nHost\n")
	fmt.Fprintf(&b, "  platform       %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "linux" {
		fmt.Fprintf(&b, "  selinux        %s%s\n", SELinuxState(), selinuxNote())
		fmt.Fprintf(&b, "  cgroups        %s%s\n", cgroupVersion(), cgroupNote())
	}
	fmt.Fprintf(&b, "  bubblewrap     %s\n", located("bwrap"))
	fmt.Fprintf(&b, "  podman         %s\n", located("podman"))
	fmt.Fprintf(&b, "  docker         %s\n", located("docker"))

	if wantsContainer() {
		fmt.Fprintf(&b, "\nContainer backend\n")
		fmt.Fprintf(&b, "  image          %s\n", containerImage())
		fmt.Fprintf(&b, "  subuid/subgid  %s\n", subuidState())
		if maj, min, ok := hostPythonVersion(); ok {
			fmt.Fprintf(&b, "  host python    %d.%d (the managed wheels are built against this)\n", maj, min)
			for _, line := range pythonSkewAdvice(maj, min) {
				fmt.Fprintf(&b, "                 %s\n", line)
			}
		}
	}

	if fixes := remedies(st); len(fixes) > 0 {
		fmt.Fprintf(&b, "\nTo fix, run these and re-run --sandbox-doctor:\n")
		for _, f := range fixes {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	return b.String(), !st.Refusing
}

// requestedSuffix names the gap between what was asked for and what is running,
// which is the single most useful line in the report: "none" alone does not say
// whether nobody configured anything or whether podman was requested and failed.
func requestedSuffix() string {
	pick := strings.ToLower(strings.TrimSpace(os.Getenv("GOHORT_SANDBOX_BACKEND")))
	if pick == "" || pick == "auto" {
		return ""
	}
	if pick == activeSandbox().name() {
		return " (requested)"
	}
	return " — " + pick + " was requested and is NOT what is running; see the log for why"
}

func located(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		return "not on PATH"
	}
	return p
}

func wantsContainer() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOHORT_SANDBOX_BACKEND"))) {
	case "podman", "docker", "container":
		return true
	}
	return false
}

func selinuxNote() string {
	if SELinuxState() != "enforcing" {
		return ""
	}
	if relabelMounts() {
		return " (bind mounts are relabeled shared; restorecon -R reverses it)"
	}
	return " (relabeling is OFF — a container will likely not read its workspace)"
}

// cgroupVersion distinguishes v1 from v2 by the file only v2 has.
//
// cgroup.controllers exists at the unified root and nowhere in a v1 hierarchy,
// which makes this a one-stat check with no syscall package and no
// platform-specific build tag. The alternative — statfs and a magic constant —
// needs a different type on every GOOS and would not compile on darwin, where
// this whole section does not run anyway.
func cgroupVersion() string {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return "v2"
	}
	if _, err := os.Stat("/sys/fs/cgroup"); err != nil {
		return "absent"
	}
	return "v1"
}

func cgroupNote() string {
	if cgroupVersion() != "v1" {
		return ""
	}
	// Worth stating rather than leaving to be discovered, and worth stating
	// that it does not matter here: gohort's own ceiling is enforced with
	// ulimit inside the command, which is independent of cgroups entirely.
	return " (podman cannot enforce --memory/--cpus rootless on v1; gohort's own limits still apply)"
}

// subuidState reports whether this user can build a user namespace, which is
// what rootless podman needs and what fails at RUN time rather than install
// time when it is missing.
func subuidState() string {
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	uid, gid := hasSubID("/etc/subuid", u.Username), hasSubID("/etc/subgid", u.Username)
	switch {
	case uid && gid:
		return "delegated to " + u.Username
	case uid || gid:
		return "INCOMPLETE for " + u.Username + " (needs both subuid and subgid)"
	default:
		return "NOT delegated to " + u.Username + " — rootless podman cannot map a user namespace"
	}
}

func hasSubID(path, username string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(strings.TrimSpace(sc.Text()), username+":") {
			return true
		}
	}
	return false
}

// freeSubIDRange picks a range that does not overlap anything already
// delegated, so the command printed below can be pasted rather than reasoned
// about. Ranges are read from both files because a host may have delegated one
// without the other.
func freeSubIDRange() (start, count int) {
	const size = 65536
	highest := 100000
	for _, path := range []string{"/etc/subuid", "/etc/subgid"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			parts := strings.Split(strings.TrimSpace(sc.Text()), ":")
			if len(parts) != 3 {
				continue
			}
			s, err1 := strconv.Atoi(parts[1])
			n, err2 := strconv.Atoi(parts[2])
			if err1 != nil || err2 != nil {
				continue
			}
			if end := s + n; end > highest {
				highest = end
			}
		}
		f.Close()
	}
	// Rounded up to the next 100000 so the ranges stay readable in a file a
	// person edits by hand.
	start = ((highest / 100000) + 1) * 100000
	return start, size
}

// remedies are the commands that would move this host to a working state.
//
// Ordered by what has to happen first, and only ever PRINTED. Each one is
// something the operator can read, understand and decline.
func remedies(st SandboxStatus) []string {
	if !st.Refusing && st.Confined {
		return nil
	}
	var out []string
	if wantsContainer() {
		if _, err := exec.LookPath("podman"); err != nil {
			if mgr := packageManager(); mgr != "" {
				out = append(out, "sudo "+mgr+" install -y podman")
			} else {
				out = append(out, "install podman with this system's package manager")
			}
		}
		if u, err := user.Current(); err == nil && !(hasSubID("/etc/subuid", u.Username) && hasSubID("/etc/subgid", u.Username)) {
			s, n := freeSubIDRange()
			out = append(out, fmt.Sprintf("sudo usermod --add-subuids %d-%d --add-subgids %d-%d %s",
				s, s+n-1, s, s+n-1, u.Username))
			out = append(out, "podman system migrate   # (as "+u.Username+", after the range is delegated)")
		}
		out = append(out, "podman pull "+containerImage())
		return out
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("bwrap"); err != nil {
			if mgr := packageManager(); mgr != "" {
				out = append(out, "sudo "+mgr+" install -y bubblewrap")
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "GOHORT_ALLOW_UNSANDBOXED=admin   # accept unconfined runs for an admin's own commands")
	}
	return out
}

// packageManager names this system's installer, or "" when it is not one of the
// two families worth printing a command for.
//
// Deliberately narrow. Printing `zypper install podman` untested is worse than
// printing nothing: a command that looks authoritative and is wrong costs more
// than the sentence it replaced.
func packageManager() string {
	for _, m := range []string{"dnf", "apt-get"} {
		if _, err := exec.LookPath(m); err == nil {
			return m
		}
	}
	return ""
}

// hostPythonVersion is the interpreter the managed wheels were built against.
// Named here rather than called inline so the report says WHY it is reported.
func hostPythonVersion() (int, int, bool) { return deps.SandboxPythonVersion() }

// pythonSkewAdvice says what a mismatched image costs and what to pin.
//
// Knowable BEFORE anything is installed, which is why it is here and not only
// in the runtime probe: an operator about to run `podman pull python:3-slim` on
// a Rocky 8 box is about to pair CPython 3.13 with wheels built for 3.6, and
// finding that out on the first failed `import numpy` is the expensive order to
// find it out in.
//
// The advice is deliberately not "pin python:3.6-slim". That tag is long out of
// support and pulling an unmaintained interpreter to run LLM-authored code is a
// worse trade than losing the compiled wheels. What actually works on an old
// host is a vendor image built for it, and the honest alternative is to accept
// the loss — pure-Python packages still import.
func pythonSkewAdvice(maj, min int) []string {
	if maj > 3 || (maj == 3 && min >= 11) {
		// New enough that a current official image is a plausible match; the
		// runtime probe still compares exactly and warns if it is not.
		return nil
	}
	return []string{
		"the default image runs a much newer Python, so COMPILED wheels (numpy, lxml,",
		fmt.Sprintf("pillow) will not import — they are built here for %d.%d. Pure Python is fine.", maj, min),
		"pin a matching image via GOHORT_SANDBOX_IMAGE (a vendor image for this OS, e.g.",
		"registry.access.redhat.com/ubi8/python-36), or accept the loss.",
	}
}
