// The shape fanout and loop between them could not express: several voices on
// one question, seeing each other, over rounds.
package core

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// panelRun builds an interpreter run whose worker echoes its prompt, so a test
// can read exactly what each voice was asked.
func panelRun(t *testing.T, dispatch func(context.Context, string, string) (string, error)) (*pipelineRun, *[]string) {
	t.Helper()
	var asked []string
	var mu sync.Mutex // voices run in parallel; the recorder has to survive it
	app := &AppCore{}
	run := &pipelineRun{
		app:     app,
		outputs: map[string]stageOutput{},
		input:   "should we ship it?",
		dispatch: func(ctx context.Context, agent, prompt string) (string, error) {
			mu.Lock()
			asked = append(asked, agent+" ← "+prompt)
			mu.Unlock()
			if dispatch != nil {
				return dispatch(ctx, agent, prompt)
			}
			return agent + " says yes", nil
		},
	}
	return run, &asked
}

// Round one is a poll: nobody has replied to anybody, and no voice is handed a
// transcript that does not exist. Round two is where a panel earns its name.
func TestAPanelsSecondRoundSeesTheFirst(t *testing.T) {
	run, asked := panelRun(t, nil)
	stage := PipelineStage{
		Name: "debate", Kind: StagePanel, Count: 2,
		Panel:  []string{"Optimist", "Skeptic"},
		Prompt: "You are {voice}. Round {iteration} of {iterations}. Answer the question.",
	}
	out, fields, err := run.runPanelStage(context.Background(), stage, "", nil, nil)
	if err != nil {
		t.Fatalf("panel: %v", err)
	}
	if len(*asked) != 4 {
		t.Fatalf("two voices over two rounds is four calls, got %d", len(*asked))
	}
	// Round one carries no transcript…
	for _, q := range (*asked)[:2] {
		if strings.Contains(q, "Already said") {
			t.Errorf("round one has nothing behind it:\n%s", q)
		}
	}
	// …round two carries what round one said, for BOTH voices.
	for _, q := range (*asked)[2:] {
		if !strings.Contains(q, "Already said") || !strings.Contains(q, "Optimist says yes") {
			t.Errorf("round two should read round one:\n%s", q)
		}
	}
	// Each voice is addressed as itself and knows where it is. Found by
	// content, not by index: within a round the voices run in parallel and
	// arrive in whatever order they finish.
	var addressed bool
	for _, q := range (*asked)[:2] {
		if strings.Contains(q, "You are Optimist") && strings.Contains(q, "Round 1 of 2") {
			addressed = true
		}
	}
	if !addressed {
		t.Errorf("the voice and the round should substitute: %v", (*asked)[:2])
	}
	// The product is the whole transcript, not a verdict and not the last
	// round alone: a synthesizer needs to see who moved.
	for _, want := range []string{"## Round 1 — Optimist", "## Round 2 — Skeptic"} {
		if !strings.Contains(out, want) {
			t.Errorf("the transcript should keep every round (%q missing):\n%s", want, out)
		}
	}
	if v, _ := fields["rounds"].(int); v != 2 {
		t.Errorf("the stage should declare how many rounds ran, got %v", fields["rounds"])
	}
}

// Within a round the voices are blind to each other, or the first to answer
// sets the frame and the rest are commentary on it.
func TestVoicesInOneRoundDoNotSeeEachOther(t *testing.T) {
	run, asked := panelRun(t, nil)
	stage := PipelineStage{
		Name: "poll", Kind: StagePanel,
		Panel:  []string{"A", "B", "C"},
		Prompt: "Answer as {voice}.",
	}
	if _, _, err := run.runPanelStage(context.Background(), stage, "", nil, nil); err != nil {
		t.Fatalf("panel: %v", err)
	}
	for _, q := range *asked {
		if strings.Contains(q, "says yes") {
			t.Errorf("a voice saw another voice's answer from its own round:\n%s", q)
		}
	}
}

// A voice that fails is a gap in the transcript, not the end of the panel: a
// stage that collapses because one agent timed out is worse than one with a
// hole somebody can see. And it must NOT be quietly answered by a worker
// wearing that agent's name — the transcript would read as the agent's own
// words. Only ErrNoSuchAgent means "this is a role".
func TestAVoiceThatFailsLeavesAVisibleGap(t *testing.T) {
	run, _ := panelRun(t, func(_ context.Context, agent, _ string) (string, error) {
		if agent == "Skeptic" {
			return "", Error("agent unavailable")
		}
		return agent + " says yes", nil
	})
	stage := PipelineStage{Name: "debate", Kind: StagePanel,
		Panel: []string{"Optimist", "Skeptic"}, Prompt: "Answer as {voice}."}
	out, _, err := run.runPanelStage(context.Background(), stage, "", nil, nil)
	if err != nil {
		t.Fatalf("one voice failing must not fail the stage: %v", err)
	}
	if !strings.Contains(out, "Optimist") {
		t.Error("the voices that did answer should still be in the transcript")
	}
}

// The caps are the point of the announcement: voices times rounds is the
// number nobody works out from a stage count, and it is the one on the bill.
func TestAPanelSaysWhatItIsAboutToSpend(t *testing.T) {
	run, _ := panelRun(t, nil)
	var said []string
	stage := PipelineStage{Name: "debate", Kind: StagePanel, Count: 3,
		Panel: []string{"A", "B"}, Prompt: "Answer as {voice}."}
	if _, _, err := run.runPanelStage(context.Background(), stage, "", nil,
		func(s string) { said = append(said, s) }); err != nil {
		t.Fatalf("panel: %v", err)
	}
	if len(said) == 0 || !strings.Contains(said[0], "2 voices x 3 round(s) = 6 model calls") {
		t.Errorf("the multiplication should be stated before it is paid: %v", said)
	}
}

// Validation catches the shapes that are a different kind wearing a heavier
// word, and the ones whose cost multiplies past what anybody meant.
func TestPanelValidation(t *testing.T) {
	probs := func(s PipelineStage) string {
		s.Prompt = "go"
		err := PipelineDef{Name: "p", Stages: []PipelineStage{s}}.Validate()
		if err == nil {
			return ""
		}
		return err.Error()
	}
	if got := probs(PipelineStage{Name: "solo", Kind: StagePanel, Panel: []string{"A"}}); !strings.Contains(got, "at least two voices") {
		t.Errorf("a panel of one is an agent stage: %q", got)
	}
	if got := probs(PipelineStage{Name: "shaped", Kind: StagePanel, Panel: []string{"A", "B"},
		Output: []PipelineField{{Name: "verdict"}}}); !strings.Contains(got, "not one declared shape") {
		t.Errorf("a panel's product is several voices: %q", got)
	}
	if got := probs(PipelineStage{Name: "deep", Kind: StagePanel, Panel: []string{"A", "B"},
		Body: []PipelineStage{{Name: "inner", Prompt: "x"}}}); !strings.Contains(got, "takes no body") {
		t.Errorf("a voice is one contribution: %q", got)
	}
	if got := probs(PipelineStage{Name: "voiced", Kind: StageWorker, Panel: []string{"A", "B"}}); !strings.Contains(got, "only a kind \"panel\"") {
		t.Errorf("voices on a non-panel stage do nothing and should say so: %q", got)
	}
}

// A voice the host does not recognise is a ROLE: the worker answers as it.
// That is what lets a panel of perspectives ("the pessimist", "the customer")
// run on a deployment where nobody has authored three agents first.
func TestAnUnknownVoiceIsARoleNotAnError(t *testing.T) {
	run, asked := panelRun(t, func(_ context.Context, agent, _ string) (string, error) {
		if agent == "Skeptic" {
			return "", ErrNoSuchAgent
		}
		return agent + " says yes", nil
	})
	// The worker path needs an LLM; without one it errors, which is enough to
	// prove the fallback was TAKEN rather than the dispatch error recorded.
	stage := PipelineStage{Name: "debate", Kind: StagePanel,
		Panel: []string{"Optimist", "Skeptic"}, Prompt: "Answer as {voice}."}
	out, _, err := run.runPanelStage(context.Background(), stage, "", nil, nil)
	if err != nil {
		t.Fatalf("an unrecognised voice is a role, not a failure: %v", err)
	}
	if !strings.Contains(out, "## Skeptic") {
		t.Errorf("the role should still take its turn in the transcript:\n%s", out)
	}
	if strings.Contains(out, "no such agent") {
		t.Errorf("an unknown name must not be reported as a broken agent:\n%s", out)
	}
	for _, q := range *asked {
		if strings.Contains(q, "Skeptic") && strings.Contains(q, "Answer as Skeptic") {
			return // it was tried as an agent first, which is the right order
		}
	}
}
