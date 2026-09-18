package core

// The scripted LLM's own contract. Everything else that uses it is trusting
// these, so they are worth stating plainly.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFakeAnswersInOrder(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "search"}}},
		{Content: "here is what I found"},
	}}

	first, err := f.Chat(context.Background(), []Message{{Role: "user", Content: "look it up"}})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "search" {
		t.Fatalf("first = %+v", first)
	}
	second, err := f.Chat(context.Background(), nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Content != "here is what I found" {
		t.Errorf("second = %q", second.Content)
	}
	if f.Calls() != 2 {
		t.Errorf("calls = %d", f.Calls())
	}
}

// Running past the script is an error, not a default reply. A loop that asks
// more times than a test scripted is either a runaway or a test that has
// drifted, and both should fail rather than be absorbed.
func TestFakeRefusesToImproviseBeyondItsScript(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{{Content: "one"}}}
	if _, err := f.Chat(context.Background(), nil); err != nil {
		t.Fatalf("scripted call: %v", err)
	}
	_, err := f.Chat(context.Background(), nil)
	if err == nil {
		t.Fatal("the fake improvised an unscripted reply")
	}
	if !strings.Contains(err.Error(), "more times than this test expects") {
		t.Errorf("err = %v", err)
	}
}

func TestFakeRepeatsItsLastTurnWhenAsked(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{{Content: "always", Repeat: true}}}
	for i := 0; i < 5; i++ {
		r, err := f.Chat(context.Background(), nil)
		if err != nil || r.Content != "always" {
			t.Fatalf("call %d: %v %+v", i, err, r)
		}
	}
}

// The chunked path, which nearly every hand-rolled stub skipped by making
// ChatStream call Chat. Chunk boundaries are where a class of real bugs lives.
func TestFakeStreamsItsChunksAndJoinsThem(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{{Chunks: []string{"the ans", "wer is ", "42"}}}}
	var got []string
	r, err := f.ChatStream(context.Background(), nil, func(c string) { got = append(got, c) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 3 || got[1] != "wer is " {
		t.Errorf("chunks arrived as %q", got)
	}
	if r.Content != "the answer is 42" {
		t.Errorf("joined content = %q", r.Content)
	}
	if !f.Streamed(0) {
		t.Error("the call was not recorded as streamed")
	}
}

func TestFakeStreamsUnchunkedContentInOnePiece(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{{Content: "one piece"}}}
	var got []string
	if _, err := f.ChatStream(context.Background(), nil, func(c string) { got = append(got, c) }); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 1 || got[0] != "one piece" {
		t.Errorf("chunks = %q", got)
	}
}

// What the loop BUILT, not only what it did with the reply. This is the half
// that makes a prompt assertable.
func TestFakeRecordsWhatItWasAsked(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	f.Chat(context.Background(), []Message{
		{Role: "system", Content: "you are a careful assistant"},
		{Role: "user", Content: "first question"},
	})
	f.Chat(context.Background(), []Message{{Role: "user", Content: "second question"}})

	if got := f.Sent(0); len(got) != 2 || got[1].Content != "first question" {
		t.Fatalf("first call = %+v", got)
	}
	if !strings.Contains(f.Prompt(0), "careful assistant") {
		t.Errorf("prompt = %q", f.Prompt(0))
	}
	if got := f.LastSent(); len(got) != 1 || got[0].Content != "second question" {
		t.Errorf("last call = %+v", got)
	}
	if f.Sent(9) != nil {
		t.Error("a call that never happened returned something")
	}
}

// The loop appends to its history between rounds, so a recorded call that kept
// the caller's slice would end up describing a later one.
func TestFakeRecordsEachCallAsItWas(t *testing.T) {
	f := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	history := []Message{{Role: "user", Content: "first"}}
	f.Chat(context.Background(), history)
	history = append(history, Message{Role: "assistant", Content: "reply"})
	f.Chat(context.Background(), history)

	if got := f.Sent(0); len(got) != 1 {
		t.Errorf("the first call now reports %d messages — it kept the caller's slice", len(got))
	}
}

func TestFakeCanFail(t *testing.T) {
	boom := errors.New("upstream is down")
	f := &FakeLLM{Turns: []FakeTurn{{Err: boom, InputTokens: 12_000}}}
	resp, err := f.Chat(context.Background(), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// The prompt went out and the provider will bill it, so a failed call
	// still reports what it sent.
	if resp == nil || resp.InputTokens != 12_000 {
		t.Errorf("a failed call dropped the tokens it sent: %+v", resp)
	}
	// The failed call is still recorded: a test asserting on a retry needs to
	// see that the first attempt happened.
	if f.Calls() != 1 {
		t.Errorf("calls = %d", f.Calls())
	}
}

func TestFakeHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &FakeLLM{Turns: []FakeTurn{{Content: "never delivered"}}}
	if _, err := f.Chat(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if f.Calls() != 1 {
		t.Error("the attempt was not recorded")
	}
}

// It satisfies the interface it is standing in for. Asserted here so a change
// to LLM breaks this rather than nineteen call sites.
func TestFakeIsAnLLM(t *testing.T) {
	var _ LLM = &FakeLLM{}
}

// What the fake makes reachable that the hand-rolled stubs did not.
//
// The loop's STREAMING path: nearly every stub made ChatStream call Chat, so no
// test ever placed a chunk boundary anywhere. This puts one inside a reserved
// marker — the exact shape that shipped a leak earlier, where a reply cut
// between "<" and the rest of the tag arrived as two pieces and neither half
// matched anything looking for a whole one.
func TestLoopStreamsChunksThroughToTheHandler(t *testing.T) {
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{
		Chunks: []string{"here is the answer <", "gohort-meta>a note</gohort-meta> and the rest"},
	}}}}

	var streamed []string
	resp, _, err := app.RunAgentLoop(context.Background(),
		[]Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{
			MaxRounds: 2,
			Stream:    func(c string) { streamed = append(streamed, c) },
		})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	// Both halves reached the handler, in order and unjoined — which is what a
	// surface downstream has to cope with, and what no stub forwarding
	// ChatStream to Chat could ever have shown it.
	if len(streamed) != 2 {
		t.Fatalf("handler saw %d chunk(s): %q", len(streamed), streamed)
	}
	if !strings.HasSuffix(streamed[0], "<") || !strings.HasPrefix(streamed[1], "gohort-meta>") {
		t.Errorf("the boundary did not land inside the marker: %q", streamed)
	}
	// And the finished response is the whole thing, so a test can assert on the
	// complete reply and on how it arrived in the same run.
	if !strings.Contains(resp.Content, "<gohort-meta>a note</gohort-meta>") {
		t.Errorf("final content = %q", resp.Content)
	}
}

// The other half: asserting on the prompt the loop BUILT. A test that cares
// whether something reached the model had no way to look before this.
func TestLoopPromptIsAssertable(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "understood"}}}
	app := &AppCore{LLM: fake}

	_, _, err := app.RunAgentLoop(context.Background(),
		[]Message{{Role: "user", Content: "remember the gate code is 4417"}},
		AgentLoopConfig{MaxRounds: 2})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if fake.Calls() != 1 {
		t.Fatalf("calls = %d", fake.Calls())
	}
	if !strings.Contains(fake.Prompt(0), "gate code is 4417") {
		t.Errorf("the user's message did not reach the model:\n%s", fake.Prompt(0))
	}
}
