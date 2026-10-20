package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tool call wrote a file when it is new, or when it was there before and
// was rewritten: a tool that saves to the same name every time made a file
// the names-only diff never saw. A file left alone is not reported.
func TestARewrittenFileCountsAsWritten(t *testing.T) {
	dir := t.TempDir()
	sess := &ToolSession{WorkspaceDir: dir}
	old := time.Now().Add(-time.Hour)
	for _, n := range []string{"generated_music.mp3", "untouched.txt"} {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("from before"), 0600)
		os.Chtimes(p, old, old)
	}
	before := snapshotWorkspaceFiles(sess)

	os.WriteFile(filepath.Join(dir, "generated_music.mp3"), []byte("the new song"), 0600)
	os.WriteFile(filepath.Join(dir, "cover.png"), []byte("png"), 0600)
	got := diffWorkspaceFiles(sess, before)
	joined := "," + strings.Join(got, ",") + ","
	if len(got) != 2 || !strings.Contains(joined, ",generated_music.mp3,") || !strings.Contains(joined, ",cover.png,") {
		t.Fatalf("written = %v, want the rewritten song and the new picture", got)
	}

	// Same size, new time: still rewritten.
	before = snapshotWorkspaceFiles(sess)
	p := filepath.Join(dir, "untouched.txt")
	os.WriteFile(p, []byte("from BEFORE"), 0600)
	newer := time.Now().Add(time.Minute)
	os.Chtimes(p, newer, newer)
	if got := diffWorkspaceFiles(sess, before); len(got) != 1 || got[0] != "untouched.txt" {
		t.Errorf("a same-size rewrite read as %v", got)
	}
}
