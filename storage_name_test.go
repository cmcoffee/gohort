package main

import (
	"os"
	"path/filepath"
	"testing"
)

// An install from before the rename keeps its data files under the old
// name, found by the main database being there; a fresh install, or one
// that already has the new file, uses the app's name.
func TestDataFilesKeepTheirOldNameWhereTheyExist(t *testing.T) {
	dir := t.TempDir()
	if got := storageName(dir); got != APPNAME {
		t.Errorf("fresh: %q", got)
	}
	os.WriteFile(filepath.Join(dir, legacyAppName+".db"), []byte("x"), 0o644)
	if got := storageName(dir); got != legacyAppName {
		t.Errorf("legacy install: %q", got)
	}
	os.WriteFile(filepath.Join(dir, APPNAME+".db"), []byte("x"), 0o644)
	if got := storageName(dir); got != APPNAME {
		t.Errorf("both present, the new one wins: %q", got)
	}
}
