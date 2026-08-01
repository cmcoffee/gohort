package orchestrate

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An action script's only route to a finished run. Without it the obvious
// button — "save this run to history" — was unwritable, so it got written
// against an invented env var and printed "run a debate first" forever, passing
// every check on the way.
func TestLatestPipelineRunSkipsOneStillRunning(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root

	base := time.Now().Add(-time.Hour)
	SavePipelineRun(root, "u", PipelineRun{ID: "old", PipelineID: "p1", Title: "first", Date: base, Output: "done"})
	SavePipelineRun(root, "u", PipelineRun{ID: "live", PipelineID: "p1", Title: "second", Date: base.Add(time.Minute), Running: true})

	run, ok := app.PublicLatestPipelineRun("u", "p1")
	if !ok {
		t.Fatal("a finished run exists and must be found")
	}
	if run.ID != "old" {
		t.Errorf("a run still in flight has half a transcript — saving it as history is worse than none; got %q", run.ID)
	}
	// Another user's runs are a different history.
	if _, ok := app.PublicLatestPipelineRun("someone-else", "p1"); ok {
		t.Error("runs are per-user; a shared app must not hand over someone else's")
	}
	if _, ok := app.PublicLatestPipelineRun("u", ""); ok {
		t.Error("no pipeline bound, no run")
	}
}
