package temptool

// A script that saved its result names the file and the dispatcher attaches
// it. Before, a tool that saved a song and printed the path delivered nothing,
// and in the background it came back with nothing attached.

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAFileTheScriptSavedIsAttached(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file just outside the working directory: refused for where it is,
	// not because it is missing.
	if err := os.WriteFile(filepath.Join(root, "outside.mp3"), []byte("ID3 someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	song, cover := []byte("ID3 not really a song"), []byte("\x89PNG not really a picture")
	for name, b := range map[string][]byte{"song.mp3": song, "cover.png": cover, "notes.txt": []byte("lyrics")} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sess := &ToolSession{}
	out := extractFileAttachMarkers("Rendered.\n<<<ATTACH_FILE:song.mp3>>>\n<<<ATTACH_FILE: cover.png >>>\n"+
		"<<<ATTACH_FILE:notes.txt>>>\n<<<ATTACH_FILE:../outside.mp3>>>\n<<<ATTACH_FILE:missing.mp3>>>", sess, dir)

	if len(sess.Videos) != 1 || sess.Videos[0] != base64.StdEncoding.EncodeToString(song) {
		t.Errorf("the song should ride out as the session's audio: %d attached", len(sess.Videos))
	}
	if len(sess.Images) != 1 || sess.Images[0] != base64.StdEncoding.EncodeToString(cover) {
		t.Errorf("the cover should ride out as the session's image: %d attached", len(sess.Images))
	}
	for _, want := range []string{"[attached song.mp3]", "[attached cover.png]",
		"[not attached: notes.txt is not an image, audio or video file]",
		"[not attached: ../outside.mp3", "[not attached: missing.mp3 is not a file"} {
		if !strings.Contains(out, want) {
			t.Errorf("the model should read %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<<<ATTACH_FILE:") {
		t.Errorf("a marker was left for the model to repeat:\n%s", out)
	}

	plain := "Saved the song to song.mp3."
	if got := extractFileAttachMarkers(plain, sess, dir); got != plain {
		t.Errorf("output without a marker should pass through: %q", got)
	}
}

// The music tool's case end to end: a real shell tool saves its song, is still
// running at the detach time, and prints the marker when done. The delivered
// product carries the song, which is what the wake attaches.
func TestASongSavedInTheBackgroundComesBackAttached(t *testing.T) {
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, "tune_tool_detach_after", 0.3)
	tdb.Set(WebTable, "tune_disk_floor_gb", 1.0)
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })

	delivered := make(chan TaskProduct, 1)
	prev := TaskRunnerFunc
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func(ctx context.Context) (TaskProduct, error)) (TaskRun, error) {
		go func() {
			out, _ := fn(context.Background())
			delivered <- out
		}()
		return TaskRun{ID: "task-7", Label: label}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = prev })

	// Outside /tmp, as in the background test: the command gets a fresh /tmp.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for a workspace outside /tmp")
	}
	ws, err := os.MkdirTemp(cache, "gohort-attach-test-")
	if err != nil {
		t.Skip("cannot make a workspace outside /tmp: ", err)
	}
	t.Cleanup(func() { os.RemoveAll(ws) })
	sess := &ToolSession{Ctx: context.Background(), Username: "alice", ChatSessionID: "chat-1", WorkspaceDir: ws}
	music := &TempTool{Name: "generate_music", Mode: TempToolModeShell, Description: "Make a song.",
		CommandTemplate: "cd {workspace_dir} && printf 'ID3 drain' > generated_music.mp3 && sleep 0.8 && echo 'Saved.' && echo '<<<ATTACH_FILE:generated_music.mp3>>>'"}
	out, err := agentToolFromTemp(sess, music).Handler(sess.Ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if notice, marked := TakeFrameworkResultMark(out); !marked || !strings.Contains(notice, "task-7") {
		t.Fatalf("the song should have moved to the background: %q", out)
	}
	select {
	case got := <-delivered:
		if len(got.Videos) != 1 || got.Videos[0] != base64.StdEncoding.EncodeToString([]byte("ID3 drain")) {
			t.Errorf("the background run came back without the song: %d attached, text %q", len(got.Videos), got.Text)
		}
		if !strings.Contains(got.Text, "[attached generated_music.mp3]") {
			t.Errorf("the wake should say what was attached: %q", got.Text)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the background run never delivered")
	}
}
