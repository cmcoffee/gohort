package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// realPaths runs the test against the real resolver, not the package's
// lenient one (main_test.go).
func realPaths(t *testing.T) {
	prev := resolveCommandPath
	resolveCommandPath = resolveOnDisk
	t.Cleanup(func() { resolveCommandPath = prev })
}

// A command given as a link runs the file the link led to when it was saved,
// with the link's path as argv[0]; once the link leads elsewhere it is not
// run until an admin saves the command again; a resolved path sent with the
// save is not taken.
func TestACommandGivenAsALinkRunsWhatItLedToWhenSaved(t *testing.T) {
	realPaths(t)
	dir := t.TempDir()
	v1 := filepath.Join(dir, "tool-1.0")
	v2 := filepath.Join(dir, "tool-2.0")
	os.WriteFile(v1, []byte("#!/bin/sh\necho one x\n"), 0o755)
	os.WriteFile(v2, []byte("#!/bin/sh\necho two\n"), 0o755)
	link := filepath.Join(dir, "tool")
	if err := os.Symlink(v1, link); err != nil {
		t.Fatal(err)
	}
	db := &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(db, Store{Name: "Caps", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveStoreCommand(db, StoreCommand{Slug: st.Slug, Name: "tool", Command: link, Resolved: v2})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(v1); saved.Resolved != want {
		t.Errorf("saved as leading to %q, want %q (a resolved path sent with the save is not taken)", saved.Resolved, want)
	}
	if !strings.Contains(saved.runsLabel(), "leads to") {
		t.Errorf("the admin's list does not say where the link leads: %q", saved.runsLabel())
	}
	out, err := runRegisteredCommand(context.Background(), t.TempDir(), saved)
	if err != nil || !strings.HasPrefix(out, "one ") {
		t.Errorf("run: %q %v (want the file the link led to)", out, err)
	}

	// The link is pointed at another file: the command does not run.
	os.Remove(link)
	os.Symlink(v2, link)
	if out, err := runRegisteredCommand(context.Background(), t.TempDir(), saved); err == nil || !strings.Contains(err.Error(), "saves it again") || strings.Contains(out, "two") {
		t.Errorf("a link that leads elsewhere now still ran: %q %v", out, err)
	}
	// Saved again, it runs the new file.
	again, err := SaveStoreCommand(db, saved)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runRegisteredCommand(context.Background(), t.TempDir(), again); err != nil || out != "two" {
		t.Errorf("after saving again: %q %v", out, err)
	}
	// A command saved before paths were resolved runs what its path leads to.
	if out, err := runRegisteredCommand(context.Background(), t.TempDir(), StoreCommand{Command: link}); err != nil || out != "two" {
		t.Errorf("a command from before: %q %v", out, err)
	}
}

// A path that is not an executable file is refused when it is saved.
func TestACommandPathMustLeadToAProgram(t *testing.T) {
	realPaths(t)
	dir := t.TempDir()
	plain := filepath.Join(dir, "notes.txt")
	os.WriteFile(plain, []byte("hi"), 0o644)
	dangling := filepath.Join(dir, "gone")
	os.Symlink(filepath.Join(dir, "missing"), dangling)
	db := &DBase{Store: kvlite.MemStore()}
	st, _ := SaveStore(db, Store{Name: "Caps", Path: t.TempDir()})
	for path, want := range map[string]string{
		filepath.Join(dir, "absent"): "nothing is installed",
		dangling:                     "nothing is installed",
		dir:                          "a folder",
		plain:                        "not executable",
	} {
		_, err := SaveStoreCommand(db, StoreCommand{Slug: st.Slug, Name: "x", Command: path})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", path, err, want)
		}
	}
}

// The program is told the path the admin gave as its argv[0], not the file
// the link leads to: a multi-call binary decides what to be from it.
func TestACommandRunsUnderThePathTheAdminGave(t *testing.T) {
	realPaths(t)
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc here")
	}
	link := filepath.Join(t.TempDir(), "mytool")
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatal(err)
	}
	real, err := resolveOnDisk(link)
	if err != nil {
		t.Skip("no /bin/sh here")
	}
	cmd := StoreCommand{Command: link, Resolved: real}
	out, err := runRegisteredCommand(context.Background(), t.TempDir(), cmd, "-c", `tr '\0' ' ' < /proc/$$/cmdline`)
	if err != nil || !strings.HasPrefix(out, link+" ") {
		t.Errorf("argv[0] is not the path the admin gave: %q %v", out, err)
	}
}
