package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// program says to an admin where a program is and how a build reaches it (in
// the sandbox by name, or as a store command when the sandbox cannot see
// it); to anybody else it says nothing of what is installed, only the steps
// to hand an admin; and it takes a program's name, never a path.
func TestBuilderAsksWhetherAProgramIsThereBeforeBuildingOnIt(t *testing.T) {
	_, _, user := newTestOrchestrate(t)
	prevFind, prevHost := findProgram, findOnHost
	t.Cleanup(func() { findProgram, findOnHost = prevFind, prevHost })
	findProgram = func(name string) (string, string, bool) {
		switch name {
		case "tshark":
			return "/usr/bin/tshark", "/usr/libexec/wireshark/tshark", true
		case "oddtool":
			return "/usr/local/bin/oddtool", "/opt/odd/bin/oddtool", false
		}
		return "", "", false
	}
	findOnHost = func(name string) string {
		if name == "vendortool" {
			return "/opt/vendor/bin/vendortool"
		}
		return ""
	}
	turn := &chatTurn{user: user}
	// Through the tool, as Builder calls it.
	ask := func(name string) string {
		out, err := appDefCall(t, turn, map[string]any{"action": "program", "name": name})
		if err != nil {
			return "error: " + err.Error()
		}
		return out
	}

	// Not an admin: no word of what is installed.
	out := ask("tshark")
	if strings.Contains(out, "/usr/bin/tshark") || strings.Contains(out, "is installed at") || !strings.Contains(out, "1. An admin installs tshark") {
		t.Errorf("somebody who is not an admin was told what is installed, or not given the steps: %s", out)
	}

	AuthDB().Set(AuthTable, "user:"+user, AuthUser{Username: user, Admin: true})
	for name, want := range map[string]string{
		"tshark":     "is installed at /usr/bin/tshark (leads to /usr/libexec/wireshark/tshark), where scripts reach it",
		"oddtool":    "which the sandbox does not see",
		"vendortool": "Register it as a command on a file store",
		"nothere":    "nothere is not installed on this server. Build the rest now",
		"../x":       "error: program needs name",
		"/usr/bin/x": "error: program needs name",
	} {
		if got := ask(name); !strings.Contains(got, want) {
			t.Errorf("%s: %s\nwant it to say %q", name, got, want)
		}
	}
}

// The real lookup: a program on the sandbox's PATH is found, and one that
// is not is not.
func TestFindProgramLooksWhereTheSandboxLooks(t *testing.T) {
	if p, _, ok := findProgram("sh"); !ok || !strings.HasSuffix(p, "/sh") {
		t.Skipf("no sh on this host's sandbox PATH: %q %v", p, ok)
	}
	if _, _, ok := findProgram("no-such-program-anywhere"); ok {
		t.Error("a program that is not installed was found")
	}
}
