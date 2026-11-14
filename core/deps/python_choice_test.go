package deps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// fakePythons stands in for PATH and the interpreters on it: name -> version.
func fakePythons(t *testing.T, onPath map[string]string) {
	t.Helper()
	prevLook, prevVer := lookPath, pythonVersionOf
	t.Cleanup(func() { lookPath, pythonVersionOf = prevLook, prevVer })
	lookPath = func(name string) (string, error) {
		if _, ok := onPath[name]; ok {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	pythonVersionOf = func(path string) PythonChoice {
		v, ok := onPath[strings.TrimPrefix(path, "/usr/bin/")]
		c := PythonChoice{Path: path}
		if !ok {
			return c
		}
		parts := strings.Split(v, ".")
		c.Major, c.Minor, c.OK = atoi(parts[0]), atoi(parts[1]), true
		return c
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// The search takes the newest python3.N that is 3.9 or newer, over a plain
// python3 that is older; the ini's python wins over the search; with nothing
// new enough, plain python3 is what runs, as before.
func TestTheScriptPythonIsChosenNotInherited(t *testing.T) {
	fakePythons(t, map[string]string{"python3": "3.6", "python3.6": "3.6", "python3.11": "3.11", "python3.12": "3.12"})
	if c := choosePython(""); c.Path != "/usr/bin/python3.12" || c.Version() != "3.12" || c.Source != "search" {
		t.Errorf("the search chose %+v, want python3.12", c)
	}
	if c := choosePython("python3.11"); c.Path != "/usr/bin/python3.11" || c.Source != "gohort.ini" {
		t.Errorf("the ini's python did not win: %+v", c)
	}
}

func TestWithNothingNewEnoughPlainPython3RunsAsBefore(t *testing.T) {
	fakePythons(t, map[string]string{"python3": "3.6", "python3.6": "3.6"})
	plainOnce = syncOnceReset()
	if c := choosePython(""); c.Path != "/usr/bin/python3" || c.Source != "fallback" || c.Version() != "3.6" {
		t.Errorf("with nothing 3.9 or newer, got %+v, want plain python3", c)
	}
	plainOnce = syncOnceReset()
	fakePythons(t, map[string]string{"python3": "3.12"})
	if c := choosePython(""); c.Path != "/usr/bin/python3" || c.Source != "search" {
		t.Errorf("a plain python3 that is new enough was not taken as the search's pick: %+v", c)
	}
	plainOnce = syncOnceReset()
}

// Packages are tied to one Python version: a choice that is plain python3's
// version keeps the directory every earlier install used, and any other gets
// its own, so a switch never loads a .so built for another CPython.
func TestPackagesLiveInADirectoryPerPythonVersion(t *testing.T) {
	v := func(maj, min int) PythonChoice { return PythonChoice{Major: maj, Minor: min, OK: true} }
	if d := pyDepsVersionDir(v(3, 6), v(3, 6)); d != "" {
		t.Errorf("an unchanged choice moved its packages: %q", d)
	}
	if d := pyDepsVersionDir(v(3, 12), v(3, 6)); d != "py3.12" {
		t.Errorf("a newer choice shares the 3.6 directory: %q", d)
	}
}

func syncOnceReset() sync.Once { return sync.Once{} }

// pip for the chosen interpreter: its own when it has one, else a private
// virtual environment made under the data directory it is given, never the
// system's or a home directory's. Offline: venv bootstraps pip from ensurepip.
func TestPipComesFromTheChosenPythonOrAPrivateVenv(t *testing.T) {
	chosen := ChosenPython()
	if !chosen.OK {
		t.Skip("no Python interpreter on this host")
	}
	base := t.TempDir()
	pipMu.Lock()
	pipCached = ""
	pipMu.Unlock()
	t.Cleanup(func() { pipMu.Lock(); pipCached = ""; pipMu.Unlock() })
	py, err := pipPython(context.Background(), base)
	if err != nil {
		t.Skipf("this host cannot provide a pip for Python %s: %v", chosen.Version(), err)
	}
	if py != chosen.Path && !strings.HasPrefix(py, base+string(os.PathSeparator)) {
		t.Errorf("pip runs from %s: neither the chosen interpreter nor under the data directory", py)
	}
	if err := exec.Command(py, "-m", "pip", "--version").Run(); err != nil {
		t.Errorf("the pip it chose does not run: %v", err)
	}
}
