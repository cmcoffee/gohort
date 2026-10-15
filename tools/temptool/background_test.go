package temptool

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A real shell tool, called the way an agent calls it: still running at the
// detach time, it moves to the background and delivers what it printed when
// it ends; gone quiet, it is stopped with what it printed and the reason,
// rather than at a fixed ninety seconds.
func TestAShellToolCalledByAnAgentRunsInTheBackground(t *testing.T) {
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, "tune_tool_detach_after", 0.3)
	tdb.Set(WebTable, "tune_tool_idle_limit", 1.0)
	// The disk floor is not under test: a nearly full test disk would stop the
	// command for space before the limits being tested were reached.
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
		return TaskRun{ID: "task-9", Label: label}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = prev })

	// Not t.TempDir(): bubblewrap gives the command a fresh /tmp, so a
	// workspace under it cannot be entered from inside.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for a workspace outside /tmp")
	}
	ws, err := os.MkdirTemp(cache, "gohort-bgrun-test-")
	if err != nil {
		t.Skip("cannot make a workspace outside /tmp: ", err)
	}
	t.Cleanup(func() { os.RemoveAll(ws) })
	turnCtx, endTurn := context.WithCancel(context.Background())
	sess := &ToolSession{Ctx: turnCtx, Username: "alice", ChatSessionID: "chat-1", WorkspaceDir: ws}
	slow := &TempTool{Name: "unpack_case", Mode: TempToolModeShell, Description: "Unpack.",
		CommandTemplate: "for i in 1 2 3 4 5 6; do echo part $i; sleep 0.2; done"}
	out, err := agentToolFromTemp(sess, slow).Handler(turnCtx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if notice, marked := TakeFrameworkResultMark(out); !marked || !strings.Contains(notice, "task-9") {
		t.Fatalf("the agent did not get the background notice: %q", out)
	}
	endTurn()
	select {
	case got := <-delivered:
		if !strings.Contains(got.Text, "part 6") {
			t.Errorf("the background run did not deliver its whole output: %q", got.Text)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the background run never delivered")
	}

	quiet := &TempTool{Name: "wait_forever", Mode: TempToolModeShell, Description: "Hang.",
		CommandTemplate: "echo started; sleep 30"}
	tdb.Set(WebTable, "tune_tool_detach_after", 60.0)
	InvalidateTunables()
	t0 := time.Now()
	next := &ToolSession{Ctx: context.Background(), Username: "alice", ChatSessionID: "chat-1", WorkspaceDir: ws}
	out, err = agentToolFromTemp(next, quiet).Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started") || !strings.Contains(out, "[STOPPED: it showed no sign of work for") {
		t.Errorf("a silent command was not stopped with its reason: %q", out)
	}
	if time.Since(t0) > 15*time.Second {
		t.Errorf("the silent command ran %s; the no-output limit was 1s", time.Since(t0))
	}
}
