package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Runs served by ServeRuns (a Builder pipeline, a machine, an app's pipeline
// section) now go through the framework's pipeline runner, the one the Go
// apps already had: a queue slot, recovery, the completion notice, and for a
// surface with a Kind, a persistent queue entry a restart resumes. These pin
// the parts that are new.

// withQueue points the persistent queue at a fresh store for one test.
func withQueue(t *testing.T) Database {
	t.Helper()
	db := memDB(t)
	SetQueueDB(func() Database { return db })
	t.Cleanup(func() { SetQueueDB(nil) })
	return db
}

func queued(id string) (QueueEntry, bool) {
	for _, e := range QueueEntries() {
		if e.ID == id {
			return e, true
		}
	}
	return QueueEntry{}, false
}

// A run with a Kind is in the persistent queue while it runs, so a restart
// can find it, and leaves it when it ends. One with no Kind never enters it:
// nothing could resume it, and an entry nobody handles is skipped and warned
// about at every start, forever.
func TestARestorableRunIsQueuedAndAnEphemeralOneIsNot(t *testing.T) {
	withQueue(t)
	for _, kind := range []string{"pipeline", ""} {
		release := make(chan struct{})
		s := runSurface(t, func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
			<-release
			return "out", nil
		})
		s.Kind = kind
		app := &AppCore{}
		go app.ServeRuns(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/stream", strings.NewReader(`{"input":"go"}`)), s, "stream")

		var id string
		deadline := time.Now().Add(3 * time.Second)
		for id == "" && time.Now().Before(deadline) {
			for _, r := range ListPipelineRuns(s.DB, s.User, s.OwnerID) {
				id = r.ID
			}
			time.Sleep(10 * time.Millisecond)
		}
		if id == "" {
			t.Fatal("the run never started")
		}
		entry, inQueue := queued(id)
		if kind != "" && (!inQueue || entry.App != "run:"+kind) {
			t.Errorf("kind %q: a restorable run must be in the queue under run:%s, got %+v (queued %v)", kind, kind, entry, inQueue)
		}
		if kind == "" && inQueue {
			t.Error("a run with no Kind must not be put in the persistent queue")
		}
		close(release)
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, still := queued(id); !still {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, still := queued(id); still {
			t.Errorf("kind %q: a finished run must leave the queue", kind)
		}
	}
}

// A run cut off by a restart comes back: the host's resolver rebuilds its
// surface and it runs again from the top, to a stored result.
func TestARunInterruptedByARestartResumes(t *testing.T) {
	withQueue(t)
	db := memDB(t)
	run := PipelineRun{ID: "run123456789", PipelineID: "p", Title: "go", Date: time.Now(), Running: true,
		Blocks: []PipelineRunBlock{{ID: "half", Title: "half a stage"}}}
	SavePipelineRun(db, "u", run)
	QueueAdd(run.ID, "run:restoretest", "go", runQueueParams{User: "u", OwnerID: "p", Input: "go", Key: "k"}, "")

	app := &AppCore{}
	gotKey := ""
	app.RegisterRunRestore("restoretest", func(user, ownerID, key string) (RunSurface, bool) {
		gotKey = key
		return RunSurface{DB: db, User: user, OwnerID: ownerID, Kind: "restoretest", Timeout: 10 * time.Second,
			Work: func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
				return "resumed: " + input, nil
			}}, true
	})
	QueueRestore()

	deadline := time.Now().Add(10 * time.Second)
	var got PipelineRun
	for time.Now().Before(deadline) {
		got, _ = LoadPipelineRun(db, "u", "p", run.ID)
		if !got.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Running || got.Output != "resumed: go" {
		t.Fatalf("the interrupted run was not resumed to a result: %+v", got)
	}
	if len(got.Blocks) != 0 {
		t.Errorf("a resumed run starts from the top; the half-finished blocks must go: %+v", got.Blocks)
	}
	if gotKey != "k" {
		t.Errorf("the resolver must get the host's restore key back, got %q", gotKey)
	}
	if _, still := queued(run.ID); still {
		t.Error("a resumed run that finished must leave the queue")
	}
}

// A run left marked running, with nothing holding it (no Kind, or its pipeline
// is gone), reads as interrupted instead of spinning in the panel forever.
func TestARunNothingWillFinishReadsAsInterrupted(t *testing.T) {
	db := memDB(t)
	SavePipelineRun(db, "u", PipelineRun{ID: "orphan123456", PipelineID: "p", Title: "go", Date: time.Now(), Running: true})
	s := RunSurface{DB: db, User: "u", OwnerID: "p"}
	app := &AppCore{}
	rec := httptest.NewRecorder()
	app.ServeRuns(rec, httptest.NewRequest(http.MethodGet, "/sessions/orphan123456", nil), s, "sessions/orphan123456")
	var one map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	if errText, _ := one["Error"].(string); !strings.Contains(errText, "interrupted") {
		t.Errorf("an orphaned running run must say it was interrupted, got %v", one["Error"])
	}
	if stored, _ := LoadPipelineRun(db, "u", "p", "orphan123456"); stored.Running {
		t.Error("the repair must be saved, not only shown")
	}
}

// An ephemeral run has no queue entry to carry its notify list, so its one
// NotifyUser is the list; a queued run reads the queue's.
func TestPipelineNameAndEphemeralQueueing(t *testing.T) {
	if pipelineName(PipelineConfig{App: "run:pipeline", Name: "My report"}) != "My report" {
		t.Error("a run is called by its Name when it has one")
	}
	if pipelineName(PipelineConfig{App: "debate"}) != "debate" {
		t.Error("without a Name, the App")
	}
	withQueue(t)
	pipelineQueueAdd("ephemeral-run", PipelineConfig{App: "x", Ephemeral: true})
	if _, in := queued("ephemeral-run"); in {
		t.Error("an ephemeral run must not be queued")
	}
	pipelineQueueAdd("kept-run-id", PipelineConfig{App: "x"})
	if _, in := queued("kept-run-id"); !in {
		t.Error("a normal run must be queued")
	}
}
