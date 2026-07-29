package core

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// The apps worth migrating onto pipelines (a debate, a deep-research run) are
// WATCHED while they run — their whole UI is "which stage is working, and what
// did it produce". A pipeline that can only report "stage 3 starting" can't
// replace them however expressive its stages are. These cover the event
// protocol that closes that gap.

// collectEvents runs a def with a recording sink. Safe for parallel stages.
func collectEvents(t *testing.T, def PipelineDef, dispatch PipelineDispatch) []PipelineEvent {
	t.Helper()
	var mu sync.Mutex
	var got []PipelineEvent
	app := &AppCore{}
	_, err := app.RunPipelineDefSyncWithSink(context.Background(), def, "the question", dispatch,
		func(ev PipelineEvent) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev)
		}, nil)
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	return got
}

// TestSinkEmitsOneBlockPerStage — a surface renders one card per stage, so
// every stage must open and close exactly once, in order.
func TestSinkEmitsOneBlockPerStage(t *testing.T) {
	def := PipelineDef{
		Name: "two-agent",
		Stages: []PipelineStage{
			{Name: "gather", Kind: StageAgent, Agent: "a1", Prompt: "{input}"},
			{Name: "verdict", Kind: StageAgent, Agent: "a2", Prompt: "{prev}"},
		},
	}
	dispatch := func(ctx context.Context, agentID, input string) (string, error) {
		return "output of " + agentID, nil
	}
	got := collectEvents(t, def, dispatch)

	var opened, closed []string
	byID := map[string]string{}
	for _, ev := range got {
		switch ev.Kind {
		case "block":
			opened = append(opened, ev.Title)
			byID[ev.ID] = ev.Title
			if ev.Type != string(StageAgent) {
				t.Errorf("block %q should carry its stage kind, got %q", ev.Title, ev.Type)
			}
		case "block_done":
			closed = append(closed, byID[ev.ID])
		}
	}
	if strings.Join(opened, ",") != "gather,verdict" {
		t.Errorf("blocks opened = %v, want gather then verdict", opened)
	}
	if strings.Join(closed, ",") != "gather,verdict" {
		t.Errorf("blocks closed = %v, want both, in order", closed)
	}
}

// TestSinkCarriesStageOutput — the block body is what the stage produced;
// without it a surface has cards with no content.
func TestSinkCarriesStageOutput(t *testing.T) {
	def := PipelineDef{
		Name:   "one",
		Stages: []PipelineStage{{Name: "only", Kind: StageAgent, Agent: "a1", Prompt: "{input}"}},
	}
	dispatch := func(ctx context.Context, agentID, input string) (string, error) {
		return "the answer body", nil
	}
	var chunks []string
	for _, ev := range collectEvents(t, def, dispatch) {
		if ev.Kind == "chunk" {
			chunks = append(chunks, ev.Text)
		}
	}
	if strings.Join(chunks, "") != "the answer body" {
		t.Errorf("chunks = %q, want the stage output", chunks)
	}
}

// TestSinkClosesBlockOnFailure — a card left spinning after a failed stage
// reads as a hung run, which is worse than a visible error.
func TestSinkClosesBlockOnFailure(t *testing.T) {
	def := PipelineDef{
		Name:   "boom",
		Stages: []PipelineStage{{Name: "explodes", Kind: StageAgent, Agent: "a1", Prompt: "{input}"}},
	}
	dispatch := func(ctx context.Context, agentID, input string) (string, error) {
		return "", Error("upstream exploded")
	}
	var mu sync.Mutex
	var got []PipelineEvent
	app := &AppCore{}
	_, err := app.RunPipelineDefSyncWithSink(context.Background(), def, "q", dispatch,
		func(ev PipelineEvent) { mu.Lock(); got = append(got, ev); mu.Unlock() }, nil)
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	var opened, closed int
	for _, ev := range got {
		switch ev.Kind {
		case "block":
			opened++
		case "block_done":
			closed++
		}
	}
	if opened != 1 || closed != 1 {
		t.Errorf("opened %d / closed %d blocks — a failed stage must still close", opened, closed)
	}
}

// TestStatusCallersUnaffected — every existing caller passes a plain status
// func. Adding the richer protocol must change nothing for them: they keep
// receiving status lines and never see block/chunk events.
func TestStatusCallersUnaffected(t *testing.T) {
	def := PipelineDef{
		Name:   "one",
		Stages: []PipelineStage{{Name: "only", Kind: StageAgent, Agent: "a1", Prompt: "{input}"}},
	}
	dispatch := func(ctx context.Context, agentID, input string) (string, error) { return "done", nil }

	var lines []string
	app := &AppCore{}
	out, err := app.RunPipelineDefSync(context.Background(), def, "q", dispatch,
		func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "done" {
		t.Errorf("output = %q", out)
	}
	if len(lines) == 0 {
		t.Error("status callers must still get their progress lines")
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "block") || strings.HasPrefix(l, "chunk") {
			t.Errorf("a status-only caller must never see protocol events: %q", l)
		}
	}
}
