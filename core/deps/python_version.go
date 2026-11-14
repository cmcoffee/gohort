// The sandbox's Python: chosen, not inherited (docs/design/sandbox-python.md).
//
// Everything that runs Python asks Python() for the interpreter, host side and
// in the sandbox alike (the sandbox bin dir's python3 is a shim to it). It is
// picked once: gohort.ini's [paths] python when set, else the newest
// python3.N on PATH that is 3.9 or newer, else plain python3 as before. RHEL 8
// and its relatives ship 3.6 as python3 and keep it for the OS's own tools,
// with newer interpreters installed beside it under their versioned names, so
// taking whatever python3 is left scripts on an interpreter out of support.
//
// The version is also what the authoring note keys on: when the chosen
// interpreter is older than 3.7 (a host with nothing newer), Builder is told
// to avoid the 3.7+ subprocess conveniences, as before. On 3.9 or newer the
// note is empty.

package deps

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cmcoffee/snugforge/nfo"
)

// MinPreferredPython is the oldest interpreter the search takes: the oldest
// still common in distributions, and what current packages commonly require.
const MinPreferredPython = 9

// newestPythonMinor is where the search for python3.N starts, counting down.
const newestPythonMinor = 20

// PythonChoice is the interpreter gohort runs scripts with.
type PythonChoice struct {
	// Path is the interpreter to run: an absolute path, or "python3" when
	// nothing could be resolved.
	Path string
	// Major and Minor are its version; OK reports that it ran and answered.
	Major, Minor int
	OK           bool
	// Source says where the choice came from, for the status panel and log:
	// "gohort.ini", "search" or "fallback".
	Source string
}

// Version is the choice's major.minor, or "" when it did not answer.
func (c PythonChoice) Version() string {
	if !c.OK {
		return ""
	}
	return strconv.Itoa(c.Major) + "." + strconv.Itoa(c.Minor)
}

// atLeast reports whether the choice is Python 3.minor or newer.
func (c PythonChoice) atLeast(minor int) bool {
	return c.OK && (c.Major > 3 || (c.Major == 3 && c.Minor >= minor))
}

var (
	pythonOverrideMu sync.Mutex
	pythonOverride   string
	pythonOnce       sync.Once
	pythonChosen     PythonChoice
	plainOnce        sync.Once
	plainPython      PythonChoice

	// lookPath and pythonVersionOf are seams for tests: the search runs
	// against a fake PATH and fake interpreters.
	lookPath        = exec.LookPath
	pythonVersionOf = probePythonVersion
)

// SetPythonOverride takes gohort.ini's [paths] python: a path, or a name on
// PATH. Called at startup, before anything runs Python; empty leaves the
// choice to the search.
func SetPythonOverride(v string) {
	pythonOverrideMu.Lock()
	pythonOverride = strings.TrimSpace(v)
	pythonOverrideMu.Unlock()
}

// Python is the interpreter to run scripts with (see ChosenPython).
func Python() string { return ChosenPython().Path }

// ChosenPython picks the interpreter once and returns it.
func ChosenPython() PythonChoice {
	pythonOnce.Do(func() {
		pythonOverrideMu.Lock()
		override := pythonOverride
		pythonOverrideMu.Unlock()
		pythonChosen = choosePython(override)
		if pythonChosen.OK {
			nfo.Log("[sandbox] script Python: %s (%s, from %s)", pythonChosen.Version(), pythonChosen.Path, pythonChosen.Source)
		} else {
			nfo.Log("[sandbox] script Python: none answered (%s, from %s)", pythonChosen.Path, pythonChosen.Source)
		}
		if pythonChosen.Source == "fallback" && pythonChosen.OK && !pythonChosen.atLeast(MinPreferredPython) {
			nfo.Notice("[sandbox] scripts run on Python %s, which is out of support: install python3.%d or newer, or set [paths] python in gohort.ini", pythonChosen.Version(), MinPreferredPython)
		}
	})
	return pythonChosen
}

// choosePython is the pick itself: the override, the newest python3.N that
// is new enough, plain python3 if it is, and plain python3 whatever it is.
func choosePython(override string) PythonChoice {
	if override != "" {
		path := override
		if !filepath.IsAbs(path) {
			if p, err := lookPath(path); err == nil {
				path = p
			}
		}
		c := pythonVersionOf(path)
		c.Source = "gohort.ini"
		return c
	}
	for minor := newestPythonMinor; minor >= MinPreferredPython; minor-- {
		p, err := lookPath("python3." + strconv.Itoa(minor))
		if err != nil {
			continue
		}
		if c := pythonVersionOf(p); c.atLeast(MinPreferredPython) {
			c.Source = "search"
			return c
		}
	}
	plain := plainPythonChoice()
	if plain.atLeast(MinPreferredPython) {
		plain.Source = "search"
	}
	return plain
}

// plainPythonChoice is whatever python3 on PATH is, which is what every run
// used before the choice existed.
func plainPythonChoice() PythonChoice {
	plainOnce.Do(func() {
		path := "python3"
		if p, err := lookPath("python3"); err == nil {
			path = p
		}
		plainPython = pythonVersionOf(path)
		plainPython.Source = "fallback"
	})
	return plainPython
}

// probePythonVersion runs path --version and parses "Python X.Y.Z".
func probePythonVersion(path string) PythonChoice {
	c := PythonChoice{Path: path}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		nfo.Debug("[sandbox] %s --version failed: %v", path, err)
		return c
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return c
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) < 2 {
		return c
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return c
	}
	c.Major, c.Minor, c.OK = maj, min, true
	return c
}

// SandboxPythonVersion returns the chosen interpreter's major.minor and
// whether it answered.
func SandboxPythonVersion() (major, minor int, ok bool) {
	c := ChosenPython()
	return c.Major, c.Minor, c.OK
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
