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
	"github.com/cmcoffee/gohort/core/filenotes"
	"github.com/cmcoffee/gohort/tools/files"
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

// A turn that produced nothing has nothing of its own to confuse a file with,
// so it is not told the file is not its work, and it is attached: the user
// asking for a file they put there themselves is an ordinary request. But an
// old file says how old it is, because something made a minute ago elsewhere
// (by an agent this one ran) is easily taken for it: "send the attachment"
// sent a nine-day-old song under the new one's name. A recent file is quiet.
func TestAttachingWithNothingStagedSaysOnlyItsAge(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "report.pdf", 48*time.Hour)
	writeWorkspaceFile(t, dir, "fresh.pdf", time.Minute)
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}

	out, err := AttachWorkspaceFile(sess, "report.pdf", "", false)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if strings.Contains(out, "NOT one your tools produced") {
		t.Errorf("nothing was made this turn, so it is not told the file is not its work:\n%s", out)
	}
	if !strings.Contains(out, "last written 2 days ago") {
		t.Errorf("an old file says how old it is:\n%s", out)
	}
	if out, _ := AttachWorkspaceFile(sess, "fresh.pdf", "", false); strings.Contains(out, "NOTE") {
		t.Errorf("a recent file attaches quietly:\n%s", out)
	}
	if len(sess.Images)+len(sess.Files) != 2 {
		t.Error("both are attached: a note, never a refusal")
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

// What a file is follows it to the person and back: the listing shows each
// made file's note and hides the notes' own file, describe names a file,
// attach can name it too, and the attach result says what it sent.
func TestAFilesNoteIsShownAndCanBeSet(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "generated_music.mp3", 9*24*time.Hour)
	writeWorkspaceFile(t, dir, "generated_music-2.mp3", 0)
	filenotes.Record(filepath.Join(dir, "generated_music-2.mp3"), filenotes.Note{By: "generate_music", Request: "make it country rock"})
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}
	sess.AddStagedFiles([]string{"generated_music-2.mp3"})

	if out, err := handleDescribe(map[string]any{"path": "generated_music-2.mp3", "title": "Gas Prices Are Too High"}, sess); err != nil || !strings.Contains(out, `"Gas Prices Are Too High", made by generate_music`) {
		t.Errorf("describe: %v %s", err, out)
	}
	ls, err := (&files.ListDirectoryTool{}).RunWithSession(map[string]any{"path": "."}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ls, filenotes.Sidecar) {
		t.Errorf("the notes' own file is not listed:\n%s", ls)
	}
	var newLine, oldLine string
	for _, l := range strings.Split(ls, "\n") {
		switch {
		case strings.Contains(l, "generated_music-2.mp3"):
			newLine = l
		case strings.Contains(l, "generated_music.mp3"):
			oldLine = l
		}
	}
	if !strings.Contains(newLine, `"Gas Prices Are Too High"`) || !strings.Contains(newLine, `for "make it country rock"`) || strings.Contains(oldLine, "made by") {
		t.Errorf("the listing tells the new song from the old:\n%s", ls)
	}
	out, err := handleAttach(map[string]any{"path": "generated_music-2.mp3"}, sess)
	if err != nil || !strings.Contains(out, `It is "Gas Prices Are Too High", made by generate_music`) {
		t.Errorf("attach says what it sent: %v %s", err, out)
	}
	if _, err := handleAttach(map[string]any{"path": "generated_music.mp3", "title": "the September song"}, sess); err == nil {
		t.Error("the older song is still refused when a renamed copy was made this turn")
	}
	writeWorkspaceFile(t, dir, "chart.png", 0)
	if out, _ := handleAttach(map[string]any{"path": "chart.png", "title": "Q3 sales by region"}, sess); !strings.Contains(out, `It is "Q3 sales by region"`) {
		t.Errorf("attach can name what it sends: %s", out)
	}
}
