package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Install is the program on disk: the binary that runs, and the one before
// it, kept beside it as <binary>.prev for a rollback.
type Install struct {
	// Exe is the running binary, its links resolved.
	Exe string
}

// Here is the running program's install.
func Here() (Install, error) {
	exe, err := os.Executable()
	if err != nil {
		return Install{}, err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return Install{Exe: exe}, nil
}

// Dir is the folder the binary is in, where downloads are written so the
// swap is a rename inside one filesystem.
func (in Install) Dir() string { return filepath.Dir(in.Exe) }

func (in Install) prev() string        { return in.Exe + ".prev" }
func (in Install) prevVersion() string { return in.Exe + ".prev.version" }

// Writable says why this install cannot replace its own binary, or "".
func (in Install) Writable() string {
	if in.Exe == "" {
		return "the running program's file is not known"
	}
	f, err := os.CreateTemp(in.Dir(), ".update-check-*")
	if err != nil {
		return fmt.Sprintf("the binary's folder (%s) is not writable by the user gohort runs as: update this install the way it was installed", in.Dir())
	}
	f.Close()
	os.Remove(f.Name())
	return ""
}

// proveTimeout bounds the new binary's one run.
var proveTimeout = 30 * time.Second

// Prove runs a downloaded binary once with --version: it must start, and say
// the version the release list says. This catches a binary for the wrong
// platform, or one that does not start, before anything is replaced.
func (in Install) Prove(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, proveTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = os.TempDir()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := out.String()
	switch {
	case ctx.Err() != nil:
		return errors.New("the new binary did not finish saying its version: not installing it")
	case err != nil:
		return fmt.Errorf("the new binary did not start (%v): not installing it: %s", err, clip(strings.TrimSpace(text)))
	case !strings.Contains(text, "v"+version):
		return fmt.Errorf("the new binary says it is not %s: not installing it", version)
	}
	return nil
}

// Swap puts a downloaded binary in place of the running one, keeping the
// running one as <binary>.prev, with its version beside it. A failure puts
// the running one back.
func (in Install) Swap(newPath, running string) error {
	os.Remove(in.prev())
	os.Remove(in.prevVersion())
	if err := os.Rename(in.Exe, in.prev()); err != nil {
		return fmt.Errorf("could not set the running binary aside: %v", err)
	}
	if err := os.Rename(newPath, in.Exe); err != nil {
		if back := os.Rename(in.prev(), in.Exe); back != nil {
			return fmt.Errorf("could not put the new binary in place (%v), nor the old one back (%v): it is at %s", err, back, in.prev())
		}
		return fmt.Errorf("could not put the new binary in place: %v", err)
	}
	_ = os.WriteFile(in.prevVersion(), []byte(running), 0o644)
	return nil
}

// Previous is the binary before this one: whether there is one, and its
// version when it was recorded.
func (in Install) Previous() (version string, ok bool) {
	if in.Exe == "" {
		return "", false
	}
	if st, err := os.Stat(in.prev()); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	b, _ := os.ReadFile(in.prevVersion())
	return strings.TrimSpace(string(b)), true
}

// Rollback puts the previous binary back, and keeps the running one as the
// previous: a second rollback goes forward again.
func (in Install) Rollback(running string) (string, error) {
	was, ok := in.Previous()
	if !ok {
		return "", errors.New("there is no previous version kept beside this one")
	}
	aside := in.Exe + ".rollback"
	os.Remove(aside)
	if err := os.Rename(in.Exe, aside); err != nil {
		return "", fmt.Errorf("could not set the running binary aside: %v", err)
	}
	if err := os.Rename(in.prev(), in.Exe); err != nil {
		if back := os.Rename(aside, in.Exe); back != nil {
			return "", fmt.Errorf("could not put the previous binary back (%v), nor this one (%v): it is at %s", err, back, aside)
		}
		return "", fmt.Errorf("could not put the previous binary back: %v", err)
	}
	if err := os.Rename(aside, in.prev()); err != nil {
		os.Remove(in.prevVersion())
		return was, nil
	}
	_ = os.WriteFile(in.prevVersion(), []byte(running), 0o644)
	return was, nil
}
