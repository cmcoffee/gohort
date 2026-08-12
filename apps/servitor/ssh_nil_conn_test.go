package servitor

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSessionOpenersRefuseANilConnection — the panic this file exists for.
//
// A peer-reached appliance has NO local ssh.Client: the session lives on the
// far side and only whole commands cross the wire, so a.conn is nil by design.
// run_pty was the one exec path that reached for the client itself rather than
// going through execOverSSH, dereferenced that nil, and panicked inside
// golang.org/x/crypto/ssh — which safeInvoke then reported as
// "run_pty: [masked]", naming the tool and explaining nothing.
func TestSessionOpenersRefuseANilConnection(t *testing.T) {
	if _, err := newPTYSession(nil); err == nil {
		t.Error("newPTYSession accepted a nil connection")
	}
	if _, err := execOverSSH(context.Background(), nil, "echo hi"); err == nil {
		t.Error("execOverSSH accepted a nil connection")
	}
}

// newSessionCall finds a NewSession call and captures its receiver.
var newSessionCall = regexp.MustCompile(`(\w+(?:\.\w+)*)\.NewSession\(\)`)

// guardWindow is how many lines back a nil check may sit and still count as
// guarding the call. Generous enough for a check at the top of a function with
// a few lines of setup after it, tight enough that a check in a DIFFERENT
// function does not launder an unguarded call below it.
const guardWindow = 15

// TestNothingOpensAnSSHSessionUnguarded sweeps the package for session opens
// with no nil check in front of them.
//
// A source sweep because the defect is structural, not logical: every call site
// was individually reasonable, and the one that was wrong was wrong only
// because it had been written before peer-reached appliances existed. Nothing
// inside run_pty could detect that it had become the exception — only a rule
// covering all of them can.
//
// The rule is "check the receiver", not "call the helper". connIsAlive opens a
// session directly and is perfectly correct, because it tests for nil four
// lines earlier; demanding it route through newPTYSession would be the test
// dictating style rather than catching the bug.
func TestNothingOpensAnSSHSessionUnguarded(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			m := newSessionCall.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			checked++
			recv := m[1]
			guarded := false
			for back := i; back >= 0 && back > i-guardWindow; back-- {
				if strings.Contains(lines[back], recv+" == nil") {
					guarded = true
					break
				}
			}
			if !guarded {
				t.Errorf("%s:%d: %s.NewSession() with no nil check on %s in front of it. "+
					"A peer-reached appliance holds no ssh.Client, and dereferencing that nil "+
					"panics inside x/crypto/ssh where safeInvoke masks the reason — which is how "+
					"run_pty came back as \"[masked]\". Check it, or call newPTYSession.",
					f, i+1, recv, recv)
			}
		}
	}
	// A sweep that finds nothing passes vacuously and would keep passing after
	// someone moved the SSH code elsewhere.
	if checked == 0 {
		t.Fatal("found no NewSession calls at all — the sweep is no longer looking where they live")
	}
}

// TestRunPtyIsWithheldWhenReachedThroughAPeer — withheld rather than offered
// and failing.
//
// A tool that is present and always errors spends rounds and pushes the worker
// into improvising around it, instead of reaching for run_command, which works
// perfectly well over the peer transport. Checked against the source because
// the toolkit is assembled inside runSession, which needs a live session.
func TestRunPtyIsWithheldWhenReachedThroughAPeer(t *testing.T) {
	data, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `ptyLocal := strings.TrimSpace(appliance.PeerName) == ""`) {
		t.Fatal("the ptyLocal predicate is gone — run_pty may now be offered on a peer appliance")
	}
	// The only registration must sit under the predicate.
	idx := strings.Index(body, "if ptyLocal {")
	if idx < 0 {
		t.Fatal("run_pty is no longer gated on ptyLocal")
	}
	gated := body[idx:min(idx+300, len(body))]
	if !strings.Contains(gated, "newRunPtyTool()") {
		t.Error("the ptyLocal gate no longer guards the run_pty registration")
	}
	// And it must not ALSO be registered unconditionally in a tool list.
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "newRunPtyTool()") {
			continue
		}
		if strings.Contains(line, "workerTools = append(workerTools, newRunPtyTool())") {
			continue // the gated one
		}
		if strings.Contains(line, "result[i] = newRunPtyTool()") {
			continue // withFreshRunTool only replaces an entry already present
		}
		t.Errorf("run_pty is registered outside the ptyLocal gate: %s", strings.TrimSpace(line))
	}
}
