package core

import (
	"context"
	"errors"
	"sync/atomic"
)

// OllamaScheduler provides a fair-queued concurrency limiter for
// Ollama requests:
//
//  1. Global parallelism cap: at most MaxParallel requests are in
//     flight at any moment.
//  2. Round-robin dispatch across callers with pending work. When a
//     slot frees, the next dispatch goes to a different caller than
//     the one most recently dispatched (when one is available),
//     preventing a single caller from monopolizing all slots while
//     others are queued.
//
// A single caller (session) CAN occupy multiple slots simultaneously
// when no other callers have pending work — fair-share kicks in only
// under contention. When two sessions both have pending requests,
// the scheduler alternates between them on every slot-free event.
//
// The scheduler is owned by a single dispatcher goroutine; all state
// is mutated there. Callers submit tokens via channels and wait on
// per-token done channels. No shared locks.
type OllamaScheduler struct {
	submit  chan *ollamaReqToken
	release chan string
	setN    chan int
	statReq chan chan OllamaSchedStats
}

// ollamaReqToken is a single in-flight acquire request.
type ollamaReqToken struct {
	callerID string
	ctx      context.Context
	done     chan error // written exactly once: nil on grant, err on cancel/reject
}

// OllamaSchedStats snapshots current scheduler state for admin views.
type OllamaSchedStats struct {
	MaxParallel int
	InFlight    int
	Callers     map[string]int // in-flight per caller
	Queued      map[string]int // queued count per caller
}

// ErrOllamaSchedulerDisabled is returned by Acquire when the scheduler
// hasn't been started. Callers should treat this as "no limiter in
// effect, proceed immediately."
var ErrOllamaSchedulerDisabled = errors.New("ollama scheduler disabled")

// Package-level singleton. nil until StartOllamaScheduler is called;
// Acquire falls through to no-op when nil so unconfigured deployments
// see no behavior change.
var (
	ollamaSchedOnce atomic.Bool
	ollamaSched     *OllamaScheduler
)

// StartOllamaScheduler initializes the global Ollama scheduler with
// the given concurrency cap. Idempotent: safe to call multiple times;
// subsequent calls adjust MaxParallel on the running dispatcher
// without restarting. A value of 0 or negative disables the
// scheduler entirely (Acquire passes through).
func StartOllamaScheduler(maxParallel int) {
	if maxParallel < 1 {
		// Disabling: stop dispatching by setting maxParallel to a
		// value larger than any realistic workload. Current approach:
		// keep the scheduler running but let every request through
		// immediately by setting a huge cap.
		if ollamaSched != nil {
			ollamaSched.setN <- 1 << 30
		}
		return
	}
	if ollamaSchedOnce.CompareAndSwap(false, true) {
		s := &OllamaScheduler{
			submit:  make(chan *ollamaReqToken, 64),
			release: make(chan string, 64),
			setN:    make(chan int, 4),
			statReq: make(chan chan OllamaSchedStats, 4),
		}
		ollamaSched = s
		go s.dispatch(maxParallel)
	} else {
		ollamaSched.setN <- maxParallel
	}
}

// AcquireOllamaSlot blocks until a slot is available for the caller,
// subject to global and per-caller limits. Returns immediately when
// the scheduler is disabled. Caller MUST call ReleaseOllamaSlot with
// the same callerID after the work completes (whether success or
// error). If the context is canceled while queued, returns the
// context error without reserving a slot.
func AcquireOllamaSlot(ctx context.Context, callerID string) error {
	s := ollamaSched
	if s == nil {
		return nil // scheduler not started; pass through
	}
	if callerID == "" {
		callerID = "unknown"
	}
	tok := &ollamaReqToken{
		callerID: callerID,
		ctx:      ctx,
		done:     make(chan error, 1),
	}
	select {
	case s.submit <- tok:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-tok.done:
		return err
	case <-ctx.Done():
		// Mark as canceled; dispatcher will skip this token on next pass.
		// We don't signal the dispatcher here because it re-checks ctx
		// when picking a token to grant.
		return ctx.Err()
	}
}

// ReleaseOllamaSlot returns the slot previously acquired by the
// caller. Safe to call when the scheduler is disabled (no-op).
func ReleaseOllamaSlot(callerID string) {
	s := ollamaSched
	if s == nil {
		return
	}
	if callerID == "" {
		callerID = "unknown"
	}
	// Non-blocking; release channel is buffered.
	select {
	case s.release <- callerID:
	default:
		// Buffer full — fall back to blocking send. This should be
		// rare; indicates the dispatcher is struggling to keep up.
		s.release <- callerID
	}
}

// OllamaSchedulerStats returns a snapshot of current state. Useful
// for admin endpoints and debug logging.
func OllamaSchedulerStats() OllamaSchedStats {
	s := ollamaSched
	if s == nil {
		return OllamaSchedStats{}
	}
	reply := make(chan OllamaSchedStats, 1)
	s.statReq <- reply
	return <-reply
}

// llamacppSched serializes requests to llama.cpp. llama.cpp is
// single-threaded and returns 503 under concurrent load. A maxParallel
// of 1 turns this into a pure mutex; >1 allows controlled bursting when
// the server supports it.
var (
	llamacppSchedOnce atomic.Bool
	llamacppSched     *OllamaScheduler
)

// StartLlamacppScheduler initializes the global llama.cpp request
// serializer. Idempotent: subsequent calls adjust MaxParallel without
// restarting the dispatcher. maxParallel should be 1 for stock
// llama.cpp (single-threaded); set higher only when the server
// explicitly supports concurrent requests.
func StartLlamacppScheduler(maxParallel int) {
	if maxParallel < 1 {
		if llamacppSched != nil {
			llamacppSched.setN <- 1 << 30
		}
		return
	}
	if llamacppSchedOnce.CompareAndSwap(false, true) {
		s := &OllamaScheduler{
			submit:  make(chan *ollamaReqToken, 64),
			release: make(chan string, 64),
			setN:    make(chan int, 4),
			statReq: make(chan chan OllamaSchedStats, 4),
		}
		llamacppSched = s
		go s.dispatch(maxParallel)
	} else {
		llamacppSched.setN <- maxParallel
	}
}

// AcquireLlamacppSlot blocks until a llama.cpp slot is available.
// Caller MUST call ReleaseLlamacppSlot after the HTTP call completes.
// Returns immediately when the scheduler has not been started.
func AcquireLlamacppSlot(ctx context.Context, callerID string) error {
	s := llamacppSched
	if s == nil {
		return nil
	}
	if callerID == "" {
		callerID = "unknown"
	}
	tok := &ollamaReqToken{
		callerID: callerID,
		ctx:      ctx,
		done:     make(chan error, 1),
	}
	select {
	case s.submit <- tok:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-tok.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseLlamacppSlot returns the slot previously acquired. Safe to
// call when the scheduler is disabled (no-op).
func ReleaseLlamacppSlot(callerID string) {
	s := llamacppSched
	if s == nil {
		return
	}
	if callerID == "" {
		callerID = "unknown"
	}
	select {
	case s.release <- callerID:
	default:
		s.release <- callerID
	}
}

// dispatch is the scheduler's main loop. Owns all mutable state; no
// other goroutine reads or writes callers/queues/order/inFlight.
//
// Dispatch policy: when picking the next caller to serve, prefer one
// that is NOT the most recently dispatched (when more than one has
// pending work). This produces strict alternation under contention
// while letting a sole caller take all slots when alone.
func (s *OllamaScheduler) dispatch(initialMax int) {
	maxParallel := initialMax
	inFlight := 0
	callers := map[string]int{}             // caller → in-flight count (no cap; informational)
	queues := map[string][]*ollamaReqToken{} // caller → FIFO of pending tokens
	order := []string{}                     // round-robin order of callers with pending work
	lastDispatched := ""                    // most recent caller, for round-robin preference

	addToOrder := func(cid string) {
		for _, c := range order {
			if c == cid {
				return
			}
		}
		order = append(order, cid)
	}

	pickNext := func() int {
		// Prefer a caller different from the one we just served, if
		// any other caller has pending work. Falls back to the head
		// of order when only one caller is active.
		var fallback = -1
		for i, cid := range order {
			if len(queues[cid]) == 0 {
				continue
			}
			if cid != lastDispatched {
				return i
			}
			if fallback == -1 {
				fallback = i
			}
		}
		return fallback
	}

	tryDispatch := func() {
		for inFlight < maxParallel && len(order) > 0 {
			picked := pickNext()
			if picked == -1 {
				return
			}
			cid := order[picked]
			tok := queues[cid][0]
			queues[cid] = queues[cid][1:]

			// Drop canceled tokens without consuming a slot.
			if err := tok.ctx.Err(); err != nil {
				tok.done <- err
				if len(queues[cid]) == 0 {
					order = append(order[:picked], order[picked+1:]...)
				}
				continue
			}

			// Grant slot.
			inFlight++
			callers[cid]++
			lastDispatched = cid
			tok.done <- nil

			// Rotate caller to end of order; remove if no more pending.
			order = append(order[:picked], order[picked+1:]...)
			if len(queues[cid]) > 0 {
				order = append(order, cid)
			}
		}
	}

	for {
		select {
		case tok := <-s.submit:
			queues[tok.callerID] = append(queues[tok.callerID], tok)
			addToOrder(tok.callerID)
			tryDispatch()
		case cid := <-s.release:
			inFlight--
			callers[cid]--
			if callers[cid] < 0 {
				callers[cid] = 0
			}
			if inFlight < 0 {
				inFlight = 0 // defensive: stray release shouldn't go negative
			}
			tryDispatch()
		case n := <-s.setN:
			maxParallel = n
			tryDispatch()
		case reply := <-s.statReq:
			snap := OllamaSchedStats{
				MaxParallel: maxParallel,
				InFlight:    inFlight,
				Callers:     map[string]int{},
				Queued:      map[string]int{},
			}
			for k, v := range callers {
				if v > 0 {
					snap.Callers[k] = v
				}
			}
			for k, q := range queues {
				if len(q) > 0 {
					snap.Queued[k] = len(q)
				}
			}
			reply <- snap
		}
	}
}
