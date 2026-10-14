// Package bgrun runs tool calls that move to the background while they are
// running.
//
// The detach in core/task_detach.go is decided BEFORE a call starts, from how long
// the tool says it will take. That suits a render, whose backend knows its own
// deadline. It cannot suit a command: an extractor, an unpacker, an app run
// against a folder of logs takes as long as its input makes it take, and
// nothing knows that up front. So those ran inline under one fixed ceiling,
// ninety seconds, and anything slower was killed however much work it was
// doing.
//
// This is the other half. The call starts inline, as any call does. If it is
// still running at the detach time, it moves to the background: the turn gets
// a notice and carries on, the run keeps going, and its result arrives in the
// conversation when it ends. A total ceiling is the wrong question for that
// kind of work, so the limit is on SILENCE instead: a run that has printed
// nothing for the no-output limit is taken to be stuck and stopped, and one
// that keeps reporting keeps going. A tool that declared its own timeout keeps
// it as a ceiling as well.
//
// The run uses a session built to outlive the turn from the very start (see
// core.ToolSession.ForDetachedTask), because the moment it detaches is not known
// when it begins, and a call cannot change sessions halfway through. When it
// finishes inline instead, what it attached is handed to the turn's session,
// so an inline call looks exactly as it did before.
package bgrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_tool_detach_after", Category: "Timeouts",
		Label:  "Move a running tool to the background after",
		Help:   "A command-running tool still going after this long continues in the background.",
		Detail: "The conversation carries on and the result arrives as its own message when the run ends, with progress and a Stop button in the live pill meanwhile. On a messaging conversation the messaging detach threshold applies when it is shorter, since there the person sees nothing until a reply arrives.",
		Kind:   KindSeconds, Default: 60, Min: 10, Max: 3600,
	})
	RegisterTunable(TunableSpec{
		Key: "tune_tool_idle_limit", Category: "Timeouts",
		Label:  "Stop a running tool after no output for",
		Help:   "A command-running tool that prints nothing for this long is taken to be stuck and stopped.",
		Detail: "There is no limit on total time for a run that keeps printing; a tool that sets its own timeout keeps that as a ceiling too. What it printed before it was stopped is returned with the reason.",
		Kind:   KindSeconds, Default: 300, Min: 30, Max: 7200,
	})
}

// errRunIdle is the cause a run is stopped with when it has printed nothing
// for the no-output limit.
var errRunIdle = errors.New("no output for too long")

// errRunCeiling is the cause a run is stopped with when it reaches the
// timeout its tool declared.
var errRunCeiling = errors.New("reached its own timeout")

// RunActivity is what a running call has said so far: when it last said
// anything, how much, and its latest line. It is an io.Writer, so a command's
// output can be copied into it as it is produced (sandbox.ShellRun.Tee).
type RunActivity struct {
	mu      sync.Mutex
	started time.Time
	last    time.Time
	bytes   int64
	lines   int
	partial []byte // the line being written, until its newline
	tail    string // the latest complete, non-blank line
}

// newRunActivity starts the clock on a run that has said nothing yet.
func newRunActivity() *RunActivity {
	now := time.Now()
	return &RunActivity{started: now, last: now}
}

// Write records output. Never fails, so it cannot break the command it tees.
func (a *RunActivity) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = time.Now()
	a.bytes += int64(len(p))
	for _, b := range p {
		if b != '\n' {
			if len(a.partial) < 4096 {
				a.partial = append(a.partial, b)
			}
			continue
		}
		a.lines++
		if line := strings.TrimSpace(string(a.partial)); line != "" {
			a.tail = line
		}
		a.partial = a.partial[:0]
	}
	return len(p), nil
}

// Quiet is how long since the run last printed anything (or started).
func (a *RunActivity) Quiet() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.last)
}

// Summary is a short progress line for a person: how much it has printed and
// its latest line, "1,204 lines - unpacking part 3 of 8". Empty until the run
// prints something.
func (a *RunActivity) Summary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bytes == 0 {
		return ""
	}
	tail := a.tail
	if p := strings.TrimSpace(string(a.partial)); p != "" {
		tail = p // a progress bar redraws one line without ending it
	}
	if i := strings.LastIndexByte(tail, '\r'); i >= 0 {
		tail = strings.TrimSpace(tail[i+1:])
	}
	if utf8.RuneCountInString(tail) > 80 {
		tail = string([]rune(tail)[:79]) + "…"
	}
	head := countOf(a.lines, "line")
	if a.lines == 0 {
		head = humanBytes(a.bytes)
	}
	if tail == "" {
		return head
	}
	return head + " - " + tail
}

func countOf(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return commaInt(n) + " " + word + "s"
}

func commaInt(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

type runActivityKey struct{}

// RunActivityFrom is the activity a background-capable run is reporting to,
// or nil when this call is not one. A dispatcher that finds one tees its
// command's output into it and leaves the time limits to the run.
func RunActivityFrom(ctx context.Context) *RunActivity {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(runActivityKey{}).(*RunActivity)
	return a
}

// BackgroundRun is one tool call that may move to the background while it
// runs. Struct-first because the call, its limits and its work travel
// together from the decision to the delivery.
type BackgroundRun struct {
	// Tool is what the model called, for the notice and the log.
	Tool string
	// Label is how the run is named in the live pill. Empty means Tool.
	Label string
	// Ceiling is the tool's own declared timeout, kept as a hard limit. Zero
	// means none: the no-output limit is the only one.
	Ceiling time.Duration
	// Run does the work. sess outlives the turn and its context carries the
	// activity (RunActivityFrom) the work reports its output to.
	Run func(sess *ToolSession) (string, error)
}

type taskProgressKey struct{}

// WithTaskProgress is how a task host hears a background run's progress: fn
// receives each line ReportTaskProgress is given under ctx. The host shows the
// latest one beside the run in the live pill.
func WithTaskProgress(ctx context.Context, fn func(line string)) context.Context {
	return context.WithValue(ctx, taskProgressKey{}, fn)
}

// ReportTaskProgress says how far a background run has got, in a short line
// for a person ("1,204 lines - unpacking part 3 of 8"). No-op outside a task,
// and safe to call as often as the run likes: only the latest line is kept.
func ReportTaskProgress(ctx context.Context, line string) {
	if fn, _ := ctx.Value(taskProgressKey{}).(func(string)); fn != nil {
		fn(line)
	}
}

// words is a duration as a person says it: "5 minutes", "90 seconds".
func words(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return countOf(int(d/time.Hour), "hour")
	case d >= time.Minute && d%time.Minute == 0:
		return countOf(int(d/time.Minute), "minute")
	}
	return countOf(int(d.Round(time.Second)/time.Second), "second")
}

// runResult is what Run returned.
type runResult struct {
	out string
	err error
}

// toolDetachAfter is how long a run stays inline on this surface.
func toolDetachAfter(sess *ToolSession) time.Duration {
	d := TuneDuration("tune_tool_detach_after")
	if sess != nil && sess.Unattended() {
		if u := TuneDuration("tune_task_detach_threshold_unattended"); u > 0 && (d <= 0 || u < d) {
			d = u
		}
	}
	return d
}

// Start runs the call: inline until the detach time, then in the background
// when the host can take it there. It returns what the call produced, or,
// once detached, the notice that it has started and will report back.
func (b BackgroundRun) Start(sess *ToolSession) (string, error) {
	if b.Run == nil {
		return "", fmt.Errorf("%s: nothing to run", b.Tool)
	}
	label := b.Label
	if label == "" {
		label = b.Tool
	}
	act := newRunActivity()
	// Not the turn's context: the run may outlive the turn. Its values travel
	// (the parent run, the lane), its cancellation does not, and the link to
	// the turn below gives a Stop pressed while it is still inline its effect.
	base, cancel := context.WithCancelCause(context.WithoutCancel(sess.Context()))
	runCtx := context.Context(base)
	stopCeiling := context.CancelFunc(func() {})
	if b.Ceiling > 0 {
		runCtx, stopCeiling = context.WithTimeoutCause(runCtx, b.Ceiling, errRunCeiling)
	}
	// release ends the run's context once the run is over, whoever is
	// holding it by then: this call when it finishes inline, the task when it
	// was detached.
	release := func() { stopCeiling(); cancel(nil) }
	runCtx = context.WithValue(runCtx, runActivityKey{}, act)
	stopRun := func() { cancel(context.Canceled) }
	unlink := context.AfterFunc(sess.Context(), stopRun)

	done := make(chan runResult, 1)
	work := sess.ForDetachedTask(runCtx)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- runResult{err: fmt.Errorf("%s panicked: %v", b.Tool, r)}
			}
		}()
		out, err := b.Run(work)
		done <- runResult{out: out, err: err}
	}()
	go watchIdle(runCtx, act, TuneDuration("tune_tool_idle_limit"), cancel)

	// inline waits the run out here, still stoppable by the turn, and hands
	// what it attached to the turn's session.
	inline := func() (string, error) {
		defer release()
		r := <-done
		handOver(work, sess)
		return stopNote(r.out, runCtx, b), r.err
	}

	detachAfter := toolDetachAfter(sess)
	if TaskRunnerFunc == nil || detachAfter <= 0 || sess.DeliverySession() == "" {
		defer unlink()
		return inline()
	}
	timer := time.NewTimer(detachAfter)
	select {
	case r := <-done:
		timer.Stop()
		unlink()
		done <- r
		return inline()
	case <-timer.C:
	}
	if !unlink() {
		// The turn was stopped at the same moment: the run is already being
		// cancelled. Wait for it rather than detach work nobody wants.
		return inline()
	}
	run, err := TaskRunnerFunc(sess, label, func(taskCtx context.Context) (TaskProduct, error) {
		defer release()
		stop := context.AfterFunc(taskCtx, stopRun)
		defer stop()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case r := <-done:
				return TaskProduct{
					Text:   stopNote(r.out, runCtx, b),
					Images: work.ClaimUnflushedImages(),
					Videos: work.ClaimUnflushedVideos(),
					Files:  work.ClaimUnflushedFiles(),
				}, r.err
			case <-tick.C:
				ReportTaskProgress(taskCtx, act.Summary())
			}
		}
	})
	if err != nil {
		// No host could take it. Keep waiting here, still stoppable by the
		// turn, rather than abandon work that is under way.
		Debug("[task] %s stayed inline past the detach time: %v", b.Tool, err)
		defer context.AfterFunc(sess.Context(), stopRun)()
		return inline()
	}
	Debug("[task] %s moved to the background after %s as %s", b.Tool, detachAfter, run.ID)
	return run.StartedNotice(), nil
}

// watchIdle stops the run when it has printed nothing for limit. It ends with
// the run.
func watchIdle(ctx context.Context, act *RunActivity, limit time.Duration, cancel context.CancelCauseFunc) {
	if limit <= 0 {
		return
	}
	step := limit / 10
	if step < time.Second {
		step = time.Second
	}
	if step > 5*time.Second {
		step = 5 * time.Second
	}
	tick := time.NewTicker(step)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if act.Quiet() >= limit {
				cancel(errRunIdle)
				return
			}
		}
	}
}

// stopNote says why a run that was stopped by a limit ended, after whatever it
// printed. Nothing is added to a run that ended by itself or was stopped by a
// person, who already knows.
func stopNote(out string, ctx context.Context, b BackgroundRun) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errRunIdle):
		out += fmt.Sprintf("\n[STOPPED: it printed nothing for %s, so it was taken to be stuck and killed. What it printed before that is above. If this command is normally silent for that long, the no-output limit is an admin setting (Timeouts).]", words(TuneDuration("tune_tool_idle_limit")))
	case errors.Is(cause, errRunCeiling):
		out += fmt.Sprintf("\n[STOPPED: it reached the %s timeout %s declares, and was killed.]", words(b.Ceiling), b.Tool)
	}
	return out
}

// handOver gives the turn's session what the run attached, when it finished
// inline: the reply carrying the result collects them from there.
func handOver(from, to *ToolSession) {
	if from == nil || to == nil || from == to {
		return
	}
	for _, img := range from.ClaimUnflushedImages() {
		to.AppendImage(img)
	}
	for _, v := range from.ClaimUnflushedVideos() {
		to.AppendVideo(v)
	}
	for _, f := range from.ClaimUnflushedFiles() {
		to.AppendFile(f)
	}
}
