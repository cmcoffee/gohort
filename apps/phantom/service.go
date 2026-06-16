// Multi-service foundation. Phantom's message engine (history, tools,
// memory, knowledge, scheduling, dispatch, gatekeeper) is channel-
// agnostic; the per-service deltas live here so adding a new source
// ("hook Telegram / Slack / a webhook in") is "register a ServicePolicy
// and run a bridge that hooks/polls under its service id", not a fork of
// the message loop.
//
// The Service dimension is threaded through APIKey (the bridge's key
// declares which service it speaks), Conversation, and OutboxItem. The
// outbound poll is scoped by the polling key's service so two bridges
// never drain each other's replies. Everything defaults to "imessage"
// when unset, so existing single-channel deployments are unchanged.

package phantom

import (
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// phantomDefaultService is the service assumed when none is recorded —
// the historical single-channel case (iMessage). Keys, conversations,
// and outbox items that predate the Service field normalize to this, so
// nothing changes for them.
const phantomDefaultService = "imessage"

// normService collapses an empty service to the default so every
// comparison + policy lookup has a concrete value.
func normService(s string) string {
	if s == "" {
		return phantomDefaultService
	}
	return s
}

// ServicePolicy declares how phantom adapts to a particular messaging
// service. Keep this to genuine per-channel deltas; anything the engine
// can do uniformly stays in the engine.
type ServicePolicy struct {
	// CleanInbound normalizes raw inbound text at STORE time for this
	// service (e.g. iMessage strips a leading bridge artifact). Keep this
	// to store-time fixes; display-time cleaning (cleanMessageText) is a
	// separate concern applied when rendering history. nil = store as-is.
	CleanInbound func(string) string
	// AttachmentDelay splits a video+text reply into two outbox items a
	// few seconds apart so the recipient sees the upload land before the
	// text (iMessage latency). Channels that deliver atomically leave
	// this false.
	AttachmentDelay bool
}

var (
	servicePolicies   = map[string]ServicePolicy{}
	servicePoliciesMu sync.RWMutex
)

// RegisterServicePolicy installs (or replaces) the policy for a service
// id. Called from init() by each service adapter.
func RegisterServicePolicy(id string, p ServicePolicy) {
	servicePoliciesMu.Lock()
	servicePolicies[normService(id)] = p
	servicePoliciesMu.Unlock()
}

// servicePolicyFor returns the registered policy for a service, or a
// safe generic default (no cleaning, atomic delivery) for an unknown one.
func servicePolicyFor(id string) ServicePolicy {
	servicePoliciesMu.RLock()
	p, ok := servicePolicies[normService(id)]
	servicePoliciesMu.RUnlock()
	if ok {
		return p
	}
	return ServicePolicy{}
}

// conversationService resolves the service a chat belongs to (default
// imessage). Used at the outbound chokepoint so each reply is tagged for
// the right channel without threading service through every enqueue call
// site.
func conversationService(db Database, chatID string) string {
	if db != nil && chatID != "" {
		var c Conversation
		if db.Get(conversationTable, chatID, &c) {
			return normService(c.Service)
		}
	}
	return phantomDefaultService
}

func init() {
	// iMessage = the historical behavior: strip the leading bridge
	// artifact at store time, stagger video+text outbound.
	// stripLeadingArtifact lives in phantom.go (same package).
	RegisterServicePolicy(phantomDefaultService, ServicePolicy{
		CleanInbound:    stripLeadingArtifact,
		AttachmentDelay: true,
	})
}
