package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/oddjob/core/sources"
)

// A run numbers what it reads so its stages can cite it, a stage's citations
// can be tidied against it, and a verify stage can check the writing against
// the text that was read. What two apps each built by hand, as data.

// sourceTool is a stand-in reader: a tool stage calls it, and its extractor
// (registered like a real tool's) turns the result into one page read.
const sourceToolName = "test_read_page"

var registerSourceTool sync.Once

func readerTool() AgentToolDef {
	registerSourceTool.Do(func() {
		sources.RegisterExtractor(sourceToolName, func(_ map[string]any, result string) []sources.Fetched {
			return []sources.Fetched{{Title: "Field Study", URL: "https://example.org/study", Text: result}}
		})
	})
	page := "The field study measured adoption across forty clinics over two years. " +
		strings.Repeat("Adoption rose steadily as clinics trained their staff and shared results with each other. ", 6) +
		"By the end, adoption reached 62 percent of eligible patients."
	return AgentToolDef{
		Tool:    Tool{Name: sourceToolName, Description: "read a page"},
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return page, nil },
	}
}

// runWithSources runs def with the stand-in reader available and a fake model
// for the verify stage's claim checks, capturing every agent stage's prompt.
func runWithSources(t *testing.T, def PipelineDef, agentReply string, verdict string) (string, map[string]any, []string) {
	t.Helper()
	var mu sync.Mutex
	var prompts []string
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{Content: verdict, Repeat: true}}}}
	out, fields, err := app.RunPipelineDefHooks(context.Background(), def, "is it working?", PipelineHooks{
		Tools: []AgentToolDef{readerTool()},
		Dispatch: func(ctx context.Context, agent, prompt string) (string, error) {
			mu.Lock()
			prompts = append(prompts, prompt)
			mu.Unlock()
			return agentReply, nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out, fields, prompts
}

// What a stage read is numbered, and {sources} hands the list to a later
// stage's prompt to cite from.
func TestALaterStageCitesWhatAnEarlierOneRead(t *testing.T) {
	def := PipelineDef{Name: "cite", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "Write it up. Cite as [N] from:\n{sources}"},
	}}
	_, _, prompts := runWithSources(t, def, "Adoption reached 62 percent [1].", "")
	if len(prompts) != 1 || !strings.Contains(prompts[0], "[1] Field Study - https://example.org/study") {
		t.Fatalf("the writer must be handed the numbered sources, got %q", prompts)
	}
}

// cite tidies a prose stage's citations: a Sources section of exactly what it
// cited, and a citation naming nothing read is noted, not passed off as real.
func TestCiteListsWhatWasCitedAndNotesWhatWasNot(t *testing.T) {
	def := PipelineDef{Name: "cite", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "{sources}", Cite: true},
	}}
	out, _, _ := runWithSources(t, def, "Adoption reached 62 percent [1]. Costs fell by half [4].", "")
	if !strings.Contains(out, "## Sources") || !strings.Contains(out, "[1] [Field Study](https://example.org/study)") {
		t.Errorf("the cited source must be listed as a link:\n%s", out)
	}
	if !strings.Contains(out, "[4]") || !strings.Contains(out, "name no source this run read") {
		t.Errorf("a citation to nothing read must be noted:\n%s", out)
	}
}

// verify checks the writing: citations naming nothing, figures found in none
// of the text read, and each cited claim put to the model against its source.
func TestVerifyChecksTheWritingAgainstWhatWasRead(t *testing.T) {
	def := PipelineDef{Name: "verify", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "{sources}"},
		{Name: "check", Kind: StageVerify, Check: "write"},
	}}
	reply := "Adoption reached 62 percent of eligible patients [1]. It saved 900 million dollars [1]. Staff liked it [7]."
	_, fields, _ := runWithSources(t, def, reply, `{"verdict":"supported","why":"the study says so"}`)
	if fields["checked"] != 2 || fields["supported"] != 2 {
		t.Errorf("the two claims citing [1] must be checked and supported: %v", fields)
	}
	if u, _ := fields["unresolved_citations"].([]any); len(u) != 1 || u[0] != "[7]" {
		t.Errorf("[7] names nothing read: %v", fields["unresolved_citations"])
	}
	if f, _ := fields["unverified_figures"].([]any); len(f) == 0 || !strings.Contains(fmtAny(f), "900") {
		t.Errorf("900 million appears in no source: %v", fields["unverified_figures"])
	}
	if fields["passed"] != false {
		t.Errorf("a draft with an unresolved citation and an unverified figure has not passed: %v", fields["passed"])
	}
}

// A claim the source does not carry comes back unsupported, with why.
func TestVerifyReportsAnUnsupportedClaim(t *testing.T) {
	def := PipelineDef{Name: "verify", Stages: []PipelineStage{
		{Name: "read", Kind: StageTool, Tool: sourceToolName},
		{Name: "write", Kind: StageAgent, Agent: "writer", Prompt: "{sources}"},
		{Name: "check", Kind: StageVerify, Check: "write"},
	}}
	_, fields, _ := runWithSources(t, def, "Every clinic adopted it within a month [1].",
		`{"verdict":"unsupported","why":"the study says adoption rose over two years"}`)
	u, _ := fields["unsupported"].([]any)
	if len(u) != 1 || !strings.Contains(fmtAny(u), "two years") {
		t.Errorf("the unsupported claim must be reported with why: %v", fields)
	}
}

// verify and cite are checked when the pipeline is saved.
func TestSourcesStagesAreCheckedOnSave(t *testing.T) {
	stages := func(s ...PipelineStage) PipelineDef { return PipelineDef{Name: "d", Stages: s} }
	write := PipelineStage{Name: "write", Kind: StageWorker, Prompt: "x"}
	for name, bad := range map[string]PipelineDef{
		"verify with no check":  stages(write, PipelineStage{Name: "v", Kind: StageVerify}),
		"verify a later stage":  stages(PipelineStage{Name: "v", Kind: StageVerify, Check: "write"}, write),
		"verify with an output": stages(write, PipelineStage{Name: "v", Kind: StageVerify, Check: "write", Output: []PipelineField{{Name: "x"}}}),
		"check off verify":      stages(PipelineStage{Name: "w", Kind: StageWorker, Prompt: "x", Check: "w"}),
		"cite on JSON":          stages(PipelineStage{Name: "w", Kind: StageWorker, Prompt: "x", Cite: true, Output: []PipelineField{{Name: "a"}}}),
		"cite on a branch":      stages(PipelineStage{Name: "b", Kind: StageWorker, Prompt: "x", Output: []PipelineField{{Name: "ok", Type: FieldBool}}}, PipelineStage{Name: "br", Kind: StageBranch, When: "b.ok", Cite: true}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	// Its fields are real to what follows: a card on them, a branch on passed.
	good := stages(write,
		PipelineStage{Name: "v", Kind: StageVerify, Check: "write", Render: "card", Card: map[string]string{"title": "summary", "accent": "passed"}},
		PipelineStage{Name: "redo", Kind: StageBranch, When: "v.passed"},
		PipelineStage{Name: "after", Kind: StageWorker, Prompt: "{stage:v.summary}"})
	if err := good.Validate(); err != nil {
		t.Errorf("a verify stage's fields must be usable: %v", err)
	}
}

// The source list on its own.
func TestRunSourcesNumberCiteAndKeepText(t *testing.T) {
	rs := sources.NewRunSources()
	if n := rs.Add(sources.Fetched{Title: "A", URL: "https://a.example/x", Text: "snippet"}); n != 1 {
		t.Fatalf("first source is [1], got %d", n)
	}
	rs.Add(sources.Fetched{Title: "B", URL: "https://b.example/y"})
	if n := rs.Add(sources.Fetched{Title: "A again", URL: "https://a.example/x/", Text: "the whole page, much longer than the snippet"}); n != 1 {
		t.Errorf("a page read twice keeps its number, got %d", n)
	}
	if _, text, _ := rs.Source(1); !strings.HasPrefix(text, "the whole page") {
		t.Errorf("the longer read replaces the snippet, got %q", text)
	}
	if got := sources.Citations("see [2] and [1, 3], again [2]"); len(got) != 3 || got[0] != 2 || got[1] != 1 || got[2] != 3 {
		t.Errorf("citations in order of first mention, got %v", got)
	}
	out, unknown := rs.Cite("A [2]. B [9].")
	if !strings.Contains(out, "[2] [B](https://b.example/y)") || strings.Contains(out, "[1] [A]") || len(unknown) != 1 || unknown[0] != 9 {
		t.Errorf("only what was cited is listed, the unknown reported: %q %v", out, unknown)
	}
}

func fmtAny(v []any) string { return fmt.Sprint(v...) }

// A worker stage's tool calls reach the run's sources through its loop's
// history: each call paired with its result, an error skipped.
func TestCollectSourcesReadsTheStageLoopsHistory(t *testing.T) {
	readerTool() // registers the extractor
	rs := sources.NewRunSources()
	ctx := context.WithValue(context.Background(), runSourcesKey{}, rs)
	collectSources(ctx, []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: sourceToolName}, {ID: "c2", Name: sourceToolName}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "c1", Content: "the page"}, {ID: "c2", Content: "failed", IsError: true}}},
	})
	if rs.Len() != 1 {
		t.Errorf("one call read a page and one errored: %d sources, want 1", rs.Len())
	}
	collectSources(context.Background(), nil) // no run listening: nothing to do, no panic
}
