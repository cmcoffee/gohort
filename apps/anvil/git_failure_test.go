package anvil

import (
	"errors"
	"testing"
)

// A git failure is reported by what git said went wrong, not its first line:
// an unpack that failed on a Mac was reported, and logged, as "Cloning into
// '.'...", which is git starting, not why it stopped.
func TestAGitFailureSaysWhyItStopped(t *testing.T) {
	exit := errors.New("exit status 128")
	for _, c := range []struct{ out, want string }{
		{"Cloning into '.'...\nfatal: Remote branch main not found in upstream origin\n", "fatal: Remote branch main not found in upstream origin"},
		{"Cloning into '.'...\nerror: unable to read sha1 file\nfatal: index-pack failed\n", "error: unable to read sha1 file fatal: index-pack failed"},
		{"Cloning into '.'...\nsomething went wrong\n", "something went wrong (exit status 128)"},
		{"", "exit status 128"},
	} {
		if got := gitFailure(c.out, exit); got != c.want {
			t.Errorf("for %q: %q, want %q", c.out, got, c.want)
		}
	}
}
