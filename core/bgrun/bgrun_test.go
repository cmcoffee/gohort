package bgrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// setTuneForTest pins tunables to values below their admin minimums, which
// only a test needs: a real run is never asked to detach in a tenth of a
// second. Stored values are read as they are; only the admin page clamps.
func setTuneForTest(t *testing.T, vals map[string]float64) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	for k, v := range vals {
		db.Set(WebTable, k, v)
	}
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })
}

// fakeTaskHost stands in for the app's task runner: it runs the task the way
// the real one does, on a context of its own with a progress reporter, and
// records what was delivered.
type fakeTaskHost struct {
	mu        sync.Mutex
	started   int
	cancel    context.CancelFunc
	progress  []string
	delivered chan TaskProduct
	errs      chan error
}

func installFakeTaskHost(t *testing.T) *fakeTaskHost {
	t.Helper()
	h := &fakeTaskHost{delivered: make(chan TaskProduct, 1), errs: make(chan error, 1)}
	prev := TaskRunnerFunc
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func(ctx context.Context) (TaskProduct, error)) (TaskRun, error) {
		ctx, cancel := context.WithCancel(context.Background())
		h.mu.Lock()
		h.started++
		h.cancel = cancel
		h.mu.Unlock()
		ctx = WithTaskProgress(ctx, func(line string) {
			h.mu.Lock()
			h.progress = append(h.progress, line)
			h.mu.Unlock()
		})
		go func() {
			out, err := fn(ctx)
			h.delivered <- out
			h.errs <- err
		}()
		return TaskRun{ID: "task-1", Label: label}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = prev })
	return h
}

func turnSession(ctx context.Context) *ToolSession {
	return &ToolSession{Ctx: ctx, Username: "u", DeliverySessionID: "chat-1"}
}

// A run that finishes before the detach time is an ordinary call: its output
// comes back in the turn, what it attached lands on the turn's session, and
// no background task is made.
func TestABackgroundRunThatFinishesQuicklyStaysInline(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 5, "tune_tool_idle_limit": 60})
	host := installFakeTaskHost(t)
	sess := turnSession(context.Background())
	out, err := BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		if RunActivityFrom(work.Context()) == nil {
			return "", fmt.Errorf("the run was given no activity to report to")
		}
		work.AppendImage("aW1n")
		return "done", nil
	}}.Start(sess)
	if err != nil || out != "done" {
		t.Fatalf("got %q %v", out, err)
	}
	if host.started != 0 {
		t.Error("a quick run was sent to the background")
	}
	if imgs := sess.ClaimUnflushedImages(); len(imgs) != 1 || imgs[0] != "aW1n" {
		t.Errorf("the attachment did not reach the turn: %v", imgs)
	}
}

// A run still going at the detach time moves to the background: the turn gets
// the framework's notice at once, the run keeps going after the turn has
// ended, reports its progress, and delivers its output and attachments.
func TestARunStillGoingAtTheDetachTimeMovesToTheBackground(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 0.1, "tune_tool_idle_limit": 60})
	host := installFakeTaskHost(t)
	turnCtx, endTurn := context.WithCancel(context.Background())
	sess := turnSession(turnCtx)
	t0 := time.Now()
	out, err := BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		act := RunActivityFrom(work.Context())
		for i := 1; i <= 25; i++ {
			fmt.Fprintf(act, "unpacking part %d\n", i)
			select {
			case <-work.Context().Done():
				return "", work.Context().Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		work.AppendFile(FileAttachment{Name: "report.txt", Data: "cmVwb3J0"})
		return "all 25 parts unpacked", nil
	}}.Start(sess)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) > time.Second {
		t.Errorf("the turn waited %s for a run that detached at 0.1s", time.Since(t0))
	}
	notice, marked := TakeFrameworkResultMark(out)
	if !marked || !strings.Contains(notice, "task-1") {
		t.Fatalf("the turn did not get the detach notice: %q", out)
	}
	endTurn() // the turn is over; the run must not be
	var got TaskProduct
	select {
	case got = <-host.delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("the background run never delivered")
	}
	if e := <-host.errs; e != nil {
		t.Fatalf("the run failed after the turn ended: %v", e)
	}
	if got.Text != "all 25 parts unpacked" || len(got.Files) != 1 {
		t.Errorf("delivered %+v", got)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.progress) == 0 || !strings.Contains(host.progress[len(host.progress)-1], "lines - unpacking part") {
		t.Errorf("no progress was reported: %q", host.progress)
	}
}

// A run that prints nothing for the no-output limit is stopped, and returns
// what it printed with the reason. One that keeps printing is not.
func TestARunThatGoesQuietIsStopped(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 60, "tune_tool_idle_limit": 0.5})
	installFakeTaskHost(t)
	out, err := BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		fmt.Fprintln(RunActivityFrom(work.Context()), "waiting for the archive")
		<-work.Context().Done() // hangs, silent
		return "waiting for the archive", nil
	}}.Start(turnSession(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "waiting for the archive") || !strings.Contains(out, "[STOPPED: it printed nothing and wrote nothing for") {
		t.Errorf("a quiet run was not stopped with its reason: %q", out)
	}

	out, _ = BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		act := RunActivityFrom(work.Context())
		for i := 0; i < 15; i++ { // 1.5s of work, a line every 0.1s
			fmt.Fprintln(act, "still going")
			time.Sleep(100 * time.Millisecond)
			if work.Context().Err() != nil {
				return "killed", nil
			}
		}
		return "finished", nil
	}}.Start(turnSession(context.Background()))
	if out != "finished" {
		t.Errorf("a run that kept printing was stopped: %q", out)
	}
}

// Stop works on both sides of the detach: pressed while the run is inline it
// ends the run, and pressed on the background task it ends the run too.
// Neither is announced as a limit, since the person already knows.
func TestStopReachesTheRunBeforeAndAfterItDetaches(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 60, "tune_tool_idle_limit": 60})
	installFakeTaskHost(t)
	turnCtx, stop := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, stop)
	out, _ := BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		<-work.Context().Done()
		return "stopped inline", nil
	}}.Start(turnSession(turnCtx))
	if out != "stopped inline" {
		t.Errorf("Stop while inline: %q", out)
	}

	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 0.1})
	host := installFakeTaskHost(t)
	if _, err := (BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		<-work.Context().Done()
		return "stopped in the background", nil
	}}).Start(turnSession(context.Background())); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	cancel := host.cancel
	host.mu.Unlock()
	cancel()
	select {
	case got := <-host.delivered:
		if got.Text != "stopped in the background" {
			t.Errorf("Stop on the task: %q", got.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop on the background task did not end the run")
	}
}

// With nowhere to deliver a later result (no chat session), the run stays
// inline however long it takes, rather than finishing into nothing.
func TestARunWithNowhereToDeliverStaysInline(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 0.1, "tune_tool_idle_limit": 60})
	host := installFakeTaskHost(t)
	out, _ := BackgroundRun{Tool: "extract", Run: func(work *ToolSession) (string, error) {
		time.Sleep(400 * time.Millisecond)
		return "done here", nil
	}}.Start(&ToolSession{Ctx: context.Background(), Username: "u"})
	if out != "done here" || host.started != 0 {
		t.Errorf("got %q, started %d background tasks", out, host.started)
	}
}

// The progress line names how much was printed and the latest line, and
// follows a progress bar that redraws one line with carriage returns.
func TestRunActivitySummary(t *testing.T) {
	a := newRunActivity()
	if a.Summary() != "" {
		t.Error("a silent run has a summary")
	}
	fmt.Fprint(a, "reading header\nunpacking 10%\runpacking 40%")
	if got := a.Summary(); got != "1 line - unpacking 40%" {
		t.Errorf("summary = %q", got)
	}
	fmt.Fprint(a, "\n\n")
	if got := a.Summary(); got != "3 lines - unpacking 40%" {
		t.Errorf("summary = %q", got)
	}
}

// A run that writes past the per-run limit is stopped with what it wrote and
// the reason; one whose disk is below the free-space floor is stopped too.
// Both through the real watcher, on a real folder.
func TestARunIsStoppedByItsDiskLimits(t *testing.T) {
	prev := storageCheckEvery
	storageCheckEvery = 50 * time.Millisecond
	t.Cleanup(func() { storageCheckEvery = prev })

	dir := t.TempDir()
	ctx, cancel := context.WithCancelCause(context.Background())
	act := newRunActivity()
	act.WatchDir(dir)
	setTuneForTest(t, map[string]float64{"tune_disk_floor_gb": 1})
	go func() {
		// 4 MB, well past a 64 KB limit; written in pieces as a command would.
		f, _ := os.Create(filepath.Join(dir, "unpacked.bin"))
		defer f.Close()
		chunk := make([]byte, 64<<10)
		for i := 0; i < 64 && ctx.Err() == nil; i++ {
			f.Write(chunk)
			f.Sync()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	watchStorage(ctx, act, 64<<10, cancel)
	if !errors.Is(context.Cause(ctx), errTooLarge) || !strings.Contains(act.stopReason(), "past the") {
		t.Errorf("a run that wrote too much was not stopped: cause=%v why=%q", context.Cause(ctx), act.stopReason())
	}

	setTuneForTest(t, map[string]float64{"tune_disk_floor_gb": 100000}) // more than any test disk has
	ctx2, cancel2 := context.WithCancelCause(context.Background())
	act2 := newRunActivity()
	act2.WatchDir(t.TempDir())
	watchStorage(ctx2, act2, 0, cancel2)
	if !errors.Is(context.Cause(ctx2), errDiskFloor) || !strings.Contains(act2.stopReason(), "kept free for the server") {
		t.Errorf("a run on a disk below the floor was not stopped: cause=%v why=%q", context.Cause(ctx2), act2.stopReason())
	}
	out := stopNote("partial output", ctx2, BackgroundRun{Tool: "unpack"}, act2)
	if !strings.Contains(out, "partial output") || !strings.Contains(out, "[STOPPED: the disk it was writing to has") {
		t.Errorf("the note does not say why: %q", out)
	}
}

// A run that prints nothing but keeps writing into the folder it was pointed
// at (a download growing a file) is working, not stuck; its progress says how
// much it has written.
func TestARunThatKeepsWritingIsNotStopped(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 60, "tune_tool_idle_limit": 0.5})
	prev := growthCheckEvery
	growthCheckEvery = 50 * time.Millisecond
	t.Cleanup(func() { growthCheckEvery = prev })
	installFakeTaskHost(t)
	dir := t.TempDir()
	var summary string
	out, err := BackgroundRun{Tool: "download", Run: func(work *ToolSession) (string, error) {
		act := RunActivityFrom(work.Context())
		act.WatchGrowth(dir)
		act.Note("downloading")
		f, err := os.Create(filepath.Join(dir, "dump.zip"))
		if err != nil {
			return "", err
		}
		defer f.Close()
		for i := 0; i < 15; i++ { // 1.5s, silent, writing every 0.1s
			f.Write(make([]byte, 4096))
			time.Sleep(100 * time.Millisecond)
			if work.Context().Err() != nil {
				return "killed", nil
			}
		}
		summary = act.Summary()
		return "downloaded", nil
	}}.Start(turnSession(context.Background()))
	if err != nil || out != "downloaded" {
		t.Fatalf("a run that kept writing was stopped: %q %v", out, err)
	}
	if !strings.Contains(summary, "wrote ") || !strings.Contains(summary, "downloading") {
		t.Errorf("progress does not say what it wrote: %q", summary)
	}
}

// A run that says it is long moves to the background at its own time, sooner
// than the deployment's.
func TestARunCanDetachSooner(t *testing.T) {
	setTuneForTest(t, map[string]float64{"tune_tool_detach_after": 60, "tune_tool_idle_limit": 60})
	host := installFakeTaskHost(t)
	t0 := time.Now()
	out, err := BackgroundRun{Tool: "download", DetachAfter: 100 * time.Millisecond, Run: func(work *ToolSession) (string, error) {
		time.Sleep(time.Second)
		return "downloaded", nil
	}}.Start(turnSession(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if _, marked := TakeFrameworkResultMark(out); !marked || time.Since(t0) > 900*time.Millisecond {
		t.Fatalf("it did not detach at its own time: %q after %s", out, time.Since(t0))
	}
	if got := <-host.delivered; got.Text != "downloaded" {
		t.Errorf("delivered %+v", got)
	}
}
