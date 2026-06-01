// Sandbox Python version probe. Builder authors Python tool_def
// scripts that run inside the bwrap sandbox; when the host Python is
// older than 3.7, modern subprocess conveniences like
// `capture_output=True` and `text=True` blow up with TypeError on
// __init__. Builder kept rediscovering this the hard way on every
// long-haired CentOS 8 / RHEL 8 user — 3.6.8 ships there.
//
// This probe runs `python3 --version` once, parses major.minor, and
// exposes a one-line authoring note that the Builder prompt + worker
// directives append when the version is < 3.7. When >= 3.7, the note
// is empty and nothing is injected.
//
// Why direct exec instead of sandboxed: /usr is bind-mounted ro into
// the sandbox, so the python3 the sandbox sees is the same binary the
// host runs. Probing in-sandbox would add bwrap setup latency for no
// signal change. If the binary ever stops resolving (no python3 on
// PATH), the probe records ok=false and the note is empty — Builder
// already prompts about Python being the script lang of choice, and a
// missing interpreter is a separate "install python3" problem the
// authoring note can't fix.

package core

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var (
	pythonProbeOnce sync.Once
	pythonMajor     int
	pythonMinor     int
	pythonOK        bool
)

// probeSandboxPython runs `python3 --version` and parses the result.
// Cached via sync.Once — subsequent calls are free. Logs the result at
// debug level so operators can see what was detected.
func probeSandboxPython() {
	pythonProbeOnce.Do(func() {
		out, err := exec.Command("python3", "--version").CombinedOutput()
		if err != nil {
			Debug("[sandbox] python3 --version failed (%v) — authoring note disabled", err)
			return
		}
		// Output is "Python X.Y.Z" on every supported release.
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 2 {
			Debug("[sandbox] python3 --version produced unparseable output %q", string(out))
			return
		}
		parts := strings.Split(fields[1], ".")
		if len(parts) < 2 {
			Debug("[sandbox] python3 version field %q has no minor component", fields[1])
			return
		}
		maj, err1 := strconv.Atoi(parts[0])
		min, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			Debug("[sandbox] python3 version %q failed to parse as int.int", fields[1])
			return
		}
		pythonMajor = maj
		pythonMinor = min
		pythonOK = true
		Debug("[sandbox] python3 detected: %d.%d (authoring note: %v)", maj, min, maj == 3 && min < 7)
	})
}

// SandboxPythonVersion returns the probed major.minor and whether the
// probe succeeded. Triggers the probe on first call.
func SandboxPythonVersion() (major, minor int, ok bool) {
	probeSandboxPython()
	return pythonMajor, pythonMinor, pythonOK
}

// SandboxPythonAuthoringNote returns a prompt block warning Builder
// and its workers to avoid Python 3.7+ subprocess kwargs (capture_output=,
// text=) when the sandbox runs an older interpreter. Empty string when
// Python is 3.7+ or the probe failed — callers can append unconditionally.
func SandboxPythonAuthoringNote() string {
	maj, min, ok := SandboxPythonVersion()
	if !ok {
		return ""
	}
	if maj > 3 || (maj == 3 && min >= 7) {
		return ""
	}
	// 3.6 and earlier: capture_output= and text= weren't added until
	// 3.7. Builder used to retry several times before finding the
	// right shape; this block short-circuits the loop.
	return "## Sandbox Python compatibility\n\n" +
		"The sandbox interpreter is **Python " + strconv.Itoa(maj) + "." + strconv.Itoa(min) + "** (older than 3.7). When writing scripts that use `subprocess.run`, you MUST use the pre-3.7 form:\n\n" +
		"- WRONG (3.7+ only): `subprocess.run([...], capture_output=True, text=True)`\n" +
		"- RIGHT (3.6-compatible): `subprocess.run([...], stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)`\n\n" +
		"Other 3.7+ features that fail on this sandbox: dict insertion-order guarantees beyond CPython detail, `dataclasses` (stdlib in 3.7+), `from __future__ import annotations` PEP 563 evaluation, walrus operator `:=` (3.8+), f-string `=` self-documenting expressions (3.8+). When in doubt, target the 3.6 stdlib surface.\n"
}
