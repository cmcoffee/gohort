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
	// auto-approve setting key — the master "trust everything" toggle.
	settingAutoApprove = "bridge_auto_approve"
	// per-tool allow-list key — tools the user chose "Always allow" on.
	// A []string of tool names; Request short-circuits to allow for any
	// name in this list (the Claude-Desktop "remember this tool" model),
	// independent of the master auto-approve toggle.
	settingAllowedTools = "bridge_allowed_tools"
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
	pending  map[string]pendingApproval
	settings *core.Settings
}

// pendingApproval is one in-flight request: the channel the waiting
// Request goroutine reads, plus the tool name so Resolve can persist
// an "Always allow <name>" decision without the caller re-passing it.
type pendingApproval struct {
	ch   chan bool
	name string
}

func newApprovalStore(settings *core.Settings) *approvalStore {
	return &approvalStore{
		pending:  map[string]pendingApproval{},
		settings: settings,
	}
}

// AutoApprove reads the persisted "trust everything" toggle. When
// true, Request short-circuits to allow without notifying the
// webview — the user opted out of the consent prompt entirely.
func (s *approvalStore) AutoApprove() bool {
	// Sidecar-backed so the DAEMON's approver (separate process) reads the
	// same toggle — the viewer's settings store was invisible to it.
	return core.ReadBridgeConfig().AutoApprove
}

// SetAutoApprove persists the toggle. Called from the Wails bridge
// (App.SetAutoApprove) so the in-page settings UI can flip it.
func (s *approvalStore) SetAutoApprove(on bool) error {
	bc := core.ReadBridgeConfig()
	bc.AutoApprove = on
	return core.WriteBridgeConfig(bc)
}

// AllowedTools returns the per-tool always-allow list. These are the
// tools the user clicked "Always allow" on; Request approves them
// silently regardless of the master auto-approve toggle.
func (s *approvalStore) AllowedTools() []string {
	return core.ReadBridgeConfig().AllowedTools
}

// IsToolAllowed reports whether name is on the per-tool allow-list.
func (s *approvalStore) IsToolAllowed(name string) bool {
	if name == "" {
		return false
	}
	for _, t := range s.AllowedTools() {
		if t == name {
			return true
		}
	}
	return false
}

// AllowTool adds name to the per-tool allow-list (no-op if already
// present). This is the persistence half of the modal's "Always
// allow <tool>" button.
func (s *approvalStore) AllowTool(name string) error {
	if name == "" {
		return nil
	}
	bc := core.ReadBridgeConfig()
	for _, t := range bc.AllowedTools {
		if t == name {
			return nil
		}
	}
	bc.AllowedTools = append(bc.AllowedTools, name)
	return core.WriteBridgeConfig(bc)
}

// RemoveAllowedTool drops name from the allow-list — the revoke path
// behind the Preferences manager. No-op if not present.
func (s *approvalStore) RemoveAllowedTool(name string) error {
	bc := core.ReadBridgeConfig()
	next := make([]string, 0, len(bc.AllowedTools))
	for _, t := range bc.AllowedTools {
		if t != name {
			next = append(next, t)
		}
	}
	bc.AllowedTools = next
	return core.WriteBridgeConfig(bc)
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
	// Two silent-approve paths: the master "trust everything" toggle,
	// and the per-tool allow-list (the user already chose "Always
	// allow" for this specific tool). Either one skips the prompt.
	if s.AutoApprove() || s.IsToolAllowed(req.Name) {
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
	s.pending[req.ID] = pendingApproval{ch: ch, name: req.Name}
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
// on unknown / already-resolved IDs. When allow && always, the tool
// is added to the per-tool allow-list so future calls skip the prompt
// (the "Always allow <tool>" button).
func (s *approvalStore) Resolve(id string, allow, always bool) {
	s.mu.Lock()
	p, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if allow && always && p.name != "" {
		if err := s.AllowTool(p.name); err != nil {
			core.Warn("[approval] persisting always-allow for %s failed: %v", p.name, err)
		} else {
			core.Log("[approval] always-allow added for %s", p.name)
		}
	}
	select {
	case p.ch <- allow:
	default:
	}
}
