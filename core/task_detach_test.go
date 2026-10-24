package core

// The hand-built path. A tool an app assembles as an AgentToolDef never touched
// the detach wrapper, so the longest-running call the framework has — a fleet
// dispatch — could not go to the background at all. These prove the policy path
// behaves identically to the ChatTool one it was extracted from.

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func stubTaskRunner(t *testing.T) *TaskProduct {
	t.Helper()
	saved := TaskRunnerFunc
	var got TaskProduct
	TaskRunnerFunc = func(_ *ToolSession, _ string, fn func(context.Context) (TaskProduct, error)) (TaskRun, error) {
		p, _ := fn(context.Background())
		got = p
		return TaskRun{ID: "task-9", Label: "stub"}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = saved })
	return &got
}

func TestAHandBuiltToolDetachesOnTheSameTerms(t *testing.T) {
	product := stubTaskRunner(t)
	sess := &ToolSession{ChatSessionID: "sess-policy"}

	ran := false
	h := WrapDetachable(DetachPolicy{
		Tool:     "agents",
		Expected: func(map[string]any, *ToolSession) time.Duration { return time.Hour },
		Detached: func(_ map[string]any, d *ToolSession) (string, error) {
			ran = true
			if !d.Detached {
				t.Error("the work must run against a session built to outlive the turn")
			}
			d.AppendImage("AAAA")
			return "the sub-agent's answer", nil
		},
	}, sess, func(context.Context, map[string]any) (string, error) {
		t.Error("it should not have run inline")
		return "", nil
	})

	out, err := h(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !ran {
		t.Fatal("the detached work never ran")
	}
	if text, marked := TakeFrameworkResultMark(out); !marked || !strings.Contains(text, "STARTED, NOT FINISHED") {
		t.Errorf("the model must get the framework's own detach notice, unfenced:\n%s", out)
	}
	// Whatever it attached has to travel with the result, or the work is done,
	// announced, and never delivered.
	if product.Text != "the sub-agent's answer" || len(product.Images) != 1 {
		t.Errorf("the product must carry text and attachments: %+v", *product)
	}
}

func TestAHandBuiltToolStaysInlineBelowTheThreshold(t *testing.T) {
	stubTaskRunner(t)
	inlineRan := false
	h := WrapDetachable(DetachPolicy{
		Tool:     "agents",
		Expected: func(map[string]any, *ToolSession) time.Duration { return time.Second },
		Detached: func(map[string]any, *ToolSession) (string, error) {
			t.Error("a one-second call must not detach")
			return "", nil
		},
	}, &ToolSession{ChatSessionID: "s"}, func(context.Context, map[string]any) (string, error) {
		inlineRan = true
		return "answered", nil
	})
	if _, err := h(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !inlineRan {
		t.Error("the inline path must still be the default")
	}
}

// Same surface rule as every other tool: on a text thread the threshold is low,
// because the person sees nothing at all while they wait.
func TestAHandBuiltToolFollowsTheSurfaceThreshold(t *testing.T) {
	stubTaskRunner(t)
	p := DetachPolicy{
		Tool:     "agents",
		Expected: func(map[string]any, *ToolSession) time.Duration { return 90 * time.Second },
		Detached: func(map[string]any, *ToolSession) (string, error) { return "done", nil },
	}
	if shouldDetachPolicy(p, nil, &ToolSession{}) {
		t.Error("a watched chat can show 90s of work happening")
	}
	if !shouldDetachPolicy(p, nil, &ToolSession{ChannelChatID: "c"}) {
		t.Error("90s of silence on a text thread is an assistant that looks gone")
	}
}

// Preflight must run BEFORE the slot is claimed, or a call rejected on its
// arguments burns the turn's one background job.
func TestAHandBuiltToolPreflightsBeforeClaimingTheSlot(t *testing.T) {
	stubTaskRunner(t)
	sess := &ToolSession{ChatSessionID: "sess-policy-2"}
	h := WrapDetachable(DetachPolicy{
		Tool:      "agents",
		Expected:  func(map[string]any, *ToolSession) time.Duration { return time.Hour },
		Preflight: func(map[string]any, *ToolSession) error { return errTestPreflight },
		Detached: func(map[string]any, *ToolSession) (string, error) {
			t.Error("preflight failed — nothing should have started")
			return "", nil
		},
	}, sess, func(context.Context, map[string]any) (string, error) { return "", nil })

	if _, err := h(context.Background(), map[string]any{}); err != errTestPreflight {
		t.Fatalf("err = %v, want the preflight error surfaced inline", err)
	}
	if _, free := sess.ClaimDetachSlot("agents"); !free {
		t.Error("a rejected call must not have taken the turn's background slot")
	}
}

var errTestPreflight = Error("no agent by that name")

// A picture this turn was about to send that the job builds on is a step
// toward the job's result. Observed 2026-10-24: a four-photo blend sent its
// first pass with "the next pass is running", then its second with the same.
func TestAPictureTheJobBuildsOnIsKeptBackForIt(t *testing.T) {
	product := stubTaskRunner(t)
	sess := &ToolSession{ChatSessionID: "sess-carry"}
	step := []byte("first pass")
	sess.AppendImage(base64.StdEncoding.EncodeToString([]byte("delivered earlier")))
	sess.ClaimUnflushedImages()
	sess.AppendImage(base64.StdEncoding.EncodeToString(step))
	sess.AppendImage(base64.StdEncoding.EncodeToString([]byte("something else")))

	h := WrapDetachable(DetachPolicy{
		Tool:   "image",
		Always: func(map[string]any, *ToolSession) bool { return true },
		Inputs: func(map[string]any, *ToolSession) ([]string, [][]byte) {
			return []string{"image#r.0001", "find-jelly.jpg"}, [][]byte{step, []byte("jelly"), []byte("delivered earlier")}
		},
		Detached: func(map[string]any, *ToolSession) (string, error) { return "done", nil },
	}, sess, func(context.Context, map[string]any) (string, error) { return "", nil })
	if _, err := h(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if len(sess.Images) != 2 || sess.Images[1] != base64.StdEncoding.EncodeToString([]byte("something else")) {
		t.Errorf("the step should leave this turn's pictures, the rest stay: %q", sess.Images)
	}
	if len(product.Carried) != 1 || product.Carried[0] != base64.StdEncoding.EncodeToString(step) {
		t.Errorf("the step should ride with the job: %q", product.Carried)
	}
	if strings.Join(product.Inputs, ",") != "image#r.0001,find-jelly.jpg" {
		t.Errorf("the result should name what went in: %q", product.Inputs)
	}
}

// A finished pass's picture, carried by the turn it woke, is a step when that
// turn starts another pass, even one that does not take it. Observed
// 2026-10-24: a blend done in pairs sent its first pair with "final pass is
// running", the second pair rendering on its own.
func TestAFinishedPassIsAStepWhenAnotherStarts(t *testing.T) {
	product := stubTaskRunner(t)
	sess := &ToolSession{ChatSessionID: "sess-steps"}
	pair := base64.StdEncoding.EncodeToString([]byte("first pair"))
	sess.AppendImage(pair)
	sess.OfferAsSteps([]string{pair})

	h := WrapDetachable(DetachPolicy{
		Tool:   "image",
		Always: func(map[string]any, *ToolSession) bool { return true },
		Inputs: func(map[string]any, *ToolSession) ([]string, [][]byte) {
			return []string{"find-a.jpg"}, [][]byte{[]byte("a")}
		},
		Detached: func(map[string]any, *ToolSession) (string, error) { return "done", nil },
	}, sess, func(context.Context, map[string]any) (string, error) { return "", nil })
	if _, err := h(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if len(sess.Images) != 0 || len(product.Carried) != 1 || product.Carried[0] != pair {
		t.Errorf("the first pair should ride with the next pass: sending %d, carried %d", len(sess.Images), len(product.Carried))
	}
	// Taken once: put back (a detach that fell to inline restores it), it
	// is this turn's to send again.
	sess.AppendImage(pair)
	if again := sess.WithdrawSteps(); len(again) != 0 || len(sess.Images) != 1 {
		t.Error("a step is taken once")
	}
}
