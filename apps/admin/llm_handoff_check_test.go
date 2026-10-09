package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// handoffFake is a model for the handoff check: it calls lookup_word when the
// last turn is a question (twice when asked to), answers when the last turn
// is tool results, and refuses whatever history refuse says it must.
type handoffFake struct {
	prefix    string // the ID prefix of its own calls
	noTools   bool
	refuse    func(msgs []Message, prefix string) error
	chatCalls int
}

func (f *handoffFake) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	f.chatCalls++
	if f.refuse != nil {
		if err := f.refuse(msgs, f.prefix); err != nil {
			return nil, err
		}
	}
	last := msgs[len(msgs)-1]
	if len(last.ToolResults) > 0 || f.noTools {
		return &Response{Content: "A kestrel is a small falcon."}, nil
	}
	calls := []ToolCall{{ID: f.prefix + "1", Name: "lookup_word", Args: map[string]any{"word": "kestrel"}}}
	if strings.Contains(last.Content, "twice") {
		calls = []ToolCall{
			{ID: f.prefix + "1", Name: "lookup_word", Args: map[string]any{"word": "heron"}},
			{ID: f.prefix + "2", Name: "lookup_word", Args: map[string]any{"word": "wren"}}}
	}
	return &Response{ToolCalls: calls}, nil
}

func (f *handoffFake) ChatStream(ctx context.Context, msgs []Message, h StreamHandler, opts ...ChatOption) (*Response, error) {
	return f.Chat(ctx, msgs, opts...)
}

// refuseAnyReplay is the Gemini 3 failure: a tool call sent back is refused.
func refuseAnyReplay(msgs []Message, _ string) error {
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 {
			return errors.New("api error (400): Function call is missing a thought_signature")
		}
	}
	return nil
}

// refuseForeign refuses a call it did not make.
func refuseForeign(msgs []Message, prefix string) error {
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if !strings.HasPrefix(tc.ID, prefix) {
				return errors.New("api error (400): tool_use.id does not match the pattern")
			}
		}
	}
	return nil
}

func runHandoff(llm, partner LLM) *handoffCheck {
	h := &handoffCheck{llm: llm, tools: true, partner: partner, partnerName: "the worker", partnerTools: partner != nil}
	h.run(context.Background())
	return h
}

func TestHandoffCheckPassesAWorkingPair(t *testing.T) {
	h := runHandoff(&handoffFake{prefix: "lead-"}, &handoffFake{prefix: "wrk-"})
	if h.failed() {
		t.Fatalf("a working pair failed:%s", h.lines())
	}
	for _, want := range []string{"✓ Tool call, then its result", "✓ Two calls in one turn", "✓ Continues the worker's tool call", "✓ The worker continues this model's tool call"} {
		if !strings.Contains(h.lines(), want) {
			t.Errorf("missing %q in:%s", want, h.lines())
		}
	}
}

// The failure the hello could not see: the first call works and sending it
// back is refused.
func TestHandoffCheckCatchesARefusedReplay(t *testing.T) {
	h := runHandoff(&handoffFake{prefix: "lead-", refuse: refuseAnyReplay}, &handoffFake{prefix: "wrk-"})
	if !h.failed() || !strings.Contains(h.firstFailure(), "Tool call, then its result") || !strings.Contains(h.firstFailure(), "thought_signature") {
		t.Fatalf("first failure = %q:%s", h.firstFailure(), h.lines())
	}
	// Its own call never went back, so there is nothing for the worker to
	// continue: that step is skipped, not failed.
	if !strings.Contains(h.lines(), "– The worker continues this model's tool call") {
		t.Errorf("the reverse step should skip:%s", h.lines())
	}
}

// A model that handles its own calls and refuses another model's: only the
// mixed-history step finds it.
func TestHandoffCheckCatchesARefusedForeignCall(t *testing.T) {
	h := runHandoff(&handoffFake{prefix: "lead-", refuse: refuseForeign}, &handoffFake{prefix: "wrk-"})
	if !strings.Contains(h.lines(), "✓ Tool call, then its result") {
		t.Errorf("its own round trip should pass:%s", h.lines())
	}
	if !strings.Contains(h.firstFailure(), "Continues the worker's tool call") {
		t.Fatalf("first failure = %q:%s", h.firstFailure(), h.lines())
	}
	// And the other direction, the worker refusing the lead's call.
	h = runHandoff(&handoffFake{prefix: "lead-"}, &handoffFake{prefix: "wrk-", refuse: refuseForeign})
	if !strings.Contains(h.firstFailure(), "The worker continues this model's tool call") {
		t.Fatalf("first failure = %q:%s", h.firstFailure(), h.lines())
	}
}

// With no partner the mixed-history step still runs, against a call written
// with another provider's ID shape.
func TestHandoffCheckWithoutAPartner(t *testing.T) {
	h := runHandoff(&handoffFake{prefix: "lead-", refuse: refuseForeign}, nil)
	if !strings.Contains(h.firstFailure(), "Continues another model's tool call") {
		t.Fatalf("first failure = %q:%s", h.firstFailure(), h.lines())
	}
	if strings.Contains(h.lines(), "continues this model's") {
		t.Errorf("no partner, no reverse step:%s", h.lines())
	}
}

func TestHandoffCheckModelThatWillNotCallTools(t *testing.T) {
	h := runHandoff(&handoffFake{prefix: "lead-", noTools: true}, &handoffFake{prefix: "wrk-"})
	if !strings.Contains(h.firstFailure(), "answered without calling the tool") {
		t.Fatalf("first failure = %q:%s", h.firstFailure(), h.lines())
	}
	// Not set to call tools natively: nothing to check, and nothing failed.
	h = &handoffCheck{llm: &handoffFake{}, tools: false}
	h.run(context.Background())
	if h.failed() || !strings.Contains(h.lines(), "– Tool steps") {
		t.Errorf("prompt-tools model:%s", h.lines())
	}
}

func TestLeadCallsLine(t *testing.T) {
	if leadCallsLine(LeadCallStats{}) != "" {
		t.Error("no calls should say nothing")
	}
	if got := leadCallsLine(LeadCallStats{Calls: 12}); !strings.Contains(got, "none failed") {
		t.Errorf("clean line = %q", got)
	}
	got := leadCallsLine(LeadCallStats{Calls: 41, Failed: 40, TopError: "missing a thought_signature", TopErrorCount: 40})
	if !strings.Contains(got, "40 failed (97%)") || !strings.Contains(got, "runs on the worker") || !strings.Contains(got, "thought_signature (40 times") {
		t.Errorf("failing line = %q", got)
	}
}
