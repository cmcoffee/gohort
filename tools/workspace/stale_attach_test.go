package workspace

// Attaching a file this turn did not make. The workspace root is per user and
// shared by every session and agent, so "a plausible-looking file in it" and
// "what I just made" are different questions — and the attach result said
// "Attached" to both. Live, that shipped two pictures from an earlier session
// as the variations the user had just asked for.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func writeWorkspaceFile(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not really a png"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestAttachingSomethingThisTurnDidNotMakeSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "gen-old.png", 3*time.Hour)
	writeWorkspaceFile(t, dir, "gen-new.png", 0)

	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}
	sess.AddStagedFiles([]string{"gen-new.png"})

	out, err := AttachWorkspaceFile(sess, "gen-old.png", "", false)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !strings.Contains(out, "NOT one your tools produced this turn") {
		t.Errorf("an older file must not pass silently as this turn's work:\n%s", out)
	}
	if !strings.Contains(out, "3 hours") {
		t.Errorf("the warning must say how old it is:\n%s", out)
	}
	if !strings.Contains(out, "gen-new.png") {
		t.Errorf("the warning must name what this turn DID produce:\n%s", out)
	}
	// Still delivered: re-sending an older picture is an ordinary request.
	if len(sess.Images)+len(sess.Files) != 1 {
		t.Error("the file must still be attached — this is a warning, not a refusal")
	}
}

func TestAttachingThisTurnsOwnOutputIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "gen-new.png", 0)
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}
	sess.AddStagedFiles([]string{"gen-new.png"})

	out, err := AttachWorkspaceFile(sess, "gen-new.png", "", false)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if strings.Contains(out, "NOT one your tools produced") {
		t.Errorf("the file this turn made must attach without a caveat:\n%s", out)
	}
}

// A turn that produced nothing is not confusing anything with anything — the
// user asking for a file they put there themselves must not be second-guessed.
func TestAttachingWithNothingStagedIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "report.pdf", 48*time.Hour)
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}

	out, err := AttachWorkspaceFile(sess, "report.pdf", "", false)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if strings.Contains(out, "NOT one your tools produced") {
		t.Errorf("nothing was made this turn, so there is nothing to warn about:\n%s", out)
	}
}

// A file of any kind handed back under a taken name arrives as name-2: the
// older file under the original name is refused this turn, pointing at the
// copy, and nothing is queued to send. The copy attaches, a name with no such
// copy still attaches with the warning, and so does the older file on a turn
// that made no copy of it.
func TestAnOlderFileWithARenamedCopyThisTurnIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"generated_music.mp3", "generated_music-2.mp3", "report.pdf", "report-3.pdf", "notes.txt", "notes-final.txt"} {
		writeWorkspaceFile(t, dir, n, time.Hour)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}
	sess.AddStagedFiles([]string{"generated_music-2.mp3", "report-3.pdf", "notes-final.txt"})

	for old, copy := range map[string]string{"generated_music.mp3": "generated_music-2.mp3", "report.pdf": "report-3.pdf"} {
		_, err := AttachWorkspaceFile(sess, old, "", false)
		if err == nil || !strings.Contains(err.Error(), "not attached") || !strings.Contains(err.Error(), copy) {
			t.Errorf("%s with %s made this turn: err %v", old, copy, err)
		}
	}
	if len(sess.Videos)+len(sess.Files)+len(sess.Images) != 0 {
		t.Fatal("a refused file was queued to send")
	}
	if _, err := AttachWorkspaceFile(sess, "generated_music-2.mp3", "", false); err != nil {
		t.Errorf("the copy itself: %v", err)
	}
	// "-final" is not a free-name number: only the warning.
	if out, err := AttachWorkspaceFile(sess, "notes.txt", "", false); err != nil || !strings.Contains(out, "NOT one your tools produced") {
		t.Errorf("notes.txt: %q %v", out, err)
	}
	// A turn that made both names itself sends either.
	both := &ToolSession{Username: "alice", WorkspaceDir: dir}
	both.AddStagedFiles([]string{"generated_music.mp3", "generated_music-2.mp3"})
	if _, err := AttachWorkspaceFile(both, "generated_music.mp3", "", false); err != nil {
		t.Errorf("a file this turn made itself was refused: %v", err)
	}
	later := &ToolSession{Username: "alice", WorkspaceDir: dir}
	if _, err := AttachWorkspaceFile(later, "generated_music.mp3", "", false); err != nil {
		t.Errorf("on a turn that made no copy, the older file is refused: %v", err)
	}
}
