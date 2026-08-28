package core

import (
	"context"
	"testing"
)

// sizedLLM is an LLM that reports a context window and nothing else.
type sizedLLM struct{ window int }

func (s sizedLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return &Response{}, nil
}
func (s sizedLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return &Response{}, nil
}
func (s sizedLLM) ContextSize() int { return s.window }

// unsizedLLM does not implement ContextSizer, which is how a model with no
// declared window looks.
type unsizedLLM struct{}

func (unsizedLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return &Response{}, nil
}
func (unsizedLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return &Response{}, nil
}

// A loop is not pinned to one tier — it starts on the route's model and can
// escalate mid-turn — so the budget has to be safe for whichever tier it lands
// on. Only the smaller window is.
//
// This is the number that decides whether history gets compacted at all, and
// getting it wrong fails SILENTLY: the server drops the system prompt, the
// model keeps writing in character out of the history it can still see, and
// stops calling the tools the dropped prompt told it about.
func TestLoopContextSizeTakesTheSmallerTier(t *testing.T) {
	cases := []struct {
		name   string
		worker LLM
		lead   LLM
		want   int
	}{
		{"a small worker bounds a large lead", sizedLLM{32768}, sizedLLM{200000}, 32768},
		{"and the other way round", sizedLLM{200000}, sizedLLM{32768}, 32768},
		{"one tier only", sizedLLM{65536}, nil, 65536},
		{"a lead that declares nothing falls back to the worker", sizedLLM{65536}, unsizedLLM{}, 65536},
		{"a worker that declares nothing leaves the lead to answer", unsizedLLM{}, sizedLLM{65536}, 65536},
		// No invented number: with nothing to go on, compaction stays off
		// exactly as it was rather than guessing a window.
		{"neither declares a window", unsizedLLM{}, unsizedLLM{}, 0},
	}
	for _, c := range cases {
		app := &AppCore{}
		app.LLM = c.worker
		app.LeadLLM = c.lead
		if got := app.loopContextSize(); got != c.want {
			t.Errorf("%s: loopContextSize() = %d, want %d", c.name, got, c.want)
		}
	}
}

// The default has to be ON. It used to be off unless a caller set it, and of
// twenty loop configs one did — so nineteen kinds of turn, every scheduled fire
// among them, grew until the window gave out.
func TestCompactionIsOnByDefaultAndOptOutIsExplicit(t *testing.T) {
	// A config that says nothing gets the running tiers' window filled in.
	app := &AppCore{}
	app.LLM = sizedLLM{32768}
	if got := app.loopContextSize(); got <= 0 {
		t.Fatalf("a loop under a sized model must get a window, got %d", got)
	}

	// And compactHistory itself: zero means "nothing to do" only because
	// RunAgentLoop has already replaced it. A NEGATIVE size is the deliberate
	// opt-out, and it must still disable.
	huge := string(make([]byte, 200000))
	msgs := []Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolResults: []ToolResult{{ID: "1", Content: huge}}},
		{Role: "assistant", ToolResults: []ToolResult{{ID: "2", Content: huge}}},
	}
	before := len(msgs[1].ToolResults[0].Content)
	compactHistory(msgs, "system", -1, false)
	if len(msgs[1].ToolResults[0].Content) != before {
		t.Error("a negative context size must disable compaction — it is the explicit opt-out")
	}
}
