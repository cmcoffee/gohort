package temptool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// What an authored tool writes into the session's folder is this turn's, as
// a built-in tool's is: a music tool that saves to the same name every time
// rewrote generated_music.mp3, nothing recorded it, and the song never went
// back to the agent that asked for it. A file it leaves alone is not this
// turn's.
func TestWhatACustomToolWritesIsThisTurns(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for a workspace outside /tmp")
	}
	ws, err := os.MkdirTemp(cache, "gohort-writes-test-")
	if err != nil {
		t.Skip("cannot make a workspace outside /tmp: ", err)
	}
	t.Cleanup(func() { os.RemoveAll(ws) })
	old := time.Now().Add(-time.Hour)
	for _, n := range []string{"generated_music.mp3", "lyrics.txt"} {
		p := filepath.Join(ws, n)
		os.WriteFile(p, []byte("the song from before"), 0600)
		os.Chtimes(p, old, old)
	}
	sess := &ToolSession{Ctx: context.Background(), Username: "alice", ChatSessionID: "chat-1", WorkspaceDir: ws}
	music := &TempTool{Name: "generate_music", Mode: TempToolModeShell, Description: "Make a song.",
		CommandTemplate: "cd {workspace_dir} && printf 'ID3 the new song' > generated_music.mp3 && echo 'Saved generated_music.mp3.'"}
	if _, err := agentToolFromTemp(sess, music).Handler(sess.Ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sess.StagedFiles(), ","); got != "generated_music.mp3" {
		t.Fatalf("staged %q, want the rewritten song alone", got)
	}
}
