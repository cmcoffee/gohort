// Phantom's WatcherResultRouter — receives a worker reply produced
// by a watcher that was minted inside a phantom conversation, and
// delivers it as an iMessage by enqueueing an outbox item against the
// originating chat. The mapping is target="phantom:<chatID>", so the
// router strips the prefix, looks up the conversation to get the
// handle (phone/email), and queues a "reply"-typed outbox item just
// like processMessage would for a normal LLM turn.
//
// Registered once at package load via init(). Generic plumbing on the
// core side: see core/watcher.go's RegisterWatcherResultRouter and
// dispatchWatcherResult.

package phantom

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterWatcherResultRouter("phantom", phantomWatcherRouter)
}

// phantomRoutingTarget composes the watcher routing target string.
// The bridge routes by ChatID for delivery, so deliverChatID is the
// only required piece. Handle is metadata (empty handle is a valid
// case in phantom's convention — it means the conversation is the
// phone owner's own thread). We bake in whichever handle we have
// (per-message preferred, then conv.Handle, else empty) so the
// outbox item carries the same info as the proven reply path.
//
// Empty deliverChatID degrades to "" → caller maps to "log" target.
func phantomRoutingTarget(deliverChatID, msgHandle, convHandle string) string {
	if deliverChatID == "" {
		return ""
	}
	recipient := msgHandle
	if recipient == "" {
		recipient = convHandle
	}
	return "phantom:" + deliverChatID + "|" + recipient
}

// phantomWatcherRouter delivers a watcher's worker reply to the
// originating iMessage thread. Target format is
// "phantom:<chat_id>|<handle>" — both pieces are baked in at session
// creation time so this path needs no conversationTable lookup, which
// avoids alias-keying issues and makes the watcher resilient to conv
// renames or migrations after creation. Errors during delivery are
// logged and swallowed — a routing failure should not unschedule the
// watcher (the worker reply is already saved to results, so no data
// loss; operator can replay via watcher(action=results)).
func phantomWatcherRouter(target string, w Watcher, reply string, runErr error) {
	rest := strings.TrimPrefix(target, "phantom:")
	chatID, handle, ok := strings.Cut(rest, "|")
	if !ok || chatID == "" {
		Log("[phantom/watcher] target %q is missing chat_id — dropping reply for %s", target, w.Name)
		return
	}
	// Empty handle is a valid case: per phantom's convention an empty
	// handle means the message originated from the phone owner (talking
	// to their own phantom). The bridge routes outbox items by ChatID
	// for delivery; handle is metadata.
	if RootDB == nil {
		Log("[phantom/watcher] RootDB not initialized — dropping reply for %s", w.Name)
		return
	}
	db := RootDB.Bucket("phantom")

	// Compose the message body. If the worker errored with no useful
	// reply, surface a short diagnostic; otherwise send what the
	// worker produced. Empty replies are skipped (worker decided
	// there was nothing meaningful to report).
	text := strings.TrimSpace(reply)
	if runErr != nil && text == "" {
		text = "[watcher " + w.Name + "] error: " + runErr.Error()
	}
	if text == "" {
		return
	}
	// Prepend the delivery prefix. DeliveryPrefixSet=false means use
	// the routing app's default; true means use w.DeliveryPrefix
	// verbatim (including empty string for "no prefix").
	prefix := "[watcher: " + w.Name + "]\n"
	if w.DeliveryPrefixSet {
		prefix = w.DeliveryPrefix
		if prefix != "" && !strings.HasSuffix(prefix, "\n") && !strings.HasSuffix(prefix, " ") {
			prefix += " "
		}
	}
	text = prefix + text

	enqueueOutbox(db, OutboxItem{
		ID:      newID(),
		ChatID:  chatID,
		Handle:  handle,
		Text:    text,
		Type:    "announce",
		Created: now(),
	})
	Log("[phantom/watcher] queued reply (%d chars) for %s → %s", len(text), w.Name, handle)
}
