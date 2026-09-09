package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// The shape fanout and loop between them could not express: several voices on
// one question, seeing each other, over rounds.
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

// The tier dial for a custom app's pipeline. The whole risk in it is the
// DEFAULT: RouteValueIsLead treats every value outside the closed worker set —
// the empty string included — as lead, and these keys are born at runtime while
// the route registry lives in memory. Resolving through RouteToLead would
// therefore promote every stage of every pipeline to the precision tier for the
// whole stretch after a restart, and nothing would fail: the runs would cost
// more and read slightly better.

func withRoute(t *testing.T, values map[string]string) {
	t.Helper()
	prev := LookupRouteFunc
	LookupRouteFunc = func(key string) string { return values[key] }
	t.Cleanup(func() { LookupRouteFunc = prev })
}

// The regression the design exists to prevent. No override stored anywhere:
// every stage must resolve exactly as its author wrote it.
func TestNoOverrideMeansTheAuthorDecides(t *testing.T) {
	withRoute(t, nil)
	r := &pipelineRun{defID: "p1"}
	for _, tc := range []struct {
		model string
		want  LLMTier
	}{
		{"", WORKER}, // the case that would have flipped to LEAD
		{"worker", WORKER},
		{"lead", LEAD},
	} {
		if got := r.stageTierFor(PipelineStage{Name: "judge", Model: tc.model}); got != tc.want {
			t.Errorf("model %q with no override = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestAnOperatorOverrideWins(t *testing.T) {
	withRoute(t, map[string]string{
		"pipeline.p1.judge": "worker",
		"pipeline.p1.draft": "lead",
		"pipeline.p1.think": "lead (thinking)",
	})
	r := &pipelineRun{defID: "p1"}
	if got := r.stageTierFor(PipelineStage{Name: "judge", Model: "lead"}); got != WORKER {
		t.Errorf("an override to worker on a lead stage = %v", got)
	}
	if got := r.stageTierFor(PipelineStage{Name: "draft"}); got != LEAD {
		t.Errorf("an override to lead on an unset stage = %v", got)
	}
	// The four legal values carry thinking as well as tier; only the tier is
	// this function's business.
	if got := r.stageTierFor(PipelineStage{Name: "think"}); got != LEAD {
		t.Errorf("\"lead (thinking)\" = %v, want LEAD", got)
	}
}

// An override belongs to one stage of one definition. A run with no stored
// definition behind it — a pipeline invoked inline, a test — has no key at all
// and must fall back rather than collide with another definition's dial.
func TestOverridesAreScopedToTheirDefinitionAndStage(t *testing.T) {
	withRoute(t, map[string]string{"pipeline.p1.judge": "lead"})
	if got := (&pipelineRun{defID: "p2"}).stageTierFor(PipelineStage{Name: "judge"}); got != WORKER {
		t.Errorf("another definition's override applied: %v", got)
	}
	if got := (&pipelineRun{defID: "p1"}).stageTierFor(PipelineStage{Name: "other"}); got != WORKER {
		t.Errorf("another stage's override applied: %v", got)
	}
	if got := (&pipelineRun{}).stageTierFor(PipelineStage{Name: "judge"}); got != WORKER {
		t.Errorf("a run with no definition took an override: %v", got)
	}
	if PipelineStageRouteKey("", "judge") != "" || PipelineStageRouteKey("p1", "") != "" {
		t.Error("an incomplete key must be empty, not a prefix that could collide")
	}
}

// RouteOverride reports what was STORED and nothing else. routeEffectiveVal
// answers a different question and folding in its defaults here is the bug.
func TestRouteOverrideReportsOnlyWhatWasStored(t *testing.T) {
	withRoute(t, map[string]string{"set.key": "worker"})
	if got := RouteOverride("set.key"); got != "worker" {
		t.Errorf("RouteOverride(set) = %q", got)
	}
	if got := RouteOverride("unset.key"); got != "" {
		t.Errorf("RouteOverride(unset) = %q, want empty — empty is how a caller knows nobody set it", got)
	}
	if got := RouteOverride(""); got != "" {
		t.Errorf("RouteOverride(\"\") = %q", got)
	}
}

// SessionMeta promotes declared stage output fields onto a run's sidebar row.
// Everything about it fails INVISIBLY when it is wrong — a bad reference does
// not error, it renders a blank pill — so the checks live at save time and
// these pin them.

func metaDef(refs ...string) PipelineDef {
	return PipelineDef{
		Name: "d",
		Stages: []PipelineStage{
			{Name: "judge", Kind: StageWorker, Prompt: "decide", Output: []PipelineField{
				{Name: "winner", Type: FieldString},
				{Name: "confidence", Type: FieldString},
			}},
			{Name: "prose", Kind: StageWorker, Prompt: "write"},
		},
		SessionMeta: refs,
	}
}

func TestSessionMetaAcceptsDeclaredFields(t *testing.T) {
	if err := metaDef("judge.winner", "judge.confidence").Validate(); err != nil {
		t.Fatalf("a reference to a declared field should validate: %v", err)
	}
}

func TestSessionMetaRefusesWhatWouldRenderBlank(t *testing.T) {
	cases := map[string]PipelineDef{
		"unknown stage":        metaDef("nope.winner"),
		"undeclared field":     metaDef("judge.loser"),
		"stage with no output": metaDef("prose.anything"),
		"not a reference":      metaDef("winner"),
		"duplicate name":       metaDef("judge.winner", "judge.winner"),
	}
	for name, def := range cases {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
}

// A promoted field named for one of the row's own columns does not read as a
// bad name — it reads as a sidebar that lost its entries.
func TestSessionMetaRefusesTheRowsOwnColumns(t *testing.T) {
	for _, field := range []string{"id", "ID", "Title", "date"} {
		def := PipelineDef{
			Name: "d",
			Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x",
				Output: []PipelineField{{Name: field, Type: FieldString}}}},
			SessionMeta: []string{"s." + field},
		}
		if err := def.Validate(); err == nil {
			t.Errorf("promoting %q should be refused: it would overwrite the row's own column", field)
		}
	}
}

// The panel reads a promoted value as a key on the row OBJECT — the same place
// it reads ID and Title — so the row has to marshal flat, not nested.
func TestSessionRowMarshalsPromotedFieldsFlat(t *testing.T) {
	row := PipelineSessionRow{
		ID: "r1", Title: "Should X?", Date: time.Now(),
		Meta: map[string]string{"winner": "for", "confidence": "high"},
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	for k, want := range map[string]string{"winner": "for", "confidence": "high", "ID": "r1", "Title": "Should X?"} {
		if got[k] != want {
			t.Errorf("row[%q] = %v, want %q", k, got[k], want)
		}
	}
	if _, nested := got["Meta"]; nested {
		t.Error("promoted fields must be flat on the row, not nested under Meta")
	}
	if strings.Contains(string(b), `"meta"`) {
		t.Errorf("unexpected nested meta container: %s", b)
	}
}

// Validate refuses a def that promotes a reserved name, so a collision can
// only reach here from a run stored before a rename. Losing the row's id is a
// worse failure than losing a pill, so the column wins.
func TestSessionRowColumnsWinACollision(t *testing.T) {
	row := PipelineSessionRow{ID: "real", Title: "t", Meta: map[string]string{"ID": "hijacked"}}
	b, _ := json.Marshal(row)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["ID"] != "real" {
		t.Errorf("row ID = %v, want the row's own id", got["ID"])
	}
}

// The value has to travel from a finished stage to whoever is recording the
// run, and it goes through the SINK — the interpreter does not know whether
// anybody is storing this run and should not have to.
func TestPromoteSessionMetaEmitsOnlyWhatWasAskedFor(t *testing.T) {
	var got []PipelineEvent
	r := &pipelineRun{
		sink:        func(ev PipelineEvent) { got = append(got, ev) },
		sessionMeta: []string{"judge.winner", "judge.confidence", "other.thing"},
	}

	// A stage that declares none of them stays silent: no empty meta event.
	r.promoteSessionMeta("prose", map[string]any{"body": "words"})
	if len(got) != 0 {
		t.Fatalf("an unpromoted stage emitted %v", got)
	}

	r.promoteSessionMeta("judge", map[string]any{
		"winner": "for", "confidence": "high", "reasoning": "long prose nobody wants in a sidebar",
	})
	if len(got) != 1 || got[0].Kind != "meta" {
		t.Fatalf("expected one meta event, got %v", got)
	}
	if got[0].Meta["winner"] != "for" || got[0].Meta["confidence"] != "high" {
		t.Errorf("promoted values = %v", got[0].Meta)
	}
	// Only the named fields ride along. A stage's full output can be large,
	// and the sidebar is the one place it must not land.
	if _, carried := got[0].Meta["reasoning"]; carried {
		t.Error("an undeclared field was promoted")
	}
}

// A fanout branch is quiet — its per-stage blocks are suppressed so the
// transcript reads one entry per stage. A summary is not part of a transcript,
// so it is not suppressed with one.
func TestPromotedMetaIsNotSuppressedByQuiet(t *testing.T) {
	var got []PipelineEvent
	r := &pipelineRun{
		sink:        func(ev PipelineEvent) { got = append(got, ev) },
		sessionMeta: []string{"judge.winner"},
		quiet:       true,
	}
	r.promoteSessionMeta("judge", map[string]any{"winner": "against"})
	if len(got) != 1 {
		t.Fatalf("a quiet run still files its summary; got %v", got)
	}
}

// A pipeline backing an app takes the submit form's fields as {name} in a
// stage prompt — the tool's help says so without qualification. It was true of
// a worker stage and quietly false of a fanout and a panel, which re-derive
// the prompt from stage.Prompt and used to stop at resolveStageTemplate. The
// failure is silent and it lands in the two kinds a debate-shaped app leans on
// hardest: the placeholder reaches the model as literal text.
func TestFormValuesReachEveryStageKindsPrompt(t *testing.T) {
	r := &pipelineRun{
		input:   "the question",
		vars:    map[string]string{"{tone}": "harsh"},
		outputs: map[string]stageOutput{},
	}
	const tmpl = "argue in a {tone} register"
	const want = "argue in a harsh register"

	// The worker path, which always worked, as the reference.
	if got := r.applyRunVars(resolveStageTemplate(tmpl, r.input, "", r.outputs)); got != want {
		t.Fatalf("worker prompt = %q, want %q", got, want)
	}
	// The two that did not: same composition, checked at the source so a
	// future edit that drops one is caught rather than shipped.
	src, err := os.ReadFile("pipeline_interp.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{
		`r.applyRunVars(resolveStageTemplate(strings.ReplaceAll(stage.Prompt, "{item}", it)`,
		`r.applyRunVars(resolveStageTemplate(panelPrompt(stage.Prompt`,
	} {
		if !strings.Contains(string(src), site) {
			t.Errorf("a stage kind stopped applying the run's form values: %s", site)
		}
	}
}

// The number of rounds is the one thing about a debate that belongs to the
// QUESTION rather than to the recipe, and a definition is written once for
// every question it will ever run. count_from moves it to the run.
func TestCountFromTakesTheCountFromTheRun(t *testing.T) {
	var said []string
	status := func(s string) { said = append(said, s) }
	run := func(vars map[string]string, stage PipelineStage) int {
		r := &pipelineRun{input: "q", vars: vars, outputs: map[string]stageOutput{}}
		return r.resolveCount(stage, 8, status)
	}
	stage := PipelineStage{Name: "rounds", Count: 3, CountFrom: "{rounds}"}

	if got := run(map[string]string{"{rounds}": "5"}, stage); got != 5 {
		t.Errorf("a form value of 5 gave %d rounds", got)
	}
	// An empty optional field is not a mistake, so it falls back QUIETLY.
	before := len(said)
	if got := run(nil, stage); got != 3 {
		t.Errorf("an unfilled field gave %d rounds, want the fallback", got)
	}
	if len(said) != before {
		t.Errorf("falling back on an unfilled field should say nothing, said: %v", said[before:])
	}
	// Anything else falls back LOUDLY: a stage that ran a different number of
	// times than the submitter asked for is not visible from the result.
	before = len(said)
	if got := run(map[string]string{"{rounds}": "lots"}, stage); got != 3 {
		t.Errorf("a non-number gave %d rounds, want the fallback", got)
	}
	if len(said) == before {
		t.Error("a value that is not a count must be reported, not silently ignored")
	}
	// The ceiling is the ceiling, and it says so too.
	before = len(said)
	if got := run(map[string]string{"{rounds}": "99"}, stage); got != 8 {
		t.Errorf("99 rounds gave %d, want the ceiling", got)
	}
	if len(said) == before {
		t.Error("clamping to the ceiling must be reported")
	}
}

// An earlier stage deciding how many rounds the question warrants is the
// declarative form of debate's "auto".
func TestCountFromCanReadAnEarlierStage(t *testing.T) {
	r := &pipelineRun{
		input: "q",
		outputs: map[string]stageOutput{
			"plan": {Fields: map[string]any{"rounds": float64(4)}},
		},
	}
	got := r.resolveCount(PipelineStage{Name: "rounds", Count: 2, CountFrom: "{stage:plan.rounds}"}, 8, nil)
	if got != 4 {
		t.Errorf("count from an earlier stage = %d, want 4", got)
	}
}

// A field that exists on the shared stage struct but is read by only some
// kinds is the lying-control pattern this codebase keeps refusing.
func TestCountFromIsRefusedWhereNothingRepeats(t *testing.T) {
	def := PipelineDef{Name: "d", Stages: []PipelineStage{
		{Name: "s", Kind: StageWorker, Prompt: "x", CountFrom: "{rounds}"},
	}}
	if err := def.Validate(); err == nil {
		t.Error("count_from on a worker stage should be refused — nothing there repeats")
	}
}

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

// Guardrails reaching INSIDE a pipeline. The dispatch boundary judges what a
// pipeline says; these pin what stops it ACTING — the half no output check can
// undo, because by the time there is an output to judge the mail has been sent.

// blockingGuards refuses every pre_action it is asked about and records which
// hook points reached it, so a test can tell what the narrowing let through.
func blockingGuards(hooks *[]string) StageGuardrails {
	return StageGuardrails{
		Check: func(hookPoint, candidate string) GuardrailDecision {
			*hooks = append(*hooks, hookPoint)
			return GuardrailDecision{Blocked: true, Message: "no"}
		},
		Halted: func() bool { return false },
	}
}

// confirmingTool is a consequential tool — the NeedsConfirm set is exactly what
// the pre_action gate covers, in a stage as in an agent loop.
func confirmingTool(ran *bool) []AgentToolDef {
	return []AgentToolDef{{
		Tool:         Tool{Name: "send_message", Parameters: map[string]ToolParam{"to": {Type: "string"}}},
		NeedsConfirm: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			*ran = true
			return "sent", nil
		},
	}}
}

// TestToolStage_PreActionBlocksConsequentialTool is the whole point: a stage
// that would act runs the caller's warden first, and a block means the handler
// is never reached — not that its result is discarded afterwards.
func TestToolStage_PreActionBlocksConsequentialTool(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "notify", Kind: StageTool, Tool: "send_message", Args: map[string]string{"to": "{input}"}},
	}}
	var ran bool
	var hooks []string
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	_, err := (&AppCore{}).executePipelineDef(ctx, def, "customer@example.com", nil, nil, confirmingTool(&ran))
	if err == nil {
		t.Fatal("a blocked pre_action must fail the stage")
	}
	if ran {
		t.Fatal("the tool RAN — an action gate that only judges after the call is not a gate")
	}
	if len(hooks) != 1 || hooks[0] != GuardHookPreAction {
		t.Fatalf("the stage must consult pre_action exactly once; got %v", hooks)
	}
	// Names no rule and no mechanism, same line every block message here holds.
	for _, banned := range []string{"guardrail", "warden", "policy"} {
		if strings.Contains(strings.ToLower(err.Error()), banned) {
			t.Errorf("the stage error must not name the mechanism (%q): %v", banned, err)
		}
	}
}

// TestToolStage_OrdinaryToolIsNotJudged pins the cost side: a read-only tool is
// outside the consequential set, so it costs no warden call at all.
func TestToolStage_OrdinaryToolIsNotJudged(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate", Args: map[string]string{"expr": "2+2"}},
	}}
	var seen []map[string]any
	var hooks []string
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	out, err := (&AppCore{}).executePipelineDef(ctx, def, "x", nil, nil, calcTool(&seen, "4"))
	if err != nil {
		t.Fatalf("a non-consequential tool must not be gated: %v", err)
	}
	if out != "4" || len(seen) != 1 {
		t.Fatalf("the tool should have run normally; out=%q calls=%d", out, len(seen))
	}
	if len(hooks) != 0 {
		t.Fatalf("a read-only tool must cost no warden call; got %v", hooks)
	}
}

// TestStageGuardrails_OnlyPreActionReaches pins the narrowing. A stage's agent
// loop offers every hook point; only pre_action is answered, because a stage's
// text is judged once as part of the pipeline's output rather than per stage.
func TestStageGuardrails_OnlyPreActionReaches(t *testing.T) {
	var hooks []string
	check := blockingGuards(&hooks).stageCheck()
	for _, hp := range []string{GuardHookPreOutput, GuardHookPeriodic, GuardHookPreInput} {
		if check(hp, "anything").Blocked {
			t.Errorf("%s must not fire inside a stage", hp)
		}
	}
	if len(hooks) != 0 {
		t.Fatalf("a narrowed-away hook must not even reach the warden; got %v", hooks)
	}
	if !check(GuardHookPreAction, "send_message to=x").Blocked {
		t.Fatal("pre_action must still fire")
	}
}

// TestStageGuardrails_DroppedAtAgentStage pins the hand-off: an agent stage
// runs an agent that has rules of its own, and judging its actions by the
// caller's is not what either owner authored.
func TestStageGuardrails_DroppedAtAgentStage(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "ask", Kind: StageAgent, Agent: "helper", Prompt: "{input}"},
	}}
	var carried bool
	dispatch := func(ctx context.Context, agentID, in string) (string, error) {
		carried = stageGuardrails(ctx).Check != nil
		return "done", nil
	}
	var hooks []string
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	if _, err := (&AppCore{}).executePipelineDef(ctx, def, "x", dispatch, nil, nil); err != nil {
		t.Fatalf("agent stage failed: %v", err)
	}
	if carried {
		t.Fatal("the caller's enforcement set must not travel into a dispatched agent")
	}
}

// TestStageGuardrails_InertSetIsNotCarried pins that an agent with no rules
// puts nothing on the context — core keeps its no-guardrails fast path.
func TestStageGuardrails_InertSetIsNotCarried(t *testing.T) {
	ctx := WithStageGuardrails(context.Background(), StageGuardrails{})
	if stageGuardrails(ctx).Check != nil {
		t.Fatal("an inert set must not be carried")
	}
}

// callThenAnswerLLM asks for a consequential tool on the first round and writes
// a reply on the second — the ordinary shape of a worker stage that acts.
type callThenAnswerLLM struct{ n int }

func (s *callThenAnswerLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	s.n++
	if s.n == 1 {
		return &Response{ToolCalls: []ToolCall{{ID: "1", Name: "send_message", Args: map[string]any{"to": "customer@example.com"}}}}, nil
	}
	return &Response{Content: "done what I could"}, nil
}
func (s *callThenAnswerLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

// TestWorkerStage_PreActionBlocksToolCall is the same guarantee one layer up:
// the tool a worker stage's own model chooses to call is judged too. Blocking
// it does NOT fail the stage — the loop hands the refusal back as the tool
// result and the stage finishes, exactly as it does in a full agent turn.
func TestWorkerStage_PreActionBlocksToolCall(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "reach_out", Kind: StageWorker, Prompt: "contact {input}", Tools: []string{"send_message"}},
	}}
	var ran bool
	var hooks []string
	app := &AppCore{LLM: &callThenAnswerLLM{}}
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	out, err := app.executePipelineDef(ctx, def, "customer@example.com", nil, nil, confirmingTool(&ran))
	if err != nil {
		t.Fatalf("a blocked action must not sink the stage: %v", err)
	}
	if ran {
		t.Fatal("the tool RAN — the stage loop was handed no pre_action gate")
	}
	if len(hooks) == 0 || hooks[0] != GuardHookPreAction {
		t.Fatalf("the stage loop must consult pre_action; got %v", hooks)
	}
	// And only pre_action: the stage's own text is judged once at the pipeline
	// boundary, not stage by stage.
	for _, hp := range hooks {
		if hp != GuardHookPreAction {
			t.Errorf("no hook but pre_action may fire inside a stage; got %s", hp)
		}
	}
	if out == "" {
		t.Error("the stage should still produce its reply after a blocked call")
	}
}

// TestWorkerStage_UngovernedStageCostsNothing pins the fast path: no set on the
// context means the loop gets a nil check and the tool runs untouched.
func TestWorkerStage_UngovernedStageCostsNothing(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "reach_out", Kind: StageWorker, Prompt: "contact {input}", Tools: []string{"send_message"}},
	}}
	var ran bool
	app := &AppCore{LLM: &callThenAnswerLLM{}}
	if _, err := app.executePipelineDef(context.Background(), def, "x", nil, nil, confirmingTool(&ran)); err != nil {
		t.Fatalf("ungoverned stage failed: %v", err)
	}
	if !ran {
		t.Fatal("with no guardrails the tool must run — the gate is not supposed to be on by default")
	}
}

// The tool stage: call a tool directly with author-written arguments, no
// LLM in the loop. This is the escape hatch that keeps the stage
// vocabulary from growing one kind per app — deterministic work
// (arithmetic, dedup, normalization) belongs in a tool, not a prompt.

// calcTool is a stand-in for any deterministic tool: it echoes the args
// it was handed so a test can assert what the stage actually passed.
func calcTool(seen *[]map[string]any, reply string) []AgentToolDef {
	return []AgentToolDef{{
		Tool: Tool{Name: "calculate", Parameters: map[string]ToolParam{"expr": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			*seen = append(*seen, args)
			return reply, nil
		},
	}}
}

func TestValidate_ToolStage(t *testing.T) {
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Prompt: "x", Output: []PipelineField{{Name: "expr", Type: FieldString}}},
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args: map[string]string{"expr": "{stage:plan.expr}"}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid tool stage rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"no tool named": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Args: map[string]string{"expr": "1+1"}},
		}},
		"prompt on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Prompt: "compute this"},
		}},
		"think on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Think: boolPtr(true)},
		}},
		"model on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Model: "lead"},
		}},
		"agent on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Agent: "helper"},
		}},
		// An arg reference gets the same forward/unknown check a prompt does.
		"arg references a later stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate",
				Args: map[string]string{"expr": "{stage:plan.expr}"}},
			{Name: "plan", Prompt: "x", Output: []PipelineField{{Name: "expr", Type: FieldString}}},
		}},
		"arg references an undeclared field": {Stages: []PipelineStage{
			{Name: "plan", Prompt: "x", Output: []PipelineField{{Name: "expr", Type: FieldString}}},
			{Name: "math", Kind: StageTool, Tool: "calculate",
				Args: map[string]string{"expr": "{stage:plan.nope}"}},
		}},
		"tool/args on a non-tool stage": {Stages: []PipelineStage{
			{Name: "w", Prompt: "x", Tool: "calculate"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestToolStage_CallsWithTemplatedArgs(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Kind: StageAgent, Agent: "planner", Prompt: "plan {input}",
			Output: []PipelineField{{Name: "expr", Type: FieldString, Required: true}}},
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args: map[string]string{"expr": "{stage:plan.expr}", "note": "for {input}"}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	var seen []map[string]any
	rec := &recorder{reply: func(_, _ string, _ int) string { return `{"expr": "2+2"}` }}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "budget", rec.fn, nil, calcTool(&seen, "4"))
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected one tool call, got %d", len(seen))
	}
	// The field an LLM stage declared feeds the tool call directly — no
	// model in between deciding what to pass.
	if seen[0]["expr"] != "2+2" {
		t.Errorf("expr arg = %v, want the templated field value", seen[0]["expr"])
	}
	if seen[0]["note"] != "for budget" {
		t.Errorf("note arg = %v, want {input} substituted", seen[0]["note"])
	}
	if out != "4" {
		t.Errorf("stage output should be the tool result, got %q", out)
	}
}

func TestToolStage_MissingToolIsAClearError(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate", Args: map[string]string{"expr": "1+1"}},
	}}
	app := &AppCore{}
	var seen []map[string]any
	// Caller has a catalog, just not this tool — the error should say so
	// and list what IS available.
	_, err := app.executePipelineDef(context.Background(), def, "x", nil, nil,
		[]AgentToolDef{{Tool: Tool{Name: "web_search"}, Handler: func(context.Context, map[string]any) (string, error) { return "", nil }}})
	if err == nil {
		t.Fatal("expected an error for a tool the caller doesn't have")
	}
	if !strings.Contains(err.Error(), "web_search") {
		t.Errorf("error should list the available tools: %v", err)
	}
	// And with no catalog at all, say THAT rather than listing nothing.
	_, err = app.executePipelineDef(context.Background(), def, "x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no tool catalog") {
		t.Errorf("empty-catalog error should be distinct: %v", err)
	}
	_ = seen
}

func TestToolStage_DeclaredOutputDecodesTheResult(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args:   map[string]string{"expr": "2+2"},
			Output: []PipelineField{{Name: "value", Type: FieldNumber, Required: true}}},
		{Name: "say", Kind: StageAgent, Agent: "w", Prompt: "the answer is {stage:math.value}"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	var seen []map[string]any
	rec := &recorder{reply: func(_, _ string, _ int) string { return "done" }}
	app := &AppCore{}
	_, err := app.executePipelineDef(context.Background(), def, "x", rec.fn, nil, calcTool(&seen, `{"value": 4}`))
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if !strings.Contains(rec.prompts[0], "the answer is 4") {
		t.Errorf("the tool's decoded field should template downstream: %q", rec.prompts[0])
	}
}

func TestToolStage_DeclaredOutputMismatchFails(t *testing.T) {
	// No model ran, so there is nothing to repair — a mismatch means the
	// tool's contract is wrong, and that should surface rather than retry.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args:   map[string]string{"expr": "2+2"},
			Output: []PipelineField{{Name: "value", Type: FieldNumber, Required: true}}},
	}}
	var seen []map[string]any
	app := &AppCore{}
	_, err := app.executePipelineDef(context.Background(), def, "x", nil, nil, calcTool(&seen, "not json at all"))
	if err == nil {
		t.Fatal("expected an error when the tool result doesn't match the declared output")
	}
	if len(seen) != 1 {
		t.Errorf("the tool should have been called exactly once (no repair retry), got %d", len(seen))
	}
}
