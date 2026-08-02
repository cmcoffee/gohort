package core

// Detaching a slow tool call. The decision is the framework's — a tool reports
// how long it expects to take and anything past the threshold runs in the
// background — so these pin the decision rule and the notice the model gets
// back, which is the part most able to go wrong.

import (
	"context"
	"strings"
	"testing"
	"time"
)

type slowTool struct {
	dur    time.Duration
	ran    chan struct{}
	result string
}

func (s *slowTool) Name() string                 { return "slow_tool" }
func (s *slowTool) Desc() string                 { return "slow" }
func (s *slowTool) Params() map[string]ToolParam { return map[string]ToolParam{} }
func (s *slowTool) Run(map[string]any) (string, error) {
	if s.ran != nil {
		close(s.ran)
	}
	return s.result, nil
}
func (s *slowTool) ExpectedDuration(map[string]any, *ToolSession) time.Duration { return s.dur }

// withTaskRunner installs a runner that executes inline, so a test can observe
// the detach decision without a scheduler.
func withTaskRunner(t *testing.T) *[]string {
	t.Helper()
	saved := TaskRunnerFunc
	var started []string
	TaskRunnerFunc = func(_ *ToolSession, label string, fn func(context.Context) (string, error)) (TaskRun, error) {
		started = append(started, label)
		go fn(context.Background())
		return TaskRun{ID: "task_1", Label: label}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = saved })
	return &started
}

func TestSlowCallsDetachAndFastOnesDoNot(t *testing.T) {
	withTaskRunner(t)
	sess := &ToolSession{}
	threshold := taskDetachThreshold()

	if _, ok := ShouldDetach(&slowTool{dur: threshold + time.Minute}, nil, sess); !ok {
		t.Error("a call past the threshold must detach")
	}
	if _, ok := ShouldDetach(&slowTool{dur: threshold - time.Second}, nil, sess); ok {
		t.Error("a call under the threshold must stay inline")
	}
	// Zero means the tool has no estimate — the safe reading is "inline".
	if _, ok := ShouldDetach(&slowTool{dur: 0}, nil, sess); ok {
		t.Error("no estimate must not detach")
	}
	// A tool that doesn't implement the interface at all.
	if _, ok := ShouldDetach(&staticProbeTool{}, nil, sess); ok {
		t.Error("a tool with no duration estimate must stay inline")
	}
}

func TestDetachNeedsAHostThatCanRunTasks(t *testing.T) {
	// Unset runner = the behaviour before any of this existed. A host that
	// hasn't wired it must not lose calls.
	saved := TaskRunnerFunc
	TaskRunnerFunc = nil
	t.Cleanup(func() { TaskRunnerFunc = saved })
	if _, ok := ShouldDetach(&slowTool{dur: time.Hour}, nil, &ToolSession{}); ok {
		t.Error("no runner installed must mean no detaching")
	}
	if _, ok := ShouldDetach(&slowTool{dur: time.Hour}, nil, nil); ok {
		t.Error("no session must mean no detaching")
	}
}

func TestDetachedCallStillRunsAndReturnsANotice(t *testing.T) {
	started := withTaskRunner(t)
	ran := make(chan struct{})
	tool := &slowTool{dur: taskDetachThreshold() + time.Hour, ran: ran, result: "the real result"}

	def := ChatToolToAgentToolDefWithSession(tool, &ToolSession{})
	out, err := def.Handler(map[string]any{"action": "edit", "prompt": "make it snowy"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the work never started — detaching must not drop the call")
	}
	if len(*started) != 1 {
		t.Fatalf("runner saw %d tasks, want 1", len(*started))
	}
	// The label has to name the work, or the live surface and the wake message
	// both say "slow_tool" and nothing else.
	if !strings.Contains((*started)[0], "make it snowy") {
		t.Errorf("label = %q, should describe the work", (*started)[0])
	}
	// The notice must not read like a result.
	if strings.Contains(out, "the real result") {
		t.Error("the caller must NOT get the result — it isn't ready")
	}
	for _, want := range []string{"STARTED, NOT FINISHED", "NO result yet", "do NOT call this tool again"} {
		if !strings.Contains(out, want) {
			t.Errorf("notice missing %q:\n%s", want, out)
		}
	}
}

func TestFailureToDetachFallsBackToRunningInline(t *testing.T) {
	// If the host can't start a task — no chat session to deliver into, say —
	// a slow answer beats no answer. The user never asked for this machinery.
	saved := TaskRunnerFunc
	TaskRunnerFunc = func(*ToolSession, string, func(context.Context) (string, error)) (TaskRun, error) {
		return TaskRun{}, errNoTaskHost
	}
	t.Cleanup(func() { TaskRunnerFunc = saved })

	tool := &slowTool{dur: taskDetachThreshold() + time.Hour, result: "inline result"}
	def := ChatToolToAgentToolDefWithSession(tool, &ToolSession{})
	out, err := def.Handler(map[string]any{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out != "inline result" {
		t.Errorf("out = %q, want the tool to have run inline", out)
	}
}

func TestDurationReadsLikeAPersonWroteIt(t *testing.T) {
	// The model repeats this to the user, so "900s" is the wrong register.
	cases := map[time.Duration]string{
		45 * time.Second:  "45 seconds",
		15 * time.Minute:  "15 minutes",
		150 * time.Minute: "2.5 hours",
	}
	for d, want := range cases {
		if got := humanizeTaskDuration(d); got != want {
			t.Errorf("%v → %q, want %q", d, got, want)
		}
	}
}

func TestGenerateStaysInlineAndEditDetaches(t *testing.T) {
	// The bug this guards: a tool's estimate is its DEADLINE, not its duration.
	// At a 120s threshold the 180s generate ceiling cleared it, so every image
	// generation detached — including ones that finish in twenty seconds, which
	// would hand back "STARTED, NOT FINISHED" and deliver as a separate message.
	//
	// Pinned against the real defaults, so moving either one without thinking
	// about the other fails here rather than in a conversation.
	threshold := taskDetachThreshold()
	genDeadline := TuneDuration("tune_image_poll_max_secs")
	editDeadline := TuneDuration("tune_image_edit_poll_max_secs")

	if genDeadline >= threshold {
		t.Errorf("a plain image generate (%v ceiling) would detach at a %v threshold — it usually finishes in seconds", genDeadline, threshold)
	}
	if editDeadline < threshold {
		t.Errorf("an image edit (%v ceiling) would stay inline at a %v threshold — that is the case detaching exists for", editDeadline, threshold)
	}
}

func TestDetachedWorkSurvivesTheTurnEnding(t *testing.T) {
	// The bug this exists for: the turn does `defer cancel()` on its context,
	// and a detached call closing over the turn's session still reads
	// sess.Context() — which the governed dispatch and the image poll loop both
	// honour, deliberately, so Stop reaches them. So the work died milliseconds
	// after the turn returned, and the user got a failure or nothing at all.
	turnCtx, endTurn := context.WithCancel(context.Background())
	turnSess := &ToolSession{Ctx: turnCtx, Username: "alice", WorkspaceDir: "/tmp/ws"}

	saw := make(chan context.Context, 1)
	saved := TaskRunnerFunc
	taskCtx, cancelTask := context.WithCancel(context.Background())
	defer cancelTask()
	TaskRunnerFunc = func(_ *ToolSession, _ string, fn func(context.Context) (string, error)) (TaskRun, error) {
		go fn(taskCtx)
		return TaskRun{ID: "task_1"}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = saved })

	tool := &ctxCapturingTool{dur: taskDetachThreshold() + time.Hour, saw: saw}
	def := ChatToolToAgentToolDefWithSession(tool, turnSess)
	if _, err := def.Handler(map[string]any{}); err != nil {
		t.Fatalf("handler: %v", err)
	}

	var got context.Context
	select {
	case got = <-saw:
	case <-time.After(2 * time.Second):
		t.Fatal("detached work never ran")
	}

	// The turn ends. This is the moment that used to kill the work.
	endTurn()
	if turnCtx.Err() == nil {
		t.Fatal("test setup: the turn context should be cancelled")
	}
	if got.Err() != nil {
		t.Fatal("detached work was cancelled when the turn ended — it must outlive it")
	}
	// And cancelling the TASK still reaches it, or Stop has nothing to grab.
	cancelTask()
	if got.Err() == nil {
		t.Error("cancelling the task must reach the detached work")
	}
}

func TestDetachedSessionKeepsIdentityAndDropsDeadSurfaces(t *testing.T) {
	fired := false
	turn := &ToolSession{
		Ctx: context.Background(), Username: "alice", AgentID: "a1",
		WorkspaceDir: "/tmp/ws", ChatSessionID: "s1",
		DeniedCredentials: map[string]bool{"x": true},
		Network:           NewNetworkConnector(false),
		StatusCallback:    func(string) { fired = true },
		ConnectPrompt:     func(string) { fired = true },
	}
	taskCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := turn.ForDetachedTask(taskCtx)

	// Identity and authority carry: without these the work cannot find the
	// user's workspace or stay inside their egress gate.
	if d.Username != "alice" || d.AgentID != "a1" || d.WorkspaceDir != "/tmp/ws" || d.ChatSessionID != "s1" {
		t.Errorf("identity did not carry over: %+v", d)
	}
	if !d.DeniedCredentials["x"] {
		t.Error("credential denials must carry — a detached call is not more privileged")
	}
	if d.Network != turn.Network {
		t.Error("the network gate must be SHARED, so a Private toggle still reaches detached work")
	}
	// Surfaces do not: the SSE stream is closed and nobody is watching a turn
	// that ended. Every one of these documents a nil fallback.
	if d.StatusCallback != nil || d.ConnectPrompt != nil {
		t.Error("callbacks wired to the finished turn must not carry over")
	}
	if fired {
		t.Error("no surface callback should have fired")
	}
	// Output accumulators start empty — appending to the turn's would race a
	// reply that has already been sent.
	if len(d.Images) != 0 || len(d.Files) != 0 {
		t.Error("output accumulators must start empty")
	}
	if d.Ctx != taskCtx {
		t.Error("the detached session must run on the TASK's context, not the turn's")
	}
}

type ctxCapturingTool struct {
	dur time.Duration
	saw chan context.Context
}

func (c *ctxCapturingTool) Name() string                 { return "ctx_tool" }
func (c *ctxCapturingTool) Desc() string                 { return "d" }
func (c *ctxCapturingTool) Params() map[string]ToolParam { return map[string]ToolParam{} }
func (c *ctxCapturingTool) Run(map[string]any) (string, error) {
	return "", nil
}
func (c *ctxCapturingTool) RunWithSession(_ map[string]any, sess *ToolSession) (string, error) {
	c.saw <- sess.Context()
	return "done", nil
}
func (c *ctxCapturingTool) ExpectedDuration(map[string]any, *ToolSession) time.Duration {
	return c.dur
}
