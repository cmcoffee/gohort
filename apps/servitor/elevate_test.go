package servitor

import (
	"strings"
	"testing"
	"time"
)

// A command that runs as root is given the system's password by servitor:
// with a sudo password, sudo -S reads it on standard input first and the
// command reads nothing from it; with only root's password, the command runs
// through su with its sudo words taken off. A command with no sudo, or a
// system with neither password, runs as written. Either password is masked
// in what comes back.
func TestACommandRunsAsRootWithTheSystemsPassword(t *testing.T) {
	sudo := elevation{sudo: "s3cret-sudo"}
	run, stdin, su := sudo.prepare("sudo -n systemctl status nginx | head -5")
	if !strings.HasPrefix(run, "sudo -S -p '' -v") || !strings.Contains(run, "</dev/null") || stdin != "s3cret-sudo\n" || su != "" {
		t.Errorf("with a sudo password: %q %q %q", run, stdin, su)
	}
	if strings.Contains(run, "s3cret") {
		t.Error("the password is on the command line")
	}
	root := elevation{root: "r00t-pw"}
	run, stdin, su = root.prepare("cat /etc/shadow && sudo -n ls /root")
	if su != "r00t-pw" || stdin != "" || strings.Contains(run, "sudo") || !strings.Contains(run, "ls /root") {
		t.Errorf("with only a root password: %q %q %q", run, stdin, su)
	}
	if run, stdin, su := sudo.prepare("uptime"); run != "uptime" || stdin != "" || su != "" {
		t.Errorf("a command with no sudo: %q %q %q", run, stdin, su)
	}
	if run, _, _ := (elevation{}).prepare("sudo -n ls"); run != "sudo -n ls" {
		t.Errorf("a system with neither password: %q", run)
	}
	both := elevation{sudo: "s3cret-sudo", root: "r00t-pw"}
	if got := string(both.mask([]byte("echoed s3cret-sudo and r00t-pw"))); strings.Contains(got, "s3cret") || strings.Contains(got, "r00t") {
		t.Errorf("not masked: %q", got)
	}
}

// The passwords are write-only: a read of the system says only whether each
// is saved, never what it is.
func TestTheSystemsPasswordsAreNeverReadBack(t *testing.T) {
	a := Appliance{SudoPassword: "s3cret-sudo", RootPassword: "r00t-pw", ClearSudoPassword: true}
	a.blankElevation()
	if a.SudoPassword != "" || a.RootPassword != "" || !a.HasSudoPassword || !a.HasRootPassword || a.ClearSudoPassword {
		t.Errorf("read back: %+v", a)
	}
}

// A run_pty run as root starts the command through sudo (or su, with only
// root's password) as the session's own command, never typed into a shell,
// with history off; the password is typed only at the prompt asked for,
// never when it does not come.
func TestARootTerminalTypesThePasswordOnlyAtItsPrompt(t *testing.T) {
	start, prompt, typed := (elevation{sudo: "s3cret-sudo", root: "r00t-pw"}).rootPTYCommand("bash")
	if !strings.HasPrefix(start, "sudo -S -p ") || !strings.Contains(start, "HISTFILE=/dev/null") || prompt != sudoPromptMarker || typed != "s3cret-sudo" {
		t.Errorf("with a sudo password: %q %q %q", start, prompt, typed)
	}
	if strings.Contains(start, "s3cret") {
		t.Error("the password is on the command line")
	}
	start, prompt, typed = (elevation{root: "r00t-pw"}).rootPTYCommand("bash")
	if !strings.HasPrefix(start, "su - -c ") || !strings.Contains(start, "HISTFILE=/dev/null") || prompt != "password" || typed != "r00t-pw" {
		t.Errorf("with only a root password: %q %q %q", start, prompt, typed)
	}
	var out lockedBuffer
	out.Write([]byte("Last login: today\n"))
	if waitForPrompt(&out, "password", 120*time.Millisecond) {
		t.Error("a prompt that never came was taken for one")
	}
	out.Write([]byte("Password: "))
	if !waitForPrompt(&out, "password", 120*time.Millisecond) {
		t.Error("su's prompt was missed")
	}
}
