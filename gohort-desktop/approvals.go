// Tool-invocation approval store. The WS bridge defers every tool
// call through this gate: when ws_client.go's handleInvoke fires,
// it asks for approval and blocks until the webview answers (or a
// deadline expires). Approvals come from the user clicking a modal
// in the page; auto-approve mode (persisted in settings) bypasses
// the prompt and accepts every call silently.
//
// Concurrency: pending requests live in an in-memory map keyed by
// the server-issued invocation ID. Each Request returns a channel
// the caller reads to learn the decision. Resolve writes once and
// closes; double-resolves are no-ops.
//
// This is the single chokepoint between "server says do X" and
// "tool actually runs," which is exactly where you want the consent
// gate. See ws_client.go's handleInvoke for the call-site.

package main

import (
	"sync"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

const (
	// approvalDeadline — how long Request waits before timing out
	// with a deny. Matches the server's desktopInvokeDeadline (60s)
	// so the server doesn't kill the call before the user has a
	// chance to react. Long enough for the user to glance at the
	// modal, short enough that a forgotten tab doesn't tie up the
	// agent loop forever.
	approvalDeadline = 60 * time.Second
	// auto-approve setting key
	settingAutoApprove = "bridge_auto_approve"
)

// ApprovalRequest is what the webview receives when a tool call
// needs consent. Shaped for direct JSON marshaling to the modal.
type ApprovalRequest struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// approvalStore tracks pending approval requests. Methods are
// goroutine-safe; modal handling runs on the Wails event loop,
// invocations on the WS read pump's per-call goroutine.
type approvalStore struct {
	mu       sync.Mutex
	pending  map[string]chan bool
	settings *core.Settings
}

func newApprovalStore(settings *core.Settings) *approvalStore {
	return &approvalStore{
		pending:  map[string]chan bool{},
		settings: settings,
	}
}

// AutoApprove reads the persisted "trust everything" toggle. When
// true, Request short-circuits to allow without notifying the
// webview — the user opted out of the consent prompt entirely.
func (s *approvalStore) AutoApprove() bool {
	if s.settings == nil {
		return false
	}
	var v bool
	s.settings.GetBool(settingAutoApprove, &v)
	return v
}

// SetAutoApprove persists the toggle. Called from the Wails bridge
// (App.SetAutoApprove) so the in-page settings UI can flip it.
func (s *approvalStore) SetAutoApprove(on bool) error {
	if s.settings == nil {
		return nil
	}
	return s.settings.SetBool(settingAutoApprove, on)
}

// Request registers a pending approval and waits for the verdict.
// Returns true (approve) when the user clicks Allow OR auto-approve
// is on; false on Deny or timeout. The ID is the server's invocation
// ID; the webview uses it to identify which request its modal is
// confirming.
//
// Caller should pass a function that delivers the request to the
// webview (typically wails.runtime.EventsEmit). Wrapping it here
// keeps approvalStore independent of the Wails runtime imports —
// easier to test, cleaner separation.
func (s *approvalStore) Request(req ApprovalRequest, deliver func(ApprovalRequest)) bool {
	if s.AutoApprove() {
		return true
	}
	s.mu.Lock()
	if _, exists := s.pending[req.ID]; exists {
		// Server-issued IDs are monotonic per connection so
		// collisions shouldn't happen, but if they do, drop the
		// old waiter so the new one doesn't double-register.
		s.mu.Unlock()
		return false
	}
	ch := make(chan bool, 1)
	s.pending[req.ID] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, req.ID)
		s.mu.Unlock()
	}()

	deliver(req)

	select {
	case allow := <-ch:
		return allow
	case <-time.After(approvalDeadline):
		core.Warn("[approval] timed out waiting for user on %s (%s) — denying", req.Name, req.ID)
		return false
	}
}

// Resolve delivers the user's verdict for a pending request. Called
// from App.ApproveInvoke when the modal's button is clicked. No-op
// on unknown / already-resolved IDs.
func (s *approvalStore) Resolve(id string, allow bool) {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- allow:
	default:
	}
}
