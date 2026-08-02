package core

// Detaching a slow tool call. The decision is the framework's — a tool reports
// how long it expects to take and anything past the threshold runs in the
// background — so these pin the decision rule and the notice the model gets
// back, which is the part most able to go wrong.

import (
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
	TaskRunnerFunc = func(_ *ToolSession, label string, fn func() (string, error)) (TaskRun, error) {
		started = append(started, label)
		go fn()
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
	TaskRunnerFunc = func(*ToolSession, string, func() (string, error)) (TaskRun, error) {
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
