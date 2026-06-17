package core

import "net/http"

// Messaging transport forwarding — a TRANSITION seam. The Bridges app registers
// its hook + poll handlers here; legacy messaging surfaces (phantom) forward
// their own /api/hook + /api/poll to them, so the existing connector daemon can
// keep hitting the old endpoints while Bridges does the actual routing. Remove
// the forward once the daemon is repointed at /bridges/api/* directly.
var (
	msgHook http.HandlerFunc
	msgPoll http.HandlerFunc
)

// RegisterMessagingTransport installs the active transport's hook/poll handlers.
func RegisterMessagingTransport(hook, poll http.HandlerFunc) {
	msgHook, msgPoll = hook, poll
}

// MessagingHook / MessagingPoll return the registered handlers, or nil when no
// transport app is loaded (legacy surface then handles inbound itself).
func MessagingHook() http.HandlerFunc { return msgHook }
func MessagingPoll() http.HandlerFunc { return msgPoll }
