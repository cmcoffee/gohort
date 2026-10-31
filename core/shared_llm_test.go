package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// A lead that built fine and later stopped answering used to be recorded
// nowhere. Every escalation fell back to the worker per call, no flag was
// set, the background retry never ran, and the only symptom was answers
// being worse: the same symptom as no lead being configured at all. One SSO
// session lapsing cost eleven hours that way.

// healthStub answers with whatever it is told to answer with.
type healthStub struct {
	mu   sync.Mutex
	err  error
	resp *Response
}

func (s *healthStub) answer() (*Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	r := s.resp
	if r == nil {
		r = &Response{Content: "ok"}
	}
	return r, nil
}

func (s *healthStub) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return s.answer()
}

func (s *healthStub) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.answer()
}

func (s *healthStub) set(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// leadHealthEnv wires a distinct worker and lead and restores whatever was
// there. Both are process-global, so a test that forgets breaks its
// neighbours rather than itself.
func leadHealthEnv(t *testing.T) (*healthStub, LLM) {
	t.Helper()
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	lead := &healthStub{}
	SetSharedLLMs(&healthStub{}, lead)
	t.Cleanup(func() {
		SetSharedLLMs(prevW, prevL)
		SetLeadInitError("", "", nil) // also clears the runtime record
	})
	return lead, ReloadableLeadLLM()
}

func TestAFailingLeadCallRecordsItself(t *testing.T) {
	stub, handle := leadHealthEnv(t)
	stub.set(errors.New("ExpiredToken: the SSO session has ended"))

	if _, err := handle.Chat(context.Background(), nil); err == nil {
		t.Fatal("the stub was told to fail")
	}
	got := leadRuntimeError()
	if got == "" {
		t.Fatal("a lead that stopped answering recorded nothing, which is the whole bug")
	}
	if !strings.Contains(got, "SSO session has ended") {
		t.Errorf("the record does not carry the provider's own reason: %q", got)
	}

	// And it reaches the breadcrumb, which is the surface somebody reads when
	// escalations quietly stop happening.
	reason := leadUnavailableReason(&AppCore{LeadLLM: handle})
	if !strings.Contains(reason, "SSO session has ended") {
		t.Errorf("the reason for an escalation that did not happen is still generic: %q", reason)
	}
	if reason == "the lead was unavailable" {
		t.Error("the bare fallthrough is what this replaces")
	}
}

// A call that WORKS is the only honest evidence the failure is over.
// Rebuilding the client proves the constructor runs, which for several
// providers does no work at all.
func TestASucceedingLeadCallClearsIt(t *testing.T) {
	stub, handle := leadHealthEnv(t)
	stub.set(errors.New("ExpiredToken"))
	_, _ = handle.Chat(context.Background(), nil)
	if leadRuntimeError() == "" {
		t.Fatal("setup: nothing was recorded")
	}
	stub.set(nil)
	if _, err := handle.Chat(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := leadRuntimeError(); got != "" {
		t.Errorf("a working lead is still marked down: %q", got)
	}
}

// The streaming path is every interactive turn. Recording on Chat alone would
// miss the surface where it matters most.
func TestTheStreamingPathRecordsToo(t *testing.T) {
	stub, handle := leadHealthEnv(t)
	stub.set(errors.New("upstream connect error"))
	_, _ = handle.ChatStream(context.Background(), nil, func(string) {})
	if leadRuntimeError() == "" {
		t.Error("a streaming lead failure recorded nothing")
	}
}

// Two failures that are NOT the lead being unavailable. Marking it down for
// either would make the diagnosis lie in the direction that wastes the most
// time: somebody goes and checks AWS because a prompt was too big.
func TestCancellationAndOversizedPromptsAreNotOutages(t *testing.T) {
	stub, handle := leadHealthEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stub.set(context.Canceled)
	_, _ = handle.Chat(ctx, nil)
	if got := leadRuntimeError(); got != "" {
		t.Errorf("the caller leaving was recorded as the lead failing: %q", got)
	}

	stub.set(ErrContextExceeded)
	_, _ = handle.Chat(context.Background(), nil)
	if got := leadRuntimeError(); got != "" {
		t.Errorf("a prompt too large for the window was recorded as an outage: %q", got)
	}
}

// The WORKER handle must never write to the lead's record, and neither must
// the lead handle while it is standing in for the worker: with no distinct
// lead configured it forwards there, and a worker outage is not a lead one.
func TestOnlyARealLeadCallCounts(t *testing.T) {
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL); SetLeadInitError("", "", nil) })

	broken := &healthStub{}
	broken.set(errors.New("worker is down"))

	// A distinct lead exists; the WORKER handle fails.
	SetSharedLLMs(broken, &healthStub{})
	if _, err := ReloadableWorkerLLM().Chat(context.Background(), nil); err == nil {
		t.Fatal("setup: the worker was told to fail")
	}
	if got := leadRuntimeError(); got != "" {
		t.Errorf("a worker failure was recorded against the lead: %q", got)
	}

	// No distinct lead: the lead handle forwards to the worker, so its failure
	// is still the worker's.
	SetSharedLLMs(broken, nil)
	if _, err := ReloadableLeadLLM().Chat(context.Background(), nil); err == nil {
		t.Fatal("setup: the worker was told to fail")
	}
	if got := leadRuntimeError(); got != "" {
		t.Errorf("a worker failure behind the lead handle was recorded as a lead outage: %q", got)
	}
}

// A rebuild is a NEW client, so the previous one's call history describes
// nothing. A complaint that outlives the thing it was about is exactly what
// leadInitErr was added to stop.
func TestRebuildingTheLeadDropsTheOldClientsHistory(t *testing.T) {
	stub, handle := leadHealthEnv(t)
	stub.set(errors.New("ExpiredToken"))
	_, _ = handle.Chat(context.Background(), nil)
	if leadRuntimeError() == "" {
		t.Fatal("setup: nothing was recorded")
	}
	SetLeadInitError("", "", nil) // what a successful rebuild reports
	if got := leadRuntimeError(); got != "" {
		t.Errorf("a rebuilt lead inherited the old client's failure: %q", got)
	}
}

// An init failure outranks a runtime one: if it never built, there is no
// client, and "its last call failed" would describe a client that is gone.
func TestAnInitFailureOutranksARuntimeOne(t *testing.T) {
	stub, handle := leadHealthEnv(t)
	stub.set(errors.New("ExpiredToken"))
	_, _ = handle.Chat(context.Background(), nil)
	SetLeadInitError("bedrock", "placeholder-model", errors.New("no resolvable credentials"))
	got := leadUnavailableReason(&AppCore{LeadLLM: handle})
	if !strings.Contains(got, "could not be initialized") {
		t.Errorf("the init failure is not what gets reported: %q", got)
	}
}

// The lead failing on SOME calls: the health line flips with every call that
// works, so only a count shows that most of the lead's work went to the
// worker. Counts roll over by the hour and drop off after 24.
func TestLeadCallsCountsAPartialFailure(t *testing.T) {
	prevNow := leadStatsNow
	leadStatsMu.Lock()
	prevHours := leadHours
	leadHours = [24]leadHour{}
	leadStatsMu.Unlock()
	t.Cleanup(func() {
		leadStatsNow = prevNow
		leadStatsMu.Lock()
		leadHours = prevHours
		leadStatsMu.Unlock()
	})
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	leadStatsNow = func() time.Time { return at }

	sig := errors.New("gemini api error (400): Function call is missing a thought_signature")
	for i := 0; i < 10; i++ {
		noteLeadCall(nil)
		noteLeadCall(sig)
		noteLeadCall(sig)
	}
	noteLeadCall(errors.New("timeout"))
	got := LeadCalls()
	if got.Calls != 31 || got.Failed != 21 || got.RecentFailed != 21 {
		t.Fatalf("counts = %+v", got)
	}
	if got.TopErrorCount != 20 || !strings.Contains(got.TopError, "thought_signature") {
		t.Errorf("top error = %q x%d", got.TopError, got.TopErrorCount)
	}

	// Three hours on, the calls are still in the day but no longer recent.
	at = at.Add(3 * time.Hour)
	if got := LeadCalls(); got.Calls != 31 || got.RecentCalls != 0 {
		t.Errorf("after 3h = %+v", got)
	}
	// A day on, they are gone.
	at = at.Add(24 * time.Hour)
	if got := LeadCalls(); got.Calls != 0 || got.TopError != "" {
		t.Errorf("after a day = %+v", got)
	}
}

// The counts describe the model that made the calls. A different lead starts
// them over; a reload that keeps the same lead (a worker save) does not.
func TestLeadCallsStartOverWithANewLead(t *testing.T) {
	leadStatsMu.Lock()
	prevHours := leadHours
	leadHours = [24]leadHour{}
	leadStatsMu.Unlock()
	prevWorker, prevLead := LiveLLMs()
	t.Cleanup(func() {
		SetLiveLLMs(prevWorker, prevLead)
		leadStatsMu.Lock()
		leadHours = prevHours
		leadStatsMu.Unlock()
	})
	SetLiveLLMs("worker-a", "provider/typo-model")
	noteLeadCall(errors.New("api error (404): model not found"))
	noteLeadCall(errors.New("api error (404): model not found"))

	SetLiveLLMs("worker-b", "provider/typo-model")
	if got := LeadCalls(); got.Failed != 2 {
		t.Fatalf("a reload keeping the same lead lost its counts: %+v", got)
	}
	SetLiveLLMs("worker-b", "provider/right-model")
	if got := LeadCalls(); got.Calls != 0 || got.Failed != 0 || got.TopError != "" || !got.LastFailure.IsZero() {
		t.Errorf("the old model's failures outlived it: %+v", got)
	}
}

// The mode used to be a per-agent snapshot taken at startup and never
// recomputed, so an admin flipping "Native tool calling" got a rebuilt LLM and
// an unchanged mode — the setting read correct everywhere it could be
// inspected while every tool call still went out prompt-parsed.
func TestPromptToolsModeIsLiveAndOverridesTheAgentField(t *testing.T) {
	restore := func(mode, set bool) {
		promptToolsMu.Lock()
		promptToolsMode, promptToolsSet = mode, set
		promptToolsMu.Unlock()
	}
	promptToolsMu.RLock()
	oldMode, oldSet := promptToolsMode, promptToolsSet
	promptToolsMu.RUnlock()
	t.Cleanup(func() { restore(oldMode, oldSet) })

	// Nothing published (an SDK embedder): the agent's own field decides.
	restore(false, false)
	agent := &AppCore{PromptTools: true}
	if !agent.promptToolsMode() {
		t.Error("with nothing published, the agent's own field must decide")
	}

	// Published: it WINS, in both directions. Turning native tools on has to
	// be able to switch the mode OFF, which is the direction that was broken.
	SetPromptToolsMode(false)
	if agent.promptToolsMode() {
		t.Error("a published mode must override the agent's stale field")
	}
	SetPromptToolsMode(true)
	if !agent.promptToolsMode() {
		t.Error("a published mode must apply in both directions")
	}

	mode, ok := PromptToolsMode()
	if !ok || !mode {
		t.Errorf("PromptToolsMode() = %v,%v", mode, ok)
	}
}

// A person's call goes at once. A background call waits while a person's
// call is out and for the quiet after it, then goes. A lowest call waits
// while ANY call is out. The waits are capped.
func TestLanesGiveWayToPeople(t *testing.T) {
	q, lq, mw, tick := laneQuiet, laneLowestQuiet, laneMaxWait, laneTick
	laneQuiet, laneLowestQuiet, laneMaxWait, laneTick = 60*time.Millisecond, 60*time.Millisecond, 400*time.Millisecond, 5*time.Millisecond
	defer func() { laneQuiet, laneLowestQuiet, laneMaxWait, laneTick = q, lq, mw, tick }()
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	r := reloadableLLM{}
	person := WithRunLane(context.Background(), LaneInteractive)
	done, err := r.beginModelUse(person)
	if err != nil {
		t.Fatal(err)
	}
	if held, _ := holdForLane(person, WORKER); held != 0 {
		t.Fatalf("a person's call was held %s", held)
	}
	// Background waits while the person's call is out, and is released soon
	// after it ends: within the quiet plus a tick or two.
	go func() { time.Sleep(50 * time.Millisecond); done() }()
	start := time.Now()
	held, err := holdForLane(context.Background(), WORKER)
	if err != nil {
		t.Fatal(err)
	}
	if held < 50*time.Millisecond || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("background held %s; want about the call plus the quiet", held)
	}
	// Past the quiet, nothing holds background.
	time.Sleep(80 * time.Millisecond)
	if held, _ := holdForLane(context.Background(), WORKER); held != 0 {
		t.Fatalf("background held %s with nobody using the model", held)
	}
	// Lowest waits for a BACKGROUND call too; background does not wait for
	// background.
	bgDone, _ := r.beginModelUse(context.Background())
	if held, _ := holdForLane(context.Background(), WORKER); held != 0 {
		t.Fatalf("background held %s behind another background call", held)
	}
	lowest := WithRunLane(context.Background(), LaneLowest)
	go func() { time.Sleep(40 * time.Millisecond); bgDone() }()
	if held, _ := holdForLane(lowest, WORKER); held < 40*time.Millisecond {
		t.Fatalf("lowest went after %s with another call out", held)
	}
	// The cap: a person's call that never ends does not hold background
	// forever.
	time.Sleep(80 * time.Millisecond)
	forever, _ := r.beginModelUse(person)
	defer forever()
	if held, _ := holdForLane(context.Background(), WORKER); held < laneMaxWait || held > laneMaxWait+100*time.Millisecond {
		t.Fatalf("background held %s; want the cap %s", held, laneMaxWait)
	}
	// A cancelled wait returns the context's error.
	cctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := holdForLane(cctx, WORKER); err == nil {
		t.Fatal("a cancelled wait should return the error")
	}
}

func TestRunLaneDefaultsToBackground(t *testing.T) {
	if RunLaneOf(context.Background()) != LaneBackground || RunLaneOf(nil) != LaneBackground {
		t.Fatal("an unmarked context is background")
	}
	if RunLaneOf(WithRunLane(context.Background(), LaneLowest)) != LaneLowest {
		t.Fatal("the mark is not read back")
	}
}

// A call with a short deadline of its own is never held, and one with a
// longer deadline is held only until the floor remains: the warden's check
// inside a held run used to expire waiting and block the turn.
func TestAShortDeadlineIsNeverHeld(t *testing.T) {
	q, mw, tick, floor := laneQuiet, laneMaxWait, laneTick, laneDeadlineFloor
	laneQuiet, laneMaxWait, laneTick, laneDeadlineFloor = time.Second, 2*time.Second, 5*time.Millisecond, 100*time.Millisecond
	defer func() { laneQuiet, laneMaxWait, laneTick, laneDeadlineFloor = q, mw, tick, floor }()
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	done, _ := reloadableLLM{}.beginModelUse(WithRunLane(context.Background(), LaneInteractive))
	defer done()
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if held, err := holdForLane(short, WORKER); held != 0 || err != nil {
		t.Fatalf("a call with less than the floor left was held %s (%v)", held, err)
	}
	longer, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	held, err := holdForLane(longer, WORKER)
	if err != nil {
		t.Fatal(err)
	}
	if held < 150*time.Millisecond || held > 260*time.Millisecond {
		t.Fatalf("held %s; want the deadline less the floor, about 200ms", held)
	}
}

// The run gives way at its OWN call, in the loop, and the wait is a figure of
// its own: the reply carries it, OnWait hears it, and a short call made
// beside the run (a warden, a title) is never held.
func TestTheLoopGivesWayAtItsOwnCall(t *testing.T) {
	q, mw, tick, every := laneQuiet, laneMaxWait, laneTick, laneProgressEvery
	laneQuiet, laneMaxWait, laneTick, laneProgressEvery = 30*time.Millisecond, 2*time.Second, 5*time.Millisecond, 20*time.Millisecond
	defer func() { laneQuiet, laneMaxWait, laneTick, laneProgressEvery = q, mw, tick, every }()
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	person := WithRunLane(context.Background(), LaneInteractive)
	done, _ := reloadableLLM{}.beginModelUse(person)
	// A helper call beside the run goes at once while the person's call is
	// out: the hold is the loop's, not every call's.
	helperDone, err := reloadableLLM{}.beginModelUse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	helperDone()

	go func() { time.Sleep(60 * time.Millisecond); done() }()
	stub := &FakeLLM{Turns: []FakeTurn{{Content: "done", Repeat: true}}}
	app := &AppCore{LLM: stub}
	var progress, finished int
	var total time.Duration
	start := time.Now()
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hi"}}, AgentLoopConfig{
		MaxRounds: 2,
		OnWait: func(waited time.Duration, isDone bool) {
			if isDone {
				finished++
				total = waited
			} else {
				progress++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.LaneWait < 60*time.Millisecond {
		t.Fatalf("the reply should carry the wait, got %+v", resp)
	}
	if finished != 1 || total != resp.LaneWait || progress == 0 {
		t.Fatalf("OnWait: progress=%d finished=%d total=%s (reply %s)", progress, finished, total, resp.LaneWait)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the loop waited far longer than the person's call")
	}
	// A person's own run is never held.
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()
	hold, _ := reloadableLLM{}.beginModelUse(person)
	defer hold()
	resp, _, err = app.RunAgentLoop(person, []Message{{Role: "user", Content: "hi"}}, AgentLoopConfig{MaxRounds: 2})
	if err != nil || resp.LaneWait != 0 {
		t.Fatalf("a person's run was held %s (%v)", resp.LaneWait, err)
	}
}

// Whether background waits is the deployment's call. Auto waits on a model
// that shares local hardware (parallel slots on one GPU share it) and not on a
// hosted API; Always and Never override; Lowest waits whatever is set.
func TestTheYieldSettingDecidesWhetherBackgroundWaits(t *testing.T) {
	q, lq, mw, lmw, tick := laneQuiet, laneLowestQuiet, laneMaxWait, laneLowestMaxWait, laneTick
	laneQuiet, laneLowestQuiet, laneMaxWait, laneLowestMaxWait, laneTick = 20*time.Millisecond, 20*time.Millisecond, 60*time.Millisecond, 60*time.Millisecond, 5*time.Millisecond
	defer func() { laneQuiet, laneLowestQuiet, laneMaxWait, laneLowestMaxWait, laneTick = q, lq, mw, lmw, tick }()
	defer SetBackgroundYield(BackgroundYieldAuto, "llama.cpp", "")
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	person := WithRunLane(context.Background(), LaneInteractive)
	done, _ := reloadableLLM{}.beginModelUse(person)
	defer done()
	waits := func(mode, provider string, lane RunLane) bool {
		SetBackgroundYield(mode, provider, "")
		held, _ := holdForLane(WithRunLane(context.Background(), lane), WORKER)
		return held > 0
	}
	for _, c := range []struct {
		mode, provider string
		lane           RunLane
		want           bool
	}{
		{BackgroundYieldAuto, "vllm", LaneBackground, true},
		{BackgroundYieldAuto, "peer:den", LaneBackground, true},
		{BackgroundYieldAuto, "anthropic", LaneBackground, false},
		{BackgroundYieldAlways, "openai", LaneBackground, true},
		{BackgroundYieldNever, "vllm", LaneBackground, false},
		{BackgroundYieldNever, "vllm", LaneLowest, true},
	} {
		if got := waits(c.mode, c.provider, c.lane); got != c.want {
			t.Errorf("mode %q on %s, %s lane: waited=%v, want %v", c.mode, c.provider, c.lane, got, c.want)
		}
	}

	// Auto decides per model: a worker on the local GPU waits for people, a
	// lead on a hosted API beside it does not, and a lead with no model of its
	// own is the worker's.
	modelUseMu.Lock()
	modelUses[LEAD] = &modelUse{inter: 1}
	modelUseMu.Unlock()
	bg := WithRunLane(context.Background(), LaneBackground)
	for _, c := range []struct {
		worker, lead string
		tier         LLMTier
		want         bool
	}{
		{"vllm", "anthropic", WORKER, true},
		{"vllm", "anthropic", LEAD, false},
		{"openai", "llama.cpp", LEAD, true},
		{"openai", "llama.cpp", WORKER, false},
		{"vllm", "", LEAD, true},
		{"anthropic", "", LEAD, false},
	} {
		SetBackgroundYield(BackgroundYieldAuto, c.worker, c.lead)
		if held, _ := holdForLane(bg, c.tier); (held > 0) != c.want {
			t.Errorf("worker %s, lead %q: a background %s call waited=%v, want %v", c.worker, c.lead, c.tier, held > 0, c.want)
		}
	}
}

// A lead that recovers says so: each call that works adds to a streak a
// failure resets, with when one last worked. Acknowledging the failures
// keeps them counted and records when.
func TestALeadThatRecoversSaysSo(t *testing.T) {
	prevNow := leadStatsNow
	leadStatsMu.Lock()
	prev := [4]any{leadHours, leadOKStreak, leadLastSuccess, leadAckAt}
	leadHours, leadOKStreak, leadLastSuccess, leadAckAt = [24]leadHour{}, 0, time.Time{}, time.Time{}
	leadStatsMu.Unlock()
	t.Cleanup(func() {
		leadStatsNow = prevNow
		leadStatsMu.Lock()
		leadHours, leadOKStreak = prev[0].([24]leadHour), prev[1].(int)
		leadLastSuccess, leadAckAt = prev[2].(time.Time), prev[3].(time.Time)
		leadStatsMu.Unlock()
	})
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	leadStatsNow = func() time.Time { return at }
	for i := 0; i < 4; i++ {
		noteLeadCall(errors.New("messages.0: use the top-level 'system' parameter"))
	}
	at = at.Add(time.Minute)
	noteLeadCall(nil)
	noteLeadCall(nil)
	got := LeadCalls()
	if got.OKStreak != 2 || !got.LastSuccess.Equal(at) || got.Failed != 4 {
		t.Fatalf("after recovering: %+v", got)
	}
	AcknowledgeLeadFailures()
	if got := LeadCalls(); !got.Acknowledged.Equal(at) || got.Failed != 4 || got.LastFailure.After(got.Acknowledged) {
		t.Errorf("acknowledged: %+v", got)
	}
	at = at.Add(time.Minute)
	noteLeadCall(errors.New("timeout"))
	if got := LeadCalls(); got.OKStreak != 0 || !got.LastFailure.After(got.Acknowledged) {
		t.Errorf("a failure after the acknowledgement: %+v", got)
	}
}
