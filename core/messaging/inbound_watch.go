// Watching what people send, apart from whether a channel answers it.
//
// A bound channel wakes its agent on a message, and that is the only thing
// most inbound traffic needs. But a conversation elsewhere can be waiting on
// the same person: an agent asked them something on the owner's behalf and
// said it would pass the answer on. That conversation needs to hear the reply
// whether the person's thread wakes an agent, is only recorded, or is bound to
// no channel at all. So the transport hands every live inbound to the watchers
// registered here, before it decides anything about channels.

package messaging

import "sync"

// InboundEvent is one live message a transport received from someone other
// than the owner, after its replay, duplicate and switched-off checks.
type InboundEvent struct {
	Owner   string // whose bridge it arrived on
	Service string // the transport ("imessage", "teams", ...)
	ChatID  string // the conversation it arrived in
	Handle  string // who sent it
	Sender  string // their display name, the handle when none is known
	Text    string
	// Addresses is every id the conversation answers to (its chat ids,
	// the sender's handle, their aliases), in the forms the transport
	// matches channels by, so a watcher can match whichever it recorded.
	Addresses []string
}

var (
	inboundWatchersMu sync.RWMutex
	inboundWatchers   []func(InboundEvent)
)

// RegisterInboundWatcher adds fn to the watchers every live inbound reaches.
// fn runs on the transport's goroutine and must return quickly: hand anything
// slow to a goroutine of its own.
func RegisterInboundWatcher(fn func(InboundEvent)) {
	if fn == nil {
		return
	}
	inboundWatchersMu.Lock()
	inboundWatchers = append(inboundWatchers, fn)
	inboundWatchersMu.Unlock()
}

// WatchInbound hands ev to every registered watcher. Called by a transport.
func WatchInbound(ev InboundEvent) {
	inboundWatchersMu.RLock()
	fns := append([](func(InboundEvent))(nil), inboundWatchers...)
	inboundWatchersMu.RUnlock()
	for _, fn := range fns {
		fn(ev)
	}
}
