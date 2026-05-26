// NetworkConnector — single-source-of-truth network capability gate.
//
// The orchestrator builds a connector at turn entry, parameterized by
// privacy mode. The connector flows down via context through every
// descendant: sub-agent dispatches, agent dispatches, sandboxed
// shell execution, built-in network tools, API-mode temp tools. Any
// layer that wants to make a network call checks the connector. If
// it's blocked, the call refuses — regardless of what that layer's
// "capability" said.
//
// This is the single guarantee: per-layer tool filtering and
// CapNetwork tagging become UX hints; the connector is the floor.
//
// Default-allowed semantics: when no connector is in the context,
// callers treat network as allowed (back-compat for paths that
// don't yet propagate one through). Tightening to default-blocked
// would silently break tools whose callsites haven't been migrated.

package core

import (
	"context"
	"sync"
)

// NetworkConnector represents the network-access state for a turn
// (and all descendants spawned from it). Use NewNetworkConnector
// to construct, WithNetworkConnector to attach to context, and
// NetworkAllowedFromContext to read from any descendant point.
//
// Mutable by design: SetAllowed flips the state live so the chat
// surface can implement a mid-turn privacy cutoff. Network refusal
// sites read Allowed() each time they fire, so a flip propagates
// to every in-flight + subsequent call within the turn (sub-agent
// dispatches, sandbox /unshare-net is decided at
// invocation time per call, web_search/fetch_url check per call).
type NetworkConnector struct {
	mu      sync.RWMutex
	allowed bool
}

// NewNetworkConnector builds a connector. blockNetwork=true (privacy
// mode is on) → connector refuses every network call; false → network
// is allowed for descendants subject to their own per-tool gates.
func NewNetworkConnector(blockNetwork bool) *NetworkConnector {
	return &NetworkConnector{allowed: !blockNetwork}
}

// Allowed reports whether the connector permits network access.
// Nil-receiver returns true (no connector = back-compat allowed).
func (c *NetworkConnector) Allowed() bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.allowed
}

// SetAllowed flips the connector's state. Used by the chat surface
// to implement a mid-turn cutoff: when the user toggles Private ON
// during a running turn, the active turn's connector is updated and
// every subsequent Allowed() check (including in-flight tools that
// re-check on each call) sees the new state immediately.
func (c *NetworkConnector) SetAllowed(v bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.allowed = v
	c.mu.Unlock()
}

type networkConnectorCtxKey struct{}

// WithNetworkConnector returns a derived context carrying the given
// connector. Descendants read it via NetworkAllowedFromContext.
func WithNetworkConnector(ctx context.Context, conn *NetworkConnector) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, networkConnectorCtxKey{}, conn)
}

// NetworkConnectorFromContext extracts the connector if one was
// attached. Returns nil when none present (callers should default
// to allowed behavior).
func NetworkConnectorFromContext(ctx context.Context) *NetworkConnector {
	if ctx == nil {
		return nil
	}
	if c, ok := ctx.Value(networkConnectorCtxKey{}).(*NetworkConnector); ok {
		return c
	}
	return nil
}

// NetworkAllowedFromContext is the shorthand callers use to decide
// whether to issue a network call. Treats missing connector as
// allowed — the connector is the floor, not the default; layers
// that don't propagate one stay back-compat permissive.
func NetworkAllowedFromContext(ctx context.Context) bool {
	return NetworkConnectorFromContext(ctx).Allowed()
}
