package core

// A lead that built fine and later stopped answering used to be recorded
// nowhere. Every escalation fell back to the worker per call, no flag was
// set, the background retry never ran, and the only symptom was answers
// being worse: the same symptom as no lead being configured at all. One SSO
// session lapsing cost eleven hours that way.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

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
