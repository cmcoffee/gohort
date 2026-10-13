package servitor

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// gohortDataFixture points gohort's data folder at a temp dir and returns it.
func gohortDataFixture(t *testing.T) string {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	prev := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(data, "workspaces"))
	t.Cleanup(func() { SetWorkspacesDir(prev) })
	return data
}

// A command naming gohort's data folder, or its database file, is caught; a
// neighbouring path that only starts the same way is not.
func TestACommandNamingGohortsDataIsCaught(t *testing.T) {
	data := gohortDataFixture(t)
	for _, cmd := range []string{
		"cat " + data + "/workspaces/.agents/u/a/board.md",
		"ls -la " + data,
		"sqlite3 '" + data + "/gohort.db' .tables",
		"cd /srv && strings gohort.db | head",
		"find / -path " + data + "* -name '*.md'",
	} {
		if !namesGohortData(cmd) {
			t.Errorf("not caught: %s", cmd)
		}
	}
	for _, cmd := range []string{
		"ls " + data + "base",
		"cat " + data + "2/notes.txt",
		"uptime",
		"journalctl -u nginx",
	} {
		if namesGohortData(cmd) {
			t.Errorf("caught by mistake: %s", cmd)
		}
	}
}

// Only on a system that is this machine: one that runs commands here, or an
// SSH system whose host is this machine. Another machine, or another
// gohort's system reached through a peer, is not gohort's data.
func TestOnlyThisMachineIsGuarded(t *testing.T) {
	for _, c := range []struct {
		app  Appliance
		want bool
	}{
		{Appliance{Type: "command"}, true},
		{Appliance{Type: "ssh", Host: "localhost"}, true},
		{Appliance{Type: "ssh", Host: "127.0.0.1:22"}, true},
		{Appliance{Host: "[::1]"}, true},
		{Appliance{Type: "ssh", Host: "203.0.113.9"}, false},
		{Appliance{Type: "command", PeerName: "lab"}, false},
		{Appliance{Type: "repo"}, false},
	} {
		if got := isThisMachine(c.app); got != c.want {
			t.Errorf("%+v: isThisMachine = %v, want %v", c.app, got, c.want)
		}
	}
}

// The gate refuses it before anything a grant allows, and says why; the
// same command on another machine goes on to the ordinary gate.
func TestTheGateRefusesGohortsDataOnThisMachine(t *testing.T) {
	data := gohortDataFixture(t)
	cmd := "cat " + data + "/workspaces/.agents/u/a/board.md"
	here := &probeRun{id: "guard-test", appliance: Appliance{ID: "sys1", Name: "Lab box", Type: "command"}}
	err := here.gateCommand(cmd)
	if err == nil || !strings.Contains(err.Error(), "NOT RUN") || !strings.Contains(err.Error(), "Lab box") {
		t.Fatalf("a read of gohort's data on this machine should be refused, got %v", err)
	}
	elsewhere := &probeRun{id: "guard-test-2", appliance: Appliance{ID: "sys2", Type: "ssh", Host: "203.0.113.9"}}
	if err := elsewhere.gateCommand(cmd); err != nil {
		t.Errorf("the same read on another machine is an ordinary read: %v", err)
	}
	if err := here.gateCommand("uptime"); err != nil {
		t.Errorf("an ordinary read on this machine was refused: %v", err)
	}
}
