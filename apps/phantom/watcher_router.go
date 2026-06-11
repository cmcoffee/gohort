// phantomRoutingTarget composes the RoutingTarget identifier set on a phantom
// ToolSession. RoutingTarget is the generic "where do follow-ups for this
// session belong?" key (core/common.go) — today it scopes the managed
// workspace (core/workspace_managed.go) for tools that write files on behalf of
// a phantom conversation.
//
// The bridge routes delivery by ChatID, so deliverChatID is the only required
// piece; the handle is metadata (an empty handle is valid — it means the
// conversation is the phone owner's own thread). We bake in whichever handle we
// have (per-message preferred, then conv.Handle, else empty).

package phantom

// phantomRoutingTarget builds "phantom:<chat_id>|<handle>", or "" when there's
// no chat id (the caller then leaves RoutingTarget empty).
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
