package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A pipeline run is long by nature, so the transcript is stored as it streams
// rather than at the end: a run only visible on completion is one nobody can
// check on, compare against, or learn a regression from.

func runsApp(t *testing.T) *OrchestrateApp {
	t.Helper()
	return &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
}

func TestPipelineRunsRoundTrip(t *testing.T) {
	app := runsApp(t)
	run := PipelineRun{
		ID: "r1", PipelineID: "p1", Title: "why is the sky blue",
		Date:   time.Now(),
		Blocks: []PipelineRunBlock{{ID: "stage-1", Type: "worker", Title: "decompose", Body: "three sub-questions"}},
	}
	app.savePipelineRun("u", run)

	got, ok := app.loadPipelineRun("u", "p1", "r1")
	if !ok {
		t.Fatal("saved run should load back")
	}
	if got.Title != run.Title || len(got.Blocks) != 1 || got.Blocks[0].Body != "three sub-questions" {
		t.Fatalf("round trip lost content: %+v", got)
	}
}

// Runs are scoped per pipeline — one pipeline's history must not show another's.
func TestPipelineRunsAreScopedAndOrdered(t *testing.T) {
	app := runsApp(t)
	base := time.Now()
	app.savePipelineRun("u", PipelineRun{ID: "old", PipelineID: "p1", Title: "older", Date: base.Add(-time.Hour)})
	app.savePipelineRun("u", PipelineRun{ID: "new", PipelineID: "p1", Title: "newer", Date: base})
	app.savePipelineRun("u", PipelineRun{ID: "other", PipelineID: "p2", Title: "different pipeline", Date: base})

	runs := app.listPipelineRuns("u", "p1")
	if len(runs) != 2 {
		t.Fatalf("p1 should have 2 runs, got %d", len(runs))
	}
	if runs[0].ID != "new" {
		t.Errorf("newest run must come first, got %q", runs[0].ID)
	}
	for _, r := range runs {
		if r.PipelineID != "p1" {
			t.Errorf("another pipeline's run leaked in: %+v", r)
		}
	}
	// And a run is per-user.
	if other := app.listPipelineRuns("someone-else", "p1"); len(other) != 0 {
		t.Errorf("runs must not cross users, got %d", len(other))
	}
}

// An interrupted run stays marked Running rather than reading as
// finished-with-no-output, which would look like the pipeline produced nothing
// instead of that it was cut off.
func TestInterruptedRunStaysMarkedRunning(t *testing.T) {
	app := runsApp(t)
	app.savePipelineRun("u", PipelineRun{ID: "r1", PipelineID: "p1", Title: "q", Date: time.Now(), Running: true})
	got, _ := app.loadPipelineRun("u", "p1", "r1")
	if !got.Running {
		t.Error("a run recorded mid-flight must stay marked running")
	}
	if got.Output != "" || got.Err != "" {
		t.Error("an interrupted run should carry neither output nor error")
	}
}

func TestPipelineRunTitleTrims(t *testing.T) {
	if got := pipelineRunTitle("  what   is\n\nthe answer  "); got != "what is the answer" {
		t.Errorf("title = %q, want whitespace collapsed", got)
	}
	long := strings.Repeat("a", 200)
	got := pipelineRunTitle(long)
	if len([]rune(got)) > 91 {
		t.Errorf("title should be capped for the sidebar, got %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated title should show it was cut: %q", got)
	}
}
