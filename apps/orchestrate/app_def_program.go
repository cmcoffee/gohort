package orchestrate

// app_def(action="program"): whether a program a build needs is here.
//
// Builder could not tell whether a program a script would run (tshark,
// ffmpeg, pdftotext) was installed, so it built on one that was not, or gave
// up without saying who could fix it. This answers before it builds.
//
// An administrator is told where it is and whether scripts can run it. The
// sandbox sees the host's /usr, /bin and /sbin read-only, on its own PATH,
// so a program installed there runs from a script by name, with no network:
// the safer place for one that only reads files. One found elsewhere (/opt)
// the sandbox cannot see, so it goes on a file store as a registered command,
// which runs on the host. Anybody else is not told what this server has
// installed, which is the administrator's to know: they get the steps to hand
// an admin, filled in for the program.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// sandboxPath is the PATH a sandboxed script runs with, and sandboxRoots the
// host folders it sees (core/sandbox: container.go, exec.go).
var (
	sandboxPath  = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}
	sandboxRoots = []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/", "/lib32/", "/etc/alternatives/"}
)

var programName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// findProgram is where name is on the sandbox's PATH, and the file that
// leads to; ok false when it is not there, or leads outside what the sandbox
// sees. A var so a test can stand in a host.
var findProgram = func(name string) (path, real string, ok bool) {
	for _, dir := range sandboxPath {
		p := filepath.Join(dir, name)
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		fi, err := os.Stat(r)
		if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		for _, root := range sandboxRoots {
			if strings.HasPrefix(r, root) {
				return p, r, true
			}
		}
		return p, r, false
	}
	return "", "", false
}

// findOnHost is name on the server's own PATH, for one the sandbox cannot see.
var findOnHost = func(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func (t *chatTurn) appDefProgram(args map[string]any) (string, error) {
	name := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "name"), stringArg(args, "program")))
	if !programName.MatchString(name) {
		return "", fmt.Errorf("program needs name: the program's own name as a script would call it (tshark, ffmpeg), not a path or a package")
	}
	steps := func(lead string) string {
		return lead + " Build the rest now, and end your reply with these steps for the person, filled in for this build:\n" +
			"1. An admin installs " + name + " on the server with its package manager, so it is under /usr.\n" +
			"2. Tell me it is there: a script then runs it by name, in the sandbox (read-only, no network).\n" +
			"If it must run outside the sandbox (it needs the network, or files the sandbox cannot reach), the admin instead registers it as a command on a file store (Admin, Files, the store's row, Add command) and maps the lines agents may run; then I build on that store's command."
	}
	if !UserIsAdmin(t.user) {
		return steps("Whether " + name + " is installed on this server is an administrator's to check, so it is not said here."), nil
	}
	if p, real, ok := findProgram(name); ok {
		where := p
		if real != p {
			where += " (leads to " + real + ")"
		}
		return name + " is installed at " + where + ", where scripts reach it: a data source or action script runs it by name, inside the sandbox, which sees the host's /usr read-only and has no network. That suits a program that reads files (an app's granted file stores are mounted for its scripts) and prints what it found. Check its output once with run before building on it.", nil
	} else if p != "" {
		return name + " is at " + p + ", but that leads to " + real + ", which the sandbox does not see, so a script cannot run it. Either install it under /usr, or register it as a command on a file store (Admin, Files, Add command) and build on that store's command.", nil
	}
	if host := findOnHost(name); host != "" {
		return name + " is installed at " + host + ", outside what the sandbox sees, so a script cannot run it by name. Register it as a command on a file store (Admin, Files, the store's row, Add command, map the lines agents may run) and build on that, or install it under /usr.", nil
	}
	return steps(name + " is not installed on this server."), nil
}
