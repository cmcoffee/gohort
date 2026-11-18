package servitor

// Running as root with a system's password, the agent never holding it.
//
// A system may keep what sudo asks for (SudoPassword) and root's own password
// (RootPassword, for su where there is no sudo). Both are write-only, like the
// login password: no read hands them back. The agent writes a command with
// sudo as it always did; servitor supplies the password:
//
//   - With a sudo password, the command runs after "sudo -S -v" has read it
//     on standard input, so the command's own sudo runs on that cached
//     authorization. The command reads nothing from standard input itself
//     (</dev/null), so a sudo that never asked cannot hand the password on.
//   - With only root's password, the command (its sudo words taken off) runs
//     as "su root -c '...'" on a terminal, and servitor types the password at
//     su's prompt.
//
// Either password is masked in the output before anything keeps it or reads
// it, a command that echoes its input included.

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// elevation is the passwords a system gives a command that runs as root.
type elevation struct {
	sudo, root string
}

// elevationOf is a's.
func elevationOf(a Appliance) elevation {
	return elevation{sudo: a.SudoPassword, root: a.RootPassword}
}

// sudoWord matches the word sudo where a command starts: at the start, or
// after a separator or an opening bracket.
var sudoWord = regexp.MustCompile(`(^|[;&|(]\s*|\s)sudo(\s+-n)?\s+`)

// prepare is cmd as it runs with el: the command, what it reads on standard
// input, and root's password when it runs through su instead. A command with
// no sudo in it, or a system with neither password, runs as it was written.
func (el elevation) prepare(cmd string) (run, stdin, su string) {
	if !sudoWord.MatchString(cmd) {
		return cmd, "", ""
	}
	switch {
	case el.sudo != "":
		return "sudo -S -p '' -v 2>/dev/null; { " + cmd + "\n} </dev/null", el.sudo + "\n", ""
	case el.root != "":
		return sudoWord.ReplaceAllString(cmd, "$1"), "", el.root
	}
	return cmd, "", ""
}

// mask is out with each of el's passwords replaced.
func (el elevation) mask(out []byte) []byte {
	for _, pw := range []string{el.sudo, el.root} {
		if len(pw) >= 3 {
			out = bytes.ReplaceAll(out, []byte(pw), []byte("••••"))
		}
	}
	return out
}

// lockedBuffer is a bytes.Buffer safe to write from the session while it is
// read here.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// suPromptWait is how long su is given to ask for the password.
const suPromptWait = 15 * time.Second

// runAsRootViaSu runs cmd as root through su on session, on a terminal with
// its echo off, typing pw at su's prompt; it returns what the command wrote,
// the prompt left out. With no prompt (already root, or no su), the password
// is never typed.
func runAsRootViaSu(session *ssh.Session, cmd, pw string) ([]byte, error) {
	modes := ssh.TerminalModes{ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty("xterm", 40, 200, modes); err != nil {
		return nil, err
	}
	in, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	var out lockedBuffer
	session.Stdout, session.Stderr = &out, &out
	if err := session.Start("su root -c " + shellQuote(cmd)); err != nil {
		return nil, err
	}
	ended := make(chan error, 1)
	go func() { ended <- session.Wait() }()
	asked, finished := false, false
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(suPromptWait)
wait:
	for {
		select {
		case err = <-ended:
			finished = true
			break wait
		case <-deadline:
			break wait
		case <-tick.C:
			if strings.Contains(strings.ToLower(out.String()), "password") {
				asked = true
				break wait
			}
		}
	}
	if asked {
		io.WriteString(in, pw+"\n")
	}
	in.Close()
	if !finished {
		err = <-ended
	}
	text := strings.ReplaceAll(out.String(), "\r\n", "\n")
	if asked {
		// What su wrote up to and including its prompt is not the command's.
		if i := strings.Index(strings.ToLower(text), "password"); i >= 0 {
			if j := strings.IndexByte(text[i:], '\n'); j >= 0 {
				text = text[i+j+1:]
			} else {
				text = ""
			}
		}
	}
	return []byte(text), err
}

// blankElevation is a with its passwords left out, saying only whether each
// is saved, for any read that hands the record back.
func (a *Appliance) blankElevation() {
	a.HasSudoPassword, a.HasRootPassword = a.SudoPassword != "", a.RootPassword != ""
	a.SudoPassword, a.RootPassword = "", ""
	a.ClearSudoPassword, a.ClearRootPassword = false, false
}

// sudoPromptMarker is the prompt a root run_pty asks sudo for, so the
// password is typed at sudo's prompt and nowhere else.
const sudoPromptMarker = "[servitor-sudo-password]"

// rootPTYCommand is cmd as a run_pty run as root starts it: through sudo
// when the system keeps a sudo password, else through su with root's; the
// prompt to wait for, and what to type at it. A shell it opens keeps no
// history (HISTFILE=/dev/null), so nothing run as root is written to one.
func (el elevation) rootPTYCommand(cmd string) (start, prompt, typed string) {
	inner := "HISTFILE=/dev/null; export HISTFILE; " + cmd
	if el.sudo != "" {
		return "sudo -S -p " + shellQuote(sudoPromptMarker) + " -- sh -c " + shellQuote(inner), sudoPromptMarker, el.sudo
	}
	return "su - -c " + shellQuote(inner), "password", el.root
}

// waitForPrompt reports whether out shows prompt (case aside) within wait.
func waitForPrompt(out *lockedBuffer, prompt string, wait time.Duration) bool {
	prompt = strings.ToLower(prompt)
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if strings.Contains(strings.ToLower(out.String()), prompt) {
			return true
		}
	}
	return false
}
