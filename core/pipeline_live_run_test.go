package core

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A run used to be owned by the request that started it: closing the tab
// cancelled the work. It is detached now, which is what makes reconnect
// possible and what makes cancel necessary. These pin both halves, plus the
// thing that pays for them — a run that outlives its reader has to be
// reachable and stoppable from somewhere else.

func runSurface(t *testing.T, work RunWork) RunSurface {
	t.Helper()
	return RunSurface{DB: memDB(t), User: "u", OwnerID: "p", Work: work, Timeout: 10 * time.Second}
}

// startRun fires the stream endpoint and returns the run id once the surface
// has announced it.
func startRun(t *testing.T, s RunSurface) (string, *httptest.ResponseRecorder) {
	t.Helper()
	app := &AppCore{}
	req := httptest.NewRequest(http.MethodPost, "/stream", strings.NewReader(`{"input":"go"}`))
	rec := httptest.NewRecorder()
	app.ServeRuns(rec, req, s, "stream")
	body := rec.Body.String()
	if !strings.Contains(body, "event: session") {
		t.Fatalf("no session event in the stream: %q", body)
	}
	for _, run := range ListPipelineRuns(s.DB, s.User, s.OwnerID) {
		return run.ID, rec
	}
	t.Fatal("no run was stored")
	return "", nil
}

// The point of the whole change: the reader leaves, the work carries on.
func TestRunSurvivesTheRequestThatStartedIt(t *testing.T) {
	released := make(chan struct{})
	finished := make(chan struct{})
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		<-released
		sink(PipelineEvent{Kind: "block", ID: "b1", Type: "worker", Title: "late"})
		sink(PipelineEvent{Kind: "block_done", ID: "b1"})
		close(finished)
		return "done", nil
	})

	// A request that goes away mid-run: cancel its context, as a closed tab does.
	app := &AppCore{}
	ctx, abandon := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/stream", strings.NewReader(`{"input":"go"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { app.ServeRuns(rec, req, s, "stream"); close(done) }()

	waitFor(t, func() bool { return len(ListPipelineRuns(s.DB, s.User, s.OwnerID)) == 1 })
	abandon()
	<-done // the handler returned; under the old shape the work died here

	close(released)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the run died with its request")
	}
	waitFor(t, func() bool {
		runs := ListPipelineRuns(s.DB, s.User, s.OwnerID)
		return len(runs) == 1 && !runs[0].Running && runs[0].Output == "done"
	})
}

// Reconnect replays from the TOP. A viewer joining halfway through a
// transcript it cannot make sense of would be worse than no reconnect at all.
func TestReconnectReplaysTheWholeTranscript(t *testing.T) {
	release := make(chan struct{})
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		sink(PipelineEvent{Kind: "block", ID: "b1", Type: "worker", Title: "first"})
		sink(PipelineEvent{Kind: "chunk", ID: "b1", Text: "hello"})
		sink(PipelineEvent{Kind: "block_done", ID: "b1"})
		<-release
		return "out", nil
	})
	id, _ := startRunDetached(t, s)
	waitFor(t, func() bool {
		run, ok := LoadPipelineRun(s.DB, s.User, s.OwnerID, id)
		return ok && len(run.Blocks) == 1
	})

	app := &AppCore{}
	rec := newSyncRecorder()
	rctx, stopWatching := context.WithCancel(context.Background())
	watched := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/reconnect/"+id, nil).WithContext(rctx)
		app.ServeRuns(rec, req, s, "reconnect/"+id)
		close(watched)
	}()
	waitFor(t, func() bool { return strings.Contains(rec.String(), "block_done") })
	stopWatching()
	<-watched
	close(release)

	body := rec.String()
	for _, want := range []string{"event: session", "event: block", "hello"} {
		if !strings.Contains(body, want) {
			t.Errorf("reconnect stream missing %q; got:\n%s", want, body)
		}
	}
}

// 404 is the contract, not a failure: the panel reads it as "finished" and
// loads the stored record instead.
func TestReconnectToAFinishedRunIsNotFound(t *testing.T) {
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		return "immediate", nil
	})
	id, _ := startRun(t, s)
	liveRuns.ScheduleCleanupAfter(id, time.Millisecond)
	waitFor(t, func() bool { frames, _ := liveRuns.SnapshotEvents(id); return frames == nil })

	app := &AppCore{}
	rec := httptest.NewRecorder()
	app.ServeRuns(rec, httptest.NewRequest(http.MethodGet, "/reconnect/"+id, nil), s, "reconnect/"+id)
	if rec.Code != http.StatusNotFound {
		t.Errorf("reconnect to a finished run = %d, want 404", rec.Code)
	}
}

// The registry is global across every surface, so the guard that matters is
// the STORE: a run is filed under (user, pipeline, id), and loading it back
// proves all three.
func TestAnotherUsersRunIsNotReachable(t *testing.T) {
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		return "mine", nil
	})
	id, _ := startRun(t, s)

	intruder := RunSurface{DB: s.DB, User: "someone-else", OwnerID: "p", Work: s.Work}
	app := &AppCore{}
	for _, probe := range []struct{ sub, method string }{
		{"reconnect/" + id, http.MethodGet},
		{"cancel", http.MethodPost},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(probe.method, "/"+probe.sub+"?id="+id, nil)
		app.ServeRuns(rec, req, intruder, probe.sub)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s as another user = %d, want 404", probe.sub, rec.Code)
		}
	}
}

func TestCancelStopsTheWorkAndFilesItAsStopped(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return "", ctx.Err()
	})
	id, _ := startRunDetached(t, s)
	<-started

	app := &AppCore{}
	rec := httptest.NewRecorder()
	app.ServeRuns(rec, httptest.NewRequest(http.MethodPost, "/cancel?id="+id, nil), s, "cancel")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	waitFor(t, func() bool {
		run, ok := LoadPipelineRun(s.DB, s.User, s.OwnerID, id)
		return ok && !run.Running
	})
	run, _ := LoadPipelineRun(s.DB, s.User, s.OwnerID, id)
	// A context error reads to a user as though something broke. It did not.
	if run.Err != "stopped" {
		t.Errorf("cancelled run recorded %q, want \"stopped\"", run.Err)
	}
}

// Cancelling something that already finished is a successful outcome, not a
// failure — the button fires on a run that may have ended a moment ago.
func TestCancelAfterTheRunEndedIsStillOK(t *testing.T) {
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		return "quick", nil
	})
	id, _ := startRun(t, s)
	app := &AppCore{}
	rec := httptest.NewRecorder()
	app.ServeRuns(rec, httptest.NewRequest(http.MethodPost, "/cancel?id="+id, nil), s, "cancel")
	if rec.Code != http.StatusOK {
		t.Errorf("cancel of a finished run = %d, want 200", rec.Code)
	}
}

// A detached run with a budget and no way to see it is worse than one that
// dies with the tab, so it has to reach the global ribbon.
func TestALiveRunIsListedWhereItCanBeStopped(t *testing.T) {
	release := make(chan struct{})
	s := runSurface(t, func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
		<-release
		return "out", nil
	})
	s.Live = RunLiveInfo{App: "Debate", URL: "/custom/d/?session={id}", CancelURL: "/custom/d/pipeline/cancel?id={id}"}
	id, _ := startRunDetached(t, s)

	var found *LiveEntry
	waitFor(t, func() bool {
		for _, e := range AllLiveSessions() {
			if e.ID == id {
				found = &e
				return true
			}
		}
		return false
	})
	if found.App != "Debate" || !strings.Contains(found.CancelURL, id) || !strings.Contains(found.URL, id) {
		t.Errorf("ribbon entry = %+v, want the app's links with {id} filled in", *found)
	}
	if found.Owner != "u" {
		t.Errorf("entry owner = %q — an unowned entry has its label masked for everyone", found.Owner)
	}
	close(release)
}

// --- helpers -----------------------------------------------------------------

// startRunDetached starts a run whose work is still going, and abandons the
// starting request the way a closed tab does.
func startRunDetached(t *testing.T, s RunSurface) (string, context.CancelFunc) {
	t.Helper()
	app := &AppCore{}
	ctx, abandon := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/stream", strings.NewReader(`{"input":"go"}`)).WithContext(ctx)
	go app.ServeRuns(httptest.NewRecorder(), req, s, "stream")
	waitFor(t, func() bool { return len(ListPipelineRuns(s.DB, s.User, s.OwnerID)) == 1 })
	runs := ListPipelineRuns(s.DB, s.User, s.OwnerID)
	t.Cleanup(abandon)
	return runs[0].ID, abandon
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the expected state")
}

// syncRecorder records a response that is READ while the handler is still
// writing it. httptest.ResponseRecorder cannot be — its buffer is bare — and a
// stream is precisely the case where a test has to look before the handler is
// done.
type syncRecorder struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	hdr  http.Header
	code int
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{hdr: http.Header{}, code: http.StatusOK}
}

func (s *syncRecorder) Header() http.Header { return s.hdr }
func (s *syncRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
}
func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// Flush is what makes this usable as an SSE sink: NewSSEWriter refuses a
// writer that cannot flush, since a stream nobody flushes arrives all at once
// at the end.
func (s *syncRecorder) Flush() {}

func (s *syncRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
